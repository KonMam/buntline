package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KonMam/buntline/internal/provider"
	"github.com/KonMam/buntline/internal/tools"
)

// fakeProvider replays a script: each Stream call pops the next canned
// event sequence. It also records every request it saw, so tests can
// assert on what the loop actually sent.
type fakeProvider struct {
	mu       sync.Mutex
	script   [][]provider.Event
	requests []provider.Request
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) SupportsImages() bool { return false }

func (f *fakeProvider) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	if len(f.script) == 0 {
		f.mu.Unlock()
		panic("fakeProvider: script exhausted")
	}
	events := f.script[0]
	f.script = f.script[1:]
	f.mu.Unlock()

	ch := make(chan provider.Event, len(events)+1)
	for _, ev := range events {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func textReply(text string) []provider.Event {
	return []provider.Event{
		{Kind: provider.EventTextDelta, Text: text},
		{Kind: provider.EventUsage, Usage: &provider.Usage{PromptTokens: 10, CompletionTokens: 5}},
		{Kind: provider.EventDone, FinishReason: "stop"},
	}
}

func toolReply(calls ...provider.ToolCall) []provider.Event {
	return []provider.Event{
		{Kind: provider.EventToolCalls, ToolCalls: calls},
		{Kind: provider.EventDone, FinishReason: "tool_calls"},
	}
}

// echoTool is a Safe test tool that echoes its "msg" argument.
type echoTool struct{}

func (echoTool) Safe() bool { return true }
func (echoTool) Def() provider.ToolDef {
	return provider.ToolDef{Name: "echo", Description: "echo", Parameters: map[string]any{"type": "object"}}
}
func (echoTool) Run(_ context.Context, args json.RawMessage) (tools.Result, error) {
	var in struct {
		Msg string `json:"msg"`
	}
	_ = json.Unmarshal(args, &in)
	return tools.Result{Content: "echo: " + in.Msg}, nil
}

// dangerTool is side-effectful; tests use it to exercise the approval gate.
type dangerTool struct{ ran *bool }

func (d dangerTool) Safe() bool { return false }
func (d dangerTool) Def() provider.ToolDef {
	return provider.ToolDef{Name: "danger", Description: "danger", Parameters: map[string]any{"type": "object"}}
}
func (d dangerTool) Run(context.Context, json.RawMessage) (tools.Result, error) {
	*d.ran = true
	return tools.Result{Content: "boom executed"}, nil
}

// longTool is a LongRunning tool whose Run blocks until released. It
// models bash: fast when released quickly, backgrounded past the grace
// period otherwise. The released channel doubles as a start signal so a
// test can wait until the tool is actually running.
type longTool struct {
	entered chan struct{}
	release chan struct{}
}

func (l longTool) Safe() bool   { return true }
func (l longTool) LongRunning() {}
func (l longTool) Def() provider.ToolDef {
	return provider.ToolDef{Name: "long", Description: "long", Parameters: map[string]any{"type": "object"}}
}
func (l longTool) Run(ctx context.Context, _ json.RawMessage) (tools.Result, error) {
	select {
	case l.entered <- struct{}{}:
	default:
	}
	select {
	case <-l.release:
		return tools.Result{Content: "long done"}, nil
	case <-ctx.Done():
		return tools.Result{Content: "long interrupted"}, ctx.Err()
	}
}

// neverLongTool is a LongRunning tool that declines backgrounding for
// every call (the bash sleep rule). It must run inline and die at its
// own timeout rather than backgrounding.
type neverLongTool struct {
	ran chan struct{}
}

func (n neverLongTool) Safe() bool                           { return true }
func (n neverLongTool) LongRunning()                         {}
func (n neverLongTool) NeverBackground(json.RawMessage) bool { return true }
func (n neverLongTool) Def() provider.ToolDef {
	return provider.ToolDef{Name: "inline", Description: "inline", Parameters: map[string]any{"type": "object"}}
}
func (n neverLongTool) Run(_ context.Context, _ json.RawMessage) (tools.Result, error) {
	select {
	case n.ran <- struct{}{}:
	default:
	}
	return tools.Result{Content: "inline done"}, nil
}

type scriptedApprover struct {
	decisions []Decision
	requests  []ApprovalRequest
}

func (s *scriptedApprover) RequestApproval(_ context.Context, req ApprovalRequest) (Decision, error) {
	s.requests = append(s.requests, req)
	if len(s.decisions) == 0 {
		return DecisionDeny, nil
	}
	d := s.decisions[0]
	s.decisions = s.decisions[1:]
	return d, nil
}

// scriptedAnswerer answers ask_user questions with a canned reply.
type scriptedAnswerer struct {
	answers   []string
	questions []tools.QuestionRequest
}

func (s *scriptedAnswerer) AskQuestion(_ context.Context, req tools.QuestionRequest) (string, error) {
	s.questions = append(s.questions, req)
	if len(s.answers) == 0 {
		return tools.InterruptAnswer, nil
	}
	a := s.answers[0]
	s.answers = s.answers[1:]
	return a, nil
}

func newTestAgent(t *testing.T, fake *fakeProvider, approver Approver, extraTools ...tools.Tool) (*Agent, *[]Event) {
	t.Helper()
	var events []Event
	var mu sync.Mutex
	reg := tools.NewRegistry(extraTools...)
	a := New(Config{
		Provider:     fake,
		Model:        "test-model",
		Tools:        reg,
		Approver:     approver,
		SystemPrompt: "you are a test",
		Emit: func(ev Event) {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		},
	})
	return a, &events
}

func eventTypes(evs []Event) []EventType {
	out := make([]EventType, len(evs))
	for i, e := range evs {
		out[i] = e.Type
	}
	return out
}

func TestPlainTextTurn(t *testing.T) {
	fake := &fakeProvider{script: [][]provider.Event{textReply("hello there")}}
	a, events := newTestAgent(t, fake, &scriptedApprover{})

	if err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}

	msgs := a.Messages()
	// system, user, assistant
	if len(msgs) != 3 {
		t.Fatalf("transcript = %d messages, want 3", len(msgs))
	}
	if msgs[2].Role != provider.RoleAssistant || msgs[2].Content != "hello there" {
		t.Errorf("assistant message = %+v", msgs[2])
	}

	types := eventTypes(*events)
	joined := ""
	for _, ty := range types {
		joined += string(ty) + " "
	}
	for _, want := range []string{"message", "turn_start", "text_delta", "usage", "turn_end"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing event %q in %s", want, joined)
		}
	}
}

func TestToolLoopFeedsResultsBack(t *testing.T) {
	fake := &fakeProvider{script: [][]provider.Event{
		toolReply(provider.ToolCall{ID: "c1", Name: "echo", Args: `{"msg":"ping"}`}),
		textReply("done"),
	}}
	a, events := newTestAgent(t, fake, &scriptedApprover{}, echoTool{})

	if err := a.Run(context.Background(), "use the tool"); err != nil {
		t.Fatal(err)
	}

	// Second request must contain the tool result linked to the call.
	second := fake.requests[1]
	last := second.Messages[len(second.Messages)-1]
	if last.Role != provider.RoleTool || last.ToolCallID != "c1" || last.Content != "echo: ping" {
		t.Errorf("tool result message = %+v", last)
	}

	var sawStart, sawEnd bool
	for _, ev := range *events {
		if ev.Type == EventToolStart && ev.ToolName == "echo" {
			sawStart = true
		}
		if ev.Type == EventToolEnd && ev.Result == "echo: ping" && ev.DurationMs >= 0 {
			sawEnd = true
		}
	}
	if !sawStart || !sawEnd {
		t.Error("tool_start/tool_end events missing")
	}
}

func TestApprovalDeny(t *testing.T) {
	ran := false
	fake := &fakeProvider{script: [][]provider.Event{
		toolReply(provider.ToolCall{ID: "c1", Name: "danger", Args: `{}`}),
		textReply("understood"),
	}}
	approver := &scriptedApprover{decisions: []Decision{DecisionDeny}}
	a, _ := newTestAgent(t, fake, approver, dangerTool{ran: &ran})

	if err := a.Run(context.Background(), "do the thing"); err != nil {
		t.Fatal(err)
	}
	if ran {
		t.Error("denied tool executed")
	}
	// The denial must be visible to the model.
	second := fake.requests[1]
	last := second.Messages[len(second.Messages)-1]
	if !strings.Contains(last.Content, "denied") {
		t.Errorf("model was not told about the denial: %q", last.Content)
	}
}

func TestAskUserCarriesAnswerAndContinues(t *testing.T) {
	fake := &fakeProvider{script: [][]provider.Event{
		// The model asks the user which approach to take.
		toolReply(provider.ToolCall{ID: "c1", Name: "ask_user",
			Args: `{"question":"Plan A or plan B?","options":["plan A","plan B"]}`}),
		// After the answer arrives, the model continues and finishes.
		textReply("going with the choice"),
	}}
	answerer := &scriptedAnswerer{answers: []string{"plan B"}}
	a, _ := newTestAgent(t, fake, &scriptedApprover{}, &tools.AskUser{Answerer: answerer})

	if err := a.Run(context.Background(), "ask me which approach"); err != nil {
		t.Fatal(err)
	}

	// The answerer saw the full question including options.
	if len(answerer.questions) != 1 {
		t.Fatalf("answerer asked %d times, want 1", len(answerer.questions))
	}
	q := answerer.questions[0]
	if q.Question != "Plan A or plan B?" || len(q.Options) != 2 || q.Options[0] != "plan A" {
		t.Errorf("question payload = %+v", q)
	}

	// The tool result the model sees carries the answer, so the next
	// request's last message is the answer as the tool result.
	second := fake.requests[1]
	last := second.Messages[len(second.Messages)-1]
	if last.Role != provider.RoleTool || last.ToolCallID != "c1" || last.Content != "plan B" {
		t.Errorf("tool result message = %+v", last)
	}

	// The final assistant reply proves the turn continued past the answer.
	msgs := a.Messages()
	final := msgs[len(msgs)-1]
	if final.Role != provider.RoleAssistant || final.Content != "going with the choice" {
		t.Errorf("final message = %+v", final)
	}
}

func TestAskUserWithoutAnswererReturnsInterruptText(t *testing.T) {
	fake := &fakeProvider{script: [][]provider.Event{
		toolReply(provider.ToolCall{ID: "c1", Name: "ask_user", Args: `{"question":"anyone there?"}`}),
		textReply("ok"),
	}}
	a, _ := newTestAgent(t, fake, &scriptedApprover{}, &tools.AskUser{})

	if err := a.Run(context.Background(), "ask"); err != nil {
		t.Fatal(err)
	}
	second := fake.requests[1]
	last := second.Messages[len(second.Messages)-1]
	if last.Role != provider.RoleTool || !strings.Contains(last.Content, "interrupted") {
		t.Errorf("no-answerer tool result = %+v", last)
	}
}

func TestAskUserInterruptedByContext(t *testing.T) {
	blocked := make(chan struct{})
	release := make(chan struct{})
	answerer := &blockingAnswerer{entered: blocked, release: release}
	fake := &fakeProvider{script: [][]provider.Event{
		toolReply(provider.ToolCall{ID: "c1", Name: "ask_user", Args: `{"question":"waiting"}`}),
	}}
	a, events := newTestAgent(t, fake, &scriptedApprover{}, &tools.AskUser{Answerer: answerer})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, "ask") }()
	<-blocked // the tool is now blocking on the answerer
	cancel()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "context canceled") {
			t.Errorf("run error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not return after context cancellation")
	}
	close(release)

	// The turn ended cancelled, and the model saw the interrupt text as
	// the tool result rather than a silent hang.
	var sawInterrupt bool
	for _, ev := range *events {
		if ev.Type == EventToolEnd && ev.ToolName == "ask_user" {
			sawInterrupt = strings.Contains(ev.Result, "interrupted")
		}
	}
	if !sawInterrupt {
		t.Error("interrupt answer missing from the tool result")
	}
}

// blockingAnswerer models the server's round-trip: it parks until an
// answer arrives or the context is cancelled.
type blockingAnswerer struct {
	entered chan struct{}
	release chan struct{}
}

func (b *blockingAnswerer) AskQuestion(ctx context.Context, _ tools.QuestionRequest) (string, error) {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	select {
	case <-b.release:
		return "released", nil
	case <-ctx.Done():
		return tools.InterruptAnswer, ctx.Err()
	}
}

func TestApprovalAllowSessionPersists(t *testing.T) {
	ran := false
	fake := &fakeProvider{script: [][]provider.Event{
		toolReply(provider.ToolCall{ID: "c1", Name: "danger", Args: `{}`}),
		toolReply(provider.ToolCall{ID: "c2", Name: "danger", Args: `{}`}),
		textReply("twice done"),
	}}
	approver := &scriptedApprover{decisions: []Decision{DecisionAllowSession}}
	a, _ := newTestAgent(t, fake, approver, dangerTool{ran: &ran})

	if err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Error("approved tool did not run")
	}
	// One approval request total: the second call was covered by the
	// session grant.
	if len(approver.requests) != 1 {
		t.Errorf("approver asked %d times, want 1", len(approver.requests))
	}
}

func TestUnknownToolBecomesResult(t *testing.T) {
	fake := &fakeProvider{script: [][]provider.Event{
		toolReply(provider.ToolCall{ID: "c1", Name: "invented_tool", Args: `{}`}),
		textReply("ok"),
	}}
	a, _ := newTestAgent(t, fake, &scriptedApprover{})

	if err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	second := fake.requests[1]
	last := second.Messages[len(second.Messages)-1]
	if !strings.Contains(last.Content, "no such tool") {
		t.Errorf("hallucinated tool call should produce an error result, got %q", last.Content)
	}
}

func TestMaxRoundsCap(t *testing.T) {
	// A model that calls tools forever with VARYING arguments: busy but
	// not looping, so only the round cap stops it.
	script := make([][]provider.Event, MaxRounds)
	for i := range script {
		script[i] = toolReply(provider.ToolCall{ID: "c", Name: "echo",
			Args: fmt.Sprintf(`{"msg":"step %d"}`, i)})
	}
	fake := &fakeProvider{script: script}
	a, events := newTestAgent(t, fake, &scriptedApprover{}, echoTool{})

	if err := a.Run(context.Background(), "work forever"); err != nil {
		t.Fatal(err)
	}
	last := (*events)[len(*events)-1]
	if last.Type != EventTurnEnd || last.StopReason != "max_rounds" {
		t.Errorf("last event = %+v, want turn_end/max_rounds", last)
	}
}

func TestLoopDetectionStopsTurn(t *testing.T) {
	// The same call with the same result, repeated: a loop, stopped well
	// before the round cap.
	script := make([][]provider.Event, MaxRounds)
	for i := range script {
		script[i] = toolReply(provider.ToolCall{ID: "c", Name: "echo", Args: `{"msg":"again"}`})
	}
	fake := &fakeProvider{script: script}
	a, events := newTestAgent(t, fake, &scriptedApprover{}, echoTool{})

	if err := a.Run(context.Background(), "loop forever"); err != nil {
		t.Fatal(err)
	}
	last := (*events)[len(*events)-1]
	if last.Type != EventTurnEnd || last.StopReason != "loop_detected" {
		t.Errorf("last event = %+v, want turn_end/loop_detected", last)
	}
	if len(fake.requests) >= MaxRounds {
		t.Errorf("loop should stop before the round cap; made %d calls", len(fake.requests))
	}
}

func TestBusyRejectsConcurrentRun(t *testing.T) {
	started := make(chan struct{})
	proceed := make(chan struct{})
	blocking := &blockingProvider{started: started, proceed: proceed}
	a, _ := newTestAgent(t, nil, &scriptedApprover{})
	a.cfg.Provider = blocking

	go func() { _ = a.Run(context.Background(), "first") }()
	<-started

	if err := a.Run(context.Background(), "second"); err != ErrBusy {
		t.Errorf("concurrent run error = %v, want ErrBusy", err)
	}
	close(proceed)
}

type blockingProvider struct {
	started chan struct{}
	proceed chan struct{}
}

func (b *blockingProvider) Name() string         { return "blocking" }
func (b *blockingProvider) SupportsImages() bool { return false }
func (b *blockingProvider) Stream(context.Context, provider.Request) (<-chan provider.Event, error) {
	close(b.started)
	<-b.proceed
	ch := make(chan provider.Event, 2)
	ch <- provider.Event{Kind: provider.EventTextDelta, Text: "ok"}
	ch <- provider.Event{Kind: provider.EventDone, FinishReason: "stop"}
	close(ch)
	return ch, nil
}

func TestEmptyAnswerGetsNudgedOnce(t *testing.T) {
	fake := &fakeProvider{script: [][]provider.Event{
		{ // thinking only, no content, no tools: the qwen failure mode
			{Kind: provider.EventThinkingDelta, Text: "hmm let me think about this"},
			{Kind: provider.EventDone, FinishReason: "stop"},
		},
		textReply("the actual answer"),
	}}
	a, _ := newTestAgent(t, fake, &scriptedApprover{})

	if err := a.Run(context.Background(), "question"); err != nil {
		t.Fatal(err)
	}
	msgs := a.Messages()
	last := msgs[len(msgs)-1]
	if last.Role != provider.RoleAssistant || last.Content != "the actual answer" {
		t.Errorf("nudge should recover an answer, got %+v", last)
	}
	// The nudge is a visible transcript message, not hidden machinery.
	var sawNudge bool
	for _, m := range msgs {
		if m.Role == provider.RoleUser && strings.Contains(m.Content, "contained no answer") {
			sawNudge = true
		}
	}
	if !sawNudge {
		t.Error("nudge message missing from transcript")
	}
}

func TestEmptyAnswerNudgeGivesUpAfterOne(t *testing.T) {
	thinkOnly := []provider.Event{
		{Kind: provider.EventThinkingDelta, Text: "still just thinking"},
		{Kind: provider.EventDone, FinishReason: "stop"},
	}
	fake := &fakeProvider{script: [][]provider.Event{thinkOnly, thinkOnly}}
	a, events := newTestAgent(t, fake, &scriptedApprover{})

	if err := a.Run(context.Background(), "question"); err != nil {
		t.Fatal(err)
	}
	// Two model calls total: original + one nudge; no infinite loop.
	if len(fake.requests) != 2 {
		t.Errorf("model called %d times, want 2", len(fake.requests))
	}
	last := (*events)[len(*events)-1]
	if last.Type != EventTurnEnd || last.StopReason != "done" {
		t.Errorf("turn should end after one nudge, got %+v", last)
	}
}

func TestSteeringDeliveredMidTurn(t *testing.T) {
	fake := &fakeProvider{script: [][]provider.Event{
		toolReply(provider.ToolCall{ID: "c1", Name: "echo", Args: `{"msg":"one"}`}),
		textReply("acknowledged the steering"),
	}}
	a, _ := newTestAgent(t, fake, &scriptedApprover{}, echoTool{})

	// Queue steering before the run so the drain after the tool batch
	// picks it up deterministically.
	a.mu.Lock()
	a.busy = true
	a.mu.Unlock()
	if err := a.Steer("actually, focus on the second file"); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.busy = false
	a.mu.Unlock()

	if err := a.Run(context.Background(), "start"); err != nil {
		t.Fatal(err)
	}

	// The second model call must include the steering message after the
	// tool result.
	second := fake.requests[1].Messages
	steerIdx, toolIdx := -1, -1
	for i, m := range second {
		if m.Role == provider.RoleTool {
			toolIdx = i
		}
		if m.Role == provider.RoleUser && strings.Contains(m.Content, "second file") {
			steerIdx = i
		}
	}
	if steerIdx == -1 || toolIdx == -1 || steerIdx < toolIdx {
		t.Errorf("steering should follow the tool result: steer=%d tool=%d", steerIdx, toolIdx)
	}
}

func TestSteeringKeepsTurnAliveAfterFinalReply(t *testing.T) {
	fake := &fakeProvider{script: [][]provider.Event{
		textReply("first answer"),
		textReply("answer to the steering message"),
	}}
	a, _ := newTestAgent(t, fake, &scriptedApprover{})

	a.mu.Lock()
	a.busy = true
	a.mu.Unlock()
	if err := a.Steer("one more thing"); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.busy = false
	a.mu.Unlock()

	if err := a.Run(context.Background(), "question"); err != nil {
		t.Fatal(err)
	}
	if len(fake.requests) != 2 {
		t.Fatalf("steering should trigger a second model call, got %d", len(fake.requests))
	}
	msgs := a.Messages()
	last := msgs[len(msgs)-1]
	if last.Content != "answer to the steering message" {
		t.Errorf("final message = %q", last.Content)
	}
}

func TestCompactReplacesTranscriptAndKeepsSystem(t *testing.T) {
	fake := &fakeProvider{script: [][]provider.Event{
		textReply("first answer"),
		{
			{Kind: provider.EventTextDelta, Text: "SUMMARY OF EVERYTHING"},
			{Kind: provider.EventUsage, Usage: &provider.Usage{PromptTokens: 100, CompletionTokens: 20}},
			{Kind: provider.EventDone, FinishReason: "stop"},
		},
	}}
	a, events := newTestAgent(t, fake, &scriptedApprover{})

	if err := a.Run(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if err := a.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}

	msgs := a.Messages()
	if len(msgs) != 2 {
		t.Fatalf("post-compact transcript = %d messages, want 2 (system + summary)", len(msgs))
	}
	if msgs[0].Role != provider.RoleSystem {
		t.Error("system prompt lost in compaction")
	}
	if !strings.Contains(msgs[1].Content, "SUMMARY OF EVERYTHING") {
		t.Errorf("summary message = %q", msgs[1].Content)
	}
	if msgs[1].Kind != SummaryKind {
		t.Errorf("summary message kind = %q, want %q", msgs[1].Kind, SummaryKind)
	}
	if msgs[1].Role != provider.RoleUser {
		t.Errorf("summary message role = %q, want user (it must round-trip as user context)", msgs[1].Role)
	}

	var sawCompact bool
	for _, ev := range *events {
		if ev.Type == EventCompact && ev.Usage != nil && ev.Usage.PromptTokens == 100 {
			sawCompact = true
		}
	}
	if !sawCompact {
		t.Error("compact event with usage missing")
	}
}

func TestMidTurnCompaction(t *testing.T) {
	// Round 1: a tool call whose usage crosses 85% of the window.
	// The loop must compact BETWEEN rounds (script entry 2 is the
	// summarization call), then continue the turn to a final answer.
	round1 := []provider.Event{
		{Kind: provider.EventToolCalls, ToolCalls: []provider.ToolCall{
			{ID: "c1", Name: "echo", Args: `{"text":"hi"}`},
		}},
		{Kind: provider.EventUsage, Usage: &provider.Usage{PromptTokens: 90}},
		{Kind: provider.EventDone, FinishReason: "tool_calls"},
	}
	compaction := []provider.Event{
		{Kind: provider.EventTextDelta, Text: "summary of everything so far"},
		{Kind: provider.EventDone, FinishReason: "stop"},
	}
	final := []provider.Event{
		{Kind: provider.EventTextDelta, Text: "done"},
		{Kind: provider.EventUsage, Usage: &provider.Usage{PromptTokens: 10}},
		{Kind: provider.EventDone, FinishReason: "stop"},
	}
	fake := &fakeProvider{script: [][]provider.Event{round1, compaction, final}}
	a, events := newTestAgent(t, fake, &scriptedApprover{}, echoTool{})
	a.cfg.ContextWindow = func() int { return 100 }

	if err := a.Run(context.Background(), "long task"); err != nil {
		t.Fatal(err)
	}

	var sawCompact bool
	for _, ev := range *events {
		if ev.Type == EventCompact {
			sawCompact = true
		}
	}
	if !sawCompact {
		t.Fatal("no compact event during the turn")
	}
	msgs := a.Messages()
	if last := msgs[len(msgs)-1]; last.Content != "done" {
		t.Errorf("turn should continue after compaction, last = %+v", last)
	}
	var sawSummary bool
	for _, m := range msgs {
		if strings.Contains(m.Content, "summary of everything so far") {
			sawSummary = true
			if m.Kind != SummaryKind {
				t.Errorf("summary message kind = %q, want %q", m.Kind, SummaryKind)
			}
		}
	}
	if !sawSummary {
		t.Error("compacted transcript missing the summary message")
	}
	// The compaction call must not offer tools.
	if len(fake.requests) != 3 || len(fake.requests[1].Tools) != 0 {
		t.Errorf("expected 3 calls with a tool-less compaction call, got %d", len(fake.requests))
	}
}

func TestBackgroundedToolDeliversAfterGrace(t *testing.T) {
	oldGrace := bgGrace
	bgGrace = 50 * time.Millisecond
	defer func() { bgGrace = oldGrace }()

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	fake := &fakeProvider{script: [][]provider.Event{
		toolReply(provider.ToolCall{ID: "c1", Name: "long", Args: `{}`}),
		textReply("finished"),
	}}
	a, events := newTestAgent(t, fake, &scriptedApprover{}, longTool{entered: entered, release: release})

	done := make(chan error, 1)
	go func() { done <- a.Run(context.Background(), "run the long tool") }()
	<-entered // the tool is now running
	// Let the grace period lapse so the tool backgrounds, then release it.
	time.Sleep(2 * bgGrace)
	close(release)

	if err := <-done; err != nil {
		t.Fatal(err)
	}

	// The transcript must contain the placeholder AND the real result, in
	// order. Only the placeholder may answer the tool_call_id: a second
	// tool message for the same id is rejected by the backend and poisons
	// the session, so the real result arrives as a user message.
	msgs := a.Messages()
	var placeholder, real bool
	for _, m := range msgs {
		if m.Role == provider.RoleTool && m.ToolCallID == "c1" && strings.HasPrefix(m.Content, "[started") {
			placeholder = true
		}
		if m.Role == provider.RoleUser && strings.Contains(m.Content, "long done") {
			real = true
		}
	}
	if !placeholder {
		t.Error("placeholder result missing from transcript")
	}
	if !real {
		t.Error("real backgrounded result missing from transcript")
	}
	assertToolPairing(t, msgs)

	// Events must read start → bg → end, with the end AFTER the bg, and
	// exactly one tool_end for the call.
	var sawStart, sawBg, sawEnd bool
	var endAfterBg bool
	for _, ev := range *events {
		if ev.Type == EventToolStart && ev.ToolID == "c1" {
			sawStart = true
		}
		if ev.Type == EventToolBg && ev.ToolID == "c1" {
			sawBg = true
		}
		if ev.Type == EventToolEnd && ev.ToolID == "c1" {
			if sawBg {
				endAfterBg = true
			}
			if sawEnd {
				t.Error("duplicate tool_end for a backgrounded call")
			}
			sawEnd = true
		}
	}
	if !sawStart || !sawBg || !sawEnd || !endAfterBg {
		t.Errorf("event order wrong: start=%v bg=%v end=%v endAfterBg=%v", sawStart, sawBg, sawEnd, endAfterBg)
	}

	// The model's second call must carry the real result, not the
	// placeholder, and must itself be a valid request.
	second := fake.requests[1]
	last := second.Messages[len(second.Messages)-1]
	if last.Role != provider.RoleUser || !strings.Contains(last.Content, "long done") {
		t.Errorf("second model call's last message = %+v, want real result", last)
	}
	assertToolPairing(t, second.Messages)
}

// assertToolPairing fails the test unless every tool message answers an
// open tool_call from the assistant message that opened it, and every
// tool_call is answered exactly once. This is what the backend checks;
// a transcript that breaks it is rejected whole and, once persisted,
// fails every later turn too. Assert it anywhere a transcript is built.
func assertToolPairing(t *testing.T, msgs []provider.Message) {
	t.Helper()
	var open []string
	for i, m := range msgs {
		switch {
		case m.Role == provider.RoleTool:
			j := slices.Index(open, m.ToolCallID)
			if j < 0 {
				t.Errorf("message %d: tool result for %q answers no open tool_call", i, m.ToolCallID)
				continue
			}
			open = slices.Delete(open, j, j+1)
		case len(m.ToolCalls) > 0:
			if len(open) > 0 {
				t.Errorf("message %d: tool_calls %v left unanswered", i, open)
			}
			open = nil
			for _, c := range m.ToolCalls {
				open = append(open, c.ID)
			}
		default:
			if len(open) > 0 {
				t.Errorf("message %d (%s): tool_calls %v left unanswered", i, m.Role, open)
			}
			open = nil
		}
	}
	if len(open) > 0 {
		t.Errorf("transcript ends with unanswered tool_calls %v", open)
	}
}

func TestFastLongToolNeverBackgrounds(t *testing.T) {
	oldGrace := bgGrace
	bgGrace = 500 * time.Millisecond
	defer func() { bgGrace = oldGrace }()

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	fake := &fakeProvider{script: [][]provider.Event{
		toolReply(provider.ToolCall{ID: "c1", Name: "long", Args: `{}`}),
		textReply("done fast"),
	}}
	a, events := newTestAgent(t, fake, &scriptedApprover{}, longTool{entered: entered, release: release})

	go func() { _ = a.Run(context.Background(), "run") }()
	<-entered
	close(release)
	// Let the turn finish (no backgrounding should happen).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		busy := a.busy
		a.mu.Unlock()
		if !busy {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	a.mu.Lock()
	busy := a.busy
	a.mu.Unlock()
	if busy {
		t.Fatal("turn still running")
	}

	for _, ev := range *events {
		if ev.Type == EventToolBg {
			t.Fatal("fast tool must not background")
		}
	}
	// One tool_end, carrying the real result.
	var ends int
	for _, ev := range *events {
		if ev.Type == EventToolEnd && ev.ToolID == "c1" {
			ends++
			if ev.Result != "long done" {
				t.Errorf("tool_end result = %q", ev.Result)
			}
		}
	}
	if ends != 1 {
		t.Errorf("tool_end count = %d, want 1", ends)
	}
}

func TestNeverBackgroundToolRunsInline(t *testing.T) {
	oldGrace := bgGrace
	bgGrace = 10 * time.Millisecond
	defer func() { bgGrace = oldGrace }()

	ran := make(chan struct{}, 1)
	fake := &fakeProvider{script: [][]provider.Event{
		toolReply(provider.ToolCall{ID: "c1", Name: "inline", Args: `{}`}),
		textReply("done"),
	}}
	a, events := newTestAgent(t, fake, &scriptedApprover{}, neverLongTool{ran: ran})

	// The tool declines backgrounding, so even though the grace period is
	// tiny, it must run inline and the turn must not background it.
	if err := a.Run(context.Background(), "run"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ran:
	default:
		t.Error("never-background tool did not run")
	}
	for _, ev := range *events {
		if ev.Type == EventToolBg {
			t.Fatal("never-background tool must not background")
		}
	}
	// The real result is inline: no background delivery appended.
	msgs := a.Messages()
	for _, m := range msgs {
		if strings.Contains(m.Content, "[background") {
			t.Errorf("unexpected background message: %q", m.Content)
		}
	}
}

func TestSteeringWakesParkedLoop(t *testing.T) {
	oldGrace := bgGrace
	bgGrace = 50 * time.Millisecond
	defer func() { bgGrace = oldGrace }()

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	fake := &fakeProvider{script: [][]provider.Event{
		toolReply(provider.ToolCall{ID: "c1", Name: "long", Args: `{}`}),
		textReply("answer to steering"),
		textReply("final answer after tool finished"),
	}}
	a, _ := newTestAgent(t, fake, &scriptedApprover{}, longTool{entered: entered, release: release})

	done := make(chan error, 1)
	go func() { done <- a.Run(context.Background(), "run the long tool") }()
	<-entered
	time.Sleep(2 * bgGrace) // backgrounded, loop parked

	// Steering while parked must wake the loop immediately: the model
	// responds before the tool finishes.
	a.mu.Lock()
	a.busy = true
	a.mu.Unlock()
	if err := a.Steer("please tell me the plan"); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.busy = false
	a.mu.Unlock()

	// Wait for the second model call (the steering response) to happen
	// while the tool is still running.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		n := len(fake.requests)
		a.mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	a.mu.Lock()
	n := len(fake.requests)
	a.mu.Unlock()
	if n < 2 {
		t.Fatalf("steering did not wake the parked loop; model called %d times", n)
	}
	// The steering response must be visible before the tool finishes.
	second := fake.requests[1]
	last := second.Messages[len(second.Messages)-1]
	if last.Role != provider.RoleUser || !strings.Contains(last.Content, "plan") {
		t.Errorf("second call should carry the steering, last = %+v", last)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// The tool result then lands and the turn delivers it.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		n := len(fake.requests)
		a.mu.Unlock()
		if n >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	a.mu.Lock()
	n = len(fake.requests)
	a.mu.Unlock()
	if n < 3 {
		t.Errorf("tool result should trigger a third model call; got %d", n)
	}
	third := fake.requests[2]
	last = third.Messages[len(third.Messages)-1]
	if last.Role != provider.RoleUser || !strings.Contains(last.Content, "long done") {
		t.Errorf("third call should carry the real tool result, last = %+v", last)
	}
	assertToolPairing(t, third.Messages)
}

func TestInterruptDuringBackgroundedToolCancelsTurn(t *testing.T) {
	oldGrace := bgGrace
	bgGrace = 50 * time.Millisecond
	defer func() { bgGrace = oldGrace }()

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	fake := &fakeProvider{script: [][]provider.Event{
		toolReply(provider.ToolCall{ID: "c1", Name: "long", Args: `{}`}),
		textReply("after tool"), // never reached: interrupted
	}}
	a, events := newTestAgent(t, fake, &scriptedApprover{}, longTool{entered: entered, release: release})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, "run") }()
	<-entered
	time.Sleep(2 * bgGrace) // backgrounded and parked
	cancel()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "context canceled") {
			t.Errorf("run error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not return after cancel")
	}
	close(release)

	// The turn ended cancelled. The stale backgrounded result must NOT
	// deliver into a later turn (there is none here, but the turn_end
	// must be the last event, not a stray tool_end).
	last := (*events)[len(*events)-1]
	if last.Type != EventTurnEnd || last.StopReason != "cancelled" {
		t.Errorf("last event = %+v, want turn_end/cancelled", last)
	}
	var ends int
	for _, ev := range *events {
		if ev.Type == EventToolEnd && ev.ToolID == "c1" {
			ends++
		}
	}
	if ends != 0 {
		t.Errorf("stale backgrounded result delivered a tool_end (%d)", ends)
	}
}

func TestBackgroundedResultDroppedAcrossTurns(t *testing.T) {
	oldGrace := bgGrace
	bgGrace = 50 * time.Millisecond
	defer func() { bgGrace = oldGrace }()

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	lt := longTool{entered: entered, release: release}
	fake := &fakeProvider{script: [][]provider.Event{
		toolReply(provider.ToolCall{ID: "c1", Name: "long", Args: `{}`}),
		textReply("turn one"),
		textReply("turn two"),
	}}
	a, events := newTestAgent(t, fake, &scriptedApprover{}, lt)

	// Turn one: background the tool, then interrupt before it finishes.
	ctx1, cancel1 := context.WithCancel(context.Background())
	done1 := make(chan error, 1)
	go func() { done1 <- a.Run(ctx1, "first turn") }()
	<-entered
	time.Sleep(2 * bgGrace)
	cancel1()
	<-done1
	close(release) // the tool finishes AFTER turn one ended

	// Turn two: must not receive the stale result.
	if err := a.Run(context.Background(), "second turn"); err != nil {
		t.Fatal(err)
	}

	// Count tool_end events for c1: the stale one must be dropped.
	var ends int
	for _, ev := range *events {
		if ev.Type == EventToolEnd && ev.ToolID == "c1" {
			ends++
		}
	}
	if ends != 0 {
		t.Errorf("stale backgrounded result delivered across turns (%d tool_end)", ends)
	}
}

// A session persisted by a build that answered one tool_call twice must
// heal on resume rather than fail every turn. This is the exact shape
// that bricked a stable session: a backgrounded bash placeholder and its
// real result, both carrying the same tool_call_id.
func TestResumeRepairsDuplicateToolResult(t *testing.T) {
	poisoned := []provider.Message{
		{Role: provider.RoleUser, Content: "run the tests"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "c1", Name: "bash", Args: "{}"}}},
		{Role: provider.RoleTool, ToolCallID: "c1", Content: "[started bash; working in the background]"},
		{Role: provider.RoleTool, ToolCallID: "c1", Content: "ok github.com/KonMam/buntline"},
	}
	fake := &fakeProvider{script: [][]provider.Event{textReply("done")}}
	a, _ := newTestAgent(t, fake, &scriptedApprover{})
	a.SetMessages(poisoned)

	assertToolPairing(t, a.Messages())

	if err := a.Run(context.Background(), "and now?"); err != nil {
		t.Fatal(err)
	}
	assertToolPairing(t, fake.requests[0].Messages)

	// The repair must not lose the real result.
	var kept bool
	for _, m := range fake.requests[0].Messages {
		if strings.Contains(m.Content, "ok github.com/KonMam/buntline") {
			kept = true
		}
	}
	if !kept {
		t.Error("repair dropped the real tool result")
	}
}

func TestTurnGrantSkipsApprovalForOneTurn(t *testing.T) {
	ran := false
	approver := &scriptedApprover{decisions: []Decision{DecisionAllowSession}}
	fake := &fakeProvider{script: [][]provider.Event{
		toolReply(provider.ToolCall{ID: "c1", Name: "danger", Args: "{}"}),
		textReply("done"),
		// Second turn: no grant, so the danger call must ask again.
		toolReply(provider.ToolCall{ID: "c2", Name: "danger", Args: "{}"}),
		textReply("done again"),
	}}
	a, _ := newTestAgent(t, fake, approver, dangerTool{ran: &ran})

	// Turn 1: grant "danger" for this turn.
	a.SetTurnGrants([]string{"danger"})
	if err := a.Run(context.Background(), "use danger"); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Fatal("danger tool did not run on turn 1")
	}
	if len(approver.requests) != 0 {
		t.Fatalf("approver asked %d times on turn 1, want 0 (granted)", len(approver.requests))
	}

	// Turn 2: no grant; the approval gate must engage again.
	ran = false
	if err := a.Run(context.Background(), "use danger again"); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Fatal("danger tool did not run on turn 2")
	}
	if len(approver.requests) != 1 {
		t.Fatalf("approver asked %d times on turn 2, want 1 (grant cleared)", len(approver.requests))
	}
}

func TestTurnGrantDoesNotLeakAcrossTurns(t *testing.T) {
	// A grant set before turn 1 must not apply to a turn started after
	// it. The grant is consumed at turn start and cleared.
	ran := false
	approver := &scriptedApprover{decisions: []Decision{DecisionAllowSession, DecisionAllowSession}}
	fake := &fakeProvider{script: [][]provider.Event{
		textReply("first"),
		toolReply(provider.ToolCall{ID: "c2", Name: "danger", Args: "{}"}),
		textReply("done"),
	}}
	a, _ := newTestAgent(t, fake, approver, dangerTool{ran: &ran})

	a.SetTurnGrants([]string{"danger"})
	if err := a.Run(context.Background(), "plain first turn"); err != nil {
		t.Fatal(err)
	}
	// The grant was consumed by turn 1 (even though no danger call
	// happened). Turn 2 must ask for approval again.
	if err := a.Run(context.Background(), "use danger now"); err != nil {
		t.Fatal(err)
	}
	if len(approver.requests) != 1 {
		t.Fatalf("approver asked %d times, want 1 (grant consumed by turn 1)", len(approver.requests))
	}
}
