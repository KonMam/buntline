package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KonMam/buntline/internal/agent"
	"github.com/KonMam/buntline/internal/config"
	"github.com/KonMam/buntline/internal/module"
	"github.com/KonMam/buntline/internal/module/tasks"
	"github.com/KonMam/buntline/internal/provider"
	"github.com/KonMam/buntline/internal/session"
)

// tasksRegistry is a real module registry carrying the tasks module, the
// way main.go assembles it.
func tasksRegistry(t *testing.T) *module.Registry {
	t.Helper()
	reg, err := module.NewRegistry(filepath.Join(t.TempDir(), "modules.json"), &tasks.Module{})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// TestTasksEndToEnd covers the full tasks path: the module registered,
// a session attached (bridge wired, events replayed), a todo_write
// through the wired tool persisting an EventTasks and folding the list,
// the /api/m/tasks/list route serving it, and a fresh server replaying
// the same list from the persisted events after "restart".
func TestTasksEndToEnd(t *testing.T) {
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := New(emptyConfig(), store, nil, tasksRegistry(t), nil)
	t.Cleanup(s.Shutdown)

	meta, err := store.Create("test-model", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ls, err := s.resolve(meta.ID)
	if err != nil {
		t.Fatal(err)
	}

	// The wired todo_write tool: attach set its store bridge.
	tw, ok := ls.registry.Get("todo_write")
	if !ok {
		t.Fatal("todo_write tool missing from the session registry")
	}
	res, err := tw.Run(context.Background(), json.RawMessage(
		`{"todos":[{"content":"read the code","status":"pending"},{"content":"write the tests","status":"completed"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "Task list updated: 1 pending, 0 in progress, 1 completed." {
		t.Errorf("ack = %q", res.Content)
	}

	// The event persisted to the session's events.jsonl.
	evs, err := store.Events(meta.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, ev := range evs {
		if ev.Type == agent.EventTasks && len(ev.Tasks) == 2 {
			found = true
			if ev.Tasks[0].Content != "read the code" || ev.Tasks[0].Status != "pending" {
				t.Errorf("persisted tasks = %+v", ev.Tasks)
			}
		}
	}
	if !found {
		t.Fatal("no EventTasks in the persisted event log")
	}

	// The module route serves the folded list.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/m/tasks/list?session="+meta.ID, nil)
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("tasks route = %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}
	var body struct {
		Tasks  []agent.TaskItem `json:"tasks"`
		Counts map[string]int   `json:"counts"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Tasks) != 2 || body.Counts["pending"] != 1 || body.Counts["completed"] != 1 {
		t.Errorf("route body = %+v", body)
	}

	// "Restart": a fresh server and module over the same store must
	// replay the list from persisted events, no live write needed.
	s2 := New(emptyConfig(), store, nil, tasksRegistry(t), nil)
	t.Cleanup(s2.Shutdown)
	ls2, err := s2.resolve(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	tr, ok := ls2.registry.Get("todo_read")
	if !ok {
		t.Fatal("todo_read tool missing after restart")
	}
	res2, err := tr.Run(context.Background(), json.RawMessage("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if res2.Content != "[pending] read the code\n[completed] write the tests" {
		t.Errorf("replayed todo_read = %q", res2.Content)
	}

	// The route agrees after replay too.
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/api/m/tasks/list?session="+meta.ID, nil)
	s2.Handler().ServeHTTP(rr2, req2)
	if !json.Valid(rr2.Body.Bytes()) || rr2.Code != http.StatusOK {
		t.Fatalf("replayed route = %d", rr2.Code)
	}
}

// TestTasksRouteDisabled covers the module toggle: with the tasks
// module disabled, the route 404s exactly like every other module.
func TestTasksRouteDisabled(t *testing.T) {
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := tasksRegistry(t)
	s := New(emptyConfig(), store, nil, reg, nil)
	t.Cleanup(s.Shutdown)

	meta, err := store.Create("test-model", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.resolve(meta.ID); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/m/tasks/list?session="+meta.ID, nil)
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("enabled route = %d, want 200", rr.Code)
	}

	if err := reg.SetEnabled("tasks", false); err != nil {
		t.Fatal(err)
	}
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/m/tasks/list?session="+meta.ID, nil)
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("disabled route = %d, want 404", rr.Code)
	}
}

// fakeTasksProvider is a minimal provider for the compaction test: the
// compact call streams a summary; nothing else calls it. Mirrors the
// agent package's fakeProvider without importing it (internal package).
type fakeTasksProvider struct{}

func (f *fakeTasksProvider) Name() string         { return "fake" }
func (f *fakeTasksProvider) SupportsImages() bool { return false }

func (f *fakeTasksProvider) Stream(_ context.Context, _ provider.Request) (<-chan provider.Event, error) {
	ch := make(chan provider.Event, 3)
	ch <- provider.Event{Kind: provider.EventTextDelta, Text: "SUMMARY OF EVERYTHING"}
	ch <- provider.Event{Kind: provider.EventUsage, Usage: &provider.Usage{PromptTokens: 100, CompletionTokens: 20}}
	ch <- provider.Event{Kind: provider.EventDone, FinishReason: "stop"}
	close(ch)
	return ch, nil
}

// TestTasksSurviveCompaction is Task 3's core guarantee: compaction
// rewrites the transcript (transcript.jsonl) but must not drop the
// tasks event from the activity log (events.jsonl), and a fresh server
// must still replay the list from the persisted events afterwards.
func TestTasksSurviveCompaction(t *testing.T) {
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := tasksRegistry(t)
	s := New(emptyConfig(), store, nil, reg, nil)
	t.Cleanup(s.Shutdown)

	meta, err := store.Create("test-model", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ls, err := s.resolve(meta.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Seed the transcript so compact has something to summarize (it
	// refuses an empty history), then write a task list through the
	// wired tool so an EventTasks is on the log.
	ls.agent.SetMessages([]provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "hello"},
	})
	tw, ok := ls.registry.Get("todo_write")
	if !ok {
		t.Fatal("todo_write tool missing")
	}
	if _, err := tw.Run(context.Background(), json.RawMessage(
		`{"todos":[{"content":"survive compaction","status":"pending"}]}`)); err != nil {
		t.Fatal(err)
	}

	// Compact through the session's agent with a fake provider.
	if err := ls.agent.SetProvider(&fakeTasksProvider{}); err != nil {
		t.Fatal(err)
	}
	if err := ls.agent.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The compact event persisted, and the EventTasks is still on the
	// activity log alongside it (nothing was dropped).
	evs, err := store.Events(meta.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var sawCompact, sawTasks bool
	for _, ev := range evs {
		switch ev.Type {
		case agent.EventCompact:
			sawCompact = true
		case agent.EventTasks:
			sawTasks = true
			if len(ev.Tasks) != 1 || ev.Tasks[0].Content != "survive compaction" {
				t.Errorf("tasks event after compaction = %+v", ev.Tasks)
			}
		}
	}
	if !sawCompact {
		t.Fatal("compact event not persisted")
	}
	if !sawTasks {
		t.Fatal("EventTasks dropped by compaction")
	}

	// A fresh server replays the list from the persisted events.
	s2 := New(emptyConfig(), store, nil, tasksRegistry(t), nil)
	t.Cleanup(s2.Shutdown)
	ls2, err := s2.resolve(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	tr, ok := ls2.registry.Get("todo_read")
	if !ok {
		t.Fatal("todo_read missing after restart")
	}
	res, err := tr.Run(context.Background(), json.RawMessage("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "[pending] survive compaction" {
		t.Errorf("replayed list after compaction = %q", res.Content)
	}
}

// TestTasksDisableReenableRestores covers the module toggle's effect on
// the list through the real HTTP path: disabling detaches idle sessions
// and unmounts the tools and route, but the events stay on the log;
// re-enabling and re-attaching restores the list from the persisted
// events.
func TestTasksDisableReenableRestores(t *testing.T) {
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := tasksRegistry(t)
	s := New(emptyConfig(), store, nil, reg, nil)
	t.Cleanup(s.Shutdown)

	meta, err := store.Create("test-model", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ls, err := s.resolve(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	tw, ok := ls.registry.Get("todo_write")
	if !ok {
		t.Fatal("todo_write tool missing")
	}
	if _, err := tw.Run(context.Background(), json.RawMessage(
		`{"todos":[{"content":"persist through toggles","status":"in_progress"}]}`)); err != nil {
		t.Fatal(err)
	}

	// Disable through the HTTP toggle (the path the UI uses): idle
	// sessions detach, so their next touch rebuilds the registry without
	// the tasks tools.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/modules/tasks",
		strings.NewReader(`{"enabled":false}`))
	req.Header.Set("Content-Type", "application/json")
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("disable toggle = %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}

	// The route now 404s.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/m/tasks/list?session="+meta.ID, nil)
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("disabled route = %d, want 404", rr.Code)
	}

	// A fresh resolve rebuilds the session without the tasks tools.
	ls2, err := s.resolve(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ls2.registry.Get("todo_write"); ok {
		t.Fatal("todo_write should be unmounted while disabled")
	}

	// Re-enable through the HTTP toggle, then re-attach: the list comes
	// back from the persisted events, no live write needed.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/modules/tasks",
		strings.NewReader(`{"enabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("re-enable toggle = %d, want 200", rr.Code)
	}
	ls3, err := s.resolve(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	tr, ok := ls3.registry.Get("todo_read")
	if !ok {
		t.Fatal("todo_read missing after re-enable")
	}
	res, err := tr.Run(context.Background(), json.RawMessage("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "[in_progress] persist through toggles" {
		t.Errorf("restored list = %q", res.Content)
	}

	// The route agrees.
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/api/m/tasks/list?session="+meta.ID, nil)
	s.Handler().ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("re-enabled route = %d, want 200", rr2.Code)
	}
}

// TestTasksClearOnFinishedTurn covers the auto-clear: a main-loop turn
// ending with an all-completed list writes an empty EventTasks through
// the bridge (folded, persisted, and replayed), so the next request
// starts clean; a list with pending or in-progress items is a live plan
// and survives the turn.
func TestTasksClearOnFinishedTurn(t *testing.T) {
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := New(emptyConfig(), store, nil, tasksRegistry(t), nil)
	t.Cleanup(s.Shutdown)

	meta, err := store.Create("test-model", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ls, err := s.resolve(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	tw, ok := ls.registry.Get("todo_write")
	if !ok {
		t.Fatal("todo_write tool missing")
	}
	if _, err := tw.Run(context.Background(), json.RawMessage(
		`{"todos":[{"content":"done thing","status":"completed"},{"content":"also done","status":"completed"}]}`)); err != nil {
		t.Fatal(err)
	}

	// The main loop ends the turn through the real dispatch path: the
	// all-completed list must clear, folding and persisting an empty
	// EventTasks.
	s.dispatch(meta.ID, ls, agent.Event{Type: agent.EventTurnEnd, StopReason: "done"})
	tr, ok := ls.registry.Get("todo_read")
	if !ok {
		t.Fatal("todo_read tool missing")
	}
	res, err := tr.Run(context.Background(), json.RawMessage("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "No tasks yet" {
		t.Errorf("list after finished turn = %q, want cleared", res.Content)
	}

	// The clear was persisted as an empty EventTasks, so a fresh server
	// replays an empty list rather than the stale completed items.
	evs, err := store.Events(meta.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var lastTasks *agent.Event
	for i := range evs {
		if evs[i].Type == agent.EventTasks {
			lastTasks = &evs[i]
		}
	}
	if lastTasks == nil || len(lastTasks.Tasks) != 0 {
		t.Fatalf("last tasks event = %+v, want an empty list", lastTasks)
	}

	s2 := New(emptyConfig(), store, nil, tasksRegistry(t), nil)
	t.Cleanup(s2.Shutdown)
	ls2, err := s2.resolve(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	tr2, ok := ls2.registry.Get("todo_read")
	if !ok {
		t.Fatal("todo_read missing after restart")
	}
	res2, err := tr2.Run(context.Background(), json.RawMessage("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if res2.Content != "No tasks yet" {
		t.Errorf("replayed list = %q, want cleared", res2.Content)
	}

	// A live plan survives the turn: in-progress items are not cleared.
	if _, err := tw.Run(context.Background(), json.RawMessage(
		`{"todos":[{"content":"ongoing work","status":"in_progress"}]}`)); err != nil {
		t.Fatal(err)
	}
	s.dispatch(meta.ID, ls, agent.Event{Type: agent.EventTurnEnd, StopReason: "done"})
	res3, err := tr.Run(context.Background(), json.RawMessage("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if res3.Content != "[in_progress] ongoing work" {
		t.Errorf("live plan after turn = %q, want kept", res3.Content)
	}
}

// TestTasksClearRespectsDisable: with the tasks module disabled, a
// finished turn leaves the folded list alone (no clear EventTasks is
// written), exactly as if the module were absent.
func TestTasksClearRespectsDisable(t *testing.T) {
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := tasksRegistry(t)
	s := New(emptyConfig(), store, nil, reg, nil)
	t.Cleanup(s.Shutdown)

	meta, err := store.Create("test-model", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ls, err := s.resolve(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	tw, ok := ls.registry.Get("todo_write")
	if !ok {
		t.Fatal("todo_write tool missing")
	}
	if _, err := tw.Run(context.Background(), json.RawMessage(
		`{"todos":[{"content":"stale","status":"completed"}]}`)); err != nil {
		t.Fatal(err)
	}

	if err := reg.SetEnabled("tasks", false); err != nil {
		t.Fatal(err)
	}
	s.dispatch(meta.ID, ls, agent.Event{Type: agent.EventTurnEnd, StopReason: "done"})

	tm, ok := reg.Get("tasks").(*tasks.Module)
	if !ok {
		t.Fatal("tasks module missing from registry")
	}
	if got := tm.Get(meta.ID); len(got) != 1 || got[0].Content != "stale" {
		t.Errorf("disabled module list after turn = %+v, want untouched", got)
	}
}

// TestPlanModeGuidance is Task 4: plan mode is an existing approval
// policy (read-only), and the module's only prompt change is a short
// guidance line gated on that mode. Inactive modes add zero tokens; the
// line lives in the system message, which is rebuilt on attach and never
// persisted to the transcript, so it survives resume and mode switches.
func TestPlanModeGuidance(t *testing.T) {
	// sessionPrompt itself: only plan mode gets the line.
	base := config.SystemPrompt(t.TempDir())
	if got := sessionPrompt(base, "ask"); got != base {
		t.Errorf("ask mode changed the prompt:\n%q", got)
	}
	if got := sessionPrompt(base, "auto"); got != base {
		t.Errorf("auto mode changed the prompt:\n%q", got)
	}
	plan := sessionPrompt(base, "plan")
	if plan == base || !strings.Contains(plan, "You are in plan mode") {
		t.Errorf("plan mode missing the guidance line:\n%q", plan)
	}

	// A session created in plan mode attaches with the line in its
	// system message.
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := tasksRegistry(t)
	s := New(emptyConfig(), store, nil, reg, nil)
	t.Cleanup(s.Shutdown)

	meta, err := store.Create("test-model", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	meta.Mode = "plan"
	if err := store.SaveMeta(meta); err != nil {
		t.Fatal(err)
	}
	ls, err := s.resolve(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	msgs := ls.agent.Messages()
	if len(msgs) == 0 || msgs[0].Role != provider.RoleSystem {
		t.Fatalf("no system message after attach: %+v", msgs)
	}
	if !strings.Contains(msgs[0].Content, "You are in plan mode") {
		t.Errorf("attached plan session system prompt missing guidance: %q", msgs[0].Content)
	}
	if !strings.Contains(msgs[0].Content, "todo_write") {
		t.Errorf("guidance should point at the task list: %q", msgs[0].Content)
	}

	// A non-plan session gets no line.
	meta2, err := store.Create("test-model", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ls2, err := s.resolve(meta2.ID)
	if err != nil {
		t.Fatal(err)
	}
	msgs2 := ls2.agent.Messages()
	if len(msgs2) == 0 || msgs2[0].Role != provider.RoleSystem {
		t.Fatalf("no system message: %+v", msgs2)
	}
	if strings.Contains(msgs2[0].Content, "You are in plan mode") {
		t.Errorf("ask session should not carry the plan line: %q", msgs2[0].Content)
	}
}

// TestPlanModeToggleGuidanceLive checks the HTTP mode toggle updates the
// attached session's system prompt live, and a fresh attach rebuilds it
// from the persisted mode (the line is never in the transcript).
func TestPlanModeToggleGuidanceLive(t *testing.T) {
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := tasksRegistry(t)
	s := New(emptyConfig(), store, nil, reg, nil)
	t.Cleanup(s.Shutdown)

	meta, err := store.Create("test-model", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ls, err := s.resolve(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	msgs := ls.agent.Messages()
	if len(msgs) == 0 || msgs[0].Role != provider.RoleSystem {
		t.Fatalf("no system message: %+v", msgs)
	}
	if strings.Contains(msgs[0].Content, "You are in plan mode") {
		t.Fatalf("fresh session should not be in plan mode: %q", msgs[0].Content)
	}

	// Toggle to plan through the real HTTP endpoint.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+meta.ID+"/mode",
		strings.NewReader(`{"mode":"plan"}`))
	req.Header.Set("Content-Type", "application/json")
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("mode toggle = %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}
	msgs = ls.agent.Messages()
	if len(msgs) == 0 || !strings.Contains(msgs[0].Content, "You are in plan mode") {
		t.Errorf("live toggle did not add the plan line: %q", msgs[0].Content)
	}

	// Toggle back to ask removes it.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/sessions/"+meta.ID+"/mode",
		strings.NewReader(`{"mode":"ask"}`))
	req.Header.Set("Content-Type", "application/json")
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("mode toggle = %d, want 200", rr.Code)
	}
	msgs = ls.agent.Messages()
	if len(msgs) == 0 || strings.Contains(msgs[0].Content, "You are in plan mode") {
		t.Errorf("live toggle did not remove the plan line: %q", msgs[0].Content)
	}

	// The transcript never carries the line: a fresh server re-attach
	// rebuilds it from meta.Mode only.
	s2 := New(emptyConfig(), store, nil, tasksRegistry(t), nil)
	t.Cleanup(s2.Shutdown)
	ls2, err := s2.resolve(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	msgs2 := ls2.agent.Messages()
	if len(msgs2) == 0 || msgs2[0].Role != provider.RoleSystem {
		t.Fatalf("no system message after re-attach: %+v", msgs2)
	}
	if strings.Contains(msgs2[0].Content, "You are in plan mode") {
		t.Errorf("re-attached ask session should not carry the plan line: %q", msgs2[0].Content)
	}
}
