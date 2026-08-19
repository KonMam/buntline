<script lang="ts">
  import { onMount } from 'svelte';
  import { api } from '../lib/api';
  import type { SessionState } from '../lib/session.svelte';
  import type { AgentInfo, SlashCommand } from '../lib/types';

  let { session, onclose }: { session: SessionState; onclose: () => void } = $props();

  let text = $state('');
  let overridden = $state(false);
  let path = $state('');
  let error = $state<string | null>(null);
  let saving = $state(false);

  // Level 2: the project instructions message this session loaded, if any.
  const instructions = $derived(session.messages.find((m) => m.kind === 'instructions'));

  // Level 3: the repository's named agents and commands, read-only here
  // (they are files under .buntline/).
  let agents = $state<AgentInfo[]>([]);
  let commands = $state<SlashCommand[]>([]);
  $effect(() => {
    const id = session.meta?.id;
    if (!id) return;
    api
      .sessionAgents(id)
      .then((r) => (agents = r.agents))
      .catch(() => (agents = []));
    api
      .commandsList(id)
      .then((r) => (commands = r.commands))
      .catch(() => (commands = []));
  });

  async function load() {
    const res = await api.getSystemPrompt();
    text = res.prompt;
    overridden = res.overridden;
    path = res.path;
  }

  async function save(prompt: string) {
    if (saving) return;
    saving = true;
    error = null;
    try {
      await api.setSystemPrompt(prompt);
      await load();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      saving = false;
    }
  }

  onMount(load);
</script>

<div
  class="backdrop"
  role="presentation"
  onclick={(e) => {
    if (e.target === e.currentTarget) onclose();
  }}
>
  <div class="panel" role="dialog" aria-label="Prompt layers">
    <header>
      <span>prompt layers</span>
      <button class="close" onclick={onclose}>close</button>
    </header>

    <div class="body">
      <section>
        <h2>1 · global system prompt {overridden ? '(customized)' : '(default)'}</h2>
        <p class="note">
          One prompt for the whole of buntline, kept small so it stays a stable, cacheable prefix of
          every request. Saved to <code>{path}</code>; applies to sessions opened after a change.
        </p>
        <textarea bind:value={text} spellcheck="false" rows="12"></textarea>
        <div class="actions">
          {#if error}<span class="err">{error}</span>{/if}
          <button class="ghost" onclick={() => save('')} disabled={saving || !overridden}>
            restore default
          </button>
          <button class="btn-primary" onclick={() => save(text)} disabled={saving || !text.trim()}>
            {saving ? 'saving' : 'save'}
          </button>
        </div>
      </section>

      <section>
        <h2>2 · project instructions</h2>
        {#if instructions}
          <p class="note">
            Delivered as the first message of every session in this folder. Edit the file in the
            working directory to change it.
          </p>
          <pre>{instructions.content}</pre>
        {:else}
          <p class="note">
            None loaded. Add an <code>AGENTS.md</code> or <code>CLAUDE.md</code> to the working directory
            and new sessions there will start with it.
          </p>
        {/if}
      </section>

      <section>
        <h2>3 · project agents and commands</h2>
        {#if agents.length === 0 && commands.length === 0}
          <p class="note">
            None defined. Agents live in <code>.buntline/agents/*.md</code> (name, description, tools
            in the front matter; the body is the agent's prompt) and commands in
            <code>.buntline/commands/*.md</code> (the body is sent as the message;
            <code>$ARGUMENTS</code> marks where the arguments go).
          </p>
        {:else}
          {#if agents.length > 0}
            <div class="tooling">
              {#each agents as a (a.name)}
                <div class="tool-row">
                  <span class="tool-name">{a.name}</span>
                  <span class="tool-desc">{a.description}</span>
                  <span class="tool-kind">{a.tools === 'all' ? 'all tools' : 'read-only'}</span>
                </div>
              {/each}
            </div>
          {/if}
          {#if commands.length > 0}
            <div class="tooling">
              {#each commands as c (c.name)}
                <div class="tool-row">
                  <span class="tool-name">/{c.name}</span>
                  <span class="tool-desc">{c.description}</span>
                </div>
              {/each}
            </div>
          {/if}
        {/if}
      </section>
    </div>
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
  .panel {
    width: min(700px, calc(100vw - 40px));
    max-height: calc(100vh - 60px);
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
  .body {
    overflow-y: auto;
    padding: 4px 16px 16px;
  }
  section {
    margin-top: 14px;
  }
  h2 {
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-muted);
    margin: 0 0 6px;
    font-weight: 600;
  }
  .note {
    font-size: 12px;
    color: var(--text-muted);
    margin: 0 0 8px;
    line-height: 1.5;
  }
  .note code {
    font-size: 11px;
  }
  textarea {
    width: 100%;
    font-family: var(--mono);
    font-size: 12px;
    line-height: 1.6;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 6px;
    outline: none;
    padding: 10px 12px;
    resize: vertical;
    box-sizing: border-box;
  }
  textarea:focus {
    border-color: var(--accent);
  }
  .actions {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: 10px;
    margin-top: 8px;
  }
  .err {
    font-size: 11.5px;
    color: var(--danger);
    margin-right: auto;
  }
  .ghost {
    font-size: 12px;
    color: var(--text-muted);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 4px 10px;
  }
  .ghost:hover:not(:disabled) {
    color: var(--text);
  }
  .ghost:disabled {
    opacity: 0.5;
    cursor: default;
  }
  .tooling {
    display: flex;
    flex-direction: column;
    gap: 2px;
    margin-bottom: 8px;
  }
  .tool-row {
    display: flex;
    align-items: baseline;
    gap: 10px;
    padding: 5px 10px;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--surface);
    font-size: 12px;
  }
  .tool-name {
    font-family: var(--mono);
    color: var(--text-strong);
    flex-shrink: 0;
  }
  .tool-desc {
    color: var(--text-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .tool-kind {
    margin-left: auto;
    font-size: 11px;
    color: var(--text-muted);
    flex-shrink: 0;
  }
  pre {
    margin: 0;
    padding: 10px 12px;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--surface);
    font-size: 11.5px;
    white-space: pre-wrap;
    word-break: break-word;
    max-height: 240px;
    overflow-y: auto;
    color: var(--text-muted);
  }
</style>
