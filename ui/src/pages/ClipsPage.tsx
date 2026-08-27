import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import { ConfirmDestructive } from "@/components/ConfirmDestructive";
import { useConfirm } from "@/hooks/useConfirm";
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
import { autoApi } from "@/lib/autoApi";
import { keyframeVerdict, windowOnBufferToggle } from "@/lib/clipBufferFacts";
import { bytes, timestamp } from "@/lib/format";
import { useT } from "@/lib/i18n";
import { useLiveData } from "@/hooks/useLiveData";

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
  const t = useT();
  // Which programme a captured clip belongs to. POST /clips and PUT
  // /clips/buffer are refused without it on any install with two.
  const { programme, programmeKnown } = useLiveData();
  const [view, setView] = useState<ClipsView | null>(null);
  const [loading, setLoading] = useState(true);
  const [capturing, setCapturing] = useState(false);
  const [windowSec, setWindow] = useState(60);
  const [custom, setCustom] = useState(20);

  const load = useCallback(
    (quiet = false) =>
      autoApi
        .listClips<ClipsView>(programme)
        .then((v) => {
          setView(v);
          if (v.buffer.buffer) setWindow(Math.round(v.buffer.buffer.windowSeconds));
        })
        .catch((err) => {
          if (!quiet) toast.error(errText(err, t("clips.couldNotLoadClips")));
        })
        .finally(() => setLoading(false)),
    // programme, because listClips is scoped and this callback closes over it.
    // Omitted, it froze at the mount value -- null -- and every poll went out
    // unscoped and took a 400 on any install with two programmes. See the same
    // mistake, and the same fix, in MetersPage and MonitoringPage.
    [t, programme],
  );

  useEffect(() => {
    if (!programmeKnown) return;
    void load();
  }, [load, t, programmeKnown]);

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
  const keyframes = keyframeVerdict(stats, buffer?.running);

  const capture = async (seconds: number) => {
    setCapturing(true);
    try {
      const res = await autoApi.captureClip<{ clip: Clip }>(seconds, programme);
      toast.success(
        `Captured ${res.clip.seconds.toFixed(1)}s${res.clip.keyframeAligned ? "" : " (not keyframe-aligned — may start with a glitch)"}.`,
      );
      await load();
    } catch (err) {
      toast.error(errText(err, t("clips.couldNotCaptureAClip")));
    } finally {
      setCapturing(false);
    }
  };

  const setBuffer = async (enabled: boolean, windowSeconds: number) => {
    try {
      const st = await autoApi.setClipBuffer<BufferStatus>(
        { enabled, windowSeconds },
        programme,
      );
      setView((v) => (v ? { ...v, buffer: st } : v));
      await load(true);
    } catch (err) {
      toast.error(errText(err, t("clips.couldNotChangeTheClip")));
    }
  };

  const confirmDelete = useConfirm<Clip>();

  const remove = async (c: Clip) => {
    try {
      await autoApi.del(`/clips/${encodeURIComponent(c.name)}`);
      toast.success(t("clips.deleted"));
      await load();
    } catch (err) {
      toast.error(errText(err, t("clips.couldNotDeleteTheClip")));
    }
  };

  return (
    <div className="p-3">
      <PageHeader
        title={t("clips.title")}
        subtitle={t("clips.subtitle")}
        actions={
          <Badge variant={buffer?.running ? "live" : buffer?.enabled ? "warn" : "outline"}>
            {buffer?.running ? "buffering" : buffer?.enabled ? t("clips.waitingForStream") : t("clips.off")}
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
                    disabled={!programmeKnown || capturing || !buffer?.running}
                    onClick={() => capture(s)}
                  >
                    {capturing && <Loader2 className="animate-spin" />}
                    Last {s}s
                  </Button>
                ))}
                <Button
                  size="sm"
                  variant="secondary"
                  disabled={!programmeKnown || capturing || !buffer?.running}
                  onClick={() => capture(0)}
                  title={t("clips.everythingHeld")}
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
                  aria-label={t("clips.customLength")}
                  onChange={(e) => setCustom(Number(e.target.value))}
                />
                <Button
                  size="sm"
                  variant="secondary"
                  disabled={!programmeKnown || capturing || !buffer?.running || custom < 1}
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
              <CardTitle>{t("clips.captured")}</CardTitle>
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
                      <TableHead>{t("clips.file")}</TableHead>
                      <TableHead>{t("clips.covers")}</TableHead>
                      <TableHead className="text-right">{t("clips.length")}</TableHead>
                      <TableHead className="text-right">{t("clips.size")}</TableHead>
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
                              title={c.note || t("clips.theClipDoesNotStart")}
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
                            <Button variant="ghost" size="icon-sm" asChild aria-label={t("clips.download")}>
                              <a href={clipDownloadUrl(c.name)} download>
                                <Download />
                              </a>
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon-sm"
                              onClick={() => confirmDelete.ask(c)}
                              aria-label={t("clips.delete")}
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
              <CardTitle>{t("clips.buffer")}</CardTitle>
            </CardHeader>
            {/* Every cell follows the Window cell's `stats ? … : "—"`.
                Five of the six used to print a zero through `?? 0` with no
                buffer to have measured it, and two of those zeros are not even
                possible readings: the Ceiling is a configured constant that is
                never zero, and Bitrate 0 kbps beside "Keyframes: none" in amber
                describes an encoder fault on a stream nobody examined. A zero
                that means "not measured" must not be drawn as a zero that means
                "none". */}
            <CardContent className="grid grid-cols-2 gap-2">
              <Stat
                label={t("clips.held")}
                value={stats ? `${held.toFixed(0)}s` : "—"}
                tone={held > 0 ? "live" : "muted"}
              />
              <Stat label={t("clips.window")} value={`${stats?.windowSeconds.toFixed(0) ?? "—"}s`} />
              <Stat
                label={t("clips.inMemory")}
                value={stats ? bytes(stats.bytes) : "—"}
                tone="muted"
              />
              <Stat
                label={t("clips.ceiling")}
                value={stats ? bytes(stats.maxBytes) : "—"}
                tone="muted"
              />
              <Stat
                label={t("clips.bitrate")}
                value={stats ? `${stats.bitrateKbps} kbps` : "—"}
                tone="muted"
              />
              <Stat
                label={t("clips.keyframes")}
                value={
                  keyframes.verdict === "unknown"
                    ? "—"
                    : keyframes.verdict === "seen"
                      ? t("clips.seen")
                      : t("clips.none")
                }
                tone={keyframes.warn ? "warn" : "muted"}
              />
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>{t("clips.settings")}</CardTitle>
            </CardHeader>
            <CardContent className="flex flex-col gap-3">
              <div className="flex items-center justify-between">
                <Label htmlFor="clip-enabled">{t("clips.keepBuffer")}</Label>
                {/* The typed window rides along with the enable. Sending a
                    literal 0 here meant "keep the current window", so the
                    number the operator had just typed -- with Apply greyed out
                    because the buffer was off -- was thrown away by the click
                    that started the buffer, and overwritten in the input by the
                    3s poll. */}
                <Switch
                  id="clip-enabled"
                  checked={buffer?.enabled ?? false}
                  onCheckedChange={(v) => setBuffer(v, windowOnBufferToggle(v, windowSec))}
                />
              </div>

              <div className="flex flex-col gap-1">
                <Label htmlFor="clip-window">{t("clips.windowSeconds")}</Label>
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
                    disabled={!programmeKnown || !buffer?.enabled}
                    onClick={() => setBuffer(true, windowSec)}
                  >
                    Apply
                  </Button>
                </div>
                {/* Apply stays greyed while the buffer is off -- there is
                    nothing to apply it to -- but the reason is now in words
                    beside it rather than nowhere, and the sentence says where
                    the number goes instead. */}
                {!buffer?.enabled && (
                  <span className="text-[10px] text-warn">{t("clips.windowAppliesOnEnable")}</span>
                )}
                <span className="text-[10px] text-muted-foreground">
                  {view?.bounds.minWindowSeconds ?? 5}–{view?.bounds.maxWindowSeconds ?? 300}s. A
                  longer window costs memory proportional to the stream's bitrate.
                </span>
              </div>

              <div className="grid grid-cols-2 gap-2">
                <Stat label={t("clips.clipsKept")} value={view?.usage.count ?? 0} />
                <Stat label={t("clips.onDisk")} value={bytes(view?.usage.usedBytes ?? 0)} tone="muted" />
              </div>
              <p className="text-[10px] text-muted-foreground">
                Retention keeps at most {view?.usage.maxClips ?? 0} clips and{" "}
                {bytes(view?.usage.maxBytes ?? 0)}; the oldest go first. Clips are MPEG-TS, which
                every editor and every player opens.
              </p>
              <p className="text-[10px] text-subtle-foreground">
            {t("clips.bufferLiveNote")}
              </p>
            </CardContent>
          </Card>
        </div>
      </div>

      <ConfirmDestructive
        open={confirmDelete.open}
        onOpenChange={confirmDelete.onOpenChange}
        subject={confirmDelete.target?.name ?? ""}
        title={t("clips.deleteTitle")}
        description={t("clips.deleteDescription")}
        requireTyping
        onConfirm={async () => {
          if (confirmDelete.target) await remove(confirmDelete.target);
        }}
      />
    </div>
  );
}
