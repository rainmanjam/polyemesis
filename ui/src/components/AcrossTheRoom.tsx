import { useEffect } from "react";
import { X } from "lucide-react";

import { Button } from "@/components/ui/button";
import { StatusDot } from "@/components/signature/StatusDot";
import { attention, onAir } from "@/lib/attention";
import { toneForState } from "@/lib/signal";
import { kbps } from "@/lib/format";
import type { Status } from "@/lib/types";
import { useStateLabel } from "@/lib/i18n";

/* ===========================================================================
   ACROSS-THE-ROOM MODE.

   The machine running polyemesis is usually not the machine the operator is
   sitting at. It is beside the desk, two or three metres away, and for most of
   a broadcast the only thing anybody needs from it is whether the show is still
   going out. At that distance the console's 10-13px type is a texture, not
   text, and the status dots are pixels.

   So this is the same information at a size that survives the distance: the
   state as a WORD in display type, the count of destinations live, and a row
   per destination. Nothing here is a control. A screen you read from across the
   room is a screen you cannot aim at, and a Stop button on it is a Stop button
   somebody leans on.

   IT COMPOSES WITH THE SILHOUETTES, and it has to. Scale weakens colour rather
   than strengthening it — a large field of amber and a large field of green
   look more alike to a colour-blind reader than two small ones, because there
   is no adjacent neutral to judge either against. Every mark here is the same
   toneMark shape the 8px dots use, at 32px, and every one of them is beside the
   word for its state.
   =========================================================================== */

export function AcrossTheRoom({
  open,
  onClose,
  status,
}: {
  open: boolean;
  onClose: () => void;
  status: Status | null | undefined;
}) {
  // Escape closes it, because every other dismissible layer in this UI answers
  // to Escape and one that does not is the one that feels stuck. Bound only
  // while open, so the console keeps Escape for its dialogs the rest of the
  // time.
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onClose]);

  const stateLabel = useStateLabel();
  if (!open) return null;

  const air = onAir(status?.destinations);
  const faults = attention(status);
  const live = air.live > 0;
  const ingest = status?.ingest;
  const enabled = (status?.destinations ?? []).filter((d) => d.enabled);

  return (
    // Fixed to the viewport rather than to <main>: this is the whole screen by
    // definition, and a version of it that left the sidebar and header showing
    // would be the ordinary console with bigger numbers in the middle.
    //
    // bg-background rather than a translucent scrim, for the same reason the
    // theme is near-black at all: anything showing through is contrast this
    // view is spending on nothing.
    <div
      role="dialog"
      aria-modal="true"
      aria-label="Across the room"
      className="fixed inset-0 z-50 flex flex-col gap-6 overflow-y-auto bg-background p-8"
    >
      <div className="flex items-start justify-between gap-4">
        <div className="flex items-center gap-5">
          <StatusDot tone={live ? "live" : air.failed > 0 ? "down" : "idle"} size="xl" />
          <div>
            {/* clamp() rather than a token: the type scale in index.css tops out
                at 28px for a page title, which is correct for the console and
                half the size this needs. A seventh scale step used by one view
                would be a step somebody picks by accident later. */}
            <div className="text-[clamp(2.5rem,9vw,7rem)] font-semibold uppercase leading-none tracking-tight">
              {live ? "On air" : "Off air"}
            </div>
            <div className="tnum mt-2 font-mono text-[clamp(1rem,2.5vw,1.75rem)] text-muted-foreground">
              {air.live} of {air.enabled} destinations
              {ingest?.state === "running" && ` · ${kbps(ingest.progress?.bitrateKbps ?? 0)}`}
            </div>
          </div>
        </div>
        <Button variant="ghost" size="icon-sm" onClick={onClose} aria-label="Close">
          <X />
        </Button>
      </div>

      {faults.length > 0 && (
        <div className="flex flex-col gap-2">
          {faults.map((f) => (
            <div
              key={f.key}
              className="flex items-center gap-4 rounded-md border border-border-strong bg-card px-4 py-3"
            >
              <StatusDot tone={f.tone} size="lg" />
              <span className="min-w-0 flex-1 truncate text-[clamp(1rem,2.5vw,1.75rem)] font-medium">
                {f.subject}
              </span>
              <span className="shrink-0 font-mono text-[clamp(0.875rem,2vw,1.5rem)] uppercase tracking-wider text-muted-foreground">
                {f.state ? stateLabel(f.state) : "check"}
              </span>
            </div>
          ))}
        </div>
      )}

      {/* Every enabled destination, not only the broken ones. The fault list
          above answers "what is wrong"; this answers "is the one I care about
          still up", which is a different question and the one somebody walks
          over to ask. */}
      <ul className="flex flex-col gap-1.5">
        {enabled.map((d) => {
          const tone = toneForState(d.process?.state);
          return (
            <li key={d.id} className="flex items-center gap-4 px-1">
              <StatusDot tone={tone} size="lg" />
              <span className="min-w-0 flex-1 truncate text-[clamp(1rem,2.2vw,1.5rem)]">
                {d.name}
              </span>
              <span className="shrink-0 font-mono text-[clamp(0.875rem,1.8vw,1.25rem)] uppercase tracking-wider text-muted-foreground">
                {stateLabel(d.process?.state)}
              </span>
            </li>
          );
        })}
      </ul>
    </div>
  );
}
