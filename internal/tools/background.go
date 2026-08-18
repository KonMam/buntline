package tools

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
)

// jobManager is the JobManager implementation. It runs each job in its
// own process group (bash -c spawns children; killing only the direct
// child would leave grandchildren holding the output pipe or running
// unsupervised).
//
// Output goes to a file the manager owns when it was constructed with a
// directory (the session's job directory, the server's case): the file
// is the source of truth, bash_output reads it with offset/limit, so a
// long job's output is pageable and never dropped, unlike the old
// in-memory cap. Without a directory (headless mode, the built-in
// registry), output is buffered in memory with the oldest half dropped
// at the cap.
//
// Jobs die with the manager: Close (session detach) and the harness
// SIGINT/SIGTERM trap both stop every running process group.
const (
	backgroundMaxJobs = 8
	jobOutputCap      = 64 * 1024
)

type backgroundJob struct {
	id      string
	command string
	cmd     *exec.Cmd
	file    *os.File // the job's output file, nil when buffered only

	mu   sync.Mutex
	out  bytes.Buffer // used only when there is no output file
	done bool
	exit string // "exit status 0" etc., set when done
}

// Write appends to the in-memory buffer, dropping the oldest half when
// the cap is hit. Used only when the job has no output file; a
// file-backed job's Write is never called.
func (j *backgroundJob) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.out.Write(p)
	if j.out.Len() > jobOutputCap {
		trimmed := j.out.Bytes()[j.out.Len()-jobOutputCap/2:]
		var b bytes.Buffer
		b.WriteString("[earlier output dropped; background output is capped in this mode]\n")
		b.Write(trimmed)
		j.out = b
	}
	return len(p), nil
}

func (j *backgroundJob) snapshot() Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	st := JobRunning
	if j.done {
		switch {
		case j.exit == "exit status 0":
			st = JobCompleted
		case strings.Contains(j.exit, "signal: killed"):
			st = JobKilled
		default:
			st = JobFailed
		}
	}
	return Job{ID: j.id, Command: j.command, Status: st, Exit: j.exit}
}

// jobManager owns one session's background jobs and their output files.
type jobManager struct {
	mu    sync.Mutex
	dir   string // output directory; empty means output is buffered only
	tasks map[string]*backgroundJob
	next  int
}

// newJobManager returns a manager whose output is buffered in memory
// only (no session directory): the built-in registry and headless mode
// use this. The server's session-scoped manager writes output files
// under the session's job directory instead.
func newJobManager() *jobManager {
	return &jobManager{tasks: map[string]*backgroundJob{}}
}

// NewSessionJobManager returns a file-backed job manager for one
// session: output streams to files under dir (the session's job
// directory), so a long job's output is durable and pageable via
// bash_output's offset. The server calls this when it builds a session
// and hands the manager to the registry through SetJobManager.
func NewSessionJobManager(dir string) JobManager {
	return &jobManager{dir: dir, tasks: map[string]*backgroundJob{}}
}

// NewJobManager returns a buffered-only job manager for one session.
// Exported so the core module can assemble the bash tool set through
// the same module seam as every other tool contribution.
func NewJobManager() JobManager { return newJobManager() }

func (m *jobManager) Start(dir, command string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	running := 0
	for _, j := range m.tasks {
		if j.snapshot().Status == JobRunning {
			running++
		}
	}
	if running >= backgroundMaxJobs {
		return "", fmt.Errorf("%d background jobs are already running; kill one first", running)
	}

	m.next++
	job := &backgroundJob{id: fmt.Sprintf("bash-%d", m.next), command: command}

	cmd := exec.Command("bash", "-c", command)
	cmd.Dir = dir
	cmd.Env = scrubbedEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Combined output: stream to the job's file when the manager has an
	// output directory, otherwise into the capped in-memory buffer.
	var out interface{ Write([]byte) (int, error) } = job
	if m.dir != "" {
		if err := os.MkdirAll(m.dir, 0o700); err != nil {
			return "", err
		}
		f, err := os.OpenFile(filepath.Join(m.dir, job.id+".out"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return "", err
		}
		job.file = f
		out = f
	}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		if job.file != nil {
			_ = job.file.Close()
		}
		return "", err
	}
	job.cmd = cmd
	m.tasks[job.id] = job

	go func() {
		err := cmd.Wait()
		job.mu.Lock()
		job.done = true
		if err != nil {
			job.exit = err.Error()
		} else {
			job.exit = "exit status 0"
		}
		job.mu.Unlock()
	}()
	return job.id, nil
}

func (m *jobManager) Get(id string) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.tasks[id]
	if !ok {
		return Job{}, fmt.Errorf("no background job %q", id)
	}
	return j.snapshot(), nil
}

func (m *jobManager) List() []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Job, 0, len(m.tasks))
	for _, j := range m.tasks {
		out = append(out, j.snapshot())
	}
	sort.Slice(out, func(i, k int) bool { return out[i].ID < out[k].ID })
	return out
}

// Output returns a character slice of one job's streamed output. The
// file is the source of truth when the job has one; the in-memory
// buffer stands in for buffered-only jobs. Offset and limit follow
// read_spill's shape: offset past the end yields a clear message,
// negative offsets start from the end.
func (m *jobManager) Output(id string, offset, limit int) (string, error) {
	m.mu.Lock()
	j, ok := m.tasks[id]
	m.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("no background job %q", id)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	var text string
	if j.file != nil {
		data, err := os.ReadFile(j.file.Name())
		if err != nil {
			return "", err
		}
		text = string(data)
	} else {
		text = j.out.String()
	}
	if offset < 0 {
		offset = len(text) + offset
		if offset < 0 {
			offset = 0
		}
	}
	if offset > len(text) {
		return fmt.Sprintf("offset %d is past the end of job %s output (%d characters)", offset, id, len(text)), nil
	}
	if limit <= 0 {
		limit = 20000
	}
	end := offset + limit
	if end > len(text) {
		end = len(text)
	}
	return text[offset:end], nil
}

func (m *jobManager) Kill(id string) error {
	m.mu.Lock()
	j, ok := m.tasks[id]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("no background job %q", id)
	}
	if j.snapshot().Status != JobRunning {
		return nil
	}
	// Negative pid = the whole process group, like the foreground tool.
	return syscall.Kill(-j.cmd.Process.Pid, syscall.SIGKILL)
}

// Close stops every running job; used at session teardown so no process
// outlives its supervisor.
func (m *jobManager) Close() {
	for _, j := range m.List() {
		_ = m.Kill(j.ID)
	}
}
