package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/KonMam/tether/internal/provider"
	"github.com/KonMam/tether/internal/tools"
)

// Agent control tools: the model-facing side of background subagents.
// They are session-scoped (they see only this session's registry), safe
// to read, and agent_kill only stops work the same turn started.
// Registered next to spawn_agent under the subagents module gate.

const agentOutputTail = 2000

// AgentList rows: id, name, status, task snippet.
type AgentList struct {
	ls *liveSession
}

func (t *AgentList) Safe() bool { return true }

func (t *AgentList) Def() provider.ToolDef {
	return provider.ToolDef{
		Name:        "agent_list",
		Description: "List the subagents spawned in this session: id, name, status, and a task snippet.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

func (t *AgentList) Run(_ context.Context, _ json.RawMessage) (tools.Result, error) {
	entries := t.ls.subagents.list()
	if len(entries) == 0 {
		return tools.Result{Content: "no subagents"}, nil
	}
	var sb strings.Builder
	for _, e := range entries {
		status, _, _ := e.snapshot()
		task := e.task
		if len(task) > 60 {
			task = task[:57] + "..."
		}
		name := e.name
		if name == "" {
			name = "subagent"
		}
		fmt.Fprintf(&sb, "%s  %s  %s  %s\n", e.id, status, name, task)
	}
	return tools.Result{Content: strings.TrimSpace(sb.String())}, nil
}

// AgentOutput reads a subagent's report (terminal) or its streamed text
// so far (running, capped at the last 2000 characters).
type AgentOutput struct {
	ls *liveSession
}

func (t *AgentOutput) Safe() bool { return true }

func (t *AgentOutput) Def() provider.ToolDef {
	return provider.ToolDef{
		Name:        "agent_output",
		Description: "Read a subagent's status and output: its final report when finished, or the last 2000 characters of streamed text while it runs.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "The subagent id returned by spawn_agent or agent_list.",
				},
			},
			"required": []string{"id"},
		},
	}
}

func (t *AgentOutput) Run(_ context.Context, args json.RawMessage) (tools.Result, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return tools.Result{}, err
	}
	e := t.ls.subagents.get(in.ID)
	if e == nil {
		return tools.Result{Content: fmt.Sprintf("no subagent %q", in.ID)}, nil
	}
	status, _, report := e.snapshot()
	if status != SubagentRunning {
		out := report
		if out == "" {
			out = "(no report)"
		}
		return tools.Result{Content: fmt.Sprintf("%s  %s\n%s", e.id, status, out)}, nil
	}
	// Running: the last of the streamed text, joined from the child's own
	// messages (the registry does not hold deltas).
	msgs := e.agent.Messages()
	var sb strings.Builder
	for _, m := range msgs {
		if m.Role == provider.RoleAssistant && m.Content != "" {
			sb.WriteString(m.Content)
			sb.WriteString("\n")
		}
	}
	out := sb.String()
	if out == "" {
		out = "(no output yet)"
	}
	if len(out) > agentOutputTail {
		out = "[earlier output dropped]\n" + out[len(out)-agentOutputTail:]
	}
	return tools.Result{Content: fmt.Sprintf("%s  %s\n%s", e.id, status, out)}, nil
}

// AgentKill interrupts a running subagent. Safe: it only stops work the
// same turn started, and the spawn tool turns the cancellation into an
// "interrupted by the user" report, not an error.
type AgentKill struct {
	ls *liveSession
}

func (t *AgentKill) Safe() bool { return true }

func (t *AgentKill) Def() provider.ToolDef {
	return provider.ToolDef{
		Name:        "agent_kill",
		Description: "Stop a running subagent. The spawn tool reports it as interrupted, not failed.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "The subagent id to stop.",
				},
			},
			"required": []string{"id"},
		},
	}
}

func (t *AgentKill) Run(_ context.Context, args json.RawMessage) (tools.Result, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return tools.Result{}, err
	}
	e := t.ls.subagents.get(in.ID)
	if e == nil {
		return tools.Result{Content: fmt.Sprintf("no subagent %q", in.ID)}, nil
	}
	status, _, _ := e.snapshot()
	if status != SubagentRunning {
		return tools.Result{Content: fmt.Sprintf("subagent %s is already %s", in.ID, status)}, nil
	}
	e.cancel()
	return tools.Result{Content: "interrupted " + in.ID}, nil
}
