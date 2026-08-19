import { describe, expect, it } from 'vitest';
import { queuedLanded } from './queued';

describe('queuedLanded', () => {
  it('matches the plain case: transcript content equals the queued text', () => {
    expect(queuedLanded('fix the tests', 'fix the tests')).toBe(true);
  });

  it('matches when the server inlined attachment contents into the message', () => {
    const content = 'read this\n\nContents of /repo/README.md:\n```\n# readme\n```';
    expect(queuedLanded(content, 'read this')).toBe(true);
  });

  it('matches when several attachments were inlined', () => {
    const content =
      'check these\n\nContents of /repo/a.go:\n```\na\n```\n\nContents of /repo/b.go:\n```\nb\n```';
    expect(queuedLanded(content, 'check these')).toBe(true);
  });

  it('does not match a message that merely shares a prefix without the expansion', () => {
    expect(queuedLanded('fix the tests now please', 'fix the tests')).toBe(false);
  });

  it('does not match a transcript message shorter than the queued text', () => {
    expect(queuedLanded('fix', 'fix the tests')).toBe(false);
  });

  it('does not match an empty queued text against a real message', () => {
    expect(queuedLanded('hello', '')).toBe(false);
  });
});
