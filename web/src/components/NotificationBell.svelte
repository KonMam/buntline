<script lang="ts">
  // The bell: unread count badge, the in-app notification list, the OS
  // permission state, and per-type settings. One instance, owned by the
  // app, fed by the global /api/events stream.
  import type { NotificationCenter } from '../lib/notifications.svelte';
  import { kindLabel } from '../lib/notifications.svelte';
  import Icon from './Icon.svelte';

  let {
    center,
    onopen,
  }: {
    center: NotificationCenter;
    onopen: (sessionId: string) => void;
  } = $props();

  let open = $state(false);

  function toggle() {
    open = !open;
    if (open) {
      center.markAllRead();
    }
  }

  // Close when the user clicks elsewhere.
  function onDocClick(e: MouseEvent) {
    const t = e.target as HTMLElement | null;
    if (t && t.closest('[data-notif-bell]')) return;
    open = false;
  }

  $effect(() => {
    if (typeof document === 'undefined') return;
    document.addEventListener('click', onDocClick);
    return () => document.removeEventListener('click', onDocClick);
  });
</script>

<div class="bell" data-notif-bell>
  <button
    class="icon-btn"
    class:active={open}
    onclick={toggle}
    title={center.unread > 0
      ? `${center.unread} notification${center.unread === 1 ? '' : 's'}`
      : 'Notifications'}
    aria-label={center.unread > 0 ? `Notifications (${center.unread} unread)` : 'Notifications'}
  >
    <Icon name="bell" />
    {#if center.unread > 0}
      <span class="badge">{center.unread > 99 ? '99+' : center.unread}</span>
    {/if}
  </button>

  {#if open}
    <div class="popover">
      <header>
        <span class="title">notifications</span>
        {#if center.osAvailable && center.osPermission === 'default' && center.canRequest}
          <button class="perm" onclick={() => void center.requestPermission()}>
            enable desktop notifications
          </button>
        {:else if center.osAvailable && center.osPermission === 'denied'}
          <span class="denied">desktop notifications blocked</span>
        {:else if center.osAvailable && center.osPermission === 'granted'}
          <span class="granted">desktop notifications on</span>
        {/if}
      </header>

      <div class="list">
        {#if center.items.length === 0}
          <div class="empty">
            nothing yet — approvals, questions, and turn ends from every session land here
          </div>
        {:else}
          {#each center.items as item (item.id)}
            <button
              class="item"
              class:unread={!item.read}
              onclick={() => {
                open = false;
                center.markAllRead();
                onopen(item.sessionId);
              }}
            >
              <span class="kind {item.kind}">{kindLabel[item.kind]}</span>
              <span class="title">{item.title}</span>
              <span class="body">{item.body}</span>
              <span class="sess">{item.sessionTitle}</span>
            </button>
          {/each}
        {/if}
      </div>

      <footer>
        <label class="switch">
          <input
            type="checkbox"
            checked={center.settings.enabled}
            onchange={(e) => center.setSetting('enabled', e.currentTarget.checked)}
          />
          <span>notifications</span>
        </label>
        {#if center.settings.enabled}
          <div class="toggles">
            <label class="switch">
              <input
                type="checkbox"
                checked={center.settings.approval}
                onchange={(e) => center.setSetting('approval', e.currentTarget.checked)}
              />
              <span>approvals</span>
            </label>
            <label class="switch">
              <input
                type="checkbox"
                checked={center.settings.question}
                onchange={(e) => center.setSetting('question', e.currentTarget.checked)}
              />
              <span>questions</span>
            </label>
            <label class="switch">
              <input
                type="checkbox"
                checked={center.settings.turnEnd}
                onchange={(e) => center.setSetting('turnEnd', e.currentTarget.checked)}
              />
              <span>turn ends</span>
            </label>
            <label class="switch">
              <input
                type="checkbox"
                checked={center.settings.error}
                onchange={(e) => center.setSetting('error', e.currentTarget.checked)}
              />
              <span>errors</span>
            </label>
            {#if center.osAvailable}
              <label class="switch">
                <input
                  type="checkbox"
                  checked={center.settings.os}
                  onchange={(e) => center.setSetting('os', e.currentTarget.checked)}
                />
                <span>desktop popups</span>
              </label>
            {/if}
          </div>
        {/if}
      </footer>
    </div>
  {/if}
</div>

<style>
  .bell {
    position: relative;
  }
  .icon-btn {
    position: relative;
  }
  .badge {
    position: absolute;
    top: -4px;
    right: -6px;
    min-width: 15px;
    height: 15px;
    padding: 0 3px;
    border-radius: 8px;
    background: var(--danger);
    color: #fff;
    font-size: 9.5px;
    font-weight: 700;
    line-height: 15px;
    text-align: center;
  }
  .popover {
    position: absolute;
    top: calc(100% + 8px);
    right: 0;
    width: 300px;
    max-height: 70vh;
    display: flex;
    flex-direction: column;
    background: var(--surface);
    border: 1px solid var(--border-strong);
    border-radius: 10px;
    box-shadow: 0 8px 28px rgba(0, 0, 0, 0.22);
    z-index: 30;
    overflow: hidden;
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding: 10px 12px;
    border-bottom: 1px solid var(--border);
  }
  header .title {
    font-size: 10.5px;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-muted);
  }
  .perm {
    font-size: 11px;
    color: var(--accent);
    border: 1px solid var(--accent-soft);
    border-radius: 6px;
    padding: 3px 8px;
  }
  .perm:hover {
    background: var(--accent-soft);
  }
  .granted {
    font-size: 10.5px;
    color: var(--ok);
  }
  .denied {
    font-size: 10.5px;
    color: var(--danger);
  }
  .list {
    flex: 1;
    overflow-y: auto;
    padding: 4px;
  }
  .item {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 1px 8px;
    width: 100%;
    text-align: left;
    padding: 8px 10px;
    border-radius: 7px;
    font-size: 12.5px;
  }
  .item:hover {
    background: var(--surface-2);
  }
  .item.unread {
    background: var(--accent-soft);
  }
  .kind {
    grid-row: span 2;
    font-size: 9.5px;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    align-self: center;
    border-radius: 4px;
    padding: 2px 5px;
  }
  .kind.approval,
  .kind.question {
    background: var(--warn-soft, var(--accent-soft));
    color: var(--warn, var(--accent));
  }
  .kind.error {
    background: var(--danger-soft, var(--surface-2));
    color: var(--danger);
  }
  .kind.turnEnd {
    background: var(--surface-2);
    color: var(--text-muted);
  }
  .item .title {
    font-weight: 600;
    color: var(--text-strong);
  }
  .item .body {
    color: var(--text-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .item .sess {
    grid-column: 2;
    font-size: 10.5px;
    color: var(--text-muted);
    font-family: var(--mono);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .empty {
    padding: 18px 14px;
    color: var(--text-muted);
    font-size: 12.5px;
  }
  footer {
    border-top: 1px solid var(--border);
    padding: 8px 12px;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .toggles {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 12px;
  }
  .switch {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font-size: 11.5px;
    color: var(--text-muted);
    cursor: pointer;
    user-select: none;
  }
  .switch input {
    accent-color: var(--accent);
    margin: 0;
  }
</style>
