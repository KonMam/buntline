<script lang="ts">
  import type { Message } from '../lib/types';
  import { copyText } from '../lib/clipboard';
  import { renderMarkdown } from '../lib/markdown';
  import { shouldCollapse } from '../lib/toolCalls';
  import Icon from './Icon.svelte';
  import ToolCallCard from './ToolCallCard.svelte';
  import ToolRunCard from './ToolRunCard.svelte';

  let copied = $state(false);
  async function copy(text: string) {
    await copyText(text);
    copied = true;
    setTimeout(() => (copied = false), 1200);
  }

  let {
    msg,
    messages,
    diffs,
    bg,
    onfork,
    onedit,
    flat = false,
  }: {
    msg: Message;
    messages: Message[];
    diffs: Map<string, string>;
    bg?: Set<string>;
    onfork?: () => void;
    onedit?: () => void;
    // flat renders tool calls as individual cards even at the
    // per-message fold threshold: inside a ToolRunGroup the group is
    // already the fold, and a fold within a fold reads as a maze.
    flat?: boolean;
  } = $props();

  // Markdown applies to assistant replies and to the compaction summary:
  // the summary arrives as a user-role message (it must survive the
  // round-trip as user context, not assistant text), but it is still
  // model-written prose and renders the same way.
  const html = $derived(
    (msg.role === 'assistant' || msg.kind === 'summary') && msg.content
      ? renderMarkdown(msg.content)
      : '',
  );

  // Machinery messages are harness-generated user-role context: project
  // instructions (AGENTS.md), the memory index, background tool results,
  // and repaired tool results. They render collapsed under a label that
  // says what they are, never as a real user bubble.
  const kindLabel = (kind: string) => {
    switch (kind) {
      case 'instructions':
        return 'project instructions loaded';
      case 'memory':
        return 'memory loaded';
      case 'background':
        return 'background task finished';
      case 'tool_result':
        return 'tool result';
      default:
        return kind;
    }
  };
</script>

{#if msg.kind && msg.kind !== 'summary'}
  {#if msg.role === 'user'}
    <details class="instructions">
      <summary>{kindLabel(msg.kind)}</summary>
      <pre>{msg.content}</pre>
    </details>
  {/if}
  <!-- assistant-role machinery (the project-instructions acknowledgement)
       is not rendered -->
{:else if msg.role === 'user' && msg.kind !== 'summary'}
  <div class="user">
    <div class="head">
      <span class="label">you</span>
      <div class="actions">
        {#if onedit}
          <button
            class="icon-btn"
            onclick={onedit}
            title="Edit and resend, in a fork"
            aria-label="Edit and resend, in a fork"
          >
            <Icon name="pencil" size={13} />
          </button>
        {/if}
        {#if onfork}
          <button
            class="icon-btn"
            onclick={onfork}
            title="Fork from here"
            aria-label="Fork from here"
          >
            <Icon name="branch" size={13} />
          </button>
        {/if}
        <button
          class="icon-btn"
          onclick={() => copy(msg.content)}
          title="Copy message"
          aria-label="Copy message"
        >
          <Icon name={copied ? 'check' : 'copy'} size={13} />
        </button>
      </div>
    </div>
    {#if msg.images?.length}
      <div class="images">
        {#each msg.images as img, i (i)}
          <img src={img} alt="attachment {i + 1}" />
        {/each}
      </div>
    {/if}
    {#if msg.content}
      <div class="body">{msg.content}</div>
    {/if}
  </div>
{:else if msg.role === 'assistant' || msg.kind === 'summary'}
  <div class="assistant">
    {#if msg.kind === 'summary'}
      <div class="summary-head">
        <span class="label">summary</span>
      </div>
    {/if}
    {#if msg.tool_calls?.length && !flat && shouldCollapse(msg.tool_calls)}
      <ToolRunCard calls={msg.tool_calls} {messages} {diffs} {bg} />
    {:else if msg.tool_calls?.length}
      <div class="calls">
        {#each msg.tool_calls as call (call.id)}
          <ToolCallCard {call} {messages} {diffs} {bg} />
        {/each}
      </div>
    {/if}
    {#if msg.thinking}
      <details class="thinking">
        <summary>thinking</summary>
        <!-- eslint-disable-next-line svelte/no-at-html-tags -- sanitized by DOMPurify -->
        <div class="md">{@html renderMarkdown(msg.thinking)}</div>
      </details>
    {/if}
    {#if msg.content}
      <!-- eslint-disable-next-line svelte/no-at-html-tags -- sanitized by DOMPurify -->
      <div class="md">{@html html}</div>
    {/if}
    {#if msg.content}
      <div class="actions reply-actions">
        <button
          class="icon-btn"
          onclick={() => copy(msg.content)}
          title="Copy reply"
          aria-label="Copy reply"
        >
          <Icon name={copied ? 'check' : 'copy'} size={13} />
        </button>
      </div>
    {/if}
  </div>
{/if}

<!-- RoleTool messages render inside their call's details, not standalone. -->

<style>
  .instructions {
    font-size: 12px;
    color: var(--text-muted);
  }
  .instructions summary {
    cursor: pointer;
    user-select: none;
    font-family: var(--mono);
    font-size: 11.5px;
  }
  .instructions pre {
    margin: 6px 0 0;
    padding: 8px 10px;
    border: 1px solid var(--border);
    border-radius: 6px;
    white-space: pre-wrap;
    word-break: break-word;
    max-height: 260px;
    overflow-y: auto;
  }
  .user {
    position: relative;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 10px 14px;
  }
  /* Actions live in reserved rows (the label row, a footer row), never
     floated over content. */
  .actions {
    display: flex;
    gap: 2px;
    opacity: 0;
  }
  .user:hover .actions,
  .assistant:hover .actions {
    opacity: 1;
  }
  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 3px;
    min-height: 18px;
  }
  .reply-actions {
    justify-content: flex-end;
    margin-top: -4px;
  }
  .label {
    font-size: 10.5px;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-muted);
  }
  .summary-head {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .body {
    white-space: pre-wrap;
    word-break: break-word;
  }
  .images {
    display: flex;
    gap: 8px;
    flex-wrap: wrap;
    margin-bottom: 6px;
  }
  .images img {
    max-height: 200px;
    max-width: 100%;
    border-radius: 6px;
    border: 1px solid var(--border);
  }
  .assistant {
    position: relative;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .thinking {
    color: var(--text-muted);
    font-size: 13px;
  }
  .thinking summary {
    cursor: pointer;
    user-select: none;
    font-size: 12px;
  }
  .thinking div {
    word-break: break-word;
    border-left: 2px solid var(--border);
    padding-left: 10px;
    margin-top: 4px;
  }
  .calls {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
</style>
