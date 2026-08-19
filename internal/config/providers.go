package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// CatalogProvider is one curated entry in the provider catalog: the data
// the Models view renders and the setup flow drives. The catalog is data,
// not code paths: every entry still speaks the one OpenAI-compatible
// client, so adding a provider is a table row, never an adapter.
type CatalogProvider struct {
	// Name is the provider's key, used as the profile name and as the
	// secret-name prefix (DEEPSEEK_API_KEY etc.).
	Name string `json:"name"`
	// Label is the human name shown in the UI ("Z.AI (GLM)").
	Label string `json:"label"`
	// Tag is the one-line description under the name in the picker.
	Tag string `json:"tag"`
	// BaseURL is the OpenAI-compatible endpoint. Local providers carry
	// their default endpoint; the setup pane lets the user edit it.
	BaseURL string `json:"base_url"`
	// Env is the conventional environment/secret name for the API key
	// (e.g. "DEEPSEEK_API_KEY"). Empty for local providers that need no
	// key (Ollama, LM Studio, llama.cpp).
	Env string `json:"env,omitempty"`
	// KeyURL is where the user creates an API key. Shown as a link in
	// the setup pane when Env is set.
	KeyURL string `json:"key_url,omitempty"`
	// KeyPrefix is the leading substring real keys from this provider
	// share, used to detect the provider from a pasted key (goose's
	// quick-setup trick). Empty when there is no stable prefix.
	KeyPrefix string `json:"key_prefix,omitempty"`
	// Local marks a provider whose models run on this machine. Local
	// providers skip the key step and always appear in the picker.
	Local bool `json:"local,omitempty"`
	// KeyMissing is computed live by the server: true when the provider
	// needs a key and the secret is not set. Never persisted.
	KeyMissing bool `json:"key_missing,omitempty"`
	// Available is computed live by the server: true when the endpoint
	// answers a health probe. Meaningful for local providers (is the
	// server actually running?). Never persisted. Not omitted when false:
	// the UI must be able to tell "offline" apart from "not reported".
	Available bool `json:"available"`
	// Models are the provider's known models, each with a short label
	// and the context window the harness uses for the meter and
	// auto-compaction.
	Models []CatalogModel `json:"models,omitempty"`
}

// CatalogModel is one model in a catalog provider's list.
type CatalogModel struct {
	Name string `json:"name"`
	// Label is the short human description shown under the model name
	// ("fastest" / "reasoning + code").
	Label string `json:"label,omitempty"`
	// ContextWindow feeds KnownContextWindow's table; 0 means unknown.
	ContextWindow int `json:"context_window,omitempty"`
}

// catalog is the curated provider table. Order matters: it is the order
// the picker renders. Hosted first (the "I want a working model now"
// path), then local servers.
var catalog = []CatalogProvider{
	{
		Name:      "deepseek",
		Label:     "DeepSeek",
		Tag:       "Reasoning and fast models, 1M context",
		BaseURL:   "https://api.deepseek.com/v1",
		Env:       "DEEPSEEK_API_KEY",
		KeyURL:    "https://platform.deepseek.com/api_keys",
		KeyPrefix: "sk-",
		Models: []CatalogModel{
			{Name: "deepseek-v4-flash", Label: "fast V4, 1M context", ContextWindow: 1048576},
			{Name: "deepseek-v4-pro", Label: "reasoning V4, 1M context", ContextWindow: 1048576},
		},
	},
	{
		Name:      "zai",
		Label:     "Z.AI (GLM)",
		Tag:       "GLM-5 coding models, 1M context",
		BaseURL:   "https://api.z.ai/api/paas/v4",
		Env:       "ZAI_API_KEY",
		KeyURL:    "https://z.ai/console",
		KeyPrefix: "zai-",
		Models: []CatalogModel{
			{Name: "glm-5.2", Label: "flagship GLM, 1M context", ContextWindow: 1048576},
			{Name: "glm-5-turbo", Label: "fast GLM-5", ContextWindow: 204800},
			{Name: "glm-4.7", Label: "cheaper GLM-4.7", ContextWindow: 204800},
		},
	},
	{
		Name:      "openrouter",
		Label:     "OpenRouter",
		Tag:       "200+ models, one key",
		BaseURL:   "https://openrouter.ai/api/v1",
		Env:       "OPENROUTER_API_KEY",
		KeyURL:    "https://openrouter.ai/keys",
		KeyPrefix: "sk-or-",
		Models: []CatalogModel{
			{Name: "deepseek/deepseek-v4-flash", Label: "DeepSeek V4 fast", ContextWindow: 1048576},
			{Name: "z-ai/glm-5.2", Label: "GLM 5.2", ContextWindow: 1048576},
			{Name: "qwen/qwen3.8-max", Label: "Qwen flagship", ContextWindow: 1000000},
		},
	},
	{
		Name:      "groq",
		Label:     "Groq",
		Tag:       "Fast open models, free tier",
		BaseURL:   "https://api.groq.com/openai/v1",
		Env:       "GROQ_API_KEY",
		KeyURL:    "https://console.groq.com/keys",
		KeyPrefix: "gsk_",
		Models: []CatalogModel{
			{Name: "llama-3.3-70b-versatile", Label: "Llama 70B", ContextWindow: 131072},
			{Name: "openai/gpt-oss-120b", Label: "GPT-OSS 120B", ContextWindow: 131072},
			{Name: "openai/gpt-oss-20b", Label: "GPT-OSS 20B", ContextWindow: 131072},
		},
	},
	{
		Name:    "together",
		Label:   "Together AI",
		Tag:     "Open models on demand",
		BaseURL: "https://api.together.xyz/v1",
		Env:     "TOGETHER_API_KEY",
		KeyURL:  "https://api.together.ai/settings/api-keys",
		Models: []CatalogModel{
			{Name: "meta-llama/Llama-3.3-70B-Instruct-Turbo", Label: "Llama 70B", ContextWindow: 131072},
			{Name: "deepseek-ai/DeepSeek-V4-Flash-0731", Label: "DeepSeek V4 flash", ContextWindow: 1000000},
			{Name: "zai-org/GLM-5.2", Label: "GLM 5.2", ContextWindow: 512000},
		},
	},
	{
		Name:    "moonshot",
		Label:   "Moonshot AI (Kimi)",
		Tag:     "Kimi K3, 1M context",
		BaseURL: "https://api.moonshot.cn/v1",
		Env:     "MOONSHOT_API_KEY",
		KeyURL:  "https://platform.moonshot.cn/console/api-keys",
		Models: []CatalogModel{
			{Name: "kimi-k3", Label: "Kimi K3 flagship", ContextWindow: 1048576},
			{Name: "kimi-k2.7-code", Label: "Kimi K2.7 coding", ContextWindow: 262144},
			{Name: "kimi-k2.6", Label: "Kimi K2.6 general", ContextWindow: 262144},
		},
	},
	{
		Name:      "cerebras",
		Label:     "Cerebras",
		Tag:       "Fast inference, GPT-OSS and open models",
		BaseURL:   "https://api.cerebras.ai/v1",
		Env:       "CEREBRAS_API_KEY",
		KeyURL:    "https://cloud.cerebras.ai/platform/account/api-keys",
		KeyPrefix: "c8-",
		Models: []CatalogModel{
			{Name: "gpt-oss-120b", Label: "GPT-OSS 120B", ContextWindow: 131072},
			{Name: "gemma-4-31b", Label: "Gemma 4 31B", ContextWindow: 131072},
		},
	},
	{
		Name:    "novita",
		Label:   "Novita AI",
		Tag:     "200+ open models",
		BaseURL: "https://api.novita.ai/openai",
		Env:     "NOVITA_API_KEY",
		KeyURL:  "https://novita.ai/settings/key-management",
		Models: []CatalogModel{
			{Name: "zai-org/glm-5.2", Label: "GLM 5.2", ContextWindow: 1048576},
			{Name: "deepseek/deepseek-v4-pro", Label: "DeepSeek V4 pro", ContextWindow: 1048576},
			{Name: "moonshotai/kimi-k2.7-code", Label: "Kimi K2.7 coding", ContextWindow: 262144},
		},
	},
	{
		Name:    "ollama",
		Label:   "Ollama",
		Tag:     "Local models, pull and manage",
		BaseURL: "http://localhost:11434/v1",
		Local:   true,
	},
	{
		Name:    "lmstudio",
		Label:   "LM Studio",
		Tag:     "Local models via LM Studio",
		BaseURL: "http://localhost:1234/v1",
		Local:   true,
	},
	{
		Name:    "llamacpp",
		Label:   "llama.cpp",
		Tag:     "Local models via llama-server",
		BaseURL: "http://localhost:8080/v1",
		Local:   true,
	},
	{
		Name:    "vllm",
		Label:   "vLLM",
		Tag:     "Local or self-hosted vLLM",
		BaseURL: "http://localhost:8000/v1",
		Local:   true,
	},
}

// Catalog returns a copy of the curated provider table.
func Catalog() []CatalogProvider {
	out := make([]CatalogProvider, len(catalog))
	copy(out, catalog)
	return out
}

// CatalogEntry returns the catalog entry for name, or nil.
func CatalogEntry(name string) *CatalogProvider {
	for i := range catalog {
		if catalog[i].Name == name {
			return &catalog[i]
		}
	}
	return nil
}

// DetectProvider matches a pasted key against catalog key prefixes and
// returns the provider name, or "" when nothing matches. Longest prefix
// wins, so an "sk-or-..." OpenRouter key is not caught by DeepSeek's
// plain "sk-".
func DetectProvider(key string) string {
	best := ""
	bestLen := 0
	for i := range catalog {
		p := &catalog[i]
		if p.KeyPrefix == "" {
			continue
		}
		if strings.HasPrefix(key, p.KeyPrefix) && len(p.KeyPrefix) > bestLen {
			best = p.Name
			bestLen = len(p.KeyPrefix)
		}
	}
	return best
}

// ProvidersPath stores providers added or activated through the app,
// kept apart from config.toml so the app never rewrites a user-edited
// file (the same rule that keeps MCP servers in mcp.json).
func ProvidersPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "buntline", "providers.json")
}

// AppProvider is one app-managed (provider, model) selection: a catalog
// model the user has added through the setup flow (given a key, or a
// local server), plus any custom providers they added. The identity is
// Name + Model, so a provider can hold several added models at once;
// each entry merges into ResolvedProfiles behind catalog defaults, below
// user-defined config.toml profiles.
type AppProvider struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	// Model is one model the user added for this provider. Together with
	// Name it is the entry's identity; adding another model of the same
	// provider is a separate entry, not a replacement.
	Model string `json:"model,omitempty"`
	// Env is the secret name the key lives under (catalog default, or
	// the user's choice for custom providers).
	Env string `json:"env,omitempty"`
	// Label/Tag/Models/KeyURL/Local mirror the catalog entry the user
	// activated; a custom provider fills them by hand.
	Label  string         `json:"label,omitempty"`
	Tag    string         `json:"tag,omitempty"`
	Models []CatalogModel `json:"models,omitempty"`
	KeyURL string         `json:"key_url,omitempty"`
	Local  bool           `json:"local,omitempty"`
	// Custom marks a provider that is not a catalog entry.
	Custom bool `json:"custom,omitempty"`
	// Default marks the app-managed (provider, model) pair new sessions
	// use when no explicit or per-repo profile applies. App-owned state;
	// the last model added through the setup flow is the default. Only
	// one entry carries it.
	Default bool `json:"default,omitempty"`
	// Removed is a tombstone: the user removed this provider in the UI.
	// A same-named config.toml profile must not resurrect it; re-adding
	// the provider in the UI clears the flag. App-owned state.
	Removed bool `json:"removed,omitempty"`
}

// LoadProviders reads app-managed providers. A missing file is an empty
// list, not an error. Never nil: the list is served as JSON, and a null
// where the UI expects an array crashes it.
func LoadProviders() []AppProvider {
	var ps []AppProvider
	if data, err := os.ReadFile(ProvidersPath()); err == nil {
		_ = json.Unmarshal(data, &ps)
	}
	if ps == nil {
		ps = []AppProvider{}
	}
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].Name != ps[j].Name {
			return ps[i].Name < ps[j].Name
		}
		return ps[i].Model < ps[j].Model
	})
	return ps
}

// DefaultAppProvider returns the app-managed provider marked as the
// default for new sessions, with its model. The marker lives in
// providers.json (app-owned), never in a user-edited file.
func DefaultAppProvider() (name, model string, ok bool) {
	for _, p := range LoadProviders() {
		if p.Default && p.Model != "" {
			return p.Name, p.Model, true
		}
	}
	return "", "", false
}

// SaveProviders persists app-managed providers atomically.
func SaveProviders(ps []AppProvider) error {
	path := ProvidersPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(ps, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// resolveProvider turns an app-managed provider into a Profile, filling
// catalog defaults where the app entry does not override them. The API
// key resolves exactly like Load() resolves config.toml keys: the env
// var expands now if the shell has it, and the KeyRef names the secret
// the secrets store falls back to at request time.
func resolveProvider(p AppProvider) Profile {
	if c := CatalogEntry(p.Name); c != nil {
		base := p.BaseURL
		if base == "" {
			base = c.BaseURL
		}
		env := p.Env
		if env == "" {
			env = c.Env
		}
		models := p.Models
		if len(models) == 0 {
			models = c.Models
		}
		key := ""
		if env != "" {
			key = os.Getenv(env)
		}
		return Profile{
			Name:          p.Name,
			BaseURL:       base,
			Model:         p.Model,
			APIKey:        key,
			KeyRef:        env,
			ContextWindow: contextWindowFor(models, p.Model),
		}
	}
	key := ""
	if p.Env != "" {
		key = os.Getenv(p.Env)
	}
	return Profile{
		Name:    p.Name,
		BaseURL: p.BaseURL,
		Model:   p.Model,
		APIKey:  key,
		KeyRef:  p.Env,
	}
}

// contextWindowFor returns the catalog model's window, or 0.
func contextWindowFor(models []CatalogModel, model string) int {
	for _, m := range models {
		if m.Name == model {
			return m.ContextWindow
		}
	}
	return 0
}
