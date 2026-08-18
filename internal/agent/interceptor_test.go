package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/KonMam/tether/internal/provider"
	"github.com/KonMam/tether/internal/tools"
)

type testInterceptor struct {
	blockTool string
	extra     string
	before    []string
	after     []string
}

func (i *testInterceptor) Name() string { return "test" }

func (i *testInterceptor) BeforeTool(_ context.Context, call provider.ToolCall) (string, error) {
	i.before = append(i.before, call.Name)
	if call.Name == i.blockTool {
		return "", fmt.Errorf("%s is not allowed here", call.Name)
	}
	return "checked " + call.Name, nil
}

func (i *testInterceptor) AfterTool(context.Context, provider.ToolCall, tools.Result, error) string {
	i.after = append(i.after, "x")
	return i.extra
}

func TestInterceptorBlocksTool(t *testing.T) {
	ran := false
	fake := &fakeProvider{script: [][]provider.Event{
		toolReply(provider.ToolCall{ID: "c1", Name: "danger", Args: `{}`}),
		textReply("ok"),
	}}
	ic := &testInterceptor{blockTool: "danger"}
	a, _ := newTestAgent(t, fake, &scriptedApprover{decisions: []Decision{DecisionAllow}}, dangerTool{ran: &ran})
	a.cfg.Interceptors = []ToolInterceptor{ic}

	if err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if ran {
		t.Error("blocked tool executed anyway")
	}
	last := fake.requests[1].Messages[len(fake.requests[1].Messages)-1]
	if !strings.Contains(last.Content, "blocked by policy") {
		t.Errorf("model should see the block: %q", last.Content)
	}
}

func TestInterceptorAppendsToResult(t *testing.T) {
	fake := &fakeProvider{script: [][]provider.Event{
		toolReply(provider.ToolCall{ID: "c1", Name: "echo", Args: `{"msg":"hi"}`}),
		textReply("done"),
	}}
	ic := &testInterceptor{extra: "diagnostics: all clear"}
	a, _ := newTestAgent(t, fake, &scriptedApprover{}, echoTool{})
	a.cfg.Interceptors = []ToolInterceptor{ic}

	if err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	last := fake.requests[1].Messages[len(fake.requests[1].Messages)-1]
	if !strings.Contains(last.Content, "echo: hi") || !strings.Contains(last.Content, "diagnostics: all clear") {
		t.Errorf("result should carry tool output plus interceptor extra: %q", last.Content)
	}
	if len(ic.before) != 1 || len(ic.after) != 1 {
		t.Errorf("interceptor calls: before=%d after=%d", len(ic.before), len(ic.after))
	}
}

func TestInterceptorActivityIsTraced(t *testing.T) {
	fake := &fakeProvider{script: [][]provider.Event{
		toolReply(provider.ToolCall{ID: "c1", Name: "echo", Args: `{"msg":"hi"}`}),
		textReply("done"),
	}}
	ic := &testInterceptor{extra: "note from after"}
	a, events := newTestAgent(t, fake, &scriptedApprover{}, echoTool{})
	a.cfg.Interceptors = []ToolInterceptor{ic}

	if err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	var traced []string
	for _, ev := range *events {
		if ev.Type == EventInterceptor {
			traced = append(traced, ev.ToolName+": "+ev.Text)
			if ev.ToolID != "c1" {
				t.Errorf("interceptor event not linked to call: %+v", ev)
			}
		}
	}
	if len(traced) != 2 {
		t.Fatalf("want 2 interceptor events (before note + after extra), got %v", traced)
	}
	if traced[0] != "test: checked echo" || traced[1] != "test: note from after" {
		t.Errorf("traced = %v", traced)
	}
}

// ctxTool proves tools can read their own call ID from ctx.
type ctxTool struct{ seen *string }

func (c ctxTool) Safe() bool { return true }
func (c ctxTool) Def() provider.ToolDef {
	return provider.ToolDef{Name: "ctxtool", Description: "x", Parameters: map[string]any{"type": "object"}}
}
func (c ctxTool) Run(ctx context.Context, _ json.RawMessage) (tools.Result, error) {
	*c.seen = ToolCallID(ctx)
	return tools.Result{Content: "ok"}, nil
}

func TestToolCallIDInContext(t *testing.T) {
	seen := ""
	fake := &fakeProvider{script: [][]provider.Event{
		toolReply(provider.ToolCall{ID: "call-xyz", Name: "ctxtool", Args: `{}`}),
		textReply("ok"),
	}}
	a, _ := newTestAgent(t, fake, &scriptedApprover{}, ctxTool{seen: &seen})
	if err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if seen != "call-xyz" {
		t.Errorf("ToolCallID = %q, want call-xyz", seen)
	}
}
