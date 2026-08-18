package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/KonMam/tether/internal/session"
	"github.com/KonMam/tether/internal/tools"
)

// sessionSearcher adapts the session store's Search to the tools package's
// narrow SessionSearcher interface, so the tools package never imports
// the store.
type sessionSearcher struct {
	store *session.Store
}

func (s sessionSearcher) SearchSessions(query string, limit int) ([]tools.SessionHit, error) {
	hits, err := s.store.Search(query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]tools.SessionHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, tools.SessionHit{
			SessionID: h.SessionID,
			Title:     h.Title,
			Workdir:   h.Workdir,
			Snippet:   h.Snippet,
		})
	}
	return out, nil
}

// spillSink stores oversized tool output inside a session's directory
// (<sessions-dir>/<session-id>/spill/<seq>.txt, dir 0700, files 0600),
// outside the workdir on purpose: the file tools must not reach it. The
// session directory's deletion covers the spill files.
type spillSink struct {
	dir string
	mu  sync.Mutex
	seq int
}

func newSpillSink(dir string) *spillSink {
	return &spillSink{dir: dir}
}

// Save writes text to the next spill file. The sequence number is
// persisted as the next file's name, so a detached session resumes
// numbering instead of overwriting.
func (s *spillSink) Save(text string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return "", err
	}
	if s.seq == 0 {
		entries, err := os.ReadDir(s.dir)
		if err != nil {
			return "", err
		}
		for _, e := range entries {
			if n, err := strconv.Atoi(strings.TrimSuffix(e.Name(), ".txt")); err == nil && n >= s.seq {
				s.seq = n + 1
			}
		}
		if s.seq == 0 {
			s.seq = 1
		}
	}
	id := strconv.Itoa(s.seq)
	if err := os.WriteFile(filepath.Join(s.dir, id+".txt"), []byte(text), 0o600); err != nil {
		return "", err
	}
	s.seq++
	return id, nil
}

// Read returns a character slice of one spill file. Path separators in
// the id are rejected, offset past the end yields a clear message, and
// negative offsets start from the end of the file.
func (s *spillSink) Read(id string, offset, limit int) (string, error) {
	if id == "" || strings.ContainsAny(id, "/\\") {
		return "", fmt.Errorf("invalid spill id %q", id)
	}
	if limit <= 0 {
		limit = 20000
	}
	data, err := os.ReadFile(filepath.Join(s.dir, id+".txt"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no spill %s", id)
		}
		return "", err
	}
	text := string(data)
	if offset < 0 {
		offset = len(text) + offset
		if offset < 0 {
			offset = 0
		}
	}
	if offset > len(text) {
		return fmt.Sprintf("offset %d is past the end of spill %s (%d characters)", offset, id, len(text)), nil
	}
	end := offset + limit
	if end > len(text) {
		end = len(text)
	}
	return text[offset:end], nil
}
