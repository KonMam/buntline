package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KonMam/buntline/internal/config"
	"github.com/KonMam/buntline/internal/session"
)

// TestSendUnresolvableProfileErrors: a session whose profile matches no
// configured provider must refuse the send with a clear error, never
// fall back to another endpoint. This is the Pi regression: the rebrand
// orphaned providers.json and sessions silently dialed the old hardcoded
// Ollama default instead of erroring.
func TestSendUnresolvableProfileErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, store := newTestServer(t)
	meta, err := store.Create("test-model", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	meta.Profile = "ghost"
	if err := store.SaveMeta(meta); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+meta.ID+"/messages",
		strings.NewReader(`{"content":"hi"}`))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rr.Code, rr.Body.String())
	}
	// The body is JSON, so the quoted profile name arrives escaped.
	if !strings.Contains(rr.Body.String(), "ghost") ||
		!strings.Contains(rr.Body.String(), "is not configured") {
		t.Errorf("error = %s, want the unresolved profile named", rr.Body.String())
	}
	msgs, err := store.Messages(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Errorf("transcript has %d messages, want 0 (refused send must not persist)", len(msgs))
	}
}

// TestProviderAddedMidSessionHealsLiveAgent: a session attached while its
// profile was unresolvable holds the error stub; once the provider is
// added (the user re-adds a key/model in the Models view), the very next
// send must pick it up: no restart, no idle eviction, no profile switch.
func TestProviderAddedMidSessionHealsLiveAgent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// A real logger: the send's turn goroutine logs its stream failure.
	s := New(emptyConfig(), store, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(s.Shutdown)
	meta, err := store.Create("test-model", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	meta.Profile = "deepseek"
	if err := store.SaveMeta(meta); err != nil {
		t.Fatal(err)
	}

	// Attach with nothing configured: the agent holds the error stub.
	ls, err := s.resolve(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := ls.agent.Provider().Name(); got != "unconfigured" {
		t.Fatalf("provider before setup = %q, want the error stub", got)
	}

	// The user adds the provider through the UI (providers.json appears).
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer backend.Close()
	err = config.SaveProviders([]config.AppProvider{{
		Name: "deepseek", BaseURL: backend.URL + "/v1", Model: "test-model",
	}})
	if err != nil {
		t.Fatal(err)
	}

	// A plain send, same profile and nothing switched, must re-resolve.
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+meta.ID+"/messages",
		strings.NewReader(`{"content":"hi"}`))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", rr.Code, rr.Body.String())
	}
	if got := ls.agent.Provider().Name(); got == "unconfigured" {
		t.Error("provider still the error stub after the provider was added")
	}
	// Let the turn goroutine finish (its stream fails fast against the
	// 500 backend) so TempDir cleanup does not race session writes.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if msgs, err := store.Messages(meta.ID); err == nil && len(msgs) > 0 && !ls.agent.Busy() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("turn never finished")
}

// TestSetModelSameProfileRebuildsProvider: re-picking the session's
// current profile in the model dropdown must rebuild the provider (the
// user's way of applying a fixed key/endpoint), not short-circuit on the
// unchanged name.
func TestSetModelSameProfileRebuildsProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, store := newTestServer(t)
	meta, err := store.Create("test-model", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	meta.Profile = "deepseek"
	if err := store.SaveMeta(meta); err != nil {
		t.Fatal(err)
	}
	ls, err := s.resolve(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := ls.agent.Provider().Name(); got != "unconfigured" {
		t.Fatalf("provider before setup = %q, want the error stub", got)
	}

	err = config.SaveProviders([]config.AppProvider{{
		Name: "deepseek", BaseURL: "https://api.deepseek.com/v1", Model: "test-model",
	}})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+meta.ID+"/model",
		strings.NewReader(`{"model":"test-model","profile":"deepseek"}`))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("set-model status = %d (body %s)", rr.Code, rr.Body.String())
	}
	if got := ls.agent.Provider().Name(); got != "openai-compat" {
		t.Errorf("provider after set-model = %q, want a real provider", got)
	}
}

// TestSetModelUnresolvableProfileRejected: switching to a profile that
// does not resolve is a 400 and leaves the session's profile untouched.
func TestSetModelUnresolvableProfileRejected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, store := newTestServer(t)
	meta, err := store.Create("test-model", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.resolve(meta.ID); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+meta.ID+"/model",
		strings.NewReader(`{"model":"m","profile":"ghost"}`))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rr.Code, rr.Body.String())
	}
	got, err := s.store.GetMeta(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Profile != "" {
		t.Errorf("profile = %q, want unchanged empty", got.Profile)
	}
}

// TestCreateSessionUnconfigured: with no provider anywhere, creating a
// session refuses loudly instead of minting one that cannot run a turn.
func TestCreateSessionUnconfigured(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "no model is configured") {
		t.Errorf("error = %s, want the missing-model message", rr.Body.String())
	}
}

// TestConfigReportsConfigured: /api/config carries the live configured
// flag: false on a fresh install, true as soon as a model is added
// through the UI, with no restart in between.
func TestConfigReportsConfigured(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, _ := newTestServer(t)

	configured := func() bool {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, req)
		var out struct {
			Configured bool `json:"configured"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Configured
	}

	if configured() {
		t.Fatal("fresh install reports configured")
	}
	err := config.SaveProviders([]config.AppProvider{{
		Name: "deepseek", BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-v4-flash",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !configured() {
		t.Fatal("adding a model did not flip configured")
	}
}
