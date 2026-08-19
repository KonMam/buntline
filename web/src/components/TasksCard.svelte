<script lang="ts">
  // The tasks strip: the model's current task list, read-only. Fed by
  // the session's folded tasks state (last-write-wins over tasks
  // events), so it updates live as todo_write lands and survives a
  // reload from the replayed event stream. The model owns the list; the
  // user steers it by talking, so there are no editing controls here.
  // The card is collapsible: the header (label + live counts) always
  // stays visible, and the list tucks away when a long todo list is
  // eating the trace.
  import type { TaskItem } from '../lib/types';

  let { tasks }: { tasks: TaskItem[] } = $props();

  const counts = $derived.by(() => {
    const c = { pending: 0, in_progress: 0, completed: 0 };
    for (const t of tasks) c[t.status]++;
    return c;
  });

  // Render in a stable order: pending, in_progress, completed.
  const order: TaskItem['status'][] = ['pending', 'in_progress', 'completed'];
  const sorted = $derived(
    [...tasks].sort((a, b) => order.indexOf(a.status) - order.indexOf(b.status)),
  );
</script>

<details class="tasks" open>
  <summary>
    <span class="disclosure" aria-hidden="true"></span>
    <span class="label">tasks</span>
    <span class="counts">
      {#if counts.pending > 0}<span class="count pending">{counts.pending} pending</span>{/if}
      {#if counts.in_progress > 0}
        <span class="count in_progress">{counts.in_progress} in progress</span>
      {/if}
      {#if counts.completed > 0}
        <span class="count completed">{counts.completed} completed</span>
      {/if}
    </span>
  </summary>

  {#if sorted.length === 0}
    <div class="empty">no tasks yet</div>
  {:else}
    <ul>
      {#each sorted as t (t.content)}
        <li class={t.status}>
          <i class="dot"></i>
          <span class="content">{t.content}</span>
        </li>
      {/each}
    </ul>
  {/if}
</details>

<style>
  .tasks {
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface);
    padding: 9px 12px;
  }
  summary {
    display: flex;
    align-items: baseline;
    gap: 8px;
    flex-wrap: wrap;
    cursor: pointer;
    user-select: none;
    list-style: none;
  }
  summary::-webkit-details-marker {
    display: none;
  }
  .disclosure {
    color: var(--text-muted);
    font-family: var(--mono);
    width: 10px;
  }
  .disclosure::after {
    content: '+';
  }
  .tasks[open] .disclosure::after {
    content: '−';
  }
  summary:hover .label {
    color: var(--text-strong);
  }
  .label {
    font-size: 10.5px;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-muted);
  }
  .counts {
    display: flex;
    gap: 8px;
    margin-left: auto;
    font-family: var(--mono);
    font-size: 10.5px;
    color: var(--text-muted);
  }
  .count.pending {
    color: var(--text-muted);
  }
  .count.in_progress {
    color: var(--accent);
  }
  .count.completed {
    color: var(--ok);
  }
  .empty {
    color: var(--text-muted);
    font-size: 12.5px;
    margin-top: 7px;
  }
  ul {
    list-style: none;
    margin: 7px 0 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  li {
    display: flex;
    align-items: baseline;
    gap: 7px;
    font-size: 12.5px;
    color: var(--text);
  }
  li .dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    flex-shrink: 0;
    align-self: center;
    background: var(--text-muted);
  }
  li.in_progress .dot {
    background: var(--accent);
  }
  li.completed .dot {
    background: var(--ok);
  }
  li.completed .content {
    color: var(--text-muted);
    text-decoration: line-through;
  }
  .content {
    min-width: 0;
    overflow-wrap: anywhere;
  }
</style>
