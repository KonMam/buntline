import { describe, expect, it } from 'vitest';
import { shouldCollapse, COLLAPSE_THRESHOLD } from './toolCalls';
import type { ToolCall } from './types';

const call = (id: string): ToolCall => ({ id, name: 'read_file', args: '{}' });

describe('shouldCollapse', () => {
  it('collapses three or more calls in one message', () => {
    expect(shouldCollapse([call('a'), call('b'), call('c')])).toBe(true);
    expect(
      shouldCollapse(Array.from({ length: COLLAPSE_THRESHOLD }, (_, i) => call(String(i)))),
    ).toBe(true);
  });

  it('keeps one or two calls individual', () => {
    expect(shouldCollapse([])).toBe(false);
    expect(shouldCollapse([call('a')])).toBe(false);
    expect(shouldCollapse([call('a'), call('b')])).toBe(false);
  });

  it('handles an absent tool_calls array', () => {
    expect(shouldCollapse(undefined)).toBe(false);
  });
});
