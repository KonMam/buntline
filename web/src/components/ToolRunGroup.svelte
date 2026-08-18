<script lang="ts">
  import type { Message } from '../lib/types';
  import Icon from './Icon.svelte';
  import MessageView from './Message.svelte';

  // A run of consecutive model calls that produced only tool calls, no
  // visible text, folded into one card. This is the cross-message
  // counterpart of ToolRunCard: one model call with many tool calls
  // folds there; many single-call model calls fold here. Expanding shows
  // each member message as it would normally render (its thinking, its
  // calls, its results). The live tail of a running turn is never
  // grouped (Chat.svelte only builds a group once the run has settled),
  // so watching the agent work stays possible.
  let open = $state(false);
  let {
    members,
    messages,
    diffs,
    bg,
  }: {
    members: { msg: Message; index: number; key: string }[];
    messages: Message[];
    diffs: Map<string, string>;
    bg?: Set<string>;
  } = $props();

  // "8 tool calls · bash ×6 · grep ×2", names in first-appearance order.
  const summary = $derived.by(() => {
    const counts = new Map<string, number>();
    let total = 0;
    for (const m of members) {
      for (const call of m.msg.tool_calls ?? []) {
        counts.set(call.name, (counts.get(call.name) ?? 0) + 1);
        total++;
      }
    }
    const parts = [...counts.entries()].map(([name, n]) => (n > 1 ? `${name} ×${n}` : name));
    return { total, detail: parts.join(' · ') };
  });
</script>

<div class="group">
  <button class="group-head" onclick={() => (open = !open)} aria-expanded={open}>
    <Icon name="chevron" size={11} class={open ? 'flip' : ''} />
    <span class="group-name">{summary.total} tool calls</span>
    <span class="group-detail">{summary.detail}</span>
  </button>
  {#if open}
    <div class="group-body">
      {#each members as m (m.key)}
        <MessageView msg={m.msg} {messages} {diffs} {bg} flat />
      {/each}
    </div>
  {/if}
</div>

<style>
  .group {
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--surface);
    font-size: 12.5px;
  }
  .group-head {
    display: flex;
    align-items: baseline;
    gap: 8px;
    width: 100%;
    padding: 7px 10px;
    text-align: left;
    cursor: pointer;
    user-select: none;
    min-width: 0;
  }
  .group-head :global(svg) {
    color: var(--text-muted);
    align-self: center;
    flex-shrink: 0;
    transition: transform 0.12s ease;
  }
  .group-head :global(svg.flip) {
    transform: rotate(180deg);
  }
  .group-name {
    font-family: var(--mono);
    font-weight: 600;
    color: var(--text);
    flex-shrink: 0;
  }
  .group-detail {
    font-family: var(--mono);
    color: var(--text-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .group-body {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 8px 10px 10px;
    border-top: 1px solid var(--border);
  }
</style>
