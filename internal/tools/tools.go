// Package tools implements the harness's built-in tools and the registry
// the agent loop dispatches through. Schemas are hand-written literals:
// at six tools, reflection buys nothing and inlined schemas avoid the
// $ref-output problem LLM APIs have with generated ones.
package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/KonMam/tether/internal/provider"
)

// Result is a tool's output. Content is what the model sees; Diff is an
// optional unified diff for the UI when the tool changed a file.
type Result struct {
	Content string
	Diff    string
}

// Tool is one capability exposed to the model.
type Tool interface {
	Def() provider.ToolDef
	// Safe tools run without user approval (read-only operations).
	Safe() bool
	Run(ctx context.Context, args json.RawMessage) (Result, error)
}

// LongRunning marks tools whose execution may legitimately outlive a
// single model round: bash commands (tests, builds, installs) run for
// minutes. The agent gives such a tool a grace period; past it, the
// turn keeps going with a placeholder result and the real result is
// delivered at a later round instead of blocking the loop.
type LongRunning interface {
	Tool
	// LongRunning is a marker: presence on the interface is the whole
	// contract.
	LongRunning()
}

// NeverBackground is implemented by a LongRunning tool that wants to
// decline the background path for a specific call. The agent checks it
// before backgrounding: a call that must run inline dies at its own
// timeout instead of moving off the loop. Bash uses it for commands
// that start with sleep: a sleep that outlives the grace period is
// never useful, only noisy.
type NeverBackground interface {
	// NeverBackground reports whether the call must run inline instead
	// of being backgrounded past the grace period.
	NeverBackground(args json.RawMessage) bool
}

// Registry holds tools in a stable order (the order they're presented to
// the model and listed in the UI).
type Registry struct {
	order []string
	tools map[string]Tool
	// sink is the optional spill destination for oversized tool output
	// (see SpillSink). Set by the server when it builds a session.
	sink SpillSink
}

func NewRegistry(ts ...Tool) *Registry {
	r := &Registry{tools: map[string]Tool{}}
	for _, t := range ts {
		name := t.Def().Name
		r.order = append(r.order, name)
		r.tools[name] = t
	}
	return r
}

// Close releases whatever the registry's tools hold beyond memory:
// background processes, chiefly. Called when a session detaches.
func (r *Registry) Close() {
	for _, t := range r.tools {
		if c, ok := t.(interface{ Close() }); ok {
			c.Close()
		}
	}
}

// SetSpillSink hands the registry the session's spill destination. Called
// by the server when it builds a session; without it, oversized results
// are plain-truncated and read_spill is absent.
func (r *Registry) SetSpillSink(s SpillSink) {
	r.sink = s
	if s != nil && r.tools["read_spill"] == nil {
		r.order = append(r.order, "read_spill")
		r.tools["read_spill"] = &ReadSpill{r: r}
	}
}

// CapResult applies the registry's output policy to one tool result. With
// a spill sink, output over the cap is saved in full and the inline
// result is truncated (task-2 tail bias) plus a locator line; without
// one, plain truncation stands.
func (r *Registry) CapResult(res Result) Result {
	if len(res.Content) <= outputCap || r.sink == nil {
		res.Content = truncate(res.Content)
		return res
	}
	id, err := r.sink.Save(res.Content)
	inline := truncate(res.Content)
	if err != nil {
		inline += fmt.Sprintf("\n[warning: could not spill full output: %v]", err)
	} else {
		inline += fmt.Sprintf("\n[full output: spill %s, %d characters. Read it with the read_spill tool.]",
			id, len(res.Content))
	}
	res.Content = inline
	return res
}

// ReadSpill retrieves a saved spill file through the registry's sink.
// Read-only and safe: it never writes, and the sink confines reads to the
// calling session's spill directory.
type ReadSpill struct {
	r *Registry
}

func (t *ReadSpill) Safe() bool { return true }

func (t *ReadSpill) Def() provider.ToolDef {
	return provider.ToolDef{
		Name:        "read_spill",
		Description: "Read part of a spilled tool result (full output of a tool call that exceeded the inline cap). id is the spill number from a '[full output: spill N ...]' line; offset and limit are in characters (default limit 20000).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "The spill number from the locator line.",
				},
				"offset": map[string]any{
					"type":        "integer",
					"description": "Character offset to start reading from (default 0).",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum characters to read (default 20000).",
				},
			},
			"required": []string{"id"},
		},
	}
}

func (t *ReadSpill) Run(_ context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		ID     string `json:"id"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := decode(args, &in); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(in.ID) == "" {
		return Result{}, fmt.Errorf("id is required")
	}
	if t.r == nil || t.r.sink == nil {
		return Result{Content: "no spill storage is configured for this session"}, nil
	}
	if in.Limit <= 0 {
		in.Limit = 20000
	}
	text, err := t.r.sink.Read(in.ID, in.Offset, in.Limit)
	if err != nil {
		return Result{}, err
	}
	return Result{Content: text}, nil
}

// QuestionRequest is a question the ask_user tool posed to the user: the
// tool blocks until an answer arrives through the answerer, or the turn
// is interrupted.
type QuestionRequest struct {
	ID       string   `json:"id"`
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}

// Answerer resolves the ask_user tool's blocking question. The server
// implements it with a browser round-trip like approvals; headless mode
// answers with the interrupt text.
type Answerer interface {
	// AskQuestion blocks until the user answers, ctx is cancelled, or the
	// question is interrupted. It returns the answer text.
	AskQuestion(ctx context.Context, req QuestionRequest) (string, error)
}

// AskUser pauses the turn and asks the user a real question: which of
// two approaches, confirm a destructive step, pick a target. Safe by
// definition (asking is never a side effect), so it skips the approval
// gate. The answer comes back as the tool result and the model continues
// the same turn.
type AskUser struct {
	// Answerer bridges to the UI (like approvals); nil when running
	// without a human (headless, tests), in which case the answer is the
	// interrupt text.
	Answerer Answerer
}

func (t *AskUser) Safe() bool { return true }

func (t *AskUser) Def() provider.ToolDef {
	return provider.ToolDef{
		Name: "ask_user",
		Description: "Pause the turn and ask the user a question with optional answer options. " +
			"Use it when you need a decision before acting: which of two approaches, confirmation of a destructive step, " +
			"or a target to aim at. The user's answer is returned to you and the turn continues.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question": map[string]any{
					"type":        "string",
					"description": "The question to ask, in plain words.",
				},
				"options": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Optional answer options shown as buttons.",
				},
			},
			"required": []string{"question"},
		},
	}
}

// InterruptAnswer is what the turn's cancellation resolves a pending
// question with: the model sees that the user interrupted instead of
// answering.
const InterruptAnswer = "the user interrupted instead of answering"

func (t *AskUser) Run(ctx context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Question string   `json:"question"`
		Options  []string `json:"options"`
	}
	if err := decode(args, &in); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(in.Question) == "" {
		return Result{}, fmt.Errorf("question is required")
	}
	if t.Answerer == nil {
		return Result{Content: InterruptAnswer}, nil
	}
	answer, err := t.Answerer.AskQuestion(ctx, QuestionRequest{
		ID:       newID(),
		Question: in.Question,
		Options:  in.Options,
	})
	if err != nil {
		// The turn was interrupted (Esc); the user did not answer. The
		// interrupt answer lands as the tool result, not a crash.
		if ctx.Err() != nil {
			return Result{Content: InterruptAnswer}, nil
		}
		return Result{}, err
	}
	if answer == "" {
		answer = InterruptAnswer
	}
	return Result{Content: answer}, nil
}

// newID is a shared tiny id generator for question ids. AskUser lives in
// the tools package while approvals generate ids in the agent package;
// both are opaque strings to the server.
func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// SessionHit is one search result: which session matched, and a snippet
// of the matching message.
type SessionHit struct {
	SessionID string
	Title     string
	Workdir   string
	Snippet   string
}

// SessionSearcher finds past sessions whose transcripts match a query.
// The server implements it over the session store (the tools package must
// not import the store); without one, session_search explains that it is
// unavailable.
type SessionSearcher interface {
	SearchSessions(query string, limit int) ([]SessionHit, error)
}

// SessionSearch recalls what past sessions did: "did we already fix this
// bug", "how did we set up X". Read-only and safe.
type SessionSearch struct {
	Searcher SessionSearcher
}

func (t *SessionSearch) Safe() bool { return true }

func (t *SessionSearch) Def() provider.ToolDef {
	return provider.ToolDef{
		Name: "session_search",
		Description: "Search past sessions' transcripts for a query and return up to 10 hits as title, session id, and snippet. " +
			"Use it to recall what earlier sessions did: whether a bug was already fixed, how something was set up, " +
			"what was decided about a topic.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Text to search for in past sessions' transcripts.",
				},
			},
			"required": []string{"query"},
		},
	}
}

func (t *SessionSearch) Run(_ context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Query string `json:"query"`
	}
	if err := decode(args, &in); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(in.Query) == "" {
		return Result{Content: "a query is required; describe what you want to recall, for example 'did we fix the dropdown bug'"}, nil
	}
	if t.Searcher == nil {
		return Result{Content: "session search is not available in this context"}, nil
	}
	hits, err := t.Searcher.SearchSessions(in.Query, 10)
	if err != nil {
		return Result{}, err
	}
	if len(hits) == 0 {
		return Result{Content: fmt.Sprintf("no past sessions mention %q", in.Query)}, nil
	}
	var sb strings.Builder
	for _, h := range hits {
		fmt.Fprintf(&sb, "%s | %s | %s\n", h.Title, h.SessionID, h.Snippet)
	}
	return Result{Content: strings.TrimSpace(sb.String())}, nil
}

// Add registers an extra tool (module-contributed). Last write wins on
// name collision, which lets a module deliberately override a built-in.
func (r *Registry) Add(t Tool) {
	name := t.Def().Name
	if _, exists := r.tools[name]; !exists {
		r.order = append(r.order, name)
	}
	r.tools[name] = t
}

func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

func (r *Registry) Defs() []provider.ToolDef {
	defs := make([]provider.ToolDef, 0, len(r.order))
	for _, name := range r.order {
		defs = append(defs, r.tools[name].Def())
	}
	return defs
}

// Default returns the standard v1 tool set rooted at workdir, in the
// same order the server's module assembly presents them (see
// internal/module/core). Headless mode and the subagent "all" policy use
// this; the server builds the same surface from the core modules so the
// tool set has a single definition.
func Default(workdir string) *Registry {
	w := Workdir(workdir)
	jm := newJobManager()
	return NewRegistry(
		&ReadFile{W: w},
		&WriteFile{W: w},
		&EditFile{W: w},
		&Bash{Dir: workdir},
		&BashBackground{Dir: workdir, jobTools: jobTools{Manager: jm}},
		&BashOutput{jobTools: jobTools{Manager: jm}},
		&BashWait{jobTools: jobTools{Manager: jm}},
		&BashKill{jobTools: jobTools{Manager: jm}},
		&Grep{Dir: workdir},
		&Glob{Dir: workdir},
		&AskUser{},
	)
}

// FileTools returns the core file tools (read, write, edit), rooted at// workdir, in the built-in registry's order. Exported for the core
// module; tools.Default assembles the same set.
func FileTools(workdir string) []Tool {
	w := Workdir(workdir)
	return []Tool{
		&ReadFile{W: w},
		&WriteFile{W: w},
		&EditFile{W: w},
	}
}

// GrepTools returns the core search tools (grep, glob), rooted at
// workdir, in the built-in registry's order. Exported for the core
// module; tools.Default assembles the same set.
func GrepTools(workdir string) []Tool {
	return []Tool{
		&Grep{Dir: workdir},
		&Glob{Dir: workdir},
	}
}

// BashTools returns the core shell tools (bash plus the background job
// surface) sharing one job manager. Exported for the core module; the
// built-in registry (tools.Default) assembles the same set.
func BashTools(workdir string) []Tool {
	jm := newJobManager()
	return []Tool{
		&Bash{Dir: workdir},
		&BashBackground{Dir: workdir, jobTools: jobTools{Manager: jm}},
		&BashOutput{jobTools: jobTools{Manager: jm}},
		&BashWait{jobTools: jobTools{Manager: jm}},
		&BashKill{jobTools: jobTools{Manager: jm}},
	}
}

// AskUserTool returns a fresh ask_user tool. Exported for the core
// module; the server wires its answerer to the browser bridge.
func AskUserTool() *AskUser { return &AskUser{} }

// TaskStore is the per-session bridge the tasks module's tools need to
// reach the folded task list. The tools live in a different package
// than the server and cannot know their session id, so the server
// implements this and hands it over at attach time. The Write payload
// is the same []TaskItem shape the module and the agent event carry,
// defined here as a plain struct so the tools package needs no agent
// import.
type TaskItem struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

type TaskStore interface {
	// Write emits an EventTasks with the full list (persisting and
	// broadcasting it) and folds it into the session state.
	Write(tasks []TaskItem) error
	// Read returns the session's current folded task list.
	Read() []TaskItem
}

// TaskStoreSetter lets a tool accept the server-provided task store
// bridge. todo_write and todo_read implement it; the registry checks
// for the interface so it never needs to know the tools' concrete
// types.
type TaskStoreSetter interface {
	SetStore(TaskStore)
}

// SetAnswerer hands the registry's ask_user tool its bridge to the UI.
// Called by the server when it builds a session; without an answerer the
// tool answers with the interrupt text (headless, tests).
func (r *Registry) SetAnswerer(a Answerer) {
	if t, ok := r.tools["ask_user"]; ok {
		if ask, ok := t.(*AskUser); ok {
			ask.Answerer = a
		}
	}
}

// SetTaskStore hands the registry's todo_write and todo_read tools
// their per-session bridge to the folded task list. Called by the
// server when it builds a session; without one the tools say the tasks
// module is unavailable.
func (r *Registry) SetTaskStore(store TaskStore) {
	for _, name := range []string{"todo_write", "todo_read"} {
		if t, ok := r.tools[name]; ok {
			if tw, ok := t.(TaskStoreSetter); ok {
				tw.SetStore(store)
			}
		}
	}
}

// SetSessionSearcher hands the registry's session_search tool its bridge
// to the session store. Called by the server when it builds a session;
// without one the tool explains that session search is unavailable.
func (r *Registry) SetSessionSearcher(searcher SessionSearcher) {
	if t, ok := r.tools["session_search"]; ok {
		if ss, ok := t.(*SessionSearch); ok {
			ss.Searcher = searcher
		}
	}
}

// Workdir confines file-tool paths to a root directory. Relative paths
// resolve against it; anything escaping it is rejected. This is a
// correctness fence against model mistakes, not a sandbox; bash is the
// hole, and the permission gate covers it.
type Workdir string

func (w Workdir) Resolve(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("path is required")
	}
	abs := string(w)
	if !filepath.IsAbs(p) {
		p = filepath.Join(abs, p)
	}
	p = filepath.Clean(p)
	if p != abs && !strings.HasPrefix(p, abs+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the working directory", p)
	}
	return p, nil
}

const outputCap = 48 * 1024

// SpillSink is where oversized tool output goes when the registry has one
// configured. The tools package must not know about sessions, so the
// server implements this against the session's spill directory and hands
// it to the registry. When no sink is set, results are plain-truncated.
type SpillSink interface {
	// Save writes the full text to the next spill file and returns its
	// id (the sequence number, no path separators).
	Save(text string) (id string, err error)
	// Read returns a character slice of one spill file. offset past the
	// end yields a clear message rather than an empty result. An id with
	// path separators is rejected.
	Read(id string, offset, limit int) (string, error)
}

// truncate caps tool output before it enters the transcript. Test runs,
// builds, and logs fail at the end, so the budget is tail-biased: the
// first quarter of the cap and the last three quarters are kept, with a
// marker that states how much was cut so the model knows content is
// missing.
func truncate(s string) string {
	if len(s) <= outputCap {
		return s
	}
	head := outputCap / 4
	tail := outputCap - head
	omitted := len(s) - head - tail
	marker := fmt.Sprintf("\n[... %d characters omitted ...]\n", omitted)
	return s[:head] + marker + s[len(s)-tail:]
}

func decode(args json.RawMessage, into any) error {
	dec := json.NewDecoder(strings.NewReader(string(args)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}
