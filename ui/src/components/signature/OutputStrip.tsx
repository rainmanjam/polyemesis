import { AlertTriangle, ArrowRight } from "lucide-react";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { LUFS_FLOOR, routingDropsSignal, type OutputState } from "@/lib/outputStrip";

/* A10: where each reading on this destination is taken. The source meter reads
 * the ingest before routing; the output reading is this destination's loudness
 * analyser after routing. Side by side they answer the question the source
 * meter alone cannot: is silence at the platform the source's, or the mix's? */

const fmt = (v: number) => v.toFixed(1);

export function OutputStrip({
  state,
  source,
}: {
  state: OutputState;
  /** The loudest ingest peak among the tracks this destination sends, in dBFS,
   *  or null when no live meter covers them. */
  source: number | null;
}) {
  const t = useT();
  const dropped = routingDropsSignal(state, source);

  let output: string;
  switch (state.kind) {
    case "off":
      output = t("route.strip.off");
      break;
    case "absent":
      output = t("route.strip.absent");
      break;
    case "starting":
      output = t("route.strip.starting");
      break;
    case "error":
      output = t("route.strip.error", { error: state.error });
      break;
    case "stale":
      output = t("route.strip.stale", {
        time: new Date(state.report.at).toLocaleTimeString(undefined, {
          hour: "2-digit",
          minute: "2-digit",
          second: "2-digit",
        }),
      });
      break;
    case "live":
      output =
        state.report.momentaryLufs <= LUFS_FLOOR
          ? t("route.strip.outputBelowGate", { tp: fmt(state.report.truePeakDbtp) })
          : t("route.strip.outputReading", {
              lufs: fmt(state.report.momentaryLufs),
              tp: fmt(state.report.truePeakDbtp),
            });
  }

  return (
    <div className="flex flex-col gap-1.5 rounded-md border border-border bg-card px-3 py-2" data-testid="output-strip">
      <div className="text-sm uppercase tracking-wider text-muted-foreground">{t("route.strip.title")}</div>
      <div className="grid grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)] items-start gap-2">
        <div className="min-w-0">
          <div className="text-sm text-muted-foreground">{t("route.strip.source")}</div>
          <div className="tnum font-mono text-sm" data-testid="strip-source">
            {source === null ? t("route.strip.sourceNone") : t("route.strip.sourcePeak", { db: fmt(source) })}
          </div>
        </div>
        <ArrowRight className="mt-3 h-3.5 w-3.5 text-muted-foreground" aria-hidden />
        <div className="min-w-0">
          <div className="text-sm text-muted-foreground">{t("route.strip.output")}</div>
          <div
            className={cn(
              "tnum font-mono text-sm",
              state.kind === "live" ? "text-foreground" : "text-muted-foreground",
            )}
            data-testid="strip-output"
            data-state={state.kind}
          >
            {output}
          </div>
        </div>
      </div>
      {dropped && (
        <div
          className="flex items-start gap-1.5 rounded border border-warn/30 bg-warn-dim px-2 py-1.5 text-sm text-warn"
          role="status"
          data-testid="strip-dropped"
        >
          <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
          <span>{t("route.strip.dropped")}</span>
        </div>
      )}
    </div>
  );
}
