/* What one track's meter column is entitled to say.
 *
 * The column had two strings for four situations, so two of them were lies —
 * on the page where routing is decided, which is the page where "this track
 * carries nothing" is the reading that gets a track left out of the mix.
 *
 *   - "no signal" WAS PRINTED FOR A TRACK NOBODY HAS MEASURED. `probed` is the
 *     INGEST LAYOUT — it says FFprobe has described the streams — and it has
 *     nothing to do with whether the meter feed is running. With meters off, or
 *     before the first levels frame arrives, every track on a perfectly healthy
 *     ingest read "no signal". Note that a genuinely silent track still
 *     produces level arrays full of zeros, so "no signal" was never the right
 *     string for silence either: the meter itself shows that.
 *
 *   - AFTER A SOCKET DROP THE METERS FROZE AT A PLAUSIBLE LEVEL. `levels` was
 *     never cleared in `ws.onclose` (LiveDataProvider), so the last frame
 *     before the disconnection stayed on screen, bouncing at nothing, and read
 *     as live audio. That is the worse half: a stale reading that looks healthy
 *     is worse than no reading, because there is nothing to notice.
 *
 * Liveness is therefore consulted FIRST, before the levels are looked at at
 * all. Paired with clearing `levels` on close it is belt and braces: the frozen
 * frame is gone, and even if something puts one back this refuses to render it
 * as current. */

/** The four things the meter column may be. */
export type TrackSignal =
  /** Real, current levels — draw the meter. */
  | "meter"
  /** The meter feed is not connected; anything on screen would be stale. */
  | "offline"
  /** The ingest has not been described yet, so there is not even a track
   *  layout to measure against. */
  | "waiting"
  /** Described, feed connected, and no numbers have arrived: nobody has
   *  measured this. NOT the same claim as silence. */
  | "notMeasured";

export function trackSignal(a: {
  /** Whether this track has level samples to draw. */
  hasLevels: boolean;
  /** Whether the ingest has been probed — the track LAYOUT, not the meters. */
  probed: boolean;
  /** Whether the feed carrying levels is connected right now. */
  feedLive: boolean;
}): TrackSignal {
  if (!a.feedLive) return "offline";
  if (a.hasLevels) return "meter";
  if (!a.probed) return "waiting";
  return "notMeasured";
}

/** The words, kept beside the decision so the two cannot drift.
 *
 *  Literal English to match every other string in TrackRows and its siblings
 *  under components/signature, none of which is keyed. */
export const TRACK_SIGNAL_TEXT: Record<Exclude<TrackSignal, "meter">, string> = {
  offline: "meter feed offline",
  waiting: "waiting for stream",
  notMeasured: "not measured",
};
