<script lang="ts">
  import { onMount } from 'svelte';
  import { api } from '../lib/api';
  import type { SessionState } from '../lib/session.svelte';
  import type { Profile } from '../lib/types';
  import Dropdown from './Dropdown.svelte';

  let {
    session,
  }: {
    session: SessionState;
  } = $props();

  let profiles = $state<Profile[]>([]);
  let defaultModels = $state<string[]>([]);

  // Options: every model on the default endpoint (listed via the
  // generic /v1/models call; Ollama, LM Studio, llama.cpp, vLLM all
  // answer it), plus named profiles for other endpoints. Value encodes
  // both.
  const options = $derived.by(() => {
    const opts: { value: string; label: string; hint?: string; model: string; profile: string }[] =
      [];
    for (const m of defaultModels) {
      opts.push({ value: `default:${m}`, label: m, model: m, profile: 'default' });
    }
    for (const p of profiles) {
      if (p.name === 'default') continue;
      opts.push({
        value: `${p.name}:${p.model}`,
        label: `${p.name} (${p.model})`,
        hint: p.key_missing ? 'key missing' : undefined,
        model: p.model,
        profile: p.name,
      });
    }
    const current = `${session.meta?.profile || 'default'}:${session.meta?.model ?? ''}`;
    if (session.meta && !opts.some((o) => o.value === current)) {
      opts.unshift({
        value: current,
        label: session.meta.model,
        model: session.meta.model,
        profile: session.meta.profile || 'default',
      });
    }
    return opts;
  });

  const current = $derived(`${session.meta?.profile || 'default'}:${session.meta?.model ?? ''}`);

  function pick(value: string) {
    const opt = options.find((o) => o.value === value);
    if (opt) void session.setModel(opt.model, opt.profile);
  }

  onMount(async () => {
    profiles = await api.profiles();
  });

  // The default endpoint's models, via the generic listing. Works for
  // any local server, and degrades to an empty list (profiles still
  // work) when the endpoint is unreachable.
  $effect(() => {
    api
      .providerModels('default')
      .then((ms) => (defaultModels = ms))
      .catch(() => {
        defaultModels = [];
      });
  });
</script>

<Dropdown
  {options}
  value={current}
  onselect={pick}
  disabled={session.busy}
  title="model for this session"
/>
