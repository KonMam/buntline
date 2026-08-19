<script lang="ts">
  import Icon from './Icon.svelte';

  interface Option {
    value: string;
    label: string;
    hint?: string;
  }

  let {
    options,
    value,
    label,
    onselect,
    disabled = false,
    title = '',
    direction = 'up',
  }: {
    options: Option[];
    value: string;
    /** Fixed text on the trigger; the selection itself is only marked by
        the check in the open menu. Keeps the trigger short and stable. */
    label: string;
    onselect: (value: string) => void;
    disabled?: boolean;
    title?: string;
    direction?: 'up' | 'down';
  } = $props();

  let open = $state(false);
  let active = $state(-1); // keyboard-highlighted row
  let root = $state<HTMLElement | null>(null);
  let list = $state<HTMLElement | null>(null);

  function toggle() {
    if (disabled) return;
    open = !open;
    if (open) {
      active = options.findIndex((o) => o.value === value);
      requestAnimationFrame(() => {
        list?.querySelector('.selected')?.scrollIntoView({ block: 'nearest' });
      });
    }
  }

  function pick(v: string) {
    open = false;
    if (v !== value) onselect(v);
  }

  function onkeydown(e: KeyboardEvent) {
    if (!open) {
      if (e.key === 'Enter' || e.key === ' ' || e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault();
        toggle();
      }
      return;
    }
    switch (e.key) {
      case 'Escape':
        e.preventDefault();
        open = false;
        break;
      case 'ArrowDown':
        e.preventDefault();
        active = Math.min(active + 1, options.length - 1);
        scrollToActive();
        break;
      case 'ArrowUp':
        e.preventDefault();
        active = Math.max(active - 1, 0);
        scrollToActive();
        break;
      case 'Enter':
        e.preventDefault();
        if (active >= 0) pick(options[active].value);
        break;
    }
  }

  function scrollToActive() {
    requestAnimationFrame(() => {
      list?.querySelectorAll('button')[active]?.scrollIntoView({ block: 'nearest' });
    });
  }

  function onDocumentClick(e: MouseEvent) {
    if (open && root && !root.contains(e.target as Node)) open = false;
  }
</script>

<svelte:document onclick={onDocumentClick} />

<div class="dropdown" bind:this={root}>
  <button
    class="trigger"
    {title}
    {disabled}
    aria-haspopup="listbox"
    aria-expanded={open}
    onclick={toggle}
    {onkeydown}
  >
    <span class="label">{label}</span>
    <Icon name="chevron" size={9} />
  </button>

  {#if open}
    <div class="menu" class:up={direction === 'up'} role="listbox" bind:this={list}>
      {#each options as o, i (o.value)}
        <button
          class="option"
          class:selected={o.value === value}
          class:active={i === active}
          role="option"
          aria-selected={o.value === value}
          onclick={() => pick(o.value)}
          onmouseenter={() => (active = i)}
        >
          <span class="check"
            >{#if o.value === value}<Icon name="check" size={11} />{/if}</span
          >
          <span class="option-label">{o.label}</span>
          {#if o.hint}
            <span class="hint">{o.hint}</span>
          {/if}
        </button>
      {/each}
    </div>
  {/if}
</div>

<style>
  .dropdown {
    position: relative;
    min-width: 0;
    max-width: 220px;
  }
  .trigger {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font-family: var(--mono);
    font-size: 11.5px;
    color: var(--text-muted);
    /* Cap at the wrapper, not a fixed width: when the row squeezes the
       wrapper below 220px the trigger must shrink with it, not paint
       over its neighbors. */
    max-width: 100%;
    padding: 2px 4px;
    border-radius: 5px;
  }
  .trigger :global(svg) {
    flex-shrink: 0;
  }
  .trigger:hover:not(:disabled),
  .trigger[aria-expanded='true'] {
    color: var(--text);
    background: var(--surface-2);
  }
  .trigger:disabled {
    opacity: 0.6;
    cursor: default;
  }
  .label {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .menu {
    position: absolute;
    left: 0;
    top: calc(100% + 4px);
    z-index: 40;
    min-width: 100%;
    max-width: min(320px, 80vw);
    max-height: 260px;
    overflow-y: auto;
    background: var(--bg);
    border: 1px solid var(--border-strong);
    border-radius: 8px;
    padding: 4px;
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.2);
  }
  .menu.up {
    top: auto;
    bottom: calc(100% + 4px);
  }
  .option {
    display: flex;
    width: 100%;
    align-items: center;
    gap: 7px;
    padding: 5px 8px;
    border-radius: 5px;
    text-align: left;
    font-size: 12px;
    color: var(--text);
    white-space: nowrap;
  }
  .option.active {
    background: var(--surface-2);
  }
  .option.selected {
    color: var(--text-strong);
  }
  .check {
    width: 11px;
    flex-shrink: 0;
    display: inline-flex;
    color: var(--accent);
  }
  .option-label {
    font-family: var(--mono);
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .hint {
    margin-left: auto;
    padding-left: 12px;
    font-size: 11px;
    color: var(--text-muted);
  }
</style>
