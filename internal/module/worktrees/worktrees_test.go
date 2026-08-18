package worktrees

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initRepo creates a scratch git repository with one commit, so
// worktree operations have something to branch from. Returns the repo
// path.
func initRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	// Resolve symlinks the way Create does, so path comparisons in
	// tests match the paths the module returns (macOS: /var and /tmp
	// point into /private).
	if abs, err := filepath.Abs(repo); err == nil {
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			repo = resolved
		}
	}
	runOK(t, repo, "init", "-q")
	runOK(t, repo, "config", "user.email", "test@local")
	runOK(t, repo, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runOK(t, repo, "add", "-A")
	runOK(t, repo, "commit", "-q", "-m", "initial")
	return repo
}

func runOK(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// Under a git hook (lefthook) git exports GIT_DIR/GIT_INDEX_FILE to
	// child processes, which would redirect these commands away from the
	// scratch repo. The module scrubs the same vars in production.
	cmd.Env = scrubGitEnv(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestCreateIsolatesAndBinds covers the happy path: a worktree is
// created on its own branch, the binding records it, and the session
// can be bound to it.
func TestCreateIsolatesAndBinds(t *testing.T) {
	repo := initRepo(t)
	m := &Module{}
	ctx := context.Background()

	path, branch, err := m.Create(ctx, repo, "feature-a")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.HasPrefix(path, repo) {
		t.Errorf("worktree path %q should live under the repo %q", path, repo)
	}
	if branch != "worktree-feature-a" {
		t.Errorf("branch = %q, want worktree-feature-a", branch)
	}
	if _, err := os.Stat(filepath.Join(path, "main.go")); err != nil {
		t.Errorf("worktree should contain the repo's files: %v", err)
	}
	// The worktree is on its own branch, detached from the main one.
	b := runOK(t, path, "rev-parse", "--abbrev-ref", "HEAD")
	if b != "worktree-feature-a" {
		t.Errorf("worktree branch = %q, want worktree-feature-a", b)
	}

	// Editing inside the worktree does not touch the main checkout.
	if err := os.WriteFile(filepath.Join(path, "note.txt"), []byte("isolated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, "note.txt")); err == nil {
		t.Error("editing the worktree leaked into the main checkout")
	}

	// The binding is persisted and readable back.
	bs := m.List(repo)
	if len(bs) != 1 || bs[0].Path != path || bs[0].Branch != branch {
		t.Fatalf("bindings = %+v, want the new worktree", bs)
	}

	if err := m.Bind(repo, path, "sess1"); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if got := m.SessionFor(path); got != "sess1" {
		t.Errorf("SessionFor = %q, want sess1", got)
	}
}

// TestCreateRequiresARepository rejects directories that are not inside
// a git repository, before any worktree is made.
func TestCreateRequiresARepository(t *testing.T) {
	m := &Module{}
	if _, _, err := m.Create(context.Background(), t.TempDir(), "x"); err == nil {
		t.Error("creating a worktree outside a git repository should fail")
	}
}

// TestRemoveRefusesDirtyWork leaves a worktree with uncommitted work in
// place, and removes a clean one.
func TestRemoveRefusesDirtyWork(t *testing.T) {
	repo := initRepo(t)
	m := &Module{}
	ctx := context.Background()

	path, _, err := m.Create(ctx, repo, "dirty")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "uncommitted.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(ctx, path); err == nil {
		t.Error("removing a dirty worktree should fail")
	}
	if _, err := os.Stat(path); err != nil {
		t.Error("the dirty worktree should still exist")
	}

	// Commit the work, then removal succeeds and the binding clears.
	runOK(t, path, "add", "-A")
	runOK(t, path, "commit", "-q", "-m", "finish")
	if err := m.Remove(ctx, path); err != nil {
		t.Fatalf("remove clean worktree: %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("worktree directory should be gone after remove")
	}
	bs := m.List(repo)
	for _, b := range bs {
		if b.Path == path {
			t.Errorf("binding should be cleared after remove: %+v", bs)
		}
	}
}

// TestBootstrapCopiesContext carries the repo's agent context into the
// fresh worktree: the project instructions, per-repo settings, memory,
// and the bindings file itself.
func TestBootstrapCopiesContext(t *testing.T) {
	repo := initRepo(t)
	// The bootstrap files are gitignored (untracked), like the real
	// .tether dir.
	gitignore := ".tether/\n"
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(gitignore), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, "AGENTS.md"), "# Project\nBuild with make.\n")
	writeFile(t, filepath.Join(repo, ".tether", "settings.json"), `{"model":"test-model"}`)
	writeFile(t, filepath.Join(repo, ".tether", "memory", "MEMORY.md"), "# Memory\n- The build is make\n")

	m := &Module{}
	path, _, err := m.Create(context.Background(), repo, "ctx")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	for _, rel := range []string{"AGENTS.md", ".tether/settings.json", ".tether/memory/MEMORY.md", ".tether/worktrees.json"} {
		if _, err := os.Stat(filepath.Join(path, rel)); err != nil {
			t.Errorf("bootstrap did not copy %s: %v", rel, err)
		}
	}
	// The worktree is on its own branch, detached from the main one.
	b := runOK(t, path, "rev-parse", "--abbrev-ref", "HEAD")
	if b != "worktree-ctx" {
		t.Errorf("worktree branch = %q, want worktree-ctx", b)
	}
}

// TestDuplicateNameIsRejected: creating a second worktree with the same
// name fails without touching the first.
func TestDuplicateNameIsRejected(t *testing.T) {
	repo := initRepo(t)
	m := &Module{}
	ctx := context.Background()
	if _, _, err := m.Create(ctx, repo, "same"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Create(ctx, repo, "same"); err == nil {
		t.Error("duplicate worktree name should fail")
	}
	if len(m.List(repo)) != 1 {
		t.Errorf("bindings = %d, want 1", len(m.List(repo)))
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
