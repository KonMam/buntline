<script lang="ts">
  // The memory card: the model's cross-session notes for this workdir,
  // read-only. It fetches the MEMORY.md index from the memory module
  // route and refreshes when a memory_write tool call lands in the
  // activity stream, so the card stays truthful without a poll loop.
  // The model owns memory; the user edits by talking or by editing the
  // file directly, exactly like the tasks strip.
  import { api } from '../lib/api';
  import type { SessionState } from '../lib/session.svelte';

  let { session }: { session: SessionState } = $props();

  let index = $state('');
  let loaded = $state(false);

  // Refresh whenever a memory_write tool_end lands (activity grows) or
  // the session changes. The fetch is cheap and the route is local.
  let memoryActivity = $derived(
    session.activity.filter((e) => e.type === 'tool_end' && e.tool_name === 'memory_write').length,
  );

  $effect(() => {
    void memoryActivity;
    void session.meta?.id;
    const workdir = session.meta?.workdir;
    if (!workdir) {
      index = '';
      loaded = false;
      return;
    }
    let alive = true;
    api
      .memoryIndex(workdir)
      .then((res) => {
        if (!alive) return;
        index = res.exists ? res.index : '';
        loaded = true;
      })
      .catch(() => {
        if (!alive) return;
        index = '';
        loaded = true;
      });
    return () => {
      alive = false;
    };
  });
</script>

{#if loaded && index.trim() !== ''}
  <div class="memory">
    <div class="head">
      <span class="label">memory</span>
    </div>
    <pre class="index">{index}</pre>
  </div>
{/if}

<style>
  .memory {
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface);
    padding: 9px 12px;
    display: flex;
    flex-direction: column;
    gap: 7px;
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
  .index {
    margin: 0;
    font-family: var(--mono);
    font-size: 11.5px;
    line-height: 1.5;
    color: var(--text);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    max-height: 180px;
    overflow-y: auto;
  }
</style>
