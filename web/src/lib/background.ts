// Client-side fold of the activity stream into the set of tool calls
// still running in the background. Mirrors the agent's own accounting:
// a tool_bg marks the start (the call outlived its grace period), and
// the matching tool_end clears it. Cards render these calls as
// "running in the background" until their real result lands.
import type { AgentEvent } from './types';

// foldBgEvents reduces the activity stream to the backgrounded call ids
// that are still live. The subtle part is the turn boundary: when a turn
// ends with a backgrounded tool still running (cancelled, errored, round
// cap, or the loop moved on), the server drops the stale result without
// ever emitting a tool_end — `deliverBgResults` discards results stamped
// for a non-current turn, and RunMessages clears the board at every turn
// start. Without a boundary reset the fold would keep that id forever,
// stranding a "running in the background" card on an old turn. A turn
// boundary clears the set: by definition no backgrounded tool outlives
// its turn, so anything still in the set is dead.
export function foldBgEvents(events: AgentEvent[]): Set<string> {
  const bg = new Set<string>();
  for (const ev of events) {
    if (ev.type === 'turn_start' || ev.type === 'turn_end') {
      bg.clear();
    } else if (ev.type === 'tool_bg' && ev.tool_id) {
      bg.add(ev.tool_id);
    } else if (ev.type === 'tool_end' && ev.tool_id) {
      bg.delete(ev.tool_id);
    }
  }
  return bg;
}
