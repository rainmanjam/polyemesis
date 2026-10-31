import { Scissors, Timer } from "lucide-react";
import { Button } from "@/components/ui/button";
import type { DestStatus } from "@/lib/types";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/** A destination's broadcast delay on its card (#11).
 *
 *  Three states, each answering the question the operator has at that moment:
 *
 *  - buffering: "when does this go out?" -- the fill, against the delay.
 *  - on air: "is this delayed?" -- a marker, so nobody wonders why the
 *    platform is behind the preview.
 *  - draining: "is this still on the air after I stopped it?" -- YES, for
 *    this long, with the control that ends it. A delay exists to keep things
 *    off the air, so the cut is on the card, not behind a menu. */
export function DelayPanel({
  dest,
  onCutNow,
  busy,
}: {
  dest: DestStatus;
  /** Discards the buffered tail. Absent for a card that cannot act. */
  onCutNow?: () => void;
  busy?: boolean;
}) {
  const t = useT();
  const d = dest.delay;
  const seconds = d?.seconds ?? dest.delaySeconds ?? 0;
  if (!seconds) return null;

  if (d?.phase === "draining") {
    return (
      <div
        data-testid="delay-draining"
        role="status"
        className="flex items-center gap-2 rounded border border-down/30 bg-down-dim px-2 py-1 text-sm text-down"
      >
        <Timer className="h-3 w-3 shrink-0" />
        <span className="flex-1">
          {t("delay.draining", { seconds: Math.ceil(d.drainRemainingSeconds ?? 0) })}
        </span>
        {onCutNow && (
          <Button
            size="sm"
            variant="destructive"
            onClick={onCutNow}
            disabled={busy}
            title={t("delay.cutNowTitle", { name: dest.name })}
          >
            <Scissors /> {t("delay.cutNow")}
          </Button>
        )}
      </div>
    );
  }

  if (d?.phase === "buffering") {
    const pct = Math.round(Math.min(1, Math.max(0, d.fill)) * 100);
    return (
      <div data-testid="delay-buffering" className="flex flex-col gap-1 text-sm text-muted-foreground">
        <span className="flex items-center gap-1.5">
          <Timer className="h-3 w-3 shrink-0" />
          {t("delay.buffering", { buffered: Math.floor(d.bufferedSeconds), seconds })}
        </span>
        <div
          className="h-1 w-full overflow-hidden rounded bg-muted"
          role="progressbar"
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={pct}
          aria-label={t("delay.fillLabel")}
        >
          <div className="h-full bg-primary" style={{ width: `${pct}%` }} />
        </div>
      </div>
    );
  }

  return (
    <span
      data-testid="delay-badge"
      className={cn(
        "inline-flex w-fit items-center gap-1 rounded border px-1.5 py-0.5 text-[11px]",
        d?.phase === "on-air" ? "border-primary/40 text-primary" : "border-border text-muted-foreground",
      )}
    >
      <Timer className="h-3 w-3" />
      {t("delay.badge", { seconds })}
    </span>
  );
}
