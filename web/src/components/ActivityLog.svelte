<script lang="ts">
  import type { AgentEvent } from '../lib/types';
  import { formatDuration, formatTime, formatTokens, prettyArgs } from '../lib/format';

  let { events }: { events: AgentEvent[] } = $props();

  // The log shows structural events; message events duplicate the chat.
  const visible = $derived(events.filter((e) => e.type !== 'message'));

  let scroller = $state<HTMLElement | null>(null);
  $effect(() => {
    void visible.length;
    if (scroller) scroller.scrollTop = scroller.scrollHeight;
  });

  function describe(ev: AgentEvent): string {
    switch (ev.type) {
      case 'turn_start':
        return 'turn started';
      case 'turn_end':
        return `turn ended (${ev.stop_reason})`;
      case 'tool_start':
        return `${ev.tool_name} ${prettyArgs(ev.tool_args ?? '', 80)}`;
      case 'tool_bg':
        return `${ev.tool_name} moved to the background`;
      case 'tool_end':
        return ev.error
          ? `${ev.tool_name} failed: ${ev.error}`
          : `${ev.tool_name} done in ${formatDuration(ev.duration_ms ?? 0)}`;
      case 'approval_request':
        return `approval: ${ev.tool_name}`;
      case 'approval_result':
        return `approval: ${ev.tool_name} → ${ev.decision}`;
      case 'usage': {
        const u = ev.usage!;
        const cached = u.cached_tokens > 0 ? ` (${formatTokens(u.cached_tokens)} cached)` : '';
        return `${formatTokens(u.prompt_tokens)} prompt${cached} → ${formatTokens(u.completion_tokens)} completion`;
      }
      case 'compact':
        return 'transcript compacted';
      case 'error':
        return ev.error ?? 'error';
      default:
        return ev.type;
    }
  }

  function klass(ev: AgentEvent): string {
    switch (ev.type) {
      case 'error':
        return 'err';
      case 'usage':
        return 'usage';
      case 'approval_request':
      case 'approval_result':
        return 'approval';
      case 'tool_start':
      case 'tool_end':
      case 'tool_bg':
        return 'tool';
      default:
        return 'meta';
    }
  }
</script>

<aside>
  <header>activity</header>
  <div class="rows" bind:this={scroller}>
    {#each visible as ev, i (i)}
      <div class="row {klass(ev)}">
        <span class="time">{formatTime(ev.time)}</span>
        <span class="desc">{describe(ev)}</span>
      </div>
    {/each}
    {#if visible.length === 0}
      <div class="empty">every model call, tool run, and approval lands here</div>
    {/if}
  </div>
</aside>

<style>
  aside {
    border-left: 1px solid var(--border);
    background: var(--surface);
    display: flex;
    flex-direction: column;
    min-height: 0;
  }
  header {
    padding: 12px 14px;
    border-bottom: 1px solid var(--border);
    font-size: 10.5px;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-muted);
  }
  .rows {
    flex: 1;
    overflow-y: auto;
    padding: 8px 0;
    font-family: var(--mono);
    font-size: 11.5px;
  }
  .row {
    display: flex;
    gap: 8px;
    padding: 2.5px 14px;
    align-items: baseline;
  }
  .time {
    color: var(--text-muted);
    flex-shrink: 0;
  }
  .desc {
    word-break: break-word;
    min-width: 0;
  }
  .row.meta .desc {
    color: var(--text-muted);
  }
  .row.tool .desc {
    color: var(--text);
  }
  .row.usage .desc {
    color: var(--ok);
  }
  .row.approval .desc {
    color: var(--warn);
  }
  .row.err .desc {
    color: var(--danger);
  }
  .empty {
    padding: 14px;
    color: var(--text-muted);
    font-family: var(--sans);
    font-size: 12.5px;
  }
</style>
