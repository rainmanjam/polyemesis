/** The five setup steps for an install that has a source but no destination.
 *
 *  EVERY TICK IS DERIVED, NOT STORED. There is no "setup finished" flag, because
 *  one would drift the first time a source was deleted or an encoder dropped
 *  off: the checklist would still say "done" over an empty page. Each step
 *  reads what the server reports now. A step that is undone again simply stops
 *  ticking, which is the honest behaviour for a setup list.
 *
 *  Null while the source count has not arrived. Drawing a checklist on a guess
 *  would tick step 1 on an install the server has not yet said has a source. */

export type ChecklistStepId = 1 | 2 | 3 | 4 | 5;

export type ChecklistStepState = "done" | "next" | "waiting" | "todo" | "locked";

/** Why Go live is locked, named exactly, so the operator reads the one thing
 *  they are missing rather than "complete setup". */
export type GoLiveReason = "needSourceAndDestination" | "needDestination" | "needSource" | null;

export interface ChecklistInput {
  /** From setupStatus. Null until it arrives. */
  sourceCount: number | null;
  /** Whether bytes are arriving on the ingest (useIngestLive), not whether a
   *  process is running. Only meaningful with a source. */
  signal: boolean;
  destinations: number;
}

export interface Checklist {
  steps: { id: ChecklistStepId; state: ChecklistStepState }[];
  /** Ticked steps among 1 to 4. Go live is an action, not a setup fact, so it
   *  never counts as done. */
  done: number;
  reason: GoLiveReason;
}

export function setupChecklist(input: ChecklistInput): Checklist | null {
  if (input.sourceCount === null) return null;

  const hasSource = input.sourceCount > 0;
  const signal = hasSource && input.signal;
  const hasDestination = input.destinations > 0;

  const step1: ChecklistStepState = hasSource ? "done" : "next";
  // Step 2 has no reading of its own. It ticks with the signal it leads to.
  const step2: ChecklistStepState = signal ? "done" : hasSource ? "next" : "todo";
  const step3: ChecklistStepState = signal ? "done" : hasSource ? "waiting" : "todo";
  const step4: ChecklistStepState = hasDestination ? "done" : "todo";

  const ready = hasSource && hasDestination;
  const step5: ChecklistStepState = ready ? "next" : "locked";

  let reason: GoLiveReason = null;
  if (!hasSource && !hasDestination) reason = "needSourceAndDestination";
  else if (!hasDestination) reason = "needDestination";
  else if (!hasSource) reason = "needSource";

  const steps = [
    { id: 1 as const, state: step1 },
    { id: 2 as const, state: step2 },
    { id: 3 as const, state: step3 },
    { id: 4 as const, state: step4 },
    { id: 5 as const, state: step5 },
  ];
  const done = steps.slice(0, 4).filter((s) => s.state === "done").length;
  return { steps, done, reason };
}

/** Whether the Settings readiness strip may say "Ready to stream". Only when
 *  every step before Go live is done (source, encoder, signal, destination),
 *  the same steps the checklist counts in "{done} of 5". It used to read the
 *  Go live reason instead, which is null as soon as a source and a destination
 *  exist, so the strip said "Ready" in green directly above a checklist that
 *  said "2 of 5 done" and was still waiting for a signal. One derivation, so
 *  the two can no longer disagree. */
export function readyToStream(checklist: Checklist): boolean {
  return checklist.steps.slice(0, 4).every((s) => s.state === "done");
}
