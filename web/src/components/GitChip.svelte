<script lang="ts">
  import { api } from '../lib/api';
  import type { SessionState } from '../lib/session.svelte';

  let { session }: { session: SessionState } = $props();

  interface FileStat {
    path: string;
    additions: number;
    deletions: number;
    new?: boolean;
  }

  let repo = $state(false);
  let branch = $state('');
  let changed = $state(0);
  let additions = $state(0);
  let deletions = $state(0);
  let files = $state<FileStat[]>([]);
  let open = $state(false);
  let message = $state('');
  let error = $state<string | null>(null);
  let committing = $state(false);

  async function refresh() {
    if (!session.meta) return;
    try {
      const s = await api.gitStatus(session.meta.id);
      repo = s.repo;
      branch = s.branch ?? '';
      changed = s.changed ?? 0;
      additions = s.additions ?? 0;
      deletions = s.deletions ?? 0;
      files = s.files ?? [];
    } catch {
      repo = false;
    }
  }

  // Refresh on session switch and whenever a turn finishes.
  $effect(() => {
    void session.meta?.id;
    void session.busy;
    if (!session.busy) void refresh();
  });

  async function commit() {
    if (!session.meta || !message.trim() || committing) return;
    committing = true;
    error = null;
    try {
      await api.gitCommit(session.meta.id, message.trim());
      message = '';
      open = false;
      await refresh();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      committing = false;
    }
  }
</script>

{#if repo}
  <div class="git">
    <button
      class="chip"
      class:dirty={changed > 0}
      onclick={() => (open = !open)}
      title={changed > 0 ? `${changed} changed files on ${branch}` : `clean on ${branch}`}
    >
      <span class="branch">{branch}</span>
      {#if additions > 0}<em class="add">+{additions}</em>{/if}
      {#if deletions > 0}<em class="del">−{deletions}</em>{/if}
      {#if changed > 0 && additions === 0 && deletions === 0}<em>· {changed}</em>{/if}
    </button>
    {#if open}
      <div class="pop">
        {#if changed > 0}
          <div class="files">
            {#each files as f (f.path)}
              <div class="file">
                <span class="path" title={f.path}>{f.path}</span>
                {#if f.new}
                  <span class="stat new">new</span>
                {:else}
                  <span class="stat add">+{f.additions}</span>
                  <span class="stat del">−{f.deletions}</span>
                {/if}
              </div>
            {/each}
          </div>
          <div class="row">
            <input
              placeholder="Commit message"
              bind:value={message}
              onkeydown={(e) => e.key === 'Enter' && commit()}
            />
            <button class="btn-primary" onclick={commit} disabled={!message.trim() || committing}>
              {committing ? 'committing' : 'commit'}
            </button>
          </div>
        {:else}
          <span class="clean">working tree clean</span>
        {/if}
        {#if error}<span class="err">{error}</span>{/if}
      </div>
    {/if}
  </div>
{/if}

<style>
  .git {
    position: relative;
    /* Shrink below the chip's text when the header is tight. */
    min-width: 0;
  }
  .chip {
    display: inline-flex;
    align-items: baseline;
    gap: 6px;
    font-family: var(--mono);
    font-size: 11px;
    color: var(--text-muted);
    border: 1px solid var(--border);
    border-radius: 20px;
    padding: 2px 9px;
    white-space: nowrap;
    /* A long branch name truncates inside the chip instead of pushing
       the header's controls off a narrow screen; the full name is in
       the button's title. */
    max-width: min(200px, 34vw);
  }
  /* The branch name is the shrinkable part; the +N/−N counters stay. */
  .branch {
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
  }
  .chip:hover {
    color: var(--text);
    background: var(--surface-2);
  }
  .chip em {
    font-style: normal;
  }
  .chip .add {
    color: var(--ok);
  }
  .chip .del {
    color: var(--danger);
  }
  .pop {
    position: absolute;
    top: calc(100% + 6px);
    right: 0;
    z-index: 5;
    display: flex;
    flex-direction: column;
    gap: 8px;
    background: var(--bg);
    border: 1px solid var(--border-strong);
    border-radius: 8px;
    padding: 10px;
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.2);
    min-width: 360px;
  }
  /* Drawer tier: the chip sits mid-header, so a 360px popover anchored
     to it runs off a phone screen. Span the viewport under the header
     instead. */
  @media (max-width: 719px) {
    .pop {
      position: fixed;
      left: 12px;
      right: 12px;
      top: 54px;
      min-width: 0;
    }
  }
  .files {
    max-height: 180px;
    overflow-y: auto;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .file {
    display: flex;
    align-items: baseline;
    gap: 8px;
    font-family: var(--mono);
    font-size: 11.5px;
  }
  .path {
    flex: 1;
    min-width: 0;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    color: var(--text);
  }
  .stat {
    flex-shrink: 0;
  }
  .stat.add {
    color: var(--ok);
  }
  .stat.del {
    color: var(--danger);
  }
  .stat.new {
    color: var(--text-muted);
  }
  .row {
    display: flex;
    gap: 6px;
    align-items: center;
  }
  .row input {
    flex: 1;
    font: inherit;
    font-size: 12px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 4px 9px;
    outline: none;
  }
  .row input:focus {
    border-color: var(--accent);
  }
  .clean {
    font-size: 12px;
    color: var(--text-muted);
    padding: 0 4px;
  }
  .err {
    font-size: 11px;
    color: var(--danger);
  }
</style>
