/** The chat overlay's client side: the style shape, the display stream, and the
 *  pure rules that decide which lines are on screen.
 *
 *  Kept free of React so the rules are unit-tested directly and the overlay
 *  page's chunk stays small: OBS loads it in a browser source that should cost
 *  next to nothing. */

/** The overlay key travels in this header, never in a URL the server sees. */
export const OVERLAY_KEY_HEADER = "X-Overlay-Key";

export type OverlayPreset = "classic" | "bubble" | "outline" | "compact";
export type OverlayFont = "sans" | "serif" | "mono" | "rounded";

/** Mirrors internal/overlay.Style; the server validates every field. */
export interface OverlayStyle {
  preset: OverlayPreset;
  font: OverlayFont;
  fontSize: number;
  textColor: string;
  backgroundColor: string;
  backgroundOpacity: number;
  authorColors: boolean;
  fadeSeconds: number;
  maxLines: number;
  showPlatform: boolean;
  showBadges: boolean;
}

export const OVERLAY_FONTS: readonly OverlayFont[] = ["sans", "serif", "mono", "rounded"];

/** Bounds, matching internal/overlay. */
export const OVERLAY_LIMITS = {
  fontSize: { min: 10, max: 72 },
  fadeSeconds: { min: 0, max: 600 },
  maxLines: { min: 1, max: 50 },
  backgroundOpacity: { min: 0, max: 100 },
} as const;

/** One line as the display stream delivers it (eventstream.DisplayLine). There
 *  is no author id, account or moderation field to render, by construction. */
export interface DisplayLine {
  id: string;
  platform: string;
  name: string;
  color?: string;
  badges?: { id: string; version?: string; label?: string }[];
  moderator?: boolean;
  subscriber?: boolean;
  broadcaster?: boolean;
  text: string;
  action?: boolean;
  at: string;
}

/** A line on screen: the line plus when this client received it, which is what
 *  fade counts from (a platform's own timestamp can be minutes off). */
export interface ShownLine extends DisplayLine {
  receivedAt: number;
}

/** What GET /overlay/config returns: everything the page needs, no key. */
export interface OverlayConfig {
  name: string;
  platforms: string[];
  style: OverlayStyle;
}

/** One overlay as Settings manages it. */
export interface ChatOverlay {
  id: number;
  name: string;
  /** Empty when revoked. */
  obsKey: string;
  platforms: string[];
  style: OverlayStyle;
  createdAt: string;
  keyRotatedAt: string;
}

/** What Settings sends to create or update one. */
export interface OverlayInput {
  name: string;
  platforms: string[];
  style: OverlayStyle;
}

export interface OverlaysResponse {
  overlays: ChatOverlay[];
  presets: Record<OverlayPreset, OverlayStyle>;
  presetOrder: OverlayPreset[];
  platforms: string[];
  stats: { shown: number; blocked: number; expired: number };
  maxHoldMs: number;
}

/** The OBS URL. The key is in the FRAGMENT: a browser never sends a fragment
 *  to a server, never puts it in a Referer, and no proxy logs it. */
export function overlayUrl(origin: string, key: string, dock = false): string {
  return `${origin}/overlay${dock ? "/dock" : ""}#key=${encodeURIComponent(key)}`;
}

/** The OBS URL with the key in the PATH: /overlay/k/{key}, /overlay/dock/k/{key}.
 *
 *  For an OBS browser source that drops the fragment, which leaves the
 *  fragment URL with no key at all. It is the SECOND choice, and the settings
 *  page offers it as such: a path reaches the server, so a reverse proxy in
 *  front of polyemesis writes it into its own access log, where polyemesis
 *  cannot scrub it. polyemesis's own log records the route pattern instead,
 *  and the page sends Referrer-Policy: no-referrer, so neither of those
 *  carries it. The page still sends the key as X-Overlay-Key; the server
 *  never reads it from the path. */
export function overlayPathUrl(origin: string, key: string, dock = false): string {
  return `${origin}/overlay${dock ? "/dock" : ""}/k/${encodeURIComponent(key)}`;
}

/** The poll widget's OBS URL (#14): the same key, its own page, so the poll can
 *  be placed apart from the chat lines. Key in the fragment, like overlayUrl. */
export function pollWidgetUrl(origin: string, key: string): string {
  return `${origin}/overlay/poll#key=${encodeURIComponent(key)}`;
}

/** The poll widget with the key in the path, for an OBS that drops fragments.
 *  Same caveat as overlayPathUrl: a reverse proxy logs the path. */
export function pollWidgetPathUrl(origin: string, key: string): string {
  return `${origin}/overlay/poll/k/${encodeURIComponent(key)}`;
}

/** Reads the key back out of the fragment. */
export function keyFromHash(hash: string): string {
  const h = hash.startsWith("#") ? hash.slice(1) : hash;
  return new URLSearchParams(h).get("key") ?? "";
}

/** /overlay, /overlay/dock or /overlay/poll, each optionally with /k/{key}. */
const OVERLAY_PATH = /^\/overlay(?:\/(dock|poll))?(?:\/k\/([^/]+))?$/;

/** Whether this page is the overlay or its dock, and its key -- or null when
 *  the path is not one of the four the server serves the overlay page on.
 *
 *  ONE PARSER FOR BOTH QUESTIONS, because they used to be two and disagreed:
 *  the app decided "overlay" by exact match on /overlay and /overlay/dock, and
 *  the page decided "dock" by a path ending in /dock. The path form ends in its
 *  key, so it would have been a login screen inside OBS, or an overlay drawn as
 *  a dock.
 *
 *  The poll widget (/overlay/poll) is a third page on the same parser.
 *
 *  A key in the path wins over one in the fragment. Both appear only in a URL
 *  edited by hand, and the path is the one the page was served under. */
export function overlayLocation(
  pathname: string,
  hash: string,
): { key: string; dock: boolean; poll: boolean } | null {
  // Trailing slashes trimmed by hand: /\/+$/ backtracks on a long run of them.
  let end = pathname.length;
  while (end > 1 && pathname[end - 1] === "/") end--;
  const m = OVERLAY_PATH.exec(pathname.slice(0, end));
  if (!m) return null;
  let key = "";
  if (m[2]) {
    try {
      key = decodeURIComponent(m[2]);
    } catch {
      key = ""; // a malformed escape is no key, and the server answers 401
    }
  }
  return { key: key || keyFromHash(hash), dock: m[1] === "dock", poll: m[1] === "poll" };
}

// ---------------------------------------------------------------- the rules

/** Applies a new line: appended, then trimmed to maxLines (oldest first). */
export function addLine(lines: ShownLine[], line: ShownLine, maxLines: number): ShownLine[] {
  if (lines.some((l) => l.id === line.id)) return lines;
  const next = [...lines, line];
  return next.length > maxLines ? next.slice(next.length - maxLines) : next;
}

/** Applies a removal. The same render that receives it drops the lines. */
export function removeLines(lines: ShownLine[], ids: readonly string[]): ShownLine[] {
  if (ids.length === 0) return lines;
  const gone = new Set(ids);
  const next = lines.filter((l) => !gone.has(l.id));
  return next.length === lines.length ? lines : next;
}

/** Drops lines older than fadeSeconds. Zero keeps every line. */
export function expireLines(lines: ShownLine[], fadeSeconds: number, now: number): ShownLine[] {
  if (fadeSeconds <= 0) return lines;
  const cutoff = now - fadeSeconds * 1000;
  const next = lines.filter((l) => l.receivedAt > cutoff);
  return next.length === lines.length ? lines : next;
}

/** A colour with an alpha channel, from #rrggbb and 0-100. */
export function withOpacity(hex: string, opacity: number): string {
  const m = /^#([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})$/i.exec(hex);
  if (!m) return "transparent";
  const [r, g, b] = [m[1], m[2], m[3]].map((x) => parseInt(x, 16));
  const a = Math.max(0, Math.min(100, opacity)) / 100;
  return `rgba(${r}, ${g}, ${b}, ${a})`;
}

export const FONT_STACKS: Record<OverlayFont, string> = {
  sans: "system-ui, -apple-system, 'Segoe UI', Roboto, sans-serif",
  serif: "Georgia, 'Times New Roman', serif",
  mono: "ui-monospace, 'SFMono-Regular', Menlo, Consolas, monospace",
  rounded: "ui-rounded, 'SF Pro Rounded', 'Nunito', system-ui, sans-serif",
};

/** Short platform marks: text, not logos, so a Twitch-only overlay carries no
 *  other platform's branding even when the mark is on. */
export const PLATFORM_MARK: Record<string, { label: string; color: string }> = {
  twitch: { label: "TTV", color: "#9146ff" },
  youtube: { label: "YT", color: "#ff0033" },
  kick: { label: "KICK", color: "#53fc18" },
  facebook: { label: "FB", color: "#1877f2" },
  rumble: { label: "RMBL", color: "#85c742" },
};

// ---------------------------------------------------------- the poll widget

/** A poll as the widget draws it (eventstream.PollDisplay): labels and totals.
 *  There is no platform and no voter in it, by construction -- the widget is
 *  logo-free and shown on every overlay whatever its platform filter. */
export interface PollDisplay {
  id: string;
  question: string;
  options: { label: string; votes: number }[];
  total: number;
  state: "open" | "closing" | "ended" | string;
  endsAt: string;
  endedAt?: string;
}

/** A poll on screen, plus when this client received its latest update. */
export interface ShownPoll extends PollDisplay {
  receivedAt: number;
}

/** How long a finished poll's result stays on screen. */
export const POLL_LINGER_MS = 20_000;

/** How far the OBS machine's clock may disagree with the server's before an
 *  ended poll replayed on reconnect is judged by the server's end time. */
const CLOCK_SLACK_MS = 60_000;

/** Applies a poll.display. Events arrive in order, so the newest always wins --
 *  for the same poll an update, for a new id a replacement. Anything that does
 *  not look like a poll is ignored. */
export function applyPoll(_current: ShownPoll | null, next: PollDisplay, now: number): ShownPoll | null {
  if (!next || typeof next.id !== "string" || !Array.isArray(next.options)) return _current;
  return { ...next, receivedAt: now };
}

/** Whether the widget shows the poll now: while it is open or closing, and for
 *  POLL_LINGER_MS after it ended. An ended poll replayed on a reconnect long
 *  after it finished is not shown again: judged by when it arrived AND by the
 *  server's end time, with slack for a skewed clock. */
export function pollVisible(p: ShownPoll | null, now: number, lingerMs = POLL_LINGER_MS): boolean {
  if (!p) return false;
  if (p.state === "open" || p.state === "closing") return true;
  if (p.state !== "ended") return false;
  if (now - p.receivedAt >= lingerMs) return false;
  const ended = p.endedAt ? Date.parse(p.endedAt) : NaN;
  if (!Number.isNaN(ended) && now - ended >= lingerMs + CLOCK_SLACK_MS) return false;
  return true;
}

/** Each option's share of the votes, whole percent, 0 when nobody voted. */
export function pollPercents(p: PollDisplay): number[] {
  return p.options.map((o) => (p.total > 0 ? Math.round((o.votes * 100) / p.total) : 0));
}

/** Seconds until the poll closes, never negative. */
export function pollSecondsLeft(p: PollDisplay, now: number): number {
  const ends = Date.parse(p.endsAt);
  if (Number.isNaN(ends)) return 0;
  return Math.max(0, Math.ceil((ends - now) / 1000));
}

// ---------------------------------------------------------- the stream

/** One server-sent event. */
export interface SSEFrame {
  id?: string;
  event?: string;
  data: string;
}

/** Splits an SSE byte stream into frames. Feed it chunks as they arrive; it
 *  keeps the partial tail between calls. */
export class SSEParser {
  private buf = "";

  push(chunk: string): SSEFrame[] {
    this.buf += chunk.replace(/\r\n/g, "\n");
    const out: SSEFrame[] = [];
    let at: number;
    while ((at = this.buf.indexOf("\n\n")) >= 0) {
      const block = this.buf.slice(0, at);
      this.buf = this.buf.slice(at + 2);
      const f: SSEFrame = { data: "" };
      const data: string[] = [];
      for (const line of block.split("\n")) {
        if (line === "" || line.startsWith(":")) continue;
        const colon = line.indexOf(":");
        const field = colon < 0 ? line : line.slice(0, colon);
        let value = colon < 0 ? "" : line.slice(colon + 1);
        if (value.startsWith(" ")) value = value.slice(1);
        if (field === "id") f.id = value;
        else if (field === "event") f.event = value;
        else if (field === "data") data.push(value);
      }
      if (data.length === 0) continue;
      f.data = data.join("\n");
      out.push(f);
    }
    return out;
  }
}

export interface StreamHandlers {
  onLine?: (line: DisplayLine) => void;
  onRemove?: (ids: string[]) => void;
  /** The poll widget's projection. Only the poll page listens; the chat
   *  overlay and dock leave it unset and never draw a poll. */
  onPoll?: (p: PollDisplay) => void;
  /** The key was refused (401): rotated, revoked or mistyped. Not retried. */
  onDenied: () => void;
  onState?: (connected: boolean) => void;
  /** Each time the stream (re)connects. A reconnect follows every save in
   *  Settings -- the server ends open streams so a new platform filter
   *  applies -- so this is where the page re-reads its style. */
  onConnect?: () => void | Promise<void>;
}

/** Reads the overlay's config with its key. null means refused (401). */
export async function fetchOverlayConfig(
  key: string,
  signal: AbortSignal,
  fetchImpl: typeof fetch = fetch,
): Promise<OverlayConfig | null> {
  const res = await fetchImpl("/api/v1/overlay/config", {
    headers: { [OVERLAY_KEY_HEADER]: key },
    cache: "no-store",
    credentials: "omit",
    referrerPolicy: "no-referrer",
    signal,
  });
  if (res.status === 401) return null;
  if (!res.ok) throw new Error(`config ${res.status}`);
  return (await res.json()) as OverlayConfig;
}

/** Opens the display stream and keeps it open, resuming with Last-Event-ID
 *  after a drop. fetch rather than EventSource because EventSource cannot send
 *  the key in a header, and the key must not go in the stream's URL.
 *
 *  Resolves when `signal` aborts or the key is refused. */
export async function runOverlayStream(
  key: string,
  h: StreamHandlers,
  signal: AbortSignal,
  fetchImpl: typeof fetch = fetch,
  wait: (ms: number) => Promise<void> = (ms) => new Promise((r) => setTimeout(r, ms)),
): Promise<void> {
  let lastId = "";
  let attempt = 0;
  while (!signal.aborted) {
    try {
      const headers: Record<string, string> = { [OVERLAY_KEY_HEADER]: key };
      if (lastId) headers["Last-Event-ID"] = lastId;
      const res = await fetchImpl("/api/v1/overlay/events", {
        headers,
        signal,
        cache: "no-store",
        credentials: "omit",
        referrerPolicy: "no-referrer",
      });
      if (res.status === 401) {
        h.onDenied();
        return;
      }
      if (!res.ok || !res.body) throw new Error(`stream ${res.status}`);
      h.onState?.(true);
      attempt = 0;
      await h.onConnect?.();
      const reader = res.body.getReader();
      const dec = new TextDecoder();
      const parser = new SSEParser();
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        for (const f of parser.push(dec.decode(value, { stream: true }))) {
          if (f.id) lastId = f.id;
          dispatch(f, h);
        }
      }
    } catch {
      if (signal.aborted) return;
    }
    h.onState?.(false);
    if (signal.aborted) return;
    // A stream that ends is normal (a key rotation ends it on purpose, and the
    // next attempt then gets its 401); back off gently, capped at 10 s.
    attempt++;
    await wait(Math.min(10_000, 500 * 2 ** Math.min(attempt, 5)));
  }
}

function dispatch(f: SSEFrame, h: StreamHandlers): void {
  let env: { type?: string; data?: unknown };
  try {
    env = JSON.parse(f.data);
  } catch {
    return;
  }
  if (!env.data || typeof env.data !== "object") return;
  if (env.type === "chat.display") {
    h.onLine?.(env.data as DisplayLine);
  } else if (env.type === "chat.undisplay") {
    const ids = (env.data as { ids?: unknown }).ids;
    if (Array.isArray(ids)) h.onRemove?.(ids.filter((x): x is string => typeof x === "string"));
  } else if (env.type === "poll.display") {
    h.onPoll?.(env.data as PollDisplay);
  }
  // Anything else is ignored: the server sends nothing else to an overlay key.
}
