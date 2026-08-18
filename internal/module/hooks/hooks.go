// Package hooks runs project shell hooks around tool calls, configured in
// <workdir>/.tether/hooks.json:
//
//	{
//	  "pre_tool":  [{"match": "bash",      "command": "./scripts/guard.sh"}],
//	  "post_tool": [{"match": "edit_file", "command": "gofmt -l ."}]
//	}
//
// The hook receives {"tool": ..., "args": ...} as JSON on stdin. A pre_tool
// hook exiting with code 2 blocks the call; its output becomes the reason
// the model sees. post_tool output is appended to the tool result. "match"
// is a tool name or "*".
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/KonMam/tether/internal/agent"
	"github.com/KonMam/tether/internal/module"
	"github.com/KonMam/tether/internal/provider"
	"github.com/KonMam/tether/internal/tools"
)

type Module struct{}

func (m *Module) Info() module.Info {
	return module.Info{
		ID:          "hooks",
		Name:        "Hooks",
		Description: "Run project scripts before and after tool calls (.tether/hooks.json); a pre hook can block a call.",
		Default:     true,
	}
}

// Interceptor loads hook config for one session's workdir.
func (m *Module) Interceptor(_ string, workdir string) agent.ToolInterceptor {
	return &interceptor{workdir: workdir}
}

type hook struct {
	Match   string `json:"match"`
	Command string `json:"command"`
}

type hookConfig struct {
	PreTool  []hook `json:"pre_tool"`
	PostTool []hook `json:"post_tool"`
}

type interceptor struct {
	workdir string
}

// load reads the config on every call: hooks are edited mid-session and
// should take effect immediately; the file is tiny.
func (i *interceptor) load() hookConfig {
	var cfg hookConfig
	data, err := os.ReadFile(filepath.Join(i.workdir, ".tether", "hooks.json"))
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(data, &cfg)
	return cfg
}

func matches(h hook, tool string) bool {
	return h.Match == "*" || h.Match == tool
}

const hookTimeout = 30 * time.Second

func (i *interceptor) run(ctx context.Context, h hook, call provider.ToolCall) (string, int, error) {
	ctx, cancel := context.WithTimeout(ctx, hookTimeout)
	defer cancel()

	payload, _ := json.Marshal(map[string]any{
		"tool": call.Name,
		"args": json.RawMessage(call.Args),
	})
	cmd := exec.CommandContext(ctx, "bash", "-c", h.Command)
	cmd.Dir = i.workdir
	cmd.Stdin = bytes.NewReader(payload)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	err := cmd.Run()
	text := strings.TrimSpace(out.String())
	if len(text) > 4096 {
		text = text[:4096] + "…"
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return text, exitErr.ExitCode(), nil
	}
	return text, 0, err
}

func (i *interceptor) Name() string { return "hooks" }

func (i *interceptor) BeforeTool(ctx context.Context, call provider.ToolCall) (string, error) {
	var notes []string
	for _, h := range i.load().PreTool {
		if !matches(h, call.Name) {
			continue
		}
		out, code, err := i.run(ctx, h, call)
		if err != nil {
			// A broken hook shouldn't brick the harness; note it and move on.
			notes = append(notes, fmt.Sprintf("pre-hook %q failed to run: %v", h.Command, err))
			continue
		}
		if code == 2 {
			if out == "" {
				out = fmt.Sprintf("pre_tool hook %q rejected the call", h.Command)
			}
			return strings.Join(notes, "\n"), fmt.Errorf("%s", out)
		}
		notes = append(notes, fmt.Sprintf("pre-hook %q passed", h.Command))
	}
	return strings.Join(notes, "\n"), nil
}

func (i *interceptor) AfterTool(ctx context.Context, call provider.ToolCall, _ tools.Result, runErr error) string {
	if runErr != nil {
		return ""
	}
	var notes []string
	for _, h := range i.load().PostTool {
		if !matches(h, call.Name) {
			continue
		}
		out, code, err := i.run(ctx, h, call)
		if err != nil || out == "" {
			continue
		}
		if code != 0 {
			notes = append(notes, fmt.Sprintf("post-hook (%s, exit %d):\n%s", h.Command, code, out))
		} else {
			notes = append(notes, fmt.Sprintf("post-hook (%s):\n%s", h.Command, out))
		}
	}
	return strings.Join(notes, "\n\n")
}
