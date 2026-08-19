// Package memory is the cross-session forward state: the model writes
// facts it wants to remember (build commands, debugging insights,
// conventions it was corrected on) to a per-workdir memory directory,
// and a MEMORY.md index loads at the start of every session so the
// model starts knowing the repo's gotchas instead of rediscovering them.
//
// Memory is a file, not transcript state: it survives compaction by
// construction (compaction rewrites only transcript.jsonl), it is
// machine-local, and the index loads in the same collapsed first-user-
// message position as AGENTS.md, never the system prompt. The model
// decides what is worth remembering; nothing is saved without an
// explicit memory_write call, so every write is visible in the trace.
//
// Shape (Claude Code's auto memory): a MEMORY.md index (first
// MemoryIndexLines lines / MemoryIndexBytes bytes, whichever comes
// first, loaded every session) plus topic files read on demand with
// memory_read or the ordinary file tools.
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/KonMam/buntline/internal/module"
	"github.com/KonMam/buntline/internal/provider"
	"github.com/KonMam/buntline/internal/tools"
)

// MemoryIndexLines and MemoryIndexBytes cap the MEMORY.md index: the
// first of either is what loads every session. The model is told to
// keep one line per entry and move detail into topic files (Claude
// Code's discipline).
const (
	MemoryIndexLines = 200
	MemoryIndexBytes = 25 * 1024
)

// Module is the memory feature: two tools (memory_write, memory_read)
// and an attach-time index injection. It holds no resources beyond
// memory, so Stop is a no-op.
type Module struct{}

func (m *Module) Info() module.Info {
	return module.Info{
		ID:          "memory",
		Name:        "Memory",
		Description: "The model can remember facts across sessions with memory_write and memory_read; the index loads at session start.",
		Default:     true,
	}
}

// Tools returns the module's model-facing tools, built with the session
// workdir so they read and write the right memory directory.
func (m *Module) Tools(workdir string) []tools.Tool {
	return []tools.Tool{
		&MemoryWrite{Dir: memoryDir(workdir)},
		&MemoryRead{Dir: memoryDir(workdir)},
	}
}

// Routes: the UI reads memory through the module routes (the Memory
// panel): an overview of the index plus the topic files, and a single
// topic's content. The model reads memory through the tools.
func (m *Module) Routes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"GET /overview": m.handleOverview,
		"GET /topic":    m.handleTopic,
	}
}

func (m *Module) handleOverview(w http.ResponseWriter, r *http.Request) {
	// The route is session-less: the workdir comes from the session's
	// meta, which the server resolves. Read-only, like the tools; the
	// user edits memory by talking or by editing the file directly.
	workdir := r.URL.Query().Get("workdir")
	if workdir == "" {
		http.Error(w, `{"error":"workdir is required"}`, http.StatusBadRequest)
		return
	}
	dir := memoryDir(workdir)
	index, exists := LoadIndex(dir)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"index":  index,
		"exists": exists,
		"topics": listTopicsMeta(dir),
	})
}

func (m *Module) handleTopic(w http.ResponseWriter, r *http.Request) {
	workdir := r.URL.Query().Get("workdir")
	name := r.URL.Query().Get("name")
	if workdir == "" || name == "" {
		http.Error(w, `{"error":"workdir and name are required"}`, http.StatusBadRequest)
		return
	}
	// Topic names are plain .md file names, exactly as the tools confine
	// them: no separators, no traversal.
	if strings.ContainsAny(name, "/\\") || name == "." || name == ".." || !strings.HasSuffix(name, ".md") {
		http.Error(w, `{"error":"invalid topic name"}`, http.StatusBadRequest)
		return
	}
	data, err := os.ReadFile(filepath.Join(memoryDir(workdir), name))
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"name": name, "content": "", "exists": false})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"name":    name,
		"content": string(data),
		"exists":  true,
	})
}

// TopicMeta is one memory topic file in the overview: the UI shows the
// list so the user can audit what the model has stashed where.
type TopicMeta struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

// listTopicsMeta lists the memory directory's topic files (excluding
// MEMORY.md) with their sizes and modification times, sorted by name.
func listTopicsMeta(dir string) []TopicMeta {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []TopicMeta
	for _, e := range entries {
		if e.IsDir() || e.Name() == "MEMORY.md" || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, TopicMeta{Name: e.Name(), Size: info.Size(), Modified: info.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// memoryDir returns the per-workdir memory directory.
func memoryDir(workdir string) string {
	return filepath.Join(workdir, ".buntline", "memory")
}

// LoadIndexFor loads the memory index for a workdir (capped), for the
// server's attach-time injection.
func LoadIndexFor(workdir string) (string, bool) {
	return LoadIndex(memoryDir(workdir))
}

// ensureDir creates the memory directory (0700) if missing.
func ensureDir(dir string) error {
	return os.MkdirAll(dir, 0o700)
}

// LoadIndex returns the MEMORY.md index capped to the load budget, plus
// whether the file exists. The cap is applied on the *loaded* content
// (the first lines/bytes), matching Claude Code: content beyond the
// threshold is not loaded at session start.
func LoadIndex(dir string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(dir, "MEMORY.md"))
	if err != nil {
		return "", false
	}
	content := string(data)
	// Line cap first (cheap, and the model's discipline is line-based).
	lines := strings.Split(content, "\n")
	if len(lines) > MemoryIndexLines {
		content = strings.Join(lines[:MemoryIndexLines], "\n")
	}
	if len(content) > MemoryIndexBytes {
		content = content[:MemoryIndexBytes]
	}
	return content, true
}

// ListTopics returns the memory directory's topic files (excluding
// MEMORY.md), sorted.
func ListTopics(dir string) []string {
	metas := listTopicsMeta(dir)
	out := make([]string, 0, len(metas))
	for _, m := range metas {
		out = append(out, strings.TrimSuffix(m.Name, ".md"))
	}
	return out
}

// MemoryWrite appends or replaces facts in the memory directory. The
// model writes to MEMORY.md (the index) or a named topic file; writing
// a topic the index doesn't mention is allowed (the model keeps the
// index current). The write is session state on disk, visible in the
// trace like any other tool call, so it is safe and needs no approval.
type MemoryWrite struct {
	// Dir is the memory directory (wired by Tools(workdir)).
	Dir string
}

func (t *MemoryWrite) Safe() bool { return true }

func (t *MemoryWrite) Def() provider.ToolDef {
	return provider.ToolDef{
		Name: "memory_write",
		Description: "Write a fact to this repository's persistent memory so future sessions know it. " +
			"Use it for things you will need again: build commands, debugging insights, conventions you were corrected on, " +
			"decisions that matter. " +
			"Pass file=\"MEMORY.md\" to update the index (one line per entry, keep it under the load budget; move detail into topic files), " +
			"or file=\"<topic>.md\" to write a detailed topic file. " +
			"The index loads at the start of every session; topic files load on demand with memory_read.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file": map[string]any{
					"type":        "string",
					"description": "The memory file to write: \"MEMORY.md\" for the index, or a topic name like \"debugging.md\". Defaults to MEMORY.md.",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "The fact or note to write. For MEMORY.md keep it to one line per entry.",
				},
			},
			"required": []string{"content"},
		},
	}
}

func (t *MemoryWrite) Run(_ context.Context, args json.RawMessage) (tools.Result, error) {
	var in struct {
		File    string `json:"file"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return tools.Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	content := strings.TrimSpace(in.Content)
	if content == "" {
		return tools.Result{}, fmt.Errorf("content is required")
	}
	name := strings.TrimSpace(in.File)
	if name == "" {
		name = "MEMORY.md"
	}
	// Confine the target to the memory directory: a topic name is a
	// single .md file, no separators.
	if strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
		return tools.Result{}, fmt.Errorf("file must be a plain name like MEMORY.md or topic.md")
	}
	if !strings.HasSuffix(name, ".md") {
		name += ".md"
	}
	if err := ensureDir(t.Dir); err != nil {
		return tools.Result{}, fmt.Errorf("memory directory: %w", err)
	}
	path := filepath.Join(t.Dir, name)
	// For MEMORY.md, refuse writes that would exceed the load budget:
	// the model must rewrite the index instead (Claude Code's limit).
	if name == "MEMORY.md" {
		existing, _ := os.ReadFile(path)
		combined := strings.TrimSpace(string(existing)) + "\n" + content + "\n"
		lines := strings.Split(combined, "\n")
		if len(lines) > MemoryIndexLines || len(combined) > MemoryIndexBytes {
			return tools.Result{}, fmt.Errorf(
				"MEMORY.md would exceed the load budget (%d lines / %d bytes); "+
					"rewrite the index to stay under it, or move detail into a topic file", MemoryIndexLines, MemoryIndexBytes)
		}
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0o600); err != nil {
		return tools.Result{}, fmt.Errorf("write memory: %w", err)
	}
	if name == "MEMORY.md" {
		return tools.Result{Content: "Memory index updated."}, nil
	}
	return tools.Result{Content: "Memory topic " + name + " written."}, nil
}

// MemoryRead returns the memory index or a named topic file. Safe and
// read-only.
type MemoryRead struct {
	// Dir is the memory directory (wired by Tools(workdir)).
	Dir string
}

func (t *MemoryRead) Safe() bool { return true }

func (t *MemoryRead) Def() provider.ToolDef {
	return provider.ToolDef{
		Name: "memory_read",
		Description: "Read this repository's persistent memory: the MEMORY.md index (loaded at session start) or a named topic file. " +
			"Use it to recall what you learned or decided in earlier sessions. " +
			"With no file argument, returns the index; with file=\"<topic>.md\", returns that topic. " +
			"List available topics with file=\"list\".",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file": map[string]any{
					"type":        "string",
					"description": "The memory file to read: omit for the index, \"list\" for the topic list, or a topic name like \"debugging.md\".",
				},
			},
		},
	}
}

func (t *MemoryRead) Run(_ context.Context, args json.RawMessage) (tools.Result, error) {
	var in struct {
		File string `json:"file"`
	}
	_ = json.Unmarshal(args, &in)
	name := strings.TrimSpace(in.File)
	if name == "" || name == "MEMORY.md" {
		index, ok := LoadIndex(t.Dir)
		if !ok {
			return tools.Result{Content: "No memory yet. Use memory_write to save facts across sessions."}, nil
		}
		return tools.Result{Content: index}, nil
	}
	if name == "list" {
		topics := ListTopics(t.Dir)
		if len(topics) == 0 {
			return tools.Result{Content: "No memory topic files yet."}, nil
		}
		return tools.Result{Content: "Memory topics: " + strings.Join(topics, ", ")}, nil
	}
	if strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
		return tools.Result{}, fmt.Errorf("file must be a plain name like topic.md")
	}
	if !strings.HasSuffix(name, ".md") {
		name += ".md"
	}
	data, err := os.ReadFile(filepath.Join(t.Dir, name))
	if err != nil {
		return tools.Result{}, fmt.Errorf("no memory topic %q; use memory_read with no file for the index or file=\"list\" for topics", name)
	}
	return tools.Result{Content: strings.TrimSpace(string(data))}, nil
}
