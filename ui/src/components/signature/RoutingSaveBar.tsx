import { Button } from "@/components/ui/button";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { Loader2, Save, Undo2 } from "lucide-react";

export interface RoutingSaveBarProps {
  /** True while the form differs from what is stored. Nothing renders otherwise. */
  dirty: boolean;
  saving: boolean;
  /** A compile error or similar means the stored result would be wrong. */
  blocked: boolean;
  /** The destination the pending change applies to. Named, never implied. */
  destinationName: string;
  /** An enabled destination restarts when saved, so the bar says so. Not the
   *  same as on air: a disabled destination saves without a restart. */
  restarts: boolean;
  /** The pending change moves nothing but live volume levels (lib/saveImpact),
   *  so an enabled destination takes it on its command channel instead of
   *  restarting, on a server that supports that. */
  levelOnly?: boolean;
  onSave: () => void;
  onDiscard: () => void;
}

/** The pending-change bar for the routing editor (A7).
 *
 *  Sticky to the bottom of the page while there is something to save, so the
 *  change stays in view wherever the operator has scrolled. It names the
 *  destination, warns when saving restarts it, and keeps Save and Discard as the
 *  only ways out. Nothing is applied on its own. */
export function RoutingSaveBar({
  dirty,
  saving,
  blocked,
  destinationName,
  restarts,
  levelOnly = false,
  onSave,
  onDiscard,
}: RoutingSaveBarProps) {
  const t = useT();
  if (!dirty) return null;
  const warnRestart = restarts && !levelOnly;

  return (
    <div
      className={cn(
        "sticky bottom-0 z-20 mt-3 flex flex-wrap items-center justify-between gap-2 rounded-md border px-3 py-2 shadow-sm",
        warnRestart ? "border-warn bg-warn-dim" : "border-border bg-card",
      )}
    >
      <p role="status" className="min-w-0 flex-1 text-sm">
        {warnRestart
          ? t("routing.saveBar.restarts", { name: destinationName })
          : restarts
            ? t("routing.saveBar.appliesLive", { name: destinationName })
            : t("routing.saveBar.changed", { name: destinationName })}
      </p>
      <div className="flex shrink-0 items-center gap-2">
        <Button variant="ghost" size="sm" onClick={onDiscard} disabled={saving}>
          <Undo2 /> {t("routing.saveBar.discard")}
        </Button>
        <Button size="sm" onClick={onSave} disabled={saving || blocked}>
          {saving ? <Loader2 className="animate-spin" /> : <Save />}
          {t("routing.saveBar.save")}
        </Button>
      </div>
    </div>
  );
}
