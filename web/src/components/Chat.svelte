<script lang="ts">
  import type { SessionState } from '../lib/session.svelte';
  import { renderMarkdown } from '../lib/markdown';
  import MessageView from './Message.svelte';
  import ToolRunGroup from './ToolRunGroup.svelte';
  import { buildRenderItems } from '../lib/runGroups';
  import ApprovalCard from './ApprovalCard.svelte';
  import QuestionCard from './QuestionCard.svelte';
  import Composer from './Composer.svelte';
  import GitChip from './GitChip.svelte';
  import Icon from './Icon.svelte';
  import PromptOverlay from './PromptOverlay.svelte';
  import { api } from '../lib/api';
  import type { NotificationCenter } from '../lib/notifications.svelte';
  import NotificationBell from './NotificationBell.svelte';

  let showPrompt = $state(false);

  let {
    session,
    commandsEnabled,
    gitEnabled,
    filesEnabled,
    mcpEnabled,
    notif,
    notificationsEnabled = true,
    onnotifyopen,
    onfork,
    onedit,
    showPanel = $bindable(),
    showSidebar = $bindable(),
  }: {
    session: SessionState;
    commandsEnabled: boolean;
    gitEnabled: boolean;
    filesEnabled: boolean;
    mcpEnabled: boolean;
    notif?: NotificationCenter;
    /** The notifications module toggle; off hides the bell entirely. */
    notificationsEnabled?: boolean;
    onnotifyopen?: (id: string) => void;
    onfork?: (index: number) => void;
    onedit?: (index: number) => void;
    showPanel: boolean;
    showSidebar: boolean;
  } = $props();

  let scroller = $state<HTMLElement | null>(null);
  let pinned = $state(true); // stick to bottom unless the user scrolled up

  // Long transcripts render windowed: the last chunk plus a control to
  // reveal earlier messages. DOM stays small; the data is all still here.
  const windowSize = 50;
  let showAll = $state(false);
  const hiddenCount = $derived(showAll ? 0 : Math.max(0, session.messages.length - windowSize));
  const visibleMessages = $derived(session.messages.slice(hiddenCount));

  // Consecutive tool activity folds into one group card. A run gathers
  // tool-call messages, their results, and thinking-only messages; it
  // folds once it has 2 or more calls across 2 or more messages. Visible
  // text breaks the run (prose is the deliverable), but a text message
  // that ALSO carries tool calls contributes them to the run that
  // follows, so "Now the Go side:" plus its command folds with the
  // commands after it. The tail of a running turn never folds, so live
  // activity stays watchable and collapses only once it settles.
  const renderItems = $derived(buildRenderItems(visibleMessages, hiddenCount, session.busy));
  $effect(() => {
    // A different session starts windowed again.
    void session.meta?.id;
    showAll = false;
  });

  // Streaming markdown, rendered incrementally. A throttle, not a
  // debounce: tokens arrive faster than any debounce window, so a
  // trailing-edge debounce postpones forever and the markdown (with its
  // syntax highlighting) only appears when generation stops. Rendering
  // at a fixed cadence keeps highlighting live while the text streams,
  // at a few re-parses per second.
  function throttledRender(read: () => string, write: (html: string) => void) {
    let timer: ReturnType<typeof setTimeout> | undefined;
    let last = 0;
    $effect(() => {
      const text = read();
      clearTimeout(timer);
      if (!text) {
        write('');
        return;
      }
      const wait = Math.max(0, 120 - (performance.now() - last));
      timer = setTimeout(() => {
        last = performance.now();
        write(renderMarkdown(text));
      }, wait);
      return () => clearTimeout(timer);
    });
  }

  let streamHtml = $state('');
  let thinkingHtml = $state('');
  throttledRender(
    () => session.streamText,
    (h) => (streamHtml = h),
  );
  throttledRender(
    () => session.streamThinking,
    (h) => (thinkingHtml = h),
  );

  // Pinning follows user intent, not proximity: any upward wheel motion
  // releases the pin immediately (a proximity rule kept re-capturing
  // small scrolls and yanking the view back down mid-stream), and only
  // the user scrolling to the bottom re-pins. Programmatic scrolls are
  // flagged so they never count as intent.
  let programmatic = false;
  function scrollToBottom() {
    if (!scroller) return;
    programmatic = true;
    scroller.scrollTop = scroller.scrollHeight;
  }
  function onScroll() {
    if (!scroller) return;
    if (programmatic) {
      programmatic = false;
      return;
    }
    pinned = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 12;
  }
  function onWheel(e: WheelEvent) {
    if (e.deltaY < 0) pinned = false;
  }
  function jumpToLatest() {
    pinned = true;
    scrollToBottom();
  }

  $effect(() => {
    // Touch the reactive values that grow during streaming, then keep the
    // view pinned to the bottom. The rendered stream html re-flows the
    // page after the raw text does, so it is a dependency too.
    void session.messages.length;
    void session.streamText;
    void session.streamThinking;
    void streamHtml;
    void thinkingHtml;
    void session.approval;
    void session.question;
    if (pinned) scrollToBottom();
  });
</script>

<main>
  <header>
    <button
      class="icon-btn"
      class:active={showSidebar}
      onclick={() => (showSidebar = !showSidebar)}
      title={showSidebar ? 'Hide the session list' : 'Show the session list'}
      aria-label={showSidebar ? 'Hide the session list' : 'Show the session list'}
    >
      <Icon name="panel-left" />
    </button>
    <h1>{session.meta?.title ?? ''}</h1>
    <div class="controls">
      {#if gitEnabled}
        <GitChip {session} />
      {/if}
      <button
        class="prompt-btn"
        onclick={() => (showPrompt = true)}
        title="View the prompt layers: global system prompt and project instructions"
      >
        prompt
      </button>
      {#if notif && notificationsEnabled}
        <NotificationBell center={notif} onopen={(id) => onnotifyopen?.(id)} />
      {/if}
      {#if session.meta}
        <a
          class="icon-btn"
          href={api.exportURL(session.meta.id)}
          download
          title="Export session as markdown"
          aria-label="Export session as markdown"
        >
          <Icon name="download" />
        </a>
      {/if}
      <button
        class="icon-btn"
        class:active={showPanel}
        onclick={() => (showPanel = !showPanel)}
        title={showPanel ? 'Hide the side panel' : 'Show the side panel'}
        aria-label={showPanel ? 'Hide the side panel' : 'Show the side panel'}
      >
        <Icon name="panel" />
      </button>
    </div>
  </header>

  <section bind:this={scroller} onscroll={onScroll} onwheel={onWheel}>
    <div class="thread">
      {#if hiddenCount > 0}
        <button class="show-earlier" onclick={() => (showAll = true)}>
          Show {hiddenCount} earlier {hiddenCount === 1 ? 'message' : 'messages'}
        </button>
      {/if}
      {#each renderItems as item (item.key)}
        {#if item.kind === 'run'}
          <ToolRunGroup
            members={item.members}
            messages={session.messages}
            diffs={session.diffs}
            bg={session.bg}
          />
        {:else}
          <MessageView
            msg={item.msg}
            messages={session.messages}
            diffs={session.diffs}
            bg={session.bg}
            onfork={item.msg.role === 'user' && item.msg.kind !== 'instructions' && onfork
              ? () => onfork(item.index)
              : undefined}
            onedit={item.msg.role === 'user' && item.msg.kind !== 'instructions' && onedit
              ? () => onedit(item.index)
              : undefined}
          />
        {/if}
      {/each}

      {#if session.streamThinking && !session.streamText}
        {#if thinkingHtml}
          <!-- eslint-disable-next-line svelte/no-at-html-tags -- sanitized by DOMPurify -->
          <div class="thinking-live md">{@html thinkingHtml}</div>
        {:else}
          <div class="thinking-live">{session.streamThinking}</div>
        {/if}
      {/if}
      {#if session.streamText}
        {#if streamHtml}
          <!-- eslint-disable-next-line svelte/no-at-html-tags -- sanitized by DOMPurify -->
          <div class="md md-streaming">{@html streamHtml}</div>
        {:else}
          <div class="streaming">{session.streamText}</div>
        {/if}
      {/if}

      {#if session.approval}
        <ApprovalCard approval={session.approval} decide={(d) => session.decide(d)} />
      {/if}

      {#if session.question}
        <QuestionCard question={session.question} answer={(a) => session.answerQuestion(a)} />
      {/if}

      {#if session.queued.length > 0}
        {#each session.queued as q (q.time)}
          <div class="queued">
            <div class="queued-head">
              <span class="label">you · queued</span>
            </div>
            <div class="queued-body">{q.text}</div>
          </div>
        {/each}
      {/if}

      {#if session.busy && !session.streamText && !session.streamThinking && !session.approval && !session.question}
        <div class="working">
          {session.runningTool ? `running ${session.runningTool}` : 'waiting for model'}
        </div>
      {/if}

      {#if session.error}
        <div class="error">{session.error}</div>
      {/if}
    </div>
  </section>

  {#if !pinned && (session.busy || session.streamText || session.streamThinking)}
    <div class="jump-wrap">
      <button class="jump" onclick={jumpToLatest}>
        <Icon name="chevron" size={11} />
        latest
      </button>
    </div>
  {/if}

  <Composer {session} {commandsEnabled} {filesEnabled} {mcpEnabled} />
</main>

{#if showPrompt}
  <PromptOverlay {session} onclose={() => (showPrompt = false)} />
{/if}

<style>
  main {
    display: flex;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
    background: var(--bg);
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    padding: 11px 20px;
    border-bottom: 1px solid var(--border);
  }
  h1 {
    flex: 1;
    min-width: 0;
    font-size: 13.5px;
    font-weight: 600;
    margin: 0;
    color: var(--text-strong);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .controls {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-shrink: 0;
  }
  .prompt-btn {
    font-size: 12px;
    color: var(--text-muted);
    padding: 2px 8px;
    border-radius: 6px;
  }
  .prompt-btn:hover {
    color: var(--text-strong);
    background: var(--surface-2);
  }
  .icon-btn.active {
    color: var(--text-strong);
  }
  section {
    flex: 1;
    overflow-y: auto;
    /* Never pan the chat sideways: overflow-y alone computes overflow-x
       to auto, so one unbroken token would scroll the whole thread. Wide
       content scrolls inside its own block (pre, table) instead. */
    overflow-x: hidden;
    min-height: 0;
  }
  .thread {
    max-width: 760px;
    margin: 0 auto;
    padding: 20px 20px 30px;
    display: flex;
    flex-direction: column;
    gap: 16px;
  }
  .show-earlier {
    align-self: center;
    font-size: 12px;
    color: var(--text-muted);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 5px 14px;
  }
  .show-earlier:hover {
    color: var(--text-strong);
    border-color: var(--border-strong);
  }
  /* A message queued while the agent is busy: same shape as a user
     message, visually pending, replaced by the real card when the loop
     picks it up. */
  .queued {
    position: relative;
    border: 1px dashed var(--border-strong);
    border-radius: 8px;
    padding: 10px 14px;
    color: var(--text-muted);
    font-size: 13px;
  }
  .queued-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 3px;
  }
  .queued-head .label {
    font-size: 10.5px;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-muted);
  }
  .queued-body {
    white-space: pre-wrap;
    word-break: break-word;
  }
  .streaming {
    white-space: pre-wrap;
    word-break: break-word;
  }
  /* While streaming, the markdown re-renders several times a second;
     scrollable code blocks re-create their scrollbars each time and the
     bar visibly flickers. Wrap long lines during the stream instead; the
     completed message renders with normal scrolling blocks. */
  .md-streaming :global(pre) {
    white-space: pre-wrap;
    word-break: break-word;
    overflow-x: hidden;
  }
  .thinking-live {
    color: var(--text-muted);
    font-size: 13px;
    white-space: pre-wrap;
    word-break: break-word;
    border-left: 2px solid var(--border);
    padding-left: 10px;
  }
  .thinking-live.md {
    white-space: normal;
  }
  .jump-wrap {
    position: relative;
    height: 0;
    display: flex;
    justify-content: center;
  }
  .jump {
    position: absolute;
    bottom: 10px;
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font-size: 12px;
    color: var(--text);
    background: var(--surface);
    border: 1px solid var(--border-strong);
    border-radius: 14px;
    padding: 4px 12px;
    box-shadow: 0 2px 10px rgba(0, 0, 0, 0.18);
  }
  .jump:hover {
    color: var(--text-strong);
    background: var(--surface-2);
  }
  .working {
    color: var(--text-muted);
    font-size: 13px;
    font-family: var(--mono);
  }
  .working::after {
    content: '…';
    animation: pulse 1.4s steps(1) infinite;
  }
  @keyframes pulse {
    50% {
      opacity: 0;
    }
  }
  .error {
    color: var(--danger);
    font-size: 13px;
    border: 1px solid var(--danger);
    border-radius: 6px;
    padding: 8px 12px;
  }
</style>
