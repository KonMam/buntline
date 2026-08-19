package provider

import (
	"strings"
	"testing"
)

func TestRepairToolPairingLeavesValidTranscriptAlone(t *testing.T) {
	msgs := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "a"}, {ID: "b"}}},
		{Role: RoleTool, ToolCallID: "a", Content: "ra"},
		{Role: RoleTool, ToolCallID: "b", Content: "rb"},
		{Role: RoleAssistant, Content: "done"},
	}
	got, notes := RepairToolPairing(msgs)
	if len(notes) != 0 {
		t.Errorf("notes = %v, want none", notes)
	}
	if len(got) != len(msgs) {
		t.Errorf("len = %d, want %d", len(got), len(msgs))
	}
}

// Results may come back in any order within their block; that is valid
// and must not be "repaired".
func TestRepairToolPairingAcceptsOutOfOrderResults(t *testing.T) {
	msgs := []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "a"}, {ID: "b"}}},
		{Role: RoleTool, ToolCallID: "b", Content: "rb"},
		{Role: RoleTool, ToolCallID: "a", Content: "ra"},
	}
	if _, notes := RepairToolPairing(msgs); len(notes) != 0 {
		t.Errorf("notes = %v, want none", notes)
	}
}

// The bug that bricked a stable session: a backgrounded tool's
// placeholder and its real result both answering one tool_call.
func TestRepairToolPairingDuplicateAnswer(t *testing.T) {
	msgs := []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "a"}}},
		{Role: RoleTool, ToolCallID: "a", Content: "[started bash]"},
		{Role: RoleTool, ToolCallID: "a", Content: "real result"},
	}
	got, notes := RepairToolPairing(msgs)
	if len(notes) != 1 {
		t.Fatalf("notes = %v, want 1", notes)
	}
	if got[2].Role != RoleUser {
		t.Errorf("duplicate answer role = %s, want user", got[2].Role)
	}
	if got[2].Kind != "tool_result" {
		t.Errorf("duplicate answer kind = %q, want %q", got[2].Kind, "tool_result")
	}
	if !strings.Contains(got[2].Content, "real result") {
		t.Errorf("repair lost the result: %q", got[2].Content)
	}
	assertValid(t, got)
}

func TestRepairToolPairingUnansweredCallGetsStub(t *testing.T) {
	msgs := []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "a"}, {ID: "b"}}},
		{Role: RoleTool, ToolCallID: "a", Content: "ra"},
		{Role: RoleUser, Content: "never mind"},
	}
	got, notes := RepairToolPairing(msgs)
	if len(notes) != 1 {
		t.Fatalf("notes = %v, want 1", notes)
	}
	if got[2].Role != RoleTool || got[2].ToolCallID != "b" {
		t.Errorf("stub = %+v, want a tool result for b", got[2])
	}
	if got[3].Role != RoleUser {
		t.Errorf("stub was not inserted before the user message: %+v", got[3])
	}
	assertValid(t, got)
}

// A trailing tool_call with no result at all: an interrupted turn.
func TestRepairToolPairingTrailingCallGetsStub(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "go"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "a"}}},
	}
	got, notes := RepairToolPairing(msgs)
	if len(notes) != 1 {
		t.Fatalf("notes = %v, want 1", notes)
	}
	assertValid(t, got)
}

// A tool result whose call is gone entirely (windowing or compaction cut
// the assistant message that opened it).
func TestRepairToolPairingOrphanResult(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "go"},
		{Role: RoleTool, ToolCallID: "ghost", Content: "orphan output"},
	}
	got, notes := RepairToolPairing(msgs)
	if len(notes) != 1 {
		t.Fatalf("notes = %v, want 1", notes)
	}
	if got[1].Role != RoleUser || !strings.Contains(got[1].Content, "orphan output") {
		t.Errorf("orphan = %+v, want its text kept as a user message", got[1])
	}
	if got[1].Kind != "tool_result" {
		t.Errorf("orphan result kind = %q, want %q", got[1].Kind, "tool_result")
	}
	assertValid(t, got)
}

// assertValid enforces the invariant the backend enforces: each tool
// message answers an open call, each call is answered exactly once.
func assertValid(t *testing.T, msgs []Message) {
	t.Helper()
	open := map[string]bool{}
	for i, m := range msgs {
		switch {
		case m.Role == RoleTool:
			if !open[m.ToolCallID] {
				t.Errorf("message %d: tool result %q answers no open call", i, m.ToolCallID)
			}
			delete(open, m.ToolCallID)
		case len(m.ToolCalls) > 0:
			if len(open) > 0 {
				t.Errorf("message %d: unanswered calls %v", i, open)
			}
			open = map[string]bool{}
			for _, c := range m.ToolCalls {
				open[c.ID] = true
			}
		default:
			if len(open) > 0 {
				t.Errorf("message %d: unanswered calls %v", i, open)
			}
		}
	}
	if len(open) > 0 {
		t.Errorf("transcript ends with unanswered calls %v", open)
	}
}
