// Package tasks is the model-facing task list: two tools (todo_write,
// todo_read) and a Tasks route the trace panel reads. The list is a
// session-scoped, whole-list-replace state folded from EventTasks
// events; live observation and replay share one fold function so they
// cannot drift. Deliberately minimal item shape (content plus a
// three-state status, no ids, no priority), modeled on
// deepseek-harness's todo_write: the model sends the entire list on
// every write, so items need no stable identity.
//
// A turn that ends with an all-completed (or empty) list is cleared by
// the server (Server.clearTasksIfDone): the model usually re-sends the
// old list marked completed, and without the clear the finished work
// would stick around for the next request. The clear is written as an
// empty EventTasks through the same bridge as a todo_write, so it
// persists and replays like any other write.
package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/KonMam/buntline/internal/agent"
	"github.com/KonMam/buntline/internal/module"
	"github.com/KonMam/buntline/internal/provider"
	"github.com/KonMam/buntline/internal/tools"
)

// Task statuses, the three-state set the list understands. The tool
// accepts these and nothing else; the UI renders them in this order.
const (
	StatusPending    = "pending"
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
)

// validStatus reports whether s is one of the three task statuses.
func validStatus(s string) bool {
	switch s {
	case StatusPending, StatusInProgress, StatusCompleted:
		return true
	}
	return false
}

// Module is the tasks feature: tools, the trace route, and the fold
// that keeps the per-session list. It holds no resources beyond memory,
// so Stop is a no-op and the fold simply follows the persisted events.
type Module struct {
	mu    sync.Mutex
	lists map[string][]agent.TaskItem // sessionID → current folded list
}

func (m *Module) Info() module.Info {
	return module.Info{
		ID:          "tasks",
		Name:        "Tasks",
		Description: "The model can keep a task list with todo_write and todo_read, shown as a Tasks card in the trace.",
		Default:     true,
	}
}

// Tools returns the module's model-facing tools: todo_write (whole-list
// replace) and todo_read (the current list). The server hands them a
// per-session TaskStore bridge before any turn runs, exactly like the
// ask_user answerer.
func (m *Module) Tools(_ string) []tools.Tool {
	return []tools.Tool{
		&TodoWrite{},
		&TodoRead{},
	}
}

// Observer subscribes the module to one session's event stream: any
// EventTasks that flows through the agent's emit path is folded into
// the session list. The server's bridge folds its own writes directly,
// so this is a safety net rather than the only writer, but it is what
// keeps the fold correct if a future emitter sends tasks events through
// the loop.
func (m *Module) Observer(sessionID, _ string) func(agent.Event) {
	return func(ev agent.Event) {
		if ev.Type == agent.EventTasks {
			m.Fold(sessionID, ev.Tasks)
		}
	}
}

// Replay rebuilds a session's folded list from the persisted event log.
// It folds the same way the observer does (last write wins, in event
// order), so a session that detached and re-attached shows exactly what
// live observation would have folded.
func (m *Module) Replay(sessionID string, evs []agent.Event) {
	for _, ev := range evs {
		if ev.Type == agent.EventTasks {
			m.Fold(sessionID, ev.Tasks)
		}
	}
}

// Stop is a no-op: the module holds nothing beyond memory. The fold may
// outlive a disable; it is cheap, and re-enabling restores it from the
// persisted events on the next attach anyway.
func (m *Module) Stop() {}

// Fold replaces a session's list. This is the single fold function:
// the observer, the replay path, and the server's bridge all go through
// it, so live observation and on-load replay cannot drift. An empty or
// nil list clears (the model wrote the whole list, which can be empty).
func (m *Module) Fold(sessionID string, tasks []agent.TaskItem) {
	m.mu.Lock()
	if m.lists == nil {
		m.lists = map[string][]agent.TaskItem{}
	}
	m.lists[sessionID] = append([]agent.TaskItem(nil), tasks...)
	m.mu.Unlock()
}

// Get returns a copy of a session's folded list (nil when none was
// ever written).
func (m *Module) Get(sessionID string) []agent.TaskItem {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]agent.TaskItem(nil), m.lists[sessionID]...)
}

// bridge is the per-session TaskStore the tools hold: Write folds and
// emits one EventTasks, Read returns the folded list. The emit hook is
// wired by the server so the event is stamped, persisted, and
// broadcast exactly like one that left the loop.
type bridge struct {
	m        *Module
	session  string
	emitHook func(agent.Event)
}

// NewBridge returns a per-session TaskStore. emit is called with the
// EventTasks carrying the full list after a Write, so the server can
// dispatch it through the normal event sink.
func (m *Module) NewBridge(session string, emit func(agent.Event)) tools.TaskStore {
	return &bridge{m: m, session: session, emitHook: emit}
}

func (b *bridge) Write(tasks []tools.TaskItem) error {
	// Fold here so the tools' state is coherent the moment Write
	// returns, and emit for the durable record. The observer folds the
	// same list again when the event reaches it; Fold is a whole-list
	// replace, so the double fold is idempotent.
	b.m.Fold(b.session, fromStoreItems(tasks))
	if b.emitHook != nil {
		b.emitHook(agent.Event{Type: agent.EventTasks, Tasks: fromStoreItems(tasks)})
	}
	return nil
}

func (b *bridge) Read() []tools.TaskItem {
	return toStoreItems(b.m.Get(b.session))
}

// validate checks a whole-list replacement: every item has trimmed,
// non-empty, unique content and a known status. It trims content in
// place so the stored list matches what the model sees in the ack.
func validate(tasks []agent.TaskItem) error {
	seen := map[string]bool{}
	for i := range tasks {
		t := &tasks[i]
		t.Content = strings.TrimSpace(t.Content)
		if t.Content == "" {
			return fmt.Errorf("todo %d: content is required", i)
		}
		if !validStatus(t.Status) {
			return fmt.Errorf("todo %d: status must be one of pending, in_progress, completed (got %q)", i, t.Status)
		}
		if seen[t.Content] {
			return fmt.Errorf("duplicate todo %q", t.Content)
		}
		seen[t.Content] = true
	}
	return nil
}

// Ack is the compact acknowledgement todo_write returns to the model:
// the new counts, nothing more.
func Ack(tasks []agent.TaskItem) string {
	var pending, inProgress, completed int
	for _, t := range tasks {
		switch t.Status {
		case StatusPending:
			pending++
		case StatusInProgress:
			inProgress++
		case StatusCompleted:
			completed++
		}
	}
	return fmt.Sprintf("Task list updated: %d pending, %d in progress, %d completed.", pending, inProgress, completed)
}

// Render formats a task list as the todo_read output: one "[status]
// content" line per item, or "No tasks yet" when the list is empty.
func Render(tasks []agent.TaskItem) string {
	if len(tasks) == 0 {
		return "No tasks yet"
	}
	var sb strings.Builder
	for _, t := range tasks {
		fmt.Fprintf(&sb, "[%s] %s\n", t.Status, t.Content)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// Routes exposes the folded list to the UI: GET /list?session=<id>
// returns the current tasks plus counts. The mount's enabled-check
// rejects requests while the module is disabled.
func (m *Module) Routes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"GET /list": m.handleList,
	}
}

func (m *Module) handleList(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session")
	if sessionID == "" {
		httpError(w, http.StatusBadRequest, fmt.Errorf("session is required"))
		return
	}
	tasks := m.Get(sessionID)
	writeJSON(w, map[string]any{
		"tasks":  tasks,
		"counts": counts(tasks),
	})
}

func counts(tasks []agent.TaskItem) map[string]int {
	out := map[string]int{StatusPending: 0, StatusInProgress: 0, StatusCompleted: 0}
	for _, t := range tasks {
		out[t.Status]++
	}
	return out
}

// toStoreItems converts the agent event's item shape to the tools
// package's bridge shape (structurally identical; the packages cannot
// share the type because agent imports tools).
func toStoreItems(in []agent.TaskItem) []tools.TaskItem {
	out := make([]tools.TaskItem, len(in))
	for i, t := range in {
		out[i] = tools.TaskItem{Content: t.Content, Status: t.Status}
	}
	return out
}

// fromStoreItems converts the tools package's bridge shape back to the
// agent event's item shape.
func fromStoreItems(in []tools.TaskItem) []agent.TaskItem {
	out := make([]agent.TaskItem, len(in))
	for i, t := range in {
		out[i] = agent.TaskItem{Content: t.Content, Status: t.Status}
	}
	return out
}

// TodoWrite replaces the whole task list. The model sends the entire
// list on every call; there are no partial updates, per-item edits, or
// ids (the deepseek-harness todo minimum). The list is session state,
// not a side effect on the user's system, so the write is safe and runs
// without the approval gate, and it is visible in the trace either way.
type TodoWrite struct {
	store tools.TaskStore
}

func (t *TodoWrite) Safe() bool { return true }

// SetStore wires the per-session bridge (the server calls this through
// the registry's SetTaskStore). Without one, the tool reports the
// module unavailable.
func (t *TodoWrite) SetStore(s tools.TaskStore) { t.store = s }

func (t *TodoWrite) Def() provider.ToolDef {
	return provider.ToolDef{
		Name:        "todo_write",
		Description: "Replace the entire task list with the given todos. Send the full list on every call; the previous list is discarded. Each todo has content and a status: pending, in_progress, or completed. Use it to keep track of the steps ahead and update statuses as work progresses. The list is shown in the UI and persists for the session; a turn that ends with nothing left to do (all completed) clears it automatically, so each new request starts with a fresh list.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"todos": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"content": map[string]any{"type": "string", "description": "The task, stated as a clear instruction or outcome."},
							"status":  map[string]any{"type": "string", "description": "pending, in_progress, or completed."},
						},
						"required": []string{"content", "status"},
					},
					"description": "The complete new task list, in the order you want it kept. An empty list clears it.",
				},
			},
			"required": []string{"todos"},
		},
	}
}

func (t *TodoWrite) Run(_ context.Context, args json.RawMessage) (tools.Result, error) {
	var in struct {
		Todos []agent.TaskItem `json:"todos"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return tools.Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	if err := validate(in.Todos); err != nil {
		return tools.Result{}, err
	}
	if t.store == nil {
		return tools.Result{}, fmt.Errorf("the tasks module is not available in this session")
	}
	if err := t.store.Write(toStoreItems(in.Todos)); err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Content: Ack(in.Todos)}, nil
}

// TodoRead returns the current task list. Safe and read-only.
type TodoRead struct {
	store tools.TaskStore
}

func (t *TodoRead) Safe() bool { return true }

// SetStore wires the per-session bridge (the server calls this through
// the registry's SetTaskStore).
func (t *TodoRead) SetStore(s tools.TaskStore) { t.store = s }

func (t *TodoRead) Def() provider.ToolDef {
	return provider.ToolDef{
		Name:        "todo_read",
		Description: "Return the current task list as lines like [pending] content, [in_progress] content, [completed] content, or 'No tasks yet' when the list is empty. Read it when you need to recall the steps ahead or check what is left.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

func (t *TodoRead) Run(_ context.Context, _ json.RawMessage) (tools.Result, error) {
	if t.store == nil {
		return tools.Result{Content: "No tasks yet"}, nil
	}
	return tools.Result{Content: Render(fromStoreItems(t.store.Read()))}, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
