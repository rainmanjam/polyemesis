/* A matrix cell stores linear gain (0 to 2). An operator reads decibels, so the
 * chip says what the number means: 0 is a removed connection, 1 is unity, and
 * 0.707 is the -3 dB point every mixer knows. */

export type GainState = "off" | "unity" | "custom";

const UNITY_TOLERANCE = 0.005;

export function gainState(gain: number): GainState {
  if (gain <= 0) return "off";
  if (Math.abs(gain - 1) < UNITY_TOLERANCE) return "unity";
  return "custom";
}

/** The dB reading of a linear gain, to one decimal, with an explicit sign above
 *  unity. Zero is "off" rather than minus infinity. */
export function gainDbLabel(gain: number): string {
  if (gain <= 0) return "off";
  const db = 20 * Math.log10(gain);
  const rounded = Math.round(db * 10) / 10;
  const text = rounded.toFixed(1);
  const signed = rounded > 0 ? `+${text}` : text;
  return `${signed} dB`;
}
