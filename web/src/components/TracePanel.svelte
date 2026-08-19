<script lang="ts">
  import { api } from '../lib/api';
  import { latestRequest } from '../lib/latestRequest';
  import type { SessionState } from '../lib/session.svelte';
  import { buildTrace, waterfall, type Turn, type TraceSpan } from '../lib/trace';
  import { formatDuration, formatTokens, prettyArgs } from '../lib/format';
  import DiffView from './DiffView.svelte';
  import TasksCard from './TasksCard.svelte';

  let {
    session,
    checkpointsEnabled,
    ollamaEnabled,
    tasksEnabled,
  }: {
    session: SessionState;
    checkpointsEnabled: boolean;
    ollamaEnabled: boolean;
    tasksEnabled: boolean;
  } = $props();

  const turns = $derived(buildTrace(session.activity));

  // Context pressure: the latest main-loop prompt size against the
  // model's window. "How full am I" beats a history of growing bars.
  const lastCall = $derived.by(() => {
    for (let i = session.activity.length - 1; i >= 0; i--) {
      const e = session.activity[i];
      if (e.type === 'usage' && !e.parent_id && e.usage) return e.usage;
    }
    return null;
  });
  // The server reports the window for profiles and known API models;
  // for local models, ask ollama.
  let ollamaLength = $state(0);
  $effect(() => {
    const model = session.meta?.model;
    if (!model || !ollamaEnabled || session.contextWindow > 0) {
      ollamaLength = 0;
      return;
    }
    api
      .ollamaContext(model)
      .then((r) => (ollamaLength = r.context_length))
      .catch(() => (ollamaLength = 0));
  });
  const contextLength = $derived(session.contextWindow > 0 ? session.contextWindow : ollamaLength);
  const contextPct = $derived(
    contextLength > 0 && lastCall ? lastCall.prompt_tokens / contextLength : 0,
  );

  // Checkpoint refs: tool_call id → snapshot, refreshed when a turn ends.
  // The refetch is async and can outlive this effect; the request-sequence
  // guard drops a stale response from a previous session or toggle so it
  // can never overwrite a newer one.
  let refs = $state<Record<string, string>>({});
  let restoring = $state<string | null>(null);
  const refsReqs = latestRequest();
  $effect(() => {
    const id = session.meta?.id;
    void session.busy;
    const seq = refsReqs.seq();
    if (!id || !checkpointsEnabled) {
      refs = {};
      return;
    }
    api
      .checkpointRefs(id)
      .then((r) => {
        if (!refsReqs.current(seq)) return;
        refs = r.refs;
      })
      .catch(() => {
        if (!refsReqs.current(seq)) return;
        refs = {};
      });
  });

  async function restore(ref: string) {
    if (!session.meta) return;
    restoring = ref;
    try {
      await api.checkpointRestore(session.meta.id, ref);
    } finally {
      restoring = null;
    }
  }

  // Latest turn stays expanded; older ones collapse.
  let expanded = $state<Set<string>>(new Set());
  function toggle(id: string) {
    const next = new Set(expanded);
    if (!next.delete(id)) next.add(id);
    expanded = next;
  }
  const isOpen = (t: Turn, i: number) => expanded.has(t.id) !== (i === turns.length - 1);

  function turnSummary(t: Turn): string {
    const parts = [formatDuration(Math.max(t.end - t.start, 0))];
    if (t.input > 0) parts.push(`in ${formatTokens(t.input)}`);
    if (t.output > 0) parts.push(`out ${formatTokens(t.output)}`);
    if (t.cached > 0) parts.push(`cached ${formatTokens(t.cached)}`);
    return parts.join(' · ');
  }
</script>

{#snippet spanRow(span: TraceSpan)}
  <details class="span {span.kind}">
    <summary>
      <span class="dot"></span>
      <span class="label">{span.label}</span>
      {#if span.kind === 'tool' && span.detail}
        <span class="detail">{prettyArgs(span.detail, 60)}</span>
      {/if}
      {#if span.kind === 'question' && span.detail}
        <span class="detail">{span.detail}</span>
      {/if}
      <span class="dur">
        {#if span.kind === 'model' && span.usage}
          in {formatTokens(span.usage.prompt_tokens)} · out
          {formatTokens(span.usage.completion_tokens)} ·
        {/if}
        {#if span.kind === 'model' && span.firstTokenMs}
          first {formatDuration(span.firstTokenMs)} ·
        {/if}
        {#if span.decision}{span.decision} ·
        {/if}
        {formatDuration(Math.max(span.end - span.start, 0))}
      </span>
    </summary>
    {#if span.kind === 'model' && span.usage && span.usage.cached_tokens > 0}
      <div class="body meta">
        cached {formatTokens(span.usage.cached_tokens)} of
        {formatTokens(span.usage.prompt_tokens)} prompt tokens
      </div>
    {/if}
    {#if span.notes?.length}
      <div class="body notes">
        {#each span.notes as note, k (k)}
          <div class="note" class:noted-err={!!note.error}>
            <span class="module">{note.module}</span>
            <span class="note-text">{note.error ?? note.text}</span>
          </div>
        {/each}
      </div>
    {/if}
    {#if span.args}
      <pre class="body">{span.args}</pre>
    {/if}
    {#if span.error}
      <div class="body err">{span.error}</div>
    {:else if span.diff}
      <DiffView diff={span.diff} />
    {:else if span.result}
      <pre class="body">{span.result}</pre>
    {/if}
    {#if span.toolId && refs[span.toolId]}
      <div class="body actions">
        <button
          class="restore"
          disabled={restoring !== null || session.busy}
          onclick={() => restore(refs[span.toolId!])}
        >
          {restoring === refs[span.toolId] ? 'restoring' : 'restore files to before this call'}
        </button>
      </div>
    {/if}
    {#if span.children?.length}
      <div class="children">
        {#each span.children as child, k (k)}
          {@render spanRow(child)}
        {/each}
      </div>
    {/if}
  </details>
{/snippet}

<div class="trace">
  {#if tasksEnabled && session.tasks.length > 0}
    <div class="tasks-wrap">
      <TasksCard tasks={session.tasks} />
    </div>
  {/if}

  <div class="totals">
    <span class="totals-label">session</span>
    <span class="nums">
      in {formatTokens(session.totals.input)} · out {formatTokens(session.totals.output)}
      {#if session.totals.cached > 0}
        · cached {formatTokens(session.totals.cached)}{/if}
    </span>
    <button class="compact" onclick={() => session.compact()} disabled={session.busy}>
      compact
    </button>
  </div>

  {#if lastCall}
    <div
      class="context"
      title="The last model call's prompt size{contextLength > 0
        ? ` against the model's ${formatTokens(contextLength)}-token window`
        : ''}. Compact when this gets tight."
    >
      <span class="context-label">context</span>
      {#if contextLength > 0}
        <div class="meter" class:tight={contextPct > 0.7}>
          <i style="width: {Math.min(contextPct * 100, 100)}%"></i>
        </div>
        <span class="context-nums">
          {formatTokens(lastCall.prompt_tokens)} of {formatTokens(contextLength)} ·
          {Math.round(contextPct * 100)}%
        </span>
      {:else}
        <span class="context-nums">last prompt {formatTokens(lastCall.prompt_tokens)} tokens</span>
      {/if}
    </div>
  {/if}

  <div class="turns">
    {#each turns as turn, i (turn.id)}
      <section>
        <button class="head" onclick={() => toggle(turn.id)}>
          <span class="disclosure">{isOpen(turn, i) ? '−' : '+'}</span>
          <span class="name">
            {turn.stopReason === 'compacted' ? 'compact' : `turn ${i + 1}`}
            {#if turn.open}<em>running</em>{/if}
          </span>
          <span class="summary">{turnSummary(turn)}</span>
        </button>

        <div class="bar" title="time spent: model vs tools vs waiting on approval">
          {#each waterfall(turn) as seg, j (j)}
            <i class={seg.kind} style="left: {seg.left * 100}%; width: {seg.width * 100}%"></i>
          {/each}
        </div>

        {#if isOpen(turn, i)}
          <div class="spans">
            {#each turn.spans as span, j (j)}
              {@render spanRow(span)}
            {/each}
          </div>
        {/if}
      </section>
    {/each}
    {#if turns.length === 0}
      <div class="empty">each turn's model calls, tool runs, and timings land here</div>
    {/if}
  </div>
</div>

<style>
  .trace {
    display: flex;
    flex-direction: column;
    min-height: 0;
    height: 100%;
  }
  .tasks-wrap {
    padding: 10px 14px 0;
  }
  .totals {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 9px 14px;
    border-bottom: 1px solid var(--border);
    font-size: 12px;
    color: var(--text-muted);
  }
  .totals-label {
    font-size: 10.5px;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-muted);
  }
  .totals .nums {
    font-family: var(--mono);
    font-size: 10.5px;
    flex: 1;
    min-width: 0;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .compact {
    font-size: 12px;
    color: var(--text-muted);
    border: 1px solid var(--border);
    border-radius: 5px;
    padding: 2px 8px;
  }
  .compact:hover:not(:disabled) {
    color: var(--text);
    background: var(--surface-2);
  }
  .compact:disabled {
    opacity: 0.5;
    cursor: default;
  }
  .context {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 14px;
    border-bottom: 1px solid var(--border);
  }
  .context-label {
    font-size: 10.5px;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-muted);
  }
  .meter {
    flex: 1;
    min-width: 0;
    height: 5px;
    border-radius: 3px;
    background: var(--surface-2);
    overflow: hidden;
  }
  .meter i {
    display: block;
    height: 100%;
    border-radius: 3px;
    background: color-mix(in srgb, var(--text-muted), transparent 30%);
  }
  .meter.tight i {
    background: var(--warn);
  }
  .context-nums {
    font-family: var(--mono);
    font-size: 10.5px;
    color: var(--text-muted);
    white-space: nowrap;
  }
  .turns {
    flex: 1;
    overflow-y: auto;
    padding: 10px 14px 20px;
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  section {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .head {
    display: flex;
    align-items: baseline;
    gap: 8px;
    text-align: left;
    font-size: 12.5px;
  }
  .disclosure {
    color: var(--text-muted);
    font-family: var(--mono);
    width: 10px;
  }
  .name {
    font-weight: 600;
    color: var(--text-strong);
  }
  .name em {
    font-style: normal;
    color: var(--accent);
    font-weight: 400;
    margin-left: 6px;
    font-size: 11px;
  }
  .summary {
    font-family: var(--mono);
    font-size: 11px;
    color: var(--text-muted);
    margin-left: auto;
  }
  .bar {
    position: relative;
    height: 6px;
    border-radius: 3px;
    background: var(--surface-2);
    overflow: hidden;
  }
  .bar i {
    position: absolute;
    top: 0;
    bottom: 0;
    border-radius: 2px;
  }
  .bar i.model {
    background: color-mix(in srgb, var(--text-muted), transparent 40%);
  }
  .bar i.tool {
    background: var(--accent);
  }
  .bar i.approval,
  .bar i.compact,
  .bar i.question {
    background: var(--warn);
  }
  .bar i.error {
    background: var(--danger);
  }
  .spans {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .span {
    font-size: 12px;
    border: 1px solid transparent;
    border-radius: 6px;
  }
  .span[open] {
    border-color: var(--border);
    background: var(--surface);
  }
  .span summary {
    display: flex;
    align-items: baseline;
    gap: 7px;
    padding: 3px 6px;
    cursor: pointer;
    user-select: none;
    list-style: none;
  }
  .span summary::-webkit-details-marker {
    display: none;
  }
  .dot {
    width: 7px;
    height: 7px;
    border-radius: 2px;
    flex-shrink: 0;
    align-self: center;
  }
  .span.model .dot {
    background: color-mix(in srgb, var(--text-muted), transparent 40%);
  }
  .span.tool .dot {
    background: var(--accent);
  }
  .span.approval .dot,
  .span.question .dot {
    background: var(--warn);
  }
  .span.error .dot,
  .span.compact .dot {
    background: var(--danger);
  }
  .label {
    font-family: var(--mono);
    color: var(--text);
  }
  .detail {
    font-family: var(--mono);
    color: var(--text-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
  }
  .dur {
    margin-left: auto;
    font-family: var(--mono);
    font-size: 10.5px;
    color: var(--text-muted);
    white-space: nowrap;
  }
  .body {
    margin: 0;
    padding: 7px 10px;
    border-top: 1px solid var(--border);
    white-space: pre-wrap;
    word-break: break-word;
    max-height: 260px;
    overflow-y: auto;
    font-size: 11.5px;
  }
  .body.meta {
    color: var(--ok);
    font-family: var(--mono);
  }
  .body.err {
    color: var(--danger);
  }
  .body.notes {
    display: flex;
    flex-direction: column;
    gap: 3px;
  }
  .note {
    display: flex;
    gap: 8px;
    align-items: baseline;
    font-size: 11px;
  }
  .note .module {
    font-family: var(--mono);
    color: var(--text-muted);
    flex-shrink: 0;
    min-width: 78px;
  }
  .note .note-text {
    white-space: pre-wrap;
    word-break: break-word;
  }
  .note.noted-err .note-text {
    color: var(--danger);
  }
  .body.actions {
    display: flex;
    justify-content: flex-end;
  }
  .restore {
    font-size: 11.5px;
    color: var(--text-muted);
    border: 1px solid var(--border);
    border-radius: 5px;
    padding: 3px 9px;
  }
  .restore:hover:not(:disabled) {
    color: var(--danger);
    border-color: var(--danger);
  }
  .restore:disabled {
    opacity: 0.5;
    cursor: default;
  }
  .children {
    border-top: 1px solid var(--border);
    padding: 4px 4px 4px 14px;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .empty {
    color: var(--text-muted);
    font-size: 12.5px;
    padding: 6px 0;
  }
</style>
