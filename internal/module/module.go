// Package module is the extension seam for built-in features: a module
// contributes API routes (and, through them, UI panels) and can be enabled
// or disabled at runtime. The store UI is a thin veneer over this registry.
//
// Third-party extensibility is deliberately NOT this interface; that role
// belongs to MCP servers, which will surface in the same store UI but speak
// a protocol with an ecosystem behind it.
package module

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/KonMam/buntline/internal/agent"
	"github.com/KonMam/buntline/internal/tools"
)

type Info struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Default is the enabled state before the user ever touches the toggle.
	Default bool `json:"-"`
	// Core marks a module as part of the harness itself, not an optional
	// feature. Core modules are not toggleable: they load at startup,
	// cost nothing while disabled, and their contribution to the model's
	// tool set is decided at build time, not by the runtime toggle.
	Core bool `json:"-"`
}

type Status struct {
	Info
	Enabled bool `json:"enabled"`
}

// CoreStatus carries the non-core registry to the UI: the full list
// (with enabled state) plus a separate "core" section. The store renders
// core modules read-only so the toggle set is exactly the features the
// user can drop.
type CoreStatus struct {
	Core []Status `json:"core"`
	// Modules holds every non-core module. The label is the store page's
	// historical field name; the semantics are now "toggleable features".
	Modules []Status `json:"modules"`
}

type Module interface {
	Info() Info
}

// RouteProvider is implemented by modules that expose HTTP endpoints.
// Patterns are "METHOD /subpath" and get mounted under /api/m/<id>/;
// requests to disabled modules are rejected before the handler runs.
type RouteProvider interface {
	Routes() map[string]http.HandlerFunc
}

// ToolProvider is implemented by modules that contribute agent tools.
// Tools from enabled modules join the built-in registry when a session
// attaches; toggling a module applies to sessions opened afterwards.
type ToolProvider interface {
	Tools(workdir string) []tools.Tool
}

// InterceptorProvider is implemented by modules that hook tool execution
// (hooks, checkpoints, diagnostics). One interceptor per session.
type InterceptorProvider interface {
	Interceptor(sessionID, workdir string) agent.ToolInterceptor
}

// Stopper is implemented by modules that hold resources beyond memory
// (connections, child processes). Stop is called when the module is
// disabled; a re-enable acquires everything lazily again. A disabled
// module must cost nothing.
type Stopper interface {
	Stop()
}

// EventObserver is implemented by modules that watch the agent's event
// stream (Pi-style: modules see everything the UI sees). The returned
// function is called synchronously for every event of one session.
// Observers must be fast and must not block; anything slow belongs in a
// goroutine on the module's side.
type EventObserver interface {
	Observer(sessionID, workdir string) func(agent.Event)
}

// Registry holds modules and their persisted enabled state.
type Registry struct {
	mu      sync.Mutex
	mods    []Module
	core    []Module // core modules: part of the harness, never toggleable
	enabled map[string]bool
	path    string
}

// NewRegistry loads persisted state from statePath (missing file = all
// defaults) and registers the given modules.
func NewRegistry(statePath string, mods ...Module) (*Registry, error) {
	r := &Registry{mods: mods, enabled: map[string]bool{}, path: statePath}
	for _, m := range mods {
		r.enabled[m.Info().ID] = m.Info().Default
	}
	data, err := os.ReadFile(statePath)
	if err == nil {
		var saved map[string]bool
		if err := json.Unmarshal(data, &saved); err != nil {
			return nil, fmt.Errorf("corrupt module state %s: %w", statePath, err)
		}
		for id, on := range saved {
			if _, known := r.enabled[id]; known {
				r.enabled[id] = on
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return r, nil
}

// Core registers a module as part of the harness itself: never shown as
// a toggle, but its resources still join the run (Registry.Tools works on
// it, so a core module may contribute tools). Disabled non-core modules
// stay out of Tools; core modules always contribute.
func (r *Registry) Core(mods ...Module) {
	for _, m := range mods {
		if m == nil {
			continue
		}
		r.core = append(r.core, m)
	}
}

// List returns the toggleable (non-core) modules and their enabled state.
func (r *Registry) List() []Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Status, 0, len(r.mods))
	for _, m := range r.mods {
		out = append(out, Status{Info: m.Info(), Enabled: r.enabled[m.Info().ID]})
	}
	return out
}

// CoreList returns the core modules. Core modules are never toggleable;
// they are shown for transparency, with Enabled always true.
func (r *Registry) CoreList() []Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Status, 0, len(r.core))
	for _, m := range r.core {
		out = append(out, Status{Info: m.Info(), Enabled: true})
	}
	return out
}

// Store returns what the modules page renders: a read-only core section
// and the toggleable feature modules.
func (r *Registry) Store() CoreStatus {
	return CoreStatus{Core: r.CoreList(), Modules: r.List()}
}

func (r *Registry) Enabled(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.enabled[id]
}

func (r *Registry) SetEnabled(id string, on bool) error {
	r.mu.Lock()
	_, known := r.enabled[id]
	if !known {
		r.mu.Unlock()
		return fmt.Errorf("unknown module %q (core modules are never toggleable)", id)
	}
	wasOn := r.enabled[id]
	r.enabled[id] = on
	data, err := json.MarshalIndent(r.enabled, "", "  ")
	if err != nil {
		r.mu.Unlock()
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		r.mu.Unlock()
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		r.mu.Unlock()
		return err
	}
	if err := os.Rename(tmp, r.path); err != nil {
		r.mu.Unlock()
		return err
	}
	var stopping Stopper
	if wasOn && !on {
		for _, m := range r.mods {
			if m.Info().ID != id {
				continue
			}
			if st, ok := m.(Stopper); ok {
				stopping = st
			}
		}
	}
	r.mu.Unlock()
	// Outside the lock: Stop may close connections or kill processes.
	if stopping != nil {
		stopping.Stop()
	}
	return nil
}

// Get returns the module with the given id (nil when unknown).
func (r *Registry) Get(id string) Module {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range r.mods {
		if m.Info().ID == id {
			return m
		}
	}
	return nil
}

// EnabledFor resolves a module's effective state for one session:
// per-repository overrides (from .buntline/settings.json) win over the
// global toggle. Core modules are always effectively enabled: their
// contribution is decided at build time, not by a toggle.
func (r *Registry) EnabledFor(id string, overrides map[string]bool) bool {
	if r.isCore(id) {
		return true
	}
	if v, ok := overrides[id]; ok {
		return v
	}
	return r.Enabled(id)
}

func (r *Registry) isCore(id string) bool {
	for _, m := range r.core {
		if m.Info().ID == id {
			return true
		}
	}
	return false
}

// Tools collects tools from every core module plus every
// effectively-enabled ToolProvider module. Core modules always
// contribute, and first: they are the harness's working surface and are
// decided at build time, not by the runtime toggle.
func (r *Registry) Tools(workdir string, overrides map[string]bool) []tools.Tool {
	var out []tools.Tool
	for _, m := range r.core {
		tp, ok := m.(ToolProvider)
		if !ok {
			continue
		}
		out = append(out, tp.Tools(workdir)...)
	}
	for _, m := range r.mods {
		tp, ok := m.(ToolProvider)
		if !ok || !r.EnabledFor(m.Info().ID, overrides) {
			continue
		}
		out = append(out, tp.Tools(workdir)...)
	}
	return out
}

// Interceptors collects tool interceptors from enabled modules for one
// session.
func (r *Registry) Interceptors(sessionID, workdir string, overrides map[string]bool) []agent.ToolInterceptor {
	var out []agent.ToolInterceptor
	for _, m := range r.mods {
		ip, ok := m.(InterceptorProvider)
		if !ok || !r.EnabledFor(m.Info().ID, overrides) {
			continue
		}
		out = append(out, ip.Interceptor(sessionID, workdir))
	}
	return out
}

// Observers collects event observers from enabled modules for one session.
func (r *Registry) Observers(sessionID, workdir string, overrides map[string]bool) []func(agent.Event) {
	var out []func(agent.Event)
	for _, m := range r.mods {
		eo, ok := m.(EventObserver)
		if !ok || !r.EnabledFor(m.Info().ID, overrides) {
			continue
		}
		if fn := eo.Observer(sessionID, workdir); fn != nil {
			out = append(out, fn)
		}
	}
	return out
}

// Mount registers every RouteProvider module's endpoints on mux under
// /api/m/<id>/, wrapped in an enabled-check.
func (r *Registry) Mount(mux *http.ServeMux) {
	for _, m := range r.mods {
		rp, ok := m.(RouteProvider)
		if !ok {
			continue
		}
		id := m.Info().ID
		for pattern, handler := range rp.Routes() {
			var method, sub string
			if _, err := fmt.Sscanf(pattern, "%s %s", &method, &sub); err != nil {
				panic(fmt.Sprintf("module %s: bad route pattern %q", id, pattern))
			}
			h := handler
			mux.HandleFunc(method+" /api/m/"+id+sub, func(w http.ResponseWriter, req *http.Request) {
				if !r.Enabled(id) {
					http.Error(w, `{"error":"module disabled"}`, http.StatusNotFound)
					return
				}
				h(w, req)
			})
		}
	}
}
