import { useEffect, useState } from "react";
import { AlertTriangle, Info, OctagonAlert } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { api } from "@/lib/api";
import { timestamp } from "@/lib/format";
import type { TimelineRow } from "@/lib/types";
import { useT, type TranslationKey } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";

/** How often the timeline re-reads. Transitions are a few per incident, so a
 *  slow poll is enough and keeps this off the live socket. */
const REFRESH_MS = 15_000;

const SEVERITY: Record<string, { icon: typeof Info; tone: string; label: TranslationKey }> = {
  critical: { icon: OctagonAlert, tone: "text-down", label: "timeline.severityCritical" },
  warning: { icon: AlertTriangle, tone: "text-warn", label: "timeline.severityWarning" },
  info: { icon: Info, tone: "text-muted-foreground", label: "timeline.severityInfo" },
};

/** The filter families, in chip order. A row's family is the part of its type
 *  before the dot (internal/alerts names them that way); anything else is
 *  Other, so a type added on the server is never filtered out of sight. */
const FAMILIES = ["failover", "ingest", "destination", "other"] as const;
type Family = (typeof FAMILIES)[number];
const FAMILY_LABEL: Record<Family, TranslationKey> = {
  // The names the rest of the console uses for these, so a chip reads the
  // same as the Settings card and the header chip it refers to.
  failover: "set.failover",
  ingest: "chrome.ingest",
  destination: "route.destinations",
  other: "timeline.filterOther",
};

function timelineFamily(type: string): Family {
  const head = type.split(".")[0];
  return (FAMILIES as readonly string[]).includes(head) ? (head as Family) : "other";
}

/** The stream's history, newest first: failovers, restarts and outages. An
 *  empty list says so, and a failed read says so, so nobody reads a broken
 *  fetch as a quiet stream. */
export function MonitorTimeline() {
  const t = useT();
  const [rows, setRows] = useState<TimelineRow[] | null>(null);
  const [failed, setFailed] = useState(false);
  const [family, setFamily] = useState<Family | null>(null);

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
          <div className="flex flex-col gap-2">
          {(() => {
            // Chips only for families the history has, and only when there is
            // more than one: a filter with one choice filters nothing.
            const present = FAMILIES.filter((f) => rows.some((r) => timelineFamily(r.type) === f));
            if (present.length < 2) return null;
            return (
              <div role="group" aria-label={t("timeline.filterLabel")} className="flex flex-wrap gap-1">
                {[null, ...present].map((f) => (
                  <Button
                    key={f ?? "all"}
                    size="sm"
                    variant={family === f ? "secondary" : "ghost"}
                    aria-pressed={family === f}
                    className="h-6 px-2 text-tiny"
                    onClick={() => setFamily(f)}
                  >
                    {f === null ? t("timeline.filterAll") : t(FAMILY_LABEL[f])}
                  </Button>
                ))}
              </div>
            );
          })()}
          <ol className="flex flex-col gap-1.5">
            {rows.filter((row) => family === null || timelineFamily(row.type) === family).map((row) => {
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
                  <span className="flex min-w-0 flex-col">
                    <span className="break-words">{row.title}</span>
                    {row.detail && <span className="break-words text-muted-foreground">{row.detail}</span>}
                  </span>
                </li>
              );
            })}
          </ol>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
