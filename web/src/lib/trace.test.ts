import { describe, expect, it } from 'vitest';
import { buildTrace, waterfall } from './trace';
import type { AgentEvent } from './types';

const at = (s: number) => new Date(1_700_000_000_000 + s * 1000).toISOString();

const events: AgentEvent[] = [
  { type: 'turn_start', time: at(0), turn_id: 't1' },
  { type: 'model_start', time: at(0), turn_id: 't1', round: 0 },
  {
    type: 'usage',
    time: at(5),
    turn_id: 't1',
    round: 0,
    usage: { prompt_tokens: 100, completion_tokens: 20, cached_tokens: 80 },
  },
  {
    type: 'tool_start',
    time: at(5),
    turn_id: 't1',
    tool_id: 'c1',
    tool_name: 'bash',
    tool_args: '{"command":"ls"}',
  },
  { type: 'tool_end', time: at(7), turn_id: 't1', tool_id: 'c1', result: 'ok', diff: '' },
  { type: 'model_start', time: at(7), turn_id: 't1', round: 1 },
  {
    type: 'usage',
    time: at(10),
    turn_id: 't1',
    round: 1,
    usage: { prompt_tokens: 150, completion_tokens: 30, cached_tokens: 100 },
  },
  { type: 'turn_end', time: at(10), turn_id: 't1', stop_reason: 'done' },
];

describe('buildTrace', () => {
  it('groups events into one turn with spans and totals', () => {
    const turns = buildTrace(events);
    expect(turns).toHaveLength(1);
    const t = turns[0];
    expect(t.stopReason).toBe('done');
    expect(t.open).toBe(false);
    expect(t.spans.map((s) => s.kind)).toEqual(['model', 'tool', 'model']);
    expect(t.input).toBe(250);
    expect(t.output).toBe(50);
    expect(t.cached).toBe(180);
  });

  it('closes spans with real durations', () => {
    const t = buildTrace(events)[0];
    const [m1, tool, m2] = t.spans;
    expect(m1.end - m1.start).toBe(5000);
    expect(tool.end - tool.start).toBe(2000);
    expect(m2.end - m2.start).toBe(3000);
    expect(tool.result).toBe('ok');
  });

  it('tolerates usage without token counts', () => {
    const turns = buildTrace([
      { type: 'turn_start', time: at(0), turn_id: 'x' },
      { type: 'model_start', time: at(0), turn_id: 'x', round: 0 },
      { type: 'usage', time: at(2), turn_id: 'x', round: 0, usage: null },
      { type: 'turn_end', time: at(2), turn_id: 'x', stop_reason: 'done' },
    ]);
    expect(turns[0].spans[0].end - turns[0].spans[0].start).toBe(2000);
    expect(turns[0].input).toBe(0);
  });

  it('groups pre-trace events under one fallback turn', () => {
    const turns = buildTrace([
      { type: 'tool_start', time: at(0), tool_id: 'c', tool_name: 'grep' },
      { type: 'tool_end', time: at(1), tool_id: 'c', result: 'x' },
    ]);
    expect(turns).toHaveLength(1);
    expect(turns[0].id).toBe('earlier');
  });

  it('renders a question span with the answer as its decision', () => {
    const turns = buildTrace([
      { type: 'turn_start', time: at(0), turn_id: 'q1' },
      {
        type: 'question_request',
        time: at(0),
        turn_id: 'q1',
        approval_id: 'qq1',
        question: 'Plan A or plan B?',
        options: ['plan A', 'plan B'],
      },
      { type: 'question_result', time: at(3), turn_id: 'q1', approval_id: 'qq1', answer: 'plan B' },
      { type: 'turn_end', time: at(3), turn_id: 'q1', stop_reason: 'done' },
    ]);
    expect(turns).toHaveLength(1);
    const span = turns[0].spans[0];
    expect(span.kind).toBe('question');
    expect(span.detail).toBe('Plan A or plan B?');
    expect(span.decision).toBe('plan B');
    expect(span.end - span.start).toBe(3000);
  });
});

describe('waterfall', () => {
  it('maps spans onto the turn timeline as fractions', () => {
    const t = buildTrace(events)[0];
    const segs = waterfall(t);
    expect(segs).toHaveLength(3);
    expect(segs[0].left).toBeCloseTo(0);
    expect(segs[0].width).toBeCloseTo(0.5);
    expect(segs[1].left).toBeCloseTo(0.5);
    expect(segs[2].width).toBeCloseTo(0.3);
  });
});

describe('tasks events in the trace', () => {
  it('does not shape the trace; the strip folds them separately', () => {
    const evs: AgentEvent[] = [
      { type: 'turn_start', time: at(0), turn_id: 't1' },
      {
        type: 'tasks',
        time: at(1),
        turn_id: 't1',
        tasks: [{ content: 'a', status: 'pending' }],
      },
      { type: 'turn_end', time: at(2), turn_id: 't1', stop_reason: 'done' },
    ];
    const turns = buildTrace(evs);
    expect(turns).toHaveLength(1);
    expect(turns[0].spans).toEqual([]);
  });
});
