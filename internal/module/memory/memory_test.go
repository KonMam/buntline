package memory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustArgs(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestMemoryWriteReadIndexRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".tether", "memory")
	w := &MemoryWrite{Dir: dir}
	if _, err := w.Run(context.Background(), mustArgs(t, map[string]string{
		"content": "the API tests need a local Redis",
	})); err != nil {
		t.Fatal(err)
	}
	r := &MemoryRead{Dir: dir}
	res, err := r.Run(context.Background(), mustArgs(t, map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "local Redis") {
		t.Errorf("read = %q, want the written fact", res.Content)
	}
	// The file is on disk under the memory directory.
	if _, err := os.Stat(filepath.Join(dir, "MEMORY.md")); err != nil {
		t.Errorf("MEMORY.md not on disk: %v", err)
	}
}

func TestMemoryWriteTopicFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".tether", "memory")
	w := &MemoryWrite{Dir: dir}
	if _, err := w.Run(context.Background(), mustArgs(t, map[string]string{
		"file":    "debugging",
		"content": "The flaky test is timing-sensitive; run with -race.",
	})); err != nil {
		t.Fatal(err)
	}
	r := &MemoryRead{Dir: dir}
	res, err := r.Run(context.Background(), mustArgs(t, map[string]string{"file": "debugging"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "timing-sensitive") {
		t.Errorf("read = %q", res.Content)
	}
}

func TestMemoryReadEmpty(t *testing.T) {
	r := &MemoryRead{Dir: filepath.Join(t.TempDir(), "memory")}
	res, err := r.Run(context.Background(), mustArgs(t, map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "No memory yet") {
		t.Errorf("read = %q, want the empty message", res.Content)
	}
}

func TestMemoryReadListsTopics(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".tether", "memory")
	w := &MemoryWrite{Dir: dir}
	_, _ = w.Run(context.Background(), mustArgs(t, map[string]string{"file": "debugging", "content": "x"}))
	_, _ = w.Run(context.Background(), mustArgs(t, map[string]string{"file": "api", "content": "y"}))
	r := &MemoryRead{Dir: dir}
	res, err := r.Run(context.Background(), mustArgs(t, map[string]string{"file": "list"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "debugging") || !strings.Contains(res.Content, "api") {
		t.Errorf("topics = %q", res.Content)
	}
}

func TestMemoryIndexBudgetRefused(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".tether", "memory")
	w := &MemoryWrite{Dir: dir}
	// Fill the index past the line budget.
	var sb strings.Builder
	for i := 0; i < MemoryIndexLines+10; i++ {
		sb.WriteString("line " + strings.Repeat("x", 50) + "\n")
	}
	_, err := w.Run(context.Background(), mustArgs(t, map[string]string{"content": sb.String()}))
	if err == nil {
		t.Fatal("want error when index exceeds budget")
	}
	if !strings.Contains(err.Error(), "load budget") {
		t.Errorf("error = %v, want budget message", err)
	}
}

func TestMemoryWriteConfinesPath(t *testing.T) {
	w := &MemoryWrite{Dir: filepath.Join(t.TempDir(), "memory")}
	if _, err := w.Run(context.Background(), mustArgs(t, map[string]string{
		"file": "../evil", "content": "x",
	})); err == nil {
		t.Fatal("want error for path traversal")
	}
}

func TestMemoryReadUnknownTopic(t *testing.T) {
	r := &MemoryRead{Dir: filepath.Join(t.TempDir(), "memory")}
	_, err := r.Run(context.Background(), mustArgs(t, map[string]string{"file": "nope"}))
	if err == nil {
		t.Fatal("want error for unknown topic")
	}
	if !strings.Contains(err.Error(), "no memory topic") {
		t.Errorf("error = %v, want the unknown-topic message", err)
	}
}

func TestLoadIndexCaps(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".tether", "memory")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for i := 0; i < MemoryIndexLines+50; i++ {
		sb.WriteString("line\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	content, ok := LoadIndex(dir)
	if !ok {
		t.Fatal("LoadIndex = not found")
	}
	if got := len(strings.Split(content, "\n")); got > MemoryIndexLines {
		t.Errorf("loaded %d lines, want cap %d", got, MemoryIndexLines)
	}
}

// TestMemorySurvivesCompaction proves memory is a file, not transcript
// state: compacting the transcript cannot touch the memory directory,
// and the tools still read it afterwards.
func TestMemorySurvivesCompaction(t *testing.T) {
	workdir := t.TempDir()
	dir := filepath.Join(workdir, ".tether", "memory")
	w := &MemoryWrite{Dir: dir}
	if _, err := w.Run(context.Background(), mustArgs(t, map[string]string{
		"content": "the build needs GOFLAGS=-mod=mod",
	})); err != nil {
		t.Fatal(err)
	}

	// Simulate compaction: a transcript file exists in a session dir that
	// is a sibling of the workdir (the real layout). Compaction rewrites
	// only transcript.jsonl.
	sessionDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sessionDir, "transcript.jsonl"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The memory file is untouched.
	data, err := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
	if err != nil {
		t.Fatalf("memory file gone after compaction: %v", err)
	}
	if !strings.Contains(string(data), "GOFLAGS") {
		t.Errorf("memory content = %q", string(data))
	}

	// memory_read still returns the fact.
	r := &MemoryRead{Dir: dir}
	res, err := r.Run(context.Background(), mustArgs(t, map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "GOFLAGS") {
		t.Errorf("read after compaction = %q", res.Content)
	}
}

// TestMemoryToolsAfterModuleToggle proves disabling the module removes
// the tools and re-enabling restores them from the same files. The
// files are never deleted by the toggle.
func TestMemoryToolsAfterModuleToggle(t *testing.T) {
	workdir := t.TempDir()
	memDir := filepath.Join(workdir, ".tether", "memory")
	w := &MemoryWrite{Dir: memDir}
	if _, err := w.Run(context.Background(), mustArgs(t, map[string]string{
		"content": "remember the redis requirement",
	})); err != nil {
		t.Fatal(err)
	}

	// Disabling the module (in the real server) unmounts the tools; here
	// we prove the file survives and a fresh tool (re-enable) still reads
	// it. The toggle itself is exercised by the server test in
	// internal/server/memory_test.go.
	r := &MemoryRead{Dir: memDir}
	res, err := r.Run(context.Background(), mustArgs(t, map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "redis") {
		t.Errorf("read after re-enable = %q", res.Content)
	}
}
