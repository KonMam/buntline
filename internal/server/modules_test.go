package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KonMam/buntline/internal/module"
	"github.com/KonMam/buntline/internal/session"
)

// tinyRouteModule is a minimal non-core module with a route, so the HTTP
// toggle can be exercised end to end.
type tinyRouteModule struct{}

func (tinyRouteModule) Info() module.Info {
	return module.Info{ID: "tiny", Name: "Tiny", Description: "test", Default: true}
}

func (tinyRouteModule) Routes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"GET /ping": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("pong"))
		},
	}
}

// TestSetModuleHTTPToggle proves the HTTP surface of disabling works:
// the toggle call itself succeeds, the store reflects the change, and
// the module's mounted route goes 404 without restarting anything.
func TestSetModuleHTTPToggle(t *testing.T) {
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg, err := module.NewRegistry(filepath.Join(t.TempDir(), "modules.json"), tinyRouteModule{})
	if err != nil {
		t.Fatal(err)
	}
	s := New(emptyConfig(), store, nil, reg, nil)
	t.Cleanup(s.Shutdown)

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	// Enabled: route answers.
	resp, err := ts.Client().Get(ts.URL + "/api/m/tiny/ping")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enabled route = %d, want 200", resp.StatusCode)
	}

	// Toggle off over HTTP.
	resp, err = ts.Client().Post(ts.URL+"/api/modules/tiny",
		"application/json", strings.NewReader(`{"enabled":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("toggle status = %d, want 200", resp.StatusCode)
	}
	var st module.CoreStatus
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if len(st.Modules) != 1 || st.Modules[0].ID != "tiny" || st.Modules[0].Enabled {
		t.Fatalf("store should report tiny disabled: %+v", st.Modules)
	}

	// Disabled: route 404s.
	resp, err = ts.Client().Get(ts.URL + "/api/m/tiny/ping")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled route = %d, want 404", resp.StatusCode)
	}

	// GET /api/modules returns the same split shape.
	resp, err = ts.Client().Get(ts.URL + "/api/modules")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var got module.CoreStatus
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Modules) != 1 || got.Modules[0].Enabled {
		t.Fatalf("GET /api/modules should report tiny disabled: %+v", got.Modules)
	}
}
