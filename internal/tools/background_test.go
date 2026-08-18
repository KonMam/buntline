package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestJobManager returns a job manager plus the tools wired to it,
// sharing one manager (the way BashTools assembles them).
func newTestJobManager(dir string) (JobManager, *BashBackground, *BashOutput, *BashWait, *BashKill) {
	m := newJobManager()
	bg := &BashBackground{Dir: dir, jobTools: jobTools{Manager: m}}
	out := &BashOutput{jobTools: jobTools{Manager: m}}
	wait := &BashWait{jobTools: jobTools{Manager: m}}
	kill := &BashKill{jobTools: jobTools{Manager: m}}
	return m, bg, out, wait, kill
}

func TestBackgroundJobLifecycle(t *testing.T) {
	dir := t.TempDir()
	m, bg, out, _, kill := newTestJobManager(dir)

	res, err := bg.Run(context.Background(), json.RawMessage(`{"command":"echo started; sleep 30"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "bash-1") || !strings.Contains(res.Content, "running") {
		t.Fatalf("start result: %q", res.Content)
	}
	if !strings.Contains(res.Content, "started") {
		t.Errorf("initial output missing: %q", res.Content)
	}

	res, err = out.Run(context.Background(), json.RawMessage(`{"task_id":"bash-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "running") {
		t.Errorf("output should report running: %q", res.Content)
	}

	res, err = out.Run(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "bash-1") {
		t.Errorf("list should include the task: %q", res.Content)
	}

	if _, err = kill.Run(context.Background(), json.RawMessage(`{"task_id":"bash-1"}`)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		job, err := m.Get("bash-1")
		if err != nil {
			t.Fatal(err)
		}
		if job.Status != JobRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("task did not die after kill")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestBackgroundImmediateExitReported(t *testing.T) {
	_, bg, _, _, _ := newTestJobManager(t.TempDir())
	res, err := bg.Run(context.Background(), json.RawMessage(`{"command":"exit 3"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "already exited") {
		t.Errorf("immediate failure should be reported at start: %q", res.Content)
	}
}

func TestBackgroundOutputFileBacked(t *testing.T) {
	dir := t.TempDir()
	jobDir := filepath.Join(dir, "jobs")
	m := NewSessionJobManager(jobDir)
	bg := &BashBackground{Dir: dir, jobTools: jobTools{Manager: m}}
	out := &BashOutput{jobTools: jobTools{Manager: m}}

	if _, err := bg.Run(context.Background(), json.RawMessage(`{"command":"printf 'line1\\nline2\\nline3\\n'"}`)); err != nil {
		t.Fatal(err)
	}
	// The short command finishes within the grace period; wait for it.
	deadline := time.Now().Add(5 * time.Second)
	for {
		job, err := m.Get("bash-1")
		if err != nil {
			t.Fatal(err)
		}
		if job.Status != JobRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}

	res, err := out.Run(context.Background(), json.RawMessage(`{"task_id":"bash-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "line1") || !strings.Contains(res.Content, "line3") {
		t.Errorf("file-backed output missing lines: %q", res.Content)
	}

	// The output file exists under the session job directory.
	if _, err := os.Stat(filepath.Join(jobDir, "bash-1.out")); err != nil {
		t.Errorf("job output file missing: %v", err)
	}

	// Paging: offset into the middle (line2 starts at index 6).
	res, err = out.Run(context.Background(), json.RawMessage(`{"task_id":"bash-1","offset":6,"limit":5}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "line2") {
		t.Errorf("offset read should hit line2: %q", res.Content)
	}

	// Negative offset reads the tail.
	res, err = out.Run(context.Background(), json.RawMessage(`{"task_id":"bash-1","offset":-6,"limit":6}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "line3") {
		t.Errorf("tail read should hit line3: %q", res.Content)
	}
}

// TestBackgroundOutputNotDroppedBeyondWindow proves the file-backed
// claim: output past the old in-memory cap is still fully readable from
// the file, unlike the previous buffered-only implementation that
// dropped the oldest half.
func TestBackgroundOutputNotDroppedBeyondWindow(t *testing.T) {
	dir := t.TempDir()
	jobDir := filepath.Join(dir, "jobs")
	m := NewSessionJobManager(jobDir)
	// Write more than the 64KiB read-back cap, with a marker at the very
	// start that the old buffer would have dropped.
	if _, err := m.Start(dir, "printf 'HEAD-MARKER\\n'; head -c 200000 /dev/zero | tr '\\0' 'x'; printf '\\nTAIL-MARKER\\n'"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		job, err := m.Get("bash-1")
		if err != nil {
			t.Fatal(err)
		}
		if job.Status != JobRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
	out, err := m.Output("bash-1", 0, 20000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "HEAD-MARKER") {
		t.Errorf("file-backed output dropped the head: %q", out[:min(200, len(out))])
	}
	tail, err := m.Output("bash-1", -200, 200)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tail, "TAIL-MARKER") {
		t.Errorf("file-backed output missing the tail: %q", tail)
	}
}

func TestBackgroundWait(t *testing.T) {
	_, bg, _, wait, _ := newTestJobManager(t.TempDir())
	if _, err := bg.Run(context.Background(), json.RawMessage(`{"command":"sleep 0.5; echo done"}`)); err != nil {
		t.Fatal(err)
	}
	res, err := wait.Run(context.Background(), json.RawMessage(`{"task_id":"bash-1","timeout_seconds":5}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "finished") || !strings.Contains(res.Content, "done") {
		t.Errorf("wait should report completion and tail: %q", res.Content)
	}
}

func TestBackgroundWaitTimeout(t *testing.T) {
	_, bg, _, wait, _ := newTestJobManager(t.TempDir())
	if _, err := bg.Run(context.Background(), json.RawMessage(`{"command":"sleep 30"}`)); err != nil {
		t.Fatal(err)
	}
	// A very short wait budget must report still-running, not block.
	start := time.Now()
	res, err := wait.Run(context.Background(), json.RawMessage(`{"task_id":"bash-1","timeout_seconds":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "still running") {
		t.Errorf("wait timeout should report still running: %q", res.Content)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("wait timeout took too long: %s", elapsed)
	}
}

func TestBackgroundListEmpty(t *testing.T) {
	_, _, out, _, _ := newTestJobManager(t.TempDir())
	res, err := out.Run(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "no background jobs") {
		t.Errorf("empty list message: %q", res.Content)
	}
}
