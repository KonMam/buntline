package tools

import (
	"regexp"
	"strings"
)

// Terminal escape sequences (colors, cursor movement, OSC titles) are
// noise in a transcript: the model reads them as garbage tokens and the
// UI renders them as literal text. Shell output is stripped of them, and
// scrubbedEnv sets NO_COLOR so well-behaved programs skip them entirely.
var ansiRe = regexp.MustCompile(`\x1b(\[[0-9;?]*[ -/]*[@-~]|\][^\x07\x1b]*(\x07|\x1b\\)|[@-Z\\-_])`)

func stripANSI(s string) string {
	if !strings.Contains(s, "\x1b") {
		return s
	}
	return ansiRe.ReplaceAllString(s, "")
}
