// Package files is the file-browser module: a read-only view of a
// session's working directory for the UI. Same path confinement as the
// file tools.
package files

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/KonMam/buntline/internal/module"
	"github.com/KonMam/buntline/internal/tools"
)

const readCap = 256 * 1024

// Workdir resolves a session id to its working directory.
type Workdir func(sessionID string) (string, error)

type Module struct {
	Lookup Workdir
}

func (m *Module) Info() module.Info {
	return module.Info{
		ID:          "files",
		Name:        "File browser",
		Description: "Browse and view files in the session's working directory.",
		Default:     true,
	}
}

func (m *Module) Routes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"GET /tree": m.handleTree,
		"GET /file": m.handleFile,
		"GET /list": m.handleList,
	}
}

const listCap = 3000

// handleList returns the workdir's files recursively (gitignore-aware via
// rg, walk fallback), the source for @-mention completion in the composer.
func (m *Module) handleList(w http.ResponseWriter, r *http.Request) {
	workdir, err := m.Lookup(r.URL.Query().Get("session"))
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	var paths []string
	if tools.RipgrepAvailable() {
		cmd := exec.CommandContext(r.Context(), "rg", "--files", ".")
		cmd.Dir = workdir
		if out, err := cmd.Output(); err == nil {
			for _, p := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if p != "" {
					paths = append(paths, filepath.ToSlash(strings.TrimPrefix(p, "./")))
				}
			}
		}
	}
	if paths == nil {
		_ = filepath.WalkDir(workdir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			name := d.Name()
			if d.IsDir() {
				if strings.HasPrefix(name, ".") || name == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if rel, err := filepath.Rel(workdir, p); err == nil {
				paths = append(paths, filepath.ToSlash(rel))
			}
			return nil
		})
	}
	sort.Strings(paths)
	truncated := false
	if len(paths) > listCap {
		paths = paths[:listCap]
		truncated = true
	}
	writeJSON(w, map[string]any{"files": paths, "truncated": truncated})
}

type entry struct {
	Name  string `json:"name"`
	Dir   bool   `json:"dir"`
	Size  int64  `json:"size,omitempty"`
	Count int    `json:"count,omitempty"` // child entries, for directories
}

func (m *Module) resolve(r *http.Request) (string, error) {
	workdir, err := m.Lookup(r.URL.Query().Get("session"))
	if err != nil {
		return "", err
	}
	rel := r.URL.Query().Get("path")
	if rel == "" {
		rel = "."
	}
	return tools.Workdir(workdir).Resolve(rel)
}

func (m *Module) handleTree(w http.ResponseWriter, r *http.Request) {
	path, err := m.resolve(r)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	dirents, err := os.ReadDir(path)
	if err != nil {
		fail(w, http.StatusNotFound, err)
		return
	}
	entries := []entry{}
	for _, d := range dirents {
		name := d.Name()
		// Hide dotfiles and dependency dirs; this is a workbench view,
		// not a filesystem audit.
		if strings.HasPrefix(name, ".") || name == "node_modules" {
			continue
		}
		e := entry{Name: name, Dir: d.IsDir()}
		if info, err := d.Info(); err == nil && !d.IsDir() {
			e.Size = info.Size()
		}
		if d.IsDir() {
			if children, err := os.ReadDir(filepath.Join(path, name)); err == nil {
				e.Count = len(children)
			}
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Dir != entries[j].Dir {
			return entries[i].Dir
		}
		return entries[i].Name < entries[j].Name
	})
	writeJSON(w, map[string]any{"entries": entries})
}

func (m *Module) handleFile(w http.ResponseWriter, r *http.Request) {
	path, err := m.resolve(r)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fail(w, http.StatusNotFound, err)
		return
	}
	truncated := false
	if len(data) > readCap {
		data = data[:readCap]
		truncated = true
	}
	writeJSON(w, map[string]any{
		"content":   string(data),
		"truncated": truncated,
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
