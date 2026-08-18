package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/KonMam/tether/internal/config"
	"github.com/KonMam/tether/internal/provider"
	"github.com/KonMam/tether/internal/tools"
)

func mcpServerNamed(name string) config.MCPServer {
	return config.MCPServer{Name: name, Transport: "stdio", Command: "true"}
}

type fakeTool struct {
	name string
}

func (t *fakeTool) Safe() bool { return false }
func (t *fakeTool) Def() provider.ToolDef {
	return provider.ToolDef{
		Name:        t.name,
		Description: "fake " + t.name,
		Parameters:  map[string]any{"type": "object"},
	}
}
func (t *fakeTool) Run(_ context.Context, _ json.RawMessage) (tools.Result, error) {
	return tools.Result{Content: "ran " + t.name}, nil
}

// seeded returns a module pre-loaded with n fake tools on one server,
// as if the connection already happened.
func seeded(n int) *Module {
	m := New(nil)
	m.loaded = true
	var ts []tools.Tool
	for i := 0; i < n; i++ {
		ts = append(ts, &fakeTool{name: fmt.Sprintf("srv_tool_%d", i)})
	}
	m.byServer["srv"] = ts
	m.dynamic = append(m.dynamic, mcpServerNamed("srv"))
	return m
}

func TestToolsBelowThresholdPassThrough(t *testing.T) {
	m := seeded(deferThreshold)
	got := m.Tools("")
	if len(got) != deferThreshold {
		t.Fatalf("expected %d tools passed through, got %d", deferThreshold, len(got))
	}
	if got[0].Def().Name != "srv_tool_0" {
		t.Errorf("expected real tool defs, got %s", got[0].Def().Name)
	}
}

func TestToolsAboveThresholdDeferred(t *testing.T) {
	m := seeded(deferThreshold + 1)
	got := m.Tools("")
	if len(got) != 2 {
		t.Fatalf("expected the two meta-tools, got %d", len(got))
	}
	names := []string{got[0].Def().Name, got[1].Def().Name}
	if names[0] != "mcp_list_tools" || names[1] != "mcp_call_tool" {
		t.Fatalf("unexpected meta-tools %v", names)
	}
	if !got[0].Safe() {
		t.Error("mcp_list_tools should be safe (read-only)")
	}
	if got[1].Safe() {
		t.Error("mcp_call_tool must pass the approval gate")
	}
}

func TestListToolFilters(t *testing.T) {
	m := seeded(20)
	lt := &listTool{mod: m}
	res, err := lt.Run(context.Background(), json.RawMessage(`{"query":"tool_7"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "srv_tool_7") || strings.Contains(res.Content, "srv_tool_8") {
		t.Errorf("filter failed:\n%s", res.Content)
	}
}

func TestCallToolDispatchesByName(t *testing.T) {
	m := seeded(20)
	ct := &callTool{mod: m}
	res, err := ct.Run(context.Background(), json.RawMessage(`{"name":"srv_tool_3","arguments":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "ran srv_tool_3" {
		t.Errorf("dispatch result = %q", res.Content)
	}

	res, err = ct.Run(context.Background(), json.RawMessage(`{"name":"nope"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "unknown tool") {
		t.Errorf("unknown tool should return guidance, got %q", res.Content)
	}
}
