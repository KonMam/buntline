<script lang="ts">
  import { onMount } from 'svelte';
  import { api } from '../lib/api';
  import type { SessionState } from '../lib/session.svelte';
  import type { AppProvider, CatalogProvider, OllamaModel } from '../lib/types';
  import { formatBytes } from '../lib/format';
  import Icon from './Icon.svelte';

  let {
    session,
    ollamaEnabled,
  }: {
    session: SessionState;
    ollamaEnabled: boolean;
  } = $props();

  // --- providers (catalog + app-managed) ---
  let providers = $state<CatalogProvider[]>([]);
  let appProviders = $state<AppProvider[]>([]);
  let error = $state<string | null>(null);

  // --- pane state (shared by every provider) ---
  let setupName = $state<string | null>(null);
  let setupModels = $state<string[]>([]); // live /v1/models
  let setupModelsError = $state<string | null>(null);
  let loadingModels = $state(false);
  let keyValue = $state('');
  let keySaving = $state(false);
  let chosenModel = $state<string | null>(null);
  let setupCustomModel = $state('');
  let saving = $state(false);
  let paneMsg = $state<string | null>(null);
  let keyError = $state<string | null>(null);

  // --- Ollama installed models (module-gated) ---
  let models = $state<OllamaModel[]>([]);
  let ollamaError = $state<string | null>(null);
  let pullName = $state('');
  let pulling = $state(false);
  let pullStatus = $state('');
  let pullPct = $state(0);

  const fitLabel: Record<string, string> = {
    comfortable: 'fits well',
    tight: 'fits, tight',
    too_large: 'too large',
    unknown: 'unknown fit',
  };

  const hosted = $derived(providers.filter((p) => !p.local));
  const local = $derived(providers.filter((p) => p.local));
  const setupProvider = $derived(
    setupName ? (providers.find((p) => p.name === setupName) ?? null) : null,
  );
  const isLocal = $derived(!!setupProvider?.local);
  const needsKey = $derived(!isLocal && !!setupProvider?.env);
  const keyPresent = $derived(!!setupProvider && !setupProvider.key_missing);
  const isOllamaPane = $derived(setupName === 'ollama');

  // Models added for this provider (from providers.json). A provider can
  // hold several; each card shows its own state.
  const addedModels = $derived(
    appProviders.filter((a) => a.name === setupName).map((a) => a.model),
  );
  const defaultModel = $derived(
    appProviders.find((a) => a.name === setupName && a.default)?.model ?? null,
  );

  // Curated catalog models, always shown so a configured model stays
  // visible even when the live list differs.
  const catalogModelNames = $derived(
    setupProvider?.models?.map((m) => ({ name: m.name, label: m.label })) ?? [],
  );
  // Live models merged in (deduped), catalog first, live after.
  const modelOptions = $derived.by(() => {
    const seen = new Set<string>();
    const out: { name: string; label?: string }[] = [];
    for (const m of catalogModelNames) {
      if (!seen.has(m.name)) {
        seen.add(m.name);
        out.push(m);
      }
    }
    for (const m of setupModels) {
      if (!seen.has(m)) {
        seen.add(m);
        out.push({ name: m });
      }
    }
    return out;
  });

  // One unified list of cards for the pane: for Ollama the installed
  // local models (with size/fit), for everything else the model options.
  const paneModels = $derived.by(() => {
    type Row = {
      name: string;
      label?: string;
      fit?: OllamaModel['fit'];
      active: boolean;
      isDefault: boolean;
    };
    if (isOllamaPane && ollamaEnabled) {
      return models.map((m): Row => ({
        name: m.name,
        label: `${m.params} · ${m.quant} · ${formatBytes(m.size)}`,
        fit: m.fit,
        active: addedModels.includes(m.name),
        isDefault: m.name === defaultModel,
      }));
    }
    return modelOptions.map((m): Row => ({
      name: m.name,
      label: m.label ?? '',
      active: addedModels.includes(m.name),
      isDefault: m.name === defaultModel,
    }));
  });

  async function refreshAll() {
    await Promise.all([refreshProviders(), refreshOllama()]);
  }

  async function refreshProviders() {
    try {
      const [provs, apps] = await Promise.all([api.providers(), api.appProviders()]);
      providers = provs;
      appProviders = apps;
      error = null;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  async function saveKey(): Promise<boolean> {
    const p = setupProvider;
    if (!p || !p.env || !keyValue.trim() || keySaving) return false;
    keySaving = true;
    keyError = null;
    try {
      await api.setSecret(p.env, keyValue);
      keyValue = '';
      await refreshProviders();
      // Live models only for providers that need them (no curated list).
      if (shouldListLive(p)) {
        await loadSetupModels(p);
      }
      paneMsg = 'key saved';
      return true;
    } catch (e) {
      keyError = e instanceof Error ? e.message : String(e);
      return false;
    } finally {
      keySaving = false;
    }
  }

  async function removeKey() {
    const p = setupProvider;
    if (!p || !p.env) return;
    try {
      await api.deleteSecret(p.env);
      await refreshProviders();
      paneMsg = 'key removed';
    } catch (e) {
      keyError = e instanceof Error ? e.message : String(e);
    }
  }

  // --- Ollama ---
  async function refreshOllama() {
    if (!ollamaEnabled) return;
    try {
      const res = await api.ollamaModels();
      models = res.models;
      ollamaError = null;
    } catch (e) {
      ollamaError = e instanceof Error ? e.message : String(e);
    }
  }

  async function removeInstalled(name: string) {
    try {
      await api.ollamaDelete(name);
      if (addedModels.includes(name)) {
        // Removing the installed model also drops its added entry.
        await api.deleteAppProvider('ollama', name);
        await refreshProviders();
      }
      await refreshOllama();
      paneMsg = `removed ${name}`;
    } catch (e) {
      ollamaError = e instanceof Error ? e.message : String(e);
    }
  }

  async function pull() {
    const name = pullName.trim();
    if (!name || pulling) return;
    pulling = true;
    pullStatus = 'starting';
    pullPct = 0;
    try {
      await api.ollamaPull(name, (p) => {
        pullStatus = String(p.status ?? '');
        const total = Number(p.total ?? 0);
        const completed = Number(p.completed ?? 0);
        if (total > 0) pullPct = completed / total;
      });
      pullName = '';
      pullStatus = '';
      await refreshOllama();
    } catch (e) {
      pullStatus = e instanceof Error ? e.message : String(e);
    } finally {
      pulling = false;
    }
  }

  // Live models are only needed when the provider has no curated
  // catalog list: local servers (LM Studio, llama.cpp, vLLM) must be
  // asked what they serve. Providers with a curated list (DeepSeek,
  // Z.AI, ...) show that stable list; fetching live models there makes
  // extra cards pop in after the pane opens. Anything the catalog does
  // not list can still be typed in the model-name box.
  function shouldListLive(p: CatalogProvider): boolean {
    return !p.models || p.models.length === 0;
  }

  // --- pane flow ---
  async function openSetup(name: string) {
    setupName = name;
    keyValue = '';
    keyError = null;
    chosenModel = null;
    setupCustomModel = '';
    setupModels = [];
    setupModelsError = null;
    paneMsg = null;
    const p = providers.find((c) => c.name === name);
    if (!p) return;
    if (name === 'ollama' && ollamaEnabled) {
      await refreshOllama();
    }
    // Load live models when the provider needs them and the key (if
    // any) is present.
    if (shouldListLive(p) && (p.local || !p.key_missing)) {
      await loadSetupModels(p);
    }
  }

  async function loadSetupModels(p: CatalogProvider) {
    loadingModels = true;
    setupModelsError = null;
    try {
      setupModels = await api.providerModels(p.name);
    } catch (e) {
      setupModelsError = e instanceof Error ? e.message : String(e);
      setupModels = [];
    } finally {
      loadingModels = false;
    }
  }

  function selectModel(name: string) {
    chosenModel = name;
    setupCustomModel = '';
    paneMsg = null;
  }

  // Add a model to this provider as a (provider, model) entry and
  // switch the active session to it. Adding or re-using a model never
  // changes what is default; that is an explicit choice through the
  // star button on the model card.
  async function addModel() {
    const p = setupProvider;
    const model = chosenModel || setupCustomModel.trim();
    if (!p || !model || saving) return;
    saving = true;
    paneMsg = null;
    try {
      const entry: AppProvider = {
        name: p.name,
        base_url: p.base_url,
        model,
        env: p.env,
        label: p.label,
        tag: p.tag,
        local: p.local,
        models: p.models,
      };
      await api.putAppProvider(entry);
      await refreshProviders();
      if (session.meta) {
        await session.setModel(model, p.name);
      }
      paneMsg = `added ${model}`;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      saving = false;
    }
  }

  // Set or clear which added model new sessions start on. Explicit,
  // per-model: adding a model never makes it the default.
  async function setDefault(name: string, isDefault: boolean) {
    if (!setupName || saving) return;
    saving = true;
    paneMsg = null;
    try {
      await api.setAppDefault(setupName, name, isDefault);
      await refreshProviders();
      paneMsg = isDefault ? `${name} is the default` : 'default cleared';
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      saving = false;
    }
  }

  // Remove one added model for this provider (the (name, model) entry).
  async function removeAdded(name: string) {
    if (!setupName || saving) return;
    saving = true;
    paneMsg = null;
    try {
      await api.deleteAppProvider(setupName, name);
      await refreshProviders();
      if (chosenModel === name) chosenModel = null;
      paneMsg = 'removed';
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      saving = false;
    }
  }

  function closePane() {
    setupName = null;
    setupModels = [];
    chosenModel = null;
  }

  // Pasted key detects its provider.
  async function onKeyInput(value: string) {
    const k = value.trim();
    if (!k) return;
    try {
      const res = await api.detectProvider(k);
      if (res.provider && res.provider !== setupName) await openSetup(res.provider);
    } catch {
      // detection is best-effort
    }
  }

  onMount(() => {
    void refreshAll();
  });
</script>

<div class="page">
  {#if setupName}
    <!-- one unified pane for every provider -->
    <div class="pane">
      <button class="back" onclick={closePane}><Icon name="back" size={13} /> back</button>
      <header>
        <h1>{setupProvider?.label ?? setupName}</h1>
        {#if setupProvider?.tag}<p>{setupProvider.tag}</p>{/if}
      </header>

      <!-- key: hosted providers only -->
      {#if needsKey}
        <div class="key-step">
          <label for="setup-key">API key</label>
          <div class="key-row">
            <input
              id="setup-key"
              type="password"
              placeholder={setupProvider?.env}
              bind:value={keyValue}
              oninput={(e) => onKeyInput((e.target as HTMLInputElement).value)}
              onkeydown={(e) => e.key === 'Enter' && saveKey()}
            />
            <button class="btn-primary" onclick={saveKey} disabled={!keyValue.trim() || keySaving}>
              {keySaving ? 'saving…' : 'save key'}
            </button>
          </div>
          {#if keyPresent}
            <div class="key-status">
              <span class="ok">key set</span>
              <button class="link-btn" onclick={removeKey}>remove</button>
            </div>
          {/if}
          {#if keyError}<div class="error">{keyError}</div>{/if}
        </div>
      {/if}

      <!-- download: Ollama only, above the model list -->
      {#if isOllamaPane && ollamaEnabled}
        <div class="toolbar">
          <input
            placeholder="Download a model, e.g. qwen3.5:14b"
            bind:value={pullName}
            disabled={pulling}
            onkeydown={(e) => e.key === 'Enter' && pull()}
          />
          <button class="btn-primary" onclick={pull} disabled={pulling || !pullName.trim()}>
            <Icon name="download" size={14} />
            {pulling ? 'downloading' : 'download'}
          </button>
          {#if pullStatus}
            <span class="progress">
              {pullStatus}{pulling && pullPct > 0 ? ` · ${Math.round(pullPct * 100)}%` : ''}
            </span>
          {/if}
          {#if ollamaError}<div class="error">{ollamaError}</div>{/if}
        </div>
      {/if}

      <!-- models: the same list for every provider -->
      <div class="models">
        <div class="models-head">
          <h2>Model</h2>
          {#if loadingModels}<span class="muted">listing…</span>{/if}
          {#if setupModelsError && paneModels.length === 0}
            <span class="muted error-text">{setupModelsError}</span>
          {/if}
        </div>

        <div class="model-cards">
          {#each paneModels as m (m.name)}
            <div
              class="model-card"
              class:active={chosenModel === m.name}
              onclick={() => selectModel(m.name)}
              role="button"
              tabindex="0"
              onkeydown={(e) => e.key === 'Enter' && selectModel(m.name)}
            >
              <div class="mc-left">
                <span class="model-name">{m.name}</span>
                {#if m.label}<span class="model-label">{m.label}</span>{/if}
                {#if m.fit}<span class="fit {m.fit}">{fitLabel[m.fit]}</span>{/if}
              </div>
              <div class="mc-right">
                {#if m.active}
                  <button
                    class="icon-btn"
                    class:default={m.isDefault}
                    title={m.isDefault
                      ? 'Default for new sessions (click to clear)'
                      : 'Make the default for new sessions'}
                    aria-label={m.isDefault ? 'Clear default' : 'Make default'}
                    onclick={(e) => {
                      e.stopPropagation();
                      void setDefault(m.name, !m.isDefault);
                    }}
                  >
                    <Icon name="star" size={13} />
                  </button>
                  <span class="pill">{m.isDefault ? 'default' : 'added'}</span>
                  <button
                    class="icon-btn danger"
                    title="Remove {m.name}"
                    aria-label="Remove {m.name}"
                    onclick={(e) => {
                      e.stopPropagation();
                      void removeAdded(m.name);
                    }}
                  >
                    <Icon name="close" size={13} />
                  </button>
                {:else if isOllamaPane}
                  <button
                    class="icon-btn danger"
                    title="Delete installed model {m.name}"
                    aria-label="Delete installed model {m.name}"
                    onclick={(e) => {
                      e.stopPropagation();
                      void removeInstalled(m.name);
                    }}
                  >
                    <Icon name="trash" size={13} />
                  </button>
                {/if}
              </div>
            </div>
          {/each}
          {#if paneModels.length === 0 && !loadingModels}
            <p class="empty">
              {#if isOllamaPane}
                No local models. Download one above.
              {:else}
                No models listed. Enter a model name to add it.
              {/if}
            </p>
          {/if}
        </div>

        {#if !isOllamaPane}
          <div class="custom-model">
            <input
              placeholder="Or type a model name, e.g. deepseek-v4-flash"
              bind:value={setupCustomModel}
              onkeydown={(e) => e.key === 'Enter' && addModel()}
            />
          </div>
        {/if}
      </div>

      <div class="pane-actions">
        <button
          class="btn-primary add-btn"
          onclick={addModel}
          disabled={(!chosenModel && !setupCustomModel.trim()) || saving}
        >
          Add
        </button>
        {#if paneMsg}<span class="ok">{paneMsg}</span>{/if}
      </div>
    </div>
  {:else}
    <header class="page-head">
      <h1>Models</h1>
      <p>
        Pick a provider, add the key if it needs one, add a model. Star a model to make it the
        default for new sessions.
      </p>
    </header>
    {#if error}<div class="error">{error}</div>{/if}

    <header class="section-head">
      <h1>Hosted</h1>
    </header>
    <div class="cards">
      {#each hosted as p (p.name)}
        <button class="provider-card" onclick={() => openSetup(p.name)}>
          <div class="pc-top">
            <h2>{p.label}</h2>
            {#if p.key_missing}
              <span class="badge warn">needs key</span>
            {:else}
              <span class="badge ok">ready</span>
            {/if}
          </div>
          <div class="meta">{p.tag}</div>
        </button>
      {/each}
    </div>

    <header class="section-head">
      <h1>Local</h1>
    </header>
    <div class="cards">
      {#each local as p (p.name)}
        <button class="provider-card" onclick={() => openSetup(p.name)}>
          <div class="pc-top">
            <h2>{p.label}</h2>
            {#if p.available === false}
              <span class="badge warn">offline</span>
            {:else}
              <span class="badge ok">local</span>
            {/if}
          </div>
          <div class="meta">{p.tag}</div>
        </button>
      {/each}
    </div>
  {/if}
</div>

<style>
  .page {
    overflow-y: auto;
    padding: 28px 32px;
    background: var(--bg);
  }
  /* Drawer tier: tighter sides, headroom for the floating menu button. */
  @media (max-width: 719px) {
    .page {
      padding: 52px 16px 24px;
    }
  }
  .page-head {
    max-width: 720px;
    margin-bottom: 18px;
  }
  .section-head {
    margin-top: 28px;
    margin-bottom: 10px;
  }
  h1 {
    font-size: 16px;
    font-weight: 650;
    color: var(--text-strong);
    margin: 0;
  }
  header p {
    margin: 4px 0 0;
    font-size: 13px;
    color: var(--text-muted);
    line-height: 1.5;
  }
  .cards {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
    gap: 12px;
    max-width: 900px;
  }
  .provider-card {
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface);
    padding: 13px 15px;
    display: flex;
    flex-direction: column;
    gap: 5px;
    text-align: left;
    font: inherit;
    cursor: pointer;
    color: inherit;
    transition: border-color 0.12s ease;
  }
  .provider-card:hover {
    border-color: var(--accent);
  }
  .pc-top {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
  }
  .badge {
    font-size: 10.5px;
    font-weight: 600;
    padding: 2px 7px;
    border-radius: 20px;
    white-space: nowrap;
  }
  .badge.ok {
    color: var(--ok);
    background: color-mix(in srgb, var(--ok), transparent 90%);
  }
  .badge.warn {
    color: var(--warn);
    background: color-mix(in srgb, var(--warn), transparent 90%);
  }
  h2 {
    font-family: var(--mono);
    font-size: 13px;
    font-weight: 600;
    color: var(--text-strong);
    margin: 0;
    display: flex;
    align-items: center;
    gap: 7px;
    min-width: 0;
  }
  .meta {
    font-family: var(--mono);
    font-size: 11.5px;
    color: var(--text-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .muted {
    color: var(--text-muted);
  }
  .empty {
    color: var(--text-muted);
    font-size: 13px;
  }
  .error {
    color: var(--danger);
    font-size: 12.5px;
    margin-bottom: 10px;
  }
  .error-text {
    color: var(--danger);
  }
  .ok {
    color: var(--ok);
    font-size: 12.5px;
  }
  .pill {
    font-size: 10px;
    font-weight: 600;
    letter-spacing: 0.02em;
    text-transform: uppercase;
    color: var(--ok);
    background: color-mix(in srgb, var(--ok), transparent 90%);
    padding: 1px 7px;
    border-radius: 20px;
    white-space: nowrap;
  }
  .fit {
    font-size: 11px;
  }
  .fit.comfortable {
    color: var(--ok);
  }
  .fit.tight {
    color: var(--warn);
  }
  .fit.too_large {
    color: var(--danger);
  }
  .fit.unknown {
    color: var(--text-muted);
  }
  .icon-btn.danger {
    color: var(--danger);
  }
  .icon-btn.danger:hover {
    background: color-mix(in srgb, var(--danger), transparent 90%);
  }
  .icon-btn.default {
    color: var(--accent);
  }
  .icon-btn.default:hover {
    background: color-mix(in srgb, var(--accent), transparent 88%);
  }

  /* --- unified pane --- */
  .pane {
    max-width: 720px;
  }
  .back {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font: inherit;
    font-size: 12.5px;
    color: var(--text-muted);
    background: none;
    border: none;
    cursor: pointer;
    padding: 0 0 12px;
  }
  .back:hover {
    color: var(--text-strong);
  }
  .key-step {
    margin: 14px 0;
  }
  .key-step label {
    display: block;
    font-size: 12.5px;
    color: var(--text-muted);
    margin-bottom: 6px;
  }
  .key-step .key-row {
    border: none;
    padding: 0;
    display: flex;
    gap: 8px;
    align-items: center;
  }
  .key-step input,
  .toolbar input,
  .custom-model input {
    font: inherit;
    font-family: var(--mono);
    font-size: 12.5px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 7px 12px;
    outline: none;
  }
  .key-step input:focus,
  .toolbar input:focus,
  .custom-model input:focus {
    border-color: var(--accent);
  }
  .key-step .key-row input {
    flex: 1;
  }
  .key-status {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-top: 8px;
    font-size: 12.5px;
  }
  .link-btn {
    font: inherit;
    font-size: 12px;
    color: var(--text-muted);
    background: none;
    border: none;
    cursor: pointer;
    text-decoration: underline;
  }
  .link-btn:hover {
    color: var(--danger);
  }
  .toolbar {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    margin: 12px 0;
  }
  .toolbar input {
    flex: 1;
    min-width: 240px;
  }
  .progress {
    font-family: var(--mono);
    font-size: 11px;
    color: var(--text-muted);
  }
  .models {
    margin-top: 18px;
  }
  .models-head {
    display: flex;
    align-items: baseline;
    gap: 10px;
    margin-bottom: 10px;
  }
  .models-head h2 {
    font-family: inherit;
    font-size: 13px;
  }
  .model-cards {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .model-card {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    font: inherit;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 9px 13px;
    cursor: pointer;
    text-align: left;
  }
  .model-card:hover {
    border-color: var(--accent);
  }
  .model-card.active {
    border-color: var(--accent);
    background: color-mix(in srgb, var(--accent), transparent 92%);
  }
  .mc-left {
    display: flex;
    align-items: center;
    gap: 10px;
    min-width: 0;
    flex: 1;
  }
  .model-name {
    font-family: var(--mono);
    font-size: 12.5px;
    color: var(--text);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .model-label {
    font-family: inherit;
    font-size: 11px;
    color: var(--text-muted);
    white-space: nowrap;
  }
  .mc-right {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .custom-model {
    margin-top: 10px;
  }
  .custom-model input {
    width: 100%;
  }
  .pane-actions {
    display: flex;
    align-items: center;
    gap: 12px;
    margin-top: 16px;
  }
  .add-btn {
    min-width: 120px;
  }
</style>
