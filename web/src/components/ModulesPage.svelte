<script lang="ts">
  import { onMount } from 'svelte';
  import { api } from '../lib/api';
  import type { MCPServerInfo, ModuleStatus } from '../lib/types';
  import Dropdown from './Dropdown.svelte';

  let { onchange }: { onchange: (modules: ModuleStatus[]) => void } = $props();

  let modules = $state<ModuleStatus[]>([]);
  let core = $state<ModuleStatus[]>([]);

  async function load() {
    const res = await api.modules();
    modules = res.modules;
    core = res.core;
  }

  async function toggle(m: ModuleStatus) {
    const res = await api.setModule(m.id, !m.enabled);
    modules = res.modules;
    core = res.core;
    onchange(modules);
    if (m.id === 'mcp') void loadServers();
  }

  // MCP server management: servers from config.toml are read-only here;
  // servers added on this page persist to mcp.json.
  const mcpEnabled = $derived(modules.find((m) => m.id === 'mcp')?.enabled ?? false);
  let servers = $state<MCPServerInfo[]>([]);
  let mcpError = $state('');
  let mcpBusy = $state(false);
  let addName = $state('');
  let addTransport = $state('stdio');
  let addTarget = $state(''); // command line (stdio) or URL (http)
  let addEnv = $state(''); // KEY=VALUE, comma-separated; ${secret:NAME} references supported

  async function loadServers() {
    mcpError = '';
    try {
      servers = (await api.mcpServers()).servers;
    } catch {
      servers = [];
    }
  }

  async function mcpAction(fn: () => Promise<{ servers: MCPServerInfo[] }>) {
    mcpError = '';
    mcpBusy = true;
    try {
      servers = (await fn()).servers;
    } catch (e) {
      mcpError = e instanceof Error ? e.message : String(e);
    } finally {
      mcpBusy = false;
    }
  }

  function addServer() {
    const name = addName.trim();
    const target = addTarget.trim();
    if (!name || !target) return;
    let env: Record<string, string> | undefined;
    if (addEnv.trim()) {
      env = {};
      for (const pair of addEnv.split(',')) {
        const eq = pair.indexOf('=');
        if (eq > 0) env[pair.slice(0, eq).trim()] = pair.slice(eq + 1).trim();
      }
    }
    const srv =
      addTransport === 'http'
        ? { name, transport: 'http', url: target, env }
        : {
            name,
            transport: 'stdio',
            command: target.split(/\s+/)[0],
            args: target.split(/\s+/).slice(1),
            env,
          };
    void mcpAction(() => api.mcpAddServer(srv)).then(() => {
      if (!mcpError) {
        addName = '';
        addTarget = '';
        addEnv = '';
      }
    });
  }

  let confirmRemove = $state<string | null>(null);

  onMount(async () => {
    await load();
    if (mcpEnabled) await loadServers();
  });

  $effect(() => {
    if (mcpEnabled) void loadServers();
  });
</script>

<div class="page">
  <header>
    <h1>Modules</h1>
    <p>
      Features are modules. Switch off what you don't use; tool changes apply to newly opened
      sessions. External tools connect as MCP servers, managed below.
    </p>
  </header>

  {#if core.length > 0}
    <section class="core">
      <h2>Core</h2>
      <p class="hint">
        Part of the harness itself: the agent's working surface. Always on, nothing to toggle.
      </p>
      <div class="cards">
        {#each core as m (m.id)}
          <article class="core-card">
            <div class="top">
              <h3>{m.name}</h3>
              <span class="always">always on</span>
            </div>
            <p>{m.description}</p>
            <span class="id">{m.id}</span>
          </article>
        {/each}
      </div>
    </section>
  {/if}

  <section class="toggleable">
    <h2>Features</h2>
    <p class="hint">
      Toggleable modules. Disabled modules release their resources and cost nothing until enabled
      again; tool changes apply to newly opened sessions.
    </p>
    <div class="cards">
      {#each modules as m (m.id)}
        <article class:off={!m.enabled}>
          <div class="top">
            <h3>{m.name}</h3>
            <button
              class="switch"
              class:on={m.enabled}
              role="switch"
              aria-checked={m.enabled}
              aria-label="{m.name} {m.enabled ? 'enabled' : 'disabled'}"
              onclick={() => toggle(m)}
            >
              <i></i>
            </button>
          </div>
          <p>{m.description}</p>
          <span class="id">{m.id}</span>
        </article>
      {/each}
    </div>
  </section>

  {#if mcpEnabled}
    <section class="mcp">
      <h2>MCP servers</h2>
      <p class="hint">
        Servers added here are stored in mcp.json; entries from config.toml are shown read-only.
        Tool changes apply to newly opened sessions.
      </p>

      {#if servers.length > 0}
        <div class="server-list">
          {#each servers as srv (srv.name)}
            <div class="server">
              <div class="server-main">
                <span class="server-name">{srv.name}</span>
                <span class="server-target">
                  {srv.transport === 'http'
                    ? srv.url
                    : [srv.command, ...(srv.args ?? [])].join(' ')}
                </span>
                <span
                  class="server-status"
                  class:ok={srv.status.startsWith('connected')}
                  class:bad={srv.status !== '' && !srv.status.startsWith('connected')}
                >
                  {srv.status || 'not connected'}
                </span>
              </div>
              <div class="server-actions">
                {#if srv.status !== '' && !srv.status.startsWith('connected')}
                  <button
                    class="small"
                    disabled={mcpBusy}
                    onclick={() => mcpAction(() => api.mcpReconnect(srv.name))}
                  >
                    reconnect
                  </button>
                {/if}
                {#if srv.source === 'app'}
                  {#if confirmRemove === srv.name}
                    <button
                      class="small danger"
                      disabled={mcpBusy}
                      onclick={() => {
                        confirmRemove = null;
                        void mcpAction(() => api.mcpRemoveServer(srv.name));
                      }}
                    >
                      confirm
                    </button>
                  {:else}
                    <button class="small" onclick={() => (confirmRemove = srv.name)}>
                      remove
                    </button>
                  {/if}
                {:else}
                  <span class="source">config.toml</span>
                {/if}
              </div>
              {#if srv.env_keys?.length}
                <div class="server-env">env: {srv.env_keys.join(', ')}</div>
              {/if}
              {#if srv.tools.length > 0}
                <details class="server-tools">
                  <summary>{srv.tools.length} tools</summary>
                  <div>{srv.tools.join(', ')}</div>
                </details>
              {/if}
            </div>
          {/each}
        </div>
      {/if}

      <div class="add">
        <input placeholder="name" bind:value={addName} spellcheck="false" />
        <Dropdown
          options={[
            { value: 'stdio', label: 'stdio' },
            { value: 'http', label: 'http' },
          ]}
          value={addTransport}
          onselect={(v) => (addTransport = v)}
          direction="down"
          title="transport"
        />
        <input
          class="target"
          placeholder={addTransport === 'http' ? 'https://server/mcp' : 'command --with args'}
          bind:value={addTarget}
          spellcheck="false"
          onkeydown={(e) => e.key === 'Enter' && addServer()}
        />
        <button
          class="small"
          disabled={mcpBusy || !addName.trim() || !addTarget.trim()}
          onclick={addServer}
        >
          add
        </button>
      </div>
      {#if addTransport === 'stdio'}
        <div class="add env-row">
          <input
            class="target"
            placeholder={'environment (optional): TOKEN=${secret:NAME}, OTHER=${VAR}'}
            bind:value={addEnv}
            spellcheck="false"
            onkeydown={(e) => e.key === 'Enter' && addServer()}
          />
        </div>
        <p class="hint env-hint">
          Reference API keys as {'${secret:NAME}'} (stored under API keys on the models page) or
          {'${VAR}'} from the environment. Values resolve when the server starts and stay out of the config
          file.
        </p>
      {/if}
      {#if mcpError}
        <div class="mcp-error">{mcpError}</div>
      {/if}
    </section>
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
  header {
    max-width: 720px;
    margin-bottom: 20px;
  }
  h1 {
    font-size: 16px;
    font-weight: 650;
    color: var(--text-strong);
    margin: 0 0 6px;
  }
  header p {
    margin: 0;
    font-size: 13px;
    color: var(--text-muted);
    line-height: 1.5;
  }
  .cards {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
    gap: 12px;
    max-width: 900px;
  }
  .core {
    margin-bottom: 28px;
  }
  .core h2,
  .toggleable h2,
  .mcp h2 {
    font-size: 14px;
    font-weight: 600;
    color: var(--text-strong);
    margin: 0 0 4px;
  }
  .hint {
    font-size: 12.5px;
    color: var(--text-muted);
    margin: 0 0 14px;
    line-height: 1.5;
  }
  .core-card {
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface);
    padding: 14px 16px;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .always {
    font-size: 10.5px;
    font-weight: 600;
    color: var(--text-muted);
    opacity: 0.8;
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  article {
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface);
    padding: 14px 16px;
    display: flex;
    flex-direction: column;
    gap: 8px;
    transition: opacity 120ms ease;
  }
  article.off {
    opacity: 0.55;
  }
  .top {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
  }
  h3 {
    font-size: 13.5px;
    font-weight: 600;
    color: var(--text-strong);
    margin: 0;
  }
  article p,
  .core-card p {
    margin: 0;
    font-size: 12.5px;
    color: var(--text-muted);
    line-height: 1.5;
    flex: 1;
  }
  .id {
    font-family: var(--mono);
    font-size: 10.5px;
    color: var(--text-muted);
    opacity: 0.7;
  }
  .switch {
    position: relative;
    width: 32px;
    height: 18px;
    border-radius: 9px;
    background: var(--surface-2);
    border: 1px solid var(--border-strong);
    flex-shrink: 0;
    transition: background 120ms ease;
  }
  .switch i {
    position: absolute;
    top: 2px;
    left: 2px;
    width: 12px;
    height: 12px;
    border-radius: 50%;
    background: var(--text-muted);
    transition:
      transform 120ms ease,
      background 120ms ease;
  }
  .switch.on {
    background: var(--accent);
    border-color: var(--accent);
  }
  .switch.on i {
    transform: translateX(14px);
    background: var(--accent-contrast);
  }

  .mcp {
    max-width: 900px;
    margin-top: 28px;
    padding-top: 20px;
    border-top: 1px solid var(--border);
  }
  .server-list {
    display: flex;
    flex-direction: column;
    gap: 8px;
    margin-bottom: 14px;
  }
  .server {
    display: grid;
    grid-template-columns: 1fr auto;
    gap: 4px 12px;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface);
    padding: 10px 14px;
    align-items: center;
  }
  .server-main {
    display: flex;
    align-items: baseline;
    gap: 12px;
    min-width: 0;
  }
  .server-name {
    font-family: var(--mono);
    font-size: 12.5px;
    font-weight: 600;
    color: var(--text-strong);
    flex-shrink: 0;
  }
  .server-target {
    font-family: var(--mono);
    font-size: 11.5px;
    color: var(--text-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .server-status {
    font-size: 11.5px;
    color: var(--text-muted);
    flex-shrink: 0;
  }
  .server-status.ok {
    color: var(--ok);
  }
  .server-status.bad {
    color: var(--danger);
  }
  .server-actions {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .server-tools {
    grid-column: 1 / -1;
    font-size: 11.5px;
    color: var(--text-muted);
  }
  .server-tools summary {
    cursor: pointer;
    user-select: none;
  }
  .server-tools div {
    margin-top: 4px;
    font-family: var(--mono);
    line-height: 1.6;
  }
  .source {
    font-size: 11px;
    color: var(--text-muted);
    opacity: 0.7;
  }
  .add {
    display: flex;
    gap: 8px;
    align-items: center;
    /* Inputs keep intrinsic minimums, so the row wraps on narrow
       screens instead of scrolling the page sideways. */
    flex-wrap: wrap;
  }
  .add input {
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--surface);
    padding: 6px 10px;
    font-size: 12.5px;
    color: var(--text);
    min-width: 0;
  }
  .add input:not(.target) {
    flex: 0 1 140px;
  }
  .add input:focus {
    outline: none;
    border-color: var(--border-strong);
  }
  .add .target {
    flex: 1 1 220px;
    font-family: var(--mono);
  }
  .small {
    font-size: 12px;
    padding: 5px 12px;
    border: 1px solid var(--border-strong);
    border-radius: 6px;
    color: var(--text);
  }
  .small:hover:not(:disabled) {
    background: var(--surface-2);
  }
  .small:disabled {
    opacity: 0.5;
  }
  .small.danger {
    border-color: var(--danger);
    color: var(--danger);
  }
  .env-row {
    margin-top: 8px;
  }
  .env-hint {
    margin-top: 8px;
    margin-bottom: 0;
  }
  .server-env {
    grid-column: 1 / -1;
    font-size: 11px;
    font-family: var(--mono);
    color: var(--text-muted);
  }
  .mcp-error {
    margin-top: 10px;
    font-size: 12.5px;
    color: var(--danger);
  }
</style>
