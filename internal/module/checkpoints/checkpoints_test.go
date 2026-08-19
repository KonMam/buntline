package checkpoints

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KonMam/buntline/internal/provider"
)

func TestSnapshotAndRestore(t *testing.T) {
	workdir := t.TempDir()
	path := filepath.Join(workdir, "main.go")
	if err := os.WriteFile(path, []byte("original content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := New(t.TempDir(), func(string) (string, error) { return workdir, nil })
	restore := func(ref string) int {
		t.Helper()
		body := strings.NewReader(`{"session":"sess1","ref":"` + ref + `"}`)
		req := httptest.NewRequest("POST", "/restore", body)
		rec := httptest.NewRecorder()
		m.handleRestore(rec, req)
		return rec.Code
	}
	ic := m.Interceptor("sess1", workdir)

	// Snapshot fires before a side-effectful call...
	call := provider.ToolCall{ID: "call1", Name: "edit_file", Args: `{}`}
	note, err := ic.BeforeTool(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(note, "snapshot ") {
		t.Errorf("snapshot should be noted for the trace, got %q", note)
	}
	m.mu.Lock()
	ref := m.refs["sess1"]["call1"]
	m.mu.Unlock()
	if ref == "" {
		t.Fatal("no snapshot ref recorded")
	}
	if len(ref) != 40 {
		t.Fatalf("snapshot ref = %q, want a 40-char SHA-1", ref)
	}

	// ...the tool then damages the file...
	if err := os.WriteFile(path, []byte("broken content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// ...and restore puts it back.
	if code := restore(ref); code != 204 {
		t.Fatalf("restore status %d", code)
	}

	got, _ := os.ReadFile(path)
	if string(got) != "original content\n" {
		t.Errorf("file after restore = %q", got)
	}

	// Read-only tools don't snapshot.
	if _, err := ic.BeforeTool(context.Background(), provider.ToolCall{ID: "c2", Name: "read_file"}); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	_, snapped := m.refs["sess1"]["c2"]
	m.mu.Unlock()
	if snapped {
		t.Error("read_file should not snapshot")
	}
}

// TestRestoreRejectsNonRefs locks the restore endpoint's input rule:
// only the full SHA-1 refs the module itself records are accepted.
// Flag-shaped values ("--output=...") must not reach git as refs, and
// path-shaped values ("..", "refs/heads/x", "../outside") must not be
// able to name files outside the working tree. Rejections leave the
// workdir untouched.
func TestRestoreRejectsNonRefs(t *testing.T) {
	workdir := t.TempDir()
	path := filepath.Join(workdir, "main.go")
	if err := os.WriteFile(path, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New(t.TempDir(), func(string) (string, error) { return workdir, nil })

	for _, ref := range []string{
		"--output=evil",
		"..",
		"refs/heads/main",
		"../outside",
		"abc",
		strings.Repeat("a", 39),                  // one char short
		strings.Repeat("a", 41),                  // one char long
		"ABCDEF0123456789abcdef0123456789abcdef", // uppercase hex
		"abcdef0123456789abcdef0123456789abcdef\n", // trailing newline
	} {
		body := strings.NewReader(`{"session":"sess1","ref":"` + ref + `"}`)
		req := httptest.NewRequest("POST", "/restore", body)
		rec := httptest.NewRecorder()
		m.handleRestore(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("ref %q status = %d, want 400 (body %s)", ref, rec.Code, rec.Body.String())
		}
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "keep me\n" {
		t.Errorf("rejected refs touched the workdir: %q", got)
	}
}
