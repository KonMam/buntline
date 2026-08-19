package module

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/KonMam/buntline/internal/agent"
	"github.com/KonMam/buntline/internal/provider"
	"github.com/KonMam/buntline/internal/tools"
)

type observerModule struct {
	seen []agent.EventType
}

func (m *observerModule) Info() Info {
	return Info{ID: "obs", Name: "Observer", Description: "test", Default: true}
}

func (m *observerModule) Observer(_, _ string) func(agent.Event) {
	return func(ev agent.Event) { m.seen = append(m.seen, ev.Type) }
}

func TestObserversRespectEnabledState(t *testing.T) {
	mod := &observerModule{}
	r, err := NewRegistry(filepath.Join(t.TempDir(), "modules.json"), mod)
	if err != nil {
		t.Fatal(err)
	}

	obs := r.Observers("s1", "/tmp", nil)
	if len(obs) != 1 {
		t.Fatalf("enabled module should observe, got %d observers", len(obs))
	}
	obs[0](agent.Event{Type: agent.EventTurnStart})
	if len(mod.seen) != 1 || mod.seen[0] != agent.EventTurnStart {
		t.Errorf("observer not invoked: %v", mod.seen)
	}

	if err := r.SetEnabled("obs", false); err != nil {
		t.Fatal(err)
	}
	if got := r.Observers("s1", "/tmp", nil); len(got) != 0 {
		t.Errorf("disabled module should not observe, got %d", len(got))
	}
}

type toolModule struct {
	id string
}

func (m *toolModule) Info() Info {
	return Info{ID: m.id, Name: "Feature", Description: "test", Default: true}
}

func (m *toolModule) Tools(_ string) []tools.Tool {
	return []tools.Tool{&fakeTool{name: m.id + "_tool"}}
}

type fakeTool struct{ name string }

func (t *fakeTool) Safe() bool { return true }
func (t *fakeTool) Def() provider.ToolDef {
	return provider.ToolDef{Name: t.name, Description: "fake"}
}
func (t *fakeTool) Run(ctx context.Context, _ json.RawMessage) (tools.Result, error) {
	return tools.Result{}, nil
}

func TestCoreModulesAlwaysContributeTools(t *testing.T) {
	r, err := NewRegistry(filepath.Join(t.TempDir(), "modules.json"), &toolModule{id: "feat"})
	if err != nil {
		t.Fatal(err)
	}
	r.Core(&toolModule{id: "core"})

	got := r.Tools("/tmp", nil)
	names := []string{}
	for _, tl := range got {
		names = append(names, tl.Def().Name)
	}
	if len(names) != 2 {
		t.Fatalf("core + enabled feature should contribute 2 tools, got %v", names)
	}

	// Disabling the feature module leaves the core tool in place.
	if err := r.SetEnabled("feat", false); err != nil {
		t.Fatal(err)
	}
	got = r.Tools("/tmp", nil)
	if len(got) != 1 || got[0].Def().Name != "core_tool" {
		t.Fatalf("disabled feature should drop its tool, core stays: %v", got)
	}
}

func TestCoreModulesNotToggleable(t *testing.T) {
	r, err := NewRegistry(filepath.Join(t.TempDir(), "modules.json"))
	if err != nil {
		t.Fatal(err)
	}
	r.Core(&toolModule{id: "core"})

	if err := r.SetEnabled("core", false); err == nil {
		t.Error("core module id should not be toggleable")
	}
	if got := r.EnabledFor("core", nil); !got {
		t.Error("core module should always be effectively enabled")
	}
	if got := r.EnabledFor("core", map[string]bool{"core": false}); !got {
		t.Error("per-repo override must not disable a core module")
	}
}

func TestStoreSeparatesCoreAndModules(t *testing.T) {
	r, err := NewRegistry(filepath.Join(t.TempDir(), "modules.json"), &toolModule{id: "feat"})
	if err != nil {
		t.Fatal(err)
	}
	r.Core(&toolModule{id: "core"})

	st := r.Store()
	if len(st.Core) != 1 || st.Core[0].ID != "core" {
		t.Fatalf("store core section wrong: %+v", st.Core)
	}
	if len(st.Modules) != 1 || st.Modules[0].ID != "feat" {
		t.Fatalf("store modules section wrong: %+v", st.Modules)
	}
	if !st.Core[0].Enabled {
		t.Error("core modules are always enabled")
	}
}

// routeModule contributes a route so the enabled-check wrapper can be
// exercised.
type routeModule struct {
	id   string
	hits *int
}

func (m *routeModule) Info() Info {
	return Info{ID: m.id, Name: "Route", Description: "test", Default: true}
}

func (m *routeModule) Routes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"GET /ping": func(w http.ResponseWriter, _ *http.Request) {
			if m.hits != nil {
				*m.hits++
			}
			_, _ = w.Write([]byte("pong"))
		},
	}
}

func TestDisabledModuleRoutes404(t *testing.T) {
	hits := 0
	r, err := NewRegistry(filepath.Join(t.TempDir(), "modules.json"),
		&routeModule{id: "route", hits: &hits})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	r.Mount(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Enabled: route answers.
	resp, err := http.Get(srv.URL + "/api/m/route/ping")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enabled route status = %d, want 200", resp.StatusCode)
	}
	if hits != 1 {
		t.Fatalf("enabled handler should run, hits = %d", hits)
	}

	// Disabled: the enabled-check rejects before the handler runs.
	if err := r.SetEnabled("route", false); err != nil {
		t.Fatal(err)
	}
	resp, err = http.Get(srv.URL + "/api/m/route/ping")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled route status = %d, want 404", resp.StatusCode)
	}
	if hits != 1 {
		t.Fatalf("disabled handler must not run, hits = %d", hits)
	}
}

// stopperModule records whether its Stop hook ran.
type stopperModule struct {
	id   string
	stop *bool
}

func (m *stopperModule) Info() Info {
	return Info{ID: m.id, Name: "Stopper", Description: "test", Default: true}
}

func (m *stopperModule) Stop() {
	if m.stop != nil {
		*m.stop = true
	}
}

func TestDisablingCallsStopHook(t *testing.T) {
	stopped := false
	r, err := NewRegistry(filepath.Join(t.TempDir(), "modules.json"),
		&stopperModule{id: "stop", stop: &stopped})
	if err != nil {
		t.Fatal(err)
	}
	if stopped {
		t.Fatal("Stop must not run at startup")
	}
	if err := r.SetEnabled("stop", false); err != nil {
		t.Fatal(err)
	}
	if !stopped {
		t.Error("SetEnabled(false) must call the module's Stop hook")
	}

	// Re-enabling does not re-run Stop (the toggle already flipped).
	stopped = false
	if err := r.SetEnabled("stop", true); err != nil {
		t.Fatal(err)
	}
	if stopped {
		t.Error("re-enable must not call Stop")
	}
}

// resourceModule tracks per-session state that must die with the module.
type resourceModule struct {
	id     string
	active *int
}

func (m *resourceModule) Info() Info {
	return Info{ID: m.id, Name: "Resources", Description: "test", Default: true}
}

func (m *resourceModule) Tools(_ string) []tools.Tool {
	*m.active++
	return []tools.Tool{&fakeTool{name: m.id + "_tool"}}
}

func (m *resourceModule) Stop() {
	*m.active = 0
}

// TestDisabledModulesReleaseResources ties the seams together for the
// promise "disabled means zero cost": disabling drops the tools, calls
// Stop (which releases the held resource), and the core tool surface
// stays intact.
func TestDisabledModulesReleaseResources(t *testing.T) {
	active := 0
	r, err := NewRegistry(filepath.Join(t.TempDir(), "modules.json"),
		&resourceModule{id: "res", active: &active})
	if err != nil {
		t.Fatal(err)
	}
	r.Core(&toolModule{id: "core"})

	if got := r.Tools("/tmp", nil); len(got) != 2 {
		t.Fatalf("core + enabled feature = 2 tools, got %d", len(got))
	}
	if active != 1 {
		t.Fatalf("enabled module should hold its resource, active = %d", active)
	}

	if err := r.SetEnabled("res", false); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatalf("Stop must release the resource, active = %d", active)
	}
	got := r.Tools("/tmp", nil)
	if len(got) != 1 || got[0].Def().Name != "core_tool" {
		t.Fatalf("after disable: only the core tool remains, got %v", got)
	}
}

// capabilitiesModule implements every extension seam so a single module
// can be asserted across all of them.
type capabilitiesModule struct {
	id     string
	hits   int
	active int
}

func (m *capabilitiesModule) Info() Info {
	return Info{ID: m.id, Name: "Everything", Description: "test", Default: true}
}

func (m *capabilitiesModule) Tools(_ string) []tools.Tool {
	m.active++
	return []tools.Tool{&fakeTool{name: m.id + "_tool"}}
}

func (m *capabilitiesModule) Interceptor(_, _ string) agent.ToolInterceptor {
	m.active++
	return &fakeInterceptor{}
}

func (m *capabilitiesModule) Observer(_, _ string) func(agent.Event) {
	m.active++
	return func(ev agent.Event) { _ = ev }
}

func (m *capabilitiesModule) Routes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"GET /ping": func(w http.ResponseWriter, _ *http.Request) {
			m.hits++
			_, _ = w.Write([]byte("pong"))
		},
	}
}

func (m *capabilitiesModule) Stop() { m.active = 0 }

type fakeInterceptor struct{}

func (f *fakeInterceptor) Name() string { return "fake" }
func (f *fakeInterceptor) BeforeTool(context.Context, provider.ToolCall) (string, error) {
	return "", nil
}
func (f *fakeInterceptor) AfterTool(context.Context, provider.ToolCall, tools.Result, error) string {
	return ""
}

// TestEverythingModuleDisables asserts every capability surface of a
// non-core module disappears when it is switched off: tools, observers,
// interceptors, routes, and the Stop hook releases its held resource.
func TestEverythingModuleDisables(t *testing.T) {
	m := &capabilitiesModule{id: "everything"}
	r, err := NewRegistry(filepath.Join(t.TempDir(), "modules.json"), m)
	if err != nil {
		t.Fatal(err)
	}
	r.Core(&toolModule{id: "core"})

	// Enabled: every seam contributes.
	if got := r.Tools("/tmp", nil); len(got) != 2 {
		t.Fatalf("enabled tools = %d, want 2", len(got))
	}
	if got := r.Interceptors("s1", "/tmp", nil); len(got) != 1 {
		t.Fatalf("enabled interceptors = %d, want 1", len(got))
	}
	if got := r.Observers("s1", "/tmp", nil); len(got) != 1 {
		t.Fatalf("enabled observers = %d, want 1", len(got))
	}
	mux := http.NewServeMux()
	r.Mount(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/m/everything/ping")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enabled route = %d, want 200", resp.StatusCode)
	}
	if m.hits != 1 || m.active != 3 {
		t.Fatalf("enabled state wrong: hits=%d active=%d", m.hits, m.active)
	}

	// Disabled: every surface is gone, Stop released the resource.
	if err := r.SetEnabled("everything", false); err != nil {
		t.Fatal(err)
	}
	if got := r.Tools("/tmp", nil); len(got) != 1 || got[0].Def().Name != "core_tool" {
		t.Fatalf("disabled tools = %v, want only core_tool", got)
	}
	if got := r.Interceptors("s1", "/tmp", nil); len(got) != 0 {
		t.Fatalf("disabled interceptors = %d, want 0", len(got))
	}
	if got := r.Observers("s1", "/tmp", nil); len(got) != 0 {
		t.Fatalf("disabled observers = %d, want 0", len(got))
	}
	resp, err = http.Get(srv.URL + "/api/m/everything/ping")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled route = %d, want 404", resp.StatusCode)
	}
	if m.hits != 1 {
		t.Fatalf("disabled handler must not run, hits = %d", m.hits)
	}
	if m.active != 0 {
		t.Fatalf("Stop must release the resource, active = %d", m.active)
	}
}
