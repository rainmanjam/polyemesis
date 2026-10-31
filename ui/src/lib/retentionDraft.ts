import type { Settings } from "./types";

/* Which recording settings a given control is allowed to send.
 *
 * The whole defect was one shared object: the four retention number inputs
 * mutated `settings` in place, and every switch on the card then PUT that same
 * object. Flipping Stems therefore committed a half-typed segment length --
 * either a silent recorder restart on a value nobody confirmed, or a 400
 * blaming a control the operator never touched (db/settings.go floors
 * SegmentSeconds at 10, so "3" while typing "30" is a rejection).
 *
 * The fix is a CONTROL, not a warning: the two kinds of control no longer share
 * a body. A switch sends the server's own last answer plus its one field; the
 * Save policy button sends the server's answer plus the whole draft. There is
 * no expression left in which a switch can carry an unconfirmed number. */

/** The four numbers held in local draft state rather than in `settings`. */
export interface RetentionDraft {
  segmentSeconds: number;
  maxGb: number;
  maxAgeHours: number;
  minFreeGb: number;
}

/** Seeds the draft from what the server last returned. */
export function retentionDraft(s: Settings): RetentionDraft {
  return {
    segmentSeconds: s.recording.segmentSeconds,
    maxGb: s.recording.maxGb,
    maxAgeHours: s.recording.maxAgeHours,
    minFreeGb: s.recording.minFreeGb,
  };
}

/** Whether the operator has typed something not yet saved. Drives the note
 *  beside Save, so an edit abandoned mid-word is visible rather than merely
 *  ignored. */
export function retentionDirty(s: Settings, d: RetentionDraft | null): boolean {
  if (!d) return false;
  const base = retentionDraft(s);
  return (
    base.segmentSeconds !== d.segmentSeconds ||
    base.maxGb !== d.maxGb ||
    base.maxAgeHours !== d.maxAgeHours ||
    base.minFreeGb !== d.minFreeGb
  );
}

/** The body a SWITCH (or the stem format select) sends.
 *
 *  Built from the server's last answer, never from the draft: turning stems on
 *  is a decision about stems and must not also commit whatever is sitting in
 *  the segment-length box. */
export function switchPatchBody(
  server: Settings,
  patch: Partial<Settings["recording"]>,
): Settings {
  return { ...server, recording: { ...server.recording, ...patch } };
}

/** The body the Save policy button sends: the server's answer with the whole
 *  draft applied. The one control on the card that means "commit what I typed",
 *  and the only one that carries it. */
export function savePolicyBody(server: Settings, draft: RetentionDraft): Settings {
  return { ...server, recording: { ...server.recording, ...draft } };
}

/** The fields that cannot be saved as typed. A blank box is NaN here, never 0:
 *  the server reads 0 as "no limit", so a blank cap that became 0 removed the
 *  disk cap without the operator choosing that (A22). Segment length has the
 *  server's own bounds (10 to 86400 seconds); the three caps cannot be negative. */
export function retentionProblems(d: RetentionDraft): (keyof RetentionDraft)[] {
  const bad: (keyof RetentionDraft)[] = [];
  if (!(Number.isFinite(d.segmentSeconds) && d.segmentSeconds >= 10 && d.segmentSeconds <= 86400)) {
    bad.push("segmentSeconds");
  }
  for (const k of ["maxGb", "maxAgeHours", "minFreeGb"] as const) {
    if (!(Number.isFinite(d[k]) && d[k] >= 0)) bad.push(k);
  }
  return bad;
}
