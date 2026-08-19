package server

import (
	"testing"
	"time"

	"github.com/KonMam/buntline/internal/agent"
)

// TestAutoApprovalNeverAnnounced: approvals the session mode resolves
// without a human (auto, auto_edit for the edit tools, plan) must not
// raise approval_request events on the global stream. That emission used
// to happen before the approver decided, so every auto-approved call
// flashed "needs you" in the bell, attention banner, and OS popups.
func TestAutoApprovalNeverAnnounced(t *testing.T) {
	s, store := newTestServer(t)
	ls, id := startSession(t, s, store)
	h := &httpApprover{server: s, sessionID: id, ls: ls}

	sub := s.globalHub.subscribe()
	defer s.globalHub.unsubscribe(sub)

	cases := []struct {
		mode string
		req  agent.ApprovalRequest
		want agent.Decision
	}{
		{mode: "auto", req: agent.ApprovalRequest{ID: "a1", ToolName: "bash", ToolArgs: `{"command":"rm -rf x"}`}, want: agent.DecisionAllow},
		{mode: "auto_edit", req: agent.ApprovalRequest{ID: "a2", ToolName: "write_file", ToolArgs: `{"path":"x"}`}, want: agent.DecisionAllow},
		// auto_edit still asks for non-edit tools — that case is covered by
		// TestAskApprovalAnnounced below via the default mode.
		{mode: "plan", req: agent.ApprovalRequest{ID: "a3", ToolName: "bash", ToolArgs: `{"command":"ls"}`}, want: agent.DecisionDeny},
	}
	for _, c := range cases {
		ls.setMode(c.mode)
		h.AnnounceApproval(c.req)
		d, auto := h.autoDecision(c.req)
		if !auto {
			t.Fatalf("mode %s: expected an automatic decision", c.mode)
		}
		if d != c.want {
			t.Fatalf("mode %s: decision = %s, want %s", c.mode, d, c.want)
		}
	}

	// None of the auto-resolved requests may have reached the global
	// stream. Broadcast is synchronous and the channel is buffered, so a
	// stray event would already be waiting.
	select {
	case ev := <-sub:
		t.Fatalf("auto-approved approval was announced on the global stream: %+v", ev)
	default:
	}
}

// TestAskApprovalAnnounced: an approval that really needs a human is
// announced exactly once, on the session's id, before the round-trip
// parks its channel.
func TestAskApprovalAnnounced(t *testing.T) {
	s, store := newTestServer(t)
	ls, id := startSession(t, s, store)
	ls.setMode("ask") // explicit; "" means the same thing
	h := &httpApprover{server: s, sessionID: id, ls: ls}

	sub := s.globalHub.subscribe()
	defer s.globalHub.unsubscribe(sub)

	h.AnnounceApproval(agent.ApprovalRequest{ID: "a1", ToolName: "bash", ToolArgs: `{"command":"ls"}`})

	select {
	case ev := <-sub:
		if ev.SessionID != id {
			t.Fatalf("event session = %q, want %q", ev.SessionID, id)
		}
		if ev.Event.Type != agent.EventApprovalRequest || ev.Event.ApprovalID != "a1" {
			t.Fatalf("event = %+v, want approval_request a1", ev.Event)
		}
	case <-time.After(time.Second):
		t.Fatal("no approval_request on the global stream for a human approval")
	}
}
