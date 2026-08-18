<script lang="ts">
  import { api } from '../lib/api';
  import type { SearchHit, SessionMeta } from '../lib/types';

  let {
    sessions,
    onselect,
    onnew,
    onclose,
  }: {
    sessions: SessionMeta[];
    onselect: (id: string) => void;
    onnew: () => void;
    onclose: () => void;
  } = $props();

  let query = $state('');
  let highlighted = $state(0);
  let input = $state<HTMLInputElement | null>(null);

  const filtered = $derived(
    sessions.filter(
      (s) =>
        s.title.toLowerCase().includes(query.toLowerCase()) ||
        s.workdir.toLowerCase().includes(query.toLowerCase()),
    ),
  );

  // With an empty query, running sessions surface first: parallel work
  // across sessions should be findable at a glance.
  const sorted = $derived(
    query.trim()
      ? filtered
      : [...filtered].sort((a, b) => Number(b.busy ?? false) - Number(a.busy ?? false)),
  );

  // Transcript search: full-text recall across sessions, debounced.
  // Sessions already matched by title stay out of the second list.
  let hits = $state<SearchHit[]>([]);
  let searchTimer: ReturnType<typeof setTimeout> | undefined;
  $effect(() => {
    const q = query.trim();
    clearTimeout(searchTimer);
    if (q.length < 3) {
      hits = [];
      return;
    }
    searchTimer = setTimeout(() => {
      api
        .search(q)
        .then((r) => (hits = r.hits.filter((h) => !filtered.some((s) => s.id === h.session_id))))
        .catch(() => (hits = []));
    }, 200);
    return () => clearTimeout(searchTimer);
  });

  // Entry 0 is always "New session"; sessions follow, then transcript hits.
  const count = $derived(sorted.length + hits.length + 1);

  function choose(index: number) {
    if (index === 0) onnew();
    else if (index <= sorted.length) onselect(sorted[index - 1].id);
    else onselect(hits[index - 1 - sorted.length].session_id);
    onclose();
  }

  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'Escape') onclose();
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      highlighted = (highlighted + 1) % count;
    }
    if (e.key === 'ArrowUp') {
      e.preventDefault();
      highlighted = (highlighted - 1 + count) % count;
    }
    if (e.key === 'Enter') {
      e.preventDefault();
      choose(highlighted);
    }
  }

  $effect(() => {
    void query;
    highlighted = 0;
  });
  $effect(() => {
    input?.focus();
  });
</script>

<div
  class="backdrop"
  role="presentation"
  onclick={(e) => {
    if (e.target === e.currentTarget) onclose();
  }}
>
  <div class="switcher" role="dialog" aria-label="Switch session">
    <input
      bind:this={input}
      bind:value={query}
      {onkeydown}
      placeholder="Search sessions"
      spellcheck="false"
    />
    <div class="list">
      <button class="entry" class:highlighted={highlighted === 0} onclick={() => choose(0)}>
        <span class="title new">New session</span>
      </button>
      {#each sorted as s, i (s.id)}
        <button
          class="entry"
          class:highlighted={highlighted === i + 1}
          onclick={() => choose(i + 1)}
        >
          <span class="title">{s.title}</span>
          {#if s.busy}<i class="busy-dot" title="running a turn"></i>{/if}
          <span class="dir">{s.workdir.split('/').slice(-2).join('/')}</span>
        </button>
      {/each}
      {#if hits.length > 0}
        <div class="section">in transcripts</div>
        {#each hits as h, i (h.session_id)}
          <button
            class="entry hit"
            class:highlighted={highlighted === filtered.length + i + 1}
            onclick={() => choose(filtered.length + i + 1)}
          >
            <span class="title">{h.title}</span>
            <span class="snippet">{h.snippet}</span>
          </button>
        {/each}
      {/if}
    </div>
  </div>
</div>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    background: color-mix(in srgb, var(--bg), transparent 30%);
    display: flex;
    justify-content: center;
    padding-top: 12vh;
    z-index: 20;
  }
  .switcher {
    width: min(520px, calc(100vw - 40px));
    max-height: 50vh;
    display: flex;
    flex-direction: column;
    background: var(--bg);
    border: 1px solid var(--border-strong);
    border-radius: 10px;
    overflow: hidden;
    align-self: flex-start;
    box-shadow: 0 8px 32px rgba(0, 0, 0, 0.25);
  }
  input {
    font: inherit;
    font-size: 13.5px;
    color: var(--text);
    background: none;
    border: none;
    outline: none;
    padding: 12px 16px;
    border-bottom: 1px solid var(--border);
  }
  .list {
    overflow-y: auto;
    padding: 6px;
  }
  .entry {
    display: flex;
    width: 100%;
    align-items: baseline;
    gap: 10px;
    padding: 7px 10px;
    border-radius: 6px;
    text-align: left;
  }
  .entry.highlighted,
  .entry:hover {
    background: var(--surface-2);
  }
  .title {
    font-size: 13px;
    color: var(--text-strong);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
  }
  .title.new {
    color: var(--accent);
  }
  .busy-dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--accent);
    flex-shrink: 0;
    animation: switcher-pulse 1.6s ease-in-out infinite;
  }
  @keyframes switcher-pulse {
    50% {
      opacity: 0.35;
    }
  }
  .dir {
    margin-left: auto;
    font-family: var(--mono);
    font-size: 10.5px;
    color: var(--text-muted);
    flex-shrink: 0;
  }
  .section {
    padding: 8px 10px 3px;
    font-size: 10.5px;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-muted);
  }
  .entry.hit {
    flex-direction: column;
    align-items: stretch;
    gap: 2px;
  }
  .snippet {
    font-size: 11.5px;
    color: var(--text-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
</style>
