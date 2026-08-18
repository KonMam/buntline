package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/KonMam/tether/internal/provider"
)

// ReadFile returns file contents, capped.
type ReadFile struct{ W Workdir }

func (t *ReadFile) Safe() bool { return true }

func (t *ReadFile) Def() provider.ToolDef {
	return provider.ToolDef{
		Name:        "read_file",
		Description: "Read a file and return its contents. Large files are truncated.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "File path, relative to the working directory.",
				},
			},
			"required": []string{"path"},
		},
	}
}

func (t *ReadFile) Run(_ context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := decode(args, &in); err != nil {
		return Result{}, err
	}
	path, err := t.W.Resolve(in.Path)
	if err != nil {
		return Result{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, err
	}
	if len(data) == 0 {
		return Result{Content: "(empty file)"}, nil
	}
	return Result{Content: string(data)}, nil
}

// WriteFile creates or overwrites a file, creating parent directories.
type WriteFile struct{ W Workdir }

func (t *WriteFile) Safe() bool { return false }

func (t *WriteFile) Def() provider.ToolDef {
	return provider.ToolDef{
		Name:        "write_file",
		Description: "Create or overwrite a file with the given content. Parent directories are created as needed.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "File path, relative to the working directory.",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "Full file content to write.",
				},
			},
			"required": []string{"path", "content"},
		},
	}
}

func (t *WriteFile) Run(ctx context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := decode(args, &in); err != nil {
		return Result{}, err
	}
	path, err := t.W.Resolve(in.Path)
	if err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Result{}, err
	}
	// Overwrites diff against the previous content; new files against "".
	before := ""
	if prev, err := os.ReadFile(path); err == nil {
		before = string(prev)
	}
	if err := os.WriteFile(path, []byte(in.Content), 0o644); err != nil {
		return Result{}, err
	}
	return Result{
		Content: fmt.Sprintf("wrote %d bytes to %s", len(in.Content), in.Path),
		Diff:    unifiedDiff(ctx, in.Path, before, in.Content),
	}, nil
}

// EditFile replaces one exact occurrence of a string. Zero matches is an
// error (the model's view of the file is stale); more than one is an error
// (ambiguous edit). The constraint turns silent mis-edits into loud ones.
type EditFile struct{ W Workdir }

func (t *EditFile) Safe() bool { return false }

func (t *EditFile) Def() provider.ToolDef {
	return provider.ToolDef{
		Name:        "edit_file",
		Description: "Replace an exact string in a file. old_string must appear exactly once; include enough surrounding context to make it unique.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "File path, relative to the working directory.",
				},
				"old_string": map[string]any{
					"type":        "string",
					"description": "Exact text to replace. Must match exactly one location.",
				},
				"new_string": map[string]any{
					"type":        "string",
					"description": "Replacement text.",
				},
			},
			"required": []string{"path", "old_string", "new_string"},
		},
	}
}

func (t *EditFile) Run(ctx context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Path      string `json:"path"`
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	}
	if err := decode(args, &in); err != nil {
		return Result{}, err
	}
	if in.OldString == "" {
		return Result{}, fmt.Errorf("old_string is required")
	}
	if in.OldString == in.NewString {
		return Result{}, fmt.Errorf("old_string and new_string are identical")
	}
	path, err := t.W.Resolve(in.Path)
	if err != nil {
		return Result{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, err
	}
	content := string(data)
	switch n := strings.Count(content, in.OldString); n {
	case 0:
		return Result{}, fmt.Errorf("old_string not found in %s; re-read the file, its content may have changed", in.Path)
	case 1:
		// exactly one match: proceed
	default:
		return Result{}, fmt.Errorf("old_string matches %d locations in %s; include more surrounding context to make it unique", n, in.Path)
	}
	updated := strings.Replace(content, in.OldString, in.NewString, 1)
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return Result{}, err
	}
	return Result{
		Content: fmt.Sprintf("edited %s", in.Path),
		Diff:    unifiedDiff(ctx, in.Path, content, updated),
	}, nil
}
