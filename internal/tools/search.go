package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/KonMam/tether/internal/provider"
)

// RipgrepAvailable reports whether rg is on PATH. Checked at startup so the
// failure is a clear message, not a per-call surprise.
func RipgrepAvailable() bool {
	_, err := exec.LookPath("rg")
	return err == nil
}

// Grep searches file contents via ripgrep, the same choice every major
// harness made: fast, .gitignore-aware, battle-tested.
type Grep struct {
	Dir string
}

func (t *Grep) Safe() bool { return true }

func (t *Grep) Def() provider.ToolDef {
	return provider.ToolDef{
		Name:        "grep",
		Description: "Search file contents with a regular expression (ripgrep). Returns matching lines as path:line:text.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Regular expression to search for.",
				},
				"glob": map[string]any{
					"type":        "string",
					"description": "Optional file filter, e.g. '*.go' or 'src/**/*.ts'.",
				},
			},
			"required": []string{"pattern"},
		},
	}
}

func (t *Grep) Run(ctx context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Pattern string `json:"pattern"`
		Glob    string `json:"glob"`
	}
	if err := decode(args, &in); err != nil {
		return Result{}, err
	}
	if in.Pattern == "" {
		return Result{}, fmt.Errorf("pattern is required")
	}
	argv := []string{"--no-heading", "--line-number", "--smart-case", "--max-count", "50"}
	if in.Glob != "" {
		argv = append(argv, "--glob", in.Glob)
	}
	argv = append(argv, "--regexp", in.Pattern, ".")

	cmd := exec.CommandContext(ctx, "rg", argv...)
	cmd.Dir = t.Dir
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return Result{Content: "no matches"}, nil // rg exit 1 = clean no-match
		}
		return Result{}, fmt.Errorf("rg: %w", err)
	}
	return Result{Content: string(out)}, nil
}

// Glob lists files matching a pattern. Uses rg --files -g (gitignore-aware);
// falls back to a filesystem walk when rg is unavailable.
type Glob struct {
	Dir string
}

func (t *Glob) Safe() bool { return true }

func (t *Glob) Def() provider.ToolDef {
	return provider.ToolDef{
		Name:        "glob",
		Description: "List files matching a glob pattern, e.g. '**/*.go' or 'web/src/*.svelte'.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Glob pattern; ** matches directories recursively.",
				},
			},
			"required": []string{"pattern"},
		},
	}
}

func (t *Glob) Run(ctx context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Pattern string `json:"pattern"`
	}
	if err := decode(args, &in); err != nil {
		return Result{}, err
	}
	if in.Pattern == "" {
		return Result{}, fmt.Errorf("pattern is required")
	}

	if RipgrepAvailable() {
		cmd := exec.CommandContext(ctx, "rg", "--files", "--glob", in.Pattern, ".")
		cmd.Dir = t.Dir
		out, err := cmd.Output()
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
				return Result{Content: "no matches"}, nil
			}
			return Result{}, fmt.Errorf("rg: %w", err)
		}
		return Result{Content: string(out)}, nil
	}
	return t.walk(in.Pattern)
}

// walk is the rg-less fallback: match the pattern segment-wise, with **
// crossing directory boundaries. Good enough for the fallback path; rg is
// the real implementation.
func (t *Glob) walk(pattern string) (Result, error) {
	var matches []string
	err := filepath.WalkDir(t.Dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // unreadable entries are skipped, not fatal
		}
		rel, err := filepath.Rel(t.Dir, path)
		if err != nil {
			return nil //nolint:nilerr
		}
		if strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
			return nil
		}
		if matchGlob(pattern, filepath.ToSlash(rel)) {
			matches = append(matches, rel)
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	if len(matches) == 0 {
		return Result{Content: "no matches"}, nil
	}
	return Result{Content: strings.Join(matches, "\n")}, nil
}

// matchGlob matches slash-separated paths where ** spans segments and
// path.Match handles single segments.
func matchGlob(pattern, name string) bool {
	ps := strings.Split(pattern, "/")
	ns := strings.Split(name, "/")
	return matchSegments(ps, ns)
}

func matchSegments(ps, ns []string) bool {
	for len(ps) > 0 {
		if ps[0] == "**" {
			for i := 0; i <= len(ns); i++ {
				if matchSegments(ps[1:], ns[i:]) {
					return true
				}
			}
			return false
		}
		if len(ns) == 0 {
			return false
		}
		if ok, _ := filepath.Match(ps[0], ns[0]); !ok {
			return false
		}
		ps, ns = ps[1:], ns[1:]
	}
	return len(ns) == 0
}
