import { useEffect, useState } from "react";
import { AlertTriangle, Info, OctagonAlert } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { api } from "@/lib/api";
import { timestamp } from "@/lib/format";
import type { TimelineRow } from "@/lib/types";
import { useT, type TranslationKey } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/** How often the timeline re-reads. Transitions are a few per incident, so a
 *  slow poll is enough and keeps this off the live socket. */
const REFRESH_MS = 15_000;

const SEVERITY: Record<string, { icon: typeof Info; tone: string; label: TranslationKey }> = {
  critical: { icon: OctagonAlert, tone: "text-down", label: "timeline.severityCritical" },
  warning: { icon: AlertTriangle, tone: "text-warn", label: "timeline.severityWarning" },
  info: { icon: Info, tone: "text-muted-foreground", label: "timeline.severityInfo" },
};

/** The stream's history, newest first: failovers, restarts and outages. An
 *  empty list says so, and a failed read says so, so nobody reads a broken
 *  fetch as a quiet stream. */
export function MonitorTimeline() {
  const t = useT();
  const [rows, setRows] = useState<TimelineRow[] | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let cancelled = false;
    const load = () =>
      api
        .listTimeline()
        .then((r) => {
          if (cancelled) return;
          setRows(r);
          setFailed(false);
        })
        .catch(() => {
          if (!cancelled) setFailed(true);
        });
    void load();
    const id = window.setInterval(load, REFRESH_MS);
    return () => {
      cancelled = true;
      window.clearInterval(id);
    };
  }, []);

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("timeline.title")}</CardTitle>
      </CardHeader>
      <CardContent>
        {failed ? (
          <p role="alert" className="text-tiny text-down">
            {t("timeline.loadFailed")}
          </p>
        ) : rows === null ? null : rows.length === 0 ? (
          <p className="text-tiny text-muted-foreground">{t("timeline.empty")}</p>
        ) : (
          <ol className="flex flex-col gap-1.5">
            {rows.map((row) => {
              const sev = SEVERITY[row.severity] ?? SEVERITY.info;
              const Icon = sev.icon;
              return (
                <li key={row.id} className="flex items-start gap-2 text-tiny">
                  <Icon
                    aria-label={t(sev.label)}
                    role="img"
                    className={cn("mt-0.5 h-3.5 w-3.5 shrink-0", sev.tone)}
                  />
                  <span className="tnum shrink-0 text-muted-foreground">{timestamp(row.at)}</span>
                  <span className="min-w-0 break-words">{row.title}</span>
                </li>
              );
            })}
          </ol>
        )}
      </CardContent>
    </Card>
  );
}
