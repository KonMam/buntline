// Package tools: job manager for long-running work.
//
// BashBackground, BashOutput, BashWait, and BashKill are one tool
// surface over a JobManager. The manager owns the id space, process
// groups, status, and output storage; the tools are a thin rendering
// layer over it. The model-facing surface is here, the process
// management is in background.go, and the seam is the JobManager
// interface so the server can hand the tools a session-scoped
// implementation without the tools package knowing about sessions.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/KonMam/buntline/internal/provider"
)

// JobStatus is one background job's lifecycle state. Terminal states
// never move again; the exit detail rides alongside (see Job).
const (
	JobRunning   = "running"
	JobCompleted = "completed"
	JobKilled    = "killed"
	JobFailed    = "failed"
)

// Job is one background job's snapshot: identity, status, the exit
// detail when settled, and the command that started it. It is a plain
// value type on purpose: the tools render it, the manager owns the
// actual process, and no internal handle leaks across the seam.
type Job struct {
	ID      string
	Command string
	Status  string // JobRunning, JobCompleted, JobKilled, or JobFailed
	Exit    string // "exit status 3" etc., empty while running
}

// JobManager is the background-job seam. The tools package must not
// know about sessions, so the server implements this against the
// session's job directory and hands it to the registry (the spill sink
// pattern, file-backed output included). One manager per session; the
// zero value is not usable.
//
// Output streams to a file the manager owns as the job runs, plus a
// small bounded read-back window kept by the manager; the file is the
// source of truth and read_spill can page the whole thing, so a long
// job's output never drops. Reads of that file go through the same
// offset/limit shape as read_spill.
type JobManager interface {
	// Start launches command in dir and returns the job id ("bash-3").
	// The command runs in its own process group; Kill and Close stop the
	// whole group. Output streams to the job's file as it runs.
	Start(dir, command string) (id string, err error)
	// Get returns one job's snapshot (error when the id is unknown).
	Get(id string) (Job, error)
	// List returns every job in registration order.
	List() []Job
	// Output returns a character slice of one job's streamed output.
	// Offset and limit follow read_spill's shape: offset past the end
	// yields a clear message, negative offsets start from the end.
	Output(id string, offset, limit int) (string, error)
	// Kill stops a running job's whole process group. Stopping an
	// already-settled job is a no-op.
	Kill(id string) error
	// Close stops every running job. Called through the registry's
	// Close hook when the session detaches.
	Close()
}

// JobManagerSetter lets the background tools accept the server-provided
// session job manager. The registry checks for the interface so it
// never needs to know the tools' concrete types.
type JobManagerSetter interface {
	SetJobManager(JobManager)
}

// jobTools is the shared field the background tools hold: their
// per-session JobManager, injected by the server (SetJobManager) or by
// the registry's constructor (tools.BashTools). Without one the tools
// report that background jobs are unavailable.
type jobTools struct {
	Manager JobManager
}

// SetJobManager hands the registry's background tools their per-session
// job manager. Called by the server when it builds a session; without
// one the tools run on the built-in manager (headless, tests).
func (r *Registry) SetJobManager(m JobManager) {
	for _, name := range []string{"bash_background", "bash_output", "bash_wait", "bash_kill"} {
		if t, ok := r.tools[name]; ok {
			if js, ok := t.(JobManagerSetter); ok {
				js.SetJobManager(m)
			}
		}
	}
}

func (t *jobTools) SetJobManager(m JobManager) { t.Manager = m }

// BashBackground starts a long-running command without blocking the
// turn. The command runs detached in its own process group; its output
// streams to a file under the session's job directory, so nothing is
// lost and read_spill can page the whole thing. Use bash_output to
// read what it has printed so far, bash_wait to poll for completion,
// and bash_kill to stop it. Tasks end when the session detaches or the
// harness exits.
type BashBackground struct {
	jobTools
	Dir string
}

// Close implements the registry's teardown hook: every job the manager
// owns dies with its session.
func (t *BashBackground) Close() {
	if t.Manager != nil {
		t.Manager.Close()
	}
}

func (t *BashBackground) Safe() bool { return false }

func (t *BashBackground) Def() provider.ToolDef {
	return provider.ToolDef{
		Name:        "bash_background",
		Description: "Start a long-running shell command (dev server, watcher) in the background and return a task id. The command runs detached in its own process group; its output streams to a file in the session's job directory. Use bash_output to read what it has printed, bash_wait to poll for completion, and bash_kill to stop it. The task ends when the session detaches or the harness exits.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "The shell command to run in the background.",
				},
			},
			"required": []string{"command"},
		},
	}
}

func (t *BashBackground) Run(_ context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Command string `json:"command"`
	}
	if err := decode(args, &in); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(in.Command) == "" {
		return Result{}, fmt.Errorf("command is required")
	}
	if t.Manager == nil {
		return Result{}, fmt.Errorf("background jobs are not available in this session")
	}
	id, err := t.Manager.Start(t.Dir, in.Command)
	if err != nil {
		return Result{}, err
	}
	// A brief grace period catches commands that die immediately (typo,
	// port in use) so the model hears about it in the same round.
	time.Sleep(300 * time.Millisecond)
	job, err := t.Manager.Get(id)
	if err != nil {
		return Result{}, err
	}
	status := "running"
	if job.Status != JobRunning {
		status = "already exited: " + job.Exit
	}
	out, err := t.Manager.Output(id, 0, 4000)
	if err != nil {
		return Result{}, err
	}
	msg := fmt.Sprintf("started %s (%s)", id, status)
	if out != "" {
		msg += "\ninitial output:\n" + out
	}
	return Result{Content: msg}, nil
}

// BashOutput reads a background job's accumulated output. Without a
// task id it lists all background jobs.
type BashOutput struct {
	jobTools
}

func (t *BashOutput) Safe() bool { return true }

func (t *BashOutput) Def() provider.ToolDef {
	return provider.ToolDef{
		Name:        "bash_output",
		Description: "Read the accumulated output and status of a background job started with bash_background. The output is read back from the job's file, so nothing is dropped; add an offset to page through it. Without a task id, list all background jobs.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{
					"type":        "string",
					"description": "The task id returned by bash_background. Omit to list tasks.",
				},
				"offset": map[string]any{
					"type":        "integer",
					"description": "Character offset to start reading from (default 0, the start). Use a negative offset to start from the end of the output.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum characters to read (default 20000).",
				},
			},
		},
	}
}

func (t *BashOutput) Run(_ context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		TaskID string `json:"task_id"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := decode(args, &in); err != nil {
		return Result{}, err
	}
	if t.Manager == nil {
		return Result{Content: "background jobs are not available in this session"}, nil
	}
	if in.TaskID == "" {
		jobs := t.Manager.List()
		if len(jobs) == 0 {
			return Result{Content: "no background jobs"}, nil
		}
		var sb strings.Builder
		for _, j := range jobs {
			status := j.Status
			if status != JobRunning {
				status = j.Exit
			}
			fmt.Fprintf(&sb, "%s  %s  %s\n", j.ID, status, j.Command)
		}
		return Result{Content: strings.TrimSpace(sb.String())}, nil
	}
	job, err := t.Manager.Get(in.TaskID)
	if err != nil {
		return Result{Content: fmt.Sprintf("no background job %q", in.TaskID)}, nil
	}
	status := job.Status
	if status != JobRunning {
		status = job.Exit
	}
	out, err := t.Manager.Output(in.TaskID, in.Offset, in.Limit)
	if err != nil {
		return Result{}, err
	}
	if out == "" {
		out = "(no output yet)"
	}
	return Result{Content: fmt.Sprintf("%s (%s)\n%s", job.ID, status, out)}, nil
}

// BashWait polls a background job until it settles or the wait budget
// runs out. Returns the job's status and the tail of its output so the
// model learns the outcome in one call instead of blind-polling
// bash_output.
type BashWait struct {
	jobTools
}

func (t *BashWait) Safe() bool { return true }

func (t *BashWait) Def() provider.ToolDef {
	return provider.ToolDef{
		Name:        "bash_wait",
		Description: "Wait up to timeout_seconds for a background job started with bash_background to finish, then return its status and the tail of its output. Use it when the next step depends on the job's result (a test run, a build).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{
					"type":        "string",
					"description": "The task id returned by bash_background.",
				},
				"timeout_seconds": map[string]any{
					"type":        "integer",
					"description": "How long to wait for the job to finish (default 30, max 300).",
				},
			},
			"required": []string{"task_id"},
		},
	}
}

func (t *BashWait) Run(_ context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		TaskID         string `json:"task_id"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := decode(args, &in); err != nil {
		return Result{}, err
	}
	if t.Manager == nil {
		return Result{Content: "background jobs are not available in this session"}, nil
	}
	wait := 30 * time.Second
	if in.TimeoutSeconds > 0 {
		wait = min(time.Duration(in.TimeoutSeconds)*time.Second, 300*time.Second)
	}
	deadline := time.Now().Add(wait)
	for {
		job, err := t.Manager.Get(in.TaskID)
		if err != nil {
			return Result{Content: fmt.Sprintf("no background job %q", in.TaskID)}, nil
		}
		if job.Status != JobRunning {
			out, err := t.Manager.Output(in.TaskID, -6000, 6000)
			if err != nil {
				return Result{}, err
			}
			status := job.Status
			if status == JobCompleted {
				status = "finished: " + job.Exit
			} else {
				status = job.Exit
			}
			return Result{Content: fmt.Sprintf("%s %s\n%s", job.ID, status, out)}, nil
		}
		if time.Now().After(deadline) {
			return Result{Content: fmt.Sprintf("%s still running after %s; use bash_output or bash_wait again", job.ID, wait)}, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// BashKill stops a background job's whole process group.
type BashKill struct {
	jobTools
}

func (t *BashKill) Safe() bool { return true }

func (t *BashKill) Def() provider.ToolDef {
	return provider.ToolDef{
		Name:        "bash_kill",
		Description: "Stop a background job started with bash_background. The whole process group is killed, so children die with the command.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{
					"type":        "string",
					"description": "The task id to stop.",
				},
			},
			"required": []string{"task_id"},
		},
	}
}

func (t *BashKill) Run(_ context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		TaskID string `json:"task_id"`
	}
	if err := decode(args, &in); err != nil {
		return Result{}, err
	}
	if t.Manager == nil {
		return Result{Content: "background jobs are not available in this session"}, nil
	}
	if err := t.Manager.Kill(in.TaskID); err != nil {
		return Result{Content: err.Error()}, nil
	}
	return Result{Content: "stopped " + in.TaskID}, nil
}
