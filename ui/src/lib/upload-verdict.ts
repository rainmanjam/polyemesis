import type { MediaFile, MediaVerdict } from "./types";

/* ===========================================================================
   What an upload's verdict means to the operator, in one place.

   A stored upload is in one of FOUR states and only one of them is "fine". The
   other three used to be two, because the server carried a single boolean, and
   the state that had nowhere to live is the one that matters most here:
   INSPECTED AND REFUSED. Recording that as "not checked" — the only thing the
   old two-state record could say — would have made every consumer state
   something the server knows is false, and would have handed the operator the
   one remedy that cannot work, since re-sending the same bytes earns the same
   refusal.

   Extracted from the components rather than written inline in both because the
   two consumers must not disagree: the Library says why a file is unusable and
   the playlist editor decides whether to offer it, and a file shown as usable
   in one place and refused in the other is worse than either answer alone.
   ========================================================================= */

/** Whether an upload may be offered as a new playlist item.
 *
 *  The server is the gate — api.playlistUploadProblems refuses a save naming an
 *  upload that is anything but verified or unrecorded — and this only avoids
 *  offering a choice that will be refused on save. It asks the same question the
 *  server asks, in the same terms, which is the point of both of them keying on
 *  `outcome`.
 *
 *  `unrecorded` is allowed, and that is not an oversight. Every upload an
 *  install stored before verdicts existed has no record at all; refusing those
 *  would strand media an operator has had for a year over a file that was never
 *  written. They are covered instead by the normalise worker, which re-runs the
 *  format allowlist at the moment of use. */
export function isSelectableUpload(u: Pick<MediaFile, "outcome">): boolean {
  return u.outcome === "verified" || u.outcome === "unrecorded";
}

/** Whether to offer "check again" on this upload's row.
 *
 *  #202. The marker used to be a dead end: the Library could say "Not checked"
 *  and the only remedy on offer was to upload the bytes a second time, which is
 *  not a remedy at all for a file the operator no longer has a local copy of.
 *  The server can now re-read a file already on disk.
 *
 *  OFFERED FOR THE TWO STATES WHERE NOBODY HAS READ THE FILE, and deliberately
 *  NOT for `refused`. That is the same line uploadNotice draws below and it is
 *  drawn for the same reason: a refusal is a statement about the FILE and
 *  reading it again reaches the same conclusion, so a "check again" button
 *  beside it is the "upload it again" advice all over again in a new spelling —
 *  an action that looks like a fix, does nothing, and teaches the operator that
 *  the state is noise.
 *
 *  The SERVER accepts a re-check for any stored upload, refused included, and
 *  that asymmetry is on purpose rather than an oversight: the one case where
 *  re-reading a refused file can legitimately change the answer is a server
 *  whose FFmpeg has been upgraded or whose format allowlist has grown, and that
 *  is an operator action with a restart in it, not something to invite from a
 *  row. See #202 for what a UI affordance for it would have to say.
 *
 *  `verified` gets nothing either. Re-reading a file that passed can only
 *  produce the same answer or a worse one, and offering it invites an operator
 *  to go looking for reassurance the row already gives them. */
export function canReverify(u: Pick<MediaFile, "outcome">): boolean {
  return u.outcome === "unverified" || u.outcome === "unrecorded";
}

/** The warning line for one upload's row, or null when there is nothing to say.
 *
 *  `tone` picks the colour and `label` the word. They differ between the two
 *  unusable states ON PURPOSE: "Not checked" is a statement about this server
 *  and is recoverable, "Refused" is a statement about the file and is not, and
 *  an operator who cannot tell them apart cannot tell which of their two very
 *  different remedies to reach for. */
export type UploadNotice = {
  outcome: Exclude<MediaVerdict, "verified">;
  tone: "warn" | "refused";
  label: string;
  detail: string;
};

export function uploadNotice(
  u: Pick<MediaFile, "outcome" | "unverifiedReason">,
): UploadNotice | null {
  switch (u.outcome) {
    case "verified":
      return null;
    case "refused":
      return {
        outcome: "refused",
        tone: "refused",
        label: "Refused",
        // No "upload it again". That sentence belongs to the state below and is
        // actively wrong here: the file was read, and reading it a second time
        // reaches the same conclusion.
        detail: `${u.unverifiedReason}. This file was inspected and is not media this server can use; sending it again will not change that. It cannot be used as a playlist item.`,
      };
    case "unverified":
      return {
        outcome: "unverified",
        tone: "warn",
        label: "Not checked",
        detail: `${u.unverifiedReason}. Upload it again to have it checked; it cannot be used as a playlist item until it has been.`,
      };
    default:
      // `unrecorded`, and the default arm is deliberate: a state a newer server
      // names and this build has not learned is not silently treated as fine.
      return {
        outcome: "unrecorded",
        tone: "warn",
        label: "Not checked",
        detail:
          "This file was stored before uploads were checked, so nothing here describes what is in it.",
      };
  }
}
