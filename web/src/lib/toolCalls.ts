import type { ToolCall } from './types';

// Collapse a single assistant message's tool calls into one card when
// there are enough of them. The collapse never spans messages: each
// assistant message is one discrete model call, and the thinking or
// reasoning between model calls must stay visible. The threshold exists
// so one or two calls keep rendering as individual cards (they cost
// nothing and often matter: a lone bash call with a failing result)
// while a burst of three or more reads as one unit.
export const COLLAPSE_THRESHOLD = 3;

export function shouldCollapse(calls: ToolCall[] | undefined): boolean {
  return (calls?.length ?? 0) >= COLLAPSE_THRESHOLD;
}
