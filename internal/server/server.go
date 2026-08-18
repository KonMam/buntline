// Package server exposes the agent over HTTP for the browser UI: a small
// REST surface for sessions/messages/approvals and an SSE stream of agent
// events: the same protocol the harness consumes from the model, now on
// the serving side.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/KonMam/tether/internal/agent"
	"github.com/KonMam/tether/internal/config"
	"github.com/KonMam/tether/internal/module"
	"github.com/KonMam/tether/internal/module/memory"
	"github.com/KonMam/tether/internal/module/tasks"
	"github.com/KonMam/tether/internal/module/worktrees"
	"github.com/KonMam/tether/internal/provider"
	"github.com/KonMam/tether/internal/secrets"
	"github.com/KonMam/tether/internal/session"
	"github.com/KonMam/tether/internal/tools"
)

type Server struct {
	cfg      config.Config
	store    *session.Store
	log      *slog.Logger
	staticFS http.Handler
	modules  *module.Registry

	mu        sync.Mutex
	live      map[string]*liveSession  // session id → running state
	resolving map[string]chan struct{} // session id → attach in progress
	pending   map[string]chan agent.Decision
	questions map[string]chan string // ask_user questions awaiting an answer
}

// liveSession is a session with an in-memory agent attached.
type liveSession struct {
	agent    *agent.Agent
	hub      *hub
	cancel   context.CancelFunc // cancels the in-flight turn, if any
	workdir  string
	registry *tools.Registry
	touched  atomic.Int64 // unix nanos of the last resolve/dispatch

	// mode is the approval policy (see session.Meta.Mode); guarded by
	// modeMu because the approver reads it from the agent goroutine.
	modeMu sync.Mutex
	mode   string

	// In-flight assistant output. Deltas are ephemeral by design, but a
	// browser that reloads mid-stream must not lose the text already
	// streamed; GET /api/sessions/{id} serves this as the seed.
	partialMu       sync.Mutex
	partialText     string
	partialThinking string
	// A pending approval or question outlives a page reload the same way:
	// the card re-opens from this state. Guarded by partialMu.
	pendingApproval *agent.Event
	pendingQuestion *agent.Event

	// Auto-compaction bookkeeping: the model's context window (0 = off)
	// and the last main-loop prompt size, from usage events. The window
	// is atomic because a model switch rewrites it while readers exist.
	contextWindow atomic.Int64
	lastPromptMu  sync.Mutex
	lastPrompt    int

	// The tool call the main loop is executing right now, for the
	// session list's live state. Guarded by the server mutex.
	runningTool string

	// subagents is the record of this session's spawned children; the
	// spawn tool registers each child here and marks it terminal when it
	// ends. Finished entries persist for the life of the liveSession.
	subagents *subagentRegistry
}

func (ls *liveSession) touch()               { ls.touched.Store(time.Now().UnixNano()) }
func (ls *liveSession) touchedAt() time.Time { return time.Unix(0, ls.touched.Load()) }

// runningToolFor returns the session's live state for the session list:
// whether a turn is in flight and which tool call it is executing.
func (s *Server) runningToolFor(id string) (busy bool, runningTool string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ls, ok := s.live[id]
	if !ok {
		return false, ""
	}
	return ls.agent.Busy(), ls.runningTool
}

// teardown releases what a detached session holds outside its own
// memory: today that is background bash processes.
func (ls *liveSession) teardown() {
	if ls.registry != nil {
		ls.registry.Close()
	}
}

func (ls *liveSession) getMode() string {
	ls.modeMu.Lock()
	defer ls.modeMu.Unlock()
	return ls.mode
}

func (ls *liveSession) setMode(m string) {
	ls.modeMu.Lock()
	ls.mode = m
	ls.modeMu.Unlock()
}

func (ls *liveSession) partial() (text, thinking string) {
	ls.partialMu.Lock()
	defer ls.partialMu.Unlock()
	return ls.partialText, ls.partialThinking
}

func New(cfg config.Config, store *session.Store, static http.Handler, modules *module.Registry, log *slog.Logger) *Server {
	s := &Server{
		cfg:       cfg,
		store:     store,
		log:       log,
		staticFS:  static,
		modules:   modules,
		live:      map[string]*liveSession{},
		resolving: map[string]chan struct{}{},
		pending:   map[string]chan agent.Decision{},
		questions: map[string]chan string{},
	}
	go s.janitor()
	return s
}

// janitor detaches live sessions nobody is looking at: not running a
// turn, no SSE subscriber, no running child, untouched for a while. The
// transcript is on disk; the next touch rebuilds the agent. Without
// this, every session ever opened would hold its transcript in memory
// for the process lifetime. A session with background children running
// stays alive: their events must keep flowing into the same stream.
func (s *Server) janitor() {
	const idleAfter = 30 * time.Minute
	for range time.Tick(5 * time.Minute) {
		var evicted []*liveSession
		s.mu.Lock()
		for id, ls := range s.live {
			if ls.agent.Busy() || ls.hub.count() > 0 || ls.childrenRunning() {
				continue
			}
			if time.Since(ls.touchedAt()) > idleAfter {
				evicted = append(evicted, ls)
				delete(s.live, id)
			}
		}
		s.mu.Unlock()
		for _, ls := range evicted {
			ls.teardown()
		}
	}
}

// childrenRunning reports whether any spawned child is still running.
// The janitor keeps such a session alive even when the parent turn
// ended, so background children's events keep flowing into the stream.
func (ls *liveSession) childrenRunning() bool {
	if ls.subagents == nil {
		return false
	}
	for _, e := range ls.subagents.list() {
		status, _, _ := e.snapshot()
		if status == SubagentRunning {
			return true
		}
	}
	return false
}

// Shutdown tears down every live session, killing their background
// tasks, before the process exits.
func (s *Server) Shutdown() {
	s.mu.Lock()
	all := make([]*liveSession, 0, len(s.live))
	for id, ls := range s.live {
		all = append(all, ls)
		delete(s.live, id)
	}
	s.mu.Unlock()
	for _, ls := range all {
		if ls.cancel != nil {
			ls.cancel()
		}
		ls.teardown()
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/config", s.handleConfig)
	mux.HandleFunc("GET /api/sessions", s.handleListSessions)
	mux.HandleFunc("POST /api/sessions", s.handleCreateSession)
	mux.HandleFunc("GET /api/sessions/{id}", s.handleGetSession)
	mux.HandleFunc("DELETE /api/sessions/{id}", s.handleDeleteSession)
	mux.HandleFunc("GET /api/sessions/{id}/events", s.handleEvents)
	mux.HandleFunc("POST /api/sessions/{id}/messages", s.handleSendMessage)
	mux.HandleFunc("POST /api/sessions/{id}/compact", s.handleCompact)
	mux.HandleFunc("POST /api/sessions/{id}/interrupt", s.handleInterrupt)
	mux.HandleFunc("POST /api/sessions/{id}/model", s.handleSetModel)
	mux.HandleFunc("POST /api/sessions/{id}/mode", s.handleSetMode)
	mux.HandleFunc("GET /api/sessions/{id}/export", s.handleExport)
	mux.HandleFunc("POST /api/sessions/{id}/fork", s.handleFork)
	mux.HandleFunc("GET /api/search", s.handleSearch)
	mux.HandleFunc("GET /api/sessions/{id}/agents", s.handleAgents)
	mux.HandleFunc("GET /api/sessions/{id}/subagents", s.handleSubagents)
	mux.HandleFunc("POST /api/sessions/{id}/subagents/{sid}/steer", s.handleSubagentSteer)
	mux.HandleFunc("POST /api/sessions/{id}/subagents/{sid}/interrupt", s.handleSubagentInterrupt)
	mux.HandleFunc("GET /api/system-prompt", s.handleGetSystemPrompt)
	mux.HandleFunc("PUT /api/system-prompt", s.handleSetSystemPrompt)
	mux.HandleFunc("POST /api/approvals/{id}", s.handleApproval)
	mux.HandleFunc("POST /api/questions/{id}", s.handleQuestion)
	mux.HandleFunc("GET /api/fs", s.handleBrowse)
	mux.HandleFunc("GET /api/workdirs", s.handleWorkdirs)
	mux.HandleFunc("GET /api/profiles", s.handleProfiles)
	mux.HandleFunc("GET /api/providers", s.handleProviders)
	mux.HandleFunc("POST /api/providers/detect", s.handleDetectProvider)
	mux.HandleFunc("GET /api/providers/{name}/models", s.handleProviderModels)
	mux.HandleFunc("GET /api/providers/app", s.handleAppProviders)
	mux.HandleFunc("PUT /api/providers/app", s.handlePutAppProvider)
	mux.HandleFunc("PUT /api/providers/app/{name}/default", s.handleSetAppDefault)
	mux.HandleFunc("DELETE /api/providers/app/{name}", s.handleDeleteAppProvider)
	mux.HandleFunc("GET /api/secrets", s.handleListSecrets)
	mux.HandleFunc("PUT /api/secrets", s.handleSetSecret)
	mux.HandleFunc("DELETE /api/secrets/{name}", s.handleDeleteSecret)
	mux.HandleFunc("GET /api/modules", s.handleModules)
	mux.HandleFunc("POST /api/modules/{id}", s.handleSetModule)
	if s.modules != nil {
		s.modules.Mount(mux)
	}
	if s.staticFS != nil {
		mux.Handle("/", s.staticFS)
	}
	return s.guard(mux)
}

// WorkdirFor resolves a session's working directory (module.files hook).
func (s *Server) WorkdirFor(sessionID string) (string, error) {
	meta, err := s.store.GetMeta(sessionID)
	if err != nil {
		return "", err
	}
	if meta.Workdir != "" {
		return meta.Workdir, nil
	}
	return s.cfg.Workdir, nil
}

// windowFor resolves a model's context window: the profile's explicit
// context_window first (matched on the model too, so a provider with
// several added models of different windows resolves each correctly),
// then the documented default for known API models.
func (s *Server) windowFor(profileName, model string) int {
	for _, p := range s.cfg.ResolvedProfiles() {
		if (p.Name == profileName || (profileName == "" && p.Name == "default")) &&
			p.Model == model && p.ContextWindow > 0 {
			return p.ContextWindow
		}
	}
	for _, p := range s.cfg.ResolvedProfiles() {
		if (p.Name == profileName || (profileName == "" && p.Name == "default")) && p.ContextWindow > 0 {
			return p.ContextWindow
		}
	}
	return config.KnownContextWindow(model)
}

// providerFor builds the provider for a session from its profile. Keys
// resolve at attach time: environment first (startup expansion), then the
// secrets store, so a key entered in the UI works without a restart.
// Ollama endpoints are the harness's vision-capable default (local models
// advertise the capability and accept image_url parts); every other
// endpoint is constructed text-only, because the OpenAI-compatible API
// does not advertise vision and most backends (DeepSeek included) reject
// image parts outright.
func (s *Server) providerFor(meta *session.Meta) provider.Provider {
	for _, p := range s.cfg.ResolvedProfiles() {
		if p.Name == meta.Profile {
			key := p.APIKey
			if key == "" && p.KeyRef != "" {
				key = secrets.Get(p.KeyRef)
			}
			if isOllamaEndpoint(p.BaseURL) {
				return provider.NewOpenAICompatVision(p.BaseURL, key)
			}
			return provider.NewOpenAICompat(p.BaseURL, key)
		}
	}
	if isOllamaEndpoint(s.cfg.BaseURL) {
		return provider.NewOpenAICompatVision(s.cfg.BaseURL, s.cfg.APIKey)
	}
	return provider.NewOpenAICompat(s.cfg.BaseURL, s.cfg.APIKey)
}

// isOllamaEndpoint recognizes Ollama's OpenAI-compat adapter by its host.
// This is how the harness knows a profile can accept images: the local
// server advertises per-model vision capabilities through its native API,
// but the /v1 adapter carries no such signal. Host sniffing is the only
// reliable distinction: the model name says nothing (Ollama can serve
// non-vision models), and the endpoint's /v1/models list is not the
// capability table.
func isOllamaEndpoint(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

// visionDescriber is the capability the vision module provides: describe
// attached images so a text-only main model can read them.
type visionDescriber interface {
	Configured() bool
	Model() string
	Describe(ctx context.Context, images []string) (string, error)
}

// visionModule returns the enabled vision module as a describer, or nil
// when the feature is off (disabled, or not registered).
func (s *Server) visionModule() visionDescriber {
	if s.modules == nil || !s.modules.Enabled("vision") {
		return nil
	}
	m := s.modules.Get("vision")
	vd, ok := m.(visionDescriber)
	if !ok {
		return nil
	}
	return vd
}

// describeImages runs the image-to-text translation for a send on a
// text-only provider: the vision model describes the images and the
// description is appended to the message content, labeled. It returns a
// refusal error when the feature is unavailable (disabled or
// unconfigured) or the vision call fails; the send fails before the
// transcript is touched, so nothing half-described enters the
// conversation.
func (s *Server) describeImages(ctx context.Context, model string, images []string, content string) (string, error) {
	const hint = " switch to a vision-capable model (e.g. an ollama model) or send the message without the image"
	vd := s.visionModule()
	if vd == nil {
		return "", fmt.Errorf("this model (%s) does not accept images;%s", model, hint)
	}
	if !vd.Configured() {
		return "", fmt.Errorf("this model (%s) does not accept images;%s. Or configure [vision] in tether.toml to have images described by a vision model automatically", model, hint)
	}
	desc, err := vd.Describe(ctx, images)
	if err != nil {
		return "", fmt.Errorf("this model (%s) does not accept images and the vision backend failed to describe them: %v", model, err)
	}
	if strings.TrimSpace(desc) == "" {
		return "", fmt.Errorf("this model (%s) does not accept images;%s", model, hint)
	}
	return attachVisionDescription(content, vd.Model(), images, desc), nil
}

// attachVisionDescription appends a vision model's description to a user
// message, labeled so the main model and the trace both know the
// provenance. Multiple images share one numbered description.
func attachVisionDescription(content, model string, images []string, desc string) string {
	noun := "image"
	if len(images) > 1 {
		noun = "images"
	}
	label := fmt.Sprintf("[A vision model (%s) described the attached %s:]\n%s", model, noun, desc)
	if strings.TrimSpace(content) == "" {
		return label
	}
	return strings.TrimSpace(content) + "\n\n" + label
}

// resolve returns the live session, attaching an agent (and loading the
// transcript from disk) on first touch. The global lock guards only the
// maps: attach itself can take seconds (MCP servers connect there), and
// holding the lock through it would freeze every other request. A
// per-session channel makes concurrent first touches wait for the one
// attach instead of racing it.
func (s *Server) resolve(id string) (*liveSession, error) {
	for {
		s.mu.Lock()
		if ls, ok := s.live[id]; ok {
			s.mu.Unlock()
			ls.touch()
			return ls, nil
		}
		wait, inflight := s.resolving[id]
		if !inflight {
			wait = make(chan struct{})
			s.resolving[id] = wait
			s.mu.Unlock()
			break
		}
		s.mu.Unlock()
		<-wait // someone else is attaching; take their result next loop
	}

	ls, err := s.attach(id)

	s.mu.Lock()
	wait := s.resolving[id]
	delete(s.resolving, id)
	if err == nil {
		s.live[id] = ls
	}
	s.mu.Unlock()
	close(wait)
	if err == nil {
		ls.touch()
	}
	return ls, err
}

// attach builds the live session: provider, tools, modules, agent, and
// the transcript from disk. Runs without the server lock.
func (s *Server) attach(id string) (*liveSession, error) {
	meta, err := s.store.GetMeta(id)
	if err != nil {
		return nil, err
	}
	workdir := meta.Workdir
	if workdir == "" {
		workdir = s.cfg.Workdir
	}
	systemPrompt := sessionPrompt(config.SystemPrompt(workdir), meta.Mode)
	ws := config.LoadWorkdirSettings(workdir)
	prov := s.providerFor(meta)
	registry := tools.NewRegistry()
	var interceptors []agent.ToolInterceptor
	var observers []func(agent.Event)
	h := newHub()
	ls := &liveSession{hub: h, workdir: workdir, registry: registry, subagents: newSubagentRegistry()}
	if s.modules != nil {
		for _, t := range s.modules.Tools(workdir, ws.Modules) {
			registry.Add(t)
		}
		interceptors = s.modules.Interceptors(id, workdir, ws.Modules)
		observers = s.modules.Observers(id, workdir, ws.Modules)
		if s.modules.EnabledFor("subagents", ws.Modules) {
			registry.Add(&spawnTool{
				server: s, sessionID: id, ls: ls,
				workdir: workdir, prov: prov, model: meta.Model,
				defs: loadAgentDefs(workdir),
			})
			registry.Add(&AgentList{ls: ls})
			registry.Add(&AgentOutput{ls: ls})
			registry.Add(&AgentKill{ls: ls})
		}
	}
	// Spill storage lives in the session directory (see spillSink); the
	// read_spill tool and oversized-result spilling ride on it. Registered
	// after the tool surface so the model sees the working tools first.
	spillDir := filepath.Join(s.store.Dir(), id, "spill")
	registry.SetSpillSink(newSpillSink(spillDir))
	// Background jobs stream to files under the session's job directory,
	// so a long job's output is durable and pageable (bash_output's
	// offset), and dies with the session via the registry's Close hook.
	jobDir := filepath.Join(s.store.Dir(), id, "jobs")
	registry.SetJobManager(tools.NewSessionJobManager(jobDir))
	// Bridges from the registry's tools to the browser and the store. The
	// tools arrive through the module seam above; wiring their fields must
	// come after so the lookups find them.
	registry.SetAnswerer(&httpAnswerer{server: s, sessionID: id, ls: ls})
	registry.SetSessionSearcher(sessionSearcher{store: s.store})
	// The tasks module's todo tools write through a session bridge (the
	// ask_user answerer pattern: the tool cannot know its session, so
	// the server wires one per session). Fold the persisted events first
	// so the list agrees with the durable record after a restart.
	if s.modules != nil && s.modules.EnabledFor("tasks", ws.Modules) {
		if tm, ok := s.modules.Get("tasks").(*tasks.Module); ok {
			if evs, err := s.store.Events(id, 0); err == nil {
				tm.Replay(id, evs)
			}
			registry.SetTaskStore(tm.NewBridge(id, func(ev agent.Event) {
				// Stamped here, not by the loop: this event leaves the
				// harness through the bridge, like question events.
				ev.TurnID = ls.agent.TurnID()
				ev.Time = time.Now()
				s.dispatch(id, ls, ev)
			}))
		}
	}
	ls.mode = meta.Mode
	ls.contextWindow.Store(int64(s.windowFor(meta.Profile, meta.Model)))
	ls.agent = agent.New(agent.Config{
		Provider:      prov,
		Model:         meta.Model,
		Tools:         registry,
		Approver:      &httpApprover{server: s, sessionID: id, ls: ls},
		SystemPrompt:  systemPrompt,
		MaxRounds:     s.cfg.MaxRounds,
		ContextWindow: func() int { return int(ls.contextWindow.Load()) },
		Interceptors:  interceptors,
		Emit: func(ev agent.Event) {
			s.dispatch(id, ls, ev)
			for _, observe := range observers {
				observe(ev)
			}
		},
	})
	if msgs, err := s.store.Messages(id); err == nil && len(msgs) > 0 {
		ls.agent.SetMessages(msgs)
	} else {
		// Instruction files enter the conversation, not the system prompt
		// (fat system prompts stop small models from calling tools, and
		// this keeps the system prefix stable for caching). Visible in
		// the transcript: labeled, collapsed by the UI.
		var seed []provider.Message
		if name, content := config.ProjectInstructions(workdir); content != "" {
			instr := provider.Message{
				Role: provider.RoleUser,
				Kind: "instructions",
				Content: "Project instructions from " + name + " (background context for how to " +
					"work in this repository, not a substitute for its actual files; read files " +
					"with tools when asked about them):\n\n" + content,
			}
			ack := provider.Message{
				Role: provider.RoleAssistant,
				Kind: "instructions",
				Content: "Understood. I'll follow these project instructions and read the actual " +
					"files with my tools when asked about them.",
			}
			seed = append(seed, instr, ack)
		}
		// Memory index: the model's own cross-session notes, loaded at
		// the start of every session in the same position as AGENTS.md.
		// A file, not transcript state, so it survives compaction.
		if s.modules != nil && s.modules.EnabledFor("memory", ws.Modules) {
			if index, ok := memory.LoadIndexFor(workdir); ok && strings.TrimSpace(index) != "" {
				seed = append(seed, provider.Message{
					Role: provider.RoleUser,
					Kind: "instructions",
					Content: "Memory from earlier sessions (your own notes; keep them current with " +
						"memory_write, read topics with memory_read):\n\n" + index,
				})
			}
		}
		if len(seed) > 0 {
			ls.agent.SetMessages(seed)
			for _, m := range seed {
				_ = s.store.AppendMessage(id, &m)
			}
		}
	}
	s.live[id] = ls
	return ls, nil
}

// dispatch is the single sink for agent events: persist what's durable,
// broadcast everything to connected browsers.
func (s *Server) dispatch(sessionID string, ls *liveSession, ev agent.Event) {
	ls.touch()
	switch ev.Type {
	case agent.EventTextDelta, agent.EventThinkingDelta:
		// Ephemeral: streamed to the UI, never persisted, but buffered
		// so a page reload mid-stream can pick up where the text is.
		// Subagent deltas (ParentID set) stay out of the main buffer.
		if ev.ParentID == "" {
			ls.partialMu.Lock()
			if ev.Type == agent.EventTextDelta {
				ls.partialText += ev.Text
			} else {
				ls.partialThinking += ev.Text
			}
			ls.partialMu.Unlock()
		}
	case agent.EventMessage:
		if ev.ParentID == "" && ev.Message != nil && ev.Message.Role == provider.RoleAssistant {
			ls.partialMu.Lock()
			ls.partialText, ls.partialThinking = "", ""
			ls.partialMu.Unlock()
		}
		if err := s.store.AppendMessage(sessionID, ev.Message); err != nil {
			s.log.Error("persist message", "session", sessionID, "err", err)
		}
	case agent.EventCompact:
		if err := s.store.ReplaceTranscript(sessionID, ls.agent.Messages()); err != nil {
			s.log.Error("persist compacted transcript", "session", sessionID, "err", err)
		}
		s.persistEvent(sessionID, ev)
	default:
		s.persistEvent(sessionID, ev)
	}

	// Track the open approval or question so a page reload re-opens its
	// card instead of leaving the turn silently wedged.
	switch ev.Type {
	case agent.EventApprovalRequest:
		e := ev
		ls.partialMu.Lock()
		ls.pendingApproval = &e
		ls.partialMu.Unlock()
	case agent.EventApprovalResult:
		ls.partialMu.Lock()
		ls.pendingApproval = nil
		ls.partialMu.Unlock()
	case agent.EventQuestionRequest:
		e := ev
		ls.partialMu.Lock()
		ls.pendingQuestion = &e
		ls.partialMu.Unlock()
	case agent.EventQuestionResult:
		ls.partialMu.Lock()
		ls.pendingQuestion = nil
		ls.partialMu.Unlock()
	case agent.EventTurnEnd:
		ls.partialMu.Lock()
		ls.pendingApproval, ls.pendingQuestion = nil, nil
		ls.partialMu.Unlock()
		s.mu.Lock()
		ls.runningTool = ""
		s.mu.Unlock()
	}
	// The session list's live state: which tool the main loop is
	// executing. Subagent calls (ParentID set) don't count: the session
	// is busy on their behalf, but the main loop is the thing the row
	// reports. Turn end and the event that follows it both clear it.
	switch ev.Type {
	case agent.EventToolStart:
		if ev.ParentID == "" {
			s.mu.Lock()
			ls.runningTool = ev.ToolName
			s.mu.Unlock()
		}
	case agent.EventToolEnd:
		if ev.ParentID == "" {
			s.mu.Lock()
			ls.runningTool = ""
			s.mu.Unlock()
		}
	}
	ls.hub.broadcast(ev)

	// Automatic compaction, threshold-driven: when the profile declares a
	// context window and a turn's prompt crossed 85% of it, compact after
	// the turn ends. The compact event and the next turn's usage make the
	// cost visible rather than hidden.
	if window := int(ls.contextWindow.Load()); window > 0 {
		switch ev.Type {
		case agent.EventUsage:
			if ev.ParentID == "" && ev.Usage != nil {
				ls.lastPromptMu.Lock()
				ls.lastPrompt = ev.Usage.PromptTokens
				ls.lastPromptMu.Unlock()
			}
		case agent.EventTurnEnd:
			ls.lastPromptMu.Lock()
			over := ls.lastPrompt > window*85/100
			ls.lastPromptMu.Unlock()
			if over && ev.StopReason == "done" {
				go func() {
					if err := ls.agent.Compact(context.Background()); err == nil {
						_ = s.store.ReplaceTranscript(sessionID, ls.agent.Messages())
					}
				}()
			}
		}
	}
}

func (s *Server) persistEvent(sessionID string, ev agent.Event) {
	// Result payloads are already capped by the tools; log as-is.
	if err := s.store.AppendEvent(sessionID, &ev); err != nil {
		s.log.Error("persist event", "session", sessionID, "err", err)
	}
}

// --- handlers ---

func (s *Server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]string{
		"model":    s.cfg.Model,
		"base_url": s.cfg.BaseURL,
		"workdir":  s.cfg.Workdir,
	})
}

func (s *Server) handleListSessions(w http.ResponseWriter, _ *http.Request) {
	metas, err := s.store.List()
	if err != nil {
		httpError(w, http.StatusInternalServerError, err)
		return
	}
	if metas == nil {
		metas = []*session.Meta{}
	}
	// Per-session live state: a session running a turn looks different
	// from an idle one, so parallel runs across sessions are visible.
	out := make([]map[string]any, 0, len(metas))
	for _, m := range metas {
		busy, runningTool := s.runningToolFor(m.ID)
		out = append(out, map[string]any{
			"id":           m.ID,
			"title":        m.Title,
			"model":        m.Model,
			"profile":      m.Profile,
			"workdir":      m.Workdir,
			"mode":         m.Mode,
			"created_at":   m.CreatedAt,
			"updated_at":   m.UpdatedAt,
			"busy":         busy,
			"running_tool": runningTool,
		})
	}
	writeJSON(w, out)
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Workdir string `json:"workdir"`
		Profile string `json:"profile"`
		// Model overrides the default model for this session; the app
		// setup flow passes it alongside Profile.
		Model string `json:"model"`
		// Worktree, when set, creates an isolated git worktree of the
		// given repo and opens the session there, so parallel sessions
		// in one repository never collide. Overrides Workdir.
		Worktree string `json:"worktree"`
		// WorktreeName is the optional name for the new worktree (a
		// slug); a timestamped default is used when empty.
		WorktreeName string `json:"worktree_name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in) // empty body = defaults

	workdir := s.cfg.Workdir
	var wtRepo string // repo root, when this session is worktree-backed
	if in.Worktree != "" {
		// Create the worktree and bind the session to it in one step.
		abs, err := resolveDir(in.Worktree)
		if err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		wt, ok := s.modules.Get("worktrees").(*worktrees.Module)
		if !ok {
			httpError(w, http.StatusInternalServerError, fmt.Errorf("worktrees module unavailable"))
			return
		}
		path, _, err := wt.Create(r.Context(), abs, in.WorktreeName)
		if err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		workdir = path
		wtRepo = abs
	} else if in.Workdir != "" {
		abs, err := resolveDir(in.Workdir)
		if err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		workdir = abs
	}
	// Per-repository defaults apply when the request doesn't specify.
	ws := config.LoadWorkdirSettings(workdir)
	if in.Profile == "" && ws.Profile != "" {
		in.Profile = ws.Profile
	}
	model := s.cfg.Model
	if ws.Model != "" {
		model = ws.Model
	}
	if in.Model != "" {
		model = in.Model
	}
	profile := ""
	// No explicit or per-repo profile: the app-managed default (the
	// provider last activated through the Models view) wins over the
	// plain top-level default. When it applies, its UI-chosen model
	// wins over the profile's configured model below.
	appDefault := false
	if in.Profile == "" && ws.Profile == "" {
		if name, m, ok := config.DefaultAppProvider(); ok {
			in.Profile, model, appDefault = name, m, true
		}
	}
	if in.Profile != "" && in.Profile != "default" {
		found := false
		for _, p := range s.cfg.ResolvedProfiles() {
			if p.Name == in.Profile {
				// The profile's configured model is only the fallback:
				// an explicit request model or the app default wins.
				if !appDefault && in.Model == "" {
					model = p.Model
				}
				profile, found = p.Name, true
				break
			}
		}
		if !found {
			httpError(w, http.StatusBadRequest, fmt.Errorf("unknown profile %q", in.Profile))
			return
		}
	}

	meta, err := s.store.Create(model, workdir)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err)
		return
	}
	if profile != "" {
		meta.Profile = profile
		_ = s.store.SaveMeta(meta)
	}
	// Record which session owns a worktree, so cleanup can refuse to
	// remove one that is still in use.
	if wtRepo != "" {
		if wt, ok := s.modules.Get("worktrees").(*worktrees.Module); ok {
			_ = wt.Bind(wtRepo, workdir, meta.ID)
		}
	}
	writeJSON(w, meta)
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	ls, wasLive := s.live[id]
	if wasLive {
		if ls.cancel != nil {
			ls.cancel()
		}
		delete(s.live, id)
	}
	s.mu.Unlock()
	if wasLive {
		ls.teardown()
	}
	if err := s.store.Delete(id); err != nil {
		httpError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSetModel switches a session's model and, optionally, its provider
// profile. Rejected while a turn is running.
func (s *Server) handleSetModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		Model   string `json:"model"`
		Profile string `json:"profile"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Model == "" {
		httpError(w, http.StatusBadRequest, fmt.Errorf("model is required"))
		return
	}
	meta, err := s.store.GetMeta(id)
	if err != nil {
		httpError(w, http.StatusNotFound, err)
		return
	}
	ls, err := s.resolve(id)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err)
		return
	}

	if in.Profile != "" && in.Profile != meta.Profile {
		meta.Profile = in.Profile
		if in.Profile == "default" {
			meta.Profile = ""
		}
		if err := ls.agent.SetProvider(s.providerFor(meta)); err != nil {
			httpError(w, http.StatusConflict, err)
			return
		}
	}
	if err := ls.agent.SetModel(in.Model); err != nil {
		httpError(w, http.StatusConflict, err)
		return
	}
	meta.Model = in.Model
	if err := s.store.SaveMeta(meta); err != nil {
		httpError(w, http.StatusInternalServerError, err)
		return
	}
	// The context window follows the model: a switch mid-session must
	// not keep the old model's meter (or lose it entirely).
	ls.contextWindow.Store(int64(s.windowFor(meta.Profile, meta.Model)))
	writeJSON(w, struct {
		*session.Meta
		ContextWindow int64 `json:"context_window"`
	}{meta, ls.contextWindow.Load()})
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	meta, err := s.store.GetMeta(id)
	if err != nil {
		httpError(w, http.StatusNotFound, err)
		return
	}
	ls, err := s.resolve(id)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err)
		return
	}
	events, _ := s.store.Events(id, 500)
	if events == nil {
		events = []agent.Event{}
	}
	text, thinking := ls.partial()
	ls.partialMu.Lock()
	pendingApproval, pendingQuestion := ls.pendingApproval, ls.pendingQuestion
	ls.partialMu.Unlock()
	systemPrompt := ""
	if msgs := ls.agent.Messages(); len(msgs) > 0 && msgs[0].Role == provider.RoleSystem {
		systemPrompt = msgs[0].Content
	}
	writeJSON(w, map[string]any{
		"meta":             meta,
		"messages":         ls.agent.Messages(),
		"events":           events,
		"partial":          map[string]string{"text": text, "thinking": thinking},
		"system_prompt":    systemPrompt,
		"context_window":   ls.contextWindow.Load(),
		"pending_approval": pendingApproval,
		"pending_question": pendingQuestion,
	})
}

func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		Content string   `json:"content"`
		Images  []string `json:"images"`
		// Attachments are workdir-relative paths from @-mentions. They
		// enter the transcript as a synthetic read_file exchange, not as
		// text pasted into the user message.
		Attachments []string `json:"attachments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil ||
		(strings.TrimSpace(in.Content) == "" && len(in.Images) == 0) {
		httpError(w, http.StatusBadRequest, fmt.Errorf("content is required"))
		return
	}
	// Images arrive as data URLs from the browser; anything else (remote
	// URLs, other schemes) is refused rather than fetched.
	for _, img := range in.Images {
		if !strings.HasPrefix(img, "data:image/") {
			httpError(w, http.StatusBadRequest, fmt.Errorf("images must be data:image/ URLs"))
			return
		}
	}
	if len(in.Images) > 0 {
		meta, err := s.store.GetMeta(id)
		if err != nil {
			httpError(w, http.StatusNotFound, err)
			return
		}
		if !s.providerFor(meta).SupportsImages() {
			content, err := s.describeImages(r.Context(), meta.Model, in.Images, in.Content)
			if err != nil {
				httpError(w, http.StatusBadRequest, err)
				return
			}
			in.Content = content
		}
	}
	msg := provider.Message{Role: provider.RoleUser, Content: in.Content, Images: in.Images}
	ls, err := s.resolve(id)
	if err != nil {
		httpError(w, http.StatusNotFound, err)
		return
	}

	// A message during a running turn is steering: delivered at the
	// loop's next step rather than rejected. Steering cannot carry a
	// synthetic tool exchange (the queue is user messages only), so
	// attachment content is inlined there instead.
	if ls.agent.Busy() {
		for _, path := range in.Attachments {
			if content, err := readAttachment(ls.workdir, path); err == nil {
				msg.Content += fmt.Sprintf("\n\nContents of %s:\n```\n%s\n```", path, content)
			}
		}
		if err := ls.agent.SteerMessage(msg); err != nil {
			httpError(w, http.StatusConflict, err)
			return
		}
		// Accepted, queued: the message rides the steering queue into the
		// transcript at the loop's next step. The UI shows it as pending
		// so it never looks lost.
		writeJSON(w, map[string]bool{"queued": true})
		return
	}

	msgs := append([]provider.Message{msg}, attachmentExchange(ls.workdir, in.Attachments)...)

	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	ls.cancel = cancel
	s.mu.Unlock()

	go func() {
		defer cancel()
		if err := ls.agent.RunMessages(ctx, msgs); err != nil {
			if err == agent.ErrBusy {
				return // lost the race with another sender; their turn runs
			}
			s.log.Error("run", "session", id, "err", err)
		}
		s.touchTitle(id, in.Content)
	}()
	w.WriteHeader(http.StatusAccepted)
}

const attachmentCap = 24 * 1024

// readAttachment reads an @-mentioned file, confined to the session's
// workdir and capped.
func readAttachment(workdir, path string) (string, error) {
	resolved, err := tools.Workdir(workdir).Resolve(path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", err
	}
	if len(data) > attachmentCap {
		return string(data[:attachmentCap]) + "\n… (truncated)", nil
	}
	return string(data), nil
}

// attachmentExchange turns @-mentioned files into a synthetic read_file
// exchange: one assistant message calling read_file per path, plus the
// results. The model sees pinned content exactly as if it had read the
// files itself, and the transcript shows collapsed tool cards instead of
// pasted file bodies.
func attachmentExchange(workdir string, paths []string) []provider.Message {
	if len(paths) > 4 {
		paths = paths[:4]
	}
	var calls []provider.ToolCall
	var results []provider.Message
	for i, path := range paths {
		content, err := readAttachment(workdir, path)
		if err != nil {
			content = fmt.Sprintf("could not read %s: %v", path, err)
		}
		args, _ := json.Marshal(map[string]string{"path": path})
		id := fmt.Sprintf("attachment_%d", i+1)
		calls = append(calls, provider.ToolCall{ID: id, Name: "read_file", Args: string(args)})
		results = append(results, provider.Message{
			Role: provider.RoleTool, ToolCallID: id, Content: content,
		})
	}
	if len(calls) == 0 {
		return nil
	}
	return append([]provider.Message{{Role: provider.RoleAssistant, ToolCalls: calls}}, results...)
}

// planModeGuidance is the ONLY prompt change the tasks module makes, and
// it is gated: inactive modes add zero tokens, so the system prompt stays
// small and cacheable (the plan's invariant). Plan mode itself is an
// existing approval policy (read-only, every non-safe tool is denied),
// so the guidance line only tells the model what it already cannot do,
// pointing it at the task list as the plan surface.
const planModeGuidance = "\n\nYou are in plan mode. Explore and design before presenting the complete plan through the task list (todo_write). Do not write files or run commands; the harness will deny them."

// sessionPrompt composes the session's system prompt: the global prompt
// plus, only in plan mode, the plan guidance line. The line lives in the
// system message (rebuilt on every attach, never persisted to the
// transcript), so it survives resume and mode switches without touching
// the durable record.
func sessionPrompt(base, mode string) string {
	if mode == "plan" {
		return base + planModeGuidance
	}
	return base
}

// handleSetMode switches the session's approval policy, effective from
// the next tool call.
func (s *Server) handleSetMode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpError(w, http.StatusBadRequest, err)
		return
	}
	switch in.Mode {
	case "", "ask", "plan", "auto_edit", "auto":
	default:
		httpError(w, http.StatusBadRequest, fmt.Errorf("unknown mode %q", in.Mode))
		return
	}
	meta, err := s.store.GetMeta(id)
	if err != nil {
		httpError(w, http.StatusNotFound, err)
		return
	}
	ls, err := s.resolve(id)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err)
		return
	}
	ls.setMode(in.Mode)
	meta.Mode = in.Mode
	if err := s.store.SaveMeta(meta); err != nil {
		httpError(w, http.StatusInternalServerError, err)
		return
	}
	// The plan guidance line follows the mode live: it takes effect at
	// the next model call (SetSystemPrompt is rejected mid-turn). The
	// system message is rebuilt on attach anyway, so this is only for a
	// mode switch while the session is attached.
	_ = ls.agent.SetSystemPrompt(sessionPrompt(config.SystemPrompt(ls.workdir), in.Mode))
	writeJSON(w, meta)
}

// touchTitle names the session after its first user message: a short
// model-generated title, with plain truncation as the fallback.
func (s *Server) touchTitle(id, firstMessage string) {
	meta, err := s.store.GetMeta(id)
	if err != nil || meta.Title != "new session" {
		return
	}
	fallback := strings.TrimSpace(firstMessage)
	if len(fallback) > 60 {
		fallback = fallback[:60] + "…"
	}
	meta.Title = fallback
	if title := s.generateTitle(meta, firstMessage); title != "" {
		meta.Title = title
	}
	_ = s.store.SaveMeta(meta)
}

// generateTitle asks the session's own model for a short title; any
// failure returns "" and the truncated fallback stands.
func (s *Server) generateTitle(meta *session.Meta, firstMessage string) string {
	if len(firstMessage) > 500 {
		firstMessage = firstMessage[:500]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	events, err := s.providerFor(meta).Stream(ctx, provider.Request{
		Model: meta.Model,
		Messages: []provider.Message{{
			Role: provider.RoleUser,
			Content: "Write a three-to-six-word title for a conversation that starts with the " +
				"following message. Reply with the title only, no quotes.\n\n" + firstMessage,
		}},
	})
	if err != nil {
		return ""
	}
	var title string
	for ev := range events {
		if ev.Kind == provider.EventTextDelta {
			title += ev.Text
		}
		if ev.Kind == provider.EventError {
			return ""
		}
	}
	title = strings.TrimSpace(strings.Trim(strings.TrimSpace(title), `"'`))
	if i := strings.IndexByte(title, '\n'); i >= 0 {
		title = title[:i]
	}
	if len(title) > 60 {
		title = title[:60] + "…"
	}
	return title
}

func (s *Server) handleCompact(w http.ResponseWriter, r *http.Request) {
	ls, err := s.resolve(r.PathValue("id"))
	if err != nil {
		httpError(w, http.StatusNotFound, err)
		return
	}
	go func() {
		if err := ls.agent.Compact(context.Background()); err != nil {
			s.log.Error("compact", "err", err)
			ls.hub.broadcast(agent.Event{Type: agent.EventError, Error: "compact: " + err.Error()})
		}
	}()
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleInterrupt(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	ls, ok := s.live[id]
	var cancel context.CancelFunc
	if ok {
		cancel = ls.cancel
	}
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAgents lists the named subagents defined in the session's
// repository (.tether/agents/*.md), for the project view.
func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	workdir, err := s.WorkdirFor(r.PathValue("id"))
	if err != nil {
		httpError(w, http.StatusNotFound, err)
		return
	}
	type agentInfo struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Tools       string `json:"tools"`
	}
	out := []agentInfo{}
	for _, d := range loadAgentDefs(workdir) {
		out = append(out, agentInfo{Name: d.Name, Description: d.Description, Tools: d.Tools})
	}
	writeJSON(w, map[string]any{"agents": out})
}

// handleSearch scans transcripts for a query: full-text recall across
// sessions, feeding the switcher.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	hits, err := s.store.Search(r.URL.Query().Get("q"), 20)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err)
		return
	}
	if hits == nil {
		hits = []session.SearchHit{}
	}
	writeJSON(w, map[string]any{"hits": hits})
}

// httpAnswerer bridges the ask_user tool's blocking question to a browser
// round-trip, like httpApprover: park a channel, let the UI POST the
// answer into it. Interruption (turn cancelled) resolves the question
// with the interrupt answer rather than erroring, so the model hears what
// happened.
type httpAnswerer struct {
	server    *Server
	sessionID string
	ls        *liveSession
}

func (h *httpAnswerer) AskQuestion(ctx context.Context, req tools.QuestionRequest) (string, error) {
	ch := make(chan string, 1)
	h.server.mu.Lock()
	h.server.questions[req.ID] = ch
	h.server.mu.Unlock()

	ls := h.ls
	if ls == nil {
		ls, _ = h.server.resolve(h.sessionID)
	}
	if ls != nil {
		// Through dispatch, not the hub: question events persist and
		// reach observers exactly like approval events. The agent stamps
		// its own events with Time; server-emitted ones need it here.
		h.server.dispatch(h.sessionID, ls, agent.Event{
			Type:       agent.EventQuestionRequest,
			Time:       time.Now(),
			TurnID:     ls.agent.TurnID(),
			ApprovalID: req.ID,
			Question:   req.Question,
			Options:    req.Options,
		})
	}

	select {
	case answer := <-ch:
		if ls != nil {
			h.server.dispatch(h.sessionID, ls, agent.Event{
				Type: agent.EventQuestionResult, Time: time.Now(),
				TurnID: ls.agent.TurnID(), ApprovalID: req.ID, Answer: answer,
			})
		}
		return answer, nil
	case <-ctx.Done():
		h.server.mu.Lock()
		delete(h.server.questions, req.ID)
		h.server.mu.Unlock()
		return tools.InterruptAnswer, ctx.Err()
	}
}

// handleQuestion accepts an answer to an ask_user question.
func (s *Server) handleQuestion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		Answer string `json:"answer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(in.Answer) == "" {
		httpError(w, http.StatusBadRequest, fmt.Errorf("answer is required"))
		return
	}

	s.mu.Lock()
	ch, ok := s.questions[id]
	delete(s.questions, id)
	s.mu.Unlock()
	if !ok {
		httpError(w, http.StatusNotFound, fmt.Errorf("no pending question %s", id))
		return
	}
	ch <- in.Answer
	w.WriteHeader(http.StatusNoContent)
}

// handleApproval is the approval round-trip endpoint.
func (s *Server) handleApproval(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		Decision agent.Decision `json:"decision"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpError(w, http.StatusBadRequest, err)
		return
	}
	switch in.Decision {
	case agent.DecisionAllow, agent.DecisionAllowSession, agent.DecisionAllowAlways, agent.DecisionDeny:
	default:
		httpError(w, http.StatusBadRequest, fmt.Errorf("invalid decision %q", in.Decision))
		return
	}

	s.mu.Lock()
	ch, ok := s.pending[id]
	delete(s.pending, id)
	s.mu.Unlock()
	if !ok {
		httpError(w, http.StatusNotFound, fmt.Errorf("no pending approval %s", id))
		return
	}
	ch <- in.Decision
	w.WriteHeader(http.StatusNoContent)
}

// handleEvents is the SSE endpoint: replays nothing (the UI loads history
// via GET /api/sessions/{id}), then streams live events until the client
// disconnects.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	ls, err := s.resolve(r.PathValue("id"))
	if err != nil {
		httpError(w, http.StatusNotFound, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpError(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	sub := ls.hub.subscribe()
	defer ls.hub.unsubscribe(sub)

	for {
		select {
		case ev := <-sub:
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// handleGetSystemPrompt reports level 1 of the prompt stack: the global
// prompt shared by every session.
func (s *Server) handleGetSystemPrompt(w http.ResponseWriter, _ *http.Request) {
	prompt, overridden := config.GlobalSystemPrompt()
	writeJSON(w, map[string]any{
		"prompt":     prompt,
		"overridden": overridden,
		"path":       config.SystemPromptPath(),
	})
}

// handleSetSystemPrompt writes the global override (empty restores the
// built-in default). Applies to sessions attached after the change.
func (s *Server) handleSetSystemPrompt(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpError(w, http.StatusBadRequest, err)
		return
	}
	if err := config.SetGlobalSystemPrompt(in.Prompt); err != nil {
		httpError(w, http.StatusInternalServerError, err)
		return
	}
	prompt, overridden := config.GlobalSystemPrompt()
	writeJSON(w, map[string]any{"prompt": prompt, "overridden": overridden})
}

// handleExport renders the session as a self-contained markdown document:
// transcript, tool calls with results, and token totals.
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	meta, err := s.store.GetMeta(id)
	if err != nil {
		httpError(w, http.StatusNotFound, err)
		return
	}
	msgs, err := s.store.Messages(id)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err)
		return
	}
	events, _ := s.store.Events(id, 0)

	var input, output, cached int
	for _, ev := range events {
		if ev.Type == agent.EventUsage && ev.Usage != nil {
			input += ev.Usage.PromptTokens
			output += ev.Usage.CompletionTokens
			cached += ev.Usage.CachedTokens
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s\n\n", meta.Title)
	fmt.Fprintf(&sb, "- model: %s\n- workdir: %s\n- created: %s\n", meta.Model, meta.Workdir, meta.CreatedAt.Format("2006-01-02 15:04"))
	fmt.Fprintf(&sb, "- tokens: %d in, %d out", input, output)
	if cached > 0 {
		fmt.Fprintf(&sb, " (%d cached)", cached)
	}
	sb.WriteString("\n\n---\n\n")

	for _, m := range msgs {
		switch m.Role {
		case provider.RoleUser:
			fmt.Fprintf(&sb, "## User\n\n%s\n\n", m.Content)
		case provider.RoleAssistant:
			if m.Content != "" {
				fmt.Fprintf(&sb, "## Assistant\n\n%s\n\n", m.Content)
			}
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&sb, "### Tool call: %s\n\n```json\n%s\n```\n\n", tc.Name, tc.Args)
			}
		case provider.RoleTool:
			fmt.Fprintf(&sb, "```\n%s\n```\n\n", m.Content)
		}
	}

	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "tether-"+meta.ID+".md"))
	_, _ = io.WriteString(w, sb.String())
}

// handleFork creates a new session whose transcript is this session's
// first N messages: rewind as a branch. The original stays intact;
// nothing is ever destroyed by going back.
func (s *Server) handleFork(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		// Keep is the number of transcript messages (excluding the system
		// prompt) to carry into the fork.
		Keep int `json:"keep"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Keep < 1 {
		httpError(w, http.StatusBadRequest, fmt.Errorf("keep must be a positive message count"))
		return
	}
	meta, err := s.store.GetMeta(id)
	if err != nil {
		httpError(w, http.StatusNotFound, err)
		return
	}
	msgs, err := s.store.Messages(id)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err)
		return
	}
	if in.Keep > len(msgs) {
		in.Keep = len(msgs)
	}

	fork, err := s.store.Create(meta.Model, meta.Workdir)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err)
		return
	}
	fork.Profile = meta.Profile
	fork.Mode = meta.Mode
	fork.Title = "fork of " + meta.Title
	if len(fork.Title) > 60 {
		fork.Title = fork.Title[:60] + "…"
	}
	if err := s.store.SaveMeta(fork); err != nil {
		httpError(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.store.ReplaceTranscript(fork.ID, msgs[:in.Keep]); err != nil {
		httpError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, fork)
}

// handleBrowse lists subdirectories for the folder picker. Directories
// only: picking a workdir, not browsing files (the files module does that
// within a session).
func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	home, _ := os.UserHomeDir()
	if path == "" {
		path = home
	}
	// People type ~/projects; meet them there.
	if path == "~" || strings.HasPrefix(path, "~/") {
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	abs, err := resolveDir(path)
	if err != nil {
		httpError(w, http.StatusBadRequest, err)
		return
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		httpError(w, http.StatusBadRequest, err)
		return
	}
	dirs := []string{}
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && e.Name() != "node_modules" {
			dirs = append(dirs, e.Name())
		}
	}
	parent := filepath.Dir(abs)
	if parent == abs {
		parent = ""
	}
	writeJSON(w, map[string]any{"path": abs, "parent": parent, "dirs": dirs})
}

// handleWorkdirs suggests starting points: recently used session workdirs,
// the configured default, and home.
func (s *Server) handleWorkdirs(w http.ResponseWriter, _ *http.Request) {
	home, _ := os.UserHomeDir()
	recent := []string{}
	seen := map[string]bool{}
	if metas, err := s.store.List(); err == nil {
		for _, m := range metas {
			if m.Workdir != "" && !seen[m.Workdir] {
				seen[m.Workdir] = true
				recent = append(recent, m.Workdir)
			}
		}
	}
	sort.Strings(recent)
	writeJSON(w, map[string]any{
		"recent":  recent,
		"default": s.cfg.Workdir,
		"home":    home,
	})
}

func (s *Server) handleProfiles(w http.ResponseWriter, _ *http.Request) {
	// APIKey is json:"-", never leaves. key_missing is computed live so a
	// key just entered in the UI clears the flag without a restart.
	profiles := s.cfg.ResolvedProfiles()
	for i := range profiles {
		p := &profiles[i]
		p.KeyMissing = p.APIKey == "" && p.KeyRef != "" && secrets.Get(p.KeyRef) == ""
	}
	writeJSON(w, profiles)
}

// handleProviders serves the curated catalog plus live per-provider
// state: key-missing for hosted providers, and availability for local
// ones (is the server actually running?). Local probes hit the endpoint
// with a short timeout, so the Models view can show "offline" at a
// glance without the user having to open the pane.
func (s *Server) handleProviders(w http.ResponseWriter, _ *http.Request) {
	out := config.Catalog()
	for i := range out {
		p := &out[i]
		p.KeyMissing = !p.Local && p.Env != "" && secrets.Get(p.Env) == ""
		if p.Local {
			p.Available = s.probeProvider(p.BaseURL)
		} else {
			p.Available = true // hosted endpoints are always reachable
		}
	}
	writeJSON(w, out)
}

// probeProvider reports whether an OpenAI-compatible endpoint answers.
// It dials with a short timeout and treats any HTTP response (even an
// auth error) as "the server is there". Local providers that are not
// running fail to connect and report offline.
func (s *Server) probeProvider(baseURL string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return true
}

// handleDetectProvider matches a pasted key against the catalog's key
// prefixes and names the provider, so the UI can pre-select it (goose's
// quick-setup trick, made data-driven).
func (s *Server) handleDetectProvider(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]string{"provider": config.DetectProvider(strings.TrimSpace(in.Key))})
}

// handleProviderModels lists a provider's models via the generic
// OpenAI-compatible /v1/models call. This is a provider capability, not
// a feature-module one: the route lives here so it works for every
// provider and never depends on a module toggle.
func (s *Server) handleProviderModels(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	profile, ok := s.profileForName(name)
	if !ok {
		httpError(w, http.StatusNotFound, fmt.Errorf("unknown provider %q", name))
		return
	}
	// Resolve the key the same way providerFor does at attach: the env
	// value first, then the secrets store, so a key entered in the UI
	// (or the Keychain) works without a restart.
	key := profile.APIKey
	if key == "" && profile.KeyRef != "" {
		key = secrets.Get(profile.KeyRef)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	prov := provider.NewOpenAICompat(profile.BaseURL, key)
	if isOllamaEndpoint(profile.BaseURL) {
		prov = provider.NewOpenAICompatVision(profile.BaseURL, key)
	}
	names, err := prov.ListModels(ctx)
	if err != nil {
		httpError(w, http.StatusBadGateway, err)
		return
	}
	if names == nil {
		names = []string{}
	}
	writeJSON(w, names)
}

// profileForName resolves a provider name (catalog, app-managed, or
// user-defined profile) to its endpoint and resolved key. Catalog
// entries that are not yet activated resolve to their default endpoint,
// so listing a provider's models works before any setup.
func (s *Server) profileForName(name string) (config.Profile, bool) {
	for _, p := range s.cfg.ResolvedProfiles() {
		if p.Name == name {
			return p, true
		}
	}
	if c := config.CatalogEntry(name); c != nil {
		key := ""
		if c.Env != "" {
			key = secrets.Get(c.Env)
		}
		return config.Profile{
			Name:    c.Name,
			BaseURL: c.BaseURL,
			APIKey:  key,
			KeyRef:  c.Env,
		}, true
	}
	return config.Profile{}, false
}

// handleAppProviders serves the app-managed provider list (what the user
// has activated or added through the UI).
func (s *Server) handleAppProviders(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, config.LoadProviders())
}

// handlePutAppProvider upserts an app-managed (provider, model) entry
// (add a catalog model with a key, or a custom one). The identity is
// Name + Model, so adding a second model of a provider is a separate
// entry, not a replacement. PUT manages presence and fields only: it
// preserves an existing entry's Default flag (and a fresh entry starts
// non-default), so adding or re-using a model can never change what new
// sessions start on. Whether an entry is the default is set explicitly
// through the dedicated default endpoint.
func (s *Server) handlePutAppProvider(w http.ResponseWriter, r *http.Request) {
	var in config.AppProvider
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Name) == "" {
		httpError(w, http.StatusBadRequest, fmt.Errorf("name is required"))
		return
	}
	ps := config.LoadProviders()
	out := make([]config.AppProvider, 0, len(ps)+1)
	replaced := false
	for _, p := range ps {
		if p.Name == in.Name && p.Removed {
			// Re-adding a model of this provider in the UI revives it;
			// drop the tombstone left by a previous removal.
			continue
		}
		if p.Name == in.Name && p.Model == in.Model {
			entry := in
			entry.Default = p.Default
			out = append(out, entry)
			replaced = true
			continue
		}
		out = append(out, p)
	}
	if !replaced {
		out = append(out, in)
	}
	if err := config.SaveProviders(out); err != nil {
		httpError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, out)
}

// handleSetAppDefault sets or clears which added (provider, model) entry
// new sessions start on. Setting one clears every other entry, so at
// most one default holds; clearing (default:false) leaves no app-managed
// default and new sessions fall back to the top-level config default.
func (s *Server) handleSetAppDefault(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var in struct {
		Model   string `json:"model"`
		Default bool   `json:"default"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(name) == "" || in.Model == "" {
		httpError(w, http.StatusBadRequest, fmt.Errorf("name and model are required"))
		return
	}
	ps := config.LoadProviders()
	found := false
	for i := range ps {
		if ps[i].Name == name && ps[i].Model == in.Model {
			ps[i].Default = in.Default
			found = true
		} else if in.Default {
			ps[i].Default = false
		}
	}
	if !found {
		httpError(w, http.StatusNotFound, fmt.Errorf("provider model %q / %q is not added", name, in.Model))
		return
	}
	if err := config.SaveProviders(ps); err != nil {
		httpError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, ps)
}

// handleDeleteAppProvider removes one app-managed (provider, model)
// entry; the model comes as a query param because model names can
// contain slashes (OpenRouter's deepseek/deepseek-v4-flash). When the
// last added model of a provider is removed and a config.toml profile
// of the same name exists, a tombstone is left in its place so the
// profile does not resurface in the model picker; the user removed this
// provider in the UI, and that decision should hold. Removing one of
// several added models leaves the provider in place, so no tombstone.
// Re-adding the provider in the UI (PUT) replaces the tombstone.
func (s *Server) handleDeleteAppProvider(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	model := r.URL.Query().Get("model")
	ps := config.LoadProviders()
	out := ps[:0]
	for _, p := range ps {
		if p.Name == name && p.Model == model {
			continue
		}
		out = append(out, p)
	}
	remaining := false
	for _, p := range out {
		if p.Name == name {
			remaining = true
			break
		}
	}
	if !remaining {
		for _, p := range s.cfg.Profiles {
			if p.Name == name {
				out = append(out, config.AppProvider{Name: name, Removed: true})
				break
			}
		}
	}
	if err := config.SaveProviders(out); err != nil {
		httpError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, out)
}

func (s *Server) handleListSecrets(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"names": secrets.List(), "backend": secrets.Backend()})
}

func (s *Server) handleSetSecret(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Name) == "" {
		httpError(w, http.StatusBadRequest, fmt.Errorf("name is required"))
		return
	}
	if err := secrets.Set(strings.TrimSpace(in.Name), in.Value); err != nil {
		httpError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteSecret(w http.ResponseWriter, r *http.Request) {
	if err := secrets.Set(r.PathValue("name"), ""); err != nil {
		httpError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleModules(w http.ResponseWriter, _ *http.Request) {
	if s.modules == nil {
		writeJSON(w, module.CoreStatus{})
		return
	}
	writeJSON(w, s.modules.Store())
}

func (s *Server) handleSetModule(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpError(w, http.StatusBadRequest, err)
		return
	}
	if s.modules == nil {
		httpError(w, http.StatusNotFound, fmt.Errorf("unknown module %q", r.PathValue("id")))
		return
	}
	if err := s.modules.SetEnabled(r.PathValue("id"), in.Enabled); err != nil {
		httpError(w, http.StatusNotFound, err)
		return
	}
	// Idle sessions detach so their next touch rebuilds the tool registry
	// with the new module set; running turns keep theirs until they finish.
	var detached []*liveSession
	s.mu.Lock()
	for id, ls := range s.live {
		if !ls.agent.Busy() {
			detached = append(detached, ls)
			delete(s.live, id)
		}
	}
	s.mu.Unlock()
	for _, ls := range detached {
		ls.teardown()
	}
	writeJSON(w, s.modules.Store())
}

// resolveDir validates and canonicalizes a directory path (symlinks
// resolved so tool confinement compares real paths).
func resolveDir(p string) (string, error) {
	info, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", p)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// httpApprover bridges the agent's blocking approval request to a browser
// round-trip: park a channel, let the UI POST the decision into it. The
// session's approval mode short-circuits the round-trip where policy
// already answers.
type httpApprover struct {
	server    *Server
	sessionID string
	ls        *liveSession
}

func (h *httpApprover) RequestApproval(ctx context.Context, req agent.ApprovalRequest) (agent.Decision, error) {
	if h.ls != nil {
		switch h.ls.getMode() {
		case "plan":
			return agent.DecisionDeny, nil // read-only: propose, don't act
		case "auto":
			return agent.DecisionAllow, nil
		case "auto_edit":
			if req.ToolName == "write_file" || req.ToolName == "edit_file" {
				return agent.DecisionAllow, nil
			}
		}
		// The repository's durable allowlist, read fresh so an entry
		// added moments ago (or by a git pull) applies immediately.
		ws := config.LoadWorkdirSettings(h.ls.workdir)
		if config.Allowed(ws.Allow, req.ToolName, req.ToolArgs) {
			return agent.DecisionAllow, nil
		}
	}
	ch := make(chan agent.Decision, 1)
	h.server.mu.Lock()
	h.server.pending[req.ID] = ch
	h.server.mu.Unlock()

	select {
	case d := <-ch:
		// "Always allow" persists the rule for this repository, then
		// acts as a plain allow.
		if d == agent.DecisionAllowAlways {
			if h.ls != nil {
				rule := config.AllowRule(req.ToolName, req.ToolArgs)
				if err := config.AddWorkdirAllow(h.ls.workdir, rule); err != nil {
					h.server.log.Error("allowlist", "err", err)
				}
			}
			return agent.DecisionAllow, nil
		}
		return d, nil
	case <-ctx.Done():
		h.server.mu.Lock()
		delete(h.server.pending, req.ID)
		h.server.mu.Unlock()
		return agent.DecisionDeny, ctx.Err()
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
