// The skill tool: the model can invoke a skill mid-turn, loading its
// body into the conversation as a tool result. This is the progressive-
// disclosure payoff: the skill's description is always in the tool
// definition, and the body loads only when the model calls it.
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/KonMam/tether/internal/provider"
	"github.com/KonMam/tether/internal/tools"
)

// SkillTool lets the model load a skill's body mid-turn. It is safe
// (loading a skill is read-only) and its description carries the
// available skill names and descriptions so the model knows what it can
// invoke.
type SkillTool struct {
	Workdir       string
	UserConfigDir string
}

func (t *SkillTool) Safe() bool { return true }

func (t *SkillTool) Def() provider.ToolDef {
	skills := loadSkills(t.Workdir, t.UserConfigDir)
	var sb strings.Builder
	sb.WriteString("Load a skill's instructions into the conversation by name. ")
	sb.WriteString("A skill is a reusable procedure (code review, deploy, migration). ")
	if len(skills) == 0 {
		sb.WriteString("No skills are available.")
	} else {
		sb.WriteString("Available skills: ")
		for i, s := range skills {
			if i > 0 {
				sb.WriteString("; ")
			}
			fmt.Fprintf(&sb, "%s (%s)", s.Name, s.Description)
		}
		sb.WriteString(".")
	}
	return provider.ToolDef{
		Name:        "skill",
		Description: sb.String(),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "The skill name to load.",
				},
				"args": map[string]any{
					"type":        "string",
					"description": "Optional arguments passed to the skill ($ARGUMENTS).",
				},
			},
			"required": []string{"name"},
		},
	}
}

func (t *SkillTool) Run(_ context.Context, args json.RawMessage) (tools.Result, error) {
	var in struct {
		Name string `json:"name"`
		Args string `json:"args"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return tools.Result{}, err
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return tools.Result{}, fmt.Errorf("name is required")
	}
	skills := loadSkills(t.Workdir, t.UserConfigDir)
	for _, s := range skills {
		if s.Name == in.Name {
			return tools.Result{Content: Render(s.Body, in.Args)}, nil
		}
	}
	// Commands are not invocable by the model (they are a human surface),
	// so an unknown name is a clear miss, not a fallback.
	return tools.Result{Content: fmt.Sprintf(
		"unknown skill %q; available skills: %s",
		in.Name, skillNames(skills))}, nil
}

func skillNames(skills []Skill) string {
	names := make([]string, 0, len(skills))
	for _, s := range skills {
		names = append(names, s.Name)
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}
