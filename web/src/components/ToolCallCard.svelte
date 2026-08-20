<script lang="ts">
  import type { Message, ToolCall } from '../lib/types';
  import { prettyArgs } from '../lib/format';
  import { linkifyText } from '../lib/markdown';
  import DiffView from './DiffView.svelte';
  import Icon from './Icon.svelte';

  let {
    call,
    messages,
    diffs,
    bg = new Set<string>(),
  }: {
    call: ToolCall;
    messages: Message[];
    diffs: Map<string, string>;
    bg?: Set<string>;
  } = $props();

  // The tool result for a call lives in a later RoleTool message. A
  // backgrounded tool leaves TWO: the placeholder ("started ... working
  // in the background") first, then the real result when it lands. The
  // last one is the one to show.
  function resultFor(callId: string): string | null {
    let found: string | null = null;
    for (const m of messages) {
      if (m.role === 'tool' && m.tool_call_id === callId) found = m.content;
    }
    return found;
  }

  const result = $derived(resultFor(call.id));
  const diff = $derived(diffs.get(call.id));
  // A call is running in the background from its tool_bg event until
  // the matching tool_end (which arrives with the real result). During
  // that window the transcript's result is the placeholder, or nothing
  // yet, if the placeholder has not landed.
  const running = $derived(bg.has(call.id) && (result === null || result.startsWith('[started ')));
  const failed = $derived(result !== null && /^error:/i.test(result));
  // File paths in the args and the result are clickable (open in the
  // file browser; the chat thread handles the clicks).
  const argsHtml = $derived(linkifyText(call.args));
  const resultHtml = $derived(result !== null ? linkifyText(result) : '');
</script>

<details class="call" class:running>
  <summary>
    <span class="tool-name">{call.name}</span>
    <span class="tool-args">{prettyArgs(call.args)}</span>
    {#if running}
      <span class="running-label">background</span>
    {/if}
    {#if failed}
      <span class="fail-icon"><Icon name="alert" size={12} /></span>
    {/if}
  </summary>
  <pre class="args">{@html argsHtml}</pre>
  {#if diff}
    <DiffView {diff} />
  {:else if result !== null && !running}
    <pre class="result">{@html resultHtml}</pre>
  {:else if running}
    <div class="pending">
      <i class="spinner"></i>
      running in the background; the result arrives when it finishes
    </div>
  {/if}
</details>

<style>
  .call {
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--surface);
    font-size: 12.5px;
  }
  .call summary {
    display: flex;
    gap: 8px;
    align-items: baseline;
    padding: 6px 10px;
    cursor: pointer;
    user-select: none;
    overflow: hidden;
  }
  .tool-name {
    font-family: var(--mono);
    font-weight: 600;
    color: var(--accent);
    flex-shrink: 0;
  }
  .tool-args {
    font-family: var(--mono);
    color: var(--text-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
  }
  .fail-icon {
    color: var(--danger);
    flex-shrink: 0;
    margin-left: auto;
    display: inline-flex;
  }
  .call.running {
    border-color: var(--border-strong);
  }
  .running-label {
    font-size: 10.5px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--text-muted);
    margin-left: auto;
    flex-shrink: 0;
    display: inline-flex;
    align-items: center;
    gap: 5px;
  }
  .pending {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 10px;
    border-top: 1px solid var(--border);
    color: var(--text-muted);
    font-size: 12px;
  }
  .spinner {
    width: 12px;
    height: 12px;
    flex-shrink: 0;
    border: 1.5px solid var(--border-strong);
    border-top-color: var(--accent);
    border-radius: 50%;
    animation: spin 0.9s linear infinite;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
  .args,
  .result {
    margin: 0;
    padding: 8px 10px;
    border-top: 1px solid var(--border);
    white-space: pre-wrap;
    word-break: break-word;
    max-height: 300px;
    overflow-y: auto;
  }
  .args {
    color: var(--text-muted);
  }
</style>
