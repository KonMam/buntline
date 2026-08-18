// Package provider defines the interface between the agent loop and an LLM
// backend, plus the typed events a streaming completion produces. Adapters
// (openai.go) translate a provider's wire format into these types; nothing
// above this package knows what protocol the model speaks.
package provider

import "context"

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one entry in the conversation transcript.
type Message struct {
	Role      Role       `json:"role"`
	Content   string     `json:"content"`
	Thinking  string     `json:"thinking,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID links a RoleTool result back to the call that produced it.
	ToolCallID string `json:"tool_call_id,omitempty"`
	// Kind tags harness-generated messages ("instructions") so UIs can
	// render them collapsed. Empty for ordinary conversation.
	Kind string `json:"kind,omitempty"`
	// Images are data URLs (data:image/...;base64,...) attached to a user
	// message. Sent as content parts to vision-capable models.
	Images []string `json:"images,omitempty"`
}

// ToolCall is a model-requested tool invocation. Args is the raw JSON
// argument string exactly as the model produced it.
type ToolCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Args string `json:"args"`
}

// ToolDef describes a tool to the model. Parameters is a JSON Schema object.
type ToolDef struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// Usage is per-request token accounting as reported by the provider.
// CachedTokens is the provider's cache-hit count where exposed (OpenAI,
// DeepSeek); for Ollama, PromptTokens is already "tokens actually evaluated",
// which carries the same signal from the other side.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CachedTokens     int `json:"cached_tokens"`
}

type Request struct {
	Model    string
	Messages []Message
	Tools    []ToolDef
}

type EventKind int

const (
	// EventTextDelta carries a fragment of assistant text.
	EventTextDelta EventKind = iota
	// EventThinkingDelta carries a fragment of reasoning text, for models
	// and servers that expose it as a separate stream.
	EventThinkingDelta
	// EventToolCalls carries the complete, accumulated tool calls for the
	// turn. Emitted once, before EventDone, only if the model called tools.
	EventToolCalls
	// EventUsage carries token accounting, if the provider reports it.
	EventUsage
	// EventDone terminates the stream. FinishReason is provider-reported
	// ("stop", "tool_calls", ...).
	EventDone
	// EventError terminates the stream with an error.
	EventError
)

// Event is one item in a completion stream. Exactly one of the payload
// fields is meaningful, selected by Kind.
type Event struct {
	Kind         EventKind
	Text         string
	ToolCalls    []ToolCall
	Usage        *Usage
	FinishReason string
	Err          error
}

// Provider streams chat completions. The returned channel is closed after
// EventDone or EventError. Cancelling ctx aborts the stream.
type Provider interface {
	Name() string
	Stream(ctx context.Context, req Request) (<-chan Event, error)
	// SupportsImages reports whether the backend accepts image content
	// parts (data URLs) in user messages. Text-only APIs (DeepSeek
	// among them) reject the image_url part outright, so the harness
	// refuses image uploads up front instead of surfacing the provider's
	// raw deserialization error. The transcript still records the
	// images; only the wire format drops them.
	SupportsImages() bool
}
