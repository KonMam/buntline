// Package agent implements the harness loop: send the transcript, stream
// the reply, execute requested tools behind the permission gate, feed
// results back, repeat until the model stops or the round cap trips.
//
// The package is UI-agnostic. It emits Events through a callback; the web
// server, the activity log, and headless mode are all just consumers.
package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/KonMam/buntline/internal/provider"
	"github.com/KonMam/buntline/internal/tools"
)

// MaxRounds caps model→tools→model iterations per user turn. It is a
// runaway backstop, not a working budget: the loop detector catches true
// loops, and a long multi-task turn legitimately uses dozens of rounds.
const MaxRounds = 100

// ToolInterceptor observes and can shape tool execution. Hooks,
// checkpoints, and diagnostics are all interceptors; the loop itself
// stays ignorant of what they do, but never of THAT they did it: every
// non-empty note lands in the event stream. Invisible policy is
// untrustworthy policy.
type ToolInterceptor interface {
	// Name identifies the interceptor in the trace.
	Name() string
	// BeforeTool runs before an approved tool call. A non-empty note is
	// logged to the trace; a non-nil error blocks the call and becomes
	// the tool result the model sees.
	BeforeTool(ctx context.Context, call provider.ToolCall) (note string, err error)
	// AfterTool runs after execution. A non-empty return is appended to
	// the tool result (e.g. diagnostics for the file just edited) and
	// logged to the trace.
	AfterTool(ctx context.Context, call provider.ToolCall, result tools.Result, runErr error) string
}

type Config struct {
	Provider     provider.Provider
	Model        string
	Tools        *tools.Registry
	Approver     Approver
	SystemPrompt string
	Interceptors []ToolInterceptor
	// MaxRounds caps model calls per turn; 0 uses the package default.
	MaxRounds int
	// ContextWindow reports the model's context window in tokens (0 =
	// unknown). A function, not a value: the window follows a mid-session
	// model switch. When the last prompt crosses 85% of it, the loop
	// compacts between rounds.
	ContextWindow func() int
	// Emit receives every event, in order, synchronously. Consumers that
	// need to fan out or buffer do it on their side.
	Emit func(Event)
}

type Agent struct {
	cfg Config

	mu         sync.Mutex
	transcript []provider.Message
	// allowedForSession holds tool names the user approved with
	// "allow for session".
	allowedForSession map[string]bool
	// pendingGrants holds turn-scoped tool grants from a skill's
	// allowed-tools frontmatter. Set by the server before the turn
	// starts, consumed and cleared at the turn's start, checked by
	// prepareTool before the approval gate. A grant lives for exactly
	// one turn and never crosses sessions.
	pendingGrants []string
	// turnGrants is the active grant set for the current turn, taken
	// from pendingGrants at turn start.
	turnGrants map[string]bool
	busy       bool
	// turnID stamps every event emitted during the current Run/Compact.
	// Written only while holding the busy slot, so unsynchronized reads
	// from emit (same goroutine) are fine.
	turnID string
	// steer holds user messages sent while a turn is running; the loop
	// delivers them at its next step instead of rejecting them.
	steer chan provider.Message
	// steerSignal wakes a loop parked in waitForActivity when a steering
	// message arrives (buffered: the wake is a hint, never a loss).
	steerSignal chan struct{}
	// Backgrounded tool execution: a LongRunning tool (bash) that has not
	// finished within the grace period keeps running off the loop, its
	// real result delivered at a later round so the turn is never
	// blocked on one command. Guarded by bgMu; only the loop goroutine
	// appends delivered results to the transcript.
	bgMu          sync.Mutex
	bgOutstanding map[string]int // running off-loop tools, per turn id
	bgResults     []bgDelivery   // completed results awaiting delivery
	bgSignal      chan struct{}  // buffered 1: a result landed
	// lastPrompt is the latest model call's prompt size in tokens.
	// Written and read only by the loop goroutine.
	lastPrompt int
}

// bgDelivery is one completed backgrounded tool result: the transcript
// message plus the tool_end event, emitted together from the loop
// goroutine so the trace stays ordered and TurnID stays race-free. The
// turn stamp is checked at delivery: a tool backgrounded in a previous
// turn (interrupted or ended) must not deliver its result into a newer
// turn's transcript.
type bgDelivery struct {
	out    toolOutcome
	turnID string
}

// toolOutcome is one tool execution's result: the transcript message,
// the tool_end event, and any interceptor notes that must be emitted
// before it. The caller emits everything (dispatchTool inline, the
// background delivery path later), so every emit happens on the loop
// goroutine in order.
type toolOutcome struct {
	msg   provider.Message
	ev    Event
	notes []Event
}

// bgGrace is how long a LongRunning tool may run before the harness
// backgrounds it and lets the turn continue. A var, not a const, so
// tests can shorten it.
var bgGrace = 1 * time.Second

// maxBgTools caps how many tools may run backgrounded at once; beyond
// it, further long tools wait inline rather than pile on.
const maxBgTools = 4

func New(cfg Config) *Agent {
	a := &Agent{cfg: cfg, allowedForSession: map[string]bool{}, turnGrants: map[string]bool{}, steer: make(chan provider.Message, 8), steerSignal: make(chan struct{}, 1), bgSignal: make(chan struct{}, 1), bgOutstanding: map[string]int{}}
	if cfg.SystemPrompt != "" {
		a.transcript = append(a.transcript, provider.Message{
			Role: provider.RoleSystem, Content: cfg.SystemPrompt,
		})
	}
	return a
}

// Messages returns a copy of the transcript (for persistence and the UI).
func (a *Agent) Messages() []provider.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]provider.Message, len(a.transcript))
	copy(out, a.transcript)
	return out
}

// SetModel switches the model for subsequent calls. Rejected mid-turn:
// a transcript built by one model finishing under another is fine, but
// switching inside a tool loop invites half-understood context.
func (a *Agent) SetModel(model string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return ErrBusy
	}
	a.cfg.Model = model
	return nil
}

// SetProvider switches the backend (profile change). Same busy rule.
func (a *Agent) SetProvider(p provider.Provider) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return ErrBusy
	}
	a.cfg.Provider = p
	return nil
}

// Provider returns the current backend. Anything that spawns work on the
// session's behalf (subagents) must read this live rather than capture a
// provider at construction: SetProvider retargets the session, and a held
// copy silently keeps calling the old backend with the old key.
func (a *Agent) Provider() provider.Provider {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.Provider
}

// Model returns the current model. Same live-read rule as Provider.
func (a *Agent) Model() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.Model
}

// SetSystemPrompt replaces the system message. Rejected mid-turn; the
// next model call re-evaluates the whole prefix (cache cost is real and
// will show in the context meter).
func (a *Agent) SetSystemPrompt(prompt string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return ErrBusy
	}
	if len(a.transcript) > 0 && a.transcript[0].Role == provider.RoleSystem {
		if prompt == "" {
			a.transcript = a.transcript[1:]
		} else {
			a.transcript[0].Content = prompt
		}
	} else if prompt != "" {
		a.transcript = append([]provider.Message{{Role: provider.RoleSystem, Content: prompt}}, a.transcript...)
	}
	return nil
}

// SetMessages replaces the transcript (session resume). Resume is where
// a transcript written by an older, buggier build comes back, so it is
// repaired on the way in: a session already holding an invalid tool
// pairing heals on its next load instead of failing every turn forever.
func (a *Agent) SetMessages(msgs []provider.Message) {
	msgs, _ = provider.RepairToolPairing(msgs)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.transcript = make([]provider.Message, len(msgs))
	copy(a.transcript, msgs)
}

func (a *Agent) emit(ev Event) {
	ev.Time = time.Now()
	ev.TurnID = a.turnID
	if a.cfg.Emit != nil {
		a.cfg.Emit(ev)
	}
}

func (a *Agent) append(m provider.Message) {
	a.mu.Lock()
	a.transcript = append(a.transcript, m)
	a.mu.Unlock()
	a.emit(Event{Type: EventMessage, Message: &m})
}

// tryAcquire marks the agent busy; one run at a time per session.
func (a *Agent) tryAcquire() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return false
	}
	a.busy = true
	return true
}

func (a *Agent) release() {
	a.mu.Lock()
	a.busy = false
	a.mu.Unlock()
}

var ErrBusy = fmt.Errorf("agent is already running a turn")

// Busy reports whether a turn is in flight.
func (a *Agent) Busy() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.busy
}

// TurnID returns the running turn's id, for events built outside the
// loop (the ask_user bridge). Meaningful only while Busy.
func (a *Agent) TurnID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.turnID
}

// Steer queues a user message for delivery at the running turn's next
// step. The message enters the transcript when the loop picks it up.
func (a *Agent) Steer(text string) error {
	return a.SteerMessage(provider.Message{Role: provider.RoleUser, Content: text})
}

// SteerMessage is Steer for a fully-formed user message (e.g. with images).
func (a *Agent) SteerMessage(m provider.Message) error {
	if !a.Busy() {
		return ErrNotRunning
	}
	m.Role = provider.RoleUser
	select {
	case a.steer <- m:
		// Wake a loop parked in waitForActivity: the steer must not wait
		// for whatever long tool is running to finish before the model
		// hears it.
		select {
		case a.steerSignal <- struct{}{}:
		default:
		}
		return nil
	default:
		return fmt.Errorf("steering queue is full")
	}
}

var ErrNotRunning = fmt.Errorf("no turn is running")

// drainSteer appends any queued steering messages to the transcript and
// reports how many arrived.
func (a *Agent) drainSteer() int {
	n := 0
	for {
		select {
		case m := <-a.steer:
			a.append(m)
			n++
		default:
			return n
		}
	}
}

// Run executes one user turn to completion. It returns when the model
// produces a reply with no tool calls, the round cap trips, ctx is
// cancelled, or the provider fails.
func (a *Agent) Run(ctx context.Context, userText string) error {
	return a.RunMessage(ctx, provider.Message{Role: provider.RoleUser, Content: userText})
}

// RunMessage is Run for a fully-formed user message (e.g. with images).
func (a *Agent) RunMessage(ctx context.Context, user provider.Message) error {
	user.Role = provider.RoleUser
	return a.RunMessages(ctx, []provider.Message{user})
}

// RunMessages starts a turn from several prepared messages: the user
// message plus harness-built context such as @-mention attachments,
// which enter the transcript as a synthetic read_file exchange (an
// assistant tool call and its result) so pinned content is
// indistinguishable from content the model read itself.
func (a *Agent) RunMessages(ctx context.Context, msgs []provider.Message) error {
	if len(msgs) == 0 {
		return fmt.Errorf("no messages")
	}
	if !a.tryAcquire() {
		return ErrBusy
	}
	defer a.release()
	a.turnID = newID()
	// Turn-scoped skill grants apply to exactly this turn: take them
	// now, then clear so nothing leaks into the next turn even if a
	// backgrounded tool outlives this one.
	a.mu.Lock()
	turnGrants := map[string]bool{}
	for _, g := range a.pendingGrants {
		turnGrants[g] = true
	}
	a.pendingGrants = nil
	a.turnGrants = turnGrants
	a.mu.Unlock()
	// Backgrounded tool results from a previous turn are stale by
	// definition (their task context died with that turn); drop them so
	// a new turn starts from a clean board.
	a.bgMu.Lock()
	a.bgResults = a.bgResults[:0]
	a.bgMu.Unlock()

	for _, m := range msgs {
		a.append(m)
	}
	a.emit(Event{Type: EventTurnStart, Text: strings.Join(a.toolNames(), ", ")})

	maxRounds := a.cfg.MaxRounds
	if maxRounds <= 0 {
		maxRounds = MaxRounds
	}
	nudged := false
	loops := newLoopDetector()
	for round := 0; round < maxRounds; round++ {
		msg, err := a.streamOnce(ctx, round)
		if err != nil {
			if ctx.Err() != nil {
				a.emit(Event{Type: EventTurnEnd, StopReason: "cancelled"})
				return ctx.Err()
			}
			a.emit(Event{Type: EventError, Error: err.Error()})
			a.emit(Event{Type: EventTurnEnd, StopReason: "error"})
			return err
		}
		a.append(msg)

		if len(msg.ToolCalls) == 0 {
			// Steering messages that arrived during generation keep the
			// turn alive: deliver them and let the model respond.
			if a.drainSteer() > 0 {
				continue
			}
			// A backgrounded tool's result landed while the model was
			// answering: deliver it and let the model incorporate it
			// before ending the turn: the placeholder promised the
			// result, so the turn owes it.
			if a.deliverBgResults() {
				continue
			}
			// The model answered while a backgrounded tool is STILL
			// running. The answer is real, but the placeholder's promise
			// is not yet fulfilled; park until the result lands or the
			// user steers, then deliver it before ending.
			if a.parked() {
				if !a.waitForActivity(ctx) {
					a.emit(Event{Type: EventTurnEnd, StopReason: "cancelled"})
					return ctx.Err()
				}
				continue
			}
			// Models sometimes stop with no answer at all: reasoning-heavy
			// ones spend the whole reply thinking, and others emit an
			// empty message right after a tool batch, abandoning the task
			// mid-way. Nudge once (visibly, in the transcript) before
			// accepting silence.
			if msg.Content == "" && !nudged {
				nudged = true
				a.append(provider.Message{
					Role:    provider.RoleUser,
					Content: "Your previous reply contained no answer. If the task is not finished, continue working on it now; otherwise state your final answer.",
				})
				continue
			}
			a.emit(Event{Type: EventTurnEnd, StopReason: "done"})
			return nil
		}
		// Calls that need no approval (safe, or granted for the session)
		// run concurrently; this is what lets several subagents work at
		// once. Anything needing a human answer runs sequentially so the
		// approval cards arrive one at a time.
		results := make([]provider.Message, len(msg.ToolCalls))
		if len(msg.ToolCalls) > 1 && a.preApproved(msg.ToolCalls) {
			var wg sync.WaitGroup
			sem := make(chan struct{}, 4)
			for i, call := range msg.ToolCalls {
				wg.Add(1)
				go func() {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					results[i] = a.dispatchTool(ctx, call)
				}()
			}
			wg.Wait()
			for _, r := range results {
				a.append(r)
			}
		} else {
			for i, call := range msg.ToolCalls {
				results[i] = a.dispatchTool(ctx, call)
				a.append(results[i])
				if ctx.Err() != nil {
					break
				}
			}
		}
		// A model repeating the same call with the same outcome is stuck;
		// stop the turn rather than burn the round budget.
		for i, call := range msg.ToolCalls {
			if loops.record(call, results[i].Content) {
				a.emit(Event{Type: EventError, Error: "stopped: the model repeated the same tool call with the same result more than five times"})
				a.emit(Event{Type: EventTurnEnd, StopReason: "loop_detected"})
				return nil
			}
		}
		if ctx.Err() != nil {
			a.emit(Event{Type: EventTurnEnd, StopReason: "cancelled"})
			return ctx.Err()
		}
		// Steering arrivals slot in after the tool batch, before the next
		// model call; the model sees results and correction together.
		a.drainSteer()

		// Backgrounded tools get a chance to deliver before the next
		// model call; results that are still running have their
		// placeholder already in the transcript, so the model works
		// around them.
		if a.deliverBgResults() {
			continue
		}

		// Mid-turn compaction: a long tool loop is exactly when the
		// window fills, and waiting for the turn to end means it never
		// happens. Compact between rounds once the last prompt crosses
		// the threshold; the summary carries the in-flight task forward.
		if a.cfg.ContextWindow != nil {
			if w := a.cfg.ContextWindow(); w > 0 && a.lastPrompt > w*85/100 {
				if err := a.compactInline(ctx); err != nil {
					a.emit(Event{Type: EventError, Error: "auto-compaction failed: " + err.Error()})
				} else {
					a.lastPrompt = 0
				}
			}
		}

		// All tool results are in and the turn would otherwise move to
		// the next model call. If a long-running tool is still working
		// (its placeholder is in the transcript), park the loop: the
		// next model call happens when the result lands or the user
		// steers. Parking means a `go test` run does not keep the model
		// spinning, and a steer delivered while parked is answered
		// immediately, because it wakes the loop.
		if a.parked() {
			if !a.waitForActivity(ctx) {
				a.emit(Event{Type: EventTurnEnd, StopReason: "cancelled"})
				return ctx.Err()
			}
			continue
		}
	}
	// Ending at the cap without a word reads as the model going silent;
	// say what happened where the user is looking.
	a.emit(Event{Type: EventError, Error: fmt.Sprintf("stopped: this turn reached the %d-round cap; send a message to continue", maxRounds)})
	a.emit(Event{Type: EventTurnEnd, StopReason: "max_rounds"})
	return nil
}

// parked reports whether the loop is waiting on backgrounded tools: at
// least one from THIS turn is still running and none have completed
// since the last delivery point. Called from the loop goroutine.
func (a *Agent) parked() bool {
	a.bgMu.Lock()
	defer a.bgMu.Unlock()
	return a.bgOutstanding[a.turnID] > 0 && len(a.bgResults) == 0
}

// waitForActivity parks the loop until a backgrounded tool delivers,
// the user steers, or the turn is cancelled. Returning false means the
// turn should end cancelled.
func (a *Agent) waitForActivity(ctx context.Context) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case <-a.bgSignal:
			if a.deliverBgResults() {
				return true
			}
			// Spurious wake (a result was already drained): park again.
		case <-a.steerSignal:
			if a.drainSteer() > 0 {
				return true
			}
		}
	}
}

// deliverBgResults appends every completed backgrounded tool result to
// the transcript (and emits its tool_end event, so the trace shows the
// result where the loop picked it up) and reports whether any arrived.
// Runs on the loop goroutine only. Results stamped for a turn that is no
// longer current (a previous turn's tool finished late) are dropped:
// their context died with that turn, so the result is stale by
// definition.
func (a *Agent) deliverBgResults() bool {
	a.bgMu.Lock()
	if len(a.bgResults) == 0 {
		a.bgMu.Unlock()
		return false
	}
	results := a.bgResults
	a.bgResults = nil
	a.bgMu.Unlock()

	delivered := false
	for _, d := range results {
		if d.turnID != a.turnID {
			continue
		}
		delivered = true
		for _, n := range d.out.notes {
			a.emit(n)
		}
		a.append(bgResultMessage(d.out))
		a.emit(d.out.ev)
	}
	return delivered
}

// bgResultMessage turns a backgrounded tool's real result into a
// transcript message. It must not stay a RoleTool message: the
// placeholder returned at grace time already answered that
// tool_call_id, and a second tool message for the same id is precisely
// the shape OpenAI-compatible backends reject, which costs the whole
// session, not one turn, because the bad prefix replays forever after.
// The result arrives as a user message instead, tagged so the UI
// collapses it (the tool card already shows the output) and so the model
// reads it as the promised result rather than a new instruction.
func bgResultMessage(out toolOutcome) provider.Message {
	if out.msg.Role != provider.RoleTool {
		return out.msg
	}
	name := out.ev.ToolName
	if name == "" {
		name = "tool"
	}
	return provider.Message{
		Role:    provider.RoleUser,
		Kind:    "background",
		Content: "[background " + name + " finished]\n" + out.msg.Content,
	}
}

// dispatchTool sends one approved tool call through execution. Approval
// and repair happen first, on the calling goroutine, so a slow approval
// never backgrounds an unapproved tool; then tool_start is emitted; then
// a LongRunning tool gets the grace period (backgrounding past it) and
// everything else runs inline. The tool_end event is emitted exactly
// once (inline on the calling goroutine, or from the loop goroutine
// when a backgrounded result is delivered), so the emit order matches
// the transcript's append order.
func (a *Agent) dispatchTool(ctx context.Context, call provider.ToolCall) provider.Message {
	msg, err := a.prepareTool(ctx, &call)
	if err != nil {
		return msg
	}
	a.emit(Event{Type: EventToolStart, ToolID: call.ID, ToolName: call.Name, ToolArgs: call.Args})
	if tool, ok := a.cfg.Tools.Get(call.Name); ok {
		if _, long := tool.(tools.LongRunning); long {
			// A call that declines backgrounding (a bash command that
			// starts with sleep) runs inline and dies at its own
			// timeout; everything else gets the grace period.
			if nb, ok := tool.(tools.NeverBackground); ok && nb.NeverBackground(json.RawMessage(call.Args)) {
				out := a.executeTool(ctx, call)
				a.emitToolOutcome(out)
				return out.msg
			}
			return a.startBg(ctx, call)
		}
	}
	out := a.executeTool(ctx, call)
	a.emitToolOutcome(out)
	return out.msg
}

// startBg runs a LongRunning tool off the loop: the grace-period attempt
// happens synchronously (fast tools return their real result inline and
// never leave the loop); when it exceeds the grace, the tool is
// backgrounded and a placeholder result is returned instead. The real
// result lands in bgResults for a later round via deliverBgResults.
func (a *Agent) startBg(ctx context.Context, call provider.ToolCall) provider.Message {
	fast, done := a.runToolBg(ctx, call)
	if done {
		return fast
	}
	// The tool is still running; background it and deliver the real
	// result when it lands.
	placeholder := provider.Message{
		Role:       provider.RoleTool,
		Content:    "[started " + call.Name + "; working in the background; you may continue with other work and I will report the result when it finishes]",
		ToolCallID: call.ID,
	}
	a.emit(Event{Type: EventToolBg, ToolID: call.ID, ToolName: call.Name})
	return placeholder
}

// runToolBg runs one LongRunning tool with a grace period. done=true
// means the result is final and should be returned inline; done=false
// means the tool is still running and the caller must background it.
// The tool runs through executeTool, which emits nothing; the caller
// owns the tool_start/tool_end events, so they land on the loop
// goroutine in order.
func (a *Agent) runToolBg(ctx context.Context, call provider.ToolCall) (provider.Message, bool) {
	// Honor the background budget: beyond the cap, a long tool waits its
	// turn inline rather than piling unbounded work onto the machine.
	a.bgMu.Lock()
	if a.bgOutstanding[a.turnID] >= maxBgTools {
		a.bgMu.Unlock()
		out := a.executeTool(ctx, call)
		a.emitToolOutcome(out)
		return out.msg, true
	}
	a.bgOutstanding[a.turnID]++
	turn := a.turnID
	a.bgMu.Unlock()

	// The tool keeps the turn's context: interrupting the turn stops
	// every backgrounded command with it (the bash tool already kills
	// its process group on ctx cancellation). The derived context is
	// cancelled when the tool's goroutine finishes, NOT when runToolBg
	// returns: the grace path returns while the tool is still running,
	// and cancelling here would kill it immediately. The loop's own ctx
	// needs no reference to gctx, so the goroutine's defer is the whole
	// lifecycle.
	gctx, cancel := context.WithCancel(ctx)
	done := make(chan toolOutcome, 1)
	go func() {
		defer cancel()
		done <- a.executeTool(gctx, call)
		close(done)
	}()

	select {
	case out := <-done:
		a.bgMu.Lock()
		if n := a.bgOutstanding[turn]; n <= 1 {
			delete(a.bgOutstanding, turn)
		} else {
			a.bgOutstanding[turn] = n - 1
		}
		a.bgMu.Unlock()
		a.emitToolOutcome(out)
		return out.msg, true
	case <-time.After(bgGrace):
		// Still running: backgrounded. The result arrives later; release
		// nothing yet: the slot is held until the tool ends, so the
		// outstanding count stays honest.
		go func() {
			out := <-done
			a.bgMu.Lock()
			if n := a.bgOutstanding[turn]; n <= 1 {
				delete(a.bgOutstanding, turn)
			} else {
				a.bgOutstanding[turn] = n - 1
			}
			a.bgMu.Unlock()
			a.bgMu.Lock()
			a.bgResults = append(a.bgResults, bgDelivery{out: out, turnID: turn})
			a.bgMu.Unlock()
			select {
			case a.bgSignal <- struct{}{}:
			default:
			}
		}()
		return provider.Message{}, false
	}
}

// emitToolOutcome emits a tool execution's notes and tool_end event in
// order. Called on the loop goroutine (inline execution or background
// delivery).
func (a *Agent) emitToolOutcome(out toolOutcome) {
	for _, n := range out.notes {
		a.emit(n)
	}
	a.emit(out.ev)
}

// streamOnce performs one model call, forwarding deltas as events and
// returning the accumulated assistant message. It brackets the call with
// model_start and usage events so the trace has a complete span.
func (a *Agent) streamOnce(ctx context.Context, round int) (provider.Message, error) {
	// Last line of defense: never put an invalid transcript on the wire.
	// A defect introduced mid-turn would otherwise 400 the request and,
	// once persisted, every request after it.
	msgs, repairs := provider.RepairToolPairing(a.Messages())
	for _, r := range repairs {
		a.emit(Event{Type: EventInterceptor, ToolName: "transcript", Text: "repaired invalid transcript: " + r})
	}
	req := provider.Request{
		Model:    a.cfg.Model,
		Messages: msgs,
		Tools:    a.cfg.Tools.Defs(),
	}
	a.emit(Event{Type: EventModelStart, Round: round})
	start := time.Now()

	events, err := a.cfg.Provider.Stream(ctx, req)
	if err != nil {
		return provider.Message{}, err
	}

	msg := provider.Message{Role: provider.RoleAssistant}
	var usage *provider.Usage
	var firstToken time.Duration
	for ev := range events {
		if firstToken == 0 && (ev.Kind == provider.EventTextDelta || ev.Kind == provider.EventThinkingDelta) {
			firstToken = time.Since(start)
		}
		switch ev.Kind {
		case provider.EventTextDelta:
			msg.Content += ev.Text
			a.emit(Event{Type: EventTextDelta, Text: ev.Text})
		case provider.EventThinkingDelta:
			msg.Thinking += ev.Text
			a.emit(Event{Type: EventThinkingDelta, Text: ev.Text})
		case provider.EventToolCalls:
			msg.ToolCalls = ev.ToolCalls
		case provider.EventUsage:
			usage = ev.Usage
		case provider.EventError:
			return provider.Message{}, ev.Err
		case provider.EventDone:
			// channel closes after this
		}
	}
	a.emit(Event{
		Type: EventUsage, Usage: usage, Round: round,
		DurationMs:   time.Since(start).Milliseconds(),
		FirstTokenMs: firstToken.Milliseconds(),
	})
	if usage != nil {
		a.lastPrompt = usage.PromptTokens
	}
	return msg, nil
}

// prepareTool resolves, repairs, and approves a tool call: everything
// that must happen before the grace period starts. It returns a result
// message plus errDoNotRun when the call must not execute (unknown tool,
// unrepaired args, denial, approval failure); on success the caller
// proceeds to executeTool. Only the loop goroutine calls it, so the
// approval round-trip and its events are serialized with the loop.
func (a *Agent) prepareTool(ctx context.Context, call *provider.ToolCall) (provider.Message, error) {
	result := func(content string) provider.Message {
		return provider.Message{
			Role: provider.RoleTool, Content: content, ToolCallID: call.ID,
		}
	}

	tool, ok := a.cfg.Tools.Get(call.Name)
	if !ok {
		// Local models mangle names in predictable ways; recover the
		// obvious cases and record the correction in the trace.
		if fixed := normalizeToolName(call.Name, func(n string) bool {
			_, ok := a.cfg.Tools.Get(n)
			return ok
		}); fixed != "" {
			a.emit(Event{
				Type: EventInterceptor, ToolID: call.ID, ToolName: "repair",
				Text: fmt.Sprintf("tool name %q corrected to %q", call.Name, fixed),
			})
			call.Name = fixed
			tool, _ = a.cfg.Tools.Get(fixed)
		} else {
			return result(fmt.Sprintf("error: no such tool %q", call.Name)), errDoNotRun
		}
	}

	// Malformed argument JSON gets one deterministic repair pass before
	// the failure bounces back to the model.
	if !json.Valid([]byte(call.Args)) && call.Args != "" {
		if fixed, ok := repairJSON([]byte(call.Args)); ok {
			a.emit(Event{
				Type: EventInterceptor, ToolID: call.ID, ToolName: "repair",
				Text: "malformed argument JSON repaired",
			})
			call.Args = string(fixed)
		}
	}

	if !tool.Safe() && !a.sessionAllowed(call.Name) {
		decision, err := a.requestApproval(ctx, *call)
		if err != nil {
			return result(fmt.Sprintf("error: approval failed: %v", err)), errDoNotRun
		}
		switch decision {
		case DecisionAllowSession:
			a.mu.Lock()
			a.allowedForSession[call.Name] = true
			a.mu.Unlock()
		case DecisionDeny:
			return result("the user denied this tool call; ask before retrying or try another approach"), errDoNotRun
		}
	}
	return provider.Message{}, nil
}

// errDoNotRun is prepareTool's "produce a final result, do not execute"
// signal.
var errDoNotRun = fmt.Errorf("tool did not run")

// executeTool runs an already-approved tool call and returns its
// outcome: the result message, the tool_end event, and any interceptor
// notes. It emits nothing; the caller (dispatchTool for inline, the
// background delivery path for backgrounded tools) emits the outcome on
// the loop goroutine, so tool_start and tool_end stay in order and every
// emit is serialized with the loop.
func (a *Agent) executeTool(ctx context.Context, call provider.ToolCall) toolOutcome {
	result := func(content string) provider.Message {
		return provider.Message{
			Role: provider.RoleTool, Content: content, ToolCallID: call.ID,
		}
	}

	tool, _ := a.cfg.Tools.Get(call.Name)
	start := time.Now()
	var notes []Event

	for _, ic := range a.cfg.Interceptors {
		icStart := time.Now()
		note, err := ic.BeforeTool(ctx, call)
		if note != "" {
			notes = append(notes, Event{
				Type: EventInterceptor, ToolID: call.ID, ToolName: ic.Name(),
				Text: note, DurationMs: time.Since(icStart).Milliseconds(),
			})
		}
		if err != nil {
			notes = append(notes, Event{
				Type: EventInterceptor, ToolID: call.ID, ToolName: ic.Name(),
				Error: err.Error(), DurationMs: time.Since(icStart).Milliseconds(),
			})
			return toolOutcome{
				msg: result(fmt.Sprintf("blocked by policy: %v", err)),
				ev: Event{
					Type: EventToolEnd, ToolID: call.ID, ToolName: call.Name,
					Error: "blocked: " + err.Error(), DurationMs: time.Since(start).Milliseconds(),
				},
				notes: notes,
			}
		}
	}

	ctx = context.WithValue(ctx, toolCallIDKey{}, call.ID)
	out, err := tool.Run(ctx, []byte(call.Args))

	for _, ic := range a.cfg.Interceptors {
		icStart := time.Now()
		if extra := ic.AfterTool(ctx, call, out, err); extra != "" {
			notes = append(notes, Event{
				Type: EventInterceptor, ToolID: call.ID, ToolName: ic.Name(),
				Text: extra, DurationMs: time.Since(icStart).Milliseconds(),
			})
			out.Content += "\n\n" + extra
		}
	}
	dur := time.Since(start).Milliseconds()

	ev := Event{
		Type: EventToolEnd, ToolID: call.ID, ToolName: call.Name, DurationMs: dur,
	}
	if err != nil {
		ev.Error = err.Error()
		return toolOutcome{msg: result(fmt.Sprintf("error: %v", err)), ev: ev, notes: notes}
	}
	// One cap point for every tool result: oversized output spills to the
	// session sink (when set) or truncates inline.
	capped := a.cfg.Tools.CapResult(out)
	ev.Result = capped.Content
	ev.Diff = capped.Diff
	return toolOutcome{msg: result(capped.Content), ev: ev, notes: notes}
}

func (a *Agent) sessionAllowed(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.turnGrants[name] {
		return true
	}
	return a.allowedForSession[name]
}

// SetTurnGrants queues turn-scoped tool grants (a skill's allowed-tools
// frontmatter). They are consumed at the next RunMessages and cleared at
// the end of that turn.
func (a *Agent) SetTurnGrants(grants []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pendingGrants = append([]string(nil), grants...)
}

// toolNames lists the registry for the turn_start event, so "were tools
// even sent?" is answerable from the trace, not a debugger.
func (a *Agent) toolNames() []string {
	defs := a.cfg.Tools.Defs()
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		names = append(names, d.Name)
	}
	return names
}

// preApproved reports whether every call can run without asking anyone.
func (a *Agent) preApproved(calls []provider.ToolCall) bool {
	for _, call := range calls {
		tool, ok := a.cfg.Tools.Get(call.Name)
		if !ok {
			return false
		}
		if !tool.Safe() && !a.sessionAllowed(call.Name) {
			return false
		}
	}
	return true
}

func (a *Agent) requestApproval(ctx context.Context, call provider.ToolCall) (Decision, error) {
	id := newID()
	req := ApprovalRequest{ID: id, ToolName: call.Name, ToolArgs: call.Args}
	// Announce the request. Approvers that decide without a human (session
	// approval modes, durable allowlists) implement ApprovalAnnouncer and
	// emit the event themselves only when a human is actually being asked;
	// everyone else gets the event unconditionally, as before.
	if ann, ok := a.cfg.Approver.(ApprovalAnnouncer); ok {
		ann.AnnounceApproval(req)
	} else {
		a.emit(Event{Type: EventApprovalRequest, ApprovalID: id, ToolName: call.Name, ToolArgs: call.Args})
	}

	decision, err := a.cfg.Approver.RequestApproval(ctx, req)
	if err != nil {
		return DecisionDeny, err
	}
	a.emit(Event{Type: EventApprovalResult, ApprovalID: id, ToolName: call.Name, Decision: string(decision)})
	return decision, nil
}

const compactPrompt = "Summarize this conversation so far for your own future reference. " +
	"Preserve: the user's goals, decisions made, file paths touched, current state of the work, " +
	"and anything you would need to continue seamlessly. Be specific and dense."

// SummaryKind tags the transcript message compaction leaves behind: the
// summary is harness-produced context, not a user instruction, so the UI
// renders it differently (as an assistant-style markdown block, not a
// plain-text user bubble).
const SummaryKind = "summary"

// Compact replaces the transcript with a model-written summary. Explicit
// and user-triggered by design: rewriting history invalidates the
// provider's prefix cache, so the next turn re-pays for the whole prompt;
// the usage events make that visible instead of hiding it.
func (a *Agent) Compact(ctx context.Context) error {
	if !a.tryAcquire() {
		return ErrBusy
	}
	defer a.release()
	a.turnID = newID()
	return a.compactInline(ctx)
}

// compactInline summarizes and replaces the transcript without touching
// the busy slot: the loop calls it BETWEEN rounds when the prompt crosses
// the context threshold, so a single long turn cannot outgrow the window
// (the turn-end trigger never fires mid-turn, and long multi-task turns
// are exactly when the window fills).
func (a *Agent) compactInline(ctx context.Context) error {
	a.mu.Lock()
	if len(a.transcript) == 0 || (len(a.transcript) == 1 && a.transcript[0].Role == provider.RoleSystem) {
		a.mu.Unlock()
		return fmt.Errorf("nothing to compact")
	}
	msgs := make([]provider.Message, len(a.transcript))
	copy(msgs, a.transcript)
	a.mu.Unlock()

	req := provider.Request{
		Model: a.cfg.Model,
		Messages: append(msgs, provider.Message{
			Role: provider.RoleUser, Content: compactPrompt,
		}),
		// No tools: this is a pure summarization call.
	}
	events, err := a.cfg.Provider.Stream(ctx, req)
	if err != nil {
		return err
	}
	var summary string
	var usage *provider.Usage
	for ev := range events {
		switch ev.Kind {
		case provider.EventTextDelta:
			summary += ev.Text
		case provider.EventUsage:
			usage = ev.Usage
		case provider.EventError:
			return ev.Err
		}
	}
	if summary == "" {
		return fmt.Errorf("model returned an empty summary")
	}

	a.mu.Lock()
	fresh := a.transcript[:0:0]
	if len(a.transcript) > 0 && a.transcript[0].Role == provider.RoleSystem {
		fresh = append(fresh, a.transcript[0])
	}
	fresh = append(fresh, provider.Message{
		Role:    provider.RoleUser,
		Kind:    SummaryKind,
		Content: "Summary of the conversation before compaction:\n\n" + summary,
	})
	a.transcript = fresh
	a.mu.Unlock()

	a.emit(Event{Type: EventCompact, Text: summary, Usage: usage})
	return nil
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type toolCallIDKey struct{}

// ToolCallID returns the ID of the tool call this ctx is executing under
// ("" outside a tool). Lets a tool (spawn_agent) tag work it causes.
func ToolCallID(ctx context.Context) string {
	id, _ := ctx.Value(toolCallIDKey{}).(string)
	return id
}
