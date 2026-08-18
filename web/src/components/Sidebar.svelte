<script lang="ts">
  import type { SessionMeta } from '../lib/types';
  import { ThemeState } from '../lib/theme.svelte';
  import Icon from './Icon.svelte';

  const theme = new ThemeState();
  const themeIcon = $derived(
    theme.current === 'system' ? 'contrast' : theme.current === 'light' ? 'sun' : 'moon',
  );

  export type View = 'chat' | 'modules' | 'models';

  let {
    sessions,
    activeId,
    view,
    onselect,
    onnew,
    ondelete,
    onnavigate,
  }: {
    sessions: SessionMeta[];
    activeId: string | null;
    view: View;
    onselect: (id: string) => void;
    onnew: () => void;
    ondelete: (id: string) => void;
    onnavigate: (view: View) => void;
  } = $props();

  // Click delete once to arm, again to confirm; any other click disarms.
  let armed = $state<string | null>(null);

  function del(e: MouseEvent, id: string) {
    e.stopPropagation();
    if (armed === id) {
      armed = null;
      ondelete(id);
    } else {
      armed = id;
    }
  }

  function shortDir(p: string): string {
    const parts = p.split('/');
    return parts.length > 2 ? parts.slice(-2).join('/') : p;
  }

  // A worktree-backed session's workdir ends with .tether/worktrees/<name>;
  // show it as a tag so parallel isolated sessions are distinguishable.
  function isWorktree(p: string): boolean {
    return p.includes('/.tether/worktrees/');
  }
</script>

<aside>
  <header>
    <span class="brand">tether</span>
    <button class="icon-btn" onclick={onnew} title="New session" aria-label="New session">
      <Icon name="plus" />
    </button>
  </header>

  <nav>
    {#each sessions as s (s.id)}
      <div
        class="item"
        class:active={s.id === activeId && view === 'chat'}
        role="button"
        tabindex="0"
        onclick={() => {
          armed = null;
          onselect(s.id);
        }}
        onkeydown={(e) => e.key === 'Enter' && onselect(s.id)}
      >
        <span class="title">{s.title}</span>
        {#if s.busy}
          <span
            class="busy-dot"
            title={s.running_tool ? `running ${s.running_tool}` : 'running a turn'}
          ></span>
        {/if}
        <span class="sub" title={s.workdir}>
          {#if isWorktree(s.workdir)}<i class="wt">worktree</i>{/if}{shortDir(s.workdir)}
        </span>
        <button
          class="del icon-btn danger"
          class:armed={armed === s.id}
          onclick={(e) => del(e, s.id)}
          title={armed === s.id ? 'Click again to delete' : 'Delete session'}
          aria-label="Delete session {s.title}"
        >
          {#if armed === s.id}<span class="sure">confirm</span>{:else}<Icon
              name="trash"
              size={13}
            />{/if}
        </button>
      </div>
    {/each}
  </nav>

  <footer>
    <button
      class="nav-btn"
      class:active={view === 'modules'}
      onclick={() => onnavigate(view === 'modules' ? 'chat' : 'modules')}
      title="Modules"
    >
      <Icon name="grid" size={14} />
      modules
    </button>
    <button
      class="nav-btn"
      class:active={view === 'models'}
      onclick={() => onnavigate(view === 'models' ? 'chat' : 'models')}
      title="Models"
    >
      <Icon name="box" size={14} />
      models
    </button>
    <button
      class="icon-btn theme"
      onclick={() => theme.cycle()}
      title="Theme: {theme.current} (click to change)"
      aria-label="Theme: {theme.current}, click to change"
    >
      <Icon name={themeIcon} size={14} />
    </button>
  </footer>
</aside>

<style>
  aside {
    display: flex;
    flex-direction: column;
    border-right: 1px solid var(--border);
    background: var(--surface);
    min-height: 0;
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 10px 10px 10px 14px;
    border-bottom: 1px solid var(--border);
  }
  .brand {
    font-weight: 650;
    letter-spacing: 0.01em;
    color: var(--text-strong);
  }
  nav {
    flex: 1;
    overflow-y: auto;
    padding: 6px;
  }
  .item {
    position: relative;
    display: block;
    width: 100%;
    text-align: left;
    padding: 7px 9px;
    border-radius: 6px;
    margin-bottom: 2px;
    cursor: pointer;
  }
  .item:hover {
    background: var(--surface-2);
  }
  .item.active {
    background: var(--accent-soft);
  }
  .title {
    display: block;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    font-size: 13px;
    padding-right: 30px;
  }
  .busy-dot {
    position: absolute;
    top: 11px;
    right: 30px;
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--accent);
    animation: busy-pulse 1.6s ease-in-out infinite;
  }
  @keyframes busy-pulse {
    50% {
      opacity: 0.35;
    }
  }
  .sub {
    font-size: 10.5px;
    color: var(--text-muted);
    font-family: var(--mono);
    display: block;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .wt {
    font-style: normal;
    font-size: 9px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--accent);
    border: 1px solid var(--accent-soft);
    border-radius: 3px;
    padding: 0 3px;
    margin-right: 5px;
    vertical-align: 1px;
  }
  .del {
    position: absolute;
    top: 6px;
    right: 6px;
    opacity: 0;
    width: 24px;
    height: 24px;
  }
  .item:hover .del,
  .del.armed {
    opacity: 1;
  }
  .del.armed {
    color: var(--danger);
    width: auto;
    padding: 0 6px;
  }
  .sure {
    font-size: 10.5px;
    font-weight: 600;
  }
  footer {
    display: flex;
    gap: 4px;
    padding: 8px;
    border-top: 1px solid var(--border);
    align-items: center;
  }
  .theme {
    margin-left: auto;
  }
  .nav-btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 12px;
    color: var(--text-muted);
    padding: 5px 9px;
    border-radius: 6px;
  }
  .nav-btn:hover {
    color: var(--text-strong);
    background: var(--surface-2);
  }
  .nav-btn.active {
    color: var(--text-strong);
    background: var(--surface-2);
  }
</style>
