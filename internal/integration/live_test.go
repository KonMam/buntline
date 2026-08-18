// Package integration holds tests that exercise tether against live
// model backends. They are opt-in: set TETHER_IT=1 (see `make
// integration`). Each test also checks for the backend it needs and
// skips with a reason when it is absent, so the suite degrades to
// whatever is reachable from this machine.
package integration

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KonMam/tether/internal/agent"
	"github.com/KonMam/tether/internal/config"
	"github.com/KonMam/tether/internal/provider"
	"github.com/KonMam/tether/internal/tools"
)

func gate(t *testing.T) {
	t.Helper()
	if os.Getenv("TETHER_IT") == "" {
		t.Skip("set TETHER_IT=1 to run live integration tests")
	}
}

type allowAll struct{}

func (allowAll) RequestApproval(context.Context, agent.ApprovalRequest) (agent.Decision, error) {
	return agent.DecisionAllow, nil
}

// probeToolPickup runs the canonical grounding probe against a live
// backend: a fact exists only in a file, so a correct answer proves the
// model discovered and called read_file rather than guessing.
func probeToolPickup(t *testing.T, baseURL, apiKey, model string) {
	t.Helper()
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	const code = "7431"
	if err := os.WriteFile(filepath.Join(dir, "fact.txt"),
		[]byte("The deploy code is "+code+".\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var toolCalled bool
	var final string
	a := agent.New(agent.Config{
		Provider:     provider.NewOpenAICompat(baseURL, apiKey),
		Model:        model,
		Tools:        tools.Default(dir),
		Approver:     allowAll{},
		SystemPrompt: config.SystemPrompt(dir),
		Emit: func(ev agent.Event) {
			switch ev.Type {
			case agent.EventToolStart:
				t.Logf("tool call: %s %s", ev.ToolName, ev.ToolArgs)
				if ev.ToolName == "read_file" || ev.ToolName == "grep" || ev.ToolName == "bash" {
					toolCalled = true
				}
			case agent.EventMessage:
				if ev.Message != nil && ev.Message.Role == provider.RoleAssistant && ev.Message.Content != "" {
					final = ev.Message.Content
				}
			case agent.EventError:
				t.Logf("error event: %s", ev.Error)
			}
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := a.Run(ctx, "Read fact.txt in the working directory and tell me the deploy code."); err != nil {
		t.Fatalf("turn failed: %v", err)
	}
	if !toolCalled {
		t.Error("the model answered without calling any file tool")
	}
	if !strings.Contains(final, code) {
		t.Errorf("final answer does not contain the code from the file:\n%s", final)
	}
}

func TestDeepSeekToolPickup(t *testing.T) {
	gate(t)
	key := os.Getenv("DEEPSEEK_API_KEY")
	if key == "" {
		t.Skip("DEEPSEEK_API_KEY is not set")
	}
	probeToolPickup(t, "https://api.deepseek.com/v1", key, "deepseek-chat")
}

// probeBackgroundJob drives the background job surface end to end: the
// model must start a job with bash_background, wait for it with
// bash_wait, and read its output. This exercises the file-backed job
// manager exactly as a real session would.
func probeBackgroundJob(t *testing.T, baseURL, apiKey, model string) {
	t.Helper()
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	// The job writes a marker file after a short delay; the model must
	// notice the background job finished and read the marker.
	const marker = "bg-done-9182"
	if err := os.WriteFile(filepath.Join(dir, "task.sh"),
		[]byte("sleep 1; echo "+marker+" > result.txt\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var bgStarted, bgWaited bool
	var final string
	a := agent.New(agent.Config{
		Provider:     provider.NewOpenAICompat(baseURL, apiKey),
		Model:        model,
		Tools:        tools.Default(dir),
		Approver:     allowAll{},
		SystemPrompt: config.SystemPrompt(dir),
		Emit: func(ev agent.Event) {
			switch ev.Type {
			case agent.EventToolStart:
				t.Logf("tool call: %s %s", ev.ToolName, ev.ToolArgs)
				if ev.ToolName == "bash_background" {
					bgStarted = true
				}
				if ev.ToolName == "bash_wait" || ev.ToolName == "bash_output" {
					bgWaited = true
				}
			case agent.EventMessage:
				if ev.Message != nil && ev.Message.Role == provider.RoleAssistant && ev.Message.Content != "" {
					final = ev.Message.Content
				}
			case agent.EventError:
				t.Logf("error event: %s", ev.Error)
			}
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	prompt := "In the working directory, run task.sh in the background with bash_background. " +
		"Then wait for it to finish and report what it wrote to result.txt."
	if err := a.Run(ctx, prompt); err != nil {
		t.Fatalf("turn failed: %v", err)
	}
	if !bgStarted {
		t.Error("the model never called bash_background")
	}
	if !bgWaited {
		t.Error("the model never polled the background job with bash_wait/bash_output")
	}
	if !strings.Contains(final, marker) {
		t.Errorf("final answer does not mention the job's result:\n%s", final)
	}
}

func TestDeepSeekBackgroundJob(t *testing.T) {
	gate(t)
	key := os.Getenv("DEEPSEEK_API_KEY")
	if key == "" {
		t.Skip("DEEPSEEK_API_KEY is not set")
	}
	probeBackgroundJob(t, "https://api.deepseek.com/v1", key, "deepseek-chat")
}

func TestOllamaToolPickup(t *testing.T) {
	gate(t)
	base := os.Getenv("TETHER_IT_OLLAMA")
	if base == "" {
		base = "http://localhost:11434"
	}
	client := &http.Client{Timeout: 2 * time.Second}
	if resp, err := client.Get(base + "/api/version"); err != nil {
		t.Skipf("ollama is not reachable at %s", base)
	} else {
		_ = resp.Body.Close()
	}
	model := os.Getenv("TETHER_IT_MODEL")
	if model == "" {
		model = "qwen3.5:9b"
	}
	probeToolPickup(t, base+"/v1", "", model)
}
