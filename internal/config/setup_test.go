package config

import (
	"testing"
)

// TestDefaultsCarryNoProvider: a fresh install has no endpoint and no
// model: "not configured" is a state the UI handles, never a guessed
// localhost server (the old baked-in Ollama default silently swallowed
// every unresolved profile).
func TestDefaultsCarryNoProvider(t *testing.T) {
	d := Defaults()
	if d.BaseURL != "" || d.Model != "" {
		t.Errorf("Defaults() = base_url %q model %q, want both empty", d.BaseURL, d.Model)
	}
}

func TestConfigured(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if (Config{}).Configured() {
		t.Error("empty config reports configured")
	}
	if !(Config{BaseURL: "http://localhost:11434/v1"}).Configured() {
		t.Error("explicit base_url not configured")
	}
	if !(Config{Profiles: []Profile{{Name: "zai"}}}).Configured() {
		t.Error("config.toml profile not configured")
	}
	err := SaveProviders([]AppProvider{{Name: "deepseek", BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-v4-flash"}})
	if err != nil {
		t.Fatal(err)
	}
	if !(Config{}).Configured() {
		t.Error("app-managed provider not configured")
	}
	// A tombstoned provider does not count.
	err = SaveProviders([]AppProvider{{Name: "deepseek", Model: "deepseek-v4-flash", Removed: true}})
	if err != nil {
		t.Fatal(err)
	}
	if (Config{}).Configured() {
		t.Error("removed provider still reports configured")
	}
}

// TestResolvedProfilesNoSyntheticDefault: with no top-level endpoint
// there is no "default" profile to offer; with one there is.
func TestResolvedProfilesNoSyntheticDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if ps := (Config{}).ResolvedProfiles(); len(ps) != 0 {
		t.Errorf("unconfigured ResolvedProfiles = %v, want empty", ps)
	}
	ps := (Config{BaseURL: "http://localhost:11434/v1", Model: "qwen3.5:9b"}).ResolvedProfiles()
	if len(ps) != 1 || ps[0].Name != "default" {
		t.Errorf("ResolvedProfiles = %v, want the synthetic default", ps)
	}
}

// TestAppDefaultProfile resolves the starred (provider, model) pair for
// headless runs; a removed or default-less list resolves nothing.
func TestAppDefaultProfile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, ok := AppDefaultProfile(); ok {
		t.Error("empty store resolved a default profile")
	}
	err := SaveProviders([]AppProvider{
		{Name: "deepseek", BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-v4-pro"},
		{Name: "deepseek", BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-v4-flash", Default: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	prof, ok := AppDefaultProfile()
	if !ok || prof.Model != "deepseek-v4-flash" || prof.BaseURL != "https://api.deepseek.com/v1" {
		t.Errorf("AppDefaultProfile = %+v %v, want the starred flash entry", prof, ok)
	}
	if prof.KeyRef != "DEEPSEEK_API_KEY" {
		t.Errorf("KeyRef = %q, want the catalog env name", prof.KeyRef)
	}
}

// TestSoleModelIsDefault: with exactly one added model and no star, that
// model serves as the default (a fresh install's first model must open
// the chat); a second model brings back the explicit-star requirement.
func TestSoleModelIsDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	err := SaveProviders([]AppProvider{
		{Name: "ollama", BaseURL: "http://localhost:11434/v1", Model: "qwen3.5:4b", Local: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	name, model, ok := DefaultAppProvider()
	if !ok || name != "ollama" || model != "qwen3.5:4b" {
		t.Errorf("DefaultAppProvider = %q %q %v, want the sole model", name, model, ok)
	}
	err = SaveProviders([]AppProvider{
		{Name: "ollama", BaseURL: "http://localhost:11434/v1", Model: "qwen3.5:4b", Local: true},
		{Name: "ollama", BaseURL: "http://localhost:11434/v1", Model: "qwen3.5:9b", Local: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := DefaultAppProvider(); ok {
		t.Error("two added models with no star resolved a default")
	}
}
