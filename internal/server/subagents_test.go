package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KonMam/tether/internal/agent"
	"github.com/KonMam/tether/internal/config"
	"github.com/KonMam/tether/internal/provider"
	"github.com/KonMam/tether/internal/session"
	"github.com/KonMam/tether/internal/tools"
)

// newTestServer builds a server on a throwaway store, ready to resolve
// sessions.
func newTestServer(t *testing.T) (*Server, *session.Store) {
	t.Helper()
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := New(emptyConfig(), store, nil, nil, nil)
	t.Cleanup(s.Shutdown)
	return s, store
}

// emptyConfig is a minimal config: New requires a config.Config, and the
// janitor goroutine only touches agent.Busy on live sessions. The
// allowlist admits httptest.NewRequest's default Host past the guard.
func emptyConfig() config.Config {
	return config.Config{AllowedHosts: []string{"example.com"}}
}

// startSession creates and resolves a session so the liveSession (and its
// subagent registry) exists.
func startSession(t *testing.T, s *Server, store *session.Store) (*liveSession, string) {
	t.Helper()
	meta, err := store.Create("test-model", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ls, err := s.resolve(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	return ls, meta.ID
}

// fakeSpawner exercises the registry the way the spawn tool does: it
// registers a child and marks it terminal with a report. The child is a
// real agent.Agent so the running handle is a genuine one.
type fakeSpawner struct {
	ls *liveSession
}

func (f *fakeSpawner) spawn(t *testing.T, id, name, task string) *subagentEntry {
	t.Helper()
	// The child agent is never run by the registry tests; a nil provider
	// is fine for construction.
	child := agent.New(agent.Config{Emit: func(agent.Event) {}})
	_, cancel := context.WithCancel(context.Background())
	e := &subagentEntry{
		id:        id,
		name:      name,
		task:      task,
		startedAt: time.Now(),
		agent:     child,
		cancel:    cancel,
		status:    SubagentRunning,
	}
	f.ls.subagents.add(e)
	return e
}

// blockingProvider keeps an agent's turn in flight: each Stream call
// blocks until the context is cancelled, then terminates with the
// cancellation error exactly like the real providers do on interrupt.
type blockingProvider struct{}

func (blockingProvider) Name() string { return "blocking" }

func (blockingProvider) SupportsImages() bool { return false }

func (blockingProvider) Stream(ctx context.Context, _ provider.Request) (<-chan provider.Event, error) {
	ch := make(chan provider.Event, 1)
	go func() {
		<-ctx.Done()
		ch <- provider.Event{Kind: provider.EventError, Err: ctx.Err()}
		close(ch)
	}()
	return ch, nil
}

// busySpawner registers a child and starts a real agent turn against a
// blocking provider, so the child is genuinely Busy until interrupted.
// It returns the entry and the child's turn context (cancelled by the
// test when the turn should stop).
type busySpawner struct {
	ls   *liveSession
	done chan struct{}
}

func (b *busySpawner) spawn(t *testing.T, id, name, task string) *subagentEntry {
	t.Helper()
	child := agent.New(agent.Config{
		Provider: blockingProvider{},
		Tools:    tools.NewRegistry(),
		Emit:     func(agent.Event) {},
	})
	ctx, cancel := context.WithCancel(context.Background())
	e := &subagentEntry{
		id:        id,
		name:      name,
		task:      task,
		startedAt: time.Now(),
		agent:     child,
		cancel:    cancel,
		status:    SubagentRunning,
	}
	b.ls.subagents.add(e)
	b.done = make(chan struct{})
	go func() {
		_ = child.Run(ctx, "task")
		// The spawn tool's job: mark the entry terminal once the child
		// turn ends, exactly like spawnTool.Run does.
		e.finish(SubagentInterrupted, "interrupted by the user")
		close(b.done)
	}()
	// The child acquires its busy slot before the first Stream blocks;
	// wait for that so steer reaches a busy agent.
	deadline := time.Now().Add(5 * time.Second)
	for !child.Busy() {
		if time.Now().After(deadline) {
			t.Fatalf("child never became busy")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return e
}

func TestSubagentRegistryLifecycle(t *testing.T) {
	s, store := newTestServer(t)
	ls, id := startSession(t, s, store)

	f := &fakeSpawner{ls: ls}
	e1 := f.spawn(t, "call-1", "", "first task")
	e2 := f.spawn(t, "call-2", "code", "second task")

	// Both running before they finish.
	if got := s.liveSubagent(id, "call-1"); got != e1 {
		t.Fatalf("running lookup for call-1 = %v, want entry", got)
	}
	if got := s.liveSubagent(id, "call-2"); got != e2 {
		t.Fatalf("running lookup for call-2 = %v, want entry", got)
	}

	// Terminal transitions: done carries the report, interrupted its own.
	e1.finish(SubagentDone, "the report")
	if got := s.liveSubagent(id, "call-1"); got != nil {
		t.Fatalf("terminal entry still looked up as running")
	}
	e2.finish(SubagentInterrupted, "interrupted by the user")

	status, _, report := e1.snapshot()
	if status != SubagentDone || report != "the report" {
		t.Errorf("e1 snapshot = %s %q, want done/the report", status, report)
	}
	status, _, report = e2.snapshot()
	if status != SubagentInterrupted || report != "interrupted by the user" {
		t.Errorf("e2 snapshot = %s %q, want interrupted/interrupted by the user", status, report)
	}

	// A second finish on the same entry is a no-op (first terminal state wins).
	e1.finish(SubagentFailed, "late failure")
	status, _, report = e1.snapshot()
	if status != SubagentDone || report != "the report" {
		t.Errorf("re-finish changed snapshot to %s %q", status, report)
	}
}

func TestSubagentsEndpoint(t *testing.T) {
	s, store := newTestServer(t)
	ls, id := startSession(t, s, store)

	f := &fakeSpawner{ls: ls}
	f.spawn(t, "call-1", "", "inspect the agent loop")
	f.spawn(t, "call-2", "", "check the provider")

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+id+"/subagents", nil)
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET subagents status = %d, want 200", rr.Code)
	}
	var rows []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	// Newest first.
	if rows[0]["id"] != "call-2" || rows[1]["id"] != "call-1" {
		t.Errorf("row order = %v, %v; want call-2 then call-1", rows[0]["id"], rows[1]["id"])
	}
	for _, r := range rows {
		if r["status"] != "running" {
			t.Errorf("row %v status = %v, want running", r["id"], r["status"])
		}
		if _, ok := r["report"]; ok {
			t.Errorf("running row %v carries a report", r["id"])
		}
		if len(r["task"].(string)) == 0 {
			t.Errorf("row %v has an empty task", r["id"])
		}
	}

	// Finish both, then the endpoint shows terminal state with reports.
	ls.subagents.get("call-1").finish(SubagentDone, "report one")
	ls.subagents.get("call-2").finish(SubagentFailed, "error: boom")

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/sessions/"+id+"/subagents", nil)
	s.Handler().ServeHTTP(rr, req)
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	byID := map[string]map[string]any{}
	for _, r := range rows {
		byID[r["id"].(string)] = r
	}
	if byID["call-1"]["status"] != "done" || byID["call-1"]["report"] != "report one" {
		t.Errorf("call-1 = %v, want done with report", byID["call-1"])
	}
	if byID["call-2"]["status"] != "failed" || !strings.Contains(byID["call-2"]["report"].(string), "boom") {
		t.Errorf("call-2 = %v, want failed with error report", byID["call-2"])
	}
}

func TestSubagentRegistryCap(t *testing.T) {
	s, store := newTestServer(t)
	ls, _ := startSession(t, s, store)

	f := &fakeSpawner{ls: ls}
	for i := 0; i < subagentCap+5; i++ {
		id := "call-" + string(rune('a'+i))
		f.spawn(t, id, "", "task")
	}
	entries := ls.subagents.list()
	if len(entries) != subagentCap {
		t.Fatalf("registry size = %d, want cap %d", len(entries), subagentCap)
	}
	// The oldest five fell off (call-a..call-e); the most recent 20
	// remain, listed newest first.
	if entries[0].id != "call-y" || entries[subagentCap-1].id != "call-f" {
		t.Errorf("kept ids from %s to %s, want call-y .. call-f", entries[0].id, entries[subagentCap-1].id)
	}
	if ls.subagents.get("call-a") != nil {
		t.Errorf("evicted entry call-a still present")
	}
}

// TestSubagentSpawnInterruptReport locks the spawn tool's exit
// classification: interrupting a child must produce an
// "interrupted by the user" report on the entry, NOT a failed status.
// This is the path the task-4 acceptance depends on (the parent receives
// an interruption report, not an error).
func TestSubagentSpawnInterruptReport(t *testing.T) {
	s, store := newTestServer(t)
	ls, id := startSession(t, s, store)

	// A spawnTool with a provider that never produces events: the child
	// stays inside its first Stream until the context is cancelled.
	st := &spawnTool{
		server:    s,
		sessionID: id,
		ls:        ls,
		workdir:   t.TempDir(),
		prov:      blockingProvider{},
		model:     "test-model",
	}
	ctx, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	done := make(chan struct{})
	var result tools.Result
	var runErr error
	go func() {
		result, runErr = st.Run(ctx, json.RawMessage(`{"task":"wait"}`))
		close(done)
	}()

	// Wait for the child to register.
	var entry *subagentEntry
	deadline := time.Now().Add(5 * time.Second)
	for entry == nil {
		if time.Now().After(deadline) {
			t.Fatal("child never registered")
		}
		entries := ls.subagents.list()
		if len(entries) > 0 {
			entry = entries[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	if entry.status != SubagentRunning {
		t.Fatalf("entry status = %s, want running", entry.status)
	}

	// The child must be mid-turn for an interrupt to be meaningful; the
	// blocking provider keeps it in its first model call.
	deadline = time.Now().Add(5 * time.Second)
	for !entry.agent.Busy() {
		if time.Now().After(deadline) {
			t.Fatal("child never became busy")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Interrupt exactly like the endpoint does: cancel the child's own
	// context, not the parent's.
	entry.cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("spawn tool did not return after interrupt")
	}
	if runErr != nil {
		t.Fatalf("spawn tool returned error %v, want nil (interruption is a report, not an error)", runErr)
	}
	if result.Content != "interrupted by the user" {
		t.Errorf("tool result = %q, want %q", result.Content, "interrupted by the user")
	}
	status, _, report := entry.snapshot()
	if status != SubagentInterrupted || report != "interrupted by the user" {
		t.Errorf("entry = %s %q, want interrupted/interrupted by the user", status, report)
	}
}
func TestSubagentSteerInterruptEndpoints(t *testing.T) {
	s, store := newTestServer(t)
	ls, id := startSession(t, s, store)

	b := &busySpawner{ls: ls}
	e := b.spawn(t, "call-run", "", "run something slow")

	// Steer: 202, and the message is queued into the child's transcript.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/sessions/"+id+"/subagents/call-run/steer",
		strings.NewReader(`{"content":"focus only on X"}`),
	)
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("steer status = %d, want 202; body %s", rr.Code, rr.Body.String())
	}
	// The child is blocked in its first Stream; drainSteer only runs at a
	// round boundary, so assert the steer landed in the channel by
	// verifying the agent accepted it without error; the round boundary
	// path is the agent's own, already covered by agent-level tests.

	// Steer with empty content: 400.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(
		http.MethodPost,
		"/api/sessions/"+id+"/subagents/call-run/steer",
		strings.NewReader(`{"content":"  "}`),
	)
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("steer empty status = %d, want 400", rr.Code)
	}

	// Unknown subagent: 404.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(
		http.MethodPost,
		"/api/sessions/"+id+"/subagents/call-nope/steer",
		strings.NewReader(`{"content":"hi"}`),
	)
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("steer unknown status = %d, want 404", rr.Code)
	}

	// Unknown session: 404.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(
		http.MethodPost,
		"/api/sessions/no-such-session/subagents/call-run/steer",
		strings.NewReader(`{"content":"hi"}`),
	)
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("steer unknown session status = %d, want 404", rr.Code)
	}

	// Interrupt: 204, and the child turn ends.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(
		http.MethodPost,
		"/api/sessions/"+id+"/subagents/call-run/interrupt",
		nil,
	)
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("interrupt status = %d, want 204", rr.Code)
	}
	select {
	case <-b.done:
	case <-time.After(5 * time.Second):
		t.Fatal("child turn did not end after interrupt")
	}

	// Interrupting a terminal id is a 404.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(
		http.MethodPost,
		"/api/sessions/"+id+"/subagents/call-run/interrupt",
		nil,
	)
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("interrupt terminal status = %d, want 404", rr.Code)
	}

	// Steering a terminal id is a 404 too.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(
		http.MethodPost,
		"/api/sessions/"+id+"/subagents/call-run/steer",
		strings.NewReader(`{"content":"hi"}`),
	)
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("steer terminal status = %d, want 404", rr.Code)
	}

	// The child still holds its entry, now terminal with the interrupt
	// report, the same shape the spawn tool produces.
	status, _, report := e.snapshot()
	if status != SubagentInterrupted || report != "interrupted by the user" {
		t.Errorf("entry snapshot = %s %q, want interrupted/interrupted by the user", status, report)
	}
}

// TestSendMessageRejectsImagesOnTextOnlyProvider covers the image-upload
// gate: a session whose provider does not accept image parts must get a
// clear 400 before any turn starts, and the transcript stays untouched.
func TestSendMessageRejectsImagesOnTextOnlyProvider(t *testing.T) {
	s, store := newTestServer(t)
	meta, err := store.Create("test-model", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	meta.Profile = "deepseek"
	if err := store.SaveMeta(meta); err != nil {
		t.Fatal(err)
	}

	body := `{"content":"what is this","images":["data:image/png;base64,AAAA"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+meta.ID+"/messages", strings.NewReader(body))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "does not accept images") {
		t.Errorf("error = %s, want a clear image-capability message", rr.Body.String())
	}

	msgs, err := store.Messages(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Errorf("transcript has %d messages, want 0 (rejected message must not persist)", len(msgs))
	}
}

// TestSendMessageAllowsImagesOnOllamaProvider: the local endpoint claims
// vision, so an image-carrying message passes the gate and starts a turn.
// The default provider points at a fast-failing local server so the turn
// and the title generation end immediately, without a live model.
func TestSendMessageAllowsImagesOnOllamaProvider(t *testing.T) {
	failFast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no model here", http.StatusBadRequest)
	}))
	defer failFast.Close()

	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := New(config.Config{BaseURL: failFast.URL + "/v1", AllowedHosts: []string{"example.com"}}, store, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(s.Shutdown)
	meta, err := store.Create("test-model", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	meta.Profile = "" // default profile → default endpoint (localhost)
	if err := store.SaveMeta(meta); err != nil {
		t.Fatal(err)
	}

	body := `{"content":"what is this","images":["data:image/png;base64,AAAA"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+meta.ID+"/messages", strings.NewReader(body))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", rr.Code, rr.Body.String())
	}

	// The turn runs in a background goroutine that keeps writing after
	// the agent reports not busy (title generation). Wait until the
	// title lands, so the store's temp dir is quiescent before cleanup.
	ls, err := s.resolve(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m, _ := store.GetMeta(meta.ID)
		if !ls.agent.Busy() && m != nil && m.Title != "new session" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	m, _ := store.GetMeta(meta.ID)
	if ls.agent.Busy() || m == nil || m.Title == "new session" {
		t.Fatal("turn did not finish")
	}
}

// TestProviderForOllamaVision covers the endpoint classification that
// decides image support: local hosts claim vision, everything else is
// text-only.
func TestProviderForOllamaVision(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want bool
	}{
		{"localhost", "http://localhost:11434/v1", true},
		{"127.0.0.1", "http://127.0.0.1:11434/v1", true},
		{"ipv6 loopback", "http://[::1]:11434/v1", true},
		{"deepseek", "https://api.deepseek.com/v1", false},
		{"remote ollama host", "http://192.168.1.5:11434/v1", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &Server{cfg: config.Config{BaseURL: c.url}}
			meta := &session.Meta{Model: "m", Profile: ""}
			if got := s.providerFor(meta).SupportsImages(); got != c.want {
				t.Errorf("SupportsImages(%q) = %v, want %v", c.url, got, c.want)
			}
		})
	}
}
