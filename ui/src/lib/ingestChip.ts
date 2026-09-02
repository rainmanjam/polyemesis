/** The header's ingest reading, extracted so a test can bind to the real
 *  expression rather than a copy of it.
 *
 *  It lived inline in AppLayout's JSX, and the first guard written for #663
 *  reimplemented it in the test file -- which asserts that a transcription
 *  behaves correctly and says nothing about the component. A guard that
 *  describes the code instead of calling it is the shape this audit keeps
 *  finding, so the expression moved here and both call it.
 */
import type { ProcessState, ProcessStatus } from "./types";

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
): { kind: "bitrate"; kbps: number } | { kind: "state"; state: ProcessState | null | undefined } {
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
