import { describe, expect, it } from 'vitest';
import { buildTrace } from './trace';
import type { AgentEvent } from './types';

const at = (s: number) => new Date(1_700_000_000_000 + s * 1000).toISOString();

describe('subagent nesting', () => {
  it('nests parent_id events under the spawning tool span', () => {
    const events: AgentEvent[] = [
      { type: 'turn_start', time: at(0), turn_id: 'parent' },
      { type: 'model_start', time: at(0), turn_id: 'parent', round: 0 },
      { type: 'usage', time: at(2), turn_id: 'parent', round: 0, usage: null },
      {
        type: 'tool_start',
        time: at(2),
        turn_id: 'parent',
        tool_id: 'spawn1',
        tool_name: 'spawn_agent',
        tool_args: '{"task":"explore"}',
      },
      // Child events: own turn id, parent_id pointing at the call.
      { type: 'turn_start', time: at(2), turn_id: 'child', parent_id: 'spawn1' },
      { type: 'model_start', time: at(2), turn_id: 'child', parent_id: 'spawn1', round: 0 },
      {
        type: 'tool_start',
        time: at(3),
        turn_id: 'child',
        parent_id: 'spawn1',
        tool_id: 'c1',
        tool_name: 'grep',
        tool_args: '{"pattern":"x"}',
      },
      {
        type: 'tool_end',
        time: at(4),
        turn_id: 'child',
        parent_id: 'spawn1',
        tool_id: 'c1',
        result: 'match',
      },
      {
        type: 'usage',
        time: at(5),
        turn_id: 'child',
        parent_id: 'spawn1',
        round: 0,
        usage: { prompt_tokens: 50, completion_tokens: 10, cached_tokens: 0 },
      },
      { type: 'turn_end', time: at(5), turn_id: 'child', parent_id: 'spawn1', stop_reason: 'done' },
      { type: 'tool_end', time: at(5), turn_id: 'parent', tool_id: 'spawn1', result: 'report' },
      { type: 'turn_end', time: at(6), turn_id: 'parent', stop_reason: 'done' },
    ];

    const turns = buildTrace(events);
    expect(turns).toHaveLength(1); // the child forms no top-level turn

    const spawn = turns[0].spans.find((s) => s.label === 'spawn_agent');
    expect(spawn).toBeDefined();
    expect(spawn!.result).toBe('report');
    expect(spawn!.children).toBeDefined();

    const kinds = spawn!.children!.map((s) => `${s.kind}:${s.label}`);
    expect(kinds).toContain('model:model call 1');
    expect(kinds).toContain('tool:grep');
    const grep = spawn!.children!.find((s) => s.label === 'grep')!;
    expect(grep.end - grep.start).toBe(1000);
  });
});
