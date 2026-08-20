import { Info } from "lucide-react";

import type { Status } from "@/lib/types";

/** Why every destination card is grey, when the engine decided that on purpose.
 *
 *  reconcileOutputs holds every destination while the ingest layout is
 *  unmeasured -- a routing graph compiled against the placeholder would map
 *  tracks that may not exist. A held destination simply has no Process, which
 *  is byte-for-byte what a CRASHED destination looks like and what one that was
 *  never planned looks like. So a destination added before the encoder connects
 *  sits at "Offline" with no reason, indistinguishable from an FFmpeg that
 *  died, and the operator goes hunting for a fault that is not there.
 *
 *  The server already computed the sentence for this screen -- HoldStatus in
 *  internal/engine/status.go, pinned by dest_hold_reason_test.go -- and nothing
 *  in ui/src rendered it. This is that sentence, once, above the grid.
 *
 *  A WARNING rather than a control, and it could not be anything else: the hold
 *  is the engine refusing to build a graph it cannot build, which is correct.
 *  What was broken was that the refusal was silent.
 *
 *  `reason` is rendered verbatim and never matched on. `code` is the stable
 *  identifier for machinery; the prose is free to be reworded, and a UI that
 *  branched on it would break on the rewording. Neutral tone deliberately:
 *  nothing is wrong, and painting it amber would train an operator to read a
 *  normal pre-stream minute as a fault.
 */
export function DestinationHoldNote({ hold }: { hold: Status["destinationHold"] }) {
  const reason = hold?.reason?.trim();
  if (!reason) return null;
  return (
    <div
      data-testid="destination-hold"
      className="mb-2 flex items-start gap-1.5 rounded border border-border bg-muted/40 px-2 py-1.5 text-[11px] leading-relaxed text-muted-foreground"
    >
      <Info className="mt-0.5 h-3 w-3 shrink-0" />
      <span>{reason}</span>
    </div>
  );
}
