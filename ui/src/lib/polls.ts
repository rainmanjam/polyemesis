/** Chat polls (#14): the shapes GET /polls returns and the pure rules the
 *  dashboard panel draws with. Counts only -- the server's Result has no field
 *  that could carry a voter, and neither does anything here. */

export type PollState = "open" | "closing" | "ended" | "interrupted";

export interface PollOption {
  label: string;
  votes: number;
  /** Votes per platform. Counts only. */
  byPlatform: Record<string, number>;
}

export interface PollAnnouncement {
  platform: string;
  ok: boolean;
  skipped?: boolean;
  detail?: string;
}

export interface PollNative {
  platform: string;
  status: "mirrored" | "stopped" | "skipped" | "failed" | string;
  reason?: string;
  votes?: number[];
}

/** internal/polls.Result */
export interface Poll {
  id: number;
  question: string;
  options: PollOption[];
  total: number;
  byPlatform: Record<string, number>;
  state: PollState;
  durationSeconds: number;
  startedAt: string;
  endsAt: string;
  endedAt?: string;
  endedEarly?: boolean;
  announced?: PollAnnouncement[];
  native?: PollNative[];
}

export interface PollsResponse {
  current: Poll | null;
  history: Poll[];
}

/** internal/polls.Spec */
export interface PollSpec {
  question: string;
  options: string[];
  durationSeconds: number;
  mirrorTwitch?: boolean;
  mirrorYouTube?: boolean;
}

/** The server's limits (internal/polls), checked here too so the form can say
 *  what is wrong before it is sent. The server still decides. */
export const POLL_LIMITS = {
  minOptions: 2,
  maxOptions: 6,
  maxQuestion: 100,
  maxOption: 30,
  minDuration: 15,
  maxDuration: 3600,
} as const;

/** What is wrong with a spec, as a translation key, or null when it is fine. */
export type PollProblem =
  | "polls.errQuestion"
  | "polls.errQuestionLong"
  | "polls.errOptionEmpty"
  | "polls.errOptionLong"
  | "polls.errOptionRepeat"
  | "polls.errOptionCount"
  | "polls.errDuration";

const chars = (s: string) => Array.from(s).length;

export function pollProblem(s: PollSpec): PollProblem | null {
  const q = s.question.trim();
  if (!q) return "polls.errQuestion";
  if (chars(q) > POLL_LIMITS.maxQuestion) return "polls.errQuestionLong";
  if (s.options.length < POLL_LIMITS.minOptions || s.options.length > POLL_LIMITS.maxOptions) return "polls.errOptionCount";
  const seen = new Set<string>();
  for (const raw of s.options) {
    const o = raw.trim();
    if (!o) return "polls.errOptionEmpty";
    if (chars(o) > POLL_LIMITS.maxOption) return "polls.errOptionLong";
    const k = o.toLowerCase();
    if (seen.has(k)) return "polls.errOptionRepeat";
    seen.add(k);
  }
  if (
    !Number.isInteger(s.durationSeconds) ||
    s.durationSeconds < POLL_LIMITS.minDuration ||
    s.durationSeconds > POLL_LIMITS.maxDuration
  ) {
    return "polls.errDuration";
  }
  return null;
}

/** Each option's share of the votes, whole percent. */
export function sharePercents(p: Pick<Poll, "options" | "total">): number[] {
  return p.options.map((o) => (p.total > 0 ? Math.round((o.votes * 100) / p.total) : 0));
}

/** Seconds left, never negative. */
export function secondsLeft(p: Pick<Poll, "endsAt">, now: number): number {
  const ends = Date.parse(p.endsAt);
  return Number.isNaN(ends) ? 0 : Math.max(0, Math.ceil((ends - now) / 1000));
}

/** "1:05" for 65 seconds. */
export function clock(seconds: number): string {
  const m = Math.floor(seconds / 60);
  const s = seconds % 60;
  return `${m}:${String(s).padStart(2, "0")}`;
}

/** The platforms that voted, most votes first, ties by name. */
export function platformsByVotes(byPlatform: Record<string, number>): [string, number][] {
  return Object.entries(byPlatform)
    .filter(([, n]) => n > 0)
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
}

/** Whether the panel should keep polling: a poll is running. */
export function isLive(p: Poll | null | undefined): boolean {
  return !!p && (p.state === "open" || p.state === "closing");
}

/** The export link: one poll, or the whole history. */
export function exportHref(id?: number): string {
  return id === undefined ? "/api/v1/polls/export" : `/api/v1/polls/export?id=${id}`;
}
