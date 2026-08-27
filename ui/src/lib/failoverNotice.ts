import type { FailoverSettings } from "@/lib/types";

/** What, if anything, to tell the operator about their failover exposure.
 *
 *  THE ONE UNRECOVERABLE FAILURE THIS PRODUCT HAS. When the encoder
 *  disappears, the source-selector tier holds the platform connection up with a
 *  standby ingest, a looping file, or a slate. Without it the destination
 *  process restarts and takes the platform connection with it -- and a
 *  completed YouTube broadcast cannot return to live. Five seconds of dropped
 *  packets ends the show.
 *
 *  It is off by default, and that default is defensible: the tier costs a
 *  `-c copy` remux hop, and a file destination on a LAN box does not need it.
 *  This is not an argument for flipping it. It is an argument that an operator
 *  should not have to read the settings tree to learn the risk exists.
 *
 *  NOT IN THE ATTENTION PANEL, deliberately. attention() is explicitly "not
 *  every imperfection" -- it lists what is wrong NOW, and its own comment
 *  explains that padding it with states somebody chose is how a panel like that
 *  becomes something an operator scrolls past. A permanent row on every install
 *  that has not enabled failover is exactly that. This is a separate, quieter
 *  line with a different job.
 *
 *  AND IT CLEARS ITSELF rather than being dismissible. A banner with an X is
 *  rung zero: it depends on the operator remembering, later, a thing they
 *  dismissed while busy. This one disappears the moment the setting it names is
 *  changed, and comes back if it is changed away -- so what is on screen is
 *  always the current exposure rather than a record of who clicked what.
 */
export type FailoverNotice =
  | { kind: "none" }
  /** Failover is off and there is a live broadcast to lose. */
  | { kind: "unprotected" }
  /** Failover is on, but the fallback is a slate rather than the operator's own
   *  video, because no playlist file has been configured. */
  | { kind: "slate-only" };

/**
 * @param enabledDestinations How many destinations the operator has switched
 *   on. The trigger is "there is something to lose", not "the stream is live":
 *   a warning that only appears once the broadcast is running arrives after the
 *   moment it was cheap to act on.
 */
export function failoverNotice(
  enabledDestinations: number,
  failover: FailoverSettings | null | undefined,
): FailoverNotice {
  // Nothing configured to go out means nothing to protect, and an install
  // mid-setup does not need to be told about a risk it does not yet carry.
  if (enabledDestinations < 1) return { kind: "none" };

  // Undefined settings are NOT treated as disabled. The settings read can fail
  // or simply not have landed, and telling an operator their broadcast is
  // unprotected because a request was slow is the same defect the dashboard's
  // source count carries a comment about: an unknown must not render as a
  // finding.
  if (!failover) return { kind: "none" };

  if (!failover.enabled) return { kind: "unprotected" };

  // Failover on, but the fallback is a black slate. The machinery to loop an
  // uploaded file is fully built and ranks correctly -- below both ingests and
  // above the slate -- so this is a step someone got most of the way through.
  const items = failover.playlist?.enabled ? (failover.playlist.items?.length ?? 0) : 0;
  if (items === 0) return { kind: "slate-only" };

  return { kind: "none" };
}
