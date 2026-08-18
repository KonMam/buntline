package tasks

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/KonMam/tether/internal/agent"
	"github.com/KonMam/tether/internal/provider"
	"github.com/KonMam/tether/internal/tools"
)

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeStore is an in-memory TaskStore for tool tests: it records
// writes and reads back the last one, mirroring the server bridge.
type fakeStore struct {
	last []tools.TaskItem
}

func (f *fakeStore) Write(tasks []tools.TaskItem) error {
	f.last = tasks
	return nil
}

func (f *fakeStore) Read() []tools.TaskItem { return f.last }

func TestTodoWriteReplacesListAndAcks(t *testing.T) {
	store := &fakeStore{}
	tw := &TodoWrite{store: store}
	args := mustMarshal(t, map[string]any{
		"todos": []map[string]string{
			{"content": "read the plan", "status": "completed"},
			{"content": "implement the module", "status": "in_progress"},
			{"content": "verify on dev", "status": "pending"},
		},
	})
	res, err := tw.Run(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Task list updated: 1 pending, 1 in progress, 1 completed."; res.Content != want {
		t.Errorf("ack = %q, want %q", res.Content, want)
	}
	if len(store.last) != 3 || store.last[0].Content != "read the plan" {
		t.Errorf("stored list = %+v", store.last)
	}
}

func TestTodoWriteRejectsEmptyContent(t *testing.T) {
	tw := &TodoWrite{store: &fakeStore{}}
	args := mustMarshal(t, map[string]any{
		"todos": []map[string]string{
			{"content": "   ", "status": "pending"},
		},
	})
	_, err := tw.Run(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "content is required") {
		t.Errorf("empty content error = %v", err)
	}
}

func TestTodoWriteRejectsDuplicateContent(t *testing.T) {
	tw := &TodoWrite{store: &fakeStore{}}
	args := mustMarshal(t, map[string]any{
		"todos": []map[string]string{
			{"content": "same", "status": "pending"},
			{"content": "same", "status": "completed"},
		},
	})
	_, err := tw.Run(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("duplicate error = %v", err)
	}
}

func TestTodoWriteRejectsBadStatus(t *testing.T) {
	tw := &TodoWrite{store: &fakeStore{}}
	args := mustMarshal(t, map[string]any{
		"todos": []map[string]string{
			{"content": "thing", "status": "maybe"},
		},
	})
	_, err := tw.Run(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "status must be one of") {
		t.Errorf("bad status error = %v", err)
	}
}

func TestTodoWriteWithoutStoreReportsUnavailable(t *testing.T) {
	tw := &TodoWrite{}
	args := mustMarshal(t, map[string]any{
		"todos": []map[string]string{{"content": "x", "status": "pending"}},
	})
	_, err := tw.Run(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Errorf("no-store error = %v", err)
	}
}

func TestTodoReadFormatsLines(t *testing.T) {
	store := &fakeStore{last: []tools.TaskItem{
		{Content: "one", Status: "pending"},
		{Content: "two", Status: "completed"},
	}}
	tr := &TodoRead{store: store}
	res, err := tr.Run(context.Background(), json.RawMessage("{}"))
	if err != nil {
		t.Fatal(err)
	}
	want := "[pending] one\n[completed] two"
	if res.Content != want {
		t.Errorf("todo_read = %q, want %q", res.Content, want)
	}
}

func TestTodoReadEmpty(t *testing.T) {
	tr := &TodoRead{store: &fakeStore{}}
	res, err := tr.Run(context.Background(), json.RawMessage("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "No tasks yet" {
		t.Errorf("empty todo_read = %q", res.Content)
	}
}

func TestTodoReadWithoutStoreSaysEmpty(t *testing.T) {
	tr := &TodoRead{}
	res, err := tr.Run(context.Background(), json.RawMessage("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "No tasks yet" {
		t.Errorf("no-store todo_read = %q", res.Content)
	}
}

func TestModuleFoldAndReplayAgree(t *testing.T) {
	m := &Module{}
	first := []agent.TaskItem{{Content: "a", Status: "pending"}}
	second := []agent.TaskItem{{Content: "b", Status: "completed"}, {Content: "c", Status: "in_progress"}}

	// Live observation: the observer folds events as they arrive.
	obs := m.Observer("s1", "/tmp")
	obs(agent.Event{Type: agent.EventTasks, Tasks: first})
	obs(agent.Event{Type: agent.EventTasks, Tasks: second})

	// Replay from the same event sequence must land on the same list.
	m2 := &Module{}
	m2.Replay("s1", []agent.Event{
		{Type: agent.EventTasks, Tasks: first},
		{Type: agent.EventTasks, Tasks: second},
	})

	got := m.Get("s1")
	want := m2.Get("s1")
	if len(got) != len(want) {
		t.Fatalf("fold and replay diverged: %+v vs %+v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("item %d diverged: %+v vs %+v", i, got[i], want[i])
		}
	}
	// Last write wins: the final list is the second one.
	if got := m.Get("s1"); len(got) != 2 || got[0].Content != "b" {
		t.Errorf("folded list = %+v, want last write", got)
	}
}

func TestModuleFoldIgnoresNonTasksEvents(t *testing.T) {
	m := &Module{}
	obs := m.Observer("s1", "/tmp")
	obs(agent.Event{Type: agent.EventMessage})
	obs(agent.Event{Type: agent.EventToolEnd})
	if got := m.Get("s1"); got != nil {
		t.Errorf("non-tasks events folded into a list: %+v", got)
	}
}

func TestModuleClearOnEmptyWrite(t *testing.T) {
	m := &Module{}
	m.Fold("s1", []agent.TaskItem{{Content: "a", Status: "pending"}})
	m.Fold("s1", nil)
	if got := m.Get("s1"); len(got) != 0 {
		t.Errorf("empty fold should clear, got %+v", got)
	}
}

func TestBridgeWriteEmitsAndFolds(t *testing.T) {
	m := &Module{}
	var emitted *agent.Event
	b := m.NewBridge("s1", func(ev agent.Event) { emitted = &ev })

	if err := b.Write([]tools.TaskItem{{Content: "x", Status: "pending"}}); err != nil {
		t.Fatal(err)
	}
	if emitted == nil || emitted.Type != agent.EventTasks || len(emitted.Tasks) != 1 {
		t.Fatalf("bridge write did not emit an EventTasks: %+v", emitted)
	}
	// The fold followed the emit, so Read agrees.
	if got := b.Read(); len(got) != 1 || got[0].Content != "x" {
		t.Errorf("bridge read = %+v", got)
	}
}

func TestAckCounts(t *testing.T) {
	got := Ack([]agent.TaskItem{
		{Content: "a", Status: "pending"},
		{Content: "b", Status: "in_progress"},
		{Content: "c", Status: "completed"},
	})
	if want := "Task list updated: 1 pending, 1 in progress, 1 completed."; got != want {
		t.Errorf("ack = %q, want %q", got, want)
	}
}

// recordingStore is the loop-test bridge: it records the last write so
// the test can assert what the tool handed over.
type recordingStore struct {
	last []tools.TaskItem
}

func (r *recordingStore) Write(tasks []tools.TaskItem) error {
	r.last = tasks
	return nil
}

func (r *recordingStore) Read() []tools.TaskItem { return r.last }

// TestTodoWriteThroughAgentLoop runs a todo_write call through the real
// agent loop: the tool result carries the ack, the next model call sees
// it, and the bridge received the full list.
func TestTodoWriteThroughAgentLoop(t *testing.T) {
	store := &recordingStore{}
	tw := &TodoWrite{store: store}
	fake := &fakeProvider{script: [][]provider.Event{
		toolReply(provider.ToolCall{ID: "c1", Name: "todo_write", Args: `{"todos":[{"content":"step one","status":"pending"},{"content":"step two","status":"in_progress"}]}`}),
		textReply("done"),
	}}
	reg := tools.NewRegistry(tw)
	var events []agent.Event
	a := agent.New(agent.Config{
		Provider:     fake,
		Model:        "test-model",
		Tools:        reg,
		Approver:     &denyApprover{},
		SystemPrompt: "you are a test",
		Emit: func(ev agent.Event) {
			events = append(events, ev)
		},
	})

	if err := a.Run(context.Background(), "plan the work"); err != nil {
		t.Fatal(err)
	}

	// The bridge got the full list, validated and trimmed.
	if len(store.last) != 2 || store.last[0].Content != "step one" {
		t.Errorf("bridge write = %+v", store.last)
	}

	// The tool result the model saw is the ack.
	second := fake.requests[1]
	last := second.Messages[len(second.Messages)-1]
	if last.Role != provider.RoleTool || !strings.Contains(last.Content, "1 pending, 1 in progress") {
		t.Errorf("todo_write result = %+v", last)
	}
}

// denyApprover is a minimal approver for the loop test (todo_write is
// Safe, so it should never be consulted).
type denyApprover struct{}

func (denyApprover) RequestApproval(context.Context, agent.ApprovalRequest) (agent.Decision, error) {
	return agent.DecisionDeny, nil
}

// fakeProvider replays a canned event script, one Stream call per entry.
type fakeProvider struct {
	mu       sync.Mutex
	script   [][]provider.Event
	requests []provider.Request
}

func (f *fakeProvider) Name() string         { return "fake" }
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
