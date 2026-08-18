<script lang="ts">
  let { diff }: { diff: string } = $props();

  const lines = $derived(
    diff.split('\n').map((text) => {
      let kind = 'ctx';
      if (text.startsWith('+++') || text.startsWith('---')) kind = 'file';
      else if (text.startsWith('@@')) kind = 'hunk';
      else if (text.startsWith('+')) kind = 'add';
      else if (text.startsWith('-')) kind = 'del';
      return { text, kind };
    }),
  );
</script>

<pre class="diff">{#each lines as line, i (i)}<span class={line.kind}
      >{line.text}
</span>{/each}</pre>

<style>
  .diff {
    margin: 0;
    padding: 8px 10px;
    overflow-x: auto;
    max-height: 320px;
    overflow-y: auto;
    line-height: 1.5;
  }
  .diff span {
    display: block;
    white-space: pre;
  }
  .add {
    color: var(--ok);
    background: color-mix(in srgb, var(--ok), transparent 92%);
  }
  .del {
    color: var(--danger);
    background: color-mix(in srgb, var(--danger), transparent 93%);
  }
  .hunk {
    color: var(--text-muted);
  }
  .file {
    color: var(--text-muted);
    font-weight: 600;
  }
</style>
