import { Badge } from "@/components/ui/badge";
import { StatusDot } from "@/components/signature/StatusDot";
import { accentFor } from "@/lib/chat";
import { useT } from "@/lib/i18n";
import type { SignalTone } from "@/lib/signal";
import { platformName } from "@/lib/platforms";
import type { ActivitySource, ActivitySourcesResponse, ChatPlatform } from "@/lib/types";
import { kindCounts } from "./eventText";

const TONE: Record<ActivitySource["state"], SignalTone> = {
  live: "live",
  connecting: "warn",
  degraded: "warn",
  failed: "down",
  stopped: "idle",
};

function badgeVariantFor(state: ActivitySource["state"]): "live" | "down" | "warn" {
  if (state === "live") return "live";
  return state === "failed" ? "down" : "warn";
}

/** The health strip: one chip per source with its state, its kind counts and
 *  its own detail text, so "why is nothing arriving from Kick" has an answer on
 *  the page. State is a word and a dot -- never a colour alone.
 *
 *  With no sources (the normal case until a platform's source ships) it says so
 *  in one neutral line rather than showing an empty strip that reads as broken. */
export function ActivitySourceChips({
  data,
  failed,
}: Readonly<{ data: ActivitySourcesResponse | null; failed: boolean }>) {
  const t = useT();
  if (!data) {
    return failed ? <p className="px-1 text-sm text-warn">{t("activity.sources.failed")}</p> : null;
  }
  const sources = data.sources ?? [];
  return (
    <div className="flex flex-col gap-1.5" role="group" aria-label={t("activity.sources.label")}>
      {sources.length === 0 ? (
        <p className="rounded-md border border-border bg-card px-2 py-1.5 text-sm text-muted-foreground">
          {t("activity.sources.none")}
        </p>
      ) : (
        <ul className="flex flex-wrap gap-1.5">
          {sources.map((s) => {
            const accent = accentFor(s.platform as ChatPlatform);
            const Icon = accent.icon;
            const c = kindCounts(s);
            return (
              <li
                key={`${s.platform}-${s.account ?? ""}`}
                tabIndex={0}
                title={s.detail || undefined}
                data-source-state={s.state}
                className="flex max-w-full flex-col gap-0.5 rounded-md border border-border bg-card px-2 py-1 text-sm focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring"
              >
                <span className="flex flex-wrap items-center gap-1.5">
                  <StatusDot tone={TONE[s.state] ?? "idle"} />
                  <Icon className={`h-3.5 w-3.5 ${accent.text}`} aria-hidden />
                  <span className="font-semibold">{platformName(s.platform)}</span>
                  {s.account && <span className="text-muted-foreground">{s.account}</span>}
                  <Badge variant={badgeVariantFor(s.state)}>
                    {t(`activity.sourceState.${s.state}`)}
                  </Badge>
                  <span className="tnum font-mono text-muted-foreground">
                    {t("activity.sources.events", { count: s.events })}
                  </span>
                </span>
                <span className="text-muted-foreground">
                  {t("activity.sources.counts", { on: c.on, unavailable: c.unavailable, reconnect: c.reconnect })}
                </span>
                {s.detail && <span className="text-muted-foreground">{s.detail}</span>}
              </li>
            );
          })}
        </ul>
      )}
      {data.stats && (
        <p className="tnum px-1 font-mono text-sm text-muted-foreground">
          {t("activity.sources.stats", {
            received: data.stats.received,
            delivered: data.stats.delivered,
            deduped: data.stats.deduped,
          })}
        </p>
      )}
    </div>
  );
}
