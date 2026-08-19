import { describe, expect, it } from 'vitest';
import { foldBgEvents } from './background';
import type { AgentEvent } from './types';

const at = (s: number) => new Date(1_700_000_000_000 + s * 1000).toISOString();

const toolBg = (id: string, time = 0): AgentEvent => ({
  type: 'tool_bg',
  tool_id: id,
  time: at(time),
});
const toolEnd = (id: string, time = 0): AgentEvent => ({
  type: 'tool_end',
  tool_id: id,
  time: at(time),
});
const turnStart = (time = 0): AgentEvent => ({ type: 'turn_start', time: at(time) });
const turnEnd = (time = 0): AgentEvent => ({ type: 'turn_end', time: at(time) });

describe('foldBgEvents', () => {
  it('returns an empty set with no events', () => {
    expect(foldBgEvents([])).toEqual(new Set());
  });

  it('tracks a backgrounded call until its tool_end', () => {
    const events = [toolBg('c1'), toolEnd('c1')];
    expect(foldBgEvents(events)).toEqual(new Set());
  });

  it('keeps a backgrounded call without a result', () => {
    expect(foldBgEvents([toolBg('c1')])).toEqual(new Set(['c1']));
  });

  it('clears backgrounded calls at the next turn boundary (stale result dropped server-side)', () => {
    // Turn one backgrounds c1 and ends with it still running; the stale
    // result is dropped, so no tool_end arrives. Turn two starts: c1
    // must not render as running.
    const events = [turnStart(0), toolBg('c1', 1), turnEnd(2), turnStart(3)];
    expect(foldBgEvents(events)).toEqual(new Set());
  });

  it('lets a backgrounded call that outlives a turn boundary become running again', () => {
    const events = [turnStart(0), toolBg('c1', 1), turnEnd(2), toolBg('c1', 3)];
    expect(foldBgEvents(events)).toEqual(new Set(['c1']));
  });
});
