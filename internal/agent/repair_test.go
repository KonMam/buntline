package agent

import (
	"testing"

	"github.com/KonMam/tether/internal/provider"
)

func TestRepairJSON(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"valid passthrough", `{"path":"a.go"}`, `{"path":"a.go"}`, true},
		{"trailing comma", `{"path":"a.go",}`, `{"path":"a.go"}`, true},
		{"truncated object", `{"path":"a.go","content":"abc`, `{"path":"a.go","content":"abc"}`, true},
		{"unclosed nesting", `{"a":{"b":[1,2`, `{"a":{"b":[1,2]}}`, true},
		{"not an object", `hello`, `hello`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := repairJSON([]byte(tt.in))
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v (got %q)", ok, tt.ok, got)
			}
			if ok && string(got) != tt.want {
				t.Errorf("repaired = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeToolName(t *testing.T) {
	registered := map[string]bool{"read_file": true, "bash": true}
	exists := func(n string) bool { return registered[n] }

	tests := []struct {
		in, want string
	}{
		{"functions.read_file", "read_file"},
		{"Read_File", "read_file"},
		{"bash:0", "bash"},
		{"read_file", ""}, // already correct: nothing to normalize
		{"unknown_tool", ""},
	}
	for _, tt := range tests {
		if got := normalizeToolName(tt.in, exists); got != tt.want {
			t.Errorf("normalizeToolName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestLoopDetector(t *testing.T) {
	l := newLoopDetector()
	same := provider.ToolCall{Name: "grep", Args: `{"pattern":"x"}`}

	// Same call, different results: progress, never a loop.
	for i := 0; i < 8; i++ {
		if l.record(same, string(rune('a'+i))) {
			t.Fatal("varying results flagged as a loop")
		}
	}
	// Same call, same result, repeated: flagged after the threshold.
	l = newLoopDetector()
	tripped := false
	for i := 0; i < 10; i++ {
		if l.record(same, "identical output") {
			tripped = true
			break
		}
	}
	if !tripped {
		t.Error("identical call+result repetition not flagged")
	}
}
