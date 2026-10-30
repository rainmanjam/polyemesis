import { AlertTriangle } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { useT, type TranslationKey } from "@/lib/i18n";
import { routingChanges, type RoutingChange } from "@/lib/routingDiff";
import type { RoutingProfile } from "@/lib/types";

/* The preset review step (A14).
 *
 * A preset rewrites the whole mix: tracks, matrix, loudness and exclusions in
 * one go. Applying it used to land straight in the draft with a success toast,
 * so the operator learnt what it had done by reading the editor afterwards.
 * This shows it first: the tracks the mix will carry, the tracks leaving and
 * joining it, the other settings it moves and the destination it changes,
 * with Back to edit as the way out. Applying still only writes the draft; Save
 * is what reaches the destination. */

export interface PresetReview {
  presetId: string;
  mix: "live" | "vod";
  before: RoutingProfile | null;
  after: RoutingProfile;
  resolve: (apply: boolean) => void;
}

const SETTING: Partial<Record<RoutingChange["kind"], TranslationKey>> = {
  mode: "routing.saveBar.diffMode",
  matrix: "routing.saveBar.diffMatrix",
  loudness: "routing.saveBar.diffLoudness",
  delay: "routing.saveBar.diffDelay",
  ducking: "routing.saveBar.diffDucking",
  music: "routing.saveBar.diffMusic",
  sampleRate: "routing.saveBar.diffSampleRate",
};

export function PresetReviewDialog({
  review,
  presetName,
  destinationName,
  live,
}: Readonly<{
  review: PresetReview | null;
  presetName: string;
  destinationName: string;
  /** The destination is enabled, so a Save after this restarts or retunes it. */
  live: boolean;
}>) {
  const t = useT();
  if (!review) return null;

  const changes = routingChanges({ stored: review.before, draft: review.after });
  const inMix = review.after.tracks.filter((r) => r.enabled).map((r) => r.track + 1);
  const leaving = changes.flatMap((c) => (c.kind === "trackRemoved" ? [c.track + 1] : []));
  const joining = changes.flatMap((c) => (c.kind === "trackAdded" ? [c.track + 1] : []));
  const settings = changes.flatMap((c) => {
    const key = SETTING[c.kind];
    return key ? [key] : [];
  });
  const list = (n: number[]) => (n.length ? n.join(", ") : t("routing.presetReview.none"));

  return (
    <Dialog open onOpenChange={(open) => !open && review.resolve(false)}>
      <DialogContent className="max-w-lg" data-testid="preset-review">
        <DialogHeader>
          <DialogTitle>{t("routing.presetReview.title", { preset: presetName, name: destinationName })}</DialogTitle>
          <DialogDescription>
            {review.mix === "vod" ? t("routing.presetReview.bodyVod") : t("routing.presetReview.body")}
          </DialogDescription>
        </DialogHeader>

        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-sm">
          <dt className="text-muted-foreground">{t("routing.presetReview.tracksIn")}</dt>
          <dd className="tnum font-mono">{list(inMix)}</dd>
          <dt className="text-muted-foreground">{t("routing.presetReview.leaving")}</dt>
          <dd className={leaving.length ? "tnum font-mono text-warn" : "tnum font-mono"}>{list(leaving)}</dd>
          <dt className="text-muted-foreground">{t("routing.presetReview.joining")}</dt>
          <dd className="tnum font-mono">{list(joining)}</dd>
          <dt className="text-muted-foreground">{t("routing.presetReview.destinations")}</dt>
          <dd>{destinationName}</dd>
        </dl>

        {settings.length > 0 && (
          <ul className="flex flex-col gap-0.5 text-sm text-muted-foreground">
            {settings.map((key) => (
              <li key={key}>{t(key, { name: destinationName })}</li>
            ))}
          </ul>
        )}
        {changes.length === 0 && (
          <p className="text-sm text-muted-foreground">{t("routing.presetReview.noChange")}</p>
        )}

        {live && (
          <p className="flex items-start gap-1.5 text-sm text-warn">
            <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
            {t("routing.presetReview.live", { name: destinationName })}
          </p>
        )}

        <DialogFooter>
          <Button variant="outline" onClick={() => review.resolve(false)}>
            {t("routing.presetReview.back")}
          </Button>
          <Button onClick={() => review.resolve(true)}>{t("routing.presetReview.apply")}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
