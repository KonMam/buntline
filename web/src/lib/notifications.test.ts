import { describe, expect, it } from 'vitest';
import {
  classifyEvent,
  dedupKey,
  defaultSettings,
  loadSettings,
  resolutionOf,
  saveSettings,
  shouldNotifyOS,
  type NotifySettings,
} from './notifications.svelte';

// A minimal storage for loadSettings/saveSettings tests.
function memoryStorage(): {
  getItem: (k: string) => string | null;
  setItem: (k: string, v: string) => void;
} {
  const m = new Map<string, string>();
  return {
    getItem: (k) => m.get(k) ?? null,
    setItem: (k, v) => void m.set(k, v),
  };
}

const event = (partial: Record<string, unknown> = {}) =>
  ({ type: 'turn_end', time: '2026-01-01T00:00:00Z', ...partial }) as never;

describe('classifyEvent', () => {
  it('classifies an approval request with the tool name and args', () => {
    const c = classifyEvent(
      event({ type: 'approval_request', tool_name: 'bash', tool_args: '{"command":"rm -rf x"}' }),
      'repo',
    )!;
    expect(c.kind).toBe('approval');
    expect(c.title).toBe('Approval needed');
    expect(c.body).toContain('bash');
    expect(c.body).toContain('rm -rf x');
  });

  it('classifies a question request with the question text', () => {
    const c = classifyEvent(event({ type: 'question_request', question: 'Deploy now?' }), 'repo')!;
    expect(c.kind).toBe('question');
    expect(c.body).toBe('Deploy now?');
  });

  it('classifies a parent turn end, skipping subagent turns', () => {
    expect(classifyEvent(event({ type: 'turn_end', stop_reason: 'done' }), 'repo')!.kind).toBe(
      'turnEnd',
    );
    expect(classifyEvent(event({ type: 'turn_end', parent_id: 'call-1' }), 'repo')).toBeNull();
  });

  it('classifies an error with its message', () => {
    const c = classifyEvent(event({ type: 'error', error: 'boom' }), 'repo')!;
    expect(c.kind).toBe('error');
    expect(c.body).toBe('boom');
  });

  it('returns null for events nobody needs to see', () => {
    for (const t of ['text_delta', 'tool_start', 'usage', 'message', 'tasks']) {
      expect(classifyEvent(event({ type: t }), 'repo')).toBeNull();
    }
  });
});

describe('dedupKey', () => {
  it('keys approvals and questions by approval id, and results resolve them', () => {
    expect(dedupKey(event({ type: 'approval_request', approval_id: 'a1' }), 's1')).toBe(
      'approval:a1',
    );
    expect(dedupKey(event({ type: 'approval_result', approval_id: 'a1' }), 's1')).toBe(
      'approval:a1',
    );
    expect(dedupKey(event({ type: 'question_request', approval_id: 'q1' }), 's1')).toBe(
      'question:q1',
    );
    expect(dedupKey(event({ type: 'question_result', approval_id: 'q1' }), 's1')).toBe(
      'question:q1',
    );
  });

  it('skips subagent turn ends and keyless events', () => {
    expect(dedupKey(event({ type: 'turn_end', parent_id: 'call-1' }), 's1')).toBeNull();
    expect(dedupKey(event({ type: 'approval_request' }), 's1')).toBeNull();
    expect(dedupKey(event({ type: 'tool_end' }), 's1')).toBeNull();
  });

  it('coalesces repeated identical errors per session', () => {
    const a = dedupKey(event({ type: 'error', error: 'boom' }), 's1');
    const b = dedupKey(event({ type: 'error', error: 'boom' }), 's1');
    const c = dedupKey(event({ type: 'error', error: 'boom' }), 's2');
    expect(a).toBe(b);
    expect(a).not.toBe(c);
  });
});

describe('resolutionOf', () => {
  it('maps result events to the kind they resolve', () => {
    expect(resolutionOf(event({ type: 'approval_result' }))).toBe('approval');
    expect(resolutionOf(event({ type: 'question_result' }))).toBe('question');
    expect(resolutionOf(event({ type: 'turn_end' }))).toBeNull();
  });
});

describe('shouldNotifyOS', () => {
  const on = (): NotifySettings => ({ ...defaultSettings() });

  it('requires the master switch, os switch, and permission', () => {
    const s = on();
    const decision = { kind: 'approval' as const, sessionId: 's1', visible: false };
    expect(shouldNotifyOS(s, decision, 'denied')).toBe(false);
    expect(shouldNotifyOS({ ...s, os: false }, decision, 'granted')).toBe(false);
    expect(shouldNotifyOS({ ...s, enabled: false }, decision, 'granted')).toBe(false);
    expect(shouldNotifyOS(s, decision, 'granted')).toBe(true);
  });

  it('honors per-kind toggles', () => {
    const s = { ...on(), turnEnd: false };
    expect(shouldNotifyOS(s, { kind: 'turnEnd', sessionId: 's1', visible: false }, 'granted')).toBe(
      false,
    );
  });

  it('stays silent when the user is looking at the active session, unless whileActive', () => {
    const s = on();
    const visible = { kind: 'approval' as const, sessionId: 's1', visible: true };
    expect(shouldNotifyOS(s, visible, 'granted')).toBe(false);
    expect(shouldNotifyOS({ ...s, whileActive: true }, visible, 'granted')).toBe(true);
  });
});

describe('settings persistence', () => {
  it('defaults when nothing is stored', () => {
    const storage = memoryStorage();
    expect(loadSettings(storage)).toEqual(defaultSettings());
  });

  it('round-trips through storage', () => {
    const storage = memoryStorage();
    const s = { ...defaultSettings(), turnEnd: true, os: false };
    saveSettings(s, storage);
    expect(loadSettings(storage)).toEqual(s);
  });

  it('merges partial saved settings over defaults (forward compatibility)', () => {
    const storage = memoryStorage();
    storage.setItem('buntline.notify.settings', JSON.stringify({ turnEnd: true }));
    const loaded = loadSettings(storage);
    expect(loaded.turnEnd).toBe(true);
    expect(loaded.approval).toBe(defaultSettings().approval);
  });

  it('falls back to defaults on corrupt data', () => {
    const storage = memoryStorage();
    storage.setItem('buntline.notify.settings', '{not json');
    expect(loadSettings(storage)).toEqual(defaultSettings());
  });
});
