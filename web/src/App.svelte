<script lang="ts">
  import { onMount } from 'svelte';
  import { api } from './lib/api';
  import { SessionState } from './lib/session.svelte';
  import type { ModuleStatus, SessionMeta } from './lib/types';
  import Sidebar, { type View } from './components/Sidebar.svelte';
  import Chat from './components/Chat.svelte';
  import RightPanel from './components/RightPanel.svelte';
  import ModulesPage from './components/ModulesPage.svelte';
  import ModelsPage from './components/ModelsPage.svelte';
  import FolderPicker from './components/FolderPicker.svelte';
  import SessionSwitcher from './components/SessionSwitcher.svelte';

  const session = new SessionState();
  let sessions = $state<SessionMeta[]>([]);
  let modules = $state<ModuleStatus[]>([]);
  let view = $state<View>('chat');
  let showPanel = $state(true);
  let showSidebar = $state(true);
  let showPicker = $state(false);
  let showSwitcher = $state(false);

  // Pane widths, draggable at the dividers and remembered per browser.
  const paneBounds = {
    sidebar: { min: 170, max: 400, fallback: 230 },
    panel: { min: 260, max: 640, fallback: 380 },
  };
  function storedWidth(key: 'sidebar' | 'panel'): number {
    const raw = Number(localStorage.getItem(`tether.pane.${key}`));
    const b = paneBounds[key];
    return raw >= b.min && raw <= b.max ? raw : b.fallback;
  }
  let sidebarWidth = $state(storedWidth('sidebar'));
  let panelWidth = $state(storedWidth('panel'));

  function startResize(key: 'sidebar' | 'panel', e: PointerEvent) {
    e.preventDefault();
    const startX = e.clientX;
    const startW = key === 'sidebar' ? sidebarWidth : panelWidth;
    const b = paneBounds[key];
    const move = (ev: PointerEvent) => {
      // The panel divider sits to its left, so dragging left widens it.
      const delta = key === 'sidebar' ? ev.clientX - startX : startX - ev.clientX;
      const w = Math.min(b.max, Math.max(b.min, startW + delta));
      if (key === 'sidebar') sidebarWidth = w;
      else panelWidth = w;
    };
    const upEvent = () => {
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', upEvent);
      localStorage.setItem(
        `tether.pane.${key}`,
        String(key === 'sidebar' ? sidebarWidth : panelWidth),
      );
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', upEvent);
  }

  // Keyboard layer: Cmd/Ctrl+K opens the session switcher, Cmd/Ctrl+J
  // toggles the side panel, Cmd/Ctrl+B toggles the sidebar.
  function onGlobalKeydown(e: KeyboardEvent) {
    if (!(e.metaKey || e.ctrlKey)) return;
    if (e.key === 'k') {
      e.preventDefault();
      showSwitcher = !showSwitcher;
    }
    if (e.key === 'j') {
      e.preventDefault();
      showPanel = !showPanel;
    }
    if (e.key === 'b') {
      e.preventDefault();
      showSidebar = !showSidebar;
    }
  }

  const enabled = (id: string) => modules.find((m) => m.id === id)?.enabled ?? false;
  const filesEnabled = $derived(enabled('files'));
  const ollamaEnabled = $derived(enabled('ollama'));
  const commandsEnabled = $derived(enabled('commands'));
  const gitEnabled = $derived(enabled('git'));
  const checkpointsEnabled = $derived(enabled('checkpoints'));
  const subagentsEnabled = $derived(enabled('subagents'));
  const tasksEnabled = $derived(enabled('tasks'));
  const memoryEnabled = $derived(enabled('memory'));
  const mcpEnabled = $derived(enabled('mcp'));

  async function refreshSessions() {
    sessions = await api.listSessions();
  }

  async function select(id: string) {
    view = 'chat';
    if (session.meta?.id !== id) await session.load(id);
  }

  async function createIn(workdir: string) {
    showPicker = false;
    const meta = await api.createSession(workdir);
    await refreshSessions();
    await select(meta.id);
  }

  // Create a worktree of the selected repo and open a session in it:
  // parallel sessions in one repository get isolated checkouts.
  async function createWorktreeSession(repo: string) {
    showPicker = false;
    const meta = await api.createSession(undefined, undefined, repo);
    await refreshSessions();
    await select(meta.id);
  }

  // Fork: a new session carrying the transcript up to (excluding) the
  // chosen message: rewind as a branch, the original untouched.
  async function forkFrom(index: number) {
    if (!session.meta) return;
    const fork = await api.fork(session.meta.id, index);
    await refreshSessions();
    await select(fork.id);
  }

  // Edit-and-resend: fork to before the message, then hand its text to
  // the composer for editing.
  async function editFrom(index: number) {
    if (!session.meta) return;
    const text = session.messages[index]?.content ?? '';
    await forkFrom(index);
    session.draft = text;
  }

  async function remove(id: string) {
    await api.deleteSession(id);
    if (session.meta?.id === id) {
      session.close();
      session.meta = null;
      session.messages = [];
      session.activity = [];
    }
    await refreshSessions();
    if (!session.meta && sessions.length > 0) await select(sessions[0].id);
  }

  onMount(async () => {
    const res = await api.modules();
    modules = res.modules;
    await refreshSessions();
    if (sessions.length > 0) {
      await select(sessions[0].id);
    } else {
      showPicker = true;
    }
  });

  // Session titles update after the first message; refresh the list when a
  // turn completes.
  $effect(() => {
    if (!session.busy) void refreshSessions();
  });

  // Live state (busy, running tool) refreshes every 5 seconds while the
  // window is visible; a long turn in another session shows up here.
  let listTimer: ReturnType<typeof setInterval> | undefined;
  $effect(() => {
    if (typeof document === 'undefined') return;
    const tick = () => {
      if (document.visibilityState === 'visible') void refreshSessions();
    };
    clearInterval(listTimer);
    listTimer = setInterval(tick, 5000);
    return () => clearInterval(listTimer);
  });
</script>

<div
  class="layout"
  class:with-panel={showPanel && view === 'chat'}
  class:no-sidebar={!showSidebar && view === 'chat'}
  style="--sidebar-w: {sidebarWidth}px; --panel-w: {panelWidth}px"
>
  {#if showSidebar || view !== 'chat'}
    <Sidebar
      {sessions}
      activeId={session.meta?.id ?? null}
      {view}
      onselect={select}
      onnew={() => (showPicker = true)}
      ondelete={remove}
      onnavigate={(v) => (view = v)}
    />
    <div
      class="resizer"
      role="separator"
      aria-orientation="vertical"
      aria-label="Resize the sidebar"
      onpointerdown={(e) => startResize('sidebar', e)}
    ></div>
  {/if}

  {#if view === 'chat'}
    <Chat
      {session}
      {commandsEnabled}
      {gitEnabled}
      {filesEnabled}
      {mcpEnabled}
      onfork={forkFrom}
      onedit={editFrom}
      bind:showPanel
      bind:showSidebar
    />
    {#if showPanel}
      <div
        class="resizer"
        role="separator"
        aria-orientation="vertical"
        aria-label="Resize the side panel"
        onpointerdown={(e) => startResize('panel', e)}
      ></div>
      <RightPanel
        {session}
        {filesEnabled}
        {checkpointsEnabled}
        {ollamaEnabled}
        {subagentsEnabled}
        {tasksEnabled}
        {memoryEnabled}
      />
    {/if}
  {:else if view === 'modules'}
    <ModulesPage onchange={(m) => (modules = m)} />
  {:else if view === 'models'}
    <ModelsPage {session} {ollamaEnabled} />
  {/if}
</div>

{#if showPicker}
  <FolderPicker
    onpick={createIn}
    onworktree={createWorktreeSession}
    oncancel={() => (showPicker = sessions.length === 0)}
  />
{/if}
{#if showSwitcher}
  <SessionSwitcher
    {sessions}
    onselect={select}
    onnew={() => (showPicker = true)}
    onclose={() => (showSwitcher = false)}
  />
{/if}

<svelte:window onkeydown={onGlobalKeydown} />

<style>
  .layout {
    display: grid;
    grid-template-columns: var(--sidebar-w) 3px minmax(0, 1fr);
    height: 100%;
  }
  .layout.with-panel {
    grid-template-columns: var(--sidebar-w) 3px minmax(360px, 1fr) 3px var(--panel-w);
  }
  .layout.no-sidebar {
    grid-template-columns: minmax(0, 1fr);
  }
  .layout.no-sidebar.with-panel {
    grid-template-columns: minmax(360px, 1fr) 3px var(--panel-w);
  }
  .resizer {
    cursor: col-resize;
    background: transparent;
    position: relative;
  }
  /* A wider invisible hit area than the 3px track. */
  .resizer::after {
    content: '';
    position: absolute;
    inset: 0 -3px;
  }
  .resizer:hover,
  .resizer:active {
    background: var(--border-strong);
  }

  /* Narrow: drop the right panel first, then the sidebar. */
  @media (max-width: 1080px) {
    .layout.with-panel {
      grid-template-columns: var(--sidebar-w) 3px minmax(0, 1fr);
    }
    .layout.no-sidebar.with-panel {
      grid-template-columns: minmax(0, 1fr);
    }
    .layout.with-panel :global(> aside:last-child),
    .layout.with-panel :global(> .resizer:nth-last-child(2)) {
      display: none;
    }
  }
  @media (max-width: 720px) {
    .layout,
    .layout.with-panel {
      grid-template-columns: minmax(0, 1fr);
    }
    .layout :global(> aside:first-child),
    .layout :global(> .resizer:first-of-type) {
      display: none;
    }
  }
</style>
