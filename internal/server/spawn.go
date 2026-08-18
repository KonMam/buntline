package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/KonMam/tether/internal/agent"
	"github.com/KonMam/tether/internal/provider"
	"github.com/KonMam/tether/internal/tools"
)

// agentDef is a named agent from <workdir>/.tether/agents/<name>.md:
// optional "description:" and "tools:" lines in a --- frontmatter block,
// body = the agent's system prompt.
type agentDef struct {
	Name        string
	Description string
	Tools       string // "read_only" (default) or "all"
	Prompt      string
}

func loadAgentDefs(workdir string) []agentDef {
	dir := filepath.Join(workdir, ".tether", "agents")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var defs []agentDef
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		def := agentDef{Name: strings.TrimSuffix(e.Name(), ".md"), Tools: "read_only"}
		body := strings.TrimSpace(string(data))
		if rest, ok := strings.CutPrefix(body, "---\n"); ok {
			if front, tail, ok := strings.Cut(rest, "\n---"); ok {
				for _, line := range strings.Split(front, "\n") {
					if v, ok := strings.CutPrefix(line, "description:"); ok {
						def.Description = strings.TrimSpace(v)
					}
					if v, ok := strings.CutPrefix(line, "tools:"); ok {
						def.Tools = strings.TrimSpace(v)
					}
				}
				body = strings.TrimSpace(tail)
			}
		}
		if body == "" {
			continue
		}
		def.Prompt = body
		defs = append(defs, def)
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs
}

// spawnTool runs an isolated child agent inside a tool call. The child has
// its own transcript (the parent's context stays lean, the whole point)
// and emits events tagged with the spawning call's ID so the trace nests
// them. Approvals flow through the same gate as the parent's.
type spawnTool struct {
	server    *Server
	sessionID string
	ls        *liveSession
	workdir   string
	prov      provider.Provider
	model     string
	defs      []agentDef
}

func (t *spawnTool) Safe() bool { return false }

func (t *spawnTool) Def() provider.ToolDef {
	desc := "Delegate a self-contained task to a subagent with its own fresh context. " +
		"It works in the same directory and returns a final report. " +
		"Use for exploration that would flood your context (searching a large codebase, reading many files)."
	props := map[string]any{
		"task": map[string]any{
			"type":        "string",
			"description": "Complete, self-contained instructions. The subagent sees nothing of this conversation.",
		},
		"tools": map[string]any{
			"type":        "string",
			"enum":        []string{"read_only", "all"},
			"description": "read_only (default): read_file, grep, glob. all: the full tool set, side effects included.",
		},
		"run_in_background": map[string]any{
			"type":        "boolean",
			"description": "Start the subagent without blocking the turn; returns immediately and the agent keeps working (check it with agent_output).",
		},
	}
	if len(t.defs) > 0 {
		names := make([]string, 0, len(t.defs))
		var lines []string
		for _, d := range t.defs {
			names = append(names, d.Name)
			lines = append(lines, fmt.Sprintf("%s: %s", d.Name, d.Description))
		}
		desc += " Named agents defined for this project: " + strings.Join(lines, "; ")
		props["agent"] = map[string]any{
			"type":        "string",
			"enum":        names,
			"description": "Optional named agent to run; its own instructions and tool policy apply.",
		}
	}
	return provider.ToolDef{
		Name:        "spawn_agent",
		Description: desc,
		Parameters: map[string]any{
			"type":       "object",
			"properties": props,
			"required":   []string{"task"},
		},
	}
}

const subagentPrompt = "You are a subagent of tether working in %s. " +
	"Complete the task you are given using the available tools, then reply with a final report. " +
	"Your last message is returned verbatim to the agent that spawned you; make it a dense, complete answer."

func (t *spawnTool) Run(ctx context.Context, args json.RawMessage) (tools.Result, error) {
	var in struct {
		Task            string `json:"task"`
		Tools           string `json:"tools"`
		Agent           string `json:"agent"`
		RunInBackground bool   `json:"run_in_background"`
	}
	if err := json.Unmarshal(args, &in); err != nil || strings.TrimSpace(in.Task) == "" {
		return tools.Result{}, fmt.Errorf("task is required")
	}

	prompt := fmt.Sprintf(subagentPrompt, t.workdir)
	toolPolicy := in.Tools
	if in.Agent != "" {
		found := false
		for _, d := range t.defs {
			if d.Name == in.Agent {
				prompt = d.Prompt + "\n\n" + prompt
				toolPolicy = d.Tools
				found = true
				break
			}
		}
		if !found {
			return tools.Result{}, fmt.Errorf("no agent named %q in .tether/agents", in.Agent)
		}
	}

	var registry *tools.Registry
	if toolPolicy == "all" {
		registry = tools.Default(t.workdir)
	} else {
		w := tools.Workdir(t.workdir)
		registry = tools.NewRegistry(
			&tools.ReadFile{W: w},
			&tools.Grep{Dir: t.workdir},
			&tools.Glob{Dir: t.workdir},
		)
	}

	parentID := agent.ToolCallID(ctx)
	child := agent.New(agent.Config{
		Provider:     t.prov,
		Model:        t.model,
		Tools:        registry,
		Approver:     &httpApprover{server: t.server, sessionID: t.sessionID},
		SystemPrompt: prompt,
		MaxRounds:    15,
		Emit: func(ev agent.Event) {
			// Nest under the spawning call; deltas stay ephemeral and the
			// child's messages don't enter the parent transcript.
			ev.ParentID = parentID
			if ev.Type == agent.EventMessage {
				return
			}
			t.server.dispatch(t.sessionID, t.ls, ev)
		},
	})

	// Foreground children derive their context from the parent's tool
	// call, so interrupting the parent turn stops them too. Background
	// children get an independent context: they must outlive the turn
	// that started them, and only the interrupt endpoint (or agent_kill)
	// stops them.
	childCtx, cancel := context.WithCancel(ctx)
	if in.RunInBackground {
		childCtx, cancel = context.WithCancel(context.Background())
	}
	// The child enters the session's registry the moment it starts, so
	// the UI can list it as running before its first event arrives.
	entry := &subagentEntry{
		id:        parentID,
		name:      in.Agent,
		task:      in.Task,
		startedAt: time.Now(),
		agent:     child,
		cancel:    cancel,
		status:    SubagentRunning,
	}
	t.ls.subagents.add(entry)

	if in.RunInBackground {
		// Start the child and return immediately. The parent turn keeps
		// going; the registry rows and agent_* tools follow the child to
		// completion, and the janitor keeps the session alive meanwhile.
		go t.runChild(childCtx, child, entry, in.Task, parentID)
		return tools.Result{Content: "started agent " + parentID + "; check it with agent_output"}, nil
	}

	report, status, err := t.awaitChild(childCtx, child, entry, in.Task)
	cancel()
	if status == SubagentInterrupted {
		return tools.Result{Content: report}, nil
	}
	if err != nil {
		return tools.Result{}, fmt.Errorf("subagent: %w", err)
	}
	return tools.Result{Content: report}, nil
}

// runChild drives a background child to its terminal state, then posts
// the finished notice to the parent.
func (t *spawnTool) runChild(childCtx context.Context, child *agent.Agent, entry *subagentEntry, task, parentID string) {
	report, _, _ := t.awaitChild(childCtx, child, entry, task)
	// The child is done; release its context so nothing keeps the
	// independent (background) context alive past the entry's life.
	entry.cancel()
	t.noticeFinished(parentID, report)
}

// awaitChild runs the child turn and classifies its exit exactly as the
// registry records it: interrupted (its own context was cancelled), a
// failure (error), or done with the child's final report.
func (t *spawnTool) awaitChild(childCtx context.Context, child *agent.Agent, entry *subagentEntry, task string) (string, SubagentStatus, error) {
	report := ""
	status := SubagentDone
	err := child.Run(childCtx, task)
	switch {
	case err != nil && childCtx.Err() != nil:
		// The child's own context was cancelled, by the interrupt
		// endpoint or agent_kill. Say so in the report rather than
		// presenting it as a failure.
		status = SubagentInterrupted
		report = "interrupted by the user"
	case err != nil:
		status = SubagentFailed
		report = "error: " + err.Error()
	default:
		msgs := child.Messages()
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role == provider.RoleAssistant && msgs[i].Content != "" {
				report = msgs[i].Content
				break
			}
		}
		if report == "" {
			report = "(subagent finished without a report)"
		}
	}
	entry.finish(status, report)
	return report, status, err
}

// noticeFinished tells the parent a background child completed: steered
// in when the parent is mid-turn (the loop picks it up at the next round
// boundary), appended as a user-role message when idle so the next turn
// sees it. Mirrors how bash_background reports early exit.
func (t *spawnTool) noticeFinished(parentID, report string) {
	ls := t.ls
	if ls == nil || ls.agent == nil {
		return
	}
	notice := fmt.Sprintf("agent %s finished; report available via agent_output", parentID)
	if report != "" {
		// A capped preview only: a child's report can be thousands of
		// tokens, and the notice lands in the parent's context. The full
		// report stays one agent_output call away.
		preview := report
		if len(preview) > 2000 {
			preview = preview[:2000] + "\n… (full report via agent_output)"
		}
		notice += "\n\n" + preview
	}
	if ls.agent.Busy() {
		if err := ls.agent.Steer(notice); err != nil {
			t.server.log.Error("subagent notice dropped", "agent", parentID, "err", err)
		}
		return
	}
	msg := provider.Message{Role: provider.RoleUser, Content: notice}
	// The in-memory transcript carries it into the next turn; dispatch
	// persists it and streams it to the UI.
	ls.agent.SetMessages(append(ls.agent.Messages(), msg))
	t.server.dispatch(t.sessionID, t.ls, agent.Event{Type: agent.EventMessage, Message: &msg})
}
