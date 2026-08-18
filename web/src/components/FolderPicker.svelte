<script lang="ts">
  import { onMount } from 'svelte';
  import { api } from '../lib/api';
  import type { WorkdirSuggestions } from '../lib/types';

  let {
    onpick,
    onworktree,
    oncancel,
  }: {
    onpick: (path: string) => void;
    // onworktree fires when the user chooses to create a worktree of the
    // currently selected directory (which must be inside a git repo).
    onworktree: (repo: string) => void;
    oncancel: () => void;
  } = $props();

  let suggestions = $state<WorkdirSuggestions | null>(null);
  let path = $state('');
  let typed = $state(''); // the editable path field, follows navigation
  let parent = $state('');
  let dirs = $state<string[]>([]);
  let error = $state<string | null>(null);

  async function browse(p?: string) {
    try {
      const res = await api.browse(p);
      path = res.path;
      typed = res.path;
      parent = res.parent;
      dirs = res.dirs;
      error = null;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  // Shortcuts: home and the configured default always; recents after.
  const shortcuts = $derived.by(() => {
    if (!suggestions) return [] as { label: string; path: string }[];
    const out: { label: string; path: string }[] = [];
    const seen = new Set<string>();
    const add = (label: string, p: string) => {
      if (p && !seen.has(p)) {
        seen.add(p);
        out.push({ label, path: p });
      }
    };
    add('home', suggestions.home);
    add('default', suggestions.default);
    for (const r of suggestions.recent.slice(0, 4)) {
      add(r.split('/').slice(-2).join('/'), r);
    }
    return out;
  });

  onMount(async () => {
    suggestions = await api.workdirs();
    await browse(suggestions.default || suggestions.home);
  });
</script>

<div
  class="backdrop"
  role="presentation"
  onclick={(e) => {
    if (e.target === e.currentTarget) oncancel();
  }}
>
  <div class="picker" role="dialog" aria-label="Choose working directory">
    <header>
      <span>working directory for the new session</span>
      <button class="close" onclick={oncancel}>close</button>
    </header>

    {#if shortcuts.length > 0}
      <div class="recent">
        {#each shortcuts as s (s.path)}
          <button class="chip" title={s.path} onclick={() => browse(s.path)}>
            {s.label}
          </button>
        {/each}
      </div>
    {/if}

    <input
      class="path"
      bind:value={typed}
      spellcheck="false"
      title="Type or paste a path, Enter to open it"
      onkeydown={(e) => {
        if (e.key === 'Enter') void browse(typed.trim());
      }}
    />
    {#if error}<div class="error">{error}</div>{/if}

    <div class="dirs">
      {#if parent}
        <button class="dir" onclick={() => browse(parent)}>..</button>
      {/if}
      {#each dirs as d (d)}
        <button
          class="dir"
          ondblclick={() => browse(`${path}/${d}`)}
          onclick={() => browse(`${path}/${d}`)}
        >
          {d}/
        </button>
      {/each}
      {#if dirs.length === 0}
        <div class="empty">no subdirectories</div>
      {/if}
    </div>

    <footer>
      <button
        class="worktree"
        onclick={() => onworktree(path)}
        title="Create an isolated git worktree of this directory and open a session there, so parallel sessions never collide"
      >
        open in a worktree
      </button>
      <button class="use" onclick={() => onpick(path)}>use this directory</button>
    </footer>
  </div>
</div>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    background: color-mix(in srgb, var(--bg), transparent 30%);
    display: grid;
    place-items: center;
    z-index: 10;
  }
  .picker {
    width: min(560px, calc(100vw - 40px));
    max-height: min(600px, calc(100vh - 80px));
    display: flex;
    flex-direction: column;
    background: var(--bg);
    border: 1px solid var(--border-strong);
    border-radius: 10px;
    overflow: hidden;
  }
  header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 12px 16px;
    border-bottom: 1px solid var(--border);
    font-size: 13px;
    color: var(--text-strong);
  }
  .close {
    color: var(--text-muted);
    font-size: 12px;
  }
  .close:hover {
    color: var(--text);
  }
  .recent {
    display: flex;
    gap: 6px;
    flex-wrap: wrap;
    padding: 10px 16px 0;
  }
  .chip {
    font-family: var(--mono);
    font-size: 11px;
    padding: 3px 8px;
    border: 1px solid var(--border);
    border-radius: 20px;
    color: var(--text-muted);
  }
  .chip:hover {
    color: var(--text);
    border-color: var(--border-strong);
  }
  .path {
    font-family: var(--mono);
    font-size: 12px;
    margin: 10px 16px 6px;
    padding: 6px 10px;
    color: var(--text-strong);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 6px;
    outline: none;
  }
  .path:focus {
    border-color: var(--accent);
  }
  .dirs {
    flex: 1;
    overflow-y: auto;
    padding: 0 10px 10px;
    min-height: 180px;
  }
  .dir {
    display: block;
    width: 100%;
    text-align: left;
    font-family: var(--mono);
    font-size: 12.5px;
    padding: 4px 8px;
    border-radius: 5px;
  }
  .dir:hover {
    background: var(--surface-2);
  }
  .empty {
    padding: 8px;
    color: var(--text-muted);
    font-size: 12px;
  }
  .error {
    padding: 4px 16px;
    color: var(--danger);
    font-size: 12px;
  }
  footer {
    padding: 10px 16px;
    border-top: 1px solid var(--border);
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }
  .worktree {
    color: var(--text-muted);
    font-size: 12px;
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 5px 12px;
  }
  .worktree:hover {
    color: var(--text);
    border-color: var(--border-strong);
  }
  .use {
    background: var(--accent);
    color: var(--accent-contrast);
    font-size: 12.5px;
    padding: 5px 14px;
    border-radius: 6px;
  }
</style>
