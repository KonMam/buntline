// Package checkpoints snapshots the working directory before every
// side-effectful tool call, into a shadow git repository; the user's own
// .git is never touched. Restore puts tracked files back to a snapshot
// (files created after it are left in place; deletion is a bigger hammer
// than a restore button should swing).
package checkpoints

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/KonMam/buntline/internal/agent"
	"github.com/KonMam/buntline/internal/module"
	"github.com/KonMam/buntline/internal/provider"
	"github.com/KonMam/buntline/internal/tools"
)

// sideEffectful lists the tools that warrant a snapshot.
var sideEffectful = map[string]bool{
	"write_file": true,
	"edit_file":  true,
	"bash":       true,
}

// refRe matches the module's snapshot refs: full SHA-1s from rev-parse
// HEAD. Rejecting anything else keeps the restore endpoint's git
// invocation honest: no flag injection, no paths outside the worktree.
var refRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// scrubGitEnv drops inherited GIT_* variables that would override the
// explicit --git-dir/--work-tree flags.
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

// Workdir resolves a session id to its working directory.
type Workdir func(sessionID string) (string, error)

type Module struct {
	// DataDir hosts the shadow repos: <DataDir>/checkpoints/<session>.git
	DataDir string
	Lookup  Workdir

	mu sync.Mutex
	// refs maps sessionID → toolCallID → snapshot ref.
	refs map[string]map[string]string
	// gitMu serializes snapshot sequences (add + commit + rev-parse).
	gitMu sync.Mutex
}

func New(dataDir string, lookup Workdir) *Module {
	return &Module{DataDir: dataDir, Lookup: lookup, refs: map[string]map[string]string{}}
}

func (m *Module) Info() module.Info {
	return module.Info{
		ID:          "checkpoints",
		Name:        "Checkpoints",
		Description: "Snapshot the working directory before every file edit or command, with one-click restore from the trace.",
		Default:     true,
	}
}

func (m *Module) gitDir(sessionID string) string {
	return filepath.Join(m.DataDir, "checkpoints", sessionID+".git")
}

func (m *Module) git(ctx context.Context, sessionID, workdir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	full := append([]string{"--git-dir=" + m.gitDir(sessionID), "--work-tree=" + workdir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	// If buntline itself runs under a git hook (or tests run under
	// lefthook), git exports GIT_DIR/GIT_INDEX_FILE to child processes,
	// which would silently redirect the shadow repo's operations.
	cmd.Env = scrubGitEnv(os.Environ())
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(out.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

func (m *Module) ensureRepo(ctx context.Context, sessionID, workdir string) error {
	gitDir := m.gitDir(sessionID)
	if _, err := os.Stat(gitDir); err == nil {
		return nil
	}
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		return err
	}
	// Plain init with --git-dir elsewhere: a bare repo would refuse the
	// --work-tree flag every subsequent command depends on.
	if _, err := m.git(ctx, sessionID, workdir, "init", "-q"); err != nil {
		return err
	}
	// The shadow repo must never index the user's own .git.
	exclude := filepath.Join(gitDir, "info", "exclude")
	return os.WriteFile(exclude, []byte(".git/\n"), 0o644)
}

// Interceptor snapshots before side-effectful calls.
func (m *Module) Interceptor(sessionID, workdir string) agent.ToolInterceptor {
	return &interceptor{m: m, sessionID: sessionID, workdir: workdir}
}

type interceptor struct {
	m         *Module
	sessionID string
	workdir   string
}

func (i *interceptor) Name() string { return "checkpoints" }

func (i *interceptor) BeforeTool(ctx context.Context, call provider.ToolCall) (string, error) {
	if !sideEffectful[call.Name] {
		return "", nil
	}
	// Tool calls may run concurrently; git's index lock does not share.
	i.m.gitMu.Lock()
	defer i.m.gitMu.Unlock()
	if err := i.m.ensureRepo(ctx, i.sessionID, i.workdir); err != nil {
		return "snapshot failed: " + err.Error(), nil // never block the tool
	}
	if _, err := i.m.git(ctx, i.sessionID, i.workdir, "add", "-A"); err != nil {
		return "snapshot failed: " + err.Error(), nil
	}
	_, _ = i.m.git(ctx, i.sessionID, i.workdir, "-c", "user.name=buntline",
		"-c", "user.email=buntline@local", "commit", "-q", "--allow-empty",
		"-m", fmt.Sprintf("before %s %s", call.Name, call.ID))
	ref, err := i.m.git(ctx, i.sessionID, i.workdir, "rev-parse", "HEAD")
	if err != nil {
		return "snapshot failed: " + err.Error(), nil
	}
	i.m.mu.Lock()
	if i.m.refs[i.sessionID] == nil {
		i.m.refs[i.sessionID] = map[string]string{}
	}
	i.m.refs[i.sessionID][call.ID] = ref
	i.m.mu.Unlock()
	return "snapshot " + ref[:8], nil
}

func (i *interceptor) AfterTool(context.Context, provider.ToolCall, tools.Result, error) string {
	return ""
}

func (m *Module) Routes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"GET /list":     m.handleList,
		"POST /restore": m.handleRestore,
	}
}

// handleList maps tool-call IDs to snapshot refs for one session, so the
// trace can offer restore buttons exactly where snapshots exist.
func (m *Module) handleList(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session")
	m.mu.Lock()
	refs := make(map[string]string, len(m.refs[sessionID]))
	for k, v := range m.refs[sessionID] {
		refs[k] = v
	}
	m.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"refs": refs})
}

func (m *Module) handleRestore(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Session string `json:"session"`
		Ref     string `json:"ref"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Session == "" || in.Ref == "" {
		http.Error(w, `{"error":"session and ref are required"}`, http.StatusBadRequest)
		return
	}
	// The module only ever records full SHAs (rev-parse HEAD), so a ref
	// is a 40-hex string. Anything else is rejected up front: a value
	// that starts with "--" would be parsed as a git flag, not a ref,
	// and one containing "/" or ".." could name paths outside the
	// working tree.
	if !refRe.MatchString(in.Ref) {
		http.Error(w, `{"error":"invalid ref"}`, http.StatusBadRequest)
		return
	}
	workdir, err := m.Lookup(in.Session)
	if err != nil {
		http.Error(w, `{"error":"unknown session"}`, http.StatusBadRequest)
		return
	}
	if _, err := m.git(r.Context(), in.Session, workdir, "checkout", "-f", in.Ref, "--", "."); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
