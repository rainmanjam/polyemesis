import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import { Download, Loader2, Scissors, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { PageHeader } from "@/components/AppLayout";
import { Stat } from "@/components/signature/Stat";
import { autoApi } from "@/pages/AutomationPage";
import { bytes, timestamp } from "@/lib/format";

interface Clip {
  name: string;
  bytes: number;
  seconds: number;
  startedAt: string;
  createdAt: string;
  keyframeAligned: boolean;
  note?: string;
}

interface ClipUsage {
  count: number;
  usedBytes: number;
  maxBytes: number;
  maxClips: number;
}

interface BufferStats {
  windowSeconds: number;
  maxBytes: number;
  bytes: number;
  packets: number;
  seconds: number;
  bitrateKbps: number;
  truncated: boolean;
  datagrams: number;
  evicted: number;
  oversized: number;
  videoFound: boolean;
}

interface BufferStatus {
  enabled: boolean;
  running: boolean;
  buffer?: BufferStats | null;
  dir: string;
}

interface ClipsView {
  clips: Clip[] | null;
  usage: ClipUsage;
  buffer: BufferStatus;
  bounds: { minWindowSeconds: number; maxWindowSeconds: number };
}

/** Common clip lengths. The buffer holds the last minute or so, so these are
 *  "what just happened" durations rather than editing decisions. */
const QUICK_SECONDS = [15, 30, 60];

const errText = (err: unknown, fallback: string) =>
  err instanceof Error && err.message ? err.message : fallback;

const clipDownloadUrl = (name: string) =>
  `/api/v1/clips/${encodeURIComponent(name)}/download`;

/** Instant replay for the operator: everything that arrived in the last N
 *  seconds is already in memory, so capturing it is a copy rather than a
 *  recording somebody has to have started in advance. */
export function ClipsPage() {
  const [view, setView] = useState<ClipsView | null>(null);
  const [loading, setLoading] = useState(true);
  const [capturing, setCapturing] = useState(false);
  const [windowSec, setWindow] = useState(60);
  const [custom, setCustom] = useState(20);

  const load = useCallback(
    (quiet = false) =>
      autoApi
        .get<ClipsView>("/clips")
        .then((v) => {
          setView(v);
          if (v.buffer.buffer) setWindow(Math.round(v.buffer.buffer.windowSeconds));
        })
        .catch((err) => {
          if (!quiet) toast.error(errText(err, "Could not load clips."));
        })
        .finally(() => setLoading(false)),
    [],
  );

  useEffect(() => {
    void load();
  }, [load]);

  // The buffer fills in real time and its depth is the only thing that says
  // whether a clip taken right now would be the full length asked for.
  useEffect(() => {
    const t = window.setInterval(() => void load(true), 3000);
    return () => window.clearInterval(t);
  }, [load]);

  const buffer = view?.buffer;
  const stats = buffer?.buffer ?? null;
  const clips = view?.clips ?? [];
  const held = stats?.seconds ?? 0;

  const capture = async (seconds: number) => {
    setCapturing(true);
    try {
      const res = await autoApi.post<{ clip: Clip }>("/clips", { seconds });
      toast.success(
        `Captured ${res.clip.seconds.toFixed(1)}s${res.clip.keyframeAligned ? "" : " (not keyframe-aligned — may start with a glitch)"}.`,
      );
      await load();
    } catch (err) {
      toast.error(errText(err, "Could not capture a clip."));
    } finally {
      setCapturing(false);
    }
  };

  const setBuffer = async (enabled: boolean, windowSeconds: number) => {
    try {
      const st = await autoApi.put<BufferStatus>("/clips/buffer", { enabled, windowSeconds });
      setView((v) => (v ? { ...v, buffer: st } : v));
      await load(true);
    } catch (err) {
      toast.error(errText(err, "Could not change the clip buffer."));
    }
  };

  const remove = async (c: Clip) => {
    if (!window.confirm(`Delete ${c.name}? This permanently removes the file.`)) return;
    try {
      await autoApi.del(`/clips/${encodeURIComponent(c.name)}`);
      toast.success("Clip deleted.");
      await load();
    } catch (err) {
      toast.error(errText(err, "Could not delete the clip."));
    }
  };

  return (
    <div className="p-3">
      <PageHeader
        title="Clips"
        subtitle="A rolling buffer of what just went out. Capture it after the fact — nothing has to be armed in advance."
        actions={
          <Badge variant={buffer?.running ? "live" : buffer?.enabled ? "warn" : "outline"}>
            {buffer?.running ? "buffering" : buffer?.enabled ? "waiting for stream" : "off"}
          </Badge>
        }
      />

      <div className="grid gap-3 lg:grid-cols-[minmax(0,1fr)_18rem]">
        {/* ---------- capture + clip list ---------- */}
        <div className="flex flex-col gap-3">
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-1.5">
                <Scissors className="h-3.5 w-3.5" /> Capture
              </CardTitle>
            </CardHeader>
            <CardContent className="flex flex-col gap-2">
              <div className="flex flex-wrap items-center gap-1.5">
                {QUICK_SECONDS.map((s) => (
                  <Button
                    key={s}
                    size="sm"
                    variant={s === 30 ? "default" : "secondary"}
                    disabled={capturing || !buffer?.running}
                    onClick={() => capture(s)}
                  >
                    {capturing && <Loader2 className="animate-spin" />}
                    Last {s}s
                  </Button>
                ))}
                <Button
                  size="sm"
                  variant="secondary"
                  disabled={capturing || !buffer?.running}
                  onClick={() => capture(0)}
                  title="Everything the buffer is holding"
                >
                  Whole buffer
                </Button>
                <span className="mx-1 h-5 w-px bg-border" />
                <Input
                  type="number"
                  min={1}
                  max={view?.bounds.maxWindowSeconds ?? 300}
                  value={custom}
                  className="w-20"
                  aria-label="Custom clip length in seconds"
                  onChange={(e) => setCustom(Number(e.target.value))}
                />
                <Button
                  size="sm"
                  variant="secondary"
                  disabled={capturing || !buffer?.running || custom < 1}
                  onClick={() => capture(custom)}
                >
                  Capture
                </Button>
              </div>

              {!buffer?.enabled ? (
                <p className="text-[11px] text-muted-foreground">
                  The buffer is off. Turn it on to the right; it costs memory, not disk, until you
                  capture something.
                </p>
              ) : !buffer.running ? (
                <p className="text-[11px] text-muted-foreground">
                  Nothing is arriving yet. The buffer starts filling as soon as a stream is live.
                </p>
              ) : (
                <p className="text-[11px] text-muted-foreground">
                  Holding {held.toFixed(0)}s of the last {stats?.windowSeconds.toFixed(0) ?? "—"}s.
                  {/* Asking for more than has arrived is not an error — the
                      capture is simply as long as the history allows. */}
                  {" "}A longer request is trimmed to what is there.
                  {stats?.truncated &&
                    " The memory ceiling, not the window, is what limits it right now."}
                  {stats && !stats.videoFound &&
                    " No keyframe has been seen yet, so a clip may start with a glitch."}
                </p>
              )}
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>Captured</CardTitle>
            </CardHeader>
            <CardContent className="px-0 pb-0">
              {loading ? (
                <div className="flex justify-center py-8">
                  <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
                </div>
              ) : clips.length === 0 ? (
                <div className="px-3 py-8 text-center text-[12px] text-muted-foreground">
                  No clips yet.
                </div>
              ) : (
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>File</TableHead>
                      <TableHead>Covers</TableHead>
                      <TableHead className="text-right">Length</TableHead>
                      <TableHead className="text-right">Size</TableHead>
                      <TableHead className="w-20" />
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {clips.map((c) => (
                      <TableRow key={c.name}>
                        <TableCell className="font-mono text-[11px]">
                          {c.name}
                          {!c.keyframeAligned && (
                            <Badge
                              variant="warn"
                              className="ml-1.5"
                              title={c.note || "The clip does not start on a keyframe."}
                            >
                              rough start
                            </Badge>
                          )}
                        </TableCell>
                        {/* StartedAt, not CreatedAt: "the clip from 20:31"
                            means when the action happened, not when the
                            operator got round to pressing the button. */}
                        <TableCell className="tnum font-mono text-[11px] text-muted-foreground">
                          {timestamp(c.startedAt)}
                        </TableCell>
                        <TableCell className="tnum text-right font-mono text-[11px]">
                          {c.seconds.toFixed(1)}s
                        </TableCell>
                        <TableCell className="tnum text-right font-mono text-[11px]">
                          {bytes(c.bytes)}
                        </TableCell>
                        <TableCell>
                          <div className="flex justify-end gap-0.5">
                            <Button variant="ghost" size="icon-sm" asChild aria-label="Download">
                              <a href={clipDownloadUrl(c.name)} download>
                                <Download />
                              </a>
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon-sm"
                              onClick={() => remove(c)}
                              aria-label="Delete"
                              className="text-muted-foreground hover:text-down"
                            >
                              <Trash2 />
                            </Button>
                          </div>
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              )}
            </CardContent>
          </Card>
        </div>

        {/* ---------- buffer state + settings ---------- */}
        <div className="flex flex-col gap-3">
          <Card>
            <CardHeader>
              <CardTitle>Buffer</CardTitle>
            </CardHeader>
            <CardContent className="grid grid-cols-2 gap-2">
              <Stat label="Held" value={`${held.toFixed(0)}s`} tone={held > 0 ? "live" : "muted"} />
              <Stat label="Window" value={`${stats?.windowSeconds.toFixed(0) ?? "—"}s`} />
              <Stat label="In memory" value={bytes(stats?.bytes ?? 0)} tone="muted" />
              <Stat label="Ceiling" value={bytes(stats?.maxBytes ?? 0)} tone="muted" />
              <Stat label="Bitrate" value={`${stats?.bitrateKbps ?? 0} kbps`} tone="muted" />
              <Stat
                label="Keyframes"
                value={stats?.videoFound ? "seen" : "none"}
                tone={stats?.videoFound ? "default" : "warn"}
              />
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>Settings</CardTitle>
            </CardHeader>
            <CardContent className="flex flex-col gap-3">
              <div className="flex items-center justify-between">
                <Label htmlFor="clip-enabled">Keep a rolling buffer</Label>
                <Switch
                  id="clip-enabled"
                  checked={buffer?.enabled ?? false}
                  onCheckedChange={(v) => setBuffer(v, 0)}
                />
              </div>

              <div className="flex flex-col gap-1">
                <Label htmlFor="clip-window">Window (seconds)</Label>
                <div className="flex gap-1.5">
                  <Input
                    id="clip-window"
                    type="number"
                    min={view?.bounds.minWindowSeconds ?? 5}
                    max={view?.bounds.maxWindowSeconds ?? 300}
                    value={windowSec}
                    onChange={(e) => setWindow(Number(e.target.value))}
                  />
                  <Button
                    size="sm"
                    variant="secondary"
                    disabled={!buffer?.enabled}
                    onClick={() => setBuffer(true, windowSec)}
                  >
                    Apply
                  </Button>
                </div>
                <span className="text-[10px] text-muted-foreground">
                  {view?.bounds.minWindowSeconds ?? 5}–{view?.bounds.maxWindowSeconds ?? 300}s. A
                  longer window costs memory proportional to the stream's bitrate.
                </span>
              </div>

              <div className="grid grid-cols-2 gap-2">
                <Stat label="Clips kept" value={view?.usage.count ?? 0} />
                <Stat label="On disk" value={bytes(view?.usage.usedBytes ?? 0)} tone="muted" />
              </div>
              <p className="text-[10px] text-muted-foreground">
                Retention keeps at most {view?.usage.maxClips ?? 0} clips and{" "}
                {bytes(view?.usage.maxBytes ?? 0)}; the oldest go first. Clips are MPEG-TS, which
                every editor and every player opens.
              </p>
              <p className="text-[10px] text-subtle-foreground">
                The buffer switch is a live toggle, not a saved setting: it is off again after a
                restart.
              </p>
            </CardContent>
          </Card>
        </div>
      </div>
    </div>
  );
}
