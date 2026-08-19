package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KonMam/buntline/internal/module"
	"github.com/KonMam/buntline/internal/module/memory"
	"github.com/KonMam/buntline/internal/session"
)

// TestMemoryIndexInjectedAtAttach proves a fresh session with a memory
// index loads it as an instructions-kind first message after AGENTS.md.
func TestMemoryIndexInjectedAtAttach(t *testing.T) {
	workdir := t.TempDir()
	// Write a memory index into the workdir's .buntline/memory.
	memDir := filepath.Join(workdir, ".buntline", "memory")
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
		if m.Kind == "memory" && strings.Contains(m.Content, "local Redis") {
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
	memDir := filepath.Join(workdir, ".buntline", "memory")
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

// TestMemoryKindDistinctFromInstructions proves the memory index loads
// under its own kind, so the UI can label the block as memory and the
// model's "keep this current" instructions stay attached to it.
func TestMemoryKindDistinctFromInstructions(t *testing.T) {
	workdir := t.TempDir()
	memDir := filepath.Join(workdir, ".buntline", "memory")
	if err := os.MkdirAll(memDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte("redis requirement"), 0o600); err != nil {
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
	// The transcript holds exactly the seed: the memory block with its
	// kind, and nothing else (no AGENTS.md in this workdir).
	if len(msgs) != 1 {
		t.Fatalf("seeded %d messages, want 1", len(msgs))
	}
	if msgs[0].Kind != "memory" {
		t.Errorf("memory seed kind = %q, want \"memory\"", msgs[0].Kind)
	}
	if !strings.Contains(msgs[0].Content, "redis requirement") {
		t.Errorf("memory seed content = %q", msgs[0].Content)
	}
}

// TestMemoryReinjectedAfterCompact proves compaction does not lose the
// memory index: compaction rewrites the transcript, and the server
// re-seeds the file-backed messages (project instructions, memory)
// after the summary, so the next turn still carries them.
func TestMemoryReinjectedAfterCompact(t *testing.T) {
	workdir := t.TempDir()
	memDir := filepath.Join(workdir, ".buntline", "memory")
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
	// A live turn the compaction re-seed path can observe: set a
	// provider and run a compact through the agent, exactly what the
	// /compact endpoint does.
	if err := ls.agent.SetProvider(immediateProvider{}); err != nil {
		t.Fatal(err)
	}
	if err := ls.agent.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}

	// dispatch() is what re-seeds; the handler runs it through the
	// agent's event loop. The agent's transcript alone has no seeds, so
	// check the persisted transcript the dispatch path owns.
	msgs, err := store.Messages(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, m := range msgs {
		if m.Kind == "memory" && strings.Contains(m.Content, "local Redis") {
			found = true
		}
	}
	if !found {
		t.Fatal("memory index lost after compaction; not re-seeded")
	}
}
