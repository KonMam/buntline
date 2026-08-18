import { describe, expect, it } from 'vitest';
import { latestRequest } from './latestRequest';

describe('latestRequest', () => {
  it('accepts the latest request and rejects earlier ones', () => {
    const g = latestRequest();
    const first = g.seq();
    const second = g.seq();
    expect(g.current(second)).toBe(true);
    expect(g.current(first)).toBe(false);
  });

  it('rejects a completion from before the most recent request', () => {
    const g = latestRequest();
    const a = g.seq();
    g.seq(); // a newer request starts while a is in flight
    expect(g.current(a)).toBe(false);
  });

  it('starts from the first request', () => {
    const g = latestRequest();
    expect(g.current(g.seq())).toBe(true);
  });
});
