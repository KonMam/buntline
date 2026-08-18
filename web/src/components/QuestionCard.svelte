<script lang="ts">
  import type { PendingQuestion } from '../lib/types';

  let {
    question,
    answer,
  }: {
    question: PendingQuestion;
    answer: (text: string) => void;
  } = $props();

  let freeText = $state('');

  function submit(text: string) {
    const t = text.trim();
    if (!t) return;
    answer(t);
  }
</script>

<div class="card">
  <div class="head">
    <span class="tool">ask_user</span>
    <span class="asks">wants to know</span>
  </div>
  <div class="body">
    <p class="q">{question.question}</p>
    {#if question.options.length > 0}
      <div class="options">
        {#each question.options as opt (opt)}
          <button class="opt" onclick={() => submit(opt)}>{opt}</button>
        {/each}
      </div>
    {/if}
    <div class="free">
      <input
        bind:value={freeText}
        placeholder="or type a free answer"
        onkeydown={(e) => {
          if (e.key === 'Enter') submit(freeText);
        }}
      />
      <button class="send" onclick={() => submit(freeText)} disabled={!freeText.trim()}>
        answer
      </button>
    </div>
  </div>
</div>

<style>
  .card {
    border: 1px solid var(--accent);
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
  .body {
    padding: 10px 12px 12px;
    border-top: 1px solid var(--border);
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .q {
    margin: 0;
    font-size: 13.5px;
  }
  .options {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }
  .opt {
    font-size: 12.5px;
    padding: 5px 14px;
    border-radius: 6px;
    border: 1px solid var(--accent);
    color: var(--accent-contrast);
    background: var(--accent);
  }
  .opt:hover {
    filter: brightness(1.08);
  }
  .free {
    display: flex;
    gap: 8px;
  }
  .free input {
    flex: 1;
    font: inherit;
    font-size: 12.5px;
    padding: 5px 10px;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--surface-2);
    color: var(--text);
    outline: none;
  }
  .free input:focus {
    border-color: var(--border-strong);
  }
  .send {
    font-size: 12.5px;
    padding: 4px 12px;
    border-radius: 6px;
    border: 1px solid var(--border);
  }
  .send:hover:not(:disabled) {
    background: var(--surface-2);
  }
  .send:disabled {
    opacity: 0.5;
  }
</style>
