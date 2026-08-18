package tools

import (
	"os"
	"strings"
)

// secretVarName reports whether an environment variable name looks like a
// secret. Spawned commands must not inherit API keys from the harness
// process, so any name containing KEY, SECRET, TOKEN, or PASSWORD
// (case-insensitive) is dropped.
func secretVarName(name string) bool {
	upper := strings.ToUpper(name)
	for _, needle := range []string{"KEY", "SECRET", "TOKEN", "PASSWORD"} {
		if strings.Contains(upper, needle) {
			return true
		}
	}
	return false
}

// scrubbedEnv returns the process environment with secret-looking
// variables removed. The keep-list survives even if a future pattern
// would match it: a command needs PATH, HOME, TMPDIR, TERM, LANG, LC_*,
// USER, and SHELL to run at all.
func scrubbedEnv() []string {
	keep := func(name string) bool {
		switch {
		case name == "PATH" || name == "HOME" || name == "TMPDIR" ||
			name == "TERM" || name == "LANG" || name == "USER" || name == "SHELL":
			return true
		case strings.HasPrefix(name, "LC_"):
			return true
		}
		return false
	}
	out := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if secretVarName(name) && !keep(name) {
			continue
		}
		// Color-forcing variables are dropped and NO_COLOR is set below:
		// escape sequences are noise in a transcript.
		if name == "FORCE_COLOR" || name == "CLICOLOR_FORCE" {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "NO_COLOR=1", "CLICOLOR=0")
}
