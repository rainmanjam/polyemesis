import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import { Check, Crosshair, Loader2, Sparkles, X } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Label } from "@/components/ui/label";
import { http } from "@/lib/api";
import { useT } from "@/lib/i18n";
import { timecode } from "@/lib/timeline";
import type { Highlight, HighlightSignal, HighlightsView } from "@/lib/vertical";

/* The highlight review queue (Wave 4 #5a).

   A scan only SUGGESTS. Nothing here or on the server cuts a clip until the
   operator presses Accept, which sends the editor's own export settings --
   vertical layout included -- with the suggestion's span. */

interface Props {
  recordingId: number;
  /** The editor's export body, minus in and out: what Accept exports with. */
  exportBody: () => Record<string, unknown>;
  onPreview: (startMs: number, endMs: number) => void;
  onAccepted: () => void;
}

interface ScanJob {
  id: number;
  state: string;
}

export function HighlightsPanel({ recordingId, exportBody, onPreview, onAccepted }: Readonly<Props>) {
  const t = useT();
  const [view, setView] = useState<HighlightsView | null>(null);
  const [useModel, setUseModel] = useState(false);
  const [scanning, setScanning] = useState(false);
  const [busy, setBusy] = useState<number | null>(null);

  const load = useCallback(() => {
    http
      .get<HighlightsView>(`/clipper/recordings/${recordingId}/highlights`)
      .then(setView)
      .catch(() => undefined);
  }, [recordingId]);

  const checkScan = useCallback(() => {
    http
      .get<{ jobs: ScanJob[] | null }>(`/jobs?kind=highlights.scan&recordingId=${recordingId}&limit=1`)
      .then((r) => {
        const active = (r.jobs ?? []).some((j) => j.state === "queued" || j.state === "running" || j.state === "deferred");
        setScanning((was) => {
          if (was && !active) load();
          return active;
        });
      })
      .catch(() => undefined);
  }, [recordingId, load]);

  useEffect(() => {
    load();
    checkScan();
  }, [load, checkScan]);

  useEffect(() => {
    if (!scanning) return;
    const h = window.setInterval(checkScan, 2000);
    return () => window.clearInterval(h);
  }, [scanning, checkScan]);

  const scan = async () => {
    try {
      await http.post(`/clipper/recordings/${recordingId}/highlights/scan`, { useModel });
      setScanning(true);
      toast.success(t("clipedit.highlights.scanQueued"));
    } catch (err) {
      toast.error(err instanceof Error && err.message ? err.message : t("clipedit.highlights.scanFailed"));
    }
  };

  const accept = async (h: Highlight) => {
    setBusy(h.id);
    try {
      await http.post(`/clipper/highlights/${h.id}/accept`, { ...exportBody(), inMs: h.startMs, outMs: h.endMs });
      toast.success(t("clipedit.highlights.acceptQueued"));
      load();
      onAccepted();
    } catch (err) {
      toast.error(err instanceof Error && err.message ? err.message : t("clipedit.highlights.acceptFailed"));
    } finally {
      setBusy(null);
    }
  };

  const dismiss = async (h: Highlight) => {
    setBusy(h.id);
    try {
      await http.post(`/clipper/highlights/${h.id}/dismiss`);
      load();
    } catch (err) {
      toast.error(err instanceof Error && err.message ? err.message : t("clipedit.highlights.dismissFailed"));
    } finally {
      setBusy(null);
    }
  };

  const signalText = (s: HighlightSignal) => {
    switch (s.kind) {
      case "chat":
        return t("clipedit.highlights.signalChat", { peak: Math.round(s.peak), baseline: Math.round(s.baseline * 10) / 10 });
      case "loudness":
        return t("clipedit.highlights.signalLoudness", { peak: Math.round(s.peak * 10) / 10, baseline: Math.round(s.baseline * 10) / 10 });
      default:
        return t("clipedit.highlights.signalModel", { pct: Math.round(s.strength * 100) });
    }
  };

  const suggestions = view?.suggestions ?? [];

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("clipedit.highlights.title")}</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        <Label className="flex items-start gap-2 text-tiny">
          <Checkbox checked={useModel} disabled={!view?.modelAvailable} onCheckedChange={(v) => setUseModel(v === true)} />
          <span>
            {t("clipedit.highlights.useModel")}
            <span className="mt-0.5 block text-tiny text-muted-foreground">
              {view && !view.modelAvailable
                ? t("clipedit.highlights.modelUnavailable", { reason: view.modelReason ?? "" })
                : t("clipedit.highlights.useModelBody")}
            </span>
          </span>
        </Label>
        <Button variant="secondary" onClick={scan} disabled={scanning}>
          {scanning ? <Loader2 className="animate-spin" /> : <Sparkles />}
          {scanning ? t("clipedit.highlights.scanning") : t("clipedit.highlights.scan")}
        </Button>
        <p className="text-tiny text-muted-foreground">{t("clipedit.highlights.acceptUsesSettings")}</p>

        {suggestions.length === 0 ? (
          <p className="text-tiny text-muted-foreground">{t("clipedit.highlights.empty")}</p>
        ) : (
          <ul className="flex flex-col gap-2" aria-label={t("clipedit.highlights.title")}>
            {suggestions.map((h) => (
              <li key={h.id} data-testid="highlight" className="flex flex-col gap-1 rounded-md border border-border p-2">
                <div className="flex items-center justify-between gap-2">
                  <span className="font-mono text-tiny">
                    {t("clipedit.highlights.range", { start: timecode(h.startMs), end: timecode(h.endMs) })}
                  </span>
                  <Badge variant="default">{t("clipedit.highlights.score", { pct: Math.round(h.score * 100) })}</Badge>
                </div>
                <div className="flex flex-wrap gap-1">
                  {h.signals.map((s, i) => (
                    <Badge key={`${i}-${s.kind}`} variant="outline">{signalText(s)}</Badge>
                  ))}
                </div>
                {h.excerpt && <p className="text-tiny text-muted-foreground">{h.excerpt}</p>}
                {h.state === "pending" ? (
                  <div className="flex flex-wrap gap-1">
                    <Button size="sm" variant="ghost" onClick={() => onPreview(h.startMs, h.endMs)}>
                      <Crosshair /> {t("clipedit.highlights.preview")}
                    </Button>
                    <Button size="sm" onClick={() => accept(h)} disabled={busy === h.id}>
                      <Check /> {t("clipedit.highlights.accept")}
                    </Button>
                    <Button size="sm" variant="ghost" onClick={() => dismiss(h)} disabled={busy === h.id}>
                      <X /> {t("clipedit.highlights.dismiss")}
                    </Button>
                  </div>
                ) : (
                  <span className="text-tiny text-muted-foreground">
                    {h.state === "accepted" ? t("clipedit.highlights.accepted") : t("clipedit.highlights.dismissed")}
                  </span>
                )}
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}
