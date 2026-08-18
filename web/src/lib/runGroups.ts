import type { Message } from './types';

// Folding consecutive tool activity into one card is transcript shape,
// not component state, so it lives here where tests can reach it: the
// rule shipped broken twice while it was buried in Chat.svelte.
//
// A run gathers tool-call messages, their results, and thinking-only
// messages, and folds once it holds enough calls across enough
// messages. Visible text breaks a run (prose is the deliverable), but a
// message carrying BOTH text and tool calls is split: the text renders
// in place and its calls seed the run that follows, so a narrated
// command folds with the commands after it.
//
// Every item carries a unique key. A split message yields two items from
// one index, so the index alone is not unique, and a duplicate key
// aborts the whole {#each} and blanks the thread.

export const RUN_MIN_CALLS = 2;
export const RUN_MIN_MESSAGES = 2;

export interface RunMember {
  msg: Message;
  index: number;
  key: string;
}

export type RenderItem =
  | { kind: 'msg'; msg: Message; index: number; key: string }
  | { kind: 'run'; members: RunMember[]; key: string };

// buildRenderItems folds a message window into render items. `offset` is
// the index of the first message in the full transcript (the window may
// hide earlier ones), and `busy` suppresses folding of the trailing run
// so a running turn stays watchable and collapses once it settles.
export function buildRenderItems(messages: Message[], offset: number, busy: boolean): RenderItem[] {
  const items: RenderItem[] = [];
  let run: RunMember[] = [];

  const flush = (tail: boolean) => {
    if (run.length === 0) return;
    const callMsgs = run.filter((m) => m.msg.tool_calls?.length);
    const calls = callMsgs.reduce((n, m) => n + (m.msg.tool_calls?.length ?? 0), 0);
    if (calls >= RUN_MIN_CALLS && callMsgs.length >= RUN_MIN_MESSAGES && !(tail && busy)) {
      items.push({ kind: 'run', members: run, key: `run-${run[0].key}` });
    } else {
      for (const m of run) items.push({ kind: 'msg', ...m });
    }
    run = [];
  };

  messages.forEach((msg, i) => {
    const index = i + offset;
    const key = String(index);
    const isInstructions = msg.kind === 'instructions';

    if (msg.role === 'tool') {
      run.push({ msg, index, key });
      return;
    }
    if (msg.role === 'assistant' && !isInstructions) {
      const hasCalls = (msg.tool_calls?.length ?? 0) > 0;
      if (!msg.content) {
        // Tool calls, thinking, or both: process, not deliverable.
        run.push({ msg, index, key });
        return;
      }
      if (hasCalls) {
        flush(false);
        items.push({
          kind: 'msg',
          msg: { ...msg, tool_calls: undefined },
          index,
          key: `${key}-text`,
        });
        run.push({
          msg: { ...msg, content: '', thinking: '' },
          index,
          key: `${key}-calls`,
        });
        return;
      }
    }
    flush(false);
    items.push({ kind: 'msg', msg, index, key });
  });
  flush(true);
  return items;
}
