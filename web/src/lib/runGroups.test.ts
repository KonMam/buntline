import { describe, expect, it } from 'vitest';
import { buildRenderItems } from './runGroups';
import type { Message, ToolCall } from './types';

const call = (id: string, name = 'bash'): ToolCall => ({ id, name, args: '{}' });

const calls = (...ids: string[]): Message => ({
  role: 'assistant',
  content: '',
  tool_calls: ids.map((id) => call(id)),
});

const result = (id: string): Message => ({ role: 'tool', content: 'ok', tool_call_id: id });
const text = (content: string): Message => ({ role: 'assistant', content });
const thinking = (): Message => ({ role: 'assistant', content: '', thinking: 'hmm' });
const user = (content: string): Message => ({ role: 'user', content });

const keys = (items: ReturnType<typeof buildRenderItems>) => items.map((i) => i.key);

describe('buildRenderItems', () => {
  it('keeps keys unique when a message splits into text and calls', () => {
    // The bug that blanked the thread: one index, two items.
    const items = buildRenderItems(
      [text('Now the Go side:'), calls('a'), result('a'), calls('b'), result('b')],
      0,
      false,
    );
    const ks = keys(items);
    expect(new Set(ks).size).toBe(ks.length);
  });

  it('never emits a duplicate key across a mixed transcript', () => {
    const msgs: Message[] = [
      user('go'),
      { role: 'assistant', content: 'narrating', tool_calls: [call('x')] },
      result('x'),
      calls('y'),
      result('y'),
      text('done'),
      { role: 'assistant', content: 'more', tool_calls: [call('z')] },
      result('z'),
    ];
    const ks = keys(buildRenderItems(msgs, 0, false));
    expect(new Set(ks).size).toBe(ks.length);
  });

  it('folds two calls across two messages', () => {
    const items = buildRenderItems([calls('a'), result('a'), calls('b'), result('b')], 0, false);
    expect(items).toHaveLength(1);
    expect(items[0].kind).toBe('run');
  });

  it('leaves a single call ungrouped', () => {
    const items = buildRenderItems([calls('a'), result('a')], 0, false);
    expect(items.every((i) => i.kind === 'msg')).toBe(true);
  });

  it('does not fold parallel calls from one message', () => {
    // One message with many calls is the per-message fold's job.
    const items = buildRenderItems([calls('a', 'b', 'c'), result('a')], 0, false);
    expect(items.every((i) => i.kind === 'msg')).toBe(true);
  });

  it('a narrating message contributes its calls to the following run', () => {
    const items = buildRenderItems(
      [
        { role: 'assistant', content: 'Now the Go side:', tool_calls: [call('a')] },
        result('a'),
        calls('b'),
        result('b'),
      ],
      0,
      false,
    );
    expect(items.map((i) => i.kind)).toEqual(['msg', 'run']);
    const [msg, run] = items;
    if (msg.kind !== 'msg' || run.kind !== 'run') throw new Error('shape');
    expect(msg.msg.content).toBe('Now the Go side:');
    expect(msg.msg.tool_calls).toBeUndefined(); // calls moved into the run
    expect(run.members.some((m) => m.msg.tool_calls?.[0]?.id === 'a')).toBe(true);
  });

  it('thinking-only messages ride inside a run instead of splitting it', () => {
    const items = buildRenderItems(
      [calls('a'), result('a'), thinking(), calls('b'), result('b')],
      0,
      false,
    );
    expect(items).toHaveLength(1);
    expect(items[0].kind).toBe('run');
  });

  it('visible text breaks a run', () => {
    const items = buildRenderItems(
      [calls('a'), result('a'), calls('b'), result('b'), text('all gates pass')],
      0,
      false,
    );
    expect(items.map((i) => i.kind)).toEqual(['run', 'msg']);
  });

  it('does not fold the trailing run while the turn is running', () => {
    const msgs = [calls('a'), result('a'), calls('b'), result('b')];
    expect(buildRenderItems(msgs, 0, true).every((i) => i.kind === 'msg')).toBe(true);
    expect(buildRenderItems(msgs, 0, false)[0].kind).toBe('run');
  });

  it('folds settled runs even while busy', () => {
    const items = buildRenderItems(
      [calls('a'), result('a'), calls('b'), result('b'), text('now this'), calls('c')],
      0,
      true,
    );
    expect(items[0].kind).toBe('run'); // settled: text closed it
    expect(items[items.length - 1].kind).toBe('msg'); // live tail stays open
  });

  it('offsets indices by the window start', () => {
    const [item] = buildRenderItems([user('hi')], 40, false);
    if (item.kind !== 'msg') throw new Error('shape');
    expect(item.index).toBe(40);
  });

  it('leaves user and instruction messages alone', () => {
    const instructions: Message = { role: 'user', content: 'project', kind: 'instructions' };
    const items = buildRenderItems([user('hello'), instructions], 0, false);
    expect(items.map((i) => i.kind)).toEqual(['msg', 'msg']);
  });
});
