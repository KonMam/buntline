<script lang="ts">
  import type { SessionState } from '../lib/session.svelte';
  import Dropdown from './Dropdown.svelte';

  let { session }: { session: SessionState } = $props();

  const modes = [
    { value: 'ask', label: 'Approve each action' },
    { value: 'plan', label: 'Plan, read-only' },
    { value: 'auto_edit', label: 'Auto-approve edits' },
    { value: 'auto', label: 'Auto-approve all' },
  ];

  const current = $derived(session.meta?.mode || 'ask');
</script>

<Dropdown
  options={modes}
  value={current}
  label="mode"
  onselect={(v) => void session.setMode(v)}
  title="Approval mode for this session, effective from the next tool call"
/>
