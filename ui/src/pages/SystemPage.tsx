import { useEffect, useState } from "react";
import { PageHeader } from "@/components/AppLayout";
import { DebugSettings } from "@/components/DebugSettings";
import { McpStatusLine } from "@/components/McpAccessCard";
import { StatusPill } from "@/components/StatusPill";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { useLiveData } from "@/hooks/useLiveData";
import { api } from "@/lib/api";
import { duration, pct } from "@/lib/format";
import { useT } from "@/lib/i18n";
import { processStatus } from "@/lib/status";

/* THE SYSTEM PAGE (A17, A74). Debug recording used to be the fifth Settings tab,
 * beside Security, for a control reached a few times a year when something
 * has already gone wrong. Settings is where an operator configures; this is
 * where they look at the machine. /settings?tab=debug still lands here, so a
 * bookmarked link or a support thread's instructions keep working.
 *
 * It leads with the machine's own status (A74): the version, the ingest state
 * and how long it has been up, and the host load. Those come from data the
 * console already reads. Disk and last restart are not here, because neither is
 * on the wire yet, and inventing a reading for them would be worse than none.
 * The debug recorder sits under Support tools, for the support case it is for. */
export function SystemPage() {
  const t = useT();
  const { status, system } = useLiveData();
  const [version, setVersion] = useState("");

  useEffect(() => {
    api
      .version()
      .then((v) => setVersion(v.version))
      .catch(() => setVersion(""));
  }, []);

  const ingest = status?.ingest ?? null;

  return (
    <div className="p-3">
      <PageHeader title={t("nav.system")} subtitle={t("system.subtitle")} />
      <div className="flex max-w-3xl flex-col gap-3">
        <Card>
          <CardHeader>
            <CardTitle>{t("system.statusTitle")}</CardTitle>
          </CardHeader>
          <CardContent className="grid gap-3 sm:grid-cols-2">
            <Fact label={t("system.version")} value={version || "—"} />
            <div className="flex flex-col gap-1">
              <span className="text-sm text-muted-foreground">{t("chrome.ingest")}</span>
              <div className="flex flex-wrap items-center gap-2">
                <StatusPill reading={processStatus(ingest?.state)} />
                {ingest && ingest.uptimeSec > 0 && (
                  <span className="tnum text-sm text-muted-foreground">
                    {t("mon.uptime")} {duration(ingest.uptimeSec)}
                  </span>
                )}
              </div>
            </div>
            <Fact label={t("mon.hostCpu")} value={system ? pct(system.cpuPercent) : "—"} />
            <Fact label={t("mon.memory")} value={system ? pct(system.memPercent) : "—"} />
          </CardContent>
        </Card>

        {/* Whether an AI assistant can reach this server, at a glance: the
            same line the Automation card shows, linking to it. */}
        <McpStatusLine />

        <section aria-labelledby="support-tools" className="flex flex-col gap-3">
          <h2 id="support-tools" className="text-base font-semibold">
            {t("system.supportTools")}
          </h2>
          <DebugSettings />
        </section>
      </div>
    </div>
  );
}

function Fact({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex flex-col gap-1">
      <span className="text-sm text-muted-foreground">{label}</span>
      <span className="tnum font-mono text-sm text-foreground">{value}</span>
    </div>
  );
}
