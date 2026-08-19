// Package worktrees isolates parallel buntline sessions in separate git
// worktrees: each session gets its own checkout and branch, so two
// sessions working the same repository cannot collide on file edits the
// way two sessions sharing one working directory can. The user's own git
// is only ever touched through the worktree plumbing itself (creation,
// cleanup, and the standard status/diff commands), and cleanup refuses to
// delete work that still exists.
//
// The isolation story follows the field's consensus (Claude Code's
// --worktree, Codex's detached-HEAD worktrees): a session's working
// directory becomes the worktree path, and every buntline mechanism that
// keys off the workdir (file-tool confinement, per-session checkpoints,
// memory, per-repo settings) keys off the isolated checkout instead.
package worktrees

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/KonMam/buntline/internal/module"
)

// Module creates and removes isolated worktrees. It is stateless by
// design: the durable record of a worktree (its repo, branch, and the
// session bound to it) lives in the repo's own .buntline/worktrees.json,
// so a worktree created by one buntline instance is visible to the next.
type Module struct {
	// Lookup resolves a session id to its working directory (wired by
	// the server), so cleanup can refuse to remove a worktree that a
	// live session is using.
	Lookup func(sessionID string) (string, error)
}

func (m *Module) Info() module.Info {
	return module.Info{
		ID:          "worktrees",
		Name:        "Worktrees",
		Description: "Create isolated git worktrees so parallel sessions in one repository never collide.",
		Default:     true,
	}
}

// Binding records one managed worktree: the repo it was created from,
// its branch, and the session that owns it ("" before the session
// exists). Persisted to <repo>/.buntline/worktrees.json.
type Binding struct {
	Path    string `json:"path"`
	Repo    string `json:"repo"`
	Branch  string `json:"branch"`
	Session string `json:"session,omitempty"`
}

// worktreesPath returns the binding file's location: inside the repo's
// .buntline dir, like settings.json and hooks.json, so it stays with the
// repository and is shared across worktrees and machines.
func worktreesPath(repo string) string {
	return filepath.Join(repo, ".buntline", "worktrees.json")
}

// Bindings reads the repository's worktree bindings; a missing file is
// an empty list.
func Bindings(repo string) []Binding {
	data, err := os.ReadFile(worktreesPath(repo))
	if err != nil {
		return nil
	}
	var out []Binding
	_ = json.Unmarshal(data, &out)
	return out
}

// List returns every managed worktree for the repository, sorted by
// path.
func (m *Module) List(repo string) []Binding {
	out := Bindings(repo)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// saveBindings persists the repository's binding list atomically.
func saveBindings(repo string, bs []Binding) error {
	if err := os.MkdirAll(filepath.Dir(worktreesPath(repo)), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(bs, "", "  ")
	if err != nil {
		return err
	}
	tmp := worktreesPath(repo) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, worktreesPath(repo))
}

// run executes git in the repository with a bounded context, returning
// combined output.
func run(ctx context.Context, repo string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repo
	// Inherited GIT_DIR/GIT_WORK_TREE would override the repo we mean
	// (buntline itself may run under a git hook); drop them.
	cmd.Env = scrubGitEnv(os.Environ())
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return strings.TrimSpace(buf.String()), err
}

// scrubGitEnv drops inherited GIT_* variables that would redirect the
// worktree operations to a different repository.
func scrubGitEnv(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, "GIT_DIR=") || strings.HasPrefix(kv, "GIT_WORK_TREE=") ||
			strings.HasPrefix(kv, "GIT_INDEX_FILE=") || strings.HasPrefix(kv, "GIT_OBJECT_DIRECTORY=") ||
			strings.HasPrefix(kv, "GIT_PREFIX=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// gitRoot resolves a directory to its git repository root.
func gitRoot(ctx context.Context, dir string) (string, error) {
	out, err := run(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("%s is not inside a git repository: %s", dir, strings.TrimPrefix(err.Error(), "exit status "))
	}
	if out == "" {
		return "", fmt.Errorf("%s is not inside a git repository", dir)
	}
	return filepath.Clean(out), nil
}

// sanitizeName turns a requested name into a safe worktree name:
// filesystem-hostile characters become dashes, and the name must not
// collide with the repo's own .buntline directory.
func sanitizeName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		name = "buntline-" + time.Now().Format("20060102-150405")
	}
	var sb strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			sb.WriteRune(r)
		default:
			sb.WriteRune('-')
		}
	}
	out := strings.Trim(sb.String(), "-")
	if out == "" {
		out = "worktree"
	}
	return out
}

// Create makes a new detached worktree of repo at
// <repo>/.buntline/worktrees/<name> on branch worktree-<name>, copies the
// repo's bootstrap files into it (so the isolated checkout carries the
// same agent context), and records the binding. Returns the worktree's
// absolute path and branch.
func (m *Module) Create(ctx context.Context, repo, name string) (string, string, error) {
	// Resolve symlinks so the returned path matches what the user sees
	// elsewhere (git resolves them too; on macOS /tmp and /var point
	// into /private).
	if abs, err := filepath.Abs(repo); err == nil {
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			repo = resolved
		}
	}
	root, err := gitRoot(ctx, repo)
	if err != nil {
		return "", "", err
	}
	if _, err := os.Stat(root); err != nil {
		return "", "", fmt.Errorf("repository does not exist: %s", root)
	}
	if err := os.MkdirAll(filepath.Join(root, ".buntline"), 0o755); err != nil {
		return "", "", err
	}

	// The worktrees dir lives under the repo's own .buntline so it stays
	// with the repository and is gitignored by the repo's own ignores
	// (the .buntline dir is where settings/hooks/memory already live).
	base := filepath.Join(root, ".buntline", "worktrees")
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", "", err
	}

	n := sanitizeName(name)
	path := filepath.Join(base, n)
	if _, err := os.Stat(path); err == nil {
		return "", "", fmt.Errorf("a worktree named %q already exists at %s", n, path)
	}
	branch := "worktree-" + n

	// Check that the branch is not already checked out elsewhere (git
	// refuses to create a worktree on a branch another worktree holds).
	if out, err := run(ctx, root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil && out != "" {
		return "", "", fmt.Errorf("branch %q already exists; use a different name", branch)
	}

	if out, err := run(ctx, root, "worktree", "add", "-b", branch, path); err != nil {
		return "", "", fmt.Errorf("git worktree add: %s", out)
	}

	bs := Bindings(root)
	bs = append(bs, Binding{Path: path, Repo: root, Branch: branch})
	if err := saveBindings(root, bs); err != nil {
		return "", "", err
	}

	// Bootstrap after the binding is persisted, so the fresh worktree's
	// copy of worktrees.json includes its own entry.
	if err := m.bootstrap(root, path); err != nil {
		return "", "", err
	}
	return path, branch, nil
}

// bootstrap copies the repository's agent context into a fresh
// worktree. The file list is deliberate and small: tracked files are
// already there; only untracked, gitignored context that a session
// needs should travel. Never overwrite something the fresh checkout
// already has (a fresh worktree can carry tracked settings.json).
// Directories (the memory dir) copy recursively.
func (m *Module) bootstrap(root, path string) error {
	for _, rel := range bootstrapFiles {
		src := filepath.Join(root, rel)
		info, err := os.Stat(src)
		if err != nil {
			continue
		}
		if info.IsDir() {
			if err := copyTree(src, filepath.Join(path, rel)); err != nil {
				return err
			}
			continue
		}
		dst := filepath.Join(path, rel)
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// bootstrapFiles are the untracked, gitignored repository files a fresh
// worktree needs to behave like the parent checkout: the project
// instructions, the per-repo settings, the model's memory, and the
// worktree bindings themselves. A file the fresh checkout already has
// (a tracked settings.json, for instance) is never overwritten.
var bootstrapFiles = []string{
	"AGENTS.md",
	"CLAUDE.md",
	".buntline/settings.json",
	".buntline/memory",
	".buntline/worktrees.json",
}

// Bind binds an existing managed worktree to a session id.
func (m *Module) Bind(repo, path, sessionID string) error {
	bs := Bindings(repo)
	for i := range bs {
		if bs[i].Path == path {
			bs[i].Session = sessionID
			return saveBindings(repo, bs)
		}
	}
	// Unknown binding: adopt it so the session can be tracked.
	bs = append(bs, Binding{Path: path, Repo: repo, Session: sessionID})
	return saveBindings(repo, bs)
}

// SessionFor returns the session bound to a worktree path ("" when
// none).
func (m *Module) SessionFor(path string) string {
	for _, b := range Bindings(repoFor(path)) {
		if b.Path == path {
			return b.Session
		}
	}
	return ""
}

// repoFor walks up from a worktree path to find the repository root.
// A managed worktree always lives at <repo>/.buntline/worktrees/<name>,
// so the repo root is the first ancestor whose .buntline dir holds the
// binding file. (The worktree's own .git is a file pointing at the main
// repo, and its copied .buntline holds only the bootstrap files.)
func repoFor(path string) string {
	cur := filepath.Clean(path)
	for {
		// A worktree itself has a copied .buntline/worktrees.json; that is
		// not the repo root. Only a .buntline at the repo root (the parent
		// of .buntline/worktrees/) counts.
		if _, err := os.Stat(filepath.Join(cur, ".buntline", "worktrees.json")); err == nil &&
			!strings.Contains(cur, filepath.Join(".buntline", "worktrees")+string(filepath.Separator)) {
			return cur
		}
		if filepath.Base(filepath.Dir(cur)) == "worktrees" {
			parent := filepath.Dir(filepath.Dir(cur)) // <repo>/.buntline
			return filepath.Dir(parent)               // <repo>
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return path
		}
		cur = parent
	}
}

// copyTree copies a directory recursively, skipping files that already
// exist at the destination.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return nil
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if _, err := os.Stat(target); err == nil {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// Remove deletes a managed worktree. It refuses to remove a worktree
// that still holds uncommitted changes or untracked files; those belong
// to a human decision (commit, keep the worktree, or force removal), not
// to a cleanup path. The repo's own worktree metadata and the binding
// entry are removed on success.
func (m *Module) Remove(ctx context.Context, path string) error {
	clean, err := isClean(ctx, path)
	if err != nil {
		return err
	}
	if !clean {
		return fmt.Errorf("worktree %s has uncommitted changes or untracked files; commit or keep it", path)
	}
	root := repoFor(path)
	if out, err := run(ctx, root, "worktree", "remove", "--force", path); err != nil {
		return fmt.Errorf("git worktree remove: %s", out)
	}
	bs := Bindings(root)
	out := bs[:0]
	for _, b := range bs {
		if b.Path != path {
			out = append(out, b)
		}
	}
	_ = saveBindings(root, out)
	return nil
}

// isClean reports whether a git worktree has no uncommitted changes and
// no untracked files.
func isClean(ctx context.Context, path string) (bool, error) {
	status, err := run(ctx, path, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("cannot check worktree cleanliness: %s", err)
	}
	return strings.TrimSpace(status) == "", nil
}

// --- HTTP surface ---

// Routes mounts the module's endpoints under /api/m/worktrees/:
//
//	GET    /          list the repository's managed worktrees
//	POST   /          create a worktree (and, when session=true, a
//	                  session bound to it) in one step
//	DELETE /{path}    remove a clean, unbound worktree
func (m *Module) Routes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"GET /":    m.handleList,
		"POST /":   m.handleCreate,
		"DELETE /": m.handleDelete,
	}
}

func (m *Module) handleList(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repo")
	if repo == "" {
		httpErr(w, http.StatusBadRequest, fmt.Errorf("repo is required"))
		return
	}
	writeJSON(w, map[string]any{"worktrees": m.List(repo)})
}

func (m *Module) handleCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Repo    string `json:"repo"`
		Name    string `json:"name"`
		Session bool   `json:"session"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(in.Repo) == "" {
		httpErr(w, http.StatusBadRequest, fmt.Errorf("repo is required"))
		return
	}
	path, branch, err := m.Create(r.Context(), in.Repo, in.Name)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]any{"path": path, "branch": branch})
}

func (m *Module) handleDelete(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(in.Path) == "" {
		httpErr(w, http.StatusBadRequest, fmt.Errorf("path is required"))
		return
	}
	// Refuse to remove a worktree a live session is using.
	if m.Lookup != nil {
		for _, b := range Bindings(repoFor(in.Path)) {
			if b.Path != in.Path || b.Session == "" {
				continue
			}
			if workdir, err := m.Lookup(b.Session); err == nil && workdir == in.Path {
				httpErr(w, http.StatusConflict, fmt.Errorf("worktree is in use by session %s", b.Session))
				return
			}
		}
	}
	if err := m.Remove(r.Context(), in.Path); err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
