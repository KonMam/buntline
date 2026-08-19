// Cross-session notifications: one EventSource on /api/events feeds the
// bell (in-app unread list), the attention banner (another session needs
// an approval or question), and OS-level popups via the Web Notifications
// API. Nothing here needs Web Push: the stream only lives while the tab
// is open, which is the agreed scope.
//
// Multi-tab: every tab keeps its own in-app bell, but OS notifications
// come from exactly one tab (the leader), elected over a BroadcastChannel
// by lowest tab id with heartbeat liveness. Without this, two open tabs
// would double every popup.
import type { AgentEvent, SessionMeta } from './types';
import { prettyArgs } from './format';

export type NotifyKind = 'approval' | 'question' | 'turnEnd' | 'error';

// kindLabel renders a notification kind for the bell (kinds double as
// settings keys, which is why they are camelCase).
export const kindLabel: Record<NotifyKind, string> = {
  approval: 'approval',
  question: 'question',
  turnEnd: 'turn end',
  error: 'error',
};

// One entry in the in-app bell list.
export interface NotificationItem {
  id: string; // dedup key, stable across tabs
  sessionId: string;
  sessionTitle: string;
  kind: NotifyKind;
  title: string;
  body: string;
  time: string;
  read: boolean;
}

// Per-browser settings (a phone and a desktop almost certainly want
// different noise levels), persisted in localStorage.
export interface NotifySettings {
  /** Master switch: everything off. */
  enabled: boolean;
  /** OS-level popups (Web Notifications). */
  os: boolean;
  /** Notify even while the app is visible and the session is active. */
  whileActive: boolean;
  approval: boolean;
  question: boolean;
  turnEnd: boolean;
  error: boolean;
}

const SETTINGS_KEY = 'buntline.notify.settings';

export function defaultSettings(): NotifySettings {
  return {
    enabled: true,
    os: true,
    whileActive: false,
    approval: true,
    question: true,
    turnEnd: false, // turn completions are the noisy one; opt in
    error: true,
  };
}

// loadSettings reads the persisted settings, falling back to defaults per
// key so a future schema keeps working. storage is injectable for tests.
export function loadSettings(storage: Pick<Storage, 'getItem'> = localStorage): NotifySettings {
  const d = defaultSettings();
  try {
    const raw = storage.getItem(SETTINGS_KEY);
    if (!raw) return d;
    const saved = JSON.parse(raw) as Partial<NotifySettings>;
    return { ...d, ...saved };
  } catch {
    return d;
  }
}

export function saveSettings(s: NotifySettings, storage: Pick<Storage, 'setItem'> = localStorage) {
  try {
    storage.setItem(SETTINGS_KEY, JSON.stringify(s));
  } catch {
    // private mode etc.; settings just don't persist
  }
}

// classifyEvent maps a global-stream agent event to a notification
// (null when it is not something the user should see). Pure: the store
// and the tests share it.
export function classifyEvent(
  ev: AgentEvent,
  sessionTitle: string,
): { kind: NotifyKind; title: string; body: string } | null {
  switch (ev.type) {
    case 'approval_request':
      return {
        kind: 'approval',
        title: 'Approval needed',
        body: `${ev.tool_name ?? 'tool'} ${prettyArgs(ev.tool_args ?? '', 60)}`.trim(),
      };
    case 'question_request':
      return {
        kind: 'question',
        title: 'Question for you',
        body: ev.question ?? '',
      };
    case 'turn_end':
      // Subagent turns (parent_id set) are not the user's turn; the
      // parent's turn_end is what they are waiting for.
      if (ev.parent_id) return null;
      return { kind: 'turnEnd', title: 'Turn finished', body: sessionTitle };
    case 'error':
      return { kind: 'error', title: 'Something went wrong', body: ev.error ?? 'error' };
    default:
      return null;
  }
}

// dedupKey gives a notification a stable identity: re-broadcast of the
// same approval/question must not double the item, and a result event
// resolves the pending item. Errors coalesce per session+body (they can
// repeat on reconnect); turn ends key on the turn id.
export function dedupKey(ev: AgentEvent, sessionId: string): string | null {
  switch (ev.type) {
    case 'approval_request':
      return ev.approval_id ? `approval:${ev.approval_id}` : null;
    case 'approval_result':
      return ev.approval_id ? `approval:${ev.approval_id}` : null;
    case 'question_request':
      return ev.approval_id ? `question:${ev.approval_id}` : null;
    case 'question_result':
      return ev.approval_id ? `question:${ev.approval_id}` : null;
    case 'turn_end':
      return ev.parent_id ? null : `turn:${ev.turn_id ?? sessionId}:${ev.time}`;
    case 'error':
      return `error:${sessionId}:${ev.error ?? ''}`;
    default:
      return null;
  }
}

// resolutionOf reports which pending kind (approval/question) an event
// resolves, so the bell can drop the item the moment the loop picks the
// answer up — even when the answer came from another tab.
export function resolutionOf(ev: AgentEvent): NotifyKind | null {
  switch (ev.type) {
    case 'approval_result':
      return 'approval';
    case 'question_result':
      return 'question';
    default:
      return null;
  }
}

export interface NotifyDecision {
  kind: NotifyKind;
  sessionId: string;
  visible: boolean; // app visible AND this is the active session
}

// shouldNotifyOS decides whether an OS popup fires for an event: the
// master + per-kind switches must be on, permission granted, and the
// user plausibly not already looking at it (unless whileActive).
export function shouldNotifyOS(
  settings: NotifySettings,
  decision: NotifyDecision,
  permission: 'default' | 'granted' | 'denied',
): boolean {
  if (!settings.enabled || !settings.os || permission !== 'granted') return false;
  if (!settings[decision.kind]) return false;
  if (settings.whileActive) return true;
  return !decision.visible;
}

export interface NotifyOpts {
  document?: Document;
  onOpen?: (sessionId: string) => void;
  storage?: Pick<Storage, 'getItem' | 'setItem'>;
  channel?: BroadcastChannel | null;
  NotificationCtor?: typeof Notification;
}

export class NotificationCenter {
  // In-app unread list, newest first.
  items = $state<NotificationItem[]>([]);
  unread = $derived(this.items.filter((i) => !i.read).length);

  // The current cross-session needs-you item (approval/question in a
  // session that is not active), for the in-app banner. Cleared when the
  // event resolves or the user jumps to that session.
  attention = $state<NotificationItem | null>(null);

  settings = $state<NotifySettings>(loadSettings());

  osAvailable = $state(false);
  osPermission = $state<'default' | 'granted' | 'denied'>('default');
  // iOS < 16.4 exposes Notification but cannot request permission
  // outside an installed home-screen app; surface that in the UI.
  canRequest = $state(true);

  #source: EventSource | null = null;
  #channel: BroadcastChannel | null = null;
  #doc: Document | undefined;
  #onOpen: (sessionId: string) => void = () => {};
  #storage: Pick<Storage, 'getItem' | 'setItem'>;
  #Notification: typeof Notification | undefined;
  #tabId = Math.random().toString(36).slice(2) + Date.now().toString(36);
  #peers = new Map<string, number>(); // tab id -> last seen (ms)
  #leaderId: string | null = null;
  #sessions = new Map<string, string>(); // session id -> title
  #activeId: string | null = null;
  #heartbeat: ReturnType<typeof setInterval> | null = null;
  #started = false;

  constructor(opts: NotifyOpts = {}) {
    this.#doc = opts.document ?? (typeof document !== 'undefined' ? document : undefined);
    this.#onOpen = opts.onOpen ?? (() => {});
    this.#storage = opts.storage ?? localStorage;
    this.#Notification =
      opts.NotificationCtor ?? (typeof Notification !== 'undefined' ? Notification : undefined);
    this.#channel =
      opts.channel !== undefined
        ? opts.channel
        : typeof BroadcastChannel !== 'undefined'
          ? new BroadcastChannel('buntline-notifications')
          : null;
    this.settings = loadSettings(this.#storage);
    this.#initOS();
  }

  #initOS() {
    const N = this.#Notification;
    if (!N || !this.#doc || this.#doc.visibilityState === undefined) {
      this.osAvailable = false;
      return;
    }
    this.osAvailable = true;
    this.osPermission = N.permission;
    // Older iOS Safari (and some embedded webviews) have the constructor
    // but no requestPermission method.
    this.canRequest = typeof N.requestPermission === 'function';
  }

  /** Session titles feed notification bodies; the sidebar list is the source. */
  setSessions(metas: SessionMeta[]) {
    this.#sessions = new Map(metas.map((m) => [m.id, m.title]));
    // The active session's title may have just been generated; refresh
    // matching items so the bell shows the real name.
    for (const item of this.items) {
      const t = this.#sessions.get(item.sessionId);
      if (t && t !== item.sessionTitle) item.sessionTitle = t;
    }
  }

  setActive(id: string | null) {
    this.#activeId = id;
    // The user is looking at the session that needed them: the attention
    // banner and its OS notification are done.
    if (id && this.attention?.sessionId === id) this.attention = null;
    for (const item of this.items) {
      if (item.sessionId === id) item.read = true;
    }
  }

  start() {
    if (this.#started) return;
    this.#started = true;
    if (this.#channel) {
      this.#channel.onmessage = (e) => this.#onChannel(e);
      this.#channel.postMessage({ type: 'hello', id: this.#tabId });
    }
    this.#peers.set(this.#tabId, Date.now());
    this.#reelect();
    this.#heartbeat = setInterval(() => {
      if (this.#channel) this.#channel.postMessage({ type: 'heartbeat', id: this.#tabId });
      this.#peers.set(this.#tabId, Date.now());
      this.#reelect();
    }, 15_000);
    this.#source = new EventSource('/api/events');
    this.#source.onmessage = (raw) => {
      try {
        const ge = JSON.parse(raw.data) as { session_id: string; event: AgentEvent };
        this.#handle(ge.session_id, ge.event);
      } catch {
        // malformed frame; ignore
      }
    };
  }

  stop() {
    this.#started = false;
    this.#source?.close();
    this.#source = null;
    if (this.#heartbeat) clearInterval(this.#heartbeat);
    this.#heartbeat = null;
    this.#channel?.close();
    this.#channel = null;
  }

  /** Ask for OS notification permission; must be called from a user gesture. */
  async requestPermission(): Promise<void> {
    const N = this.#Notification;
    if (!N || !this.canRequest) return;
    try {
      this.osPermission = await N.requestPermission();
    } catch {
      // some browsers reject the promise style
    }
    if (this.osPermission === 'granted' && this.#leaderId === this.#tabId) {
      // A confirmation popup proves the pipe works; also a chance to
      // explain what is coming.
      this.#osNotify({
        id: 'perm:test',
        sessionId: '',
        sessionTitle: '',
        kind: 'turnEnd',
        title: 'buntline notifications on',
        body: 'You will hear about approvals, questions, and errors from every session.',
        time: new Date().toISOString(),
        read: false,
      });
    }
  }

  setSetting<K extends keyof NotifySettings>(key: K, value: NotifySettings[K]) {
    this.settings = { ...this.settings, [key]: value };
    saveSettings(this.settings, this.#storage);
  }

  markAllRead() {
    for (const item of this.items) item.read = true;
  }

  dismissAttention() {
    this.attention = null;
  }

  // -- event handling -------------------------------------------------

  #handle(sessionId: string, ev: AgentEvent) {
    const settings = this.settings;
    if (!settings.enabled) return;

    // A result resolves the pending item (bell + attention), possibly
    // from another tab.
    const resolving = resolutionOf(ev);
    if (resolving) {
      const key = dedupKey(ev, sessionId);
      if (key) this.#resolve(key);
      return;
    }

    const kind = classifyEvent(ev, this.#sessions.get(sessionId) ?? sessionId)?.kind;
    if (!kind || !settings[kind]) return;

    const key = dedupKey(ev, sessionId);
    if (!key) return;
    // Idempotent: a reconnect may re-deliver a persisted event stream
    // (the SSE stream itself does not replay, but be safe).
    if (this.items.some((i) => i.id === key)) return;

    const item = this.#buildItem(key, sessionId, ev, kind);
    const visible = this.#isVisible(sessionId);

    // In-app: the bell and the banner only care when the user is not
    // already staring at the thing.
    if (!visible) {
      this.items = [item, ...this.items];
      if (this.items.length > 50) this.items = this.items.slice(0, 50);
      if ((kind === 'approval' || kind === 'question') && this.attention === null) {
        this.attention = item;
      }
    }

    // OS: leader tab only, subject to permission and settings.
    if (this.#leaderId === this.#tabId) {
      const decision: NotifyDecision = { kind, sessionId, visible };
      if (shouldNotifyOS(settings, decision, this.osPermission)) {
        this.#osNotify(item);
      }
    }
  }

  #buildItem(key: string, sessionId: string, ev: AgentEvent, kind: NotifyKind): NotificationItem {
    const sessionTitle = this.#sessions.get(sessionId) ?? 'session';
    const classified = classifyEvent(ev, sessionTitle)!;
    return {
      id: key,
      sessionId,
      sessionTitle,
      kind,
      title: classified.title,
      body: classified.body,
      time: ev.time,
      read: false,
    };
  }

  #resolve(key: string) {
    this.items = this.items.filter((i) => i.id !== key);
    if (this.attention?.id === key) this.attention = null;
  }

  #isVisible(sessionId: string): boolean {
    const doc = this.#doc;
    const pageVisible = !doc || doc.visibilityState === 'visible';
    return pageVisible && this.#activeId === sessionId;
  }

  #osNotify(item: NotificationItem) {
    const N = this.#Notification;
    if (!N || N.permission !== 'granted') return;
    try {
      const n = new N(item.title, {
        body: item.body,
        tag: item.id, // same id replaces the old popup instead of stacking
      });
      n.onclick = () => {
        this.#doc?.defaultView?.focus();
        if (item.sessionId) this.#onOpen(item.sessionId);
        n.close();
      };
    } catch {
      // construction can throw in odd webviews; never crash the app
    }
  }

  // -- leader election -------------------------------------------------

  #onChannel(e: MessageEvent) {
    const msg = e.data as { type: string; id?: string };
    if (!msg.id) return;
    this.#peers.set(msg.id, Date.now());
    if (msg.type === 'hello') this.#channel?.postMessage({ type: 'hello', id: this.#tabId });
    this.#reelect();
  }

  #reelect() {
    const now = Date.now();
    for (const [id, seen] of this.#peers) {
      if (now - seen > 45_000) this.#peers.delete(id);
    }
    let best: string | null = null;
    for (const id of this.#peers.keys()) {
      if (best === null || id < best) best = id;
    }
    this.#leaderId = best;
  }
}
