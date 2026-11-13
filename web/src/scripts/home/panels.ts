/* The feature tour: small live copies of what the product does around the
 * stream. FeatureTour.astro renders every panel in a complete resting state
 * (name, outcome sentence, the miniature in a believable still state); this
 * module only adds a short loop to each, behind the motion gate.
 *
 * The data below is imported by FeatureTour.astro too, so the resting state and
 * the loop draw from one list. Every chat line, caption, file name and handle
 * is illustrative: a match being called, a service starting, a stream going
 * live. Loops are deterministic (indexed by tick, never random), run only while
 * the panel is at least a quarter on screen and motion is allowed, and put the
 * resting state back when they stop, so a panel never rests on a half-played
 * frame.
 *
 * Markup contract, per panel root [data-panel="<name>"]:
 *   [data-live]   everything the loop changes, rendered from miniature(name).
 *                 A loop swaps its content frame by frame; stop restores it.
 * The failover panel is the exception: it reuses failover.ts and its own
 * contract (see FeatureTour.astro), so initFailover owns that panel's loop. */

import { initFailover } from "./failover";
import { whileVisible, type MotionGate } from "./motion";

/** How many rows a feed shows, at rest and while looping. */
export const FEED_ROWS = 5;

export const CHAT: ReadonlyArray<readonly [platform: string, handle: string, text: string]> = [
  ["YouTube", "maple_fox", "Hello from the east coast!"],
  ["Twitch", "kestrel22", "Audio on the commentary feed is clear tonight."],
  ["Kick", "nightowl", "Is the second-half stream starting soon?"],
  ["Facebook", "Dana R.", "Good evening everyone."],
  ["Twitch", "paperkite", "Loving the French commentary."],
  ["YouTube", "tidepool", "Can we get the lineup on screen?"],
  ["Rumble", "quill_77", "Watching on the big screen at home."],
  ["Kick", "harbor_lights", "Great picture, thanks for streaming."],
];

export const CAPTION = "Welcome back to the second half. The home side has pushed up and the crowd is on its feet.";
const CAPTION_WORDS = CAPTION.split(" ");

/* Names follow docs/CHAT-EXPORT.md: a recording, its .chat.json and its .chat.vtt. */
export const FILES: ReadonlyArray<readonly [name: string, kind: string]> = [
  ["rec-s1-20260304-200000.mkv", "4 tracks"],
  ["rec-s1-20260304-200000.chat.json", "chat"],
  ["rec-s1-20260304-200000.chat.vtt", "chat"],
  ["rec-s1-20260304-203000.mkv", "4 tracks"],
  ["rec-s1-20260304-203000.chat.json", "chat"],
];

/** The summary line under the file list when `n` files are written. */
export const filesSummary = (n: number) => `${n} of ${FILES.length} files written, offload mirrors a copy`;

/* Event names come from docs/HOOKS.md. */
const HOOKS = [
  "ingest.published",
  "destination.up",
  "destination.up",
  "ingest.disconnected",
  "failover.switched",
  "destination.up",
  "ingest.published",
  "failover.switched",
];
const CLOCK_START = 20 * 3600 + 2;
const pad = (x: number) => String(x).padStart(2, "0");

/** Row `i` of the hook feed: a clock time that advances by 3, 7 or 11 seconds, and an event. */
export function hookRow(i: number): readonly [time: string, event: string] {
  let s = CLOCK_START;
  for (let k = 1; k <= i; k++) s += 3 + (k % 3) * 4;
  return [`${pad(Math.floor(s / 3600) % 24)}:${pad(Math.floor(s / 60) % 60)}:${pad(s % 60)}`, HOOKS[i % HOOKS.length]];
}

/** The bonded-link readout at tick `t`: three link rates in 0..100 units of 50 kbps. */
export function linkSample(t: number) {
  const w = [62, 48, 35].map((b, i) => Math.round(b + 14 * Math.sin(t * 0.9 + i * 2)));
  return {
    links: w.map((v) => ({ pct: v, mbps: `${(v / 20).toFixed(1)} Mbps` })),
    bitrate: `${(w.reduce((a, b) => a + b, 0) / 20).toFixed(1)} Mbps`,
    rtt: `${Math.round(84 + 14 * Math.sin(t * 0.7))} ms`,
    loss: `${(0.4 + 0.3 * Math.abs(Math.sin(t * 0.5))).toFixed(1)}%`,
  };
}

const esc = (t: string) => t.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");

/* One frame of each looping panel's miniature, as markup. Frame 0 is the
 * resting state: FeatureTour.astro renders it into [data-live] with set:html,
 * and a loop only ever swaps [data-live]'s whole content for frame n, so what
 * stops a loop (frame 0 again) and what the server rendered are one function.
 * [data-live] wraps everything a loop changes, including readouts and summary
 * lines, so nothing it writes sits outside what stop restores. */
interface Loop {
  period: number;
  frame(n: number): string;
}

const LOOPS: Record<string, Loop> = {
  chat: {
    period: 1800,
    frame: (n) =>
      Array.from({ length: FEED_ROWS }, (_, i) => {
        const [platform, handle, text] = CHAT[(n + i) % CHAT.length];
        return `<div class="ch-m"><em>${esc(platform)}</em><b>${esc(handle)}</b><span>${esc(text)}</span></div>`;
      }).join(""),
  },
  captions: {
    period: 420,
    /* The full line at rest, then typed word by word. */
    frame: (n) => `${esc(n === 0 ? CAPTION : CAPTION_WORDS.slice(0, (n % CAPTION_WORDS.length) + 1).join(" "))} <i>|</i>`,
  },
  recordings: {
    period: 1700,
    frame(n) {
      const shown = n === 0 ? FILES.length : (n % FILES.length) + 1;
      const rows = FILES.slice(0, shown)
        .map(([name, kind]) => `<div class="rc-r"><span>${esc(name)}</span><span>${esc(kind)}</span></div>`)
        .join("");
      return `<div class="rc-list">${rows}</div><div class="rc-sum" data-recsum>${filesSummary(shown)}</div>`;
    },
  },
  link: {
    period: 1100,
    frame(n) {
      const s = linkSample(n);
      const rows = s.links
        .map((l, i) => `<div class="lk-l"><span>link ${i + 1}</span><i><u data-link style="width:${l.pct}%"></u></i><span data-rate>${l.mbps}</span></div>`)
        .join("");
      return (
        `<div class="lk-top"><div><span>bitrate</span><b data-v="bitrate">${s.bitrate}</b></div>` +
        `<div><span>RTT</span><b data-v="rtt">${s.rtt}</b></div><div><span>loss</span><b data-v="loss">${s.loss}</b></div></div>` +
        `<div class="lk-links" aria-label="Rate per link, illustrative">${rows}</div>`
      );
    },
  },
  hooks: {
    period: 1900,
    frame: (n) =>
      Array.from({ length: FEED_ROWS }, (_, i) => {
        const [time, event] = hookRow(n + i);
        return `<div class="hk-r"><time>${time}</time><span>${esc(event)}</span></div>`;
      }).join(""),
  },
};

/** The panels that loop by rewriting [data-live]. */
export const LOOP_PANELS = Object.keys(LOOPS);

/** Panel `name`'s resting markup, for the server to render into [data-live]. */
export const miniature = (name: string): string => LOOPS[name].frame(0);

/** The panels this module animates, in page order. */
export const PANEL_NAMES = ["failover", "chat", "captions", "recordings", "link", "hooks"] as const;

/** Is the miniature actually drawn? A closed fold (a phone) hides it, and so does display: none.
 *  checkVisibility is false for content a closed <details> keeps out of rendering, and true on a wide
 *  screen where the fold shows its content; without it (an old engine, jsdom) a closed ancestor
 *  <details> is taken to hide it. */
function rendered(el: HTMLElement): boolean {
  if (typeof el.checkVisibility === "function") return el.checkVisibility();
  const fold = el.closest("details");
  return !fold || fold.open;
}

/* A loop runs only while its miniature is rendered AND on screen AND motion is allowed. It starts when
 * a fold is opened and, when the fold is closed, stops and puts the resting markup back, so no timer
 * rewrites markup nobody can see. */
function runLoop(panel: HTMLElement, live: HTMLElement, loop: Loop, gate: MotionGate): () => void {
  const rest = live.innerHTML;
  let timer: ReturnType<typeof setInterval> | undefined;
  let watching = false; // on screen with motion allowed (whileVisible)
  const begin = () => {
    let n = 0;
    timer = setInterval(() => {
      live.innerHTML = loop.frame(++n);
    }, loop.period);
  };
  const end = () => {
    clearInterval(timer);
    timer = undefined;
    live.innerHTML = rest;
  };
  const sync = () => {
    const should = watching && rendered(live);
    if (should === (timer !== undefined)) return;
    if (should) begin();
    else end();
  };
  const unwatch = whileVisible(
    panel,
    gate,
    () => {
      watching = true;
      sync();
    },
    () => {
      watching = false;
      sync();
    },
    0.25,
  );
  const folds = [...new Set([live.closest("details")].filter((d): d is HTMLDetailsElement => d !== null))];
  for (const d of folds) d.addEventListener("toggle", sync);
  return () => {
    for (const d of folds) d.removeEventListener("toggle", sync);
    unwatch();
    if (timer !== undefined) end();
  };
}

export function initPanels(root: HTMLElement, gate: MotionGate): () => void {
  const disposers: Array<() => void> = [];
  for (const panel of root.querySelectorAll<HTMLElement>("[data-panel]")) {
    const name = panel.dataset.panel ?? "";
    if (name === "failover") {
      /* Scoped to the panel: initFailover reads only inside its root. */
      disposers.push(initFailover(panel, gate).destroy);
      continue;
    }
    const loop = LOOPS[name];
    const live = panel.querySelector<HTMLElement>("[data-live]");
    if (loop && live) disposers.push(runLoop(panel, live, loop, gate));
  }
  return () => {
    for (const d of disposers) d();
  };
}
