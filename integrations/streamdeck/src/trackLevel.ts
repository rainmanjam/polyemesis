// Track gain arithmetic for the gain dial. The server stores a LINEAR gain
// (1.0 is unity) and caps it at routing.MaxGain = 2.0, which is +6.02 dB
// (internal/routing/profile.go). It refuses zero, a negative, NaN and Inf; a
// silent track is a mute, not a gain of zero.
//
// The dial steps in DECIBELS, 0.5 dB a tick, not in linear steps. Loudness is
// heard on a log scale: a linear step of 0.05 is 0.4 dB at unity but 6 dB near
// 0.1, so one tick would be inaudible at the top of the range and a jump at the
// bottom. 0.5 dB is about the smallest change a listener hears, so every tick
// does something and none of them startles a live audience. Twelve ticks move
// the level 6 dB, a quarter turn of the dial.

/** routing.MaxGain on the server. A larger gain is a 400, so none is ever sent. */
export const MAX_GAIN = 2.0;
/** One dial tick. */
export const DB_STEP = 0.5;
/**
 * The top of the dial: the highest 0.5 dB step at or under MaxGain (+6.02 dB), so a
 * clamped value is always one the server accepts.
 */
export const DB_MAX = 6;
/**
 * The bottom of the dial. The server takes any gain above zero, but below -40 dB a
 * track is inaudible under a programme mix, and the way to silence it is mute, which
 * keeps the level to come back to.
 */
export const DB_MIN = -40;

export function gainToDb(gain: number): number {
  return 20 * Math.log10(gain);
}

export function dbToGain(db: number): number {
  return 10 ** (db / 20);
}

function clampDb(db: number): number {
  return Math.min(DB_MAX, Math.max(DB_MIN, db));
}

/**
 * The level a turn of `ticks` lands on, in dB, from the level now. The result is on
 * the 0.5 dB grid and inside [DB_MIN, DB_MAX]. A level set elsewhere (the UI takes a
 * percentage) is usually off the grid; the first tick moves it to the next grid point
 * in the direction of the turn, so a turn never goes the wrong way.
 */
export function stepDb(fromDb: number, ticks: number): number {
  if (!Number.isFinite(ticks) || ticks === 0) return fromDb;
  const steps = fromDb / DB_STEP;
  // A value within a hair of a grid point is on it: 20*log10 of a stored float is rarely exact.
  const onGrid = Math.abs(steps - Math.round(steps)) < 1e-6;
  const base = onGrid ? Math.round(steps) : ticks > 0 ? Math.floor(steps) : Math.ceil(steps);
  // The floor is the dial's, or the level itself when it is already below it: a track
  // stored at -60 dB (set in the UI, or over the API) moves 0.5 dB a tick from -60, not
  // 20 dB in one tick to -40. Minus infinity (a stored gain of 0) has nowhere to step
  // from, so its first tick up lands on the dial's floor.
  const floor = Number.isFinite(fromDb) ? Math.min(DB_MIN, fromDb) : DB_MIN;
  const next = Math.min(DB_MAX, Math.max(floor, (base + ticks) * DB_STEP));
  // A level already past an end (the server allows +6.02 dB, and any level above zero) is
  // left where it is by a turn towards that end. The clamp must never move it backwards.
  if ((ticks > 0 && next < fromDb) || (ticks < 0 && next > fromDb)) return fromDb;
  return next;
}

/** The smallest gain sent: the server refuses zero, and toFixed(6) rounds a very low level to it. */
const MIN_SENT_GAIN = 0.000001;

/**
 * The linear gain to send for a dial level. Never above MAX_GAIN, never zero. Not raised to
 * DB_MIN: a level below the dial's floor is one stepDb reached from a stored level already
 * there, and raising it would send the 20 dB jump the step just avoided.
 */
export function gainForDb(db: number): number {
  const gain = dbToGain(Math.min(DB_MAX, db));
  return Math.min(MAX_GAIN, Math.max(MIN_SENT_GAIN, Number(gain.toFixed(6))));
}

/** The dial's bar, 0..100, for a level in dB: the dial's own range, linear in dB. */
export function barFor(db: number): number {
  return Math.round(((clampDb(db) - DB_MIN) / (DB_MAX - DB_MIN)) * 100);
}

/** A level as the dial prints it: one decimal, with its sign, e.g. "+1.5 dB", "0.0 dB", "-12.0 dB", "-∞ dB". */
export function formatDb(db: number): string {
  // The server stores a gain of 0 for a track kept out of the mix: 20*log10(0) is -Infinity.
  if (db === Number.NEGATIVE_INFINITY) return "-∞ dB";
  const rounded = Math.round(db * 10) / 10;
  if (rounded === 0) return "0.0 dB";
  return `${rounded > 0 ? "+" : ""}${rounded.toFixed(1)} dB`;
}
