import { AlertTriangle } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { PageHeader } from "@/components/AppLayout";
import { AudioMeter, MeterScale, channelLabels } from "@/components/signature/AudioMeter";
import { useLiveData, useSourceTracks } from "@/hooks/useLiveData";
import { db } from "@/lib/format";
import { cn } from "@/lib/utils";

/** Live level meters for every channel of every ingest track.
 *
 *  This page is how a streamer verifies the clean track really is clean:
 *  play the music, watch track 1 move and track 2 stay flat. It is the only
 *  way to be certain before going live, so it gets the whole width. */
export function MetersPage() {
  const { levels, source, status } = useLiveData();
  const tracks = useSourceTracks();
  const probed = source?.probed ?? false;
  const metersRunning = status?.meters?.state === "running";

  return (
    <div className="p-3">
      <PageHeader
        title="Audio meters"
        subtitle="Every channel of every ingest track, straight from the relay."
        actions={
          <Badge variant={metersRunning ? "live" : "outline"}>
            {metersRunning ? "metering" : "idle"}
          </Badge>
        }
      />

      {!probed && (
        <Card className="mb-3">
          <CardContent className="flex items-start gap-2 py-3">
            <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-warn" />
            <div className="text-[11px] text-muted-foreground">
              No stream is arriving. Point your encoder at the ingest URL shown on the dashboard;
              the track layout and meters appear automatically.
            </div>
          </CardContent>
        </Card>
      )}

      <div className="grid gap-3 xl:grid-cols-2">
        {tracks.map((t) => {
          const peak = levels?.peak?.[t.index] ?? [];
          const rms = levels?.rms?.[t.index] ?? [];
          const labels = channelLabels(t.channels);
          const active = peak.some((p) => p > -100);
          const hottest = peak.length ? Math.max(...peak) : -100;
          const clipping = hottest >= -0.2;

          return (
            <Card key={t.index} className={cn(clipping && "border-down/50")}>
              <CardHeader className="flex-row items-center justify-between">
                <CardTitle className="flex items-center gap-2">
                  Track {t.index + 1}
                  <span className="font-mono text-[10px] font-normal text-muted-foreground">
                    {t.layout} · {t.codec}
                  </span>
                  {t.title && (
                    <span className="truncate text-[10px] font-normal text-muted-foreground">
                      {t.title}
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
                    {probed ? "no signal on this track" : "waiting for stream"}
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
