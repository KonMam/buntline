package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/KonMam/tether/internal/provider"
)

// Bash runs a shell command in the working directory.
//
// Process management is the part that bites: `bash -c` spawns children, and
// killing only the direct child leaves grandchildren holding the output pipe
// (Wait hangs) or running unsupervised. So the command gets its own process
// group, cancellation kills the whole group, and WaitDelay bounds Wait even
// if something survives with the pipe open.
type Bash struct {
	Dir string
}

const (
	bashDefaultTimeout = 60 * time.Second
	bashMaxTimeout     = 300 * time.Second
)

func (t *Bash) Safe() bool { return false }

// LongRunning marks bash as a tool that may run far past the model
// round that requested it: tests, builds, and installs are the core of
// the job and can take minutes. The harness backgrounds it after a
// grace period rather than blocking the turn.
func (t *Bash) LongRunning() {}

// NeverBackground declines the background path for commands that start
// with sleep: a sleep that outlives the grace period is never useful
// (only a placeholder plus a stray process), so it runs inline and dies
// at its own timeout instead (the same rule Claude Code applies).
func (t *Bash) NeverBackground(args json.RawMessage) bool {
	var in struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(in.Command), "sleep")
}

func (t *Bash) Def() provider.ToolDef {
	return provider.ToolDef{
		Name:        "bash",
		Description: "Run a shell command in the working directory and return its combined output. Commands time out (default 60s, max 300s).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "The shell command to run.",
				},
				"timeout_seconds": map[string]any{
					"type":        "integer",
					"description": "Optional timeout in seconds (max 300).",
				},
			},
			"required": []string{"command"},
		},
	}
}

func (t *Bash) Run(ctx context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Command        string `json:"command"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := decode(args, &in); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(in.Command) == "" {
		return Result{}, fmt.Errorf("command is required")
	}

	timeout := bashDefaultTimeout
	if in.TimeoutSeconds > 0 {
		timeout = min(time.Duration(in.TimeoutSeconds)*time.Second, bashMaxTimeout)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", in.Command)
	cmd.Dir = t.Dir
	cmd.Env = scrubbedEnv()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Negative pid = the whole process group, grandchildren included.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	err := cmd.Run()
	result := stripANSI(out.String())

	if ctx.Err() == context.DeadlineExceeded {
		return Result{}, fmt.Errorf("command timed out after %s; output so far:\n%s", timeout, result)
	}
	if err != nil {
		// Non-zero exit is information for the model, not a harness error.
		if exitErr, ok := err.(*exec.ExitError); ok {
			return Result{Content: fmt.Sprintf("exit status %d\n%s", exitErr.ExitCode(), result)}, nil
		}
		return Result{}, err
	}
	if result == "" {
		return Result{Content: "(no output)"}, nil
	}
	return Result{Content: result}, nil
}
