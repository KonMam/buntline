package ollama

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/KonMam/tether/internal/module"
)

// fakeOllama serves the native Ollama endpoints the module talks to and
// records every unload request. Loaded models are those that have been
// "generated" (keep_alive > 0) and not yet unloaded (keep_alive == 0).
type fakeOllama struct {
	mu      sync.Mutex
	loaded  []string
	unloads []string // model names sent keep_alive:0
	psCalls int
}

func (f *fakeOllama) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/ps":
		f.mu.Lock()
		f.psCalls++
		models := make([]map[string]any, 0, len(f.loaded))
		for _, name := range f.loaded {
			models = append(models, map[string]any{"name": name})
		}
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"models": models})
	case "/api/tags":
		f.mu.Lock()
		models := make([]map[string]any, 0, len(f.loaded))
		for _, name := range f.loaded {
			models = append(models, map[string]any{"name": name, "size": 1})
		}
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"models": models})
	case "/api/generate":
		var in struct {
			Model     string `json:"model"`
			KeepAlive any    `json:"keep_alive"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		if in.KeepAlive == float64(0) {
			f.unloads = append(f.unloads, in.Model)
			for i, name := range f.loaded {
				if name == in.Model {
					f.loaded = append(f.loaded[:i], f.loaded[i+1:]...)
					break
				}
			}
		} else {
			f.loaded = append(f.loaded, in.Model)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"done":true}`))
	default:
		http.NotFound(w, r)
	}
}

// loadModel simulates the agent loading a model into memory.
func (f *fakeOllama) loadModel(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loaded = append(f.loaded, name)
}

func (f *fakeOllama) loadedModels() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.loaded...)
}

func (f *fakeOllama) unloadCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.unloads...)
}

// TestStopUnloadsLoadedModels is the promise behind "disabled means zero
// cost" for local models: a model the agent loaded releases its RAM when
// the module is switched off.
func TestStopUnloadsLoadedModels(t *testing.T) {
	fake := &fakeOllama{}
	fake.loadModel("qwen3.5:9b")
	fake.loadModel("deepseek-r1:8b")
	srv := httptest.NewServer(fake)
	defer srv.Close()

	// The module's BaseURL is the OpenAI-compat URL; Stop derives the
	// native origin from it, exactly like the management routes.
	m := New(srv.URL + "/v1")
	m.Client = srv.Client()

	m.Stop()

	if got := fake.unloadCalls(); len(got) != 2 {
		t.Fatalf("Stop should unload every loaded model, got %v", got)
	}
	for _, name := range []string{"qwen3.5:9b", "deepseek-r1:8b"} {
		found := false
		for _, unloaded := range fake.unloadCalls() {
			if unloaded == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("model %q was not unloaded, unloads = %v", name, fake.unloadCalls())
		}
	}
	if got := fake.loadedModels(); len(got) != 0 {
		t.Errorf("fake server still holds models after Stop: %v", got)
	}
	if fake.psCalls != 1 {
		t.Errorf("Stop should read /api/ps once to learn loaded models, got %d calls", fake.psCalls)
	}
}

// TestStopUnloadsNothingWhenIdle: no loaded models, no unload calls, and
// no error. Disabling an idle module is a no-op.
func TestStopUnloadsNothingWhenIdle(t *testing.T) {
	fake := &fakeOllama{}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	m := New(srv.URL + "/v1")
	m.Client = srv.Client()
	m.Stop()

	if got := fake.unloadCalls(); len(got) != 0 {
		t.Errorf("idle module should unload nothing, got %v", got)
	}
	if fake.psCalls != 1 {
		t.Errorf("Stop should still learn the current load, got %d ps calls", fake.psCalls)
	}
}

// TestOllamaDisableViaRegistry wires a real ollama module through the
// registry: disabling it unloads loaded models (Stop hook) and its
// management routes go 404 while core tools stay.
func TestOllamaDisableViaRegistry(t *testing.T) {
	fake := &fakeOllama{}
	fake.loadModel("qwen3.5:9b")
	srv := httptest.NewServer(fake)
	defer srv.Close()

	om := New(srv.URL + "/v1")
	om.Client = srv.Client()
	r, err := module.NewRegistry(filepath.Join(t.TempDir(), "modules.json"), om)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	r.Mount(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Enabled: the models route answers and lists the loaded model.
	resp, err := ts.Client().Get(ts.URL + "/api/m/ollama/models")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(body.Models) != 1 || body.Models[0].Name != "qwen3.5:9b" {
		t.Fatalf("enabled route should list the model, status=%d models=%+v", resp.StatusCode, body.Models)
	}

	// Disable via the registry toggle: Stop unloads the model and the
	// route goes away.
	if err := r.SetEnabled("ollama", false); err != nil {
		t.Fatal(err)
	}
	if got := fake.unloadCalls(); len(got) != 1 || got[0] != "qwen3.5:9b" {
		t.Fatalf("disabling should unload the loaded model, got %v", got)
	}
	if got := fake.loadedModels(); len(got) != 0 {
		t.Fatalf("model still loaded after disable: %v", got)
	}
	resp, err = ts.Client().Get(ts.URL + "/api/m/ollama/models")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled route = %d, want 404", resp.StatusCode)
	}
}
