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

/** Kinds (never values) of the secrets whose value appears in the text. Values under 8 characters are ignored. */
export function findLegibleSecrets(visibleText: string, secrets: SecretEntry[]): string[] {
  const kinds: string[] = [];
  for (const s of secrets) {
    if (s.value.length < MIN_SECRET_LENGTH || !visibleText.includes(s.value)) continue;
    if (!kinds.includes(s.kind)) kinds.push(s.kind);
  }
  return kinds;
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
 * visible text SAYS failed. Two classes of word, because the console prints
 * them differently:
 *
 *  - STATE words (failed, fail, crashed, unreachable) are a badge or a verdict
 *    pill: present at all means a failure on screen.
 *  - COUNT words (errors, stopped) are also plain labels ("Errors" filter,
 *    "Stopped" in a legend), so only a non-zero count beside them is a failure.
 *
 * Neither is matched inside prose. A settings page explains "failed attempts"
 * and "Failing step" in sentences; a line is prose when it ends in a full stop
 * or runs past MAX_LABEL_WORDS words. Failover is a feature name, not a state:
 * word boundaries keep it out ("Failover" does not contain the word "fail").
 * Zero counts ("0 failed", "Errors: 0", a tile "Failed" over "0") are removed
 * first; a zero on the next line only counts when it is alone on that line, so
 * the previous tile's zero cannot hide the next tile's failure. */
const MAX_LABEL_WORDS = 10;
const STATE_WORD_LIST = "failed|fail|crashed|unreachable";
const COUNT_WORD_LIST = "errors?|stopped";
const ALL_WORDS = `${STATE_WORD_LIST}|${COUNT_WORD_LIST}`;

const ZERO_BEFORE = new RegExp(String.raw`\b0[ \t]+(?:${ALL_WORDS})\b`, "gi");
const ZERO_AFTER = new RegExp(
  String.raw`\b(?:${ALL_WORDS})\b[ \t]*[:=]?[ \t]*\n?[ \t]*0(?![\d.])(?=[ \t]*(?:$|\n|[·|,;)]))`,
  "gim",
);
const STATE_RE = new RegExp(String.raw`\b(${STATE_WORD_LIST})\b`, "gi");
const COUNT_BEFORE = new RegExp(String.raw`\b(\d+(?:\.\d+)?)[ \t]+(${COUNT_WORD_LIST})\b`, "gi");
const COUNT_AFTER = new RegExp(String.raw`\b(${COUNT_WORD_LIST})\b[ \t]*[:=]?[ \t]*\n?[ \t]*(\d+(?:\.\d+)?)`, "gi");

function normalise(word: string): string {
  const w = word.toLowerCase();
  return w === "error" ? "errors" : w;
}

/** True when the match sits on a short line that is not a sentence. */
function isLabelLine(text: string, index: number): boolean {
  const start = text.lastIndexOf("\n", index) + 1;
  const nl = text.indexOf("\n", index);
  const line = text.slice(start, nl === -1 ? text.length : nl);
  if (/[.!?]\s*$/.test(line)) return false;
  const words = line.split(/\s+/).filter((w) => w !== "" && w !== "·" && w !== "|");
  return words.length <= MAX_LABEL_WORDS;
}

/** Failed-state words on screen, after removing zero counts ("0 failed", "0 stopped", "errors: 0").
 *  `allow` names words the shot's manifest entry declares as its subject. */
export function failedStateWords(visibleText: string, allow: string[] = []): string[] {
  const text = visibleText.replace(ZERO_BEFORE, "").replace(ZERO_AFTER, "");
  const hits: { index: number; word: string }[] = [];
  for (const m of text.matchAll(STATE_RE)) hits.push({ index: m.index, word: normalise(m[1]) });
  for (const m of text.matchAll(COUNT_BEFORE)) {
    if (Number(m[1]) > 0) hits.push({ index: m.index, word: normalise(m[2]) });
  }
  for (const m of text.matchAll(COUNT_AFTER)) {
    if (Number(m[2]) > 0) hits.push({ index: m.index, word: normalise(m[1]) });
  }
  const allowed = new Set(allow.map(normalise));
  const words: string[] = [];
  for (const h of hits.sort((a, b) => a.index - b.index)) {
    if (allowed.has(h.word) || words.includes(h.word) || !isLabelLine(text, h.index)) continue;
    words.push(h.word);
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
 *  it (reports[].integrated, .verdict, .destination). `name` is optional so a
 *  caller that has only the two fields still works; it only improves the message. */
export interface LoudnessRow {
  integrated: boolean;
  verdict: string;
  name?: string;
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
  if (loudness.length < rows.length) {
    return no(`${loudness.length} loudness rows for ${rows.length} destinations`);
  }
  for (const [i, l] of loudness.entries()) {
    const who = l.name ?? `loudness row ${i + 1}`;
    if (!l.integrated) return no(`${who} loudness is not yet integrated`);
    if (l.verdict !== "pass") return no(`${who} loudness verdict is ${l.verdict}, not pass`);
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
