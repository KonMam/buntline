import { describe, expect, it } from 'vitest';
import { formatDuration, formatTokens, prettyArgs } from './format';

describe('formatTokens', () => {
  it('passes small numbers through', () => {
    expect(formatTokens(0)).toBe('0');
    expect(formatTokens(999)).toBe('999');
  });
  it('abbreviates thousands', () => {
    expect(formatTokens(1500)).toBe('1.5k');
    expect(formatTokens(3000)).toBe('3k');
    expect(formatTokens(250_000)).toBe('250k');
  });
});

describe('formatDuration', () => {
  it('formats sub-second as ms', () => {
    expect(formatDuration(280)).toBe('280ms');
  });
  it('formats seconds', () => {
    expect(formatDuration(19_600)).toBe('19.6s');
  });
  it('formats minutes', () => {
    expect(formatDuration(93_000)).toBe('1m33s');
  });
});

describe('prettyArgs', () => {
  it('flattens JSON args to key: value pairs', () => {
    expect(prettyArgs('{"path":"main.go"}')).toBe('path: main.go');
  });
  it('elides long values', () => {
    const long = JSON.stringify({ content: 'x'.repeat(200) });
    const out = prettyArgs(long);
    expect(out.length).toBeLessThanOrEqual(120);
    expect(out).toContain('…');
  });
  it('tolerates malformed JSON', () => {
    expect(prettyArgs('{oops')).toBe('{oops');
  });
});
