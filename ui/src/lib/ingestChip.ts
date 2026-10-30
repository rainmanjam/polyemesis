/** The header's ingest reading, extracted so a test can bind to the real
 *  expression rather than a copy of it.
 *
 *  It lived inline in AppLayout's JSX, and the first guard written for #663
 *  reimplemented it in the test file -- which asserts that a transcription
 *  behaves correctly and says nothing about the component. A guard that
 *  describes the code instead of calling it is the shape this audit keeps
 *  finding, so the expression moved here and both call it.
 */
import type { FailoverStatus, ProcessState, ProcessStatus } from "./types";

/** Whether the STANDBY ingest is what is carrying the broadcast right now.
 *
 *  The ingest chip and the Ingest card describe the primary (the process state,
 *  the bitrate feed), so while failover has the backup on air they read
 *  "Offline" over a show that is going out -- true of the primary, and a
 *  contradiction of the ON AIR banner beside it. `backupLive !== false` rather
 *  than `=== true`: an older server that omits the field still gets the honest
 *  label, since `active` alone says the selector chose the backup. */
export function backupOnAir(failover: FailoverStatus | null | undefined): boolean {
  return failover?.active === "backup" && failover.backupLive !== false;
}

/** ingestChipReading answers what the chrome should print for the ingest.
 *
 *  Returns either a bitrate to format, or the process state to label. The
 *  caller does the formatting, because kbps() and stateLabel() are the UI's
 *  and this is the decision.
 *
 *  ABSENT IS NOT ZERO, which is the whole reason this is a function. A child
 *  that has started but not yet reported progress has no bitrate; printing
 *  `?? 0` for it put "0 kbps" beside a live dot, telling an operator
 *  mid-broadcast that their stream was running at nothing. A child that
 *  genuinely reports zero is a different thing entirely -- a real and alarming
 *  reading -- and must still show, which is why this tests for null rather
 *  than for falsiness. #663.
 */
export function ingestChipReading(
  ingest: ProcessStatus | null | undefined,
  ingestLive: boolean,
  onBackup = false,
): { kind: "bitrate"; kbps: number } | { kind: "backup" } | { kind: "state"; state: ProcessState | null | undefined } {
  // Before the bitrate and ingestLive branches: the process and the bitrate feed
  // describe the primary, which can recover while a manual-return failover keeps
  // the backup selected. "Running" for it would hide which ingest is on air.
  if (onBackup) return { kind: "backup" };
  if (ingest?.state === "running" && ingest.progress?.bitrateKbps != null) {
    return { kind: "bitrate", kbps: ingest.progress.bitrateKbps };
  }
  // An SRT source is live with no child at all, so it has no bitrate to show
  // and "Running" is the honest answer rather than a zero at a stream that is
  // fine. A running child without progress yet lands here for the same reason.
  if (ingestLive || ingest?.state === "running") {
    return { kind: "state", state: "running" };
  }
  return { kind: "state", state: ingest?.state };
}

/** The reason to show beside a non-pass ingest (B1), or null when there is
 *  nothing to explain.
 *
 *  Null whenever the chip is not a failure: bytes arriving, the backup on air,
 *  or a running process. A lastError left over from an earlier failure must not
 *  be shown beside a stream that has since recovered, which is why this checks
 *  the same three things the chip does rather than only the error field. An
 *  empty string is no reason, so it is null too. */
export function ingestProblem(
  ingest: ProcessStatus | null | undefined,
  ingestLive: boolean,
  onBackup: boolean,
): string | null {
  if (ingestLive || onBackup) return null;
  if (!ingest || ingest.state === "running") return null;
  return ingest.lastError ? ingest.lastError : null;
}
