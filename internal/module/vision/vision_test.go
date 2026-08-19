package vision

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KonMam/buntline/internal/config"
)

// serveVision answers one /v1/chat/completions request with an SSE
// stream, and records the request body so tests can assert what the
// module actually sent.
func serveVision(t *testing.T, chunks ...string) (*httptest.Server, func() map[string]any) {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range chunks {
			_, _ = w.Write([]byte("data: " + c + "\n\n"))
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	return srv, func() map[string]any { return got }
}

func streamChunk(content string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{
			"delta": map[string]any{"content": content},
		}},
	})
	return string(b)
}

func testModule(t *testing.T, baseURL string) *Module {
	t.Helper()
	return &Module{Cfg: config.Vision{BaseURL: baseURL, Model: "qwen-vl", APIKey: "k"}}
}

func TestDescribeReturnsStreamedDescription(t *testing.T) {
	srv, body := serveVision(t, streamChunk("Image 1: a red error dialog saying "), streamChunk("'build failed'."))
	defer srv.Close()

	m := testModule(t, srv.URL+"/v1")
	desc, err := m.Describe(context.Background(), []string{"data:image/png;base64,AAAA"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(desc, "build failed") {
		t.Errorf("desc = %q, want the streamed text", desc)
	}

	// The request must carry the images as content parts and the caption
	// prompt; the whole point is that the vision model sees the image.
	got := body()
	if got == nil {
		t.Fatal("no request body captured")
	}
	if got["model"] != "qwen-vl" {
		t.Errorf("model = %v, want qwen-vl", got["model"])
	}
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	content, ok := msgs[0].(map[string]any)["content"].([]any)
	if !ok || len(content) != 2 {
		t.Fatalf("content parts = %#v, want text + image_url", msgs[0])
	}
	parts := content
	if parts[0].(map[string]any)["type"] != "text" || parts[1].(map[string]any)["type"] != "image_url" {
		t.Errorf("parts = %#v, want text then image_url", parts)
	}
}

func TestDescribeUnconfigured(t *testing.T) {
	m := &Module{Cfg: config.Vision{}}
	if m.Configured() {
		t.Fatal("empty config must not be configured")
	}
	_, err := m.Describe(context.Background(), []string{"data:image/png;base64,AAAA"})
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Errorf("err = %v, want a not-configured error", err)
	}
}

func TestDescribeEmptyDescription(t *testing.T) {
	srv, _ := serveVision(t, streamChunk("   "))
	defer srv.Close()

	m := testModule(t, srv.URL+"/v1")
	_, err := m.Describe(context.Background(), []string{"data:image/png;base64,AAAA"})
	if err == nil || !strings.Contains(err.Error(), "empty description") {
		t.Errorf("err = %v, want an empty-description error", err)
	}
}

func TestDescribeProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"error":{"message":"bad image"}}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	m := testModule(t, srv.URL+"/v1")
	_, err := m.Describe(context.Background(), []string{"data:image/png;base64,AAAA"})
	if err == nil || !strings.Contains(err.Error(), "bad image") {
		t.Errorf("err = %v, want the provider error surfaced", err)
	}
}

func TestInfoReflectsConfig(t *testing.T) {
	m := &Module{Cfg: config.Vision{}}
	if info := m.Info(); !strings.Contains(info.Description, "Needs configuration") {
		t.Errorf("unconfigured description = %q, want a config hint", info.Description)
	}
	m.Cfg = config.Vision{BaseURL: "http://x/v1", Model: "qwen-vl"}
	if info := m.Info(); !strings.Contains(info.Description, "qwen-vl") {
		t.Errorf("configured description = %q, want the model name", info.Description)
	}
}
