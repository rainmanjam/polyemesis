import { useEffect, useState } from "react";

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { api } from "@/lib/api";
import { platformName } from "@/lib/platforms";
import { orderReadings, PLAYER, readingCount, sparkPath } from "@/lib/audience";
import { clockTime } from "@/lib/format";
import { useT, type TranslationKey } from "@/lib/i18n";
import type { AnalyticsLive, AnalyticsReading } from "@/lib/types";

/** How often the panel re-reads the sampler. The sampler itself ticks every
 *  intervalSeconds (60 by default); reading faster than that only re-reads the
 *  same tick, so the panel waits at least this long and at most the interval. */
const MIN_POLL_MS = 15_000;

const REASON: Record<string, TranslationKey> = {
  offline: "aud.reasonOffline",
  starting: "aud.reasonStarting",
  hidden: "aud.reasonHidden",
  quota: "aud.reasonQuota",
  error: "aud.reasonError",
  unlinked: "aud.reasonUnlinked",
};

/** The cross-platform audience (Wave 4 #4): the total, each platform's count
 *  and a sparkline of the total, from the analytics sampler's last tick.
 *
 *  A COUNT THE PLATFORM DID NOT REPORT READS "not reported" AND IS LEFT OUT OF
 *  THE TOTAL. It is never shown as 0: a hidden YouTube count on a stream with an
 *  audience is not an audience of none. When nothing reported, the total
 *  itself reads "not reported". */
export function AudiencePanel({ className }: Readonly<{ className?: string }>) {
  const t = useT();
  const [live, setLive] = useState<AnalyticsLive | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let alive = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const load = () => {
      api.analytics
        .live()
        .then((l) => {
          if (!alive) return;
          setLive(l);
          setFailed(false);
          const every = Math.max(MIN_POLL_MS, Math.min((l.intervalSeconds || 60) * 1000, 60_000));
          timer = setTimeout(load, every);
        })
        .catch(() => {
          if (!alive) return;
          setFailed(true);
          timer = setTimeout(load, 60_000);
        });
    };
    load();
    return () => {
      alive = false;
      if (timer) clearTimeout(timer);
    };
  }, []);

  return (
    <Card className={className} data-testid="audience-panel">
      <CardHeader>
        <CardTitle>{t("aud.title")}</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        {failed && !live && <p className="text-sm text-muted-foreground">{t("aud.unavailable")}</p>}
        {live && !live.enabled && <p className="text-sm text-muted-foreground">{t("aud.disabled")}</p>}
        {live && live.enabled && !live.onAir && (
          <p className="text-sm text-muted-foreground">{t("aud.offAir")}</p>
        )}
        {/* On air with no sample since it began: the server checks the
            destinations when asked, so the panel no longer reads "Off air"
            for up to a minute beside a broadcast that has just started. Its
            own line rather than OnAir's "not reported / 0 of 0", which would
            say a platform was asked and did not answer. */}
        {live && live.enabled && live.onAir && live.pending && (
          <p className="text-sm text-muted-foreground" data-testid="audience-pending">
            {t("aud.pending")}
          </p>
        )}
        {live && live.enabled && live.onAir && !live.pending && <OnAir live={live} />}
      </CardContent>
    </Card>
  );
}

function OnAir({ live }: Readonly<{ live: AnalyticsLive }>) {
  const t = useT();
  const rows = orderReadings(live.readings);
  const path = sparkPath(live.points, 200, 32);
  // The player's count is in the total, once: say so, so a total that never
  // reads "not reported" while the player runs is not a surprise.
  const withPlayer = live.readings.some((r) => r.platform === PLAYER);
  return (
    <>
      <div className="flex items-baseline justify-between gap-2">
        <div className="flex flex-col">
          <span className="text-xs text-muted-foreground">
            {withPlayer ? t("aud.totalWithPlayer") : t("aud.total")}
          </span>
          {typeof live.total === "number" ? (
            <span className="text-2xl font-semibold tabular-nums text-foreground" data-testid="audience-total">
              {live.total.toLocaleString()}
            </span>
          ) : (
            <span className="text-sm text-muted-foreground" data-testid="audience-total">
              {t("aud.notReported")}
            </span>
          )}
          <span className="text-xs text-muted-foreground">
            {t("aud.reportedOf", { n: live.reported, of: live.readings.length })}
          </span>
          {live.partial && (
            <span className="text-xs text-warn" data-testid="audience-partial">
              {t("aud.partial")}
            </span>
          )}
        </div>
        {path && (
          <svg
            viewBox="0 0 200 32"
            className="h-8 w-40 overflow-visible"
            role="img"
            aria-label={t("aud.sparkline")}
            preserveAspectRatio="none"
          >
            <path d={path} className="fill-none stroke-primary" strokeWidth={1.5} vectorEffect="non-scaling-stroke" />
          </svg>
        )}
      </div>
      {rows.length === 0 && <p className="text-sm text-muted-foreground">{t("aud.noAccounts")}</p>}
      <ul className="flex flex-col gap-1" aria-label={t("aud.breakdown")}>
        {rows.map((r, i) => (
          <ReadingRow key={`${r.platform}-${r.account ?? ""}-${i}`} r={r} />
        ))}
      </ul>
      {live.player && (
        <p className="text-xs text-muted-foreground">
          {t("aud.playerQoE", {
            sessions: live.player.sessions,
            rebuffers: live.player.rebuffers,
            errors: live.player.sessionsWithErrors,
          })}
        </p>
      )}
      {live.at && (
        <p className="text-xs text-muted-foreground">{t("aud.sampledAt", { time: clockTime(live.at) })}</p>
      )}
      {live.stretched && live.effectiveIntervalSeconds && (
        <p className="text-xs text-muted-foreground" data-testid="audience-stretched">
          {t("aud.stretched", { seconds: live.effectiveIntervalSeconds })}
        </p>
      )}
    </>
  );
}

function ReadingRow({ r }: Readonly<{ r: AnalyticsReading }>) {
  const t = useT();
  const count = readingCount(r);
  const label = r.platform === PLAYER ? t("aud.player") : platformName(r.platform);
  const reason = r.reason ? REASON[r.reason] : undefined;
  return (
    <li className="flex items-center justify-between gap-2 text-sm" data-testid={`audience-row-${r.platform}`}>
      <span className="min-w-0 truncate">
        <span className="font-medium text-foreground">{label}</span>
        {r.name && <span className="text-muted-foreground"> · {r.name}</span>}
      </span>
      {count === null ? (
        <span className="text-muted-foreground" title={reason ? t(reason) : undefined}>
          {r.reason === "unlinked" ? t("aud.notConnected") : t("aud.notReported")}
        </span>
      ) : (
        <span className="tabular-nums text-foreground">{count.toLocaleString()}</span>
      )}
    </li>
  );
}
