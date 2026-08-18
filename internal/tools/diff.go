package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// unifiedDiff renders the change between two versions of a file as a
// unified diff, by shelling out to `git diff --no-index`, consistent with
// the project's "shell out to git" stance, and git's diff is the one users
// already read all day. Returns "" if diffing fails; a missing diff only
// degrades the UI, never the edit itself.
func unifiedDiff(ctx context.Context, label, before, after string) string {
	dir, err := os.MkdirTemp("", "tether-diff-")
	if err != nil {
		return ""
	}
	defer func() { _ = os.RemoveAll(dir) }()

	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	if os.WriteFile(a, []byte(before), 0o600) != nil ||
		os.WriteFile(b, []byte(after), 0o600) != nil {
		return ""
	}

	// Exit code 1 just means "files differ".
	out, err := exec.CommandContext(ctx, "git", "diff", "--no-index", "--unified=2", "--", a, b).Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
			return ""
		}
	}

	// Replace git's temp-path header with the real file path.
	lines := strings.Split(string(out), "\n")
	var body []string
	for _, l := range lines {
		if strings.HasPrefix(l, "diff --git") || strings.HasPrefix(l, "index ") ||
			strings.HasPrefix(l, "--- ") || strings.HasPrefix(l, "+++ ") ||
			strings.HasPrefix(l, "new file mode") || strings.HasPrefix(l, "deleted file mode") {
			continue
		}
		body = append(body, l)
	}
	if len(body) == 0 {
		return ""
	}
	header := fmt.Sprintf("--- %s\n+++ %s\n", label, label)
	return truncate(header + strings.Join(body, "\n"))
}
