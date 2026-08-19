<script lang="ts">
  import { onMount } from 'svelte';
  import { api } from './lib/api';
  import { SessionState } from './lib/session.svelte';
  import type { ModuleStatus, SessionMeta } from './lib/types';
  import Sidebar, { type View } from './components/Sidebar.svelte';
  import Icon from './components/Icon.svelte';
  import Chat from './components/Chat.svelte';
  import RightPanel from './components/RightPanel.svelte';
  import ModulesPage from './components/ModulesPage.svelte';
  import ModelsPage from './components/ModelsPage.svelte';
  import FolderPicker from './components/FolderPicker.svelte';
  import SessionSwitcher from './components/SessionSwitcher.svelte';
  import { NotificationCenter } from './lib/notifications.svelte';

  const session = new SessionState();
  const notif = new NotificationCenter({
    onOpen: (id) => void select(id),
  });
  let sessions = $state<SessionMeta[]>([]);
  let modules = $state<ModuleStatus[]>([]);
  let view = $state<View>('chat');
  let showPanel = $state(true);
  let showSidebar = $state(true);
  let showPicker = $state(false);
  let showSwitcher = $state(false);

  // Layout tiers, tracked live. Desktop docks the side panes in the
  // grid; below 1080px the right panel presents as an overlay, below
  // 720px the sidebar becomes a drawer too. The same showSidebar /
  // showPanel toggles drive both presentations, so the header buttons
  // work at every width.
  let isDesktop = $state(true);
  let isMobile = $state(false);
  $effect(() => {
    const mqDesktop = window.matchMedia('(min-width: 1080px)');
    const mqMobile = window.matchMedia('(max-width: 719px)');
    const update = () => {
      isDesktop = mqDesktop.matches;
      isMobile = mqMobile.matches;
    };
    update();
    mqDesktop.addEventListener('change', update);
    mqMobile.addEventListener('change', update);
    return () => {
      mqDesktop.removeEventListener('change', update);
      mqMobile.removeEventListener('change', update);
    };
  });
  const sidebarOverlay = $derived(isMobile);
  const panelOverlay = $derived(!isDesktop);
  const sidebarDocked = $derived(!sidebarOverlay && (showSidebar || view !== 'chat'));
  const panelDocked = $derived(!panelOverlay && showPanel && view === 'chat');

  // Crossing a tier resets the affected pane to that tier's default:
  // overlays start closed, docked panes start open.
  $effect(() => {
    showSidebar = !sidebarOverlay;
  });
  $effect(() => {
    showPanel = !panelOverlay;
  });

  // Pane widths, draggable at the dividers and remembered per browser.
  const paneBounds = {
    sidebar: { min: 170, max: 400, fallback: 230 },
    panel: { min: 260, max: 640, fallback: 380 },
  };
  function storedWidth(key: 'sidebar' | 'panel'): number {
    const raw = Number(localStorage.getItem(`buntline.pane.${key}`));
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
        `buntline.pane.${key}`,
        String(key === 'sidebar' ? sidebarWidth : panelWidth),
      );
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', upEvent);
  }

  // Keyboard layer: Cmd/Ctrl+K opens the session switcher, Cmd/Ctrl+J
  // toggles the side panel, Cmd/Ctrl+B toggles the sidebar. Escape
  // closes an open overlay pane.
  function onGlobalKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      if (sidebarOverlay && showSidebar) showSidebar = false;
      else if (panelOverlay && showPanel) showPanel = false;
      return;
    }
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
    notif.setSessions(sessions);
  }

  async function select(id: string) {
    view = 'chat';
    // Picking a session from the drawer closes it: on a small screen
    // the chat is the destination.
    if (sidebarOverlay) showSidebar = false;
    if (session.meta?.id !== id) await session.load(id);
    notif.setActive(id);
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

  // First-run gate: until a provider exists there is nothing a session
  // could talk to, so the app opens on Models instead of the chat and
  // the folder picker stays closed. Adding a model flips the flag live.
  // null = not fetched yet, so the picker effect below cannot fire early.
  let configured = $state<boolean | null>(null);
  async function refreshConfigured() {
    try {
      configured = (await api.config()).configured;
    } catch {
      // unreachable server; the chat surfaces its own errors
    }
  }

  onMount(async () => {
    await refreshConfigured();
    const res = await api.modules();
    modules = res.modules;
    await refreshSessions();
    // The notification stream runs for the life of the app: it needs no
    // session to be selected (other-session events are its whole point).
    notif.start();
    if (!configured) {
      view = 'models';
      return;
    }
    if (sessions.length > 0) {
      await select(sessions[0].id);
    } else {
      showPicker = true;
    }
  });

  // Once setup completes, entering the chat with no sessions opens the
  // folder picker, the same landing a configured install gets.
  $effect(() => {
    if (configured === true && view === 'chat' && sessions.length === 0 && !session.meta) {
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

{#snippet sidebarPane()}
  <Sidebar
    {sessions}
    activeId={session.meta?.id ?? null}
    {view}
    onselect={select}
    onnew={() => (showPicker = true)}
    ondelete={remove}
    onnavigate={(v) => {
      view = v;
      if (sidebarOverlay) showSidebar = false;
    }}
  />
{/snippet}
{#snippet panelPane()}
  <RightPanel
    {session}
    {filesEnabled}
    {checkpointsEnabled}
    {ollamaEnabled}
    {subagentsEnabled}
    {tasksEnabled}
    {memoryEnabled}
  />
{/snippet}

<div
  class="layout"
  class:with-sidebar={sidebarDocked}
  class:with-panel={panelDocked}
  style="--sidebar-w: {sidebarWidth}px; --panel-w: {panelWidth}px"
>
  {#if sidebarDocked}
    {@render sidebarPane()}
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
      {notif}
      onnotifyopen={(id) => void select(id)}
      onfork={forkFrom}
      onedit={editFrom}
      bind:showPanel
      bind:showSidebar
    />
    {#if panelDocked}
      <div
        class="resizer"
        role="separator"
        aria-orientation="vertical"
        aria-label="Resize the side panel"
        onpointerdown={(e) => startResize('panel', e)}
      ></div>
      {@render panelPane()}
    {/if}
  {:else if view === 'modules'}
    <ModulesPage onchange={(m) => (modules = m)} />
  {:else if view === 'models'}
    <ModelsPage {session} {ollamaEnabled} {configured} onconfigured={refreshConfigured} />
  {/if}
</div>

{#if notif.attention}
  {@const attention = notif.attention}
  <button class="attention" onclick={() => select(attention.sessionId)}>
    <Icon name="bell" size={13} />
    <span class="attention-text">
      {attention.title}: {attention.body}
      <span class="attention-sess"> · {attention.sessionTitle}</span>
    </span>
    <span
      class="attention-close"
      role="button"
      tabindex="0"
      onclick={(e) => {
        e.stopPropagation();
        notif.dismissAttention();
      }}
      onkeydown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.stopPropagation();
          notif.dismissAttention();
        }
      }}
    >
      dismiss
    </span>
  </button>
{/if}

{#if sidebarOverlay && showSidebar}
  <button class="scrim" aria-label="Close the session list" onclick={() => (showSidebar = false)}
  ></button>
  <div class="drawer left">{@render sidebarPane()}</div>
{/if}
{#if panelOverlay && showPanel && view === 'chat'}
  <button class="scrim" aria-label="Close the side panel" onclick={() => (showPanel = false)}
  ></button>
  <div class="drawer right">{@render panelPane()}</div>
{/if}
{#if sidebarOverlay && !showSidebar && view !== 'chat'}
  <!-- Modules/Models have no header toggle of their own; on the drawer
       tier this floating button is the way back to the menu. -->
  <button
    class="drawer-fab icon-btn"
    onclick={() => (showSidebar = true)}
    title="Show the menu"
    aria-label="Show the menu"
  >
    <Icon name="panel-left" />
  </button>
{/if}

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
  /* The grid holds only docked panes; overlay panes live outside it, so
     no width-based hiding is needed here. */
  .layout {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    height: 100%;
  }
  .layout.with-sidebar {
    grid-template-columns: var(--sidebar-w) 3px minmax(0, 1fr);
  }
  .layout.with-sidebar.with-panel {
    grid-template-columns: var(--sidebar-w) 3px minmax(360px, 1fr) 3px var(--panel-w);
  }
  .layout.with-panel:not(.with-sidebar) {
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

  /* Overlay tiers: the same panes floating over the content. Scrim and
     drawer sit below the modal overlays (z-index 10+), above the git
     popover (5). */
  .scrim {
    position: fixed;
    inset: 0;
    z-index: 8;
    background: rgba(0, 0, 0, 0.45);
  }
  .drawer {
    position: fixed;
    top: 0;
    bottom: 0;
    z-index: 9;
    display: flex;
    background: var(--bg);
  }
  .drawer > :global(aside) {
    flex: 1;
    min-width: 0;
  }
  .drawer.left {
    left: 0;
    width: min(290px, 85vw);
    box-shadow: 8px 0 24px rgba(0, 0, 0, 0.25);
    animation: drawer-in-left 0.16s ease-out;
  }
  .drawer.right {
    right: 0;
    width: min(380px, 92vw);
    box-shadow: -8px 0 24px rgba(0, 0, 0, 0.25);
    animation: drawer-in-right 0.16s ease-out;
  }
  @keyframes drawer-in-left {
    from {
      transform: translateX(-24px);
      opacity: 0.6;
    }
  }
  @keyframes drawer-in-right {
    from {
      transform: translateX(24px);
      opacity: 0.6;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .drawer {
      animation: none;
    }
  }
  .drawer-fab {
    position: fixed;
    top: 10px;
    left: 12px;
    z-index: 7;
    background: var(--bg);
    border: 1px solid var(--border);
  }
  /* Attention banner: another session needs the user. Sits above the
     chat, below the modal overlays. */
  .attention {
    position: fixed;
    bottom: 18px;
    left: 50%;
    transform: translateX(-50%);
    z-index: 12;
    display: inline-flex;
    align-items: center;
    gap: 8px;
    max-width: min(560px, calc(100vw - 32px));
    background: var(--surface);
    border: 1px solid var(--border-strong);
    border-radius: 10px;
    box-shadow: 0 6px 24px rgba(0, 0, 0, 0.25);
    padding: 9px 12px;
    font-size: 12.5px;
    color: var(--text-strong);
    cursor: pointer;
  }
  .attention:hover {
    border-color: var(--accent);
  }
  .attention-text {
    min-width: 0;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .attention-sess {
    color: var(--text-muted);
  }
  .attention-close {
    flex-shrink: 0;
    font-size: 10.5px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--text-muted);
    padding: 2px 5px;
    border-radius: 5px;
  }
  .attention-close:hover {
    color: var(--text-strong);
    background: var(--surface-2);
  }
</style>
