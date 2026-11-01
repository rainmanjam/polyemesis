import { useCallback, useEffect, useState } from "react";

import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { ApiError, api } from "@/lib/api";
import { useT } from "@/lib/i18n";
import { YOUTUBE_VERTICAL_TIERS, type YouTubeVerticalTier } from "@/lib/youtubeVertical";
import type { DestinationId, YouTubeVerticalView } from "@/lib/types";

/** Where the pairing stands, as the dialog reads it. `none` is a real answer
 *  (no vertical half yet), not a failure: the server says 404 for it. */
type PairState = { kind: "loading" } | { kind: "none" } | { kind: "paired"; view: YouTubeVerticalView };

/** The YouTube vertical half, in the destination dialog (#7).
 *
 *  Three things happen here and they are kept apart on purpose. Adding the half
 *  creates a second YouTube ingest through the API. The one-time Live Control
 *  Room step is a human action the API cannot do, so it is written out as steps
 *  and confirmed by the operator. Confirming records the fact and does not check
 *  it: polyemesis cannot see Live Control Room's pairing. The copy says so.
 *
 *  PROVISIONAL. Whether the pairing persists across broadcasts is unverified, and
 *  the panel says so rather than promising the pairing holds. */
export function YouTubeVerticalPanel({ destinationId }: Readonly<{ destinationId: DestinationId }>) {
  const t = useT();
  const [pair, setPair] = useState<PairState>({ kind: "loading" });
  const [tier, setTier] = useState<YouTubeVerticalTier>(YOUTUBE_VERTICAL_TIERS[0]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    try {
      const view = await api.youtubeVerticalStatus(destinationId);
      setPair({ kind: "paired", view });
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) {
        setPair({ kind: "none" });
      } else {
        setPair({ kind: "none" });
        setError(err instanceof Error ? err.message : "");
      }
    }
  }, [destinationId]);

  useEffect(() => {
    void load();
  }, [load]);

  const add = async () => {
    setBusy(true);
    setError("");
    try {
      await api.createYouTubeVertical(destinationId, tier);
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "");
    } finally {
      setBusy(false);
    }
  };

  const confirm = async () => {
    setBusy(true);
    setError("");
    try {
      await api.confirmYouTubeVertical(destinationId);
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "");
    } finally {
      setBusy(false);
    }
  };

  if (pair.kind === "loading") return null;

  return (
    <section
      className="flex flex-col gap-3 rounded-md border border-border p-3"
      aria-label={t("wave3.vertical.title")}
      data-testid="youtube-vertical-panel"
    >
      <h3 className="text-sm font-medium">{t("wave3.vertical.title")}</h3>
      <p className="text-xs text-muted-foreground">{t("wave3.vertical.intro")}</p>

      {pair.kind === "none" && (
        <div className="flex flex-wrap items-end gap-2">
          <div className="flex min-w-[14rem] flex-col gap-1">
            <Label>{t("wave3.vertical.tierLabel")}</Label>
            <Select value={tier} onValueChange={(v) => setTier(v as YouTubeVerticalTier)}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {YOUTUBE_VERTICAL_TIERS.map((k) => (
                  <SelectItem key={k} value={k}>
                    {t(`wave3.vertical.tier.${k}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <Button size="sm" onClick={() => void add()} disabled={busy}>
            {t("wave3.vertical.add")}
          </Button>
        </div>
      )}

      {pair.kind === "paired" && !pair.view.pairingConfirmed && (
        <div className="flex flex-col gap-2">
          <p className="text-sm font-medium">{t("wave3.vertical.stepsTitle")}</p>
          <ol className="list-decimal space-y-1 pl-5 text-xs">
            <li>{t("wave3.vertical.step1")}</li>
            <li>{t("wave3.vertical.step2")}</li>
            <li>{t("wave3.vertical.step3")}</li>
            <li>{t("wave3.vertical.step4")}</li>
          </ol>
          <p className="text-xs text-warn">{t("wave3.vertical.provisional")}</p>
          <Button size="sm" variant="outline" onClick={() => void confirm()} disabled={busy}>
            {t("wave3.vertical.confirm")}
          </Button>
        </div>
      )}

      {pair.kind === "paired" && pair.view.pairingConfirmed && (
        <p className="text-xs text-muted-foreground">{t("wave3.vertical.confirmed")}</p>
      )}

      {error && (
        <p role="alert" className="text-xs text-destructive">
          {error}
        </p>
      )}
    </section>
  );
}
