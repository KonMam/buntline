<script lang="ts">
  import { api } from '../lib/api';
  import type { SessionState } from '../lib/session.svelte';
  import type { FileEntry } from '../lib/types';
  import { copyText } from '../lib/clipboard';
  import { formatBytes } from '../lib/format';
  import { highlightFile } from '../lib/markdown';
  import { fileOpen } from '../lib/fileOpen.svelte';
  import Icon from './Icon.svelte';

  let { session }: { session: SessionState } = $props();

  let path = $state('.');
  let entries = $state<FileEntry[]>([]);
  let viewing = $state<{
    path: string;
    content: string;
    truncated: boolean;
    size: number | null;
  } | null>(null);
  let error = $state<string | null>(null);
  let filter = $state('');
  let copied = $state(false);

  const crumbs = $derived(path === '.' ? [] : path.split('/'));

  // Directories first, each group alphabetical, case-insensitive.
  const sorted = $derived(
    [...entries].sort((a, b) => {
      if (a.dir !== b.dir) return a.dir ? -1 : 1;
      return a.name.localeCompare(b.name, undefined, { sensitivity: 'base' });
    }),
  );
  const visible = $derived(
    filter.trim()
      ? sorted.filter((e) => e.name.toLowerCase().includes(filter.trim().toLowerCase()))
      : sorted,
  );

  const viewingHtml = $derived(viewing ? highlightFile(viewing.path, viewing.content) : null);
  const viewingLines = $derived(viewing ? viewing.content.split('\n').length : 0);

  async function loadDir(p: string) {
    if (!session.meta) return;
    try {
      const res = await api.filesTree(session.meta.id, p);
      path = p;
      entries = res.entries;
      viewing = null;
      filter = '';
      error = null;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  async function openFile(entry: FileEntry) {
    if (!session.meta) return;
    const p = path === '.' ? entry.name : `${path}/${entry.name}`;
    try {
      const res = await api.filesRead(session.meta.id, p);
      viewing = { path: p, size: entry.size ?? null, ...res };
      error = null;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  function enter(e: FileEntry) {
    if (e.dir) void loadDir(path === '.' ? e.name : `${path}/${e.name}`);
    else void openFile(e);
  }

  function up() {
    const parts = path.split('/');
    parts.pop();
    void loadDir(parts.length ? parts.join('/') : '.');
  }

  // A file link clicked in the chat lands here: view the file, or browse
  // the directory when the path points at one. Absolute paths under the
  // session's workdir are trimmed to relative form first.
  async function openPath(p: string) {
    if (!session.meta) return;
    const wd = session.meta.workdir;
    const rel =
      p.startsWith('/') && wd && (p === wd || p.startsWith(wd + '/'))
        ? p.slice(wd.length).replace(/^\/+/, '') || '.'
        : p;
    const dir = rel.includes('/') ? rel.slice(0, rel.lastIndexOf('/')) : '.';
    try {
      const res = await api.filesRead(session.meta.id, rel);
      // It is a file: show its parent directory listing behind it, so the
      // crumbs and "up" still navigate away.
      await loadDir(dir);
      viewing = { path: rel, size: null, ...res };
      error = null;
    } catch {
      // Not a file (or unreadable): treat it as a directory.
      void loadDir(rel);
    }
  }

  // One request per click (n increments); consumed here so a stale
  // request cannot fire again when the panel remounts later.
  $effect(() => {
    const r = fileOpen.request;
    if (!r) return;
    fileOpen.request = null;
    if (r.sessionId !== session.meta?.id) return;
    void openPath(r.path);
  });

  function touched(name: string): boolean {
    const p = path === '.' ? name : `${path}/${name}`;
    return session.touchedFiles.has(p);
  }

  async function copyContent() {
    if (!viewing) return;
    await copyText(viewing.content);
    copied = true;
    setTimeout(() => (copied = false), 1200);
  }

  // Reload when the session changes; refresh after the agent finishes a
  // turn (it may have created files).
  $effect(() => {
    void session.meta?.id;
    void session.busy;
    if (!session.busy) void loadDir(path);
  });
</script>

<div class="files">
  <div class="crumbs">
    <button class="crumb" onclick={() => loadDir('.')} disabled={path === '.' && !viewing}>
      root
    </button>
    {#each crumbs as part, i (i)}
      <span class="sep">/</span>
      <button class="crumb" onclick={() => loadDir(crumbs.slice(0, i + 1).join('/'))}>
        {part}
      </button>
    {/each}
    {#if viewing}
      <span class="sep">/</span>
      <span class="crumb current">{viewing.path.split('/').pop()}</span>
    {/if}
  </div>

  {#if error}
    <div class="error">{error}</div>
  {/if}

  {#if viewing}
    <div class="file-head">
      <span class="file-meta">
        {viewingLines}
        {viewingLines === 1 ? 'line' : 'lines'}{viewing.size !== null
          ? ` · ${formatBytes(viewing.size)}`
          : ''}{viewing.truncated ? ' · truncated' : ''}
      </span>
      <button
        class="icon-btn"
        onclick={copyContent}
        title="Copy file content"
        aria-label="Copy file content"
      >
        <Icon name={copied ? 'check' : 'copy'} size={12} />
      </button>
    </div>
    {#if viewingHtml}
      <!-- eslint-disable-next-line svelte/no-at-html-tags -- sanitized by DOMPurify -->
      <pre class="content hljs-host">{@html viewingHtml}</pre>
    {:else}
      <pre class="content">{viewing.content}</pre>
    {/if}
  {:else}
    <div class="filter-row">
      <input
        class="filter"
        placeholder="filter"
        bind:value={filter}
        spellcheck="false"
        onkeydown={(e) => {
          if (e.key === 'Escape') filter = '';
          if (e.key === 'Enter' && visible.length > 0) enter(visible[0]);
        }}
      />
      <span class="count">
        {visible.length}
        {visible.length === 1 ? 'entry' : 'entries'}
      </span>
    </div>
    <div class="list">
      {#if path !== '.' && !filter}
        <button class="row" onclick={up}>
          <span class="glyph"></span>
          <span class="name">..</span>
        </button>
      {/if}
      {#each visible as e (e.name)}
        <button class="row" onclick={() => enter(e)}>
          <span class="glyph" class:dim={!e.dir}>
            {#if e.dir}<Icon name="folder" size={12} />{/if}
          </span>
          <span class="name" class:dir={e.dir}>
            {e.name}
            {#if touched(e.name)}<i class="touched" title="changed by the agent"></i>{/if}
          </span>
          <span class="meta">
            {e.dir ? `${e.count} items` : formatBytes(e.size ?? 0)}
          </span>
        </button>
      {/each}
      {#if visible.length === 0}
        <div class="empty">{filter ? 'no matches' : 'empty directory'}</div>
      {/if}
    </div>
  {/if}
</div>

<style>
  .files {
    display: flex;
    flex-direction: column;
    min-height: 0;
    height: 100%;
    font-size: 12.5px;
  }
  .crumbs {
    display: flex;
    align-items: center;
    gap: 3px;
    padding: 9px 14px;
    border-bottom: 1px solid var(--border);
    font-family: var(--mono);
    font-size: 11.5px;
    flex-wrap: wrap;
  }
  .crumb {
    color: var(--accent);
  }
  .crumb:disabled,
  .crumb.current {
    color: var(--text-muted);
    cursor: default;
  }
  .sep {
    color: var(--text-muted);
  }
  .filter-row {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 7px 14px 5px;
  }
  .filter {
    flex: 1;
    min-width: 0;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--surface);
    padding: 4px 9px;
    font-size: 12px;
    font-family: var(--mono);
    color: var(--text);
  }
  .filter:focus {
    outline: none;
    border-color: var(--border-strong);
  }
  .count {
    font-size: 11px;
    color: var(--text-muted);
    white-space: nowrap;
  }
  .list {
    flex: 1;
    overflow-y: auto;
    padding: 4px 6px 6px;
  }
  .row {
    display: flex;
    width: 100%;
    align-items: center;
    gap: 7px;
    padding: 4px 8px;
    border-radius: 5px;
    text-align: left;
  }
  .row:hover {
    background: var(--surface-2);
  }
  .glyph {
    width: 12px;
    flex-shrink: 0;
    display: inline-flex;
    color: var(--text-muted);
  }
  .name {
    font-family: var(--mono);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .name.dir {
    color: var(--text-strong);
  }
  .touched {
    display: inline-block;
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--accent);
    margin-left: 6px;
    vertical-align: 1px;
  }
  .meta {
    margin-left: auto;
    color: var(--text-muted);
    font-size: 11px;
    white-space: nowrap;
    flex-shrink: 0;
  }
  .file-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    padding: 6px 14px;
    border-bottom: 1px solid var(--border);
  }
  .file-meta {
    font-size: 11px;
    color: var(--text-muted);
    font-family: var(--mono);
  }
  .content {
    flex: 1;
    overflow: auto;
    margin: 0;
    padding: 10px 14px;
    white-space: pre;
    font-size: 12px;
    line-height: 1.55;
    tab-size: 4;
  }
  .empty,
  .error {
    padding: 12px 14px;
    color: var(--text-muted);
  }
  .error {
    color: var(--danger);
  }
</style>
