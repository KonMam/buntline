<script lang="ts">
  // The memory panel: the model's cross-session notes for this workdir,
  // read-only, given the whole right panel instead of a card squeezed
  // into the trace (the trace is for what happened; memory is for what
  // the model knows). It fetches the MEMORY.md index plus the topic
  // files from the memory module routes, refreshes when a memory_write
  // tool call lands, and opens a topic's content on demand. The model
  // owns memory; the user edits by talking or by editing the file
  // directly, exactly like the tasks strip.
  import { api } from '../lib/api';
  import type { MemoryOverview, MemoryTopic } from '../lib/types';
  import type { SessionState } from '../lib/session.svelte';
  import { formatBytes, formatTime } from '../lib/format';
  import Icon from './Icon.svelte';

  let { session }: { session: SessionState } = $props();

  let overview = $state<MemoryOverview | null>(null);
  let loaded = $state(false);
  let openTopic = $state<MemoryTopic | null>(null);
  let error = $state('');

  // Refresh whenever a memory_write tool_end lands (activity grows) or
  // the session changes. The fetch is cheap and the routes are local.
  let memoryActivity = $derived(
    session.activity.filter((e) => e.type === 'tool_end' && e.tool_name === 'memory_write').length,
  );

  $effect(() => {
    void memoryActivity;
    void session.meta?.id;
    void session.busy;
    const workdir = session.meta?.workdir;
    if (!workdir) {
      overview = null;
      loaded = false;
      openTopic = null;
      return;
    }
    let alive = true;
    api
      .memoryOverview(workdir)
      .then((res) => {
        if (!alive) return;
        overview = res;
        loaded = true;
      })
      .catch((e) => {
        if (!alive) return;
        error = e instanceof Error ? e.message : String(e);
        loaded = true;
      });
    return () => {
      alive = false;
    };
  });

  async function open(name: string) {
    if (!session.meta?.workdir) return;
    openTopic = null;
    try {
      openTopic = await api.memoryTopic(session.meta.workdir, name);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }
</script>

<div class="memory">
  <div class="head">
    <span class="label">memory</span>
    {#if overview}
      <span class="meta">
        {overview.topics.length}
        {overview.topics.length === 1 ? 'topic' : 'topics'}
      </span>
    {/if}
  </div>
  <p class="hint">
    The model's notes for this directory, loaded at the start of every session. It writes them
    itself with memory_write; read topics with memory_read or edit the files directly.
  </p>

  {#if !loaded}
    <div class="status">loading…</div>
  {:else if !overview || (!overview.exists && overview.topics.length === 0)}
    <div class="empty">
      No memory yet. Ask the model to remember something, and it will appear here.
    </div>
  {:else}
    {#if overview.exists && overview.index.trim() !== ''}
      <details class="card" open>
        <summary>
          <span class="disclosure" aria-hidden="true"></span>
          <span class="card-label">index</span>
          <span class="card-meta">MEMORY.md</span>
        </summary>
        <pre class="index">{overview.index}</pre>
      </details>
    {/if}

    {#if openTopic}
      <div class="topic-head">
        <button class="back" onclick={() => (openTopic = null)}>
          <Icon name="back" size={12} /> topics
        </button>
        <span class="topic-name">{openTopic.name}</span>
      </div>
      <pre class="content">{openTopic.content}</pre>
    {:else if overview.topics.length > 0}
      <div class="topics">
        {#each overview.topics as t (t.name)}
          <button class="row" onclick={() => open(t.name)}>
            <Icon name="box" size={12} />
            <span class="name">{t.name}</span>
            <span class="meta">
              {formatBytes(t.size)} · {formatTime(t.modified)}
            </span>
          </button>
        {/each}
      </div>
    {/if}
  {/if}

  {#if error}
    <div class="error">{error}</div>
  {/if}
</div>

<style>
  .memory {
    display: flex;
    flex-direction: column;
    min-height: 0;
    height: 100%;
    padding: 14px;
    gap: 10px;
    overflow-y: auto;
  }
  .head {
    display: flex;
    align-items: baseline;
    gap: 8px;
  }
  .label {
    font-size: 10.5px;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-muted);
  }
  .head .meta {
    font-family: var(--mono);
    font-size: 10.5px;
    color: var(--text-muted);
    margin-left: auto;
  }
  .hint {
    margin: 0;
    font-size: 12px;
    color: var(--text-muted);
    line-height: 1.5;
  }
  .status,
  .empty {
    color: var(--text-muted);
    font-size: 12.5px;
    line-height: 1.5;
    padding: 4px 0;
  }
  .card {
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface);
    padding: 9px 12px;
  }
  summary {
    display: flex;
    align-items: baseline;
    gap: 8px;
    cursor: pointer;
    user-select: none;
    list-style: none;
  }
  summary::-webkit-details-marker {
    display: none;
  }
  .disclosure {
    color: var(--text-muted);
    font-family: var(--mono);
    width: 10px;
  }
  .disclosure::after {
    content: '+';
  }
  .card[open] .disclosure::after {
    content: '−';
  }
  summary:hover .card-label {
    color: var(--text-strong);
  }
  .card-label {
    font-size: 10.5px;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-muted);
  }
  .card-meta {
    margin-left: auto;
    font-family: var(--mono);
    font-size: 10.5px;
    color: var(--text-muted);
  }
  .index {
    margin: 8px 0 0;
    font-family: var(--mono);
    font-size: 11.5px;
    line-height: 1.5;
    color: var(--text);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    max-height: 260px;
    overflow-y: auto;
  }
  .topic-head {
    display: flex;
    align-items: baseline;
    gap: 10px;
    border-bottom: 1px solid var(--border);
    padding-bottom: 7px;
  }
  .back {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font: inherit;
    font-size: 11.5px;
    color: var(--text-muted);
    background: none;
    border: none;
    cursor: pointer;
    padding: 0;
  }
  .back:hover {
    color: var(--text-strong);
  }
  .topic-name {
    font-family: var(--mono);
    font-size: 11.5px;
    color: var(--text);
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .content {
    margin: 0;
    font-family: var(--mono);
    font-size: 11.5px;
    line-height: 1.55;
    color: var(--text);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    max-height: 420px;
    overflow-y: auto;
  }
  .topics {
    display: flex;
    flex-direction: column;
    gap: 3px;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 5px 8px;
    border-radius: 6px;
    text-align: left;
    color: var(--text);
  }
  .row:hover {
    background: var(--surface-2);
  }
  .row .name {
    font-family: var(--mono);
    font-size: 12px;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .row .meta {
    margin-left: auto;
    font-family: var(--mono);
    font-size: 10.5px;
    color: var(--text-muted);
    white-space: nowrap;
    flex-shrink: 0;
  }
  .error {
    font-size: 12.5px;
    color: var(--danger);
  }
</style>
