package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDetectProvider proves pasted-key detection: longest prefix wins
// (OpenRouter's sk-or- beats DeepSeek's sk-), unknown keys match nothing.
func TestDetectProvider(t *testing.T) {
	cases := []struct {
		key, want string
	}{
		{"sk-abc123", "deepseek"},
		{"sk-or-v1-xyz", "openrouter"},
		{"zai-abc", "zai"},
		{"gsk_abc", "groq"},
		{"c8-abc", "cerebras"},
		{"anything-else", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := DetectProvider(c.key); got != c.want {
			t.Errorf("DetectProvider(%q) = %q, want %q", c.key, got, c.want)
		}
	}
}

// TestCatalogProviderBasics checks that the catalog has the entries the
// UI depends on and that they carry the fields the setup flow needs.
func TestCatalogProviderBasics(t *testing.T) {
	cs := Catalog()
	if len(cs) == 0 {
		t.Fatal("catalog is empty")
	}
	// The example from the plan: GLM must be reachable, and local
	// providers must exist alongside hosted ones.
	if c := CatalogEntry("zai"); c == nil || !c.Local && c.Env == "" {
		t.Fatalf("zai entry wrong or missing: %+v", c)
	}
	if c := CatalogEntry("deepseek"); c == nil || c.Env == "" || c.BaseURL == "" {
		t.Fatalf("deepseek entry wrong or missing: %+v", c)
	}
	if c := CatalogEntry("ollama"); c == nil || !c.Local {
		t.Fatalf("ollama entry should be local: %+v", c)
	}
	// Every catalog entry needs a name and a base URL.
	for _, c := range cs {
		if c.Name == "" || c.BaseURL == "" {
			t.Fatalf("entry missing name or base_url: %+v", c)
		}
	}
}

// TestResolvedProfilesAppendAppProviders proves app-managed providers
// merge behind the default and config.toml profiles, and that a
// user-defined profile shadows a catalog entry of the same name.
func TestResolvedProfilesAppendAppProviders(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	path := filepath.Join(dir, ".config", "tether", "providers.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`[
		{"name":"deepseek","model":"deepseek-chat"},
		{"name":"custom-box","base_url":"http://box:8000/v1","model":"m","env":"CUSTOM_KEY"}
	]`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		BaseURL: "http://localhost:11434/v1",
		Model:   "qwen3.5:9b",
		// User config defines its own deepseek; the app-managed one must
		// win (the user's most recent UI choice stays removable).
		Profiles: []Profile{{Name: "deepseek", BaseURL: "http://user.example/v1", Model: "mine"}},
	}

	ps := cfg.ResolvedProfiles()
	if len(ps) != 3 {
		t.Fatalf("profiles = %d, want 3: %+v", len(ps), ps)
	}
	if ps[0].Name != "default" || ps[0].Model != "qwen3.5:9b" {
		t.Fatalf("default profile wrong: %+v", ps[0])
	}
	// App providers (sorted by name) win over config: custom-box first,
	// then deepseek with catalog defaults; the config deepseek is
	// shadowed. APIKey is the expanded env value (empty here).
	if ps[1].Name != "custom-box" || ps[1].KeyRef != "CUSTOM_KEY" || ps[1].APIKey != "" {
		t.Fatalf("custom app provider wrong: %+v", ps[1])
	}
	if ps[2].Name != "deepseek" || ps[2].Model != "deepseek-chat" || ps[2].BaseURL != "https://api.deepseek.com/v1" {
		t.Fatalf("app provider should win over config: %+v", ps[2])
	}
}

// TestResolvedProfilesCatalogDefaults proves an app-managed catalog
// provider fills base_url, env, and context window from the catalog when
// the app entry only names the provider and model.
func TestResolvedProfilesCatalogDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	path := filepath.Join(dir, ".config", "tether", "providers.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`[{"name":"zai","model":"glm-4.7"}]`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Config{BaseURL: "http://localhost:11434/v1", Model: "qwen3.5:9b"}
	ps := cfg.ResolvedProfiles()
	if len(ps) != 2 {
		t.Fatalf("profiles = %d, want 2", len(ps))
	}
	p := ps[1]
	if p.Name != "zai" || p.BaseURL != "https://api.z.ai/api/paas/v4" {
		t.Fatalf("catalog base_url not applied: %+v", p)
	}
	if p.KeyRef != "ZAI_API_KEY" || p.APIKey != "" {
		t.Fatalf("catalog env not applied: %+v", p)
	}
	if p.ContextWindow != 204800 {
		t.Fatalf("catalog context window not applied: got %d", p.ContextWindow)
	}
}

// TestResolvedProfilesMultipleModelsPerProvider proves one app entry per
// (name, model): adding two models of one provider yields two profiles,
// each with its own model and context window.
func TestResolvedProfilesMultipleModelsPerProvider(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	path := filepath.Join(dir, ".config", "tether", "providers.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`[
		{"name":"zai","model":"glm-5.2","default":true},
		{"name":"zai","model":"glm-5-turbo"}
	]`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Config{BaseURL: "http://localhost:11434/v1", Model: "qwen3.5:9b"}
	ps := cfg.ResolvedProfiles()
	if len(ps) != 3 {
		t.Fatalf("profiles = %d, want 3 (default + 2 zai models): %+v", len(ps), ps)
	}
	if ps[1].Name != "zai" || ps[1].Model != "glm-5-turbo" || ps[1].ContextWindow != 204800 {
		t.Fatalf("first zai entry wrong: %+v", ps[1])
	}
	if ps[2].Name != "zai" || ps[2].Model != "glm-5.2" || ps[2].ContextWindow != 1048576 {
		t.Fatalf("second zai entry wrong: %+v", ps[2])
	}
}

// TestSaveLoadProviders round-trips the app-managed store.
func TestSaveLoadProviders(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	in := []AppProvider{
		{Name: "deepseek", Model: "deepseek-chat"},
		{Name: "custom-box", BaseURL: "http://box:8000/v1", Model: "m", Env: "CUSTOM_KEY", Custom: true},
	}
	if err := SaveProviders(in); err != nil {
		t.Fatal(err)
	}
	out := LoadProviders()
	if len(out) != 2 {
		t.Fatalf("loaded %d, want 2", len(out))
	}
	if out[0].Name != "custom-box" || out[1].Name != "deepseek" {
		t.Fatalf("unexpected order: %+v", out) // sorted by name
	}
	if out[1].Model != "deepseek-chat" {
		t.Fatalf("model not round-tripped: %+v", out[1])
	}
}

// TestDefaultProfileKeyMissing: a hosted default endpoint with no key is
// flagged so the UI shows "key missing" instead of probing into a 401;
// local endpoints need no key and are never flagged.
func TestDefaultProfileKeyMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cases := []struct {
		baseURL, key string
		want         bool
	}{
		{"https://api.deepseek.com/v1", "", true},
		{"https://api.deepseek.com/v1", "sk-x", false},
		{"http://localhost:11434/v1", "", false},
		{"http://192.168.0.50:11434/v1", "", false},
	}
	for _, c := range cases {
		cfg := Config{BaseURL: c.baseURL, APIKey: c.key}
		def := cfg.ResolvedProfiles()[0]
		if def.KeyMissing != c.want {
			t.Errorf("%s key=%q: KeyMissing = %v, want %v", c.baseURL, c.key, def.KeyMissing, c.want)
		}
	}
}
