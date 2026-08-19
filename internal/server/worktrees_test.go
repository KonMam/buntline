package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KonMam/buntline/internal/module"
	"github.com/KonMam/buntline/internal/module/worktrees"
	"github.com/KonMam/buntline/internal/session"
)

// initGitRepo creates a scratch git repository with one commit and
// returns its path (symlink-resolved, matching the module).
func initGitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if abs, err := filepath.Abs(repo); err == nil {
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			repo = resolved
		}
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@local"},
		{"config", "user.name", "test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", "-A")
	cmd.Dir = repo
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-q", "-m", "initial")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
	return repo
}

// TestCreateSessionInWorktree proves the end-to-end flow: creating a
// session with worktree=true creates an isolated worktree, opens the
// session there, and binds the session to it.
func TestCreateSessionInWorktree(t *testing.T) {
	repo := initGitRepo(t)

	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg, err := module.NewRegistry(filepath.Join(t.TempDir(), "modules.json"), &worktrees.Module{})
	if err != nil {
		t.Fatal(err)
	}
	s := New(emptyConfig(), store, nil, reg, nil)
	t.Cleanup(s.Shutdown)

	body := bytes.NewReader([]byte(`{"worktree":` + jsonQuote(repo) + `,"worktree_name":"feature-x"}`))
	req := httptest.NewRequest("POST", "/api/sessions", body)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create session status = %d, body %s", rec.Code, rec.Body.String())
	}
	var meta session.Meta
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	// The session's workdir is the worktree, under the repo's .buntline.
	if !strings.Contains(meta.Workdir, filepath.Join(".buntline", "worktrees")) {
		t.Errorf("session workdir = %q, want a worktree under the repo", meta.Workdir)
	}
	if _, err := os.Stat(meta.Workdir); err != nil {
		t.Errorf("worktree directory missing: %v", err)
	}
	// The binding records the session.
	bs := worktrees.Bindings(repo)
	if len(bs) != 1 || bs[0].Session != meta.ID || bs[0].Path != meta.Workdir {
		t.Errorf("bindings = %+v, want session %s bound to %s", bs, meta.ID, meta.Workdir)
	}
}

// TestDeleteWorktreeRefusesInUseSession covers cleanup safety: a
// worktree bound to a live session is not removed.
func TestDeleteWorktreeRefusesInUseSession(t *testing.T) {
	repo := initGitRepo(t)

	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	wt := &worktrees.Module{}
	path, _, err := wt.Create(t.Context(), repo, "busy")
	if err != nil {
		t.Fatal(err)
	}
	// Bind to a session that resolves to this worktree.
	meta, err := store.Create("test-model", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := wt.Bind(repo, path, meta.ID); err != nil {
		t.Fatal(err)
	}
	reg, err := module.NewRegistry(filepath.Join(t.TempDir(), "modules.json"), &worktrees.Module{
		Lookup: func(id string) (string, error) {
			if id == meta.ID {
				return path, nil
			}
			return "", os.ErrNotExist
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := New(emptyConfig(), store, nil, reg, nil)
	t.Cleanup(s.Shutdown)

	body := bytes.NewReader([]byte(`{"path":` + jsonQuote(path) + `}`))
	req := httptest.NewRequest("DELETE", "/api/m/worktrees/", body)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete in-use worktree status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Error("the in-use worktree should still exist")
	}
}

// jsonQuote quotes a string as a JSON value.
func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
