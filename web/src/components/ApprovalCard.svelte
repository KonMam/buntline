<script lang="ts">
  import type { PendingApproval } from '../lib/types';
  import { linkifyText } from '../lib/markdown';

  let {
    approval,
    decide,
  }: {
    approval: PendingApproval;
    decide: (decision: string) => void;
  } = $props();

  const args = $derived.by(() => {
    try {
      return JSON.stringify(JSON.parse(approval.tool_args), null, 2);
    } catch {
      return approval.tool_args;
    }
  });
  // File paths in the args are clickable, like on the tool call cards.
  const argsHtml = $derived(linkifyText(args));
</script>

<div class="card">
  <div class="head">
    <span class="tool">{approval.tool_name}</span>
    <span class="asks">wants to run</span>
  </div>
  <pre>{@html argsHtml}</pre>
  <div class="actions">
    <button class="allow" onclick={() => decide('allow')}>allow once</button>
    <button class="allow-session" onclick={() => decide('allow_session')}>
      allow for session
    </button>
    <button
      class="allow-session"
      onclick={() => decide('allow_always')}
      title="Adds a rule to this repository's .buntline/settings.json; bash rules match the command's first words"
    >
      always allow in this repo
    </button>
    <button class="deny" onclick={() => decide('deny')}>deny</button>
  </div>
</div>

<style>
  .card {
    border: 1px solid var(--warn);
    border-radius: 8px;
    background: var(--surface);
    overflow: hidden;
  }
  .head {
    display: flex;
    gap: 8px;
    align-items: baseline;
    padding: 9px 12px;
  }
  .tool {
    font-family: var(--mono);
    font-weight: 600;
  }
  .asks {
    color: var(--text-muted);
    font-size: 12.5px;
  }
  pre {
    margin: 0;
    padding: 8px 12px;
    border-top: 1px solid var(--border);
    border-bottom: 1px solid var(--border);
    background: var(--surface-2);
    white-space: pre-wrap;
    word-break: break-word;
    max-height: 240px;
    overflow-y: auto;
  }
  .actions {
    display: flex;
    gap: 8px;
    padding: 9px 12px;
  }
  .actions button {
    font-size: 12.5px;
    padding: 4px 12px;
    border-radius: 6px;
    border: 1px solid var(--border);
  }
  .allow {
    background: var(--accent);
    border-color: var(--accent);
    color: var(--accent-contrast);
  }
  .allow-session:hover,
  .deny:hover {
    background: var(--surface-2);
  }
  .deny {
    color: var(--danger);
    border-color: var(--danger);
  }
</style>
