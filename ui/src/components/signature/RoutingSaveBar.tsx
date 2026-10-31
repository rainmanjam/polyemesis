import { useId } from "react";
import { Button } from "@/components/ui/button";
import { DisabledReason } from "@/components/DisabledReason";
import { SaveStatus } from "@/components/SaveStatus";
import { disabledDescribedBy } from "@/lib/disabledReason";
import type { SaveState } from "@/lib/saveModel";
import { useT, type TranslationKey } from "@/lib/i18n";
import { changedTracks, type RoutingChange } from "@/lib/routingDiff";
import { cn } from "@/lib/utils";
import { AlertTriangle, Loader2, Save, Undo2 } from "lucide-react";

export interface RoutingSaveBarProps {
  /** True while the form differs from what is stored. While false the bar
   *  stays, quiet, with both buttons greyed and the reason beside them: it is
   *  the page's only save surface (A26), so it never disappears. */
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
  /** What the save moves (lib/routingDiff). Counted in the summary; spelled out
   *  line by line when the destination is enabled, so a track leaving a live
   *  mix is read before it is applied rather than heard after. */
  changes?: readonly RoutingChange[];
  /** The kit's save state (lib/saveModel), shown beside the buttons. */
  saveState?: SaveState;
  /** Why Save changes is greyed, already translated, or null when it is not.
   *  Visible text linked to the button (A28), so a compile error in a mix the
   *  operator has collapsed out of view still says why Save is refused. */
  reason?: string | null;
  onSave: () => void;
  onDiscard: () => void;
}

const TRACK_LINE: Record<"trackAdded" | "trackRemoved" | "trackLevel", TranslationKey> = {
  trackAdded: "routing.saveBar.trackAdded",
  trackRemoved: "routing.saveBar.trackRemoved",
  trackLevel: "routing.saveBar.trackLevel",
};

const SETTING_LINE: Record<Exclude<RoutingChange["kind"], keyof typeof TRACK_LINE>, TranslationKey> = {
  mode: "routing.saveBar.diffMode",
  matrix: "routing.saveBar.diffMatrix",
  loudness: "routing.saveBar.diffLoudness",
  delay: "routing.saveBar.diffDelay",
  ducking: "routing.saveBar.diffDucking",
  music: "routing.saveBar.diffMusic",
  sampleRate: "routing.saveBar.diffSampleRate",
  secondMix: "routing.saveBar.diffSecondMix",
};

/** The pending-change bar for the routing editor (A7), and since A26 the ONLY
 *  save surface on the page: every edit, mute and gain included, waits here.
 *
 *  Sticky to the bottom of the page, so the change stays in view wherever the
 *  operator has scrolled. It names the destination, warns when saving restarts
 *  it, and keeps Save changes and Discard as the only ways out. Nothing is
 *  applied on its own. */
export function RoutingSaveBar({
  dirty,
  saving,
  blocked,
  destinationName,
  restarts,
  levelOnly = false,
  changes = [],
  saveState = dirty ? "dirty" : "clean",
  reason = null,
  onSave,
  onDiscard,
}: RoutingSaveBarProps) {
  const t = useT();
  const reasonId = useId();
  const warnRestart = dirty && restarts && !levelOnly;
  const tracks = changedTracks(changes);
  // Four lines is what fits above the buttons without the bar becoming a page.
  const shown = changes.slice(0, 4);

  return (
    <div
      className={cn(
        "sticky bottom-0 z-20 mt-3 flex flex-wrap items-center justify-between gap-2 rounded-md border px-3 py-2 shadow-sm",
        warnRestart ? "border-warn bg-warn-dim" : "border-border bg-card",
      )}
    >
      <div role="status" className="flex min-w-0 flex-1 flex-col gap-1">
        <p className="text-sm">
          {!dirty
            ? t("routingUx.unsavedNone")
            : warnRestart
              ? t("routing.saveBar.restarts", { name: destinationName })
              : restarts
                ? t("routing.saveBar.appliesLive", { name: destinationName })
                : t("routing.saveBar.changed", { name: destinationName })}
        </p>
        {dirty && changes.length > 0 && (
          <p className="text-xs text-muted-foreground" data-testid="save-bar-scope">
            {tracks.length > 0
              ? t("routing.saveBar.scopeTracks", { count: changes.length, tracks: tracks.map((n) => n + 1).join(", ") })
              : t("routing.saveBar.scope", { count: changes.length })}
          </p>
        )}
        {dirty && restarts && shown.length > 0 && (
          <ul className="flex flex-col gap-0.5 text-xs text-warn" data-testid="save-bar-diff">
            {shown.map((c) => (
              <li key={`${c.kind}-${"track" in c ? c.track : ""}`} className="flex items-center gap-1.5">
                <AlertTriangle className="h-3 w-3 shrink-0" aria-hidden="true" />
                {"track" in c
                  ? t(TRACK_LINE[c.kind], { n: c.track + 1, name: destinationName })
                  : t(SETTING_LINE[c.kind], { name: destinationName })}
              </li>
            ))}
            {changes.length > shown.length && (
              <li className="text-muted-foreground">
                {t("routing.saveBar.more", { count: changes.length - shown.length })}
              </li>
            )}
          </ul>
        )}
      </div>
      <div className="flex shrink-0 flex-wrap items-center justify-end gap-2">
        <SaveStatus state={saveState} />
        <DisabledReason id={reasonId} reason={reason} className="max-w-xs text-right" />
        <Button
          variant="ghost"
          size="sm"
          onClick={onDiscard}
          disabled={saving || !dirty}
          // Greyed for the same reason as Save changes when nothing is pending.
          aria-describedby={dirty ? undefined : disabledDescribedBy(reasonId, reason)}
        >
          <Undo2 /> {t("routing.saveBar.discard")}
        </Button>
        <Button
          size="sm"
          onClick={onSave}
          disabled={!dirty || saving || blocked}
          aria-describedby={disabledDescribedBy(reasonId, reason)}
        >
          {saving ? <Loader2 className="animate-spin" /> : <Save />}
          {t("routingUx.saveChanges")}
        </Button>
      </div>
    </div>
  );
}
