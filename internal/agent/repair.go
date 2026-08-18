package agent

import (
	"crypto/sha256"
	"encoding/json"
	"strings"

	"github.com/KonMam/tether/internal/provider"
)

// loopDetector flags a (tool, args, result) signature repeating more than
// five times within the last ten calls: the Crush heuristic. Including
// the result is deliberate: retrying a call that yields different output
// is progress, not a loop.
type loopDetector struct {
	ring   []string
	counts map[string]int
}

func newLoopDetector() *loopDetector {
	return &loopDetector{counts: map[string]int{}}
}

func (l *loopDetector) record(call provider.ToolCall, result string) bool {
	h := sha256.Sum256([]byte(call.Name + "\x00" + call.Args + "\x00" + result))
	sig := string(h[:])
	l.ring = append(l.ring, sig)
	l.counts[sig]++
	if len(l.ring) > 10 {
		old := l.ring[0]
		l.ring = l.ring[1:]
		if l.counts[old]--; l.counts[old] == 0 {
			delete(l.counts, old)
		}
	}
	return l.counts[sig] > 5
}

// repairJSON attempts deterministic fixes for the malformed tool-call
// arguments local models commonly emit: trailing commas, an unterminated
// string at the end, and unclosed braces/brackets (truncated generation).
// It returns the repaired bytes and whether they now parse. No model
// round-trip: string surgery only, applied before the schema failure
// would otherwise bounce back to the model.
func repairJSON(raw []byte) ([]byte, bool) {
	if json.Valid(raw) {
		return raw, true
	}
	s := strings.TrimSpace(string(raw))
	if s == "" || s[0] != '{' {
		return raw, false
	}

	// Walk the string tracking structure so the fixes below know what is
	// open at the point of truncation.
	var stack []byte
	inString := false
	escaped := false
	lastSignificant := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{', '[':
			stack = append(stack, c)
		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
		if c != ' ' && c != '\n' && c != '\t' && c != '\r' {
			lastSignificant = c
		}
	}

	if inString {
		s += `"`
		lastSignificant = '"'
	}
	if lastSignificant == ',' {
		s = strings.TrimRight(s, " \n\t\r")
		s = strings.TrimSuffix(s, ",")
	}
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] == '{' {
			s += "}"
		} else {
			s += "]"
		}
	}
	// Trailing commas before closers anywhere in the document.
	s = strings.ReplaceAll(s, ",}", "}")
	s = strings.ReplaceAll(s, ",]", "]")

	if json.Valid([]byte(s)) {
		return []byte(s), true
	}
	return raw, false
}

// normalizeToolName maps common model-mangled tool names back to a
// registered tool: case differences, a "functions." prefix, or an
// ":index" suffix. Returns "" if nothing matches.
func normalizeToolName(name string, exists func(string) bool) string {
	candidates := []string{name}
	if trimmed, ok := strings.CutPrefix(name, "functions."); ok {
		candidates = append(candidates, trimmed)
	}
	if i := strings.LastIndexByte(name, ':'); i > 0 {
		candidates = append(candidates, name[:i])
	}
	for _, c := range candidates {
		if c != name && exists(c) {
			return c
		}
		if lower := strings.ToLower(c); lower != name && exists(lower) {
			return lower
		}
	}
	return ""
}
