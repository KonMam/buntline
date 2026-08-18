package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KonMam/tether/internal/module"
	"github.com/KonMam/tether/internal/module/memory"
	"github.com/KonMam/tether/internal/session"
)

// TestMemoryIndexInjectedAtAttach proves a fresh session with a memory
// index loads it as an instructions-kind first message after AGENTS.md.
func TestMemoryIndexInjectedAtAttach(t *testing.T) {
	workdir := t.TempDir()
	// Write a memory index into the workdir's .tether/memory.
	memDir := filepath.Join(workdir, ".tether", "memory")
	if err := os.MkdirAll(memDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte("the API tests need a local Redis\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg, err := module.NewRegistry(filepath.Join(t.TempDir(), "modules.json"), &memory.Module{})
	if err != nil {
		t.Fatal(err)
	}
	s := New(emptyConfig(), store, nil, reg, nil)
	t.Cleanup(s.Shutdown)

	meta, err := store.Create("test-model", workdir)
	if err != nil {
		t.Fatal(err)
	}
	ls, err := s.resolve(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	msgs := ls.agent.Messages()
	if len(msgs) == 0 {
		t.Fatal("no messages seeded at attach")
	}
	var found bool
	for _, m := range msgs {
		if m.Kind == "instructions" && strings.Contains(m.Content, "local Redis") {
			found = true
		}
	}
	if !found {
		var kinds []string
		for _, m := range msgs {
			kinds = append(kinds, m.Kind)
		}
		t.Fatalf("memory index not injected; message kinds = %v", kinds)
	}
}

// TestMemoryIndexNotInjectedWhenModuleDisabled proves a disabled memory
// module leaves the transcript clean.
func TestMemoryIndexNotInjectedWhenModuleDisabled(t *testing.T) {
	workdir := t.TempDir()
	memDir := filepath.Join(workdir, ".tether", "memory")
	if err := os.MkdirAll(memDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte("remember this"), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg, err := module.NewRegistry(filepath.Join(t.TempDir(), "modules.json"), &memory.Module{})
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.SetEnabled("memory", false); err != nil {
		t.Fatal(err)
	}
	s := New(emptyConfig(), store, nil, reg, nil)
	t.Cleanup(s.Shutdown)

	meta, err := store.Create("test-model", workdir)
	if err != nil {
		t.Fatal(err)
	}
	ls, err := s.resolve(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ls.agent.Messages() {
		if strings.Contains(m.Content, "remember this") {
			t.Fatal("memory index injected while module disabled")
		}
	}
}
