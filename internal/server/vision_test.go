package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KonMam/tether/internal/module"
	"github.com/KonMam/tether/internal/session"
)

// fakeVisionModule is a vision module the server gate can exercise
// without a live backend: it implements the visionDescriber capability
// the send gate type-asserts.
type fakeVisionModule struct {
	configured bool
	model      string
	desc       string
	err        error
	gotImages  []string
}

func (f *fakeVisionModule) Info() module.Info {
	return module.Info{ID: "vision", Name: "Vision", Description: "test", Default: true}
}
func (f *fakeVisionModule) Configured() bool { return f.configured }
func (f *fakeVisionModule) Model() string    { return f.model }
func (f *fakeVisionModule) Describe(_ context.Context, images []string) (string, error) {
	f.gotImages = images
	return f.desc, f.err
}

// newVisionTestServer builds a server with a vision module registered
// and a text-only (deepseek) session ready to send to.
func newVisionTestServer(t *testing.T, vm *fakeVisionModule) (*Server, *session.Store, string) {
	t.Helper()
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg, err := module.NewRegistry(filepath.Join(t.TempDir(), "modules.json"), vm)
	if err != nil {
		t.Fatal(err)
	}
	s := New(emptyConfig(), store, nil, reg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(s.Shutdown)
	meta, err := store.Create("test-model", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	meta.Profile = "deepseek"
	if err := store.SaveMeta(meta); err != nil {
		t.Fatal(err)
	}
	return s, store, meta.ID
}

func postImages(t *testing.T, s *Server, id, content, image string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"content": content,
		"images":  []string{image},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+id+"/messages", strings.NewReader(string(body)))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	return rr
}

// waitForUserMessage polls the store until the send's user message
// lands in the transcript (the turn runs in a goroutine).
func waitForUserMessage(t *testing.T, store *session.Store, id string) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		msgs, err := store.Messages(id)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range msgs {
			if m.Role == "user" && m.Content != "" {
				var out []map[string]any
				for _, mm := range msgs {
					out = append(out, map[string]any{
						"role":    mm.Role,
						"content": mm.Content,
						"images":  mm.Images,
						"kind":    mm.Kind,
					})
				}
				return out
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("user message never landed in the transcript")
	return nil
}

// TestSendMessageTranslatesImagesOnTextOnlyProvider: a session whose
// provider is text-only (deepseek) but has a configured vision module
// must pass the gate, describe the image, and start the turn with the
// description appended to the message (images preserved for the UI).
func TestSendMessageTranslatesImagesOnTextOnlyProvider(t *testing.T) {
	vm := &fakeVisionModule{
		configured: true,
		model:      "qwen-vl",
		desc:       "Image 1: a red dialog reading 'build failed'.",
	}
	s, store, id := newVisionTestServer(t, vm)

	rr := postImages(t, s, id, "what does this mean?", "data:image/png;base64,AAAA")
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", rr.Code, rr.Body.String())
	}

	msgs := waitForUserMessage(t, store, id)
	msg := msgs[0]
	if !strings.Contains(msg["content"].(string), "build failed") {
		t.Errorf("content = %q, want the vision description", msg["content"])
	}
	if !strings.Contains(msg["content"].(string), "[A vision model (qwen-vl) described the attached image:]") {
		t.Errorf("content = %q, want a labeled description", msg["content"])
	}
	if !strings.Contains(msg["content"].(string), "what does this mean?") {
		t.Errorf("content = %q, want the original text kept", msg["content"])
	}
	images, _ := msg["images"].([]string)
	if len(images) != 1 || images[0] != "data:image/png;base64,AAAA" {
		t.Errorf("images = %v, want the original data URL preserved for the UI", images)
	}
	if len(vm.gotImages) != 1 || vm.gotImages[0] != "data:image/png;base64,AAAA" {
		t.Errorf("vision module got %v, want the image passed to Describe", vm.gotImages)
	}
}

// TestSendMessageRefusesWhenVisionUnconfigured: the vision module is
// registered but has no backend, so the refusal message gains the
// configure-[vision] hint and the transcript stays untouched.
func TestSendMessageRefusesWhenVisionUnconfigured(t *testing.T) {
	vm := &fakeVisionModule{configured: false}
	s, store, id := newVisionTestServer(t, vm)

	rr := postImages(t, s, id, "what is this", "data:image/png;base64,AAAA")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "configure [vision]") {
		t.Errorf("error = %s, want a configure-[vision] hint", rr.Body.String())
	}
	msgs, err := store.Messages(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Errorf("transcript has %d messages, want 0 (refused message must not persist)", len(msgs))
	}
}

// TestSendMessageRefusesWhenVisionBackendFails: the vision call itself
// fails, so the send refuses cleanly and names the backend failure.
func TestSendMessageRefusesWhenVisionBackendFails(t *testing.T) {
	vm := &fakeVisionModule{
		configured: true,
		model:      "qwen-vl",
		err:        context.DeadlineExceeded,
	}
	s, store, id := newVisionTestServer(t, vm)

	rr := postImages(t, s, id, "what is this", "data:image/png;base64,AAAA")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "vision backend failed") {
		t.Errorf("error = %s, want the backend failure named", rr.Body.String())
	}
	msgs, err := store.Messages(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Errorf("transcript has %d messages, want 0 (failed translation must not persist)", len(msgs))
	}
}
