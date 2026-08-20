<script lang="ts">
  import { api } from '../lib/api';
  import { latestRequest } from '../lib/latestRequest';
  import type { SessionState } from '../lib/session.svelte';
  import type { MCPPromptInfo, SlashCommand } from '../lib/types';
  import ModelSelect from './ModelSelect.svelte';
  import ModeSelect from './ModeSelect.svelte';
  import Icon from './Icon.svelte';

  let {
    session,
    commandsEnabled,
    filesEnabled,
    mcpEnabled,
  }: {
    session: SessionState;
    commandsEnabled: boolean;
    filesEnabled: boolean;
    mcpEnabled: boolean;
  } = $props();

  let text = $state('');
  let area = $state<HTMLTextAreaElement | null>(null);
  let commands = $state<SlashCommand[]>([]);
  let mcpPrompts = $state<MCPPromptInfo[]>([]);
  let files = $state<string[]>([]);

  // The composer buffer is per-session. The component itself survives
  // session switches (one SessionState is shared and reloaded), so the
  // buffer is parked under the session id on the way out and restored
  // from there on the way back: switching sessions never carries text
  // into an unrelated session, and returning finds it again.
  let draftId: string | null = null;
  $effect(() => {
    const id = session.meta?.id ?? null;
    const prev = draftId;
    // Leaving a session parks the current buffer under its id. Typing
    // in the same session (prev === id) never touches the map.
    if (prev !== null && prev !== id) {
      session.drafts[prev] = text;
    }
    draftId = id;
    if (id !== null && id !== prev) {
      text = session.drafts[id] ?? '';
    } else if (id === null) {
      text = '';
    }
  });

  // Edit-and-resend hands the original message over via session.draft.
  $effect(() => {
    if (session.draft) {
      const id = session.meta?.id;
      text = session.draft;
      session.draft = '';
      // Park it as this session's draft too, so switching away and back
      // before sending keeps the edited text.
      if (id) session.drafts[id] = text;
      area?.focus();
    }
  });
  // A message queued while the agent is busy (steering) was handed to
  // the loop, not lost: the queued bubble in the chat tracks it until
  // the message event lands.
  $effect(() => {
    if (session.queued.length > 0) {
      area?.focus();
    }
  });

  // Attachments attached by pasting or picking. Images stay data URLs
  // (the vision pipeline and the transcript render them as images);
  // every other file is uploaded to the session's attachment store and
  // travels as a workdir-relative path, entering the model's context
  // through the same read_file exchange @-mentions use. Pasting covers
  // desktop; the attach button covers mobile (where there is no
  // clipboard paste gesture) via a hidden file input.
  let images = $state<string[]>([]);
  let uploads = $state<{ path: string; name: string }[]>([]);
  let fileInput = $state<HTMLInputElement | null>(null);
  const imageCap = 8 * 1024 * 1024;
  // The server folds attachment paths into a synthetic read_file
  // exchange capped at four; mirror the cap so a pick never silently
  // drops a file the UI still shows as attached.
  const uploadCap = 4;

  // addFiles routes each picked/pasted file: images stay data URLs
  // (the vision pipeline and the transcript render them as images),
  // everything else uploads to the session and attaches as a path.
  async function addFiles(files: Iterable<File>) {
    for (const file of files) {
      if (file.type.startsWith('image/')) {
        if (file.size > imageCap) continue;
        const reader = new FileReader();
        reader.onload = () => {
          if (typeof reader.result === 'string') images = [...images, reader.result];
        };
        reader.readAsDataURL(file);
        continue;
      }
      if (!session.meta || uploads.length >= uploadCap) continue;
      try {
        const res = await api.uploadAttachment(session.meta.id, file);
        uploads = [...uploads, { path: res.path, name: file.name }];
      } catch (e) {
        session.error = e instanceof Error ? e.message : String(e);
      }
    }
  }

  function onpaste(e: ClipboardEvent) {
    const items = e.clipboardData?.items;
    if (!items) return;
    const files: File[] = [];
    for (const item of items) {
      if (!item.kind.startsWith('file')) continue;
      const file = item.getAsFile();
      if (!file) continue;
      if (file.type.startsWith('image/') && file.size > imageCap) continue;
      if (!file.type.startsWith('image/') && !(session.meta && uploads.length < uploadCap)) continue;
      files.push(file);
    }
    if (files.length === 0) return;
    e.preventDefault();
    void addFiles(files);
  }

  function onpick(e: Event) {
    const input = e.currentTarget as HTMLInputElement;
    if (input.files) void addFiles(input.files);
    // Reset so picking the same file again re-fires onchange.
    input.value = '';
  }

  function removeImage(i: number) {
    images = images.filter((_, idx) => idx !== i);
  }

  function removeUpload(i: number) {
    uploads = uploads.filter((_, idx) => idx !== i);
  }

  // @-mentions: type @ and a path fragment to pin a file's real content
  // into the message; no reliance on the model deciding to read it.
  const mentionQuery = $derived.by(() => {
    const m = text.match(/(?:^|\s)@([^\s@]*)$/);
    return m ? m[1] : null;
  });
  const mentionMenu = $derived.by(() => {
    if (mentionQuery === null || !filesEnabled) return [];
    const q = mentionQuery.toLowerCase();
    return files.filter((f) => f.toLowerCase().includes(q)).slice(0, 8);
  });

  function pickMention(path: string) {
    text = text.replace(/@[^\s@]*$/, '@' + path + ' ');
    area?.focus();
  }

  // Attached mentions render as chips (highlight overlay behind the
  // transparent-background textarea) and delete atomically: any deletion
  // touching a chip removes the whole token instead of leaving a
  // half-edited path that silently stops matching a file.
  const mentionRanges = $derived.by(() => {
    const ranges: { start: number; end: number }[] = [];
    for (const m of text.matchAll(/@([^\s@]+)/g)) {
      if (files.includes(m[1])) {
        ranges.push({ start: m.index, end: m.index + m[0].length });
      }
    }
    return ranges;
  });

  const highlightHtml = $derived.by(() => {
    const esc = (s: string) =>
      s.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;');
    let out = '';
    let pos = 0;
    for (const r of mentionRanges) {
      out += esc(text.slice(pos, r.start));
      out += `<mark>${esc(text.slice(r.start, r.end))}</mark>`;
      pos = r.end;
    }
    return out + esc(text.slice(pos)) + '\n';
  });

  function onbeforeinput(e: InputEvent) {
    if (!e.inputType.startsWith('delete') || !area) return;
    let { selectionStart: selStart, selectionEnd: selEnd } = area;
    if (selStart === null || selEnd === null) return;
    // A caret backspace/forward-delete acts on one adjacent character.
    if (selStart === selEnd) {
      if (e.inputType === 'deleteContentBackward') selStart = Math.max(0, selStart - 1);
      else if (e.inputType === 'deleteContentForward') selEnd = selEnd + 1;
    }
    const hit = mentionRanges.filter((r) => selStart < r.end && selEnd > r.start);
    if (hit.length === 0) return;
    e.preventDefault();
    const from = Math.min(selStart, ...hit.map((r) => r.start));
    const to = Math.max(selEnd, ...hit.map((r) => r.end));
    text = text.slice(0, from) + text.slice(to);
    requestAnimationFrame(() => area?.setSelectionRange(from, from));
  }

  let highlighter = $state<HTMLElement | null>(null);
  function syncScroll() {
    if (area && highlighter) highlighter.scrollTop = area.scrollTop;
  }

  // Double-click anywhere in a chip selects the whole token, the closest
  // a native textarea gets to atomic selection.
  function ondblclick() {
    if (!area || area.selectionStart === null) return;
    const pos = area.selectionStart;
    const hit = mentionRanges.find((r) => pos >= r.start && pos <= r.end);
    if (hit) area.setSelectionRange(hit.start, hit.end);
  }

  // The caret never rests inside a chip: a collapsed caret landing in a
  // token's interior snaps to the edge in the direction of travel, so
  // arrow keys step over the chip and clicks land beside it.
  let lastCaret = 0;
  function clampCaret() {
    if (!area || document.activeElement !== area) return;
    const s = area.selectionStart;
    const e = area.selectionEnd;
    if (s === null || e === null || s !== e) {
      lastCaret = s ?? 0;
      return;
    }
    const hit = mentionRanges.find((r) => s > r.start && s < r.end);
    if (!hit) {
      lastCaret = s;
      return;
    }
    const pos = s >= lastCaret ? hit.end : hit.start;
    area.setSelectionRange(pos, pos);
    lastCaret = pos;
  }

  // Mentioned files travel as attachments: the server turns them into a
  // synthetic read_file exchange, so the transcript shows tool cards
  // rather than pasted file bodies, and the message itself stays clean.
  function mentionedPaths(t: string): string[] {
    if (!filesEnabled) return [];
    return [...new Set([...t.matchAll(/@([^\s@]+)/g)].map((m) => m[1]))].filter((p) =>
      files.includes(p),
    );
  }

  const builtins: SlashCommand[] = [
    { name: 'compact', description: 'Summarize the conversation and reset the context', body: '' },
  ];

  // MCP prompts join the menu under mcp:<server>/<prompt>.
  const allCommands = $derived([
    ...builtins,
    ...commands,
    ...mcpPrompts.map((p) => ({
      name: `mcp:${p.name}`,
      description: p.description || `prompt from the ${p.name.split('/')[0]} MCP server`,
      body: '',
    })),
  ]);

  // The slash menu opens while the input is a single-line /prefix; the
  // first word picks the command, the rest becomes its arguments.
  const menu = $derived.by(() => {
    if (!text.startsWith('/') || text.includes('\n')) return [];
    const q = text.slice(1).split(/\s/, 1)[0].toLowerCase();
    return allCommands.filter((c) => c.name.toLowerCase().startsWith(q));
  });

  async function runCommand(c: SlashCommand, args: string) {
    text = '';
    if (c.name === 'compact') {
      void session.compact();
      return;
    }
    if (c.name.startsWith('mcp:')) {
      try {
        const res = await api.mcpRenderPrompt(c.name.slice(4), args);
        void session.send(res.text);
      } catch (e) {
        session.error = e instanceof Error ? e.message : String(e);
      }
      return;
    }
    // Arguments are substituted server-side ($ARGUMENTS, $0, $ARGUMENTS[N])
    // so the transcript holds the exact prompt the model receives.
    const id = session.meta?.id;
    if (id) {
      try {
        const res = await api.commandRender(id, c.name, args);
        void session.send(res.body);
      } catch (e) {
        session.error = e instanceof Error ? e.message : String(e);
      }
    }
  }

  async function submit() {
    const t = text.trim();
    if (!t && images.length === 0 && uploads.length === 0) return;
    // While a question card is open, the send button answers the
    // question instead of starting a new message: the model is waiting
    // on a human decision, and the turn continues with the answer.
    if (session.question && t) {
      const answer = t;
      text = '';
      void session.answerQuestion(answer);
      return;
    }
    if (t.startsWith('/')) {
      const space = t.search(/\s/);
      const name = space < 0 ? t.slice(1) : t.slice(1, space);
      const args = space < 0 ? '' : t.slice(space + 1).trim();
      const match = allCommands.find((c) => c.name === name);
      if (match) {
        void runCommand(match, args);
        return;
      }
    }
    const imgs = images;
    const up = uploads;
    text = '';
    images = [];
    uploads = [];
    const attachments = [...mentionedPaths(t), ...up.map((u) => u.path)];
    void session.send(
      t,
      imgs.length > 0 ? imgs : undefined,
      attachments.length > 0 ? attachments : undefined,
    );
  }

  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'Tab') {
      if (mentionMenu.length > 0) {
        e.preventDefault();
        pickMention(mentionMenu[0]);
        return;
      }
      if (menu.length > 0) {
        e.preventDefault();
        text = '/' + menu[0].name;
        return;
      }
    }
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      if (mentionMenu.length > 0 && mentionQuery !== '') {
        pickMention(mentionMenu[0]);
        return;
      }
      void submit();
    }
    if (e.key === 'Escape' && session.busy) {
      void session.interrupt();
    }
  }

  // Grow with content, capped.
  $effect(() => {
    void text;
    if (!area) return;
    area.style.height = 'auto';
    area.style.height = Math.min(area.scrollHeight, 200) + 'px';
  });

  // Project commands and the @-mention file list follow the session.
  // The loads are async and outlive the effect that started them; the
  // request-sequence guard drops a slow response from a previous session
  // so a rapid switch can never land the wrong session's menus.
  const sessionReqs = latestRequest();
  $effect(() => {
    const id = session.meta?.id;
    const seq = sessionReqs.seq();
    if (!id || !commandsEnabled) {
      commands = [];
    } else {
      api
        .commandsList(id)
        .then((res) => {
          if (!sessionReqs.current(seq)) return;
          commands = res.commands;
        })
        .catch(() => {
          if (!sessionReqs.current(seq)) return;
          commands = [];
        });
    }
    if (id && mcpEnabled) {
      api
        .mcpPrompts()
        .then((res) => {
          if (!sessionReqs.current(seq)) return;
          mcpPrompts = res.prompts;
        })
        .catch(() => {
          if (!sessionReqs.current(seq)) return;
          mcpPrompts = [];
        });
    } else {
      mcpPrompts = [];
    }
    if (!id || !filesEnabled) {
      files = [];
    } else {
      api
        .filesList(id)
        .then((res) => {
          if (!sessionReqs.current(seq)) return;
          files = res.files;
        })
        .catch(() => {
          if (!sessionReqs.current(seq)) return;
          files = [];
        });
    }
  });
</script>

<svelte:document onselectionchange={clampCaret} />

<footer>
  <div class="wrap">
    {#if menu.length > 0}
      <div class="menu">
        {#each menu as c (c.name)}
          <button class="cmd" onclick={() => runCommand(c, text.replace(/^\/\S*\s*/, '').trim())}>
            <span class="name">/{c.name}</span>
            <span class="desc">{c.description}</span>
          </button>
        {/each}
      </div>
    {:else if mentionMenu.length > 0}
      <div class="menu">
        {#each mentionMenu as f (f)}
          <button class="cmd" onclick={() => pickMention(f)}>
            <span class="name">@{f}</span>
          </button>
        {/each}
        <div class="menu-hint">attaches the file's content to your message</div>
      </div>
    {/if}

    <div class="box">
      {#if images.length > 0 || uploads.length > 0}
        <div class="attachments">
          {#each images as img, i (i)}
            <div class="attachment">
              <img src={img} alt="pasted attachment" />
              <button
                class="remove"
                onclick={() => removeImage(i)}
                title="Remove image"
                aria-label="Remove image"
              >
                <Icon name="close" size={10} />
              </button>
            </div>
          {/each}
          {#each uploads as up, i (up.path)}
            <div class="attachment file" title={up.name}>
              <Icon name="file" size={14} />
              <span class="file-name">{up.name}</span>
              <button
                class="remove"
                onclick={() => removeUpload(i)}
                title="Remove file"
                aria-label="Remove file"
              >
                <Icon name="close" size={10} />
              </button>
            </div>
          {/each}
        </div>
      {/if}
      <div class="input-wrap">
        <!-- eslint-disable-next-line svelte/no-at-html-tags -- escaped above -->
        <div class="highlighter" bind:this={highlighter} aria-hidden="true">
          {@html highlightHtml}
        </div>
        <textarea
          bind:this={area}
          bind:value={text}
          {onkeydown}
          {onbeforeinput}
          {ondblclick}
          {onpaste}
          onscroll={syncScroll}
          spellcheck="false"
          rows="1"
          placeholder={session.question
            ? 'Answer the question above and press Enter'
            : session.busy
              ? 'Agent is working. A message now is delivered at its next step · Esc interrupts'
              : 'Message · / commands · @ attach a file'}
        ></textarea>
      </div>
      <div class="bottom">
        <button
          class="attach icon-btn"
          onclick={() => fileInput?.click()}
          title="Attach an image or file"
          aria-label="Attach an image or file"
        >
          <Icon name="paperclip" size={15} />
        </button>
        <div class="selectors">
          <ModeSelect {session} />
          <ModelSelect {session} />
        </div>
        <div class="actions">
          {#if session.busy}
            <button
              class="stop"
              onclick={() => session.interrupt()}
              title="Interrupt (Esc)"
              aria-label="Interrupt"
            >
              <Icon name="stop" size={13} />
            </button>
          {/if}
          <button
            class="send"
            onclick={submit}
            disabled={!text.trim() && images.length === 0 && uploads.length === 0}
            title="Send message"
            aria-label="Send message"
          >
            <Icon name="send" size={15} />
          </button>
        </div>
      </div>
      <input
        class="file-input"
        type="file"
        accept="image/*,application/pdf,text/*,.doc,.docx,.xls,.xlsx,.ppt,.pptx,.csv,.json,.md,.txt,.log,.zip,.gz,.tar"
        multiple
        bind:this={fileInput}
        onchange={onpick}
        hidden
      />
    </div>
  </div>
</footer>

<style>
  footer {
    padding: 12px 20px 16px;
    background: var(--bg);
  }
  .wrap {
    max-width: 760px;
    margin: 0 auto;
    position: relative;
  }
  .menu {
    position: absolute;
    bottom: calc(100% + 6px);
    left: 0;
    right: 0;
    background: var(--bg);
    border: 1px solid var(--border-strong);
    border-radius: 8px;
    overflow: hidden;
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.2);
  }
  .cmd {
    display: flex;
    width: 100%;
    align-items: baseline;
    gap: 10px;
    padding: 7px 12px;
    text-align: left;
  }
  .cmd:hover {
    background: var(--surface-2);
  }
  .cmd .name {
    font-family: var(--mono);
    font-size: 12.5px;
    color: var(--text-strong);
    flex-shrink: 0;
  }
  .cmd .desc {
    font-size: 12px;
    color: var(--text-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .menu-hint {
    padding: 5px 12px;
    font-size: 11px;
    color: var(--text-muted);
    border-top: 1px solid var(--border);
  }
  .box {
    display: flex;
    flex-direction: column;
    gap: 6px;
    border: 1px solid var(--border);
    border-radius: 10px;
    background: var(--surface);
    padding: 10px 12px 8px;
    /* Size queries let the composer adapt to its actual width (a docked
       side panel squeezes the chat below the mobile breakpoint too). */
    container-type: inline-size;
  }
  .box:focus-within {
    border-color: var(--border-strong);
  }
  .input-wrap {
    position: relative;
  }
  .attachments {
    display: flex;
    gap: 8px;
    flex-wrap: wrap;
  }
  .attachment {
    position: relative;
  }
  .attachment img {
    display: block;
    height: 52px;
    width: auto;
    max-width: 120px;
    object-fit: cover;
    border-radius: 6px;
    border: 1px solid var(--border);
  }
  /* Non-image attachments render as a chip: icon + name, removable like
     an image thumbnail. */
  .attachment.file {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    max-width: 180px;
    height: 52px;
    padding: 0 26px 0 10px;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--surface);
    color: var(--text-muted);
    font-size: 12px;
  }
  .attachment.file .file-name {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .attachment.file .remove {
    right: 4px;
    top: 50%;
    transform: translateY(-50%);
  }
  .attachment .remove {
    position: absolute;
    top: -5px;
    right: -5px;
    display: flex;
    align-items: center;
    justify-content: center;
    width: 16px;
    height: 16px;
    border-radius: 50%;
    background: var(--bg);
    border: 1px solid var(--border-strong);
    color: var(--text-muted);
  }
  .attachment .remove:hover {
    color: var(--danger);
    border-color: var(--danger);
  }
  /* The highlighter mirrors the textarea exactly; its text is transparent
     so only the <mark> chip backgrounds show through behind the real
     (editable) text. */
  /* Both layers carry the same horizontal padding so the chip bleed has
     room at the box edge and the overlay stays glyph-aligned. */
  .highlighter {
    position: absolute;
    inset: 0;
    overflow: hidden;
    white-space: pre-wrap;
    word-break: break-word;
    color: transparent;
    line-height: 1.5;
    font: inherit;
    pointer-events: none;
    padding: 0 7px;
  }
  /* Chip background extends past the glyphs: horizontal padding cancelled
     by negative margin keeps the overlay in sync with the textarea's text
     metrics, and shadow spread adds the vertical bleed. The total bleed
     must stay well under a space's advance width, or the word after the
     chip starts visually inside its border. */
  .highlighter :global(mark) {
    color: transparent;
    background: var(--accent-soft);
    border-radius: 5px;
    padding: 0 2px;
    margin: 0 -2px;
    box-shadow:
      0 0 0 1px var(--accent-soft),
      0 0 0 2px color-mix(in srgb, var(--accent), transparent 70%);
  }
  textarea {
    position: relative;
    display: block;
    width: 100%;
    resize: none;
    border: none;
    outline: none;
    background: none;
    max-height: 200px;
    line-height: 1.5;
    padding: 0 7px;
  }
  .bottom {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .selectors {
    display: flex;
    align-items: center;
    gap: 12px;
    flex: 1;
    min-width: 0;
  }
  .actions {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-shrink: 0;
  }
  .attach {
    width: 30px;
    height: 30px;
    border-radius: 8px;
  }
  .send {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 30px;
    height: 30px;
    border-radius: 50%;
    background: var(--accent);
    color: var(--accent-contrast);
    flex-shrink: 0;
  }
  .send:hover:not(:disabled) {
    background: color-mix(in srgb, var(--accent), var(--text-strong) 12%);
  }
  .send:disabled {
    background: var(--surface-2);
    color: var(--text-muted);
    cursor: default;
  }
  .stop {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 30px;
    height: 30px;
    border-radius: 8px;
    color: var(--text-muted);
    flex-shrink: 0;
  }
  .stop:hover {
    color: var(--danger);
    background: var(--surface-2);
  }
  /* Narrow composer: mode/model keep their own space but stay on the
     same line as attach and send — one balanced row (paperclip · mode ·
     model · send), the labels truncating when the box squeezes instead
     of the row wrapping into a sparse two-tier layout. The dropdown
     triggers cap at the wrapper and ellipsize, so a docked side panel
     (which squeezes the chat below the mobile breakpoint too) gets the
     same treatment as a phone. */
  @container (max-width: 620px) {
    .selectors {
      flex: 1;
      min-width: 0;
    }
    .selectors :global(.dropdown) {
      flex: 1;
      min-width: 0;
    }
  }
  /* Mobile: 16px input text (below that iOS zooms the page on focus,
     and the highlighter inherits the same metrics), tighter margins. */
  @media (max-width: 719px) {
    footer {
      padding: 10px 12px 12px;
    }
    .input-wrap {
      font-size: 16px;
    }
  }
</style>
