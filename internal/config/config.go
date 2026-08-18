// Package config resolves settings with the precedence:
// flags > environment > ./tether.toml > ~/.config/tether/config.toml.
package config

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	BaseURL string `toml:"base_url"`
	Model   string `toml:"model"`
	APIKey  string `toml:"api_key"`
	Addr    string `toml:"addr"`
	// AllowedHosts are extra hostnames the server accepts in the Host
	// header, for serving under a DNS name (e.g. a Tailscale MagicDNS
	// name). Loopback names, IP literals, and the bound host are always
	// accepted; see server guard for the rebinding rationale.
	AllowedHosts []string `toml:"allowed_hosts"`
	// SessionsDir defaults to ~/.local/share/tether/sessions.
	SessionsDir string `toml:"sessions_dir"`
	// DataDir holds harness state (module toggles); defaults next to
	// SessionsDir.
	DataDir string `toml:"data_dir"`
	Workdir string `toml:"workdir"`
	// Profiles are named provider endpoints beyond the default one.
	Profiles []Profile `toml:"profiles"`
	// Search configures the web_search tool's backend.
	Search Search `toml:"search"`
	// Vision configures the image-to-text translation backend: when a
	// session's provider is text-only and the user attaches images, the
	// vision model describes them and the description rides in the text
	// the main model receives. Without configuration, image sends on
	// text-only providers refuse exactly as before.
	Vision Vision `toml:"vision"`
	// MCPServers are external MCP servers whose tools join the registry.
	MCPServers []MCPServer `toml:"mcp_servers"`
	// MaxRounds caps model calls per turn; 0 uses the built-in backstop.
	MaxRounds int `toml:"max_rounds"`
}

// Search backend: provider is "searxng" (needs url) or "brave" (needs
// api_key).
type Search struct {
	Provider string `toml:"provider"`
	URL      string `toml:"url"`
	APIKey   string `toml:"api_key"`
}

// Vision is the image-description backend: an OpenAI-compatible endpoint
// that accepts image_url content parts. When a session's main provider
// is text-only, attached images are sent here and the returned text is
// appended to the message for the main model. Local Ollama and hosted
// vision APIs both work; the adapter is the same one the main loop
// uses.
type Vision struct {
	BaseURL string `toml:"base_url"`
	Model   string `toml:"model"`
	APIKey  string `toml:"api_key"`
}

// Configured reports whether a usable vision backend is set up.
func (v Vision) Configured() bool {
	return v.BaseURL != "" && v.Model != ""
}

// MCPServer describes one external MCP server. Transport "stdio" runs
// Command+Args as a child process; "http" connects to URL (Streamable
// HTTP).
type MCPServer struct {
	Name      string   `toml:"name" json:"name"`
	Transport string   `toml:"transport" json:"transport"`
	Command   string   `toml:"command" json:"command,omitempty"`
	Args      []string `toml:"args" json:"args,omitempty"`
	URL       string   `toml:"url" json:"url,omitempty"`
	// Env is extra environment for stdio servers. Values support
	// ${VAR} (process environment) and ${secret:NAME} (tether's secrets
	// store), resolved at connect time, never persisted expanded.
	Env map[string]string `toml:"env" json:"env,omitempty"`
}

// Profile is a named provider endpoint + model pairing, switchable
// per-session.
type Profile struct {
	Name    string `toml:"name" json:"name"`
	BaseURL string `toml:"base_url" json:"base_url"`
	Model   string `toml:"model" json:"model"`
	APIKey  string `toml:"api_key" json:"-"`
	// KeyMissing flags a key that referenced an env var which expanded to
	// nothing, surfaced in the UI instead of failing as a runtime 401.
	KeyMissing bool `toml:"-" json:"key_missing,omitempty"`
	// KeyRef is the variable name the config referenced (DEEPSEEK_API_KEY
	// for "${DEEPSEEK_API_KEY}"); the secrets store resolves it at request
	// time when the environment didn't.
	KeyRef string `toml:"-" json:"key_ref,omitempty"`
	// ContextWindow (tokens) enables automatic compaction: when a turn's
	// prompt exceeds 85% of it, the session compacts after the turn.
	// Zero disables the behavior.
	ContextWindow int `toml:"context_window" json:"context_window,omitempty"`
}

// ResolvedProfiles returns the profile list with "default" (built from the
// top-level settings) first, unless the user defined their own default.
// App-managed providers (providers.json, added through the UI) take
// precedence over config.toml profiles of the same name: a provider the
// user activated in the Models view is their most recent, explicit
// choice, and it must stay removable and visible there. A provider the
// user removed in the UI is tombstoned (Removed), so a same-named
// config.toml profile does not resurrect it in the model picker.
// Each app entry is one (name, model) pair the user added, so a provider
// with several added models contributes several profiles; the composer
// dropdown lists them all.
func (c Config) ResolvedProfiles() []Profile {
	for _, p := range c.Profiles {
		if p.Name == "default" {
			return c.Profiles
		}
	}
	def := Profile{Name: "default", BaseURL: c.BaseURL, Model: c.Model, APIKey: c.APIKey}
	// A hosted endpoint with no key cannot serve anything; flag it so the
	// UI says "key missing" instead of probing and surfacing 401s.
	def.KeyMissing = c.APIKey == "" && !LocalBaseURL(c.BaseURL)
	apps := LoadProviders()
	removed := map[string]bool{}
	appNames := map[string]bool{}
	out := []Profile{def}
	for _, ap := range apps {
		if ap.Removed {
			removed[ap.Name] = true
			continue
		}
		appNames[ap.Name] = true
		out = append(out, resolveProvider(ap))
	}
	for _, p := range c.Profiles {
		if removed[p.Name] || appNames[p.Name] {
			continue
		}
		out = append(out, p)
	}
	return out
}

func Defaults() Config {
	home, _ := os.UserHomeDir()
	return Config{
		BaseURL:     "http://localhost:11434/v1",
		Model:       "qwen3.5:9b",
		Addr:        "localhost:7433",
		SessionsDir: filepath.Join(home, ".local", "share", "tether", "sessions"),
		DataDir:     filepath.Join(home, ".local", "share", "tether"),
	}
}

// Load layers config files and env vars over defaults. Flag overrides are
// applied by the caller after Load.
func Load() (Config, error) {
	cfg := Defaults()

	home, _ := os.UserHomeDir()
	for _, path := range []string{
		filepath.Join(home, ".config", "tether", "config.toml"),
		"tether.toml",
	} {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if _, err := toml.DecodeFile(path, &cfg); err != nil {
			return cfg, fmt.Errorf("%s: %w", path, err)
		}
	}

	if v := os.Getenv("TETHER_BASE_URL"); v != "" {
		cfg.BaseURL = v
	}
	if v := os.Getenv("TETHER_MODEL"); v != "" {
		cfg.Model = v
	}
	if v := os.Getenv("TETHER_API_KEY"); v != "" {
		cfg.APIKey = v
	}
	if v := os.Getenv("TETHER_ADDR"); v != "" {
		cfg.Addr = v
	}
	if v := os.Getenv("TETHER_ALLOWED_HOSTS"); v != "" {
		cfg.AllowedHosts = strings.Split(v, ",")
	}
	if v := os.Getenv("TETHER_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv("TETHER_SESSIONS_DIR"); v != "" {
		cfg.SessionsDir = v
	}
	if v := os.Getenv("TETHER_WORKDIR"); v != "" {
		cfg.Workdir = v
	}

	// API keys in config files may reference env vars ("${DEEPSEEK_API_KEY}")
	// so keys never live in a committed file. An env reference that expands
	// to nothing is flagged loudly: tether was launched from a shell that
	// doesn't have the variable, and a silent empty key means a confusing
	// 401 later.
	cfg.APIKey = os.ExpandEnv(cfg.APIKey)
	cfg.Search.APIKey = os.ExpandEnv(cfg.Search.APIKey)
	cfg.Vision.APIKey = os.ExpandEnv(cfg.Vision.APIKey)
	for i := range cfg.Profiles {
		raw := cfg.Profiles[i].APIKey
		cfg.Profiles[i].APIKey = os.ExpandEnv(raw)
		if m := keyRefRe.FindStringSubmatch(raw); m != nil {
			cfg.Profiles[i].KeyRef = m[1]
		}
		if cfg.Profiles[i].APIKey == "" && cfg.Profiles[i].KeyRef != "" {
			// Not fatal: the secrets store may supply it at request time.
			cfg.Profiles[i].KeyMissing = true
		}
	}
	return cfg, nil
}

var keyRefRe = regexp.MustCompile(`\$\{([A-Za-z0-9_]+)\}`)

// LocalBaseURL reports whether a base URL points at this machine; local
// endpoints (Ollama, llama.cpp) need no API key.
func LocalBaseURL(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	if h == "localhost" {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate()
	}
	return false
}

// WorkdirSettings are per-repository overrides from
// <workdir>/.tether/settings.json: module toggles and the default
// model/profile for sessions opened there.
type WorkdirSettings struct {
	Modules map[string]bool `json:"modules"`
	Model   string          `json:"model"`
	Profile string          `json:"profile"`
	// Allow lists tools approved for good in this repository: a tool
	// name ("write_file"), or "bash:<prefix>" matching the start of a
	// bash command ("bash:go test"). Committable, like the rest of the
	// file.
	Allow []string `json:"allow,omitempty"`
}

// LoadWorkdirSettings reads the per-repo settings; a missing or invalid
// file is simply no overrides.
func LoadWorkdirSettings(workdir string) WorkdirSettings {
	var ws WorkdirSettings
	data, err := os.ReadFile(filepath.Join(workdir, ".tether", "settings.json"))
	if err != nil {
		return ws
	}
	_ = json.Unmarshal(data, &ws)
	return ws
}

// Allowed reports whether the allowlist covers this call. Plain entries
// match the tool name; "bash:<prefix>" entries match a bash command by
// prefix, so "bash:go test" covers "go test ./..." but not "go build".
// A command with shell control operators never matches a prefix rule:
// "cd repo && anything" shares a prefix with an approved "cd repo", and
// the suffix after the operator is a different command that deserves its
// own look.
func Allowed(allow []string, toolName, argsJSON string) bool {
	for _, entry := range allow {
		if entry == toolName {
			return true
		}
		prefix, ok := strings.CutPrefix(entry, "bash:")
		if !ok || toolName != "bash" {
			continue
		}
		var args struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			continue
		}
		cmd := strings.TrimSpace(args.Command)
		if hasControlOperator(cmd) {
			continue
		}
		if strings.HasPrefix(cmd, prefix) {
			return true
		}
	}
	return false
}

// hasControlOperator reports whether a shell command chains, pipes, or
// substitutes further commands.
func hasControlOperator(cmd string) bool {
	for _, op := range []string{"&&", "||", ";", "|", "`", "$(", "\n", ">", "<"} {
		if strings.Contains(cmd, op) {
			return true
		}
	}
	return false
}

// AllowRule derives the allowlist entry an "always allow" decision
// stores: the tool name, or for bash the command's first two tokens,
// wide enough to cover reruns, narrow enough that "git commit" does not
// grant "git push".
func AllowRule(toolName, argsJSON string) string {
	if toolName != "bash" {
		return toolName
	}
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return toolName
	}
	fields := strings.Fields(args.Command)
	if len(fields) == 0 {
		return toolName
	}
	if len(fields) == 1 {
		return "bash:" + fields[0]
	}
	return "bash:" + fields[0] + " " + fields[1]
}

// AddWorkdirAllow appends a rule to the repository's allowlist,
// preserving whatever else settings.json holds.
func AddWorkdirAllow(workdir, rule string) error {
	path := filepath.Join(workdir, ".tether", "settings.json")
	raw := map[string]json.RawMessage{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &raw)
	}
	var allow []string
	if cur, ok := raw["allow"]; ok {
		_ = json.Unmarshal(cur, &allow)
	}
	for _, a := range allow {
		if a == rule {
			return nil
		}
	}
	allow = append(allow, rule)
	enc, err := json.Marshal(allow)
	if err != nil {
		return err
	}
	raw["allow"] = enc
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ProjectInstructions loads AGENTS.md (the cross-tool standard) or
// CLAUDE.md from the workdir. Injected as a labeled first user message,
// NOT into the system prompt: a fat system prompt measurably stops small
// models from calling tools (they summarize it instead), and keeping the
// system prefix small and stable is also what makes it cacheable across
// sessions, the same conclusion Claude Code's design reflects.
func ProjectInstructions(workdir string) (name, content string) {
	for _, n := range []string{"AGENTS.md", "CLAUDE.md"} {
		data, err := os.ReadFile(filepath.Join(workdir, n))
		if err != nil {
			continue
		}
		return n, string(data)
	}
	return "", ""
}

// defaultSystemPrompt is level 1 of the prompt stack: one prompt for the
// whole of tether, deliberately minimal: models are trained to know what
// a coding agent is, mainstream 10k-token prompts buy little, and a fat
// prompt measurably stops small models from calling tools. Level 2 is the
// per-directory AGENTS.md, injected into the conversation, not here. Both
// levels are visible in the UI.
const defaultSystemPrompt = `You are tether, a coding agent with real filesystem and shell access through your tools.

Rules:
- Questions about this project's files or code: read the actual files with tools first. Never answer from memory, never claim you lack file access.
- edit_file replaces one exact occurrence of old_string; include enough surrounding context to make it unique. Read a file before editing it.
- Work in small verifiable steps; failures and diagnostics come back in tool results; react to them.
- Always end your turn with a direct answer. Internal reasoning alone is not a reply. Be concise; do not restate tool output the user just saw.`

// SystemPromptPath is the global override location (level 1, user-edited).
func SystemPromptPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "tether", "system.md")
}

// knownContextWindows maps model name prefixes to their documented
// context window, for APIs that do not report one (DeepSeek, Moonshot,
// Zhipu). A profile's context_window always wins; this is the fallback
// that makes the context meter and auto-compaction work out of the box.
var knownContextWindows = []struct {
	prefix string
	window int
}{
	{"deepseek-chat", 1048576},
	{"deepseek-reasoner", 1048576},
	{"deepseek-v4", 1048576},
	{"kimi-k3", 1048576},
	{"kimi-k2", 262144},
	{"moonshot", 131072},
	{"glm-5-turbo", 204800},
	{"glm-5", 1048576},
	{"glm-4.7", 204800},
	{"glm-4", 131072},
}

// KnownContextWindow returns the documented context window for a model
// name, or 0 when the model is not in the table.
func KnownContextWindow(model string) int {
	m := strings.ToLower(model)
	for _, k := range knownContextWindows {
		if strings.HasPrefix(m, k.prefix) {
			return k.window
		}
	}
	return 0
}

// MCPServersPath stores MCP servers added through the app, kept apart
// from config.toml so the app never rewrites a user-edited file.
func MCPServersPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "tether", "mcp.json")
}

// LoadMCPServers reads app-managed MCP servers. A missing file is an
// empty list, not an error.
func LoadMCPServers() []MCPServer {
	data, err := os.ReadFile(MCPServersPath())
	if err != nil {
		return nil
	}
	var servers []MCPServer
	if err := json.Unmarshal(data, &servers); err != nil {
		return nil
	}
	return servers
}

// SaveMCPServers persists app-managed MCP servers atomically.
func SaveMCPServers(servers []MCPServer) error {
	path := MCPServersPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(servers, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// SystemPrompt composes level 1: the global prompt (override file or
// built-in default) plus a short environment block. Small and stable by
// design: it is the cacheable prefix of every request.
func SystemPrompt(workdir string) string {
	base := defaultSystemPrompt
	if data, err := os.ReadFile(SystemPromptPath()); err == nil && strings.TrimSpace(string(data)) != "" {
		base = strings.TrimSpace(string(data))
	}
	return base + "\n\nEnvironment: working directory " + workdir + ", os " + runtime.GOOS + "."
}

// GlobalSystemPrompt returns the current global prompt (without the
// environment block) and whether it is a user override.
func GlobalSystemPrompt() (prompt string, overridden bool) {
	if data, err := os.ReadFile(SystemPromptPath()); err == nil && strings.TrimSpace(string(data)) != "" {
		return strings.TrimSpace(string(data)), true
	}
	return defaultSystemPrompt, false
}

// SetGlobalSystemPrompt writes (or, with empty input, removes) the global
// override. Applies to sessions attached after the change.
func SetGlobalSystemPrompt(prompt string) error {
	path := SystemPromptPath()
	if strings.TrimSpace(prompt) == "" {
		err := os.Remove(path)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.TrimSpace(prompt)+"\n"), 0o644)
}
