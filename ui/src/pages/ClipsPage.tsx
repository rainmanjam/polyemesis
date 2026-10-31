import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import { ConfirmDestructive } from "@/components/ConfirmDestructive";
import { useConfirm } from "@/hooks/useConfirm";
import { Download, Loader2, Scissors, Trash2 } from "lucide-react";
import { EmptyState } from "@/components/EmptyState";
import { SaveStatus } from "@/components/SaveStatus";
import { StatusPill } from "@/components/StatusPill";
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
import { clipBufferStatus } from "@/lib/status";
import { useSaveModel } from "@/lib/saveModel";
import { bytes, timestamp } from "@/lib/format";
import { useT } from "@/lib/i18n";
import {
  failedRead,
  mayClaim,
  okRead,
  pendingRead,
  readFailed,
  type ReadState,
} from "@/lib/readState";
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
  /* A READ, NOT A NULLABLE VIEW. `view: ClipsView | null` stored a failed GET
   * as the same value as a page that had not answered yet, and `loading` went
   * false either way -- so a 500 on /clips drew "No clips yet." over a machine
   * that may well have had clips on it. That sentence is a positive claim: it
   * says the server answered and there is nothing there. See lib/readState.ts,
   * which exists to make exactly this unwriteable. */
  const [read, setRead] = useState<ReadState<ClipsView>>(pendingRead);
  const [capturing, setCapturing] = useState(false);
  const [custom, setCustom] = useState(20);
  // The window is the one field a 3-second poll must not overwrite (A13). It is
  // a draft over the server's window: the poll reseeds it only while clean.
  const storedWindow =
    mayClaim(read) && read.value.buffer.buffer
      ? Math.round(read.value.buffer.buffer.windowSeconds)
      : 60;
  const windowModel = useSaveModel<number>({
    stored: storedWindow,
    onSave: async (windowSeconds) => {
      try {
        const st = await autoApi.setClipBuffer<BufferStatus>(
          { enabled: true, windowSeconds },
          programme,
        );
        setRead((r) => (mayClaim(r) ? okRead({ ...r.value, buffer: st }) : r));
        await load(true);
      } catch (err) {
        toast.error(errText(err, t("clips.couldNotChangeTheClip")));
        throw err;
      }
    },
  });
  const windowSec = windowModel.draft;

  const load = useCallback(
    (quiet = false) =>
      autoApi
        .listClips<ClipsView>(programme)
        .then((v) => {
          setRead(okRead(v));
        })
        .catch((err) => {
          // The toast is still suppressed on a background poll -- a red toast
          // every three seconds is its own fault report -- but the STATE is
          // recorded either way. Silencing the notification is a choice about
          // noise; silencing the fact was the bug.
          setRead(failedRead());
          if (!quiet) toast.error(errText(err, t("clips.couldNotLoadClips")));
        }),
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

  const view = mayClaim(read) ? read.value : null;
  const failed = readFailed(read);
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
        t(res.clip.keyframeAligned ? "clips.capturedToast" : "clips.capturedRoughToast", {
          seconds: res.clip.seconds.toFixed(1),
        }),
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
      setRead((r) => (mayClaim(r) ? okRead({ ...r.value, buffer: st }) : r));
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
          <StatusPill reading={clipBufferStatus({ enabled: buffer?.enabled ?? false, running: buffer?.running ?? false })} />
        }
      />

      <div className="grid gap-3 lg:grid-cols-[minmax(0,1fr)_18rem]">
        {/* ---------- capture + clip list ---------- */}
        <div className="flex flex-col gap-3">
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-1.5">
                <Scissors className="h-3.5 w-3.5" /> {t("clips.capture")}
              </CardTitle>
            </CardHeader>
            <CardContent className="flex flex-col gap-2">
              {!buffer?.enabled ? (
                // A12: one primary action in place of presets that cannot work
                // while the buffer is off. The state is said once, here.
                <EmptyState
                  icon={Scissors}
                  title={t("clips.bufferOffTitle")}
                  body={t("clips.bufferOff")}
                  action={{
                    label: t("clips.turnOnBuffer"),
                    onClick: () => void setBuffer(true, windowSec),
                    disabled: !programmeKnown,
                    disabledReason: !programmeKnown ? t("clips.programmeUnknown") : undefined,
                  }}
                />
              ) : (
                <>
                  {buffer.running && (
                    <div className="flex flex-wrap items-center gap-1.5">
                      {QUICK_SECONDS.map((s) => (
                        <Button
                          key={s}
                          size="sm"
                          variant={s === 30 ? "default" : "secondary"}
                          disabled={!programmeKnown || capturing}
                          onClick={() => capture(s)}
                        >
                          {capturing && <Loader2 className="animate-spin" />}
                          {t("clips.lastSeconds", { n: s })}
                        </Button>
                      ))}
                      <Button
                        size="sm"
                        variant="secondary"
                        disabled={!programmeKnown || capturing}
                        onClick={() => capture(0)}
                        title={t("clips.everythingHeld")}
                      >
                        {t("clips.wholeBuffer")}
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
                      {/* A96: the unit is visible, not only in the aria-label. */}
                      <span className="text-sm text-muted-foreground" aria-hidden="true">s</span>
                      <Button
                        size="sm"
                        variant="secondary"
                        disabled={!programmeKnown || capturing || custom < 1}
                        onClick={() => capture(custom)}
                      >
                        {t("clips.capture")}
                      </Button>
                    </div>
                  )}

                  {!buffer.running ? (
                    <p className="text-tiny text-muted-foreground">
                      {t("clips.bufferIdle")}
                    </p>
                  ) : (
                    <p className="text-tiny text-muted-foreground">
                      {t("clips.holding", {
                        held: held.toFixed(0),
                        window: stats?.windowSeconds.toFixed(0) ?? "—",
                      })}
                      {/* Asking for more than has arrived is not an error — the
                          capture is simply as long as the history allows. */}
                      {" "}{t("clips.trimmedNote")}
                      {stats?.truncated && ` ${t("clips.memoryLimits")}`}
                      {stats && !stats.videoFound && ` ${t("clips.noKeyframeYet")}`}
                    </p>
                  )}
                </>
              )}
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>{t("clips.captured")}</CardTitle>
            </CardHeader>
            <CardContent className="px-0 pb-0">
              {failed ? (
                <div className="px-3 py-8 text-center text-sm text-warn">
                  {t("clips.listUnread")}
                </div>
              ) : !mayClaim(read) ? (
                <div className="flex justify-center py-8">
                  <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
                </div>
              ) : clips.length === 0 ? (
                <div className="px-3 py-8 text-center text-sm text-muted-foreground">
                  {t("clips.noneYet")}
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
                        <TableCell className="font-mono text-tiny">
                          {c.name}
                          {!c.keyframeAligned && (
                            <Badge
                              variant="warn"
                              className="ml-1.5"
                              title={c.note || t("clips.theClipDoesNotStart")}
                            >
                              {t("clips.roughStart")}
                            </Badge>
                          )}
                        </TableCell>
                        {/* StartedAt, not CreatedAt: "the clip from 20:31"
                            means when the action happened, not when the
                            operator got round to pressing the button. */}
                        <TableCell className="tnum font-mono text-tiny text-muted-foreground">
                          {timestamp(c.startedAt)}
                        </TableCell>
                        <TableCell className="tnum text-right font-mono text-tiny">
                          {c.seconds.toFixed(1)}s
                        </TableCell>
                        <TableCell className="tnum text-right font-mono text-tiny">
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
                labelKey="clips.held"
                value={stats ? `${held.toFixed(0)}s` : "—"}
                tone={held > 0 ? "live" : "muted"}
              />
              <Stat labelKey="clips.window" value={`${stats?.windowSeconds.toFixed(0) ?? "—"}s`} />
              <Stat
                labelKey="clips.inMemory"
                value={stats ? bytes(stats.bytes) : "—"}
                tone="muted"
              />
              <Stat
                labelKey="clips.ceiling"
                value={stats ? bytes(stats.maxBytes) : "—"}
                tone="muted"
              />
              <Stat
                labelKey="clips.bitrate"
                value={stats ? `${stats.bitrateKbps} kbps` : "—"}
                tone="muted"
              />
              <Stat
                labelKey="clips.keyframes"
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
                    onChange={(e) => windowModel.setDraft(Number(e.target.value))}
                  />
                  <Button
                    size="sm"
                    variant="secondary"
                    disabled={!programmeKnown || !buffer?.enabled || !windowModel.dirty}
                    onClick={() => void windowModel.save()}
                  >
                    {t("common.apply")}
                  </Button>
                </div>
                <SaveStatus state={windowModel.state} />
                {/* Apply stays greyed while the buffer is off -- there is
                    nothing to apply it to -- but the reason is now in words
                    beside it rather than nowhere, and the sentence says where
                    the number goes instead. */}
                {!buffer?.enabled && (
                  <span className="text-micro text-warn">{t("clips.windowAppliesOnEnable")}</span>
                )}
                <span className="text-micro text-muted-foreground">
                  {t("clips.windowNote", {
                    min: view?.bounds.minWindowSeconds ?? 5,
                    max: view?.bounds.maxWindowSeconds ?? 300,
                  })}
                </span>
              </div>

              <div className="grid grid-cols-2 gap-2">
                <Stat labelKey="clips.clipsKept" value={view?.usage.count ?? 0} />
                <Stat labelKey="clips.onDisk" value={bytes(view?.usage.usedBytes ?? 0)} tone="muted" />
              </div>
              <p className="text-micro text-muted-foreground">
                {t("clips.retention", {
                  count: view?.usage.maxClips ?? 0,
                  bytes: bytes(view?.usage.maxBytes ?? 0),
                })}
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
