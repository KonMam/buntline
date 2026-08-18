// Package git surfaces repository state for the session workdir and a
// commit action, shelling out to the git binary, per the project's
// standing decision.
package git

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/KonMam/tether/internal/module"
)

// Workdir resolves a session id to its working directory.
type Workdir func(sessionID string) (string, error)

type Module struct {
	Lookup Workdir
}

func (m *Module) Info() module.Info {
	return module.Info{
		ID:          "git",
		Name:        "Git",
		Description: "Show the workdir's branch and change count in the header, with a one-click commit of the agent's work.",
		Default:     true,
	}
}

func (m *Module) Routes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"GET /status":  m.handleStatus,
		"POST /commit": m.handleCommit,
	}
}

func run(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

func (m *Module) handleStatus(w http.ResponseWriter, r *http.Request) {
	dir, err := m.Lookup(r.URL.Query().Get("session"))
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if _, err := run(r.Context(), dir, "rev-parse", "--is-inside-work-tree"); err != nil {
		writeJSON(w, map[string]any{"repo": false})
		return
	}
	branch, _ := run(r.Context(), dir, "rev-parse", "--abbrev-ref", "HEAD")
	status, _ := run(r.Context(), dir, "status", "--porcelain")
	changed := 0
	if status != "" {
		changed = len(strings.Split(status, "\n"))
	}

	// Per-file line counts for tracked changes; untracked files are
	// listed as new without counts (git can't diff what it never saw).
	type fileStat struct {
		Path      string `json:"path"`
		Additions int    `json:"additions"`
		Deletions int    `json:"deletions"`
		New       bool   `json:"new,omitempty"`
	}
	files := []fileStat{}
	additions, deletions := 0, 0
	if numstat, err := run(r.Context(), dir, "diff", "HEAD", "--numstat"); err == nil && numstat != "" {
		for _, line := range strings.Split(numstat, "\n") {
			parts := strings.SplitN(line, "\t", 3)
			if len(parts) != 3 {
				continue
			}
			var a, d int
			_, _ = fmt.Sscanf(parts[0], "%d", &a)
			_, _ = fmt.Sscanf(parts[1], "%d", &d)
			additions += a
			deletions += d
			files = append(files, fileStat{Path: parts[2], Additions: a, Deletions: d})
		}
	}
	for _, line := range strings.Split(status, "\n") {
		if strings.HasPrefix(line, "?? ") {
			files = append(files, fileStat{Path: strings.TrimPrefix(line, "?? "), New: true})
		}
	}

	writeJSON(w, map[string]any{
		"repo":      true,
		"branch":    branch,
		"changed":   changed,
		"additions": additions,
		"deletions": deletions,
		"files":     files,
	})
}

func (m *Module) handleCommit(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Session string `json:"session"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Message) == "" {
		fail(w, http.StatusBadRequest, fmt.Errorf("commit message is required"))
		return
	}
	dir, err := m.Lookup(in.Session)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if _, err := run(r.Context(), dir, "add", "-A"); err != nil {
		fail(w, http.StatusInternalServerError, fmt.Errorf("git add: %w", err))
		return
	}
	if out, err := run(r.Context(), dir, "commit", "-m", in.Message); err != nil {
		fail(w, http.StatusConflict, fmt.Errorf("git commit: %s", out))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
