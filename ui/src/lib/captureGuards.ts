/* The media capture's refusals, as pure functions.
 *
 * They live here, not in ui/e2e, so that vitest runs and measures them: a guard
 * that is wrong either photographs a bad frame or refuses every good one, and
 * neither shows until a screenshot run has already cost ten minutes. The
 * Playwright side (ui/e2e/captureKit.ts) is meant to be a thin wrapper that
 * only reads the page and the API and hands the result to these.
 *
 * NOTHING HERE RETURNS OR THROWS A SECRET'S VALUE. A refusal message is read in
 * a terminal and pasted into a report; it carries kinds, file names and words.
 */

export interface SecretEntry {
  kind: string;
  value: string;
}

/** A value shorter than this is a count or a port, not a credential, and would
 *  match innocent text ("1234567" inside a timestamp). */
const MIN_SECRET_LENGTH = 8;

/** Whitespace and case do not change whether a value is legible: a long token
 *  wraps across lines, a stream key is shown upper-cased. */
const squash = (t: string) => t.replace(/\s+/g, "").toLowerCase();

function percentDecoded(t: string): string {
  try {
    return decodeURIComponent(t);
  } catch {
    return t; // a stray % in ordinary text
  }
}

/** Kinds (never values) of the secrets whose value appears in the text, once
 *  whitespace and case are ignored, in the text as shown and as percent-decoded
 *  (a URL on screen). Values under 8 characters are ignored. A MASKED value
 *  (dots and the last four characters) does not contain the value, so it is
 *  not a leak. */
export function findLegibleSecrets(visibleText: string, secrets: SecretEntry[]): string[] {
  const shown = squash(visibleText);
  const decoded = squash(percentDecoded(visibleText));
  const kinds: string[] = [];
  for (const s of secrets) {
    const v = squash(s.value);
    if (v.length < MIN_SECRET_LENGTH || !(shown.includes(v) || decoded.includes(v))) continue;
    if (!kinds.includes(s.kind)) kinds.push(s.kind);
  }
  return kinds;
}

/** The secrets named by SHOT_SECRETS_FILE. Required in every scene: a scene
 *  with a password on its sign-in page and no list of what to look for would
 *  pass every shot. A run with truly nothing to check says `none`. */
export function secretsFromEnv(spec: string | undefined, read: (path: string) => string): SecretEntry[] {
  if (!spec) {
    throw new Error("SHOT_SECRETS_FILE is required (set it to none when this run has nothing to check)");
  }
  return spec === "none" ? [] : parseSecretsFile(read(spec));
}

/** Parses the rig's secrets file: one `kind<TAB>value` per line, blank lines
 *  skipped. A malformed line is refused by its number, never quoted -- it may
 *  be a value with its kind missing. */
export function parseSecretsFile(text: string): SecretEntry[] {
  const out: SecretEntry[] = [];
  text.split(/\r?\n/).forEach((line, i) => {
    if (line.trim() === "") return;
    const tab = line.indexOf("\t");
    if (tab <= 0) throw new Error(`secrets file line ${i + 1} is not kind<TAB>value`);
    out.push({ kind: line.slice(0, tab), value: line.slice(tab + 1) });
  });
  return out;
}

/* FAILED-STATE WORDS.
 *
 * The console's `--down` colour is close to the site's accent, and a failed
 * card in a hero shot reads as design. So the capture refuses a frame whose
 * visible text SAYS a bad state: the words the console prints as badges and
 * verdicts (state.* , chatpage.stateDegraded, rend.signalError, the OBS bridge
 * states), matched as whole words, any case, in a badge OR a sentence -- an
 * error banner is a sentence.
 *
 * Only zero counts are stripped ("0 failed", "Errors: 0", a tile "Failed" over
 * "0"; a zero on the next line only counts when it is alone on that line, so
 * the previous tile's zero cannot hide the next tile's failure). Failover is a
 * feature name, not a state: word boundaries keep it out, as they do
 * "download" and "shutdown".
 *
 * Where a screen legitimately carries one of these words as STATIC copy (a
 * column header "Errors", help text, a viewer readout of a demo account), that
 * shot's manifest entry lists the word in allowFailedWords with a caption. */
const WORD_LIST = "failed|fail|crashed|unreachable|offline|degraded|reconnecting|errors?|stopped";

const ZERO_BEFORE = new RegExp(String.raw`\b0[ \t]+(?:${WORD_LIST})\b`, "gi");
const ZERO_AFTER = new RegExp(
  String.raw`\b(?:${WORD_LIST})\b[ \t]*[:=]?[ \t]*\n?[ \t]*0(?![\d.])(?=[ \t]*(?:$|\n|[·|,;)]))`,
  "gim",
);
const WORD_RE = new RegExp(String.raw`\b(${WORD_LIST})\b`, "gi");

function normalise(word: string): string {
  const w = word.toLowerCase();
  return w === "error" ? "errors" : w;
}

/** Failed-state words on screen, after removing zero counts ("0 failed", "0 stopped", "errors: 0").
 *  `allow` names words the shot's manifest entry declares as static copy. */
export function failedStateWords(visibleText: string, allow: string[] = []): string[] {
  const text = visibleText.replace(ZERO_BEFORE, "").replace(ZERO_AFTER, "");
  const allowed = new Set(allow.map(normalise));
  const words: string[] = [];
  for (const m of text.matchAll(WORD_RE)) {
    const w = normalise(m[1]);
    if (!allowed.has(w) && !words.includes(w)) words.push(w);
  }
  return words;
}

/** One running destination, as GET /api/v1/status reports it
 *  (destinations[].process: state, uptimeSec, progress.speed, dropFrames, dupFrames). */
export interface DestProgress {
  name: string;
  state: string;
  uptimeSec: number;
  speed: number;
  dropped: number;
  duplicated: number;
}

/** One destination's live-mix loudness report, as GET /api/v1/loudness returns
 *  it (reports[].integrated, .verdict, .destination as `name`). Rows are
 *  matched to destinations by name. */
export interface LoudnessRow {
  integrated: boolean;
  verdict: string;
  name: string;
}

/** The speed window that is neither amber nor a catch-up burst. */
export const MIN_SETTLED_SPEED = 0.97;
export const MAX_SETTLED_SPEED = 1.03;

export function destinationsSettled(
  rows: DestProgress[],
  loudness: LoudnessRow[],
  minUptimeSec: number,
): { ok: boolean; why: string } {
  const no = (why: string) => ({ ok: false, why });
  if (rows.length === 0) return no("no running destinations");
  for (const r of rows) {
    if (r.state !== "running") return no(`${r.name} is ${r.state}, not running`);
    if (r.uptimeSec < minUptimeSec) return no(`${r.name} has run ${r.uptimeSec} s, under ${minUptimeSec} s`);
    if (r.speed < MIN_SETTLED_SPEED || r.speed > MAX_SETTLED_SPEED) {
      return no(`${r.name} speed is ${r.speed}x, outside ${MIN_SETTLED_SPEED} to ${MAX_SETTLED_SPEED}`);
    }
    if (r.dropped > 0) return no(`${r.name} has dropped ${r.dropped} frames`);
    if (r.duplicated > 0) return no(`${r.name} has duplicated ${r.duplicated} frames`);
  }
  for (const r of rows) {
    const mine = loudness.filter((l) => l.name === r.name);
    if (mine.length === 0) return no(`${r.name} has no loudness row`);
    for (const l of mine) {
      if (!l.integrated) return no(`${r.name} loudness is not yet integrated`);
      if (l.verdict !== "pass") return no(`${r.name} loudness verdict is ${l.verdict}, not pass`);
    }
  }
  return { ok: true, why: "" };
}

/** What the page and the environment say about one frame, ready to judge. */
export interface ShootFacts {
  /** html[data-theme], or null when the attribute is absent. */
  theme: string | null | undefined;
  /** document.body.innerText plus the values of text inputs and textareas. */
  visibleText: string;
  secrets: SecretEntry[];
  /** The manifest entry's allowFailedWords. */
  allow: string[];
}

/** Every reason this frame must not be photographed, in the order they are
 *  checked: theme, secrets, failed-state words. Kinds and words only. */
export function shootProblems(f: ShootFacts): string[] {
  const out: string[] = [];
  if (f.theme !== "dark") out.push(`theme is "${f.theme ?? "none"}", not "dark"`);
  const kinds = findLegibleSecrets(f.visibleText, f.secrets);
  if (kinds.length > 0) out.push(`a secret is legible on screen (kinds: ${kinds.join(", ")})`);
  const words = failedStateWords(f.visibleText, f.allow);
  if (words.length > 0) out.push(`the page shows failed-state words: ${words.join(", ")}`);
  return out;
}

/** The slice of GET /api/v1/status this reads (engine.Status). */
export interface StatusLike {
  destinations?: Array<{
    name: string;
    enabled: boolean;
    process?: {
      state: string;
      uptimeSec: number;
      progress?: { speed: number; dropFrames: number; dupFrames: number } | null;
    } | null;
  }> | null;
}

/** Enabled destinations only: a disabled one has no process on purpose. One
 *  with no process reads as stopped, which settles nothing. */
export function progressFromStatus(status: StatusLike): DestProgress[] {
  return (status.destinations ?? [])
    .filter((d) => d.enabled)
    .map((d) => ({
      name: d.name,
      state: d.process?.state ?? "stopped",
      uptimeSec: d.process?.uptimeSec ?? 0,
      speed: d.process?.progress?.speed ?? 0,
      dropped: d.process?.progress?.dropFrames ?? 0,
      duplicated: d.process?.progress?.dupFrames ?? 0,
    }));
}

/** The slice of GET /api/v1/loudness this reads (meters.Report). */
export interface LoudnessFeedLike {
  reports?: Array<{ destination: string; mix?: string; integrated: boolean; verdict: string }> | null;
}

/** The live mix only: a report with no mix is the live one, and the VOD mix is
 *  measured separately and says nothing about what is on air. */
export function loudnessRowsFromFeed(feed: LoudnessFeedLike): LoudnessRow[] {
  return (feed.reports ?? [])
    .filter((r) => r.mix !== "vod")
    .map((r) => ({ name: r.destination, integrated: r.integrated, verdict: r.verdict }));
}

/** Why the Meters page's per-track cards are not all reading a level, or null
 *  when they are. `headers` is the text of each track card's header ("Track 3",
 *  its layout, and the reading "-12.3 dBFS"; the console prints silence as
 *  "-∞", see format.ts db()). The synthetic commentary voices take turns, so a
 *  frame can catch some of them between bursts: such a frame shows a dead
 *  track under "Meters on every track" and is never to be photographed. The
 *  message names the tracks (1-based, as the page does), nothing else. */
export function silentTrackProblem(headers: string[], expected: number): string | null {
  if (headers.length !== expected) return `found ${headers.length} track cards, expected ${expected}`;
  const silent: number[] = [];
  headers.forEach((h, i) => {
    const track = Number(/\bTrack (\d+)/.exec(h)?.[1] ?? i + 1);
    const reading = /([-\u2212]\u221e|[-\u2212]?\d+(?:\.\d+)?)\s*dBFS/.exec(h)?.[1];
    if (reading === undefined || /\u221e/.test(reading)) silent.push(track);
  });
  if (silent.length === 0) return null;
  return `${silent.length === 1 ? "track" : "tracks"} ${silent.join(", ")} read${silent.length === 1 ? "s" : ""} minus infinity`;
}
