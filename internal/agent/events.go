package agent

import (
	"context"
	"time"

	"github.com/KonMam/buntline/internal/provider"
)

// EventType enumerates everything the agent does. The stream of these is
// the whole observable behavior of the harness: the UI renders it, the
// activity log persists it, headless mode prints it.
type EventType string

const (
	// EventMessage carries a complete transcript message (user, assistant,
	// or tool result). The durable record; deltas are ephemeral.
	EventMessage EventType = "message"
	// EventTextDelta / EventThinkingDelta stream assistant output.
	EventTextDelta     EventType = "text_delta"
	EventThinkingDelta EventType = "thinking_delta"
	// EventToolStart / EventToolEnd bracket one tool execution.
	EventToolStart EventType = "tool_start"
	EventToolEnd   EventType = "tool_end"
	// EventToolBg marks a tool whose execution moved off the loop: the
	// tool exceeded the grace period, its placeholder result is in the
	// transcript, and its real result will arrive as a later tool_end.
	// The UI renders it as the running state of the tool's card.
	EventToolBg EventType = "tool_bg"
	// EventApprovalRequest pauses the loop; EventApprovalResult records
	// the user's decision.
	EventApprovalRequest EventType = "approval_request"
	EventApprovalResult  EventType = "approval_result"
	// EventQuestionRequest pauses the loop for the ask_user tool;
	// EventQuestionResult carries the user's answer back to it.
	EventQuestionRequest EventType = "question_request"
	EventQuestionResult  EventType = "question_result"
	// EventModelStart marks the beginning of one model call (round N of a
	// turn); the matching EventUsage closes it with duration and tokens.
	EventModelStart EventType = "model_start"
	// EventUsage closes a model call: token accounting (when the provider
	// reports it) plus wall-clock duration. Always emitted, even if the
	// provider sent no usage; the trace needs the span regardless.
	EventUsage EventType = "usage"
	// EventTurnStart / EventTurnEnd bracket one user request.
	EventTurnStart EventType = "turn_start"
	EventTurnEnd   EventType = "turn_end"
	// EventCompact reports an explicit transcript compaction and its cost.
	EventCompact EventType = "compact"
	// EventInterceptor records what an interceptor did around a tool call
	// (snapshot taken, diagnostics found, hook output). ToolName carries
	// the interceptor's name; ToolID links to the call.
	EventInterceptor EventType = "interceptor"
	// EventTasks carries the model's task list: one whole-list snapshot
	// per todo_write call, the same shape the tool received. The current
	// list is the most recent such event (last-write-wins on replay).
	EventTasks EventType = "tasks"
	EventError EventType = "error"
)

// Event is one entry in the activity stream. Fields are a union across
// types; omitempty keeps the JSON small.
type Event struct {
	Type EventType `json:"type"`
	Time time.Time `json:"time"`
	// TurnID groups every event belonging to one user turn (or one compact
	// operation): the unit the trace view is built around.
	TurnID string `json:"turn_id,omitempty"`
	// ParentID nests this event under a tool call (subagents: the child
	// loop's events carry the spawning call's ID).
	ParentID string `json:"parent_id,omitempty"`
	// Round is the model-call index within a turn (0-based).
	Round int `json:"round,omitempty"`

	Message *provider.Message `json:"message,omitempty"`

	Text string `json:"text,omitempty"`

	ToolID   string `json:"tool_id,omitempty"`
	ToolName string `json:"tool_name,omitempty"`
	ToolArgs string `json:"tool_args,omitempty"`
	// Result is the tool output as returned to the model (already capped).
	Result string `json:"result,omitempty"`
	// Diff is a unified diff when the tool changed a file.
	Diff       string `json:"diff,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	// FirstTokenMs is the time to first streamed token of a model call:
	// the split that separates prompt cost from generation cost.
	FirstTokenMs int64 `json:"first_token_ms,omitempty"`

	ApprovalID string `json:"approval_id,omitempty"`
	Decision   string `json:"decision,omitempty"`

	// Question / Options carry the ask_user tool's payload on
	// question_request events; Answer is the user's reply on
	// question_result.
	Question string   `json:"question,omitempty"`
	Options  []string `json:"options,omitempty"`
	Answer   string   `json:"answer,omitempty"`

	// Tasks carries the whole task list on EventTasks events.
	Tasks []TaskItem `json:"tasks,omitempty"`

	Usage *provider.Usage `json:"usage,omitempty"`

	StopReason string `json:"stop_reason,omitempty"`
	Error      string `json:"error,omitempty"`
}

// TaskItem is one entry in the model's task list. Status is one of
// "pending", "in_progress", or "completed"; the list has no ids or
// priorities; the whole list is replaced on every write, so items
// need no stable identity (the deepseek-harness todo minimum).
type TaskItem struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

// Decision is a user's answer to an approval request.
type Decision string

const (
	DecisionAllow        Decision = "allow"
	DecisionAllowSession Decision = "allow_session"
	// DecisionAllowAlways persists an allowlist rule for the repository;
	// the approver that owns persistence translates it to a plain allow
	// before it reaches the loop.
	DecisionAllowAlways Decision = "allow_always"
	DecisionDeny        Decision = "deny"
)

// ApprovalRequest describes a side-effectful tool call awaiting the user.
type ApprovalRequest struct {
	ID       string `json:"id"`
	ToolName string `json:"tool_name"`
	ToolArgs string `json:"tool_args"`
}

// Approver decides tool-call approvals. The server implements it with a
// browser round-trip; headless mode with a policy flag. Blocking is the
// point: the loop pauses until a decision arrives or ctx is cancelled.
type Approver interface {
	RequestApproval(ctx context.Context, req ApprovalRequest) (Decision, error)
}

// ApprovalAnnouncer is implemented by approvers that announce their own
// approval_request events. The browser approver announces exactly when it
// decides a human is needed; calls it auto-resolves (session approval
// modes, durable allowlists) are never announced, so the notification
// bell and attention banner don't fire for approvals that never reach a
// human. Approvers without this method keep the agent's default: every
// request is announced before the decision round-trip.
type ApprovalAnnouncer interface {
	AnnounceApproval(req ApprovalRequest)
}
