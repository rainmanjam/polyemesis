/* The meter's dB range. The fixed scale is -60..0 dBFS. Auto zoom narrows the
 * floor to what is being measured, but never above -24, so the -18 and -14
 * references stay on the scale. The floor moves in 6 dB steps so the scale does
 * not jitter as the level moves. */

export interface DbRange {
  minDb: number;
  maxDb: number;
}

export const FIXED_RANGE: DbRange = { minDb: -60, maxDb: 0 };

/** The -14 LUFS streaming reference, drawn as a mark on the bar. */
export const STREAMING_REFERENCE_DB = -14;

/** Any reading at or below this is "nothing measured", the analyser's floor. */
const FLOOR_DB = -60;
/** The highest floor Auto zoom will ever choose. Keeps -18 and -14 on the scale. */
const AUTO_MIN_CEILING_DB = -24;
const AUTO_STEP_DB = 6;
const AUTO_HEADROOM_DB = 12;

/** The range Auto zoom draws for a given loudest peak, in dBFS. A silent or
 *  unmeasured input (-100 or below) gets the full fixed range. */
export function autoZoomRange(peakDb: number): DbRange {
  if (peakDb <= -100) return FIXED_RANGE;
  const wanted = Math.floor((peakDb - AUTO_HEADROOM_DB) / AUTO_STEP_DB) * AUTO_STEP_DB;
  const minDb = Math.max(FIXED_RANGE.minDb, Math.min(AUTO_MIN_CEILING_DB, wanted));
  return { minDb, maxDb: FIXED_RANGE.maxDb };
}

/** Where a level sits on a range, from 0 (floor) to 1 (top), clamped. */
export function dbFraction(db: number, range: DbRange): number {
  if (db <= range.minDb) return 0;
  if (db >= range.maxDb) return 1;
  return (db - range.minDb) / (range.maxDb - range.minDb);
}

/** A held peak as text: one decimal and its unit, or a dash where nothing has
 *  been measured. */
export function formatPeakDb(db: number): string {
  if (!Number.isFinite(db) || db <= FLOOR_DB) return "—";
  return `${db.toFixed(1)} dB`;
}
