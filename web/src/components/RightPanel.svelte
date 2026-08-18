<script lang="ts">
  import type { SessionState } from '../lib/session.svelte';
  import TracePanel from './TracePanel.svelte';
  import FilesPanel from './FilesPanel.svelte';
  import AgentsPanel from './AgentsPanel.svelte';

  let {
    session,
    filesEnabled,
    checkpointsEnabled,
    ollamaEnabled,
    subagentsEnabled,
    tasksEnabled,
    memoryEnabled,
  }: {
    session: SessionState;
    filesEnabled: boolean;
    checkpointsEnabled: boolean;
    ollamaEnabled: boolean;
    subagentsEnabled: boolean;
    tasksEnabled: boolean;
    memoryEnabled: boolean;
  } = $props();

  let tab = $state<'trace' | 'files' | 'agents'>('trace');
  $effect(() => {
    if (!filesEnabled && tab === 'files') tab = 'trace';
    if (!subagentsEnabled && tab === 'agents') tab = 'trace';
  });
</script>

<aside>
  <nav>
    <button class:active={tab === 'trace'} onclick={() => (tab = 'trace')}>trace</button>
    {#if filesEnabled}
      <button class:active={tab === 'files'} onclick={() => (tab = 'files')}>files</button>
    {/if}
    {#if subagentsEnabled}
      <button class:active={tab === 'agents'} onclick={() => (tab = 'agents')}>agents</button>
    {/if}
  </nav>
  {#if tab === 'trace'}
    <TracePanel {session} {checkpointsEnabled} {ollamaEnabled} {tasksEnabled} {memoryEnabled} />
  {:else if tab === 'files'}
    <FilesPanel {session} />
  {:else}
    <AgentsPanel {session} />
  {/if}
</aside>

<style>
  aside {
    border-left: 1px solid var(--border);
    background: var(--surface);
    display: flex;
    flex-direction: column;
    min-height: 0;
  }
  nav {
    display: flex;
    gap: 2px;
    padding: 8px 10px 0;
    border-bottom: 1px solid var(--border);
  }
  nav button {
    font-size: 12px;
    color: var(--text-muted);
    padding: 4px 10px 7px;
    border-bottom: 2px solid transparent;
    margin-bottom: -1px;
  }
  nav button:hover {
    color: var(--text);
  }
  nav button.active {
    color: var(--text-strong);
    border-bottom-color: var(--accent);
  }
</style>
