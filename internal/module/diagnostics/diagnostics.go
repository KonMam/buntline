// Package diagnostics feeds compiler feedback into tool results: after
// the agent edits a Go file, it immediately sees the type errors it just
// caused instead of discovering them three turns later.
//
// This is deliberately NOT an LSP client: `gopls check` (or `go build` as
// fallback) delivers the diagnostics-injection value at a fraction of the
// jsonrpc2 lifecycle cost. A resident LSP client is the upgrade path if
// per-edit latency ever matters.
package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/KonMam/buntline/internal/agent"
	"github.com/KonMam/buntline/internal/module"
	"github.com/KonMam/buntline/internal/provider"
	"github.com/KonMam/buntline/internal/tools"
)

type Module struct{}

func (m *Module) Info() module.Info {
	return module.Info{
		ID:          "diagnostics",
		Name:        "Diagnostics",
		Description: "After the agent edits a Go file, run gopls/go build and feed errors straight back into the result.",
		Default:     true,
	}
}

func (m *Module) Interceptor(_ string, workdir string) agent.ToolInterceptor {
	return &interceptor{workdir: workdir}
}

type interceptor struct {
	workdir string
}

func (i *interceptor) Name() string { return "diagnostics" }

func (i *interceptor) BeforeTool(context.Context, provider.ToolCall) (string, error) {
	return "", nil
}

func (i *interceptor) AfterTool(ctx context.Context, call provider.ToolCall, _ tools.Result, runErr error) string {
	if runErr != nil {
		return ""
	}
	if call.Name != "edit_file" && call.Name != "write_file" {
		return ""
	}
	var args struct {
		Path string `json:"path"`
	}
	if json.Unmarshal([]byte(call.Args), &args) != nil {
		return ""
	}
	if filepath.Ext(args.Path) != ".go" {
		return ""
	}

	out := i.check(ctx, args.Path)
	if out == "" {
		return ""
	}
	return "diagnostics for " + args.Path + ":\n" + out
}

func (i *interceptor) check(ctx context.Context, rel string) string {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if _, err := exec.LookPath("gopls"); err == nil {
		out := run(ctx, i.workdir, "gopls", "check", rel)
		return capLines(out, 20)
	}
	// Fallback: build the package containing the file.
	dir := filepath.Dir(rel)
	out := run(ctx, i.workdir, "go", "build", "./"+filepath.ToSlash(dir))
	return capLines(out, 20)
}

func run(ctx context.Context, dir, name string, args ...string) string {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	_ = cmd.Run() // non-zero exit just means "there are diagnostics"
	return strings.TrimSpace(out.String())
}

func capLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n") + fmt.Sprintf("\n… %d more lines", len(lines)-n)
}
