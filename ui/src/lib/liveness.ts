import { toneForState, type SignalTone } from "./signal";
import type { ProcessState, ProcessStatus } from "./types";

/* ONE PLACE THAT SAYS WHETHER A DESTINATION OR THE INGEST IS LIVE.
 *
 * "Running" is what the supervisor reports from the moment FFmpeg is spawned,
 * which is not the same as on air. Before this module the header, the on-air
 * bar, the across-the-room view, the Dashboard lane heading and delete confirm,
 * the routing overview and the go-live sheet each answered for themselves, and
 * one frame could read "1 live" in the header, "0 live" in the bar and
 * "Starting" on the card.
 *
 * THE POLICY, in one paragraph. A destination is ON AIR when it is enabled and
 * its process is running AND has carried media: uptimeSec > 0 (the supervisor
 * stamps it when this run's output first moves) or progress.outTimeMs > 0. A
 * running process with no media yet is STARTING. A stopped destination draining its delay tail is on air too (the engine
 * and MQTT say so). A stalled process (output not advancing) stays on air,
 * because the card still draws it live and carries the stall beside the lamp.
 * That DIFFERS from Prometheus polyemesis_destination_up and the webhook
 * "flowing" reading, which count a stall as down so an alert can fire. Only the go-live sheet, which asks "has this start
 * landed and is it moving?", additionally requires !stalled (destinationLanded).
 * The ingest is LIVE only while bytes arrive; a running process or listener
 * with nothing arriving is WAITING.
 *
 * No other file reads `process?.state === "running"` or `ingest?.state ===
 * "running"`: ingest-liveness-drift.test.ts fails the build if one does. */

type Proc = Pick<ProcessStatus, "state"> & {
  uptimeSec?: number;
  stalled?: boolean;
  progress?: { outTimeMs?: number } | null;
};

/** The supervisor's word and nothing more: a process in the running state. The
 *  one place that reads it raw, for the callers (a card's live readings, a
 *  rendition's bitrate) that mean exactly that and not "on air". */
export function processRunning(p?: Pick<ProcessStatus, "state"> | null): boolean {
  return p?.state === "running";
}

/** This run's output has carried media. */
function mediaMoved(p: Proc): boolean {
  return (p.uptimeSec ?? 0) > 0 || (p.progress?.outTimeMs ?? 0) > 0;
}

/** The state a destination's process is IN, for anything that draws it as on
 *  air or not: the server's state, except that "running" with no media yet is
 *  "starting".
 *
 *  The supervisor reports "running" straight after cmd.Start -- before FFmpeg
 *  has connected, let alone published -- and keeps the previous run's failure
 *  reason until a run moves media. So a Kick destination that Amazon IVS
 *  refused on every connect drew a red LIVE lamp at 0 kbps beside the failure
 *  box. The server is right about the process; reading it as "on air" was
 *  wrong, so it is corrected here and nothing sent back to the server changes. */
export function destinationShownState(process?: Proc | null): ProcessState | undefined {
  const state = process?.state;
  if (state === "running" && !mediaMoved(process!)) return "starting";
  return state ?? undefined;
}

/** Whether the destination is on air: enabled, running and carrying media.
 *
 *  A stopped destination playing its broadcast-delay tail out (enabled false,
 *  process still running, delay.phase "draining") is still on the air, as it
 *  is to the engine's alert projection and MQTT: viewers are still watching. */
export function destinationOnAir(d: {
  enabled?: boolean;
  process?: Proc | null;
  delay?: { phase?: string } | null;
}): boolean {
  if (d.delay?.phase === "draining" && processRunning(d.process)) return true;
  return !!d.enabled && destinationShownState(d.process) === "running";
}

/** The go-live sheet's question: has this start landed and is it moving. On air
 *  and not stalled. */
export function destinationLanded(process?: Proc | null): boolean {
  return destinationShownState(process) === "running" && !process?.stalled;
}

/** How long the broadcast has been on air: the longest-running enabled
 *  destination, since the first one up is when the audience could first see it. */
export function onAirSeconds(
  destinations: readonly {
    enabled?: boolean;
    process?: (Proc & { uptimeSec?: number }) | null;
    delay?: { phase?: string } | null;
  }[] | null | undefined,
): number {
  return (destinations ?? []).reduce(
    (max, d) => (destinationOnAir(d) ? Math.max(max, d.process?.uptimeSec ?? 0) : max),
    0,
  );
}

/** Whether the ingest process is in the running state. For an RTMP child or an
 *  SRT listener that is "able to receive", not "receiving": see ingestWaiting. */
export function ingestProcessUp(ingest?: Pick<ProcessStatus, "state"> | null): boolean {
  return processRunning(ingest);
}

/** The ingest is running with nothing arriving: a listener idling for its
 *  encoder. `arriving` is useProgrammeIngest's verdict (bytes flowing). */
export function ingestWaiting(ingest: Pick<ProcessStatus, "state"> | null | undefined, arriving: boolean): boolean {
  return !arriving && ingestProcessUp(ingest);
}

/** The ingest's tone: the standby carrying the show is warn, bytes arriving is
 *  live, a waiting listener is armed (loaded, not firing) and anything else is
 *  the process state's own tone. */
export function ingestSignalTone(
  ingest: Pick<ProcessStatus, "state"> | null | undefined,
  arriving: boolean,
  onBackup: boolean,
): SignalTone {
  if (onBackup) return "warn";
  if (arriving) return "live";
  if (ingestWaiting(ingest, arriving)) return "armed";
  return toneForState(ingest?.state);
}
