// Package commands loads project slash commands and skills: markdown
// files in <workdir>/.buntline/commands/ (one command per file, the body is
// the prompt) and Agent Skills from skills/*/SKILL.md in the project and
// user-global skill directories. Commands keep the historical shape;
// skills add frontmatter, progressive disclosure, and turn-scoped
// allowed-tools grants.
package commands

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/KonMam/buntline/internal/module"
	"github.com/KonMam/buntline/internal/tools"
)

// Workdir resolves a session id to its working directory.
type Workdir func(sessionID string) (string, error)

// UserConfigDir resolves the user config directory (~/.config/buntline),
// where user-global skills live. Injected so the module stays testable.
type UserConfigDir func() string

type Module struct {
	Lookup        Workdir
	UserConfigDir UserConfigDir
}

func (m *Module) Info() module.Info {
	return module.Info{
		ID:          "commands",
		Name:        "Slash commands and skills",
		Description: "Project prompts from .buntline/commands/*.md and Agent Skills from skills/*/SKILL.md, available as /name in the composer.",
		Default:     true,
	}
}

func (m *Module) Routes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"GET /list":    m.handleList,
		"POST /render": m.handleRender,
	}
}

// Tools contributes the skill tool, letting the model invoke a skill
// mid-turn. The tool's description carries the available skill names and
// descriptions (progressive disclosure: names always in context, bodies
// on demand).
func (m *Module) Tools(workdir string) []tools.Tool {
	return []tools.Tool{&SkillTool{Workdir: workdir, UserConfigDir: m.userConfigDir()}}
}

type command struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
	// Skill marks a SKILL.md entry; commands without frontmatter are
	// plain prompts. The flag lets the UI distinguish the two.
	Skill bool `json:"skill,omitempty"`
	// AllowedTools is the turn-scoped approval grant (skills only).
	AllowedTools []string `json:"allowedTools,omitempty"`
}

func (m *Module) userConfigDir() string {
	if m.UserConfigDir != nil {
		return m.UserConfigDir()
	}
	return defaultUserConfigDir()
}

func (m *Module) handleList(w http.ResponseWriter, r *http.Request) {
	workdir, err := m.Lookup(r.URL.Query().Get("session"))
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	cmds := loadCommands(workdir)
	for _, s := range loadSkills(workdir, m.userConfigDir()) {
		// A skill and a command with the same name resolve to the skill
		// (Claude Code's precedence). Commands are read first so the
		// skill can override.
		cmds = dropCommand(cmds, s.Name)
		cmds = append(cmds, command{
			Name:         s.Name,
			Description:  s.Description,
			Body:         s.Body,
			Skill:        true,
			AllowedTools: s.AllowedTools,
		})
	}
	sort.Slice(cmds, func(i, j int) bool { return cmds[i].Name < cmds[j].Name })
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"commands": cmds})
}

// loadCommands reads <workdir>/.buntline/commands/*.md.
func loadCommands(workdir string) []command {
	cmds := []command{}
	dir := filepath.Join(workdir, ".buntline", "commands")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return cmds
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		body := strings.TrimSpace(string(data))
		if body == "" {
			continue
		}
		first := strings.TrimSpace(strings.TrimLeft(strings.SplitN(body, "\n", 2)[0], "# "))
		cmds = append(cmds, command{
			Name:        strings.TrimSuffix(e.Name(), ".md"),
			Description: first,
			Body:        body,
		})
	}
	return cmds
}

// dropCommand removes a command by name (the skill with the same name
// takes precedence).
func dropCommand(cmds []command, name string) []command {
	out := cmds[:0]
	for _, c := range cmds {
		if c.Name != name {
			out = append(out, c)
		}
	}
	return out
}

// handleRender substitutes arguments into a command or skill body. The
// composer renders the prompt before sending it, so the transcript holds
// the exact text the model receives; the same renderer backs the A4 skill
// tool, so one implementation serves both invocation paths.
func (m *Module) handleRender(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Session string `json:"session"`
		Name    string `json:"name"`
		Args    string `json:"args"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Name == "" {
		http.Error(w, `{"error":"name is required"}`, http.StatusBadRequest)
		return
	}
	workdir, err := m.Lookup(in.Session)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	body := m.findBody(workdir, in.Name)
	if body == "" {
		http.Error(w, `{"error":"unknown command or skill"}`, http.StatusNotFound)
		return
	}
	rendered := Render(body, in.Args)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"body": rendered})
}

// findBody returns the body of a named command or skill, skills taking
// precedence over commands.
func (m *Module) findBody(workdir, name string) string {
	for _, s := range loadSkills(workdir, m.userConfigDir()) {
		if s.Name == name {
			return s.Body
		}
	}
	for _, c := range loadCommands(workdir) {
		if c.Name == name {
			return c.Body
		}
	}
	return ""
}

// defaultUserConfigDir is the user config directory for skills.
func defaultUserConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "buntline")
}
