// Package session persists conversations as plain files: one directory per
// session holding meta.json, transcript.jsonl (messages: the durable
// record, enough to resume), and events.jsonl (the activity log: every tool
// call, approval, and usage report). JSONL over SQLite: human-readable,
// grep-able, and the write volume of a chat session doesn't need a database.
//
// Text deltas are never persisted: the final message is the record;
// thousands of delta lines per reply would be noise.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/KonMam/buntline/internal/agent"
	"github.com/KonMam/buntline/internal/provider"
)

type Meta struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Model   string `json:"model"`
	Profile string `json:"profile,omitempty"`
	Workdir string `json:"workdir"`
	// Mode is the approval policy: "" or "ask" (approve each action),
	// "plan" (read-only), "auto_edit" (file edits pre-approved), "auto"
	// (everything pre-approved).
	Mode      string    `json:"mode,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Store struct {
	dir string
	mu  sync.Mutex
}

func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

// Dir returns the sessions root directory (for session-scoped storage
// like spill files that live beside the transcript).
func (s *Store) Dir() string { return s.dir }

func (s *Store) path(id string, file string) string {
	return filepath.Join(s.dir, id, file)
}

func (s *Store) Create(model, workdir string) (*Meta, error) {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	now := time.Now()
	m := &Meta{
		ID:        now.Format("20060102-150405") + "-" + hex.EncodeToString(b)[:6],
		Title:     "new session",
		Model:     model,
		Workdir:   workdir,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := os.MkdirAll(filepath.Join(s.dir, m.ID), 0o755); err != nil {
		return nil, err
	}
	if err := s.SaveMeta(m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Store) SaveMeta(m *Meta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	// Write-then-rename: a concurrent List() must never observe a
	// half-written meta.json (it would skip the session and the UI would
	// treat the store as empty).
	tmp := s.path(m.ID, "meta.json.tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(m.ID, "meta.json"))
}

func (s *Store) GetMeta(id string) (*Meta, error) {
	data, err := os.ReadFile(s.path(id, "meta.json"))
	if err != nil {
		return nil, fmt.Errorf("session %s: %w", id, err)
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *Store) List() ([]*Meta, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var metas []*Meta
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := s.GetMeta(e.Name())
		if err != nil {
			continue // skip corrupt/foreign directories
		}
		metas = append(metas, m)
	}
	sort.Slice(metas, func(i, j int) bool {
		return metas[i].UpdatedAt.After(metas[j].UpdatedAt)
	})
	return metas, nil
}

// Delete removes a session and everything it recorded.
func (s *Store) Delete(id string) error {
	if id == "" || strings.ContainsAny(id, "/\\") {
		return fmt.Errorf("invalid session id %q", id)
	}
	return os.RemoveAll(filepath.Join(s.dir, id))
}

// AppendMessage adds one message to the transcript log.
func (s *Store) AppendMessage(id string, m *provider.Message) error {
	return s.appendJSONL(s.path(id, "transcript.jsonl"), m)
}

// ReplaceTranscript rewrites the whole transcript (after /compact).
func (s *Store) ReplaceTranscript(id string, msgs []provider.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	for i := range msgs {
		if err := enc.Encode(&msgs[i]); err != nil {
			return err
		}
	}
	return os.WriteFile(s.path(id, "transcript.jsonl"), []byte(sb.String()), 0o644)
}

// Messages loads the transcript for resume.
func (s *Store) Messages(id string) ([]provider.Message, error) {
	data, err := os.ReadFile(s.path(id, "transcript.jsonl"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var msgs []provider.Message
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m provider.Message
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			return nil, fmt.Errorf("corrupt transcript line: %w", err)
		}
		msgs = append(msgs, m)
	}
	return msgs, nil
}

// AppendEvent adds one entry to the activity log.
func (s *Store) AppendEvent(id string, ev *agent.Event) error {
	return s.appendJSONL(s.path(id, "events.jsonl"), ev)
}

// Events returns up to limit most-recent activity-log entries.
func (s *Store) Events(id string, limit int) ([]agent.Event, error) {
	data, err := os.ReadFile(s.path(id, "events.jsonl"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if limit > 0 && len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	var evs []agent.Event
	for _, line := range lines {
		if line == "" {
			continue
		}
		var ev agent.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue // tolerate partial writes in the log
		}
		evs = append(evs, ev)
	}
	return evs, nil
}

func (s *Store) appendJSONL(path string, v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	// Close errors matter on writes: a failed flush is a lost log line.
	if err := json.NewEncoder(f).Encode(v); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// SearchHit is one transcript match: which session, and a snippet of
// the matching message.
type SearchHit struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	Workdir   string `json:"workdir"`
	Role      string `json:"role"`
	Snippet   string `json:"snippet"`
}

// Search scans every session's transcript for the query,
// case-insensitively, newest session first. One hit per session (the
// first matching message), capped at limit.
func (s *Store) Search(query string, limit int) ([]SearchHit, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil, nil
	}
	metas, err := s.List()
	if err != nil {
		return nil, err
	}
	var hits []SearchHit
	for _, m := range metas {
		if len(hits) >= limit {
			break
		}
		msgs, err := s.Messages(m.ID)
		if err != nil {
			continue
		}
		for _, msg := range msgs {
			if msg.Kind != "" || msg.Role == provider.RoleTool {
				continue
			}
			idx := strings.Index(strings.ToLower(msg.Content), q)
			if idx < 0 {
				continue
			}
			hits = append(hits, SearchHit{
				SessionID: m.ID,
				Title:     m.Title,
				Workdir:   m.Workdir,
				Role:      string(msg.Role),
				Snippet:   snippet(msg.Content, idx, len(q)),
			})
			break
		}
	}
	return hits, nil
}

// snippet returns the match with surrounding context, on one line.
func snippet(content string, idx, qlen int) string {
	const around = 44
	start := max(idx-around, 0)
	end := min(idx+qlen+around, len(content))
	out := content[start:end]
	out = strings.Join(strings.Fields(out), " ")
	if start > 0 {
		out = "…" + out
	}
	if end < len(content) {
		out += "…"
	}
	return out
}
