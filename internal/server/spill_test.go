package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KonMam/tether/internal/provider"
	"github.com/KonMam/tether/internal/session"
)

func TestSpillSinkSaveAndRead(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sess", "spill")
	s := newSpillSink(dir)

	content := strings.Repeat("0123456789", 100) // 1000 chars
	id, err := s.Save(content)
	if err != nil {
		t.Fatal(err)
	}
	if id != "1" {
		t.Errorf("first id = %q, want 1", id)
	}
	if got, err := s.Read(id, 0, 0); err != nil || got != content {
		t.Errorf("full read = %q, %v", got, err)
	}

	// Permissions: dir 0700, file 0600.
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("spill dir mode = %v, want 0700", info.Mode().Perm())
	}
	info, err = os.Stat(filepath.Join(dir, id+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("spill file mode = %v, want 0600", info.Mode().Perm())
	}

	// Slices.
	if got, err := s.Read(id, 10, 20); err != nil || got != strings.Repeat("0123456789", 2) {
		t.Errorf("slice read = %q, %v", got, err)
	}
	if got, err := s.Read(id, 995, 100); err != nil || got != "56789" {
		t.Errorf("tail read = %q, %v", got, err)
	}

	// Offset past the end: clear message, not empty.
	got, err := s.Read(id, 2000, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "past the end") {
		t.Errorf("past-end message = %q", got)
	}

	// Bad ids rejected.
	for _, bad := range []string{"../x", "a/b", `a\b`, ""} {
		if _, err := s.Read(bad, 0, 10); err == nil {
			t.Errorf("id %q should be rejected", bad)
		}
	}

	// Sequence advances.
	id2, err := s.Save("second")
	if err != nil {
		t.Fatal(err)
	}
	if id2 != "2" {
		t.Errorf("second id = %q, want 2", id2)
	}

	// A fresh sink on the same dir resumes numbering (detached session).
	s2 := newSpillSink(dir)
	id3, err := s2.Save("third")
	if err != nil {
		t.Fatal(err)
	}
	if id3 != "3" {
		t.Errorf("resumed id = %q, want 3", id3)
	}
}

func TestSpillSinkMissingFile(t *testing.T) {
	s := newSpillSink(filepath.Join(t.TempDir(), "spill"))
	_, err := s.Read("99", 0, 10)
	if err == nil || !strings.Contains(err.Error(), "no spill 99") {
		t.Errorf("missing spill error = %v", err)
	}
}

func TestSessionSearcherOverStore(t *testing.T) {
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seed := func(title, msg string) {
		meta, err := store.Create("test-model", "/repo")
		if err != nil {
			t.Fatal(err)
		}
		meta.Title = title
		if err := store.SaveMeta(meta); err != nil {
			t.Fatal(err)
		}
		user := &provider.Message{Role: provider.RoleUser, Content: msg}
		if err := store.AppendMessage(meta.ID, user); err != nil {
			t.Fatal(err)
		}
	}
	seed("dropdown fix", "we replaced the native select with the Dropdown component")
	seed("ci setup", "the gate runs go test, golangci-lint, and prettier")

	s := sessionSearcher{store: store}
	hits, err := s.SearchSessions("dropdown", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	if hits[0].Title != "dropdown fix" || !strings.Contains(hits[0].Snippet, "Dropdown") {
		t.Errorf("hit = %+v", hits[0])
	}

	// Blank query returns nothing, not everything.
	blank, err := s.SearchSessions("   ", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(blank) != 0 {
		t.Errorf("blank query returned %d hits, want 0", len(blank))
	}
}
