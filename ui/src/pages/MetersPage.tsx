import { useEffect, useRef, useState } from "react";
import { AlertTriangle } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { Label } from "@/components/ui/label";
import { PageHeader } from "@/components/AppLayout";
import { AudioMeter, MeterScale } from "@/components/signature/AudioMeter";
import { channelLabels } from "@/lib/channels";
import { Stat } from "@/components/signature/Stat";
import { useLiveData, useSourceTracks, useStaleTracker } from "@/hooks/useLiveData";
import { autoApi } from "@/lib/autoApi";
import { db } from "@/lib/format";
import { loudnessMeasured } from "@/lib/meterFacts";
import { toneBadge, type SignalTone } from "@/lib/signal";
import { cn } from "@/lib/utils";
import { useT } from "@/lib/i18n";

type Verdict = "unknown" | "pass" | "warn" | "fail";

/** The EBU R128 reading for one destination — measured after routing, so it is
 *  what the platform on the other end actually receives. */
interface LoudnessReport {
  destinationId: number;
  destination: string;
  seconds: number;
  momentaryLufs: number;
  shortTermLufs: number;
  integratedLufs: number;
  rangeLu: number;
  truePeakDbtp: number;
  integrated: boolean;
  target: {
    lufs: number;
    truePeakDbtp: number;
    toleranceLu: number;
    source: "none" | "profile" | "platform";
    reason?: string;
  };
  verdict: Verdict;
  deviationLu: number;
  reason: string;
  at: string;
  error?: string;
}

interface LoudnessView {
  /** Whether the analyser tier is running at all: the operator's monitor
   *  override AND the meters switch it follows. The page has no way to know
   *  this on its own, which is why it used to assume it. */
  enabled?: boolean;
  reports: LoudnessReport[];
  bounds: {
    toleranceLu: number;
    warnToleranceLu: number;
    minIntegrationSeconds: number;
    truePeakFailOverDb: number;
  };
}

/** Compliance uses the same signal language as everything else on the page, so
 *  "green means fine" survives the trip from a process pill to a loudness
 *  badge. Unknown is idle, not a failure: no target, or not enough programme
 *  yet, and a monitor that renders those as red teaches operators to ignore it. */
const VERDICT_TONE: Record<Verdict, SignalTone> = {
  unknown: "idle",
  pass: "live",
  warn: "warn",
  fail: "down",
};

/** The same verdict expressed in Stat's narrower vocabulary. */
const VERDICT_STAT: Record<Verdict, "default" | "live" | "warn" | "down" | "muted"> = {
  unknown: "muted",
  pass: "live",
  warn: "warn",
  fail: "down",
};

/** LUFS with one decimal; the analyser's floor reads as a dash rather than as
 *  a -70 that looks like a measurement. */
function lufs(v: number): string {
  if (!Number.isFinite(v) || v <= -70) return "—";
  return v.toFixed(1);
}

function dbtp(v: number): string {
  if (!Number.isFinite(v) || v <= -120) return "—";
  return v.toFixed(1);
}

function signed(v: number): string {
  if (!Number.isFinite(v)) return "—";
  return `${v > 0 ? "+" : ""}${v.toFixed(1)}`;
}

/** Live level meters for every channel of every ingest track.
 *
 *  This page is how a streamer verifies the clean track really is clean:
 *  play the music, watch track 1 move and track 2 stay flat. It is the only
 *  way to be certain before going live, so it gets the whole width. */
export function MetersPage() {
  const t = useT();
  const { levels, source, status, programme, programmeKnown } = useLiveData();
  const tracks = useSourceTracks();
  const probed = source?.probed ?? false;
  const metersRunning = status?.meters?.state === "running";
  const [loudness, setLoudness] = useState<LoudnessView | null>(null);
  /* null until the server has said. The switch used to be seeded `true` and
     never seeded again, so every remount drew it ON -- over a monitor the
     operator had switched off, and above a sentence explaining the empty list
     as "Nothing to measure yet. Each running destination gets its own EBU R128
     analyser", which is a claim about a tier that was not running.

     A CONTROL: the page cannot state a setting it has not been told, so the
     switch is indeterminate until the first read answers. */
  const [monitorOn, setMonitorOn] = useState<boolean | null>(null);
  /* True from the operator's click until the server has answered. A poll
     already in flight carries the state from BEFORE the click, and letting it
     land flips the switch back under their finger. */
  const toggling = useRef(false);

  /* A failing poll is silent while it might recover and explicit once it has
     not -- the same tracker MonitoringPage uses on its process list. Without
     it the last verdicts sat on screen for ever with no clock and no "as of",
     and a stale pass reads exactly like a live one. */
  const freshness = useStaleTracker();

  // Polled rather than pushed: the analyser publishes once a second, and a
  // second socket subscription for one card is not worth the wiring.
  useEffect(() => {
    const read = () =>
      autoApi
        .loudness<LoudnessView>(programme)
        .then((v) => {
          setLoudness(v);
          if (!toggling.current) setMonitorOn(v.enabled ?? null);
          freshness.ok();
        })
        .catch(freshness.failed);
    void read();
    const t = window.setInterval(read, 2000);
    return () => window.clearInterval(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const reports = loudness?.reports ?? [];

  const toggleMonitor = async (on: boolean) => {
    toggling.current = true;
    setMonitorOn(on);
    try {
      const res = await autoApi.setLoudnessMonitor<{ enabled: boolean }>(on, programme);
      setMonitorOn(res?.enabled ?? on);
    } catch {
      setMonitorOn(!on);
    } finally {
      toggling.current = false;
    }
  };

  return (
    <div className="p-3">
      <PageHeader
        title={t("meters.title")}
        subtitle={t("meters.subtitle")}
        actions={
          <Badge variant={metersRunning ? "live" : "outline"}>
            {metersRunning ? t("meters.metering") : t("dash.idle")}
          </Badge>
        }
      />

      {!probed && (
        <Card className="mb-3">
          <CardContent className="flex items-start gap-2 py-3">
            <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-warn" />
            <div className="text-[11px] text-muted-foreground">
              No stream is arriving. Point your encoder at the publish URL on the Sources page;
              the track layout and meters appear automatically.
            </div>
          </CardContent>
        </Card>
      )}

      {/* ---- compliance: what each destination is actually delivering ----
          Placed above the per-track meters on purpose. The meters answer "is
          the mix right"; this answers "will the platform turn me down", and
          only one of those is a number somebody else acts on. */}
      <Card className="mb-3">
        <CardHeader className="flex-row items-center justify-between">
          <CardTitle className="flex items-center gap-2">
            {t("meters.loudness")}
            {freshness.stale && (
              <Badge variant="warn" title={t("meters.notUpdatingTitle", { count: freshness.failures })}>
                {t("meters.notUpdating")}
              </Badge>
            )}
          </CardTitle>
          <div className="flex items-center gap-2">
            <Label htmlFor="loud-monitor" className="text-[10px] text-muted-foreground">
              Monitor
            </Label>
            {/* Disabled rather than guessed while the state is unknown: a
                switch drawn in a position nobody chose is a lie one click
                deep. */}
            <Switch
              id="loud-monitor"
              checked={monitorOn ?? false}
              // Also while the programme is unresolved: PUT /loudness is scoped, and
              // firing it unnamed on a multi-programme install is a 400 the operator
              // reads as a broken switch. See #606.
              disabled={monitorOn === null || !programmeKnown}
              onCheckedChange={toggleMonitor}
            />
          </div>
        </CardHeader>
        <CardContent className="flex flex-col gap-2">
          {reports.length === 0 ? (
            <p className="text-[11px] text-muted-foreground">
              {monitorOn === null
                ? t("meters.monitorUnknown")
                : monitorOn
                  ? t("meters.nothingToMeasure")
                  : t("meters.monitorOff")}
            </p>
          ) : (
            reports.map((rep) => (
              <ComplianceRow
                key={rep.destinationId}
                report={rep}
                truePeakFailOverDb={loudness?.bounds.truePeakFailOverDb ?? 1}
              />
            ))
          )}
          <p className="text-[10px] text-subtle-foreground">
            Integrated loudness is the figure a platform normalizes against, and it needs about{" "}
            {loudness?.bounds.minIntegrationSeconds ?? 20}s of programme before it means anything.
            Until then the verdict is unknown rather than a pass.
          </p>
        </CardContent>
      </Card>

      <div className="grid gap-3 xl:grid-cols-2">
        {tracks.map((track) => {
          const peak = levels?.peak?.[track.index] ?? [];
          const rms = levels?.rms?.[track.index] ?? [];
          const labels = channelLabels(track.channels);
          const active = peak.some((p) => p > -100);
          const hottest = peak.length ? Math.max(...peak) : -100;
          const clipping = hottest >= -0.2;

          return (
            <Card key={track.index} className={cn(clipping && "border-down/50")}>
              <CardHeader className="flex-row items-center justify-between">
                <CardTitle className="flex items-center gap-2">
                  Track {track.index + 1}
                  <span className="font-mono text-[10px] font-normal text-muted-foreground">
                    {track.layout} · {track.codec}
                  </span>
                  {track.title && (
                    <span className="truncate text-[10px] font-normal text-muted-foreground">
                      {track.title}
                    </span>
                  )}
                </CardTitle>
                <div className="flex items-center gap-1.5">
                  {clipping && <Badge variant="down">clip</Badge>}
                  <span
                    className={cn(
                      "tnum font-mono text-[11px]",
                      active ? "text-foreground" : "text-subtle-foreground",
                    )}
                  >
                    {db(hottest)} dBFS
                  </span>
                </div>
              </CardHeader>
              <CardContent className="flex flex-col gap-1">
                {peak.length > 0 ? (
                  <>
                    <AudioMeter
                      rms={rms}
                      peak={peak}
                      labels={labels}
                      barHeight={12}
                      barGap={3}
                    />
                    {/* The scale sits under the bars and is indented to match
                        the label gutter, so ticks line up with the meters. */}
                    <div className="pl-[calc(1.25rem+0.5rem)]">
                      <MeterScale />
                    </div>
                    <div className="mt-1 grid grid-cols-2 gap-x-4 gap-y-0.5 pl-[calc(1.25rem+0.5rem)] sm:grid-cols-3">
                      {labels.map((l, i) => (
                        <div key={l} className="flex items-baseline justify-between">
                          <span className="font-mono text-[10px] text-muted-foreground">{l}</span>
                          <span className="tnum font-mono text-[10px]">
                            {db(rms[i] ?? -100)}
                            <span className="text-subtle-foreground"> / </span>
                            <span className={cn((peak[i] ?? -100) >= -0.2 && "text-down")}>
                              {db(peak[i] ?? -100)}
                            </span>
                          </span>
                        </div>
                      ))}
                    </div>
                    <div className="pl-[calc(1.25rem+0.5rem)] text-[9px] uppercase tracking-wider text-subtle-foreground">
                      rms / peak dBFS
                    </div>
                  </>
                ) : (
                  <div className="py-3 text-center font-mono text-[11px] text-subtle-foreground">
                    {probed ? t("meters.noSignal") : t("clips.waitingForStream")}
                  </div>
                )}
              </CardContent>
            </Card>
          );
        })}
      </div>
    </div>
  );
}

/** One destination's compliance readout.
 *
 *  Its own component because the numbers are only half the story: which of them
 *  are trustworthy depends on whether a target exists and whether enough
 *  programme has been measured, and that logic does not belong inlined in a
 *  page that is otherwise about bars moving. */
export function ComplianceRow({
  report,
  truePeakFailOverDb,
}: {
  report: LoudnessReport;
  truePeakFailOverDb: number;
}) {
  const t = useT();
  const tone = VERDICT_TONE[report.verdict] ?? "idle";
  const targeted = report.target.source !== "none";
  /* Nothing has gone through the analyser yet: every float is still at its
     zero value, and zero is the loudest reading on both of these scales. */
  const measured = loudnessMeasured(report);
  const peakOver =
    measured &&
    targeted &&
    report.truePeakDbtp > report.target.truePeakDbtp + truePeakFailOverDb;

  return (
    <div className="flex flex-col gap-1.5 border-b border-border pb-2 last:border-0 last:pb-0">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-[12px]">{report.destination}</span>
        <Badge variant={toneBadge[tone]} title={report.reason}>
          {report.error ? "analyser failed" : report.verdict}
        </Badge>
        {targeted && (
          <span className="text-[10px] text-muted-foreground" title={report.target.reason}>
            target {report.target.lufs.toFixed(0)} LUFS ±{report.target.toleranceLu.toFixed(0)} LU,
            true peak {report.target.truePeakDbtp.toFixed(1)} dBTP
          </span>
        )}
      </div>

      {report.error ? (
        /* A destination whose meter is broken must never read as a destination
           that is compliant, so the numbers are replaced rather than left
           showing the last good frame. */
        <p className="flex items-start gap-1.5 text-[11px] text-down">
          <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
          {report.error}
        </p>
      ) : (
        <>
          <div className="grid grid-cols-3 gap-2 sm:grid-cols-6">
            {/* Every one of these is a dash until something has been measured,
                not just the two that happened to be guarded. */}
            <Stat
              label={t("meters.momentary")}
              value={measured ? lufs(report.momentaryLufs) : "—"}
              unit="LUFS"
            />
            <Stat
              label={t("meters.shortTerm")}
              value={measured ? lufs(report.shortTermLufs) : "—"}
              unit="LUFS"
            />
            {/* The only figure a platform normalizes against, so it carries the
                verdict's colour and the others stay neutral. */}
            <Stat
              label={t("meters.integrated")}
              value={measured && report.integrated ? lufs(report.integratedLufs) : "—"}
              unit="LUFS"
              tone={VERDICT_STAT[report.verdict] ?? "muted"}
            />
            <Stat
              label={t("meters.deviation")}
              value={measured && targeted && report.integrated ? signed(report.deviationLu) : "—"}
              unit="LU"
              tone="muted"
            />
            <Stat
              label={t("meters.range")}
              value={measured ? lufs(report.rangeLu) : "—"}
              unit="LU"
              tone="muted"
            />
            <Stat
              label={t("meters.truePeak")}
              value={measured ? dbtp(report.truePeakDbtp) : "—"}
              unit="dBTP"
              tone={peakOver ? "down" : "default"}
            />
          </div>
          <span className="text-[10px] text-subtle-foreground">{report.reason}</span>
        </>
      )}
    </div>
  );
}
