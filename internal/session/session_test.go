package session

import (
	"testing"

	"github.com/KonMam/buntline/internal/agent"
	"github.com/KonMam/buntline/internal/provider"
)

func TestSessionRoundTrip(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	meta, err := store.Create("test-model", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "hello"},
		{Role: provider.RoleAssistant, Content: "hi", ToolCalls: []provider.ToolCall{
			{ID: "c1", Name: "read_file", Args: `{"path":"x"}`},
		}},
		{Role: provider.RoleTool, Content: "data", ToolCallID: "c1"},
	}
	for i := range msgs {
		if err := store.AppendMessage(meta.ID, &msgs[i]); err != nil {
			t.Fatal(err)
		}
	}

	got, err := store.Messages(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d messages, want 3", len(got))
	}
	if got[1].ToolCalls[0].Name != "read_file" || got[2].ToolCallID != "c1" {
		t.Errorf("tool linkage lost in round trip: %+v", got)
	}

	// Replace (compaction path).
	if err := store.ReplaceTranscript(meta.ID, got[:1]); err != nil {
		t.Fatal(err)
	}
	got, err = store.Messages(meta.ID)
	if err != nil || len(got) != 1 {
		t.Fatalf("post-replace: %d messages, err %v", len(got), err)
	}

	// Events log.
	if err := store.AppendEvent(meta.ID, &agent.Event{Type: agent.EventTurnStart}); err != nil {
		t.Fatal(err)
	}
	evs, err := store.Events(meta.ID, 10)
	if err != nil || len(evs) != 1 {
		t.Fatalf("events: %d, err %v", len(evs), err)
	}

	// List sees the session.
	metas, err := store.List()
	if err != nil || len(metas) != 1 || metas[0].ID != meta.ID {
		t.Fatalf("list: %+v, err %v", metas, err)
	}
}

func TestEmptySessionHasNoTranscript(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	meta, err := store.Create("m", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := store.Messages(meta.ID)
	if err != nil || msgs != nil {
		t.Fatalf("fresh session should have nil transcript, got %v, %v", msgs, err)
	}
}
