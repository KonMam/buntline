package mcpclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/KonMam/tether/internal/config"
)

// startMCPServer serves a real MCP server with one echo tool over
// streamable HTTP, exercising the same client path a remote server uses.
func startMCPServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "it-server", Version: "0.1"}, nil)
	type echoIn struct {
		Text string `json:"text"`
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "echo", Description: "Echo text back."},
		func(_ context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "echo: " + in.Text}},
			}, nil, nil
		})
	srv.AddResource(&mcp.Resource{URI: "test://greeting", Name: "greeting"},
		func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{
				Contents: []*mcp.ResourceContents{{URI: "test://greeting", Text: "hello from the resource"}},
			}, nil
		})
	srv.AddPrompt(&mcp.Prompt{
		Name:      "review",
		Arguments: []*mcp.PromptArgument{{Name: "target"}},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return &mcp.GetPromptResult{
			Messages: []*mcp.PromptMessage{{
				Role:    "user",
				Content: &mcp.TextContent{Text: "review " + req.Params.Arguments["target"]},
			}},
		}, nil
	})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func TestConnectAndCallOverHTTP(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // keep the user's mcp.json out of the test
	ts := startMCPServer(t)

	m := New([]config.MCPServer{{Name: "it", Transport: "http", URL: ts.URL}})
	// Close the client session before the test server shuts down: Close
	// waits for open connections, and the MCP session keeps one.
	t.Cleanup(func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		for _, s := range m.sessions {
			_ = s.Close()
		}
	})
	got := m.Tools("")
	if len(got) != 3 {
		t.Fatalf("expected echo + the two resource tools, got %d tools", len(got))
	}
	def := got[0].Def()
	if def.Name != "it_echo" {
		t.Fatalf("tool name = %q, want it_echo (namespaced)", def.Name)
	}
	if got[0].Safe() {
		t.Error("remote tools must pass the approval gate")
	}

	res, err := got[0].Run(context.Background(), json.RawMessage(`{"text":"ping"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "echo: ping" {
		t.Errorf("result = %q", res.Content)
	}

	// Resources: list, then read the listed URI.
	res, err = (&listResourcesTool{mod: m}).Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "test://greeting") {
		t.Errorf("resource listing = %q", res.Content)
	}
	res, err = (&readResourceTool{mod: m}).Run(context.Background(), json.RawMessage(`{"uri":"test://greeting"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "hello from the resource" {
		t.Errorf("resource read = %q", res.Content)
	}

	// Prompts: discovered at connect.
	m.mu.Lock()
	prompts := m.prompts["it"]
	m.mu.Unlock()
	if len(prompts) != 1 || prompts[0].Name != "review" {
		t.Errorf("prompts = %+v", prompts)
	}

	m.mu.Lock()
	infos := m.listLocked()
	m.mu.Unlock()
	if len(infos) != 1 || !strings.HasPrefix(infos[0].Status, "connected") {
		t.Errorf("management listing should report the connection, got %+v", infos)
	}
}

func TestConnectFailureIsReported(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := New([]config.MCPServer{{Name: "dead", Transport: "http", URL: "http://127.0.0.1:1"}})
	if got := m.Tools(""); len(got) != 0 {
		t.Fatalf("dead server should contribute no tools, got %d", len(got))
	}
	m.mu.Lock()
	infos := m.listLocked()
	m.mu.Unlock()
	if len(infos) != 1 || infos[0].Status == "" || strings.HasPrefix(infos[0].Status, "connected") {
		t.Errorf("failure should surface in status, got %+v", infos)
	}
}

// TestPromptRenderNoDeadlockOnToolCallback locks the lock-ordering rule
// the prompts render handler depends on: the module must not hold its
// mutex across the session's GetPrompt round-trip, because the
// server-side handler for a prompt can itself call back into this
// module's tools, which take the same mutex. Before the release was
// added, this test hung forever; the timeout is the regression guard.
func TestPromptRenderNoDeadlockOnToolCallback(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // keep the user's mcp.json out of the test
	ts := startMCPServer(t)

	m := New([]config.MCPServer{{Name: "it", Transport: "http", URL: ts.URL}})
	t.Cleanup(func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		for _, s := range m.sessions {
			_ = s.Close()
		}
	})
	// Connect, so the session and prompt are discoverable.
	if got := m.Tools(""); len(got) == 0 {
		t.Fatal("connect contributed no tools")
	}

	body := strings.NewReader(`{"name":"it/review","argument":"the pr"}`)
	req := httptest.NewRequest("POST", "/api/m/mcp/prompts/render", body)
	rec := httptest.NewRecorder()
	m.Routes()["POST /prompts/render"](rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("render status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "review the pr") {
		t.Errorf("render output = %q", rec.Body.String())
	}
}

func TestStopClosesAndReconnectLazily(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ts := startMCPServer(t)

	m := New([]config.MCPServer{{Name: "it", Transport: "http", URL: ts.URL}})
	t.Cleanup(m.Stop)
	if got := m.Tools(""); len(got) == 0 {
		t.Fatal("expected tools before Stop")
	}

	m.Stop()
	m.mu.Lock()
	nSessions, nTools, loaded := len(m.sessions), len(m.byServer), m.loaded
	m.mu.Unlock()
	if nSessions != 0 || nTools != 0 || loaded {
		t.Fatalf("Stop left state behind: sessions=%d tools=%d loaded=%v", nSessions, nTools, loaded)
	}

	// A disabled module costs nothing; re-enabling reconnects on demand.
	if got := m.Tools(""); len(got) == 0 {
		t.Fatal("expected tools to reconnect after Stop")
	}
}
