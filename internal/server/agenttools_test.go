package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/KonMam/tether/internal/provider"
)

// immediateProvider answers every model call with a plain text reply, so
// a child completes one round and finishes.
type immediateProvider struct{}

func (immediateProvider) Name() string { return "immediate" }

func (immediateProvider) SupportsImages() bool { return false }

func (immediateProvider) Stream(_ context.Context, _ provider.Request) (<-chan provider.Event, error) {
	ch := make(chan provider.Event, 3)
	ch <- provider.Event{Kind: provider.EventTextDelta, Text: "final report"}
	ch <- provider.Event{Kind: provider.EventUsage, Usage: &provider.Usage{PromptTokens: 5, CompletionTokens: 5}}
	ch <- provider.Event{Kind: provider.EventDone, FinishReason: "stop"}
	close(ch)
	return ch, nil
}

// TestAgentTools covers the model-facing control surface: list, output
// (running vs terminal), and kill.
func TestAgentTools(t *testing.T) {
	s, store := newTestServer(t)
	ls, _ := startSession(t, s, store)

	f := &fakeSpawner{ls: ls}
	f.spawn(t, "call-a", "", "inspect the loop")
	f.spawn(t, "call-b", "code", "check the provider")

	// The running child has streamed some text into its own transcript
	// (the registry does not hold deltas; agent_output reads the child).
	ls.subagents.get("call-a").agent.SetMessages([]provider.Message{
		{Role: provider.RoleAssistant, Content: "I found a suspicious loop in spawn.go"},
	})

	list := &AgentList{ls: ls}
	res, err := list.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("agent_list: %v", err)
	}
	if !strings.Contains(res.Content, "call-a") || !strings.Contains(res.Content, "running") {
		t.Errorf("agent_list output = %q, want call-a and running", res.Content)
	}
	if !strings.Contains(res.Content, "inspect the loop") {
		t.Errorf("agent_list output missing task snippet: %q", res.Content)
	}

	// Running: agent_output shows streamed text, not a report.
	out := &AgentOutput{ls: ls}
	res, err = out.Run(context.Background(), json.RawMessage(`{"id":"call-a"}`))
	if err != nil {
		t.Fatalf("agent_output running: %v", err)
	}
	if strings.Contains(res.Content, "no output") {
		t.Errorf("running agent_output = %q, want streamed text", res.Content)
	}

	// Terminal: agent_output shows the report.
	ls.subagents.get("call-b").finish(SubagentDone, "the final report")
	res, err = out.Run(context.Background(), json.RawMessage(`{"id":"call-b"}`))
	if err != nil {
		t.Fatalf("agent_output terminal: %v", err)
	}
	if !strings.Contains(res.Content, "done") || !strings.Contains(res.Content, "the final report") {
		t.Errorf("terminal agent_output = %q, want done + report", res.Content)
	}

	// Unknown id: a readable message, not an error.
	res, err = out.Run(context.Background(), json.RawMessage(`{"id":"call-nope"}`))
	if err != nil {
		t.Fatalf("agent_output unknown: %v", err)
	}
	if !strings.Contains(res.Content, "no subagent") {
		t.Errorf("unknown agent_output = %q, want no subagent", res.Content)
	}

	// Kill a running child: cancels it, terminal stays terminal.
	kill := &AgentKill{ls: ls}
	res, err = kill.Run(context.Background(), json.RawMessage(`{"id":"call-a"}`))
	if err != nil {
		t.Fatalf("agent_kill: %v", err)
	}
	if !strings.Contains(res.Content, "interrupted call-a") {
		t.Errorf("agent_kill = %q, want interrupted call-a", res.Content)
	}
	// Killing a terminal id says so, without error.
	res, err = kill.Run(context.Background(), json.RawMessage(`{"id":"call-b"}`))
	if err != nil {
		t.Fatalf("agent_kill terminal: %v", err)
	}
	if !strings.Contains(res.Content, "already done") {
		t.Errorf("agent_kill terminal = %q, want already done", res.Content)
	}
}

// TestBackgroundSpawnAndNotice covers the background path: the tool
// returns immediately, the child completes asynchronously, the registry
// ends terminal, and the finished notice reaches the parent (idle here,
// so it lands as a user message in the transcript).
func TestBackgroundSpawnAndNotice(t *testing.T) {
	s, store := newTestServer(t)
	ls, id := startSession(t, s, store)

	// The child reads the provider live from the session agent, exactly
	// as a profile switch would leave it.
	if err := ls.agent.SetProvider(immediateProvider{}); err != nil {
		t.Fatal(err)
	}
	st := &spawnTool{
		server:    s,
		sessionID: id,
		ls:        ls,
		workdir:   t.TempDir(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res, err := st.Run(ctx, json.RawMessage(`{"task":"do the thing","run_in_background":true}`))
	if err != nil {
		t.Fatalf("background spawn: %v", err)
	}
	if !strings.HasPrefix(res.Content, "started agent ") {
		t.Errorf("background spawn result = %q, want started agent", res.Content)
	}
	parentID := strings.TrimPrefix(strings.Split(res.Content, ";")[0], "started agent ")

	// The entry registered immediately as running.
	entries := ls.subagents.list()
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].id != parentID || entries[0].status != SubagentRunning {
		t.Fatalf("entry = %s %s, want %s running", entries[0].id, entries[0].status, parentID)
	}

	// The child completes on its own (immediate provider) and the notice
	// lands as a user message in the transcript.
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, _, report := entries[0].snapshot()
		if status == SubagentDone {
			if !strings.Contains(report, "final report") {
				t.Errorf("report = %q, want final report", report)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background child never finished")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The notice is in the agent's transcript (idle path appends a user
	// message).
	found := false
	for _, m := range ls.agent.Messages() {
		if m.Role == provider.RoleUser && strings.Contains(m.Content, "agent "+parentID+" finished") {
			found = true
		}
	}
	if !found {
		t.Errorf("notice not found in parent transcript; messages: %+v", ls.agent.Messages())
	}
}
