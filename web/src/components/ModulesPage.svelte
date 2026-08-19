<script lang="ts">
  import { onMount } from 'svelte';
  import { api } from '../lib/api';
  import type { MCPServerInfo, ModuleStatus } from '../lib/types';
  import type { NotificationCenter } from '../lib/notifications.svelte';
  import { kindLabel } from '../lib/notifications.svelte';
  import Icon from './Icon.svelte';
  import Dropdown from './Dropdown.svelte';

  let {
    onchange,
    notif,
  }: {
    onchange: (modules: ModuleStatus[]) => void;
    /** The app's notification center; enables the Notifications settings pane. */
    notif?: NotificationCenter;
  } = $props();

  let modules = $state<ModuleStatus[]>([]);
  let core = $state<ModuleStatus[]>([]);

  // The Notifications card opens a settings pane the way a Models
  // provider card opens its setup pane: click the card, get the view.
  let notifPane = $state(false);

  // Notification-type toggles shown in the pane, with human labels. The
  // keys are the settings keys, so the switch flips the same field the
  // bell logic reads.
  const notifyKinds = [
    { key: 'approval', desc: 'another session wants to run a tool' },
    { key: 'question', desc: 'another session is waiting on your answer' },
    { key: 'turnEnd', desc: 'a turn finishes in any session' },
    { key: 'error', desc: 'a turn fails in any session' },
  ] as const;

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
  {#if notifPane && notif}
    <div class="pane">
      <button class="back" onclick={() => (notifPane = false)}>
        <Icon name="back" size={13} /> back
      </button>
      <header class="pane-head">
        <h1>Notifications</h1>
        <p>
          What buntline tells you about while it works: approvals and questions from every session,
          turn ends, and errors. Each browser remembers its own choices here.
        </p>
      </header>

      <section class="notif-section">
        <h2>Notifications</h2>
        <p class="hint">
          Master switch. Off stops the bell, the attention banner, and desktop popups — the module
          switch on the card above does the same, app-wide.
        </p>
        <div class="notif-row">
          <div class="notif-text">
            <span class="notif-name">Notifications</span>
            <span class="notif-desc">in-app bell, attention banner, and desktop popups</span>
          </div>
          <button
            class="switch"
            class:on={notif.settings.enabled}
            role="switch"
            aria-checked={notif.settings.enabled}
            aria-label="Notifications {notif.settings.enabled ? 'on' : 'off'}"
            onclick={() => notif.setSetting('enabled', !notif.settings.enabled)}
          >
            <i></i>
          </button>
        </div>
      </section>

      <section class="notif-section">
        <h2>Desktop popups</h2>
        <p class="hint">
          OS-level popups need this site's permission in the browser. Turn them off here any time;
          the browser keeps the permission, the popups just stop.
        </p>
        {#if notif.osAvailable}
          <div class="notif-row">
            <div class="notif-text">
              <span class="notif-name">Desktop popups</span>
              <span class="notif-desc">
                {#if notif.osPermission === 'granted'}
                  permission granted — popups {notif.settings.os ? 'on' : 'off'}
                {:else if notif.osPermission === 'denied'}
                  blocked in browser settings
                {:else}
                  not enabled yet
                {/if}
              </span>
            </div>
            <button
              class="switch"
              class:on={notif.settings.os && notif.osPermission === 'granted'}
              role="switch"
              aria-checked={notif.settings.os && notif.osPermission === 'granted'}
              aria-label="Desktop popups"
              disabled={notif.osPermission !== 'granted'}
              onclick={() => notif.setSetting('os', !notif.settings.os)}
            >
              <i></i>
            </button>
          </div>
          {#if notif.osPermission === 'default' && notif.canRequest}
            <button class="small" onclick={() => void notif.requestPermission()}>
              enable desktop notifications
            </button>
          {:else if notif.osPermission === 'denied'}
            <p class="hint">
              Allow notifications for this site in the browser's site settings (next to the address
              bar), then reload.
            </p>
          {/if}
        {:else}
          <p class="hint">Desktop popups are not available in this browser.</p>
        {/if}
      </section>

      <section class="notif-section">
        <h2>What to notify about</h2>
        <div class="notif-rows">
          {#each notifyKinds as k (k.key)}
            <div class="notif-row">
              <div class="notif-text">
                <span class="notif-name">{kindLabel[k.key]}</span>
                <span class="notif-desc">{k.desc}</span>
              </div>
              <button
                class="switch"
                class:on={notif.settings[k.key]}
                role="switch"
                aria-checked={notif.settings[k.key]}
                aria-label="Notify about {kindLabel[k.key]}"
                onclick={() => notif.setSetting(k.key, !notif.settings[k.key])}
              >
                <i></i>
              </button>
            </div>
          {/each}
        </div>
      </section>

      <section class="notif-section">
        <h2>When you are looking</h2>
        <div class="notif-row">
          <div class="notif-text">
            <span class="notif-name">While active</span>
            <span class="notif-desc">
              notify even when the app is open on the session in question
            </span>
          </div>
          <button
            class="switch"
            class:on={notif.settings.whileActive}
            role="switch"
            aria-checked={notif.settings.whileActive}
            aria-label="Notify while active"
            onclick={() => notif.setSetting('whileActive', !notif.settings.whileActive)}
          >
            <i></i>
          </button>
        </div>
      </section>
    </div>
  {:else}
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
          <!-- The Notifications card is clickable like a Models provider
             card: it opens the settings pane instead of just toggling.
             role/tabindex/keydown make it a real button at runtime;
             the check can't see the conditional role. -->
          <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
          <article
            class:off={!m.enabled}
            class:manageable={m.id === 'notifications'}
            role={m.id === 'notifications' ? 'button' : undefined}
            tabindex={m.id === 'notifications' ? 0 : undefined}
            aria-label={m.id === 'notifications' ? 'Open notification settings' : undefined}
            onclick={m.id === 'notifications' ? () => (notifPane = true) : undefined}
            onkeydown={m.id === 'notifications'
              ? (e) => e.key === 'Enter' && (notifPane = true)
              : undefined}
          >
            <div class="top">
              <h3>{m.name}</h3>
              <button
                class="switch"
                class:on={m.enabled}
                role="switch"
                aria-checked={m.enabled}
                aria-label="{m.name} {m.enabled ? 'enabled' : 'disabled'}"
                onclick={(e) => {
                  e.stopPropagation();
                  toggle(m);
                }}
              >
                <i></i>
              </button>
            </div>
            <p>{m.description}</p>
            <span class="id">
              {m.id}
              {#if m.id === 'notifications'}
                <span class="manage">settings →</span>
              {/if}
            </span>
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
            {'${VAR}'} from the environment. Values resolve when the server starts and stay out of the
            config file.
          </p>
        {/if}
        {#if mcpError}
          <div class="mcp-error">{mcpError}</div>
        {/if}
      </section>
    {/if}
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
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
  }
  /* The Notifications card is also a button: hovering hints the click,
     like the provider cards on the Models page. */
  article.manageable {
    cursor: pointer;
  }
  article.manageable:hover {
    border-color: var(--accent);
  }
  article.manageable:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
  }
  .manage {
    font-family: var(--sans);
    font-size: 10.5px;
    font-weight: 600;
    letter-spacing: 0.04em;
    text-transform: uppercase;
    color: var(--accent);
    opacity: 1;
    white-space: nowrap;
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
  .switch:disabled {
    opacity: 0.45;
    cursor: default;
  }

  /* --- Notifications settings pane (the card opens it) --- */
  .pane {
    max-width: 640px;
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
  .pane-head {
    margin-bottom: 6px;
  }
  .pane-head h1 {
    font-size: 16px;
    font-weight: 650;
    color: var(--text-strong);
    margin: 0 0 6px;
  }
  .pane-head p {
    margin: 0;
    font-size: 13px;
    color: var(--text-muted);
    line-height: 1.5;
  }
  .notif-section {
    margin-top: 24px;
    padding-top: 18px;
    border-top: 1px solid var(--border);
  }
  .notif-section h2 {
    font-size: 13px;
    font-weight: 600;
    color: var(--text-strong);
    margin: 0 0 4px;
  }
  .notif-section > .hint {
    font-size: 12px;
    color: var(--text-muted);
    margin: 0 0 12px;
    line-height: 1.5;
  }
  .notif-rows {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .notif-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--surface);
    padding: 10px 14px;
  }
  .notif-text {
    display: flex;
    flex-direction: column;
    gap: 1px;
    min-width: 0;
  }
  .notif-name {
    font-size: 13px;
    font-weight: 600;
    color: var(--text-strong);
    text-transform: capitalize;
  }
  .notif-desc {
    font-size: 11.5px;
    color: var(--text-muted);
  }
  .pane .small {
    margin-top: 10px;
    font-size: 12px;
    padding: 5px 12px;
    border: 1px solid var(--border-strong);
    border-radius: 6px;
    color: var(--text);
  }
  .pane .small:hover {
    background: var(--surface-2);
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
