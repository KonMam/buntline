<script lang="ts">
  import type { Message, ToolCall } from '../lib/types';
  import Icon from './Icon.svelte';
  import ToolCallCard from './ToolCallCard.svelte';

  // A single assistant message's tool calls, collapsed into one card
  // ("3 tool calls") when there are three or more. The card starts
  // collapsed; toggling shows every call. The collapse is per card
  // instance, not a global pref. It never spans messages: each message
  // is one discrete model call, so its own calls may fold together but
  // the thinking between model calls stays visible above/below.
  let open = $state(false);
  let {
    calls,
    messages,
    diffs,
    bg,
  }: {
    calls: ToolCall[];
    messages: Message[];
    diffs: Map<string, string>;
    bg?: Set<string>;
  } = $props();

  const summary = $derived(`${calls.length} tool ${calls.length === 1 ? 'call' : 'calls'}`);
</script>

<div class="run">
  <button class="run-head" onclick={() => (open = !open)} aria-expanded={open}>
    <Icon name="chevron" size={11} class={open ? 'flip' : ''} />
    <span class="run-name">{summary}</span>
  </button>
  {#if open}
    <div class="run-calls">
      {#each calls as call (call.id)}
        <ToolCallCard {call} {messages} {diffs} {bg} />
      {/each}
    </div>
  {/if}
</div>

<style>
  .run {
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--surface);
    font-size: 12.5px;
  }
  .run-head {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 7px 10px;
    text-align: left;
    cursor: pointer;
    user-select: none;
  }
  .run-head :global(svg) {
    color: var(--text-muted);
    transition: transform 0.12s ease;
    flex-shrink: 0;
  }
  .run-head :global(svg.flip) {
    transform: rotate(90deg);
  }
  .run-name {
    font-family: var(--mono);
    font-weight: 600;
    color: var(--text);
    flex: 1;
    min-width: 0;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .run-calls {
    display: flex;
    flex-direction: column;
    gap: 4px;
    border-top: 1px solid var(--border);
    padding: 6px;
  }
</style>
