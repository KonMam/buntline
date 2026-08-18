<script lang="ts">
  import type { SessionState } from '../lib/session.svelte';
  import { renderMarkdown } from '../lib/markdown';
  import { formatDuration, formatTokens } from '../lib/format';
  import type { SubagentInfo } from '../lib/types';
  import Icon from './Icon.svelte';

  let { session }: { session: SessionState } = $props();

  // Refresh the registry while the session is busy: children start, run,
  // and finish mid-turn.
  $effect(() => {
    void session.busy;
    if (!session.meta) return;
    if (session.busy) {
      const timer = setInterval(() => void session.refreshSubagents(), 2000);
      return () => clearInterval(timer);
    }
    void session.refreshSubagents();
  });

  const selected = $derived(
    session.subagents.find((s) => s.id === session.selectedSubagent) ?? null,
  );
  // Live buffers for the selected subagent are reactive dependencies;
  // reading them here keeps the rendered output attached to the selection.
  const selectedText = $derived(selected ? (session.subagentText[selected.id] ?? '') : '');
  const selectedThinking = $derived(selected ? (session.subagentThinking[selected.id] ?? '') : '');

  let steerText = $state('');

  function select(id: string) {
    session.selectedSubagent = id;
    steerText = '';
  }

  async function sendSteer() {
    const t = steerText.trim();
    if (!t || !selected) return;
    steerText = '';
    await session.steerSubagent(selected.id, t);
  }

  function statusLabel(s: SubagentInfo): string {
    return s.status;
  }

  function duration(s: SubagentInfo): string {
    const start = Date.parse(s.started_at);
    const end = s.ended_at ? Date.parse(s.ended_at) : Date.now();
    return formatDuration(Math.max(end - start, 0));
  }

  function subagentTotal(s: SubagentInfo): number {
    const t = session.subagentTotals(s.id);
    return t.input + t.output;
  }

  // Live markdown for the selected subagent, throttled like the main chat.
  function throttledRender(read: () => string, write: (html: string) => void) {
    let timer: ReturnType<typeof setTimeout> | undefined;
    let last = 0;
    $effect(() => {
      const text = read();
      clearTimeout(timer);
      if (!text) {
        write('');
        return;
      }
      const wait = Math.max(0, 120 - (performance.now() - last));
      timer = setTimeout(() => {
        last = performance.now();
        write(renderMarkdown(text));
      }, wait);
      return () => clearTimeout(timer);
    });
  }

  let streamHtml = $state('');
  let thinkingHtml = $state('');
  throttledRender(
    () => selectedText,
    (h) => (streamHtml = h),
  );
  throttledRender(
    () => selectedThinking,
    (h) => (thinkingHtml = h),
  );
</script>

<div class="agents">
  {#if session.subagents.length === 0}
    <div class="empty">spawned subagents land here with their live output</div>
  {/if}

  <div class="list">
    {#each session.subagents as s (s.id)}
      <button
        class="row"
        class:active={s.id === session.selectedSubagent}
        onclick={() => select(s.id)}
      >
        <span class="name">{s.name || 'subagent'}</span>
        <span class="status {s.status}">
          <i class="dot"></i>
          {statusLabel(s)}
        </span>
        <span class="dur">{duration(s)}</span>
        <span class="task" title={s.task}>{s.task}</span>
        {#if subagentTotal(s) > 0}
          <span class="tokens">{formatTokens(subagentTotal(s))}</span>
        {/if}
      </button>
    {/each}
  </div>

  {#if selected}
    <div class="detail">
      <div class="detail-head">
        <span class="name">{selected.name || 'subagent'}</span>
        <span class="status {selected.status}">
          <i class="dot"></i>
          {statusLabel(selected)}
        </span>
        <span class="dur">{duration(selected)}</span>
        {#if subagentTotal(selected) > 0}
          <span class="tokens">{formatTokens(subagentTotal(selected))}</span>
        {/if}
      </div>

      {#if selected.status !== 'running'}
        <div class="report">
          {#if selected.report}
            <!-- eslint-disable-next-line svelte/no-at-html-tags -- sanitized by DOMPurify -->
            <div class="md">{@html renderMarkdown(selected.report)}</div>
          {:else}
            <div class="report-empty">no report</div>
          {/if}
        </div>
      {:else}
        <div class="stream">
          {#if selectedThinking}
            {#if thinkingHtml}
              <div class="thinking">
                <!-- eslint-disable-next-line svelte/no-at-html-tags -- sanitized by DOMPurify -->
                <div class="md">{@html thinkingHtml}</div>
              </div>
            {:else}
              <div class="thinking">{selectedThinking}</div>
            {/if}
          {/if}
          {#if selectedText}
            {#if streamHtml}
              <!-- eslint-disable-next-line svelte/no-at-html-tags -- sanitized by DOMPurify -->
              <div class="md">{@html streamHtml}</div>
            {:else}
              <div>{selectedText}</div>
            {/if}
          {:else}
            <div class="stream-empty">working</div>
          {/if}
        </div>

        <div class="steer-row">
          <input
            bind:value={steerText}
            onkeydown={(e) => {
              if (e.key === 'Enter' && !e.shiftKey) {
                e.preventDefault();
                void sendSteer();
              }
              if (e.key === 'Escape') steerText = '';
            }}
            placeholder="steer this subagent"
            spellcheck="false"
          />
          <button
            class="icon-btn"
            onclick={() => void sendSteer()}
            disabled={!steerText.trim()}
            title="Send a message to the subagent"
            aria-label="Steer the subagent"
          >
            <Icon name="chevron" size={12} />
          </button>
          <button
            class="stop"
            onclick={() => void session.interruptSubagent(selected.id)}
            title="Stop the subagent"
          >
            <Icon name="stop" size={11} />
          </button>
        </div>
      {/if}
    </div>
  {/if}
</div>

<style>
  .agents {
    display: flex;
    flex-direction: column;
    min-height: 0;
    height: 100%;
    font-size: 12.5px;
  }
  .list {
    overflow-y: auto;
    padding: 6px;
    display: flex;
    flex-direction: column;
    gap: 2px;
    max-height: 38%;
  }
  .row {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto auto;
    gap: 4px 8px;
    align-items: baseline;
    padding: 6px 8px;
    border-radius: 6px;
    text-align: left;
  }
  .row:hover {
    background: var(--surface-2);
  }
  .row.active {
    background: var(--accent-soft);
  }
  .name {
    font-family: var(--mono);
    font-size: 12px;
    color: var(--text-strong);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .task {
    grid-column: 1 / -1;
    font-size: 11.5px;
    color: var(--text-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .status {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font-size: 11px;
    color: var(--text-muted);
    white-space: nowrap;
  }
  .status .dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--text-muted);
  }
  .status.running .dot {
    background: var(--accent);
    animation: agent-pulse 1.6s ease-in-out infinite;
  }
  .status.done .dot {
    background: var(--ok);
  }
  .status.failed .dot,
  .status.interrupted .dot {
    background: var(--danger);
  }
  @keyframes agent-pulse {
    50% {
      opacity: 0.35;
    }
  }
  .dur {
    font-family: var(--mono);
    font-size: 10.5px;
    color: var(--text-muted);
    white-space: nowrap;
  }
  .tokens {
    font-family: var(--mono);
    font-size: 10.5px;
    color: var(--text-muted);
    white-space: nowrap;
  }
  .detail {
    flex: 1;
    min-height: 0;
    display: flex;
    flex-direction: column;
    border-top: 1px solid var(--border);
  }
  .detail-head {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 12px;
    border-bottom: 1px solid var(--border);
  }
  .stream {
    flex: 1;
    overflow-y: auto;
    padding: 10px 12px;
    font-size: 12.5px;
    line-height: 1.5;
    white-space: normal;
  }
  .thinking {
    color: var(--text-muted);
    border-left: 2px solid var(--border);
    padding-left: 10px;
    margin-bottom: 10px;
  }
  .stream-empty,
  .report-empty,
  .empty {
    color: var(--text-muted);
    padding: 10px 12px;
    font-size: 12px;
  }
  .report {
    flex: 1;
    overflow-y: auto;
    padding: 10px 12px;
    font-size: 12.5px;
    line-height: 1.5;
  }
  .steer-row {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 8px 10px;
    border-top: 1px solid var(--border);
  }
  .steer-row input {
    flex: 1;
    min-width: 0;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--surface);
    padding: 5px 9px;
    font-size: 12px;
    color: var(--text);
  }
  .steer-row input:focus {
    outline: none;
    border-color: var(--border-strong);
  }
  .stop {
    display: inline-flex;
    align-items: center;
    padding: 4px;
    border-radius: 6px;
    border: 1px solid var(--border-strong);
    color: var(--text);
  }
  .stop:hover {
    border-color: var(--danger);
    color: var(--danger);
  }
</style>
