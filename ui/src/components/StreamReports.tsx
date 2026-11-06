import { useEffect, useState } from "react";
import { Link } from "react-router";

import { EmptyState } from "@/components/EmptyState";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { api } from "@/lib/api";
import { platformName } from "@/lib/platforms";
import { formatAverage, PLAYER } from "@/lib/audience";
import { shortDuration, timestamp } from "@/lib/format";
import { useT } from "@/lib/i18n";
import type { AnalyticsReport, AnalyticsRow, AnalyticsStream } from "@/lib/types";

/** Per-stream reports (Wave 4 #4): each on-air span the analytics sampler
 *  recorded, with peak and average viewers per platform, chat per platform,
 *  the top chatters, and what was recorded and clipped during it.
 *
 *  A platform that never reported a count shows "not reported" for its peak
 *  and average -- not 0 -- and the average is over the samples that did. */
export function StreamReports() {
  const t = useT();
  const [streams, setStreams] = useState<AnalyticsStream[] | null>(null);
  const [failed, setFailed] = useState(false);
  const [open, setOpen] = useState<number | null>(null);
  const [report, setReport] = useState<AnalyticsReport | null>(null);

  useEffect(() => {
    let alive = true;
    api.analytics
      .streams()
      .then((r) => {
        if (alive) setStreams(r.streams);
      })
      .catch(() => {
        if (alive) setFailed(true);
      });
    return () => {
      alive = false;
    };
  }, []);

  useEffect(() => {
    // A closed report is not cleared here: the render shows a report only when
    // it is the one open, so a stale one is simply not drawn.
    if (open === null) return;
    let alive = true;
    api.analytics
      .report(open)
      .then((r) => {
        if (alive) setReport(r);
      })
      .catch(() => {
        if (alive) setReport(null);
      });
    return () => {
      alive = false;
    };
  }, [open]);

  return (
    <Card data-testid="stream-reports">
      <CardHeader>
        <CardTitle>{t("aud.reports")}</CardTitle>
        <CardDescription>{t("aud.reportsDesc")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {failed && <p className="text-sm text-muted-foreground">{t("aud.unavailable")}</p>}
        {streams && streams.length === 0 && <EmptyState title={t("aud.noStreams")} className="py-4" />}
        {streams && streams.length > 0 && (
          <ul className="flex flex-col gap-1">
            {streams.map((s) => (
              <li key={s.id} className="flex flex-wrap items-center justify-between gap-2 text-sm">
                <span className="min-w-0 truncate">
                  <span className="text-foreground">{timestamp(s.startedAt)}</span>
                  <span className="text-muted-foreground"> · {shortDuration(s.durationMs)}</span>
                  {s.title && <span className="text-muted-foreground"> · {s.title}</span>}
                </span>
                <span className="flex items-center gap-2">
                  <span className="tabular-nums text-muted-foreground">
                    {t("aud.peak")}: {s.peakTotal ?? t("aud.notReported")}
                  </span>
                  <Button
                    variant="outline"
                    size="sm"
                    aria-expanded={open === s.id}
                    onClick={() => setOpen(open === s.id ? null : s.id)}
                  >
                    {open === s.id ? t("aud.hideReport") : t("aud.showReport")}
                  </Button>
                </span>
              </li>
            ))}
          </ul>
        )}
        {report && open === report.stream.id && <Report report={report} />}
      </CardContent>
    </Card>
  );
}

function Report({ report }: { report: AnalyticsReport }) {
  const t = useT();
  const s = report.summary;
  return (
    <div className="flex flex-col gap-3 rounded-md border border-border p-3" data-testid="stream-report">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-sm text-foreground">
          {t("aud.peakTotal")}: {s.peakTotal ?? t("aud.notReported")} · {t("aud.averageTotal")}:{" "}
          {typeof s.averageTotal === "number" ? formatAverage(s.averageTotal) : t("aud.notReported")}
        </span>
        <a
          className="text-sm text-primary-bright underline"
          href={api.analytics.csvUrl(report.stream.id)}
          download
        >
          {t("aud.downloadCsv")}
        </a>
      </div>

      <table className="w-full text-sm">
        <thead>
          <tr className="text-left text-xs text-muted-foreground">
            <th className="font-normal">{t("aud.platform")}</th>
            <th className="font-normal">{t("aud.peak")}</th>
            <th className="font-normal">{t("aud.average")}</th>
            <th className="font-normal">{t("aud.samples")}</th>
          </tr>
        </thead>
        <tbody>
          {s.rows.map((r, i) => (
            <ReportRow key={`${r.platform}-${r.account ?? ""}-${i}`} r={r} />
          ))}
        </tbody>
      </table>

      <div className="flex flex-col gap-1">
        <span className="text-xs text-muted-foreground">{t("aud.chat")}</span>
        {report.chat.length === 0 ? (
          <span className="text-sm text-muted-foreground">{t("aud.noChat")}</span>
        ) : (
          <span className="text-sm text-foreground">
            {report.chat.map((c) => `${platformName(c.platform)} ${c.messages}`).join(" · ")}
          </span>
        )}
      </div>

      {report.topChatters.length > 0 && (
        <div className="flex flex-col gap-1">
          <span className="text-xs text-muted-foreground">{t("aud.topChatters")}</span>
          <ol className="flex flex-col gap-0.5 text-sm">
            {report.topChatters.map((c, i) => (
              <li key={`${c.platform}-${c.name}-${i}`} className="flex justify-between gap-2">
                <span className="min-w-0 truncate text-foreground">
                  {c.name} <span className="text-muted-foreground">· {platformName(c.platform)}</span>
                </span>
                <span className="tabular-nums text-muted-foreground">{c.messages}</span>
              </li>
            ))}
          </ol>
        </div>
      )}

      {report.player && (
        <span className="text-xs text-muted-foreground">
          {t("aud.playerQoE", {
            sessions: report.player.sessions,
            rebuffers: report.player.rebuffers,
            errors: report.player.sessionsWithErrors,
          })}
        </span>
      )}

      <div className="flex flex-col gap-1">
        <span className="text-xs text-muted-foreground">{t("aud.made")}</span>
        {report.recordings.length === 0 && report.clips.length === 0 && (
          <span className="text-sm text-muted-foreground">{t("aud.nothingMade")}</span>
        )}
        {report.recordings.map((r) => (
          <span key={r.id} className="flex flex-wrap gap-2 text-sm">
            <a className="text-primary-bright underline" href={api.downloadUrl(r.id)} download>
              {r.filename}
            </a>
            {r.transcript && (
              <a className="text-primary-bright underline" href={api.captionExportUrl(r.id, "srt")} download>
                {t("aud.transcript")}
              </a>
            )}
            <Link className="text-muted-foreground underline" to="/library">
              {t("aud.inLibrary")}
            </Link>
          </span>
        ))}
        {report.clips.map((c) => (
          <a
            key={c.name}
            className="text-sm text-primary-bright underline"
            href={`/api/v1/clips/${encodeURIComponent(c.name)}/download`}
            download
          >
            {c.name}
          </a>
        ))}
      </div>
    </div>
  );
}

function ReportRow({ r }: { r: AnalyticsRow }) {
  const t = useT();
  const label = r.platform === PLAYER ? t("aud.player") : platformName(r.platform);
  return (
    <tr data-testid={`report-row-${r.platform}`}>
      <td>
        {label}
        {r.name && <span className="text-muted-foreground"> · {r.name}</span>}
      </td>
      <td className="tabular-nums">{typeof r.peak === "number" ? r.peak : t("aud.notReported")}</td>
      <td className="tabular-nums">
        {typeof r.average === "number" ? formatAverage(r.average) : t("aud.notReported")}
      </td>
      <td className="tabular-nums text-muted-foreground">
        {r.reported}/{r.samples}
      </td>
    </tr>
  );
}
