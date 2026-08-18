package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/KonMam/tether/internal/agent"
)

// SubagentStatus is the lifecycle state of one spawned child. Running
// entries carry a live handle; terminal entries (done, failed,
// interrupted) carry the child's final report.
type SubagentStatus string

const (
	SubagentRunning     SubagentStatus = "running"
	SubagentDone        SubagentStatus = "done"
	SubagentFailed      SubagentStatus = "failed"
	SubagentInterrupted SubagentStatus = "interrupted"
)

// subagentEntry is one row of a session's subagent registry. The id is
// the spawn_agent tool call's id (the same id every child event carries
// as ParentID) so the UI can join registry rows to event streams.
type subagentEntry struct {
	id        string
	name      string
	task      string
	startedAt time.Time
	agent     *agent.Agent
	cancel    context.CancelFunc

	mu      sync.Mutex
	status  SubagentStatus
	endedAt time.Time
	report  string
}

func (e *subagentEntry) snapshot() (status SubagentStatus, endedAt time.Time, report string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.status, e.endedAt, e.report
}

// finish marks the entry terminal exactly once, with its final report.
func (e *subagentEntry) finish(status SubagentStatus, report string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.status != SubagentRunning {
		return
	}
	e.status = status
	e.endedAt = time.Now()
	e.report = report
}

// subagentRegistry is one liveSession's record of its spawned children.
// Finished entries stay for the life of the liveSession, capped at the
// most recent subagentCap.
type subagentRegistry struct {
	mu      sync.Mutex
	entries []*subagentEntry
}

const subagentCap = 20

func newSubagentRegistry() *subagentRegistry {
	return &subagentRegistry{}
}

func (r *subagentRegistry) add(e *subagentEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, e)
	if len(r.entries) > subagentCap {
		r.entries = r.entries[len(r.entries)-subagentCap:]
	}
}

func (r *subagentRegistry) get(id string) *subagentEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.entries {
		if e.id == id {
			return e
		}
	}
	return nil
}

// list returns the entries, newest first, with a stable order for equal
// start times (a batch of children spawned together).
func (r *subagentRegistry) list() []*subagentEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*subagentEntry, len(r.entries))
	copy(out, r.entries)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// handleSubagents lists the session's spawned children: id, name, task
// (first 200 characters), status, started_at, ended_at, and the final
// report for terminal entries.
func (s *Server) handleSubagents(w http.ResponseWriter, r *http.Request) {
	ls, err := s.resolve(r.PathValue("id"))
	if err != nil {
		httpError(w, http.StatusNotFound, err)
		return
	}
	type subagentInfo struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Task      string `json:"task"`
		Status    string `json:"status"`
		StartedAt string `json:"started_at"`
		EndedAt   string `json:"ended_at,omitempty"`
		Report    string `json:"report,omitempty"`
	}
	out := []subagentInfo{}
	for _, e := range ls.subagents.list() {
		status, endedAt, report := e.snapshot()
		info := subagentInfo{
			ID:        e.id,
			Name:      e.name,
			Task:      e.task,
			Status:    string(status),
			StartedAt: e.startedAt.Format(time.RFC3339),
		}
		if status != SubagentRunning {
			info.EndedAt = endedAt.Format(time.RFC3339)
			info.Report = report
		}
		out = append(out, info)
	}
	if out == nil {
		out = []subagentInfo{}
	}
	writeJSON(w, out)
}

// handleSubagentSteer delivers a message to a running child mid-turn,
// like steering the main loop. Unknown or terminal ids are 404.
func (s *Server) handleSubagentSteer(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Content) == "" {
		httpError(w, http.StatusBadRequest, fmt.Errorf("content is required"))
		return
	}
	e := s.liveSubagent(r.PathValue("id"), r.PathValue("sid"))
	if e == nil {
		httpError(w, http.StatusNotFound, fmt.Errorf("no running subagent %s", r.PathValue("sid")))
		return
	}
	if err := e.agent.Steer(in.Content); err != nil {
		httpError(w, http.StatusConflict, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// handleSubagentInterrupt cancels a running child's context; the child
// loop stops and the spawn tool reports the interruption.
func (s *Server) handleSubagentInterrupt(w http.ResponseWriter, r *http.Request) {
	e := s.liveSubagent(r.PathValue("id"), r.PathValue("sid"))
	if e == nil {
		httpError(w, http.StatusNotFound, fmt.Errorf("no running subagent %s", r.PathValue("sid")))
		return
	}
	e.cancel()
	w.WriteHeader(http.StatusNoContent)
}

// liveSubagent finds a running child by id. Resolves the session (so a
// 404 is honest for an unknown session) and refuses terminal entries.
func (s *Server) liveSubagent(sessionID, subagentID string) *subagentEntry {
	ls, err := s.resolve(sessionID)
	if err != nil {
		return nil
	}
	e := ls.subagents.get(subagentID)
	if e == nil {
		return nil
	}
	status, _, _ := e.snapshot()
	if status != SubagentRunning {
		return nil
	}
	return e
}
