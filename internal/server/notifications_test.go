package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KonMam/buntline/internal/agent"
)

// TestHubFanout: a hub delivers every broadcast to every subscriber, and
// subscribers leave cleanly.
func TestHubFanout(t *testing.T) {
	h := newHub[int]()

	a := h.subscribe()
	b := h.subscribe()
	defer h.unsubscribe(a)
	defer h.unsubscribe(b)

	h.broadcast(1)
	h.broadcast(2)

	for _, ch := range []chan int{a, b} {
		if got := <-ch; got != 1 {
			t.Errorf("first event = %d, want 1", got)
		}
		if got := <-ch; got != 2 {
			t.Errorf("second event = %d, want 2", got)
		}
	}
	if got := h.count(); got != 2 {
		t.Errorf("count = %d, want 2", got)
	}
}

// TestHubDropSlowSubscriber: an unread subscriber must not wedge
// broadcast; the producer keeps going and only the events that fit the
// buffer reach the slow reader.
func TestHubDropSlowSubscriber(t *testing.T) {
	h := newHub[int]()
	slow := h.subscribe() // never read
	defer h.unsubscribe(slow)

	for i := 0; i < 512; i++ {
		h.broadcast(i) // no block, no panic
	}
	if got := h.count(); got != 1 {
		t.Errorf("count = %d, want 1", got)
	}
}

// TestGlobalHubWiring: dispatch fans every session's non-delta events
// into the global hub, tagged with the session id; text deltas stay off
// the global stream.
func TestGlobalHubWiring(t *testing.T) {
	s, store := newTestServer(t)
	ls, id := startSession(t, s, store)
	ls2, id2 := startSession(t, s, store)

	sub := s.globalHub.subscribe()
	defer s.globalHub.unsubscribe(sub)

	// Two real events through the dispatch path the agent uses.
	s.dispatch(id, ls, agent.Event{
		Type: agent.EventQuestionRequest, Time: time.Now(), ApprovalID: "q1",
	})
	s.dispatch(id2, ls2, agent.Event{
		Type: agent.EventTurnEnd, Time: time.Now(), TurnID: "t2",
	})
	// A token delta must NOT appear on the global stream.
	s.dispatch(id, ls, agent.Event{
		Type: agent.EventTextDelta, Time: time.Now(), Text: "chatter",
	})

	var got []globalEvent
	deadline := time.Now().Add(2 * time.Second)
	for len(got) < 2 {
		select {
		case ge := <-sub:
			got = append(got, ge)
		case <-time.After(time.Until(deadline)):
			t.Fatalf("timed out with %d events", len(got))
		}
	}
	byType := map[agent.EventType]string{}
	for _, ge := range got {
		byType[ge.Event.Type] = ge.SessionID
	}
	if byType[agent.EventQuestionRequest] != id {
		t.Errorf("question event session = %q, want %q", byType[agent.EventQuestionRequest], id)
	}
	if byType[agent.EventTurnEnd] != id2 {
		t.Errorf("turn-end event session = %q, want %q", byType[agent.EventTurnEnd], id2)
	}

	// Nothing else (in particular no delta) follows in a short window.
	select {
	case ge := <-sub:
		t.Fatalf("unexpected extra global event %+v (delta must not fan out)", ge)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestGlobalEventsEndpoint: the /api/events endpoint serves the global
// stream as SSE, one tagged frame per event.
func TestGlobalEventsEndpoint(t *testing.T) {
	s, store := newTestServer(t)
	ls, id := startSession(t, s, store)
	ls2, id2 := startSession(t, s, store)

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}

	// Wait for the endpoint to subscribe before sending, so no event is
	// dropped on the slow-start race.
	deadline := time.Now().Add(5 * time.Second)
	for s.globalHub.count() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("global endpoint never subscribed")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Read frames off the live stream in a goroutine; the test collects
	// them by deadline.
	type frame struct {
		ge  globalEvent
		err error
	}
	frames := make(chan frame, 8)
	go func() {
		br := bufio.NewReader(resp.Body)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				frames <- frame{err: err}
				return
			}
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			var ge globalEvent
			if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &ge); err != nil {
				frames <- frame{err: err}
				return
			}
			frames <- frame{ge: ge}
		}
	}()

	s.dispatch(id, ls, agent.Event{
		Type: agent.EventQuestionRequest, Time: time.Now(), ApprovalID: "q1",
	})
	s.dispatch(id2, ls2, agent.Event{
		Type: agent.EventTurnEnd, Time: time.Now(), TurnID: "t2",
	})

	var got []globalEvent
	deadline = time.Now().Add(5 * time.Second)
	for len(got) < 2 {
		select {
		case f := <-frames:
			if f.err != nil {
				t.Fatalf("stream ended after %d events: %v", len(got), f.err)
			}
			got = append(got, f.ge)
		case <-time.After(time.Until(deadline)):
			t.Fatalf("timed out with %d events", len(got))
		}
	}

	byType := map[agent.EventType]string{}
	for _, ge := range got {
		byType[ge.Event.Type] = ge.SessionID
	}
	if byType[agent.EventQuestionRequest] != id {
		t.Errorf("question event session = %q, want %q", byType[agent.EventQuestionRequest], id)
	}
	if byType[agent.EventTurnEnd] != id2 {
		t.Errorf("turn-end event session = %q, want %q", byType[agent.EventTurnEnd], id2)
	}
	cancel() // close the stream; the reader goroutine exits on EOF
}

// TestSessionListWaitingState: a session paused on an approval or
// question reports waiting; resolving the card clears it.
func TestSessionListWaitingState(t *testing.T) {
	s, store := newTestServer(t)
	ls, id := startSession(t, s, store)

	if got := s.waitingFor(id); got != "" {
		t.Fatalf("waiting before any event = %q, want empty", got)
	}

	// An open approval card makes the session "waiting: approval".
	s.dispatch(id, ls, agent.Event{
		Type: agent.EventApprovalRequest, Time: time.Now(), ApprovalID: "a1",
	})
	if got := s.waitingFor(id); got != "approval" {
		t.Fatalf("waiting with approval = %q, want approval", got)
	}

	// The approval resolves; the card closes and waiting clears.
	s.dispatch(id, ls, agent.Event{
		Type: agent.EventApprovalResult, Time: time.Now(), ApprovalID: "a1", Decision: "allow",
	})
	if got := s.waitingFor(id); got != "" {
		t.Fatalf("waiting after approval result = %q, want empty", got)
	}

	// An open question card reports "question".
	s.dispatch(id, ls, agent.Event{
		Type: agent.EventQuestionRequest, Time: time.Now(), ApprovalID: "q1",
	})
	if got := s.waitingFor(id); got != "question" {
		t.Fatalf("waiting with question = %q, want question", got)
	}
}

// TestSessionListEndpointWaitingField: the /api/sessions payload carries
// the waiting field so the sidebar can badge sessions that need the
// user.
func TestSessionListEndpointWaitingField(t *testing.T) {
	s, store := newTestServer(t)
	ls, id := startSession(t, s, store)

	s.dispatch(id, ls, agent.Event{
		Type: agent.EventApprovalRequest, Time: time.Now(), ApprovalID: "a1",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var rows []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if got := rows[0]["waiting"]; got != "approval" {
		t.Errorf("waiting = %v, want approval", got)
	}
}
