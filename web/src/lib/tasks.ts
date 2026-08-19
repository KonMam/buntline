// Client-side fold of the tasks module's event stream: the current task
// list is the most recent `tasks` event (last-write-wins), exactly like
// the Go module's fold on the server. The strip renders this; the same
// fold feeds tests, so the rule cannot drift between the trace and the
// panel.
import type { AgentEvent, TaskItem } from './types';

// foldTasks reduces the activity stream to the current task list. Every
// tasks event replaces the list. An empty list clears, and the Go
// bridge writes that as an event with no `tasks` field at all
// (omitempty drops the key), so a missing or non-array payload means
// "cleared", not "unchanged". Events before the stream's window fold
// the same way the server's fold would: later writes win, so a
// truncated window still shows the latest list.
export function foldTasks(events: AgentEvent[]): TaskItem[] {
  let tasks: TaskItem[] = [];
  for (const ev of events) {
    if (ev.type === 'tasks') {
      tasks = Array.isArray(ev.tasks) ? ev.tasks : [];
    }
  }
  return tasks;
}
