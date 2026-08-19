// Reactive state for the active session. One EventSource per selected
// session; streaming deltas are coalesced through requestAnimationFrame so
// a fast token stream costs one DOM update per frame, not one per token.
import { api } from './api';
import type {
  AgentEvent,
  Message,
  PendingApproval,
  PendingQuestion,
  SessionMeta,
  SubagentInfo,
  TaskItem,
} from './types';
import { foldTasks } from './tasks';
import { queuedLanded } from './queued';

export class SessionState {
  meta = $state<SessionMeta | null>(null);
  messages = $state<Message[]>([]);
  activity = $state<AgentEvent[]>([]);
  approval = $state<PendingApproval | null>(null);
  question = $state<PendingQuestion | null>(null);
  busy = $state(false);
  runningTool = $state<string | null>(null);
  error = $state<string | null>(null);

  // Streaming buffers for the in-flight assistant message.
  streamText = $state('');
  streamThinking = $state('');

  // Subagents spawned in this session, from the registry endpoint.
  subagents = $state<SubagentInfo[]>([]);

  // Live output for each subagent, keyed by its id (the spawn tool call
  // id / parent_id). Deltas with parent_id route here instead of the main
  // stream; the same rAF coalescing applies.
  subagentText = $state<Record<string, string>>({});
  subagentThinking = $state<Record<string, string>>({});

  // The subagent currently selected in the agents tab.
  selectedSubagent = $state<string | null>(null);

  // Per-subagent buffers for deltas awaiting a rAF flush.
  #pendingParent = new Map<string, { text: string; thinking: string }>();

  // The model's task list, folded from tasks events in the activity
  // stream (last-write-wins, the same fold the Go module does). Live
  // updates arrive as tasks events; reload re-folds from the seeded
  // activity, so the strip never needs its own fetch to stay truthful.
  tasks = $state<TaskItem[]>([]);

  // The system prompt as the server knows it (default or per-session override).
  systemPrompt = $state('');

  // The model's context window as the server knows it: the profile's
  // context_window, or a documented default for known API models.
  contextWindow = $state(0);

  // Text waiting to be placed into the composer (edit-and-resend hands
  // the original message over through here).
  draft = $state('');

  // Queued messages sent while the agent is busy: the server accepted
  // them as steering and they enter the transcript when the loop picks
  // them up. Kept here so the user sees what they sent while it waits;
  // each one is removed when its real message event lands.
  queued = $state<{ text: string; time: string }[]>([]);

  // Session token totals, from usage events.
  totals = $derived.by(() => {
    let input = 0;
    let output = 0;
    let cached = 0;
    for (const e of this.activity) {
      if (e.type === 'usage' && e.usage) {
        input += e.usage.prompt_tokens;
        output += e.usage.completion_tokens;
        cached += e.usage.cached_tokens;
      }
    }
    return { input, output, cached };
  });

  // tool_id to unified diff, for rendering diffs on tool cards in chat.
  diffs = $derived.by(() => {
    const m = new Map<string, string>();
    for (const e of this.activity) {
      if (e.type === 'tool_end' && e.tool_id && e.diff) m.set(e.tool_id, e.diff);
    }
    return m;
  });

  // tool_ids still running in the background: a tool_bg event marks the
  // start (the tool outlived its grace period and moved off the loop),
  // and the matching tool_end (delivered when the loop picks the real
  // result up) clears it. Cards for these calls show a running state
  // until their real result lands.
  bg = $derived.by(() => {
    const s = new Set<string>();
    for (const e of this.activity) {
      if (e.type === 'tool_bg' && e.tool_id) s.add(e.tool_id);
      if (e.type === 'tool_end' && e.tool_id) s.delete(e.tool_id);
    }
    return s;
  });

  // Files the agent changed this session, for badges in the file browser.
  touchedFiles = $derived.by(() => {
    const s = new Set<string>();
    for (const e of this.activity) {
      if (e.type !== 'tool_end' || e.error) continue;
      if (e.tool_name !== 'write_file' && e.tool_name !== 'edit_file') continue;
      const start = this.activity.find((a) => a.type === 'tool_start' && a.tool_id === e.tool_id);
      if (!start?.tool_args) continue;
      try {
        const args = JSON.parse(start.tool_args);
        if (typeof args.path === 'string') s.add(args.path);
      } catch {
        // unparseable args: skip
      }
    }
    return s;
  });

  #source: EventSource | null = null;
  #pendingText = '';
  #pendingThinking = '';
  #flushScheduled = false;

  async load(id: string) {
    this.close();
    const detail = await api.getSession(id);
    this.meta = detail.meta;
    this.messages = detail.messages.filter((m) => m.role !== 'system');
    this.activity = detail.events;
    this.tasks = foldTasks(detail.events);
    this.systemPrompt = detail.system_prompt ?? '';
    this.contextWindow = detail.context_window ?? 0;
    this.approval = null;
    this.question = null;
    this.busy = false;
    this.runningTool = null;
    this.error = null;
    this.draft = '';
    this.queued = [];
    this.subagents = [];
    this.subagentText = {};
    this.subagentThinking = {};
    this.selectedSubagent = null;
    // Reloading mid-stream: seed with what already streamed, so refresh
    // never loses text. Deltas after reconnect append seamlessly. A card
    // waiting on the user (approval or question) re-opens the same way.
    this.streamText = detail.partial?.text ?? '';
    this.streamThinking = detail.partial?.thinking ?? '';
    if (detail.pending_approval?.approval_id) {
      this.approval = {
        id: detail.pending_approval.approval_id,
        tool_name: detail.pending_approval.tool_name ?? '',
        tool_args: detail.pending_approval.tool_args ?? '',
      };
      this.busy = true;
    }
    if (detail.pending_question?.approval_id) {
      this.question = {
        id: detail.pending_question.approval_id,
        question: detail.pending_question.question ?? '',
        options: detail.pending_question.options ?? [],
      };
      this.busy = true;
    }
    if (this.streamText || this.streamThinking) this.busy = true;
    void this.refreshSubagents();

    this.#source = new EventSource(`/api/sessions/${id}/events`);
    this.#source.onmessage = (raw) => {
      const ev: AgentEvent = JSON.parse(raw.data);
      this.#handle(ev);
    };
  }

  close() {
    this.#source?.close();
    this.#source = null;
  }

  #handle(ev: AgentEvent) {
    // Fold tasks events before the switch so the strip updates the
    // moment a todo_write lands, whether it was dispatched by the loop
    // or by the module's bridge.
    if (ev.type === 'tasks' && Array.isArray(ev.tasks)) {
      this.tasks = ev.tasks;
    }
    switch (ev.type) {
      case 'text_delta':
        if (ev.parent_id) {
          const buf = this.#pendingParent.get(ev.parent_id) ?? { text: '', thinking: '' };
          buf.text += ev.text ?? '';
          this.#pendingParent.set(ev.parent_id, buf);
          this.#scheduleFlush();
        } else {
          // Subagent deltas belong to their own buffer; the main stream
          // only takes the parent's.
          this.#pendingText += ev.text ?? '';
          this.#scheduleFlush();
        }
        return; // deltas don't enter the activity log
      case 'thinking_delta':
        if (ev.parent_id) {
          const buf = this.#pendingParent.get(ev.parent_id) ?? { text: '', thinking: '' };
          buf.thinking += ev.text ?? '';
          this.#pendingParent.set(ev.parent_id, buf);
          this.#scheduleFlush();
        } else {
          this.#pendingThinking += ev.text ?? '';
          this.#scheduleFlush();
        }
        return;
      case 'message':
        this.#flushNow();
        if (ev.message && ev.message.role !== 'system') {
          this.messages.push(ev.message);
        }
        if (ev.message?.role === 'assistant' && !ev.parent_id) {
          this.streamText = '';
          this.streamThinking = '';
          this.#pendingText = '';
          this.#pendingThinking = '';
        }
        // A queued steering message landed in the transcript: drop the
        // matching pending bubble so the thread shows one copy, not two.
        // Content matching, not identity: the server may have expanded
        // the message (attachment contents inlined), and the loop can
        // deliver several queued messages in one drain, so the first
        // queued entry matching the event's content is the one that
        // landed.
        if (ev.message?.role === 'user' && !ev.parent_id) {
          const i = this.queued.findIndex((q) => queuedLanded(ev.message!.content, q.text));
          if (i >= 0) this.queued.splice(i, 1);
        }
        break;
      case 'turn_start':
        this.busy = true;
        this.error = null;
        break;
      case 'turn_end':
        this.busy = false;
        this.runningTool = null;
        this.approval = null;
        this.question = null;
        break;
      case 'error':
        this.error = ev.error ?? 'unknown error';
        break;
      case 'tool_start':
        // Subagent activity gets a visible prefix so "what is it doing
        // right now" always has an answer.
        this.runningTool = ev.parent_id ? `subagent · ${ev.tool_name}` : (ev.tool_name ?? null);
        break;
      case 'tool_bg':
        // A long tool moved off the loop; the turn keeps working around
        // it. The running state stays on the tool's card, so the session
        // row shows the loop's own work, not the backgrounded command.
        break;
      case 'tool_end':
        this.runningTool = ev.parent_id ? 'subagent working' : null;
        break;
      case 'approval_request':
        this.approval = {
          id: ev.approval_id!,
          tool_name: ev.tool_name ?? '',
          tool_args: ev.tool_args ?? '',
        };
        break;
      case 'approval_result':
        this.approval = null;
        break;
      case 'question_request':
        this.question = {
          id: ev.approval_id!,
          question: ev.question ?? '',
          options: ev.options ?? [],
        };
        break;
      case 'question_result':
        this.question = null;
        break;
      case 'compact':
        // Transcript was rewritten server-side; reload to stay truthful.
        if (this.meta) void this.load(this.meta.id);
        break;
    }
    this.activity.push(ev);
  }

  #scheduleFlush() {
    if (this.#flushScheduled) return;
    this.#flushScheduled = true;
    requestAnimationFrame(() => this.#flushNow());
  }

  #flushNow() {
    this.#flushScheduled = false;
    if (this.#pendingText) {
      this.streamText += this.#pendingText;
      this.#pendingText = '';
    }
    if (this.#pendingThinking) {
      this.streamThinking += this.#pendingThinking;
      this.#pendingThinking = '';
    }
    if (this.#pendingParent.size > 0) {
      for (const [id, buf] of this.#pendingParent) {
        if (buf.text) {
          this.subagentText = {
            ...this.subagentText,
            [id]: (this.subagentText[id] ?? '') + buf.text,
          };
        }
        if (buf.thinking) {
          this.subagentThinking = {
            ...this.subagentThinking,
            [id]: (this.subagentThinking[id] ?? '') + buf.thinking,
          };
        }
      }
      this.#pendingParent.clear();
    }
  }

  async send(content: string, images?: string[], attachments?: string[]) {
    if (!this.meta) return;
    try {
      const res = await api.sendMessage(this.meta.id, content, images, attachments);
      // 202 Accepted: the agent is busy, the message is queued as
      // steering and enters the transcript when the loop picks it up.
      // Show it as pending now so it does not look lost; the message
      // event removes it.
      if (res && res.queued) {
        // The SSE message event and this response race: the loop can
        // pick the steer up (and emit its message event) before the
        // fetch resolves — the event stream is already open while the
        // fetch is a fresh request, so the event often wins. If the
        // message already landed in the transcript, adding the bubble
        // would strand it: the event that would have removed it has
        // already passed.
        const landed = this.messages.some(
          (m) => m.role === 'user' && queuedLanded(m.content, content),
        );
        if (!landed) {
          this.queued = [...this.queued, { text: content, time: new Date().toISOString() }];
        }
      }
    } catch (e) {
      this.error = e instanceof Error ? e.message : String(e);
    }
  }

  async compact() {
    if (!this.meta) return;
    try {
      await api.compact(this.meta.id);
    } catch (e) {
      this.error = e instanceof Error ? e.message : String(e);
    }
  }

  async interrupt() {
    if (!this.meta) return;
    // A queued steering message is pending, not answered: interrupting
    // the turn drops the queue, so the bubble goes with it.
    this.queued = [];
    await api.interrupt(this.meta.id);
  }

  async setMode(mode: string) {
    if (!this.meta) return;
    try {
      this.meta = await api.setMode(this.meta.id, mode);
    } catch (e) {
      this.error = e instanceof Error ? e.message : String(e);
    }
  }

  async setModel(model: string, profile?: string) {
    if (!this.meta) return;
    try {
      const meta = await api.setModel(this.meta.id, model, profile);
      this.meta = meta;
      // The context window follows the model.
      this.contextWindow = meta.context_window ?? 0;
    } catch (e) {
      this.error = e instanceof Error ? e.message : String(e);
    }
  }

  // refreshSubagents reloads the registry list; called on load and
  // whenever the session is busy (children start and end mid-turn).
  async refreshSubagents() {
    if (!this.meta) return;
    try {
      const rows = await api.subagents(this.meta.id);
      // Merge the local live buffers so a list refresh never drops
      // streamed text. Unknown ids (evicted by the cap) drop their buffer.
      const text: Record<string, string> = {};
      const thinking: Record<string, string> = {};
      for (const r of rows) {
        if (this.subagentText[r.id]) text[r.id] = this.subagentText[r.id];
        if (this.subagentThinking[r.id]) thinking[r.id] = this.subagentThinking[r.id];
      }
      this.subagents = rows;
      this.subagentText = text;
      this.subagentThinking = thinking;
    } catch (e) {
      this.error = e instanceof Error ? e.message : String(e);
    }
  }

  // subagentTotals sums usage events tagged with a subagent's parent id.
  subagentTotals(id: string): { input: number; output: number; cached: number } {
    let input = 0;
    let output = 0;
    let cached = 0;
    for (const e of this.activity) {
      if (e.type === 'usage' && e.parent_id === id && e.usage) {
        input += e.usage.prompt_tokens;
        output += e.usage.completion_tokens;
        cached += e.usage.cached_tokens;
      }
    }
    return { input, output, cached };
  }

  async steerSubagent(id: string, content: string) {
    if (!this.meta) return;
    try {
      await api.subagentSteer(this.meta.id, id, content);
    } catch (e) {
      this.error = e instanceof Error ? e.message : String(e);
    }
  }

  async interruptSubagent(id: string) {
    if (!this.meta) return;
    try {
      await api.subagentInterrupt(this.meta.id, id);
    } catch (e) {
      this.error = e instanceof Error ? e.message : String(e);
    }
  }

  async decide(decision: string) {
    if (!this.approval) return;
    const id = this.approval.id;
    this.approval = null;
    try {
      await api.decide(id, decision);
    } catch (e) {
      this.error = e instanceof Error ? e.message : String(e);
    }
  }

  // answerQuestion resolves a pending ask_user question. The card stays
  // until the question_result event confirms the loop picked it up.
  async answerQuestion(text: string) {
    if (!this.question) return;
    const id = this.question.id;
    // Answering resolves the question the loop is waiting on; the answer
    // travels as the question result, so nothing is queued.
    try {
      await api.answerQuestion(id, text);
    } catch (e) {
      this.error = e instanceof Error ? e.message : String(e);
    }
  }
}
