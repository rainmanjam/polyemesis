import { useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import {
  Cpu,
  Layers,
  Loader2,
  Pencil,
  Plus,
  RotateCw,
  ScanSearch,
  ShieldAlert,
  Trash2,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { AnchorGrid } from "@/components/AnchorGrid";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { PageHeader } from "@/components/AppLayout";
import { Experimental } from "@/components/Experimental";
import { StatusDot } from "@/components/signature/StatusDot";
import { Stat } from "@/components/signature/Stat";
import { useLiveData } from "@/hooks/useLiveData";
import { api } from "@/lib/api";
import { duration, kbps } from "@/lib/format";
import { cn } from "@/lib/utils";
import { toneBadge, toneForState, type SignalTone } from "@/lib/signal";
import { deleteConsequence } from "@/lib/renditionUsers";
import type {
  DestStatus,
  EncoderInfo,
  EncoderList,
  GpuInfo,
  GpuVendor,
  Rendition,
  RenditionAspectMode,
  RenditionBounds,
  RenditionDeinterlace,
  OverlayAnchor,
  FontInfo,
  RenditionPreset,
  RenditionStatus,
  RenditionView,
  VideoStream,
} from "@/lib/types";
import {
  useT,
  useTN,
  type Translator,
  type TranslationKey,
  useStateLabel,
  type StateLabeller,
} from "@/lib/i18n";

/** Used until GET /renditions/presets answers, so the form's inputs always
 *  have bounds. The server's copy is authoritative; these only have to be
 *  wide enough not to reject something the server would accept. */
const FALLBACK_BOUNDS: RenditionBounds = {
  minDimension: 128,
  maxDimension: 7680,
  maxFps: 240,
  minBitrate: 100,
  maxBitrate: 100_000,
  minGopSeconds: 1,
  maxGopSeconds: 10,
};

/** Shown beside the presets if the server's own wording has not loaded. It is
 *  the same sentence db.PresetDisclaimer carries: the numbers are a place to
 *  start from, never a claim about what a platform accepts today. */
const FALLBACK_DISCLAIMER: TranslationKey = "rend.presetStartingPoint";

/** The sizes worth one click. "Keep source" is 0×0, the sentinel that means
 *  "do not scale", and it stays first because it is the cheapest answer. */
const SIZES: { key: string; label: string; labelKey?: TranslationKey; width: number; height: number }[] = [
  { key: "source", label: "", labelKey: "rend.keepSourceSize", width: 0, height: 0 },
  { key: "3840x2160", label: "3840 × 2160 — 2160p", width: 3840, height: 2160 },
  { key: "2560x1440", label: "2560 × 1440 — 1440p", width: 2560, height: 1440 },
  { key: "1920x1080", label: "1920 × 1080 — 1080p", width: 1920, height: 1080 },
  { key: "1280x720", label: "1280 × 720 — 720p", width: 1280, height: 720 },
  { key: "854x480", label: "854 × 480 — 480p", width: 854, height: 480 },
];

// Two coarse thresholds in pixels per second, used only to decide which of
// three sentences to show. They exist to catch "4K60 on x264" before it is
// discovered mid-stream, not to predict a frame time — the real cost depends on
// the preset, the content and every other encode sharing the machine.
//
//   1920×1080×60  = 124 MP/s
//   3840×2160×30  = 249 MP/s
//   3840×2160×60  = 498 MP/s
const SOFTWARE_BUSY_PIXELS = 120e6;
const SOFTWARE_UNREALISTIC_PIXELS = 240e6;

/** How each vendor is named in the list. "unknown" is deliberately shown as
 *  "hardware": we know it drives silicon, we just could not say whose. */
const VENDOR_LABEL: Partial<Record<GpuVendor, string>> = {
  intel: "Intel",
  nvidia: "NVIDIA",
  amd: "AMD",
  apple: "Apple",
};

/** The one-line description beside an encoder's name in the dropdown. */
function encoderLabel(t: Translator, e: EncoderInfo): string {
  const vendor =
    e.vendor === "software"
      ? t("rend.vendorSoftware")
      : (VENDOR_LABEL[e.vendor] ?? t("rend.hardwareFallback"));
  const parts = [vendor];
  if (e.default) parts.push(t("rend.encoderDefault"));
  if (!e.works) parts.push(t("rend.encoderUnavailable"));
  return parts.join(" · ");
}

/** Why this encoder is not offered, in full, for the tooltip and the line under
 *  the select. The server's `reason` is FFmpeg's own words, which is the part
 *  worth keeping: "Cannot load libcuda.so.1" and "Permission denied" are two
 *  problems with two different fixes. */
function encoderProblem(
  t: Translator,
e: EncoderInfo | undefined): string {
  if (!e || e.works) return "";
  if (!e.available) return e.reason || t("rend.encoderMissing", { name: e.name });
  const measured = e.measured
    ? t("rend.encoderTestFailed", { name: e.name })
    : t("rend.encoderRelatedFailed", { name: e.name });
  return e.reason ? t("rend.encoderFfmpegSaid", { measured, reason: e.reason }) : measured;
}

/** The encoder families whose per-encoder FLAGS have no evidence behind them.
 *
 *  Deliberately NOT `encoder.hardware`, which is what this was gated on and
 *  which made the badge false. `TestEveryConfiguredEncoderOpensWithItsOwnFlags`
 *  runs a real encode per registered encoder using that encoder's own row from
 *  `ffmpeg.encoderProfiles` — preset flag, rate control and the capped-VBR path
 *  included — and `h264_videotoolbox` and `hevc_videotoolbox` pass on macOS.
 *  Warning a Mac operator off the one hardware family that IS confirmed also
 *  costs the badge its credibility everywhere else: having watched it fire
 *  falsely once, they discount it on `h264_nvenc`, where it is true. */
const UNCONFIRMED_ENCODER_FAMILIES = ["_nvenc", "_qsv", "_vaapi", "_amf"];

function flagsUnconfirmed(name: string | undefined): boolean {
  return !!name && UNCONFIRMED_ENCODER_FAMILIES.some((family) => name.endsWith(family));
}

/** As much of a reason as fits on a dropdown row. The full text is on the
 *  item's tooltip and under the select, so nothing is only ever truncated. */
function shortReason(reason: string): string {
  return reason.length > 48 ? `${reason.slice(0, 47)}…` : reason;
}

/** Whether a diagnostic is the kind the user can fix in one command.
 *
 *  A render node that exists but cannot be opened is the most common
 *  hardware-encoding failure there is, and it is one group membership or one
 *  `--device` away from working — which makes it the one blocker worth pulling
 *  out of a tooltip and putting on the page. */
function isPermissionProblem(text: string): boolean {
  return /permission denied|not permitted|eacces|\brender group\b|\bvideo group\b/i.test(text);
}

interface Diagnostic {
  text: string;
  permission: boolean;
}

/** Everything the hardware scan and the test encodes found worth telling an
 *  operator, deduped. The notes come from the server already phrased as
 *  instructions, so they are shown verbatim rather than reworded here. */
function hardwareDiagnostics(gpu: GpuInfo | null, encoders: EncoderInfo[]): Diagnostic[] {
  const seen = new Set<string>();
  const out: Diagnostic[] = [];
  const add = (text: string) => {
    if (!text || seen.has(text)) return;
    seen.add(text);
    out.push({ text, permission: isPermissionProblem(text) });
  };

  for (const d of gpu?.devices ?? []) {
    if (!d.usable && d.problem) add(`${d.path} — ${d.problem}`);
  }
  for (const n of gpu?.notes ?? []) add(n);
  for (const e of encoders) {
    if (e.hardware && e.available && !e.works && e.measured && e.reason) {
      add(`${e.name} — ${e.reason}`);
    }
  }
  // Permission problems first: they are the ones with a fix attached.
  return out.sort((a, b) => Number(b.permission) - Number(a.permission));
}

type CostTone = "muted" | "warn" | "down";

/** How a cost verdict is painted. "muted" stays grey deliberately: most
 *  renditions are unremarkable, and spending a signal colour on them would
 *  make the two that matter harder to spot. */
const COST_CLASSES: Record<CostTone, { box: string; icon: string; title: string }> = {
  muted: {
    box: "border-border bg-background",
    icon: "text-muted-foreground",
    title: "text-foreground",
  },
  warn: { box: "border-warn/50 bg-warn/5", icon: "text-warn", title: "text-warn" },
  down: { box: "border-down/50 bg-down/5", icon: "text-down", title: "text-down" },
};

interface Cost {
  tone: CostTone;
  headline: string;
  detail: string;
  /** Pixels per second the encoder must sustain, 0 when it is not knowable. */
  pixelRate: number;
}

/** The output size a rendition actually produces, resolving the "keep source"
 *  sentinels against whatever the ingest was probed as.
 *
 *  A zero on one axis only means "keep the aspect ratio", so it is derived from
 *  the other axis rather than left at zero — otherwise a height-only rendition
 *  would look free. */
function effectiveSize(
  r: { width: number; height: number },
  src?: VideoStream | null,
): { width: number; height: number } {
  const aspect = src && src.width > 0 && src.height > 0 ? src.width / src.height : 0;
  let width = r.width;
  let height = r.height;
  if (width === 0 && height > 0) width = aspect > 0 ? Math.round(height * aspect) : 0;
  if (height === 0 && width > 0) height = aspect > 0 ? Math.round(width / aspect) : 0;
  if (width === 0) width = src?.width ?? 0;
  if (height === 0) height = src?.height ?? 0;
  return { width, height };
}

function effectiveFps(fps: number, src?: VideoStream | null): number {
  if (fps > 0) return fps;
  return src?.frameRate ? Math.round(src.frameRate) : 0;
}

/** How the configured size reads on a card, sentinels and all. */
function sizeLabel(t: Translator, r: { width: number; height: number }): string {
  if (r.width === 0 && r.height === 0) return t("rend.sizeSource");
  if (r.width === 0) return t("rend.sizeHeightOnly", { height: r.height });
  if (r.height === 0) return t("rend.sizeWidthOnly", { width: r.width });
  return `${r.width}×${r.height}`;
}

/** What this encode is likely to cost, said plainly.
 *
 *  Deliberately vague where being precise would be a lie: no percentage, no
 *  core count, no promise. What it will do is refuse to let someone pick x264
 *  at 4K60 without reading that it is not going to keep up. */
function encodeCost(
  t: Translator,
  r: { width: number; height: number; fps: number },
  encoder: EncoderInfo | undefined,
  src: VideoStream | null | undefined,
  cores?: number,
  /** Whether any hardware encoder passed its test encode on this machine. When
   *  none did, "choose a hardware encoder" is advice the user cannot act on and
   *  must not be given. */
  hardwareExists = true,
): Cost {
  const { width, height } = effectiveSize(r, src);
  const fps = effectiveFps(r.fps, src);
  const hardware = (encoder?.hardware ?? false) && (encoder?.works ?? true);

  if (width <= 0 || height <= 0 || fps <= 0) {
    return {
      tone: "muted",
      pixelRate: 0,
      headline: t("rend.costNoNumbers"),
      detail: t("rend.costNoNumbersDetail"),
    };
  }

  const rate = width * height * fps;
  const label = t("rend.costLabel", { width, height, fps });
  const mpps = `${Math.round(rate / 1e6)} MP/s`;
  const machine = cores && cores > 0 ? t("rend.costMachineCores", { cores }) : "";

  if (hardware) {
    return {
      tone: rate >= SOFTWARE_UNREALISTIC_PIXELS ? "warn" : "muted",
      pixelRate: rate,
      headline: t("rend.costOffloaded", { encoder: encoder?.name ?? t("rend.hardwareFallback") }),
      detail: t("rend.costOffloadedDetail", { label, mpps }),
    };
  }

  // The way out of a software encode this size is a hardware one — unless no
  // hardware encoder passed its test encode here, in which case there is no way
  // out and saying so is the only honest thing left.
  const escape = hardwareExists
    ? t("rend.chooseHardware")
    : t("rend.noHardwareEscape");

  if (rate >= SOFTWARE_UNREALISTIC_PIXELS) {
    return {
      tone: "down",
      pixelRate: rate,
      headline: t("rend.costBeyond"),
      detail: t("rend.costBeyondDetail", { label, mpps, machine, escape }),
    };
  }

  if (rate >= SOFTWARE_BUSY_PIXELS) {
    return {
      tone: "warn",
      pixelRate: rate,
      headline: t("rend.costBusy"),
      detail: t("rend.costBusyDetail", {
        label, mpps, machine, escape,
        encoder: encoder?.name ?? t("rend.softwareEncoderFallback"),
      }),
    };
  }

  return {
    tone: "muted",
    pixelRate: rate,
    headline: t("rend.costModest"),
    detail: t("rend.costModestDetail", { label, mpps, machine }),
  };
}

/** Things that are worth saying about this rendition against this source, and
 *  are not opinions: upscaling and frame duplication are simply waste. */
function sourceNotes(
  t: Translator,
r: { width: number; height: number; fps: number },
  src: VideoStream | null | undefined,
): string[] {
  if (!src || src.width <= 0 || src.height <= 0) return [];
  const notes: string[] = [];
  const { width, height } = effectiveSize(r, src);
  if (height > src.height || width > src.width) {
    notes.push(
      t("rend.noteUpscale", { width: src.width, height: src.height }),
    );
  }
  if (src.frameRate > 0 && r.fps > Math.round(src.frameRate)) {
    notes.push(
      t("rend.noteDuplicateFps", { srcFps: src.frameRate.toFixed(2), fps: r.fps }),
    );
  }
  return notes;
}

/** How a rendition should read at a glance.
 *
 *  No process is the normal state for a rendition nothing has selected — the
 *  ref count is doing its job — so it must not read as a failure. */
function renditionSignal(
  t: Translator,
  stateLabel: StateLabeller,
  live: RenditionStatus | undefined,
  enabledUsers: number,
): { tone: SignalTone; label: string } {
  if (live?.process) {
    return { tone: toneForState(live.process.state), label: stateLabel(live.process.state) };
  }
  if (live?.error) return { tone: "down", label: t("rend.signalError") };
  if (enabledUsers === 0) return { tone: "idle", label: t("rend.signalIdle") };
  return { tone: "warn", label: t("state.starting") };
}

export function RenditionsPage() {
  const t = useT();
  const { status, system } = useLiveData();
  const [views, setViews] = useState<RenditionView[]>([]);
  const [caps, setCaps] = useState<EncoderList | null>(null);
  const [redetecting, setRedetecting] = useState(false);
  const [presets, setPresets] = useState<RenditionPreset[]>([]);
  const [fonts, setFonts] = useState<FontsResponse | null>(null);
  const [disclaimer, setDisclaimer] = useState("");
  const [bounds, setBounds] = useState<RenditionBounds>(FALLBACK_BOUNDS);
  const [loading, setLoading] = useState(true);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editing, setEditing] = useState<Rendition | null>(null);
  const [deleting, setDeleting] = useState<Rendition | null>(null);
  const [busyId, setBusyId] = useState<number | null>(null);

  const loadRenditions = useCallback(
    () =>
      api
        .listRenditions()
        .then(setViews)
        .catch((err) =>
          toast.error(err instanceof Error ? err.message : t("rend.loadFailed")),
        )
        .finally(() => setLoading(false)),
    [t],
  );

  // The encoder capabilities and the presets are properties of the machine, not
  // of the data, so they are fetched once rather than on every refresh. The
  // re-detect button is what re-asks, because the answer only changes when the
  // hardware or its drivers do.
  useEffect(() => {
    api
      .listEncoders()
      .then(setCaps)
      .catch(() => setCaps(null));
    api
      .renditionPresets()
      .then((p) => {
        setPresets(p.presets);
        setDisclaimer(p.disclaimer || "");
        setBounds(p.bounds ?? FALLBACK_BOUNDS);
      })
      .catch(() => {});
    // The fonts and whether drawtext exists are properties of the machine and
    // its data directory, not of the rows, so they are fetched with the rest.
    api
      .fonts()
      .then(setFonts)
      .catch(() => setFonts(null));
  }, []);

  // Reload the rows whenever the live snapshot shows a different set of
  // renditions or a different ref count — that is what a destination being
  // enabled in another tab looks like from here.
  const liveSig = (status?.renditions ?? []).map((r) => `${r.id}:${r.consumers}`).join(",");
  useEffect(() => {
    void loadRenditions();
  }, [loadRenditions, liveSig]);

  const liveById = useMemo(() => {
    const m = new Map<number, RenditionStatus>();
    for (const r of status?.renditions ?? []) m.set(r.id, r);
    return m;
  }, [status]);

  // Which destinations feed off what. The socket carries names as well as
  // counts, so the chips on a card and the number beside them can never
  // disagree; the REST counts stand in only until the first snapshot lands.
  const usersById = useMemo(() => {
    const m = new Map<number, DestStatus[]>();
    for (const d of status?.destinations ?? []) {
      if (d.renditionId == null) continue;
      const list = m.get(d.renditionId);
      if (list) list.push(d);
      else m.set(d.renditionId, [d]);
    }
    return m;
  }, [status]);

  // The database's own counts for one rendition, which is what the dialogs
  // fall back to when the live list has not arrived. `null` when the rows
  // themselves have not loaded, which is a different thing again from zero.
  const countsFor = (id: number) => {
    const v = views.find((x) => x.rendition.id === id);
    return v ? { destinations: v.destinations, enabledDestinations: v.enabledDestinations } : null;
  };

  const passthrough = (status?.destinations ?? []).filter((d) => d.renditionId == null);
  const running = (status?.renditions ?? []).filter((r) => r.process?.state === "running").length;
  const sourceVideo = status?.source.video ?? null;

  const encoders = caps?.encoders ?? [];
  // Empty is meaningful and different from "not loaded yet": it is the machine
  // saying no hardware encoder passed. Before the list arrives, assume hardware
  // exists so the cost model does not tell the user something final on no data.
  const hardwareExists = caps ? (caps.hardware?.length ?? 0) > 0 : true;

  // The test encodes take a few seconds, so the button owns a spinner rather
  // than the page: everything on screen stays true until the answer changes.
  const redetect = useCallback(async () => {
    setRedetecting(true);
    try {
      const next = await api.redetectEncoders();
      setCaps(next);
      const working = next.hardware?.length ?? 0;
      toast.success(
        working > 0
          ? t("rend.redetectedHardware", { list: next.hardware?.join(", ") ?? "" })
          : t("rend.redetectedNoHardware"),
      );
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("rend.redetectFailed"));
    } finally {
      setRedetecting(false);
    }
  }, [t]);

  const openCreate = () => {
    setEditing(null);
    setDialogOpen(true);
  };

  const openEdit = (r: Rendition) => {
    setEditing(r);
    setDialogOpen(true);
  };

  const restart = async (r: Rendition) => {
    setBusyId(r.id);
    try {
      await api.restartRendition(r.id);
      toast.success(t("rend.restarting", { name: r.name }));
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("rend.restartFailed"));
    } finally {
      setBusyId(null);
    }
  };

  return (
    <div className="p-3">
      <PageHeader
        title={t("rend.title")}
        subtitle={t("rend.subtitle")}
        actions={
          <>
            {running > 0 && (
              <Badge variant="live">
                <StatusDot tone="live" size="sm" />
                {t("rend.encodingCount", { count: running })}
              </Badge>
            )}
            <Button size="sm" onClick={openCreate}>
              <Plus /> {t("rend.newRendition")}
            </Button>
          </>
        }
      />

      <Card className="mb-3">
        <CardHeader>
          <CardTitle className="flex items-center gap-1.5">
            <Layers className="h-3.5 w-3.5" /> {t("rend.howTitle")}
          </CardTitle>
          <CardDescription>{t("rend.howBody")}</CardDescription>
        </CardHeader>
        <CardContent>
          <div className="flex flex-wrap items-center gap-2 rounded border border-border bg-background px-2 py-1.5">
            <Badge variant="outline">{t("rend.passthroughBadge")}</Badge>
            <span className="text-sm text-muted-foreground">{t("rend.passthroughBody")}</span>
            <span className="tnum ml-auto font-mono text-sm">{passthrough.length}</span>
          </div>
          {passthrough.length > 0 && (
            <div className="mt-1.5 flex flex-wrap gap-1">
              {passthrough.map((d) => (
                <DestChip key={d.id} dest={d} />
              ))}
            </div>
          )}
        </CardContent>
      </Card>

      {loading ? (
        <div className="flex justify-center py-8">
          <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
        </div>
      ) : views.length === 0 ? (
        <Card>
          <CardContent className="flex flex-col items-center gap-2 py-8 text-center">
            <p className="max-w-lg text-[12px] text-muted-foreground">
            {t("rend.empty")}
            </p>
            <Button size="sm" onClick={openCreate}>
              <Plus /> {t("rend.newRendition")}
            </Button>
          </CardContent>
        </Card>
      ) : (
        <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
          {views.map((v) => (
            <RenditionCard
              key={v.rendition.id}
              view={v}
              live={liveById.get(v.rendition.id)}
              users={status ? (usersById.get(v.rendition.id) ?? []) : null}
              encoder={encoders.find((e) => e.name === v.rendition.encoder)}
              hardwareExists={hardwareExists}
              source={sourceVideo}
              cores={system?.numCpu}
              busy={busyId === v.rendition.id}
              onEdit={() => openEdit(v.rendition)}
              onRestart={() => restart(v.rendition)}
              onDelete={() => setDeleting(v.rendition)}
            />
          ))}
        </div>
      )}

      <RenditionDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        rendition={editing}
        caps={caps}
        redetecting={redetecting}
        onRedetect={redetect}
        presets={presets}
        fonts={fonts}
        disclaimer={disclaimer || t(FALLBACK_DISCLAIMER)}
        bounds={bounds}
        source={sourceVideo}
        cores={system?.numCpu}
        /* `null`, NOT `[]`, before the first snapshot. An empty array is a
           claim that nothing selects this rendition; a socket that has not
           spoken is not. Both dialogs fall back to the REST counts, which is
           what RenditionCard has always done. See lib/renditionUsers.ts. */
        users={status && editing ? (usersById.get(editing.id) ?? []) : null}
        counts={editing ? countsFor(editing.id) : null}
        onSaved={loadRenditions}
      />

      <DeleteRenditionDialog
        rendition={deleting}
        users={status && deleting ? (usersById.get(deleting.id) ?? []) : null}
        counts={deleting ? countsFor(deleting.id) : null}
        onOpenChange={(open) => !open && setDeleting(null)}
        onDeleted={loadRenditions}
      />
    </div>
  );
}

/** One destination, as a chip. Disabled rows are drawn too: they still select
 *  this rendition, and deleting it affects them the moment they are enabled. */
function DestChip({ dest }: { dest: DestStatus }) {
  const t = useT();
  const stateLabel = useStateLabel();
  const tone: SignalTone = dest.enabled ? toneForState(dest.process?.state) : "idle";
  return (
    <span
      className="inline-flex items-center gap-1.5 rounded border border-border bg-background px-1.5 py-0.5 text-sm"
      title={dest.enabled ? stateLabel(dest.process?.state) : t("state.disabled")}
    >
      <StatusDot tone={tone} size="sm" />
      <span className="max-w-40 truncate">{dest.name}</span>
    </span>
  );
}

/* Exported for its test: the card is where a rendition's live state is
 * asserted, including whether there is anything to restart. */
export function RenditionCard({
  view,
  live,
  users,
  encoder,
  hardwareExists,
  source,
  cores,
  busy,
  onEdit,
  onRestart,
  onDelete,
}: {
  view: RenditionView;
  live?: RenditionStatus;
  /** null before the first live snapshot, when only the counts are known. */
  users: DestStatus[] | null;
  encoder?: EncoderInfo;
  hardwareExists: boolean;
  source: VideoStream | null;
  cores?: number;
  busy: boolean;
  onEdit: () => void;
  onRestart: () => void;
  onDelete: () => void;
}) {
  const stateLabel = useStateLabel();
  const t = useT();
  const r = view.rendition;
  const total = users ? users.length : view.destinations;
  const enabled = users ? users.filter((d) => d.enabled).length : view.enabledDestinations;
  const signal = renditionSignal(t, stateLabel, live, enabled);
  const cost = encodeCost(t, r, encoder, source, cores, hardwareExists);
  const proc = live?.process;
  // A saved rendition goes stale: the card was swapped, the driver upgraded,
  // the container lost its --device passthrough. The engine refuses to start it
  // with this same reason, so say it here rather than leave the row looking
  // healthy until someone enables a destination on it.
  const encoderProblemText = encoderProblem(t, encoder);

  return (
    <Card>
      <CardHeader className="flex-row items-start justify-between gap-2">
        <div className="min-w-0">
          <CardTitle className="flex items-center gap-2">
            <StatusDot tone={signal.tone} />
            <span className="truncate">{r.name}</span>
          </CardTitle>
          <CardDescription className="font-mono">
            {sizeLabel(t, r)}
            {r.fps > 0 ? ` · ${r.fps} fps` : ` · ${t("rend.sourceFps")}`} · {r.encoder}
          </CardDescription>
        </div>
        <Badge variant={toneBadge[signal.tone]}>{signal.label}</Badge>
      </CardHeader>

      <CardContent className="flex flex-col gap-2.5">
        <div className="grid grid-cols-3 gap-2">
          <Stat labelKey="rend.target" value={kbps(r.videoBitrate)} />
          <Stat
            labelKey="rend.live"
            value={proc?.state === "running" ? kbps(proc.progress?.bitrateKbps ?? 0) : "—"}
            tone={proc?.state === "running" ? "live" : "muted"}
          />
          <Stat
            labelKey="rend.speed"
            value={proc?.state === "running" ? `${(proc.progress?.speed ?? 0).toFixed(2)}×` : "—"}
            // Below real time the encode is losing the race, and every
            // destination under it inherits the stutter.
            tone={
              proc?.state === "running" && (proc.progress?.speed ?? 0) < 0.98 ? "warn" : "muted"
            }
          />
          <Stat labelKey="rend.gop" value={`${r.gopSeconds}s`} tone="muted" />
          <Stat labelKey="rend.preset" value={r.preset} tone="muted" />
          <Stat
            labelKey="rend.uptime"
            value={proc?.state === "running" ? duration(proc.uptimeSec) : "—"}
            tone="muted"
          />
        </div>

        {r.note && <p className="text-sm text-muted-foreground">{r.note}</p>}

        <div>
          <div className="mb-1 flex items-center justify-between">
            <span className="text-sm uppercase tracking-wider text-muted-foreground">
              {t("rend.feeds")}
            </span>
            <span className="tnum font-mono text-sm text-muted-foreground">
              {t("rend.enabledOfTotal", { enabled, total })}
            </span>
          </div>
          {total === 0 ? (
            <p className="text-sm text-muted-foreground">
            {t("rend.unused")}
            </p>
          ) : users ? (
            <div className="flex flex-wrap gap-1">
              {users.map((d) => (
                <DestChip key={d.id} dest={d} />
              ))}
            </div>
          ) : (
            <p className="text-sm text-muted-foreground">
            {t("rend.waitingSnapshot")}
            </p>
          )}
        </div>

        {encoderProblemText && (
          <div className="rounded border border-down/30 bg-down-dim px-2 py-1 text-sm text-down">
            {encoderProblemText} {t("rend.editToFix")}
          </div>
        )}
        {live?.error && (
          <div className="rounded border border-down/30 bg-down-dim px-2 py-1 text-sm text-down">
            {live.error}
          </div>
        )}
        {proc?.lastError && proc.state !== "running" && (
          <div className="rounded border border-down/30 bg-down-dim px-2 py-1 text-sm text-down">
            {proc.lastError}
          </div>
        )}

        {/* Only the verdict worth interrupting for. Everything else the cost
            model has to say lives in the editor, next to the controls. */}
        {cost.tone === "down" && (
          <div className="rounded border border-warn/30 bg-warn-dim px-2 py-1 text-sm text-warn">
            {cost.headline}. {cost.detail}
          </div>
        )}

        <div className="flex items-center gap-1 border-t border-border pt-2">
          <Button variant="outline" size="sm" onClick={onEdit}>
            <Pencil /> {t("common.edit")}
          </Button>
          <Button
            variant="ghost"
            size="sm"
            onClick={onRestart}
            disabled={busy || !proc}
            title={proc ? t("rend.restartEncode") : t("rend.notRunning")}
          >
            {busy ? <Loader2 className="animate-spin" /> : <RotateCw />} {t("common.restart")}
          </Button>
          {/* THE REASON, IN THE CARD. button.tsx sets
              `disabled:pointer-events-none`, so the title above never reaches a
              pointer and never reaches a screen reader either -- the whole
              explanation for a greyed control lived somewhere unreachable. It
              says "not reported" rather than "not running" deliberately: `proc`
              comes from the install-wide status socket, which on a
              multi-programme install describes whichever engine spoke last. */}
          {!proc && (
            <span className="text-sm text-muted-foreground" data-testid="restart-reason">
              {t("rend.noProcessToRestart")}
            </span>
          )}
          <Button
            variant="ghost"
            size="icon-sm"
            className="ml-auto text-muted-foreground hover:text-down"
            onClick={onDelete}
            aria-label={t("sources.deleteAria", { name: r.name })}
          >
            <Trash2 />
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

/** Blank form values. Encoder is filled from what the server says this FFmpeg
 *  defaults to, which is software unless the build has no x264 at all. */
function emptyForm(defaultEncoder: string) {
  return {
    name: "",
    width: 1920,
    height: 1080,
    fps: 60,
    videoBitrate: 6000,
    // 0 on both is the CBR relationship every rendition already had.
    maxrateKbps: 0,
    bufsizeKbps: 0,
    encoder: defaultEncoder,
    preset: "veryfast",
    gopSeconds: 2,
    aspectMode: "stretch" as AspectKey,
    padColor: "",
    deinterlace: "off" as DeinterlaceKey,
    overlayImage: "",
    overlayAnchor: "bottom-right" as OverlayAnchor,
    // Percentages are held as whole numbers in the form and divided at the
    // boundary. An operator types "12", not "0.12", and the conversion belongs
    // in one place rather than in every field.
    overlayWidth: 12,
    overlayMarginX: 4,
    overlayMarginY: 4,
    overlayOpacity: 100,
    textContent: "",
    textFont: "",
    textAnchor: "bottom-left" as OverlayAnchor,
    textSize: 6,
    textColor: "white",
    textMarginX: 4,
    textMarginY: 4,
    textBox: true,
    textBoxColor: "black",
    textBoxOpacity: 50,
    note: "",
  };
}

// Radix's SelectItem refuses an empty value, and the empty string is precisely
// what the server stores for both zero values. So the form carries a sentinel
// and converts at the boundary -- never in between, or the two vocabularies
// leak into each other and a saved "stretch" becomes an unknown mode the API
// rejects.
type AspectKey = "stretch" | "crop" | "pad" | "blurpad";
type DeinterlaceKey = "off" | "auto" | "all";

const ASPECT_MODES: { key: AspectKey; label: TranslationKey; hint: TranslationKey }[] = [
  { key: "stretch", label: "rend.aspectStretch", hint: "rend.aspectStretchHint" },
  { key: "crop", label: "rend.aspectCrop", hint: "rend.aspectCropHint" },
  { key: "pad", label: "rend.aspectPad", hint: "rend.aspectPadHint" },
  { key: "blurpad", label: "rend.aspectBlurpad", hint: "rend.aspectBlurpadHint" },
];

const DEINTERLACE_MODES: { key: DeinterlaceKey; label: TranslationKey; hint: TranslationKey }[] = [
  { key: "off", label: "rend.deintOff", hint: "rend.deintOffHint" },
  { key: "auto", label: "rend.deintAuto", hint: "rend.deintAutoHint" },
  { key: "all", label: "rend.deintAll", hint: "rend.deintAllHint" },
];

// The font picker's "use the built-in" option. A sentinel rather than "",
// because a Select cannot hold an empty string as a value -- it reads as
// "nothing selected" and the trigger renders blank.
const DEFAULT_FONT_KEY = "__default__";

// Where the server looks for watermark images and extra fonts. Paths, not
// prose, so they stay as written in every language.
const OVERLAYS_DIR = "<data>/overlays/";
const FONTS_DIR = "<data>/fonts/";

type FontsResponse = {
  fonts: FontInfo[];
  defaultFont: string;
  dir: string;
  textSupported: boolean;
};

// Stored fractions (0-1) become whole percents for the form. A zero or missing
// value takes the default rather than showing "0", which would read as a
// deliberate choice of an invisible overlay.
const pctToForm = (v: number | undefined, fallback: number): number =>
  v === undefined || v === null || v <= 0 ? fallback : Math.round(v * 100);

const toAspectKey = (v: string | undefined): AspectKey => (v ? (v as AspectKey) : "stretch");
const fromAspectKey = (k: AspectKey): RenditionAspectMode => (k === "stretch" ? "" : k);
const toDeinterlaceKey = (v: string | undefined): DeinterlaceKey =>
  v ? (v as DeinterlaceKey) : "off";
const fromDeinterlaceKey = (k: DeinterlaceKey): RenditionDeinterlace => (k === "off" ? "" : k);

function sizeKeyFor(width: number, height: number): string {
  const match = SIZES.find((s) => s.width === width && s.height === height);
  return match ? match.key : "custom";
}

/* Exported for its test: the editor renders one hint per control, and one of
 * them was rendering its own translation key. */
export function RenditionDialog({
  open,
  onOpenChange,
  rendition,
  caps,
  redetecting,
  onRedetect,
  presets,
  fonts,
  disclaimer,
  bounds,
  source,
  cores,
  users,
  counts,
  onSaved,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** The rendition being edited, or null to create one. */
  rendition: Rendition | null;
  /** What this machine can encode with, or null until the list arrives. */
  caps: EncoderList | null;
  redetecting: boolean;
  onRedetect: () => void;
  presets: RenditionPreset[];
  fonts: FontsResponse | null;
  disclaimer: string;
  bounds: RenditionBounds;
  source: VideoStream | null;
  cores?: number;
  /** The live list of destinations reading this rendition, or null when the
   *  status socket has not answered yet. Never `[]` for "not known": see
   *  lib/renditionUsers.ts. */
  users: DestStatus[] | null;
  /** The database's own counts, which stand in when `users` is null. */
  counts: { destinations: number; enabledDestinations: number } | null;
  onSaved: () => void;
}) {
  const t = useT();
  const tn = useTN();
  const editing = rendition !== null;
  const encoders = useMemo(() => caps?.encoders ?? [], [caps]);
  const defaultEncoder = caps?.default ?? "libx264";
  const hardwareExists = caps ? (caps.hardware?.length ?? 0) > 0 : true;
  const [form, setForm] = useState(() => emptyForm(defaultEncoder));
  const [sizeKey, setSizeKey] = useState("1920x1080");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!open) return;
    if (rendition) {
      setForm({
        name: rendition.name,
        width: rendition.width,
        height: rendition.height,
        fps: rendition.fps,
        videoBitrate: rendition.videoBitrate,
        maxrateKbps: rendition.maxrateKbps ?? 0,
        bufsizeKbps: rendition.bufsizeKbps ?? 0,
        encoder: rendition.encoder,
        preset: rendition.preset,
        gopSeconds: rendition.gopSeconds,
        aspectMode: toAspectKey(rendition.aspectMode),
        padColor: rendition.padColor ?? "",
        deinterlace: toDeinterlaceKey(rendition.deinterlace),
        textContent: rendition.text?.content ?? "",
        textFont: rendition.text?.font ?? "",
        textAnchor: rendition.text?.anchor ?? "bottom-left",
        textSize: pctToForm(rendition.text?.sizePct, 6),
        textColor: rendition.text?.color ?? "white",
        textMarginX: pctToForm(rendition.text?.marginXPct, 4),
        textMarginY: pctToForm(rendition.text?.marginYPct, 4),
        textBox: rendition.text?.box ?? true,
        textBoxColor: rendition.text?.boxColor ?? "black",
        textBoxOpacity: pctToForm(rendition.text?.boxOpacity, 50),
        overlayImage: rendition.overlay?.image ?? "",
        overlayAnchor: rendition.overlay?.anchor ?? "bottom-right",
        overlayWidth: pctToForm(rendition.overlay?.widthPct, 12),
        overlayMarginX: pctToForm(rendition.overlay?.marginXPct, 4),
        overlayMarginY: pctToForm(rendition.overlay?.marginYPct, 4),
        overlayOpacity: pctToForm(rendition.overlay?.opacity, 100),
        note: rendition.note,
      });
      setSizeKey(sizeKeyFor(rendition.width, rendition.height));
    } else {
      const blank = emptyForm(defaultEncoder);
      setForm(blank);
      setSizeKey(sizeKeyFor(blank.width, blank.height));
    }
  }, [open, rendition, defaultEncoder]);

  const set = <K extends keyof ReturnType<typeof emptyForm>>(
    key: K,
    value: ReturnType<typeof emptyForm>[K],
  ) => setForm((f) => ({ ...f, [key]: value }));

  const applyPreset = (p: RenditionPreset) => {
    if (!p.rendition) return;
    const t = p.rendition;
    setForm({
      name: t.name,
      width: t.width,
      height: t.height,
      fps: t.fps,
      videoBitrate: t.videoBitrate,
      maxrateKbps: 0,
      bufsizeKbps: 0,
      // The preset names a software encoder because that is the one every
      // build has. If this machine's default is something else, respect it.
      encoder: encoders.some((e) => e.name === t.encoder && e.works)
        ? t.encoder
        : defaultEncoder,
      preset: t.preset,
      gopSeconds: t.gopSeconds,
      aspectMode: toAspectKey(t.aspectMode),
      padColor: t.padColor ?? "",
      deinterlace: toDeinterlaceKey(t.deinterlace),
      textContent: t.text?.content ?? "",
      textFont: t.text?.font ?? "",
      textAnchor: t.text?.anchor ?? "bottom-left",
      textSize: pctToForm(t.text?.sizePct, 6),
      textColor: t.text?.color ?? "white",
      textMarginX: pctToForm(t.text?.marginXPct, 4),
      textMarginY: pctToForm(t.text?.marginYPct, 4),
      textBox: t.text?.box ?? true,
      textBoxColor: t.text?.boxColor ?? "black",
      textBoxOpacity: pctToForm(t.text?.boxOpacity, 50),
      overlayImage: t.overlay?.image ?? "",
      overlayAnchor: t.overlay?.anchor ?? "bottom-right",
      overlayWidth: pctToForm(t.overlay?.widthPct, 12),
      overlayMarginX: pctToForm(t.overlay?.marginXPct, 4),
      overlayMarginY: pctToForm(t.overlay?.marginYPct, 4),
      overlayOpacity: pctToForm(t.overlay?.opacity, 100),
      note: t.note,
    });
    setSizeKey(sizeKeyFor(t.width, t.height));
  };

  const chooseSize = (key: string) => {
    setSizeKey(key);
    const size = SIZES.find((s) => s.key === key);
    if (size) setForm((f) => ({ ...f, width: size.width, height: size.height }));
  };

  // Every known encoder is listed, working ones first. The ones that do not
  // work stay visible and disabled with the reason attached, because
  // "h264_nvenc — no NVENC capable device found" tells the user their container
  // is missing --gpus, where a silently shorter list teaches them nothing.
  const choices = useMemo(() => {
    const working = encoders.filter((e) => e.works);
    const broken = encoders.filter((e) => !e.works);
    return [...working, ...broken];
  }, [encoders]);

  const encoder = encoders.find((e) => e.name === form.encoder);
  const cost = encodeCost(t, form, encoder, source, cores, hardwareExists);
  const notes = sourceNotes(t, form, source);
  const consequence = deleteConsequence(users, counts);
  // How many enabled destinations a save restarts. Zero when that is not known,
  // so the toast says "saved." rather than naming a count nobody measured.
  const enabledUsers =
    consequence.kind === "named" || consequence.kind === "counted" ? consequence.enabled : 0;
  const diagnostics = useMemo(
    () => hardwareDiagnostics(caps?.gpu ?? null, encoders),
    [caps, encoders],
  );
  const permissionBlocked = diagnostics.some((d) => d.permission);
  // The encoder saved on the rendition being edited stays selectable even when
  // it no longer works: the row was legal when it was written, and a form that
  // silently swapped it would hide the very failure this is meant to explain.
  const savedEncoder = editing ? rendition.encoder : "";

  const nameOk = form.name.trim().length > 0;
  const bitrateOk =
    form.videoBitrate >= bounds.minBitrate && form.videoBitrate <= bounds.maxBitrate;
  // 0 is "derive it" and always valid. Anything else has to be a ceiling the
  // encoder can actually sit under, so it may not be below the target.
  const maxrateOk =
    form.maxrateKbps === 0 ||
    (form.maxrateKbps >= form.videoBitrate && form.maxrateKbps <= bounds.maxBitrate);
  // An aspect conversion is defined by the target shape, so it needs both axes.
  // With one free, the plain scale already preserves the aspect ratio and the
  // mode would be inert -- the server refuses the pair rather than storing a
  // control that quietly does nothing, and the form says so before the save.
  const aspectApplies = form.width > 0 && form.height > 0;
  // An overlay needs both axes for the same reason and one more: the image is
  // scaled to a percentage of the OUTPUT width, so that width has to be a
  // number when the arguments are built. It also needs an image -- the geometry
  // fields alone are not an overlay.
  const overlayApplies = aspectApplies && form.overlayImage.trim() !== "";
  // Text needs both axes for the same reason, plus its own content: the type is
  // sized as a percentage of the output HEIGHT, so that height has to be a
  // number when the arguments are built.
  const textApplies = aspectApplies && form.textContent.trim() !== "";

  const save = async () => {
    setBusy(true);
    try {
      const payload: Partial<Rendition> = {
        name: form.name.trim(),
        width: form.width,
        height: form.height,
        fps: form.fps,
        videoBitrate: form.videoBitrate,
        maxrateKbps: form.maxrateKbps,
        bufsizeKbps: form.bufsizeKbps,
        encoder: form.encoder,
        preset: form.preset.trim(),
        gopSeconds: form.gopSeconds,
        // The server refuses an aspect mode when either axis is free, because
        // there is no target shape to convert to. Sending one anyway would turn
        // a disabled control into a save error the user cannot connect to
        // anything they touched, so it is cleared here to match what the form
        // shows.
        aspectMode: aspectApplies ? fromAspectKey(form.aspectMode) : "",
        padColor: aspectApplies && form.aspectMode === "pad" ? form.padColor.trim() : "",
        deinterlace: fromDeinterlaceKey(form.deinterlace),
        // The server refuses an overlay on a rendition with a free axis,
        // because the image is sized as a percentage of the output and that has
        // to resolve to a number. Cleared here rather than sent and rejected,
        // for the same reason aspectMode is: a save error the operator cannot
        // connect to anything they touched is worse than a disabled control.
        overlay: overlayApplies
          ? {
              image: form.overlayImage.trim(),
              anchor: form.overlayAnchor,
              widthPct: form.overlayWidth / 100,
              marginXPct: form.overlayMarginX / 100,
              marginYPct: form.overlayMarginY / 100,
              opacity: form.overlayOpacity / 100,
            }
          : { image: "" },
        // Cleared rather than sent and rejected, for the reason overlay is.
        text: textApplies
          ? {
              content: form.textContent.trim(),
              font: form.textFont.trim(),
              anchor: form.textAnchor,
              sizePct: form.textSize / 100,
              color: form.textColor.trim(),
              marginXPct: form.textMarginX / 100,
              marginYPct: form.textMarginY / 100,
              box: form.textBox,
              boxColor: form.textBoxColor.trim(),
              boxOpacity: form.textBoxOpacity / 100,
            }
          : { content: "" },
        note: form.note.trim(),
      };
      if (rendition) {
        await api.updateRendition(rendition.id, payload);
        toast.success(
          enabledUsers > 0
            ? t(enabledUsers === 1 ? "rend.savedRestartingOne" : "rend.savedRestartingMany", {
                name: payload.name ?? "",
                count: enabledUsers,
              })
            : t("rend.saved", { name: payload.name ?? "" }),
        );
      } else {
        await api.createRendition(payload);
        toast.success(t("rend.created"));
      }
      onSaved();
      onOpenChange(false);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("rend.saveFailed"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{editing ? t("rend.editRendition") : t("rend.newRendition")}</DialogTitle>
          <DialogDescription>{t("rend.dialogDescription")}</DialogDescription>
        </DialogHeader>

        <div className="flex flex-col gap-3">
          {presets.length > 0 && (
            <div className="flex flex-col gap-1.5 rounded border border-border bg-background p-2">
              <span className="text-sm uppercase tracking-wider text-muted-foreground">
                {t("rend.startingPoints")}
              </span>
              <div className="flex flex-wrap gap-1">
                {presets
                  .filter((p) => !p.passthrough)
                  .map((p) => (
                    <Button
                      key={p.key}
                      variant="outline"
                      size="sm"
                      type="button"
                      onClick={() => applyPreset(p)}
                    >
                      {p.label}
                    </Button>
                  ))}
              </div>
              {/* Verbatim from the server so the UI and the docs cannot drift
                  into presenting these numbers as a platform's actual limits. */}
              <span className="text-sm text-warn">{disclaimer}</span>
              <span className="text-sm text-muted-foreground">
            {t("rend.seedNote")}
              </span>
            </div>
          )}

          <div className="flex flex-col gap-1">
            <Label htmlFor="rend-name">{t("rend.name")}</Label>
            <Input
              id="rend-name"
              value={form.name}
              onChange={(e) => set("name", e.target.value)}
              placeholder={t("rend.namePlaceholder")}
            />
          </div>

          <div className="grid grid-cols-2 gap-2">
            <div className="flex flex-col gap-1">
              <Label>{t("rend.resolution")}</Label>
              <Select value={sizeKey} onValueChange={chooseSize}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {SIZES.map((s) => (
                    <SelectItem key={s.key} value={s.key}>
                      {s.labelKey ? t(s.labelKey) : s.label}
                    </SelectItem>
                  ))}
                  <SelectItem value="custom">{t("rend.custom")}</SelectItem>
                </SelectContent>
              </Select>
            </div>

            <div className="flex flex-col gap-1">
              <Label htmlFor="rend-fps">{t("rend.frameRate")}</Label>
              <Input
                id="rend-fps"
                type="number"
                min={0}
                max={bounds.maxFps}
                value={form.fps}
                onChange={(e) => set("fps", Number(e.target.value))}
              />
              <span className="text-sm text-muted-foreground">
                {t("rend.fpsHint")}
              </span>
            </div>
          </div>

          {sizeKey === "custom" && (
            <div className="grid grid-cols-2 gap-2">
              <div className="flex flex-col gap-1">
                <Label htmlFor="rend-w">{t("rend.width")}</Label>
                <Input
                  id="rend-w"
                  type="number"
                  min={0}
                  max={bounds.maxDimension}
                  step={2}
                  value={form.width}
                  onChange={(e) => set("width", Number(e.target.value))}
                />
              </div>
              <div className="flex flex-col gap-1">
                <Label htmlFor="rend-h">{t("rend.height")}</Label>
                <Input
                  id="rend-h"
                  type="number"
                  min={0}
                  max={bounds.maxDimension}
                  step={2}
                  value={form.height}
                  onChange={(e) => set("height", Number(e.target.value))}
                />
              </div>
              <span className="col-span-2 text-sm text-muted-foreground">
            {t("rend.evenNote")}
              </span>
            </div>
          )}

          <div className="grid grid-cols-2 gap-2">
            <div className="flex flex-col gap-1">
              <Label htmlFor="rend-bitrate">{t("rend.videoBitrate")}</Label>
              <Input
                id="rend-bitrate"
                type="number"
                min={bounds.minBitrate}
                max={bounds.maxBitrate}
                step={100}
                value={form.videoBitrate}
                onChange={(e) => set("videoBitrate", Number(e.target.value))}
              />
              <span className={`text-sm ${bitrateOk ? "text-muted-foreground" : "text-down"}`}>
                {t("rend.bitrateHint")}
              </span>
            </div>

            <div className="flex flex-col gap-1">
              <Label htmlFor="rend-gop">{t("rend.keyframeInterval")}</Label>
              <Input
                id="rend-gop"
                type="number"
                min={bounds.minGopSeconds}
                max={bounds.maxGopSeconds}
                step={0.5}
                value={form.gopSeconds}
                onChange={(e) => set("gopSeconds", Number(e.target.value))}
              />
              <span className="text-sm text-muted-foreground">
                {t("rend.gopHint")}
              </span>
            </div>
          </div>

          {/* Rate control. Both default to 0, which is the CBR relationship
              every rendition had before these were reachable — so a user who
              never opens this row gets the command line they always got.
              See #341 and docs/ENCODING.md. */}
          <div className="grid grid-cols-2 gap-2">
            <div className="flex flex-col gap-1">
              <Label htmlFor="rend-maxrate">{t("clips.ceiling")}</Label>
              <Input
                id="rend-maxrate"
                type="number"
                min={0}
                max={bounds.maxBitrate}
                step={100}
                value={form.maxrateKbps}
                onChange={(e) => set("maxrateKbps", Number(e.target.value))}
              />
              <span className={`text-sm ${maxrateOk ? "text-muted-foreground" : "text-down"}`}>
                {maxrateOk
                  ? t("rend.maxrateHint")
                  : t("rend.maxrateInvalid")}
              </span>
            </div>

            <div className="flex flex-col gap-1">
              <Label htmlFor="rend-bufsize">{t("rend.rateWindow")}</Label>
              <Input
                id="rend-bufsize"
                type="number"
                min={0}
                step={100}
                value={form.bufsizeKbps}
                onChange={(e) => set("bufsizeKbps", Number(e.target.value))}
              />
              <span className="text-sm text-muted-foreground">{t("rend.bufsizeHint")}</span>
            </div>
          </div>

          <div className="grid grid-cols-2 gap-2">
            <div className="flex flex-col gap-1">
              <Label htmlFor="rend-aspect">{t("rend.aspectHandling")}</Label>
              <Select
                value={form.aspectMode}
                onValueChange={(v) => set("aspectMode", v as AspectKey)}
                disabled={!aspectApplies}
              >
                <SelectTrigger id="rend-aspect">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {ASPECT_MODES.map((m) => (
                    <SelectItem key={m.key} value={m.key} title={t(m.hint)}>
                      {t(m.label)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <span className="text-sm text-muted-foreground">
                {!aspectApplies
                  ? t("rend.needsBothAWidthAnd")
                  : (() => { const m = ASPECT_MODES.find((m) => m.key === form.aspectMode); return m ? t(m.hint) : ""; })()}
              </span>
            </div>

            <div className="flex flex-col gap-1">
              <Label htmlFor="rend-deint">{t("rend.deinterlace")}</Label>
              <Select
                value={form.deinterlace}
                onValueChange={(v) => set("deinterlace", v as DeinterlaceKey)}
              >
                <SelectTrigger id="rend-deint">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {DEINTERLACE_MODES.map((m) => (
                    <SelectItem key={m.key} value={m.key} title={t(m.hint)}>
                      {t(m.label)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {/* THROUGH t(), like the SelectItem eleven lines up and like the
                  aspect-mode hint above. `hint` is a TranslationKey; rendered
                  bare it printed "rend.deintOffHint" at the operator. */}
              <span className="text-sm text-muted-foreground">
                {(() => {
                  const m = DEINTERLACE_MODES.find((m) => m.key === form.deinterlace);
                  return m ? t(m.hint) : "";
                })()}
              </span>
            </div>
          </div>

          {aspectApplies && form.aspectMode === "pad" && (
            <div className="flex flex-col gap-1">
              <Label htmlFor="rend-padcolor">{t("rend.letterboxColour")}</Label>
              <Input
                id="rend-padcolor"
                value={form.padColor}
                placeholder={t("rend.colourBlack")}
                onChange={(e) => set("padColor", e.target.value)}
              />
              <span className="text-sm text-muted-foreground">
                {tn("rend.padColorHint", {
                  black: <code>{t("rend.colourBlack")}</code>,
                  hex: <code>0x101010</code>,
                })}
              </span>
            </div>
          )}

          <div className="flex flex-col gap-2 rounded-md border border-border p-3">
            <div className="flex items-center justify-between gap-2">
              <Label htmlFor="rend-overlay-image">{t("rend.watermarkImage")}</Label>
              {!aspectApplies && (
                <span className="text-sm text-muted-foreground">
                  {t("rend.needsFixedSize")}
                </span>
              )}
            </div>
            <Input
              id="rend-overlay-image"
              value={form.overlayImage}
              disabled={!aspectApplies}
              placeholder={t("rend.watermarkPlaceholder")}
              onChange={(e) => set("overlayImage", e.target.value)}
            />
            <span className="text-sm text-muted-foreground">
              {tn("rend.overlayHint", { dir: <code>{OVERLAYS_DIR}</code> })}
            </span>

            {overlayApplies && (
              <>
                <div className="grid grid-cols-2 gap-2">
                  <AnchorGrid
                    value={form.overlayAnchor}
                    onChange={(v) => set("overlayAnchor", v)}
                  />
                  <div className="flex flex-col gap-1">
                    <Label htmlFor="rend-overlay-width">{t("rend.widthPct")}</Label>
                    <Input
                      id="rend-overlay-width"
                      type="number"
                      min={1}
                      max={100}
                      value={form.overlayWidth}
                      onChange={(e) => set("overlayWidth", Number(e.target.value))}
                    />
                  </div>
                </div>

                <div className="grid grid-cols-3 gap-2">
                  <div className="flex flex-col gap-1">
                    <Label htmlFor="rend-overlay-mx">{t("rend.marginX")}</Label>
                    <Input
                      id="rend-overlay-mx"
                      type="number"
                      min={0}
                      max={45}
                      value={form.overlayMarginX}
                      onChange={(e) => set("overlayMarginX", Number(e.target.value))}
                    />
                  </div>
                  <div className="flex flex-col gap-1">
                    <Label htmlFor="rend-overlay-my">{t("rend.marginY")}</Label>
                    <Input
                      id="rend-overlay-my"
                      type="number"
                      min={0}
                      max={45}
                      value={form.overlayMarginY}
                      onChange={(e) => set("overlayMarginY", Number(e.target.value))}
                    />
                  </div>
                  <div className="flex flex-col gap-1">
                    <Label htmlFor="rend-overlay-opacity">{t("rend.opacity")}</Label>
                    <Input
                      id="rend-overlay-opacity"
                      type="number"
                      min={1}
                      max={100}
                      value={form.overlayOpacity}
                      onChange={(e) => set("overlayOpacity", Number(e.target.value))}
                    />
                  </div>
                </div>

                <span className="text-sm text-muted-foreground">
            {t("rend.percentNote")}
                </span>
              </>
            )}
          </div>

          <div className="flex flex-col gap-2 rounded-md border border-border p-3">
            <div className="flex items-center justify-between gap-2">
              <Label htmlFor="rend-text-content">{t("rend.burnedText")}</Label>
              {!aspectApplies && (
                <span className="text-sm text-muted-foreground">
                  {t("rend.needsFixedSize")}
                </span>
              )}
              {aspectApplies && fonts && !fonts.textSupported && (
                <span className="text-sm text-warn">
                  {t("rend.noDrawtextShort")}
                </span>
              )}
            </div>
            <Input
              id="rend-text-content"
              value={form.textContent}
              disabled={!aspectApplies || (fonts ? !fonts.textSupported : false)}
              maxLength={200}
              placeholder={t("rend.textPlaceholder")}
              onChange={(e) => set("textContent", e.target.value)}
            />
            <span className="text-sm text-muted-foreground">
              {tn("rend.textHint", { pct: <code>%</code> })}
            </span>
            {fonts && !fonts.textSupported && (
              <span className="text-sm text-warn">
                {tn("rend.noFreetype", { filter: <code>drawtext</code> })}
              </span>
            )}

            {textApplies && (
              <>
                <div className="grid grid-cols-2 gap-2">
                  <div className="flex flex-col gap-1">
                    <Label>{t("rend.font")}</Label>
                    <Select
                      value={form.textFont === "" ? DEFAULT_FONT_KEY : form.textFont}
                      onValueChange={(v) => set("textFont", v === DEFAULT_FONT_KEY ? "" : v)}
                    >
                      <SelectTrigger>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value={DEFAULT_FONT_KEY}>{t("rend.builtInFont")}</SelectItem>
                        {(fonts?.fonts ?? []).map((f) => (
                          <SelectItem key={f.name} value={f.name}>
                            {f.name}
                            {f.builtIn ? ` ${t("rend.builtInSuffix")}` : ""}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                  <AnchorGrid
                    value={form.textAnchor}
                    onChange={(v) => set("textAnchor", v)}
                  />
                </div>
                <span className="text-sm text-muted-foreground">
                  {tn("rend.fontsHint", {
                    ttf: <code>.ttf</code>,
                    otf: <code>.otf</code>,
                    dir: <code>{FONTS_DIR}</code>,
                  })}
                </span>

                <div className="grid grid-cols-3 gap-2">
                  <div className="flex flex-col gap-1">
                    <Label htmlFor="rend-text-size">{t("rend.sizePct")}</Label>
                    <Input
                      id="rend-text-size"
                      type="number"
                      min={1}
                      max={50}
                      value={form.textSize}
                      onChange={(e) => set("textSize", Number(e.target.value))}
                    />
                  </div>
                  <div className="flex flex-col gap-1">
                    <Label htmlFor="rend-text-mx">{t("rend.marginX")}</Label>
                    <Input
                      id="rend-text-mx"
                      type="number"
                      min={0}
                      max={45}
                      value={form.textMarginX}
                      onChange={(e) => set("textMarginX", Number(e.target.value))}
                    />
                  </div>
                  <div className="flex flex-col gap-1">
                    <Label htmlFor="rend-text-my">{t("rend.marginY")}</Label>
                    <Input
                      id="rend-text-my"
                      type="number"
                      min={0}
                      max={45}
                      value={form.textMarginY}
                      onChange={(e) => set("textMarginY", Number(e.target.value))}
                    />
                  </div>
                </div>

                <div className="grid grid-cols-3 gap-2">
                  <div className="flex flex-col gap-1">
                    <Label htmlFor="rend-text-color">{t("rend.textColour")}</Label>
                    <Input
                      id="rend-text-color"
                      value={form.textColor}
                      placeholder={t("rend.colourWhite")}
                      onChange={(e) => set("textColor", e.target.value)}
                    />
                  </div>
                  <div className="flex flex-col gap-1">
                    <Label htmlFor="rend-text-boxcolor">{t("rend.boxColour")}</Label>
                    <Input
                      id="rend-text-boxcolor"
                      value={form.textBoxColor}
                      disabled={!form.textBox}
                      placeholder={t("rend.colourBlack")}
                      onChange={(e) => set("textBoxColor", e.target.value)}
                    />
                  </div>
                  <div className="flex flex-col gap-1">
                    <Label htmlFor="rend-text-boxopacity">{t("rend.boxOpacity")}</Label>
                    <Input
                      id="rend-text-boxopacity"
                      type="number"
                      min={0}
                      max={100}
                      value={form.textBoxOpacity}
                      disabled={!form.textBox}
                      onChange={(e) => set("textBoxOpacity", Number(e.target.value))}
                    />
                  </div>
                </div>

                <label className="flex items-center gap-2 text-xs">
                  <input
                    type="checkbox"
                    checked={form.textBox}
                    onChange={(e) => set("textBox", e.target.checked)}
                  />
                  {t("rend.drawBox")}
                </label>
                <span className="text-sm text-muted-foreground">
                  {tn("rend.boxHint", {
                    white: <code>{t("rend.colourWhite")}</code>,
                    hex: <code>0x101010</code>,
                  })}
                </span>
              </>
            )}
          </div>

          <div className="grid grid-cols-2 gap-2">
            <div className="flex flex-col gap-1">
              <div className="flex items-center justify-between gap-2">
                <Label>{t("rend.encoder")}</Label>
                <Button
                  variant="ghost"
                  size="sm"
                  type="button"
                  onClick={onRedetect}
                  disabled={redetecting}
                  title={t("rend.redetectTitle")}
                >
                  {redetecting ? <Loader2 className="animate-spin" /> : <ScanSearch />}
                  {t("rend.redetectHardware")}
                </Button>
              </div>
              <Select value={form.encoder} onValueChange={(v) => set("encoder", v)}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {choices.map((e) => (
                    <SelectItem
                      key={e.name}
                      value={e.name}
                      // Disabled, not hidden: the reason is the useful part. The
                      // one already saved stays selectable so an edit of an
                      // unrelated field is not blocked by it.
                      disabled={!e.works && e.name !== savedEncoder}
                      title={encoderProblem(t, e) || t("rend.encoderTestOk", { name: e.name })}
                      // Radix refuses to select a disabled item on its own, so
                      // the pointer events shadcn turns off here are only
                      // costing the title tooltip — which is the one thing a
                      // disabled encoder has to be able to show.
                      className="data-[disabled]:pointer-events-auto data-[disabled]:cursor-not-allowed"
                    >
                      {e.name} · {encoderLabel(t, e)}
                      {!e.works && e.reason ? ` — ${shortReason(e.reason)}` : ""}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {choices.length === 0 && (
                <span className="text-sm text-warn">
            {t("rend.noEncoder")}
                </span>
              )}
              {encoderProblem(t, encoder) && (
                <span className="text-sm text-down">{encoderProblem(t, encoder)}</span>
              )}
              {/* Only on NVENC, QSV, VA-API and AMF, and only about the FLAGS.
                  Gated on the encoder FAMILY rather than on `hardware`, which
                  is what it used to read and which made the badge false on
                  VideoToolbox — see flagsUnconfirmed. libx264/libx265 argv has
                  been running in production since before renditions existed,
                  and the VideoToolbox argv is covered by a real encode in
                  TestEveryConfiguredEncoderOpensWithItsOwnFlags. Nothing is
                  disabled here — the encoder stays selectable and the save
                  still works. */}
              {flagsUnconfirmed(encoder?.name) && (
                <Experimental>
                  {t("rend.flagsUnconfirmed", { encoder: encoder?.name ?? "" })}
                </Experimental>
              )}
              {!caps?.tested && choices.length > 0 && (
                <span className="text-sm text-muted-foreground">
            {t("rend.notProbed")}
                </span>
              )}
            </div>

            <div className="flex flex-col gap-1">
              <Label htmlFor="rend-preset">{t("rend.encoderPreset")}</Label>
              <Input
                id="rend-preset"
                value={form.preset}
                maxLength={32}
                className="font-mono"
                onChange={(e) => set("preset", e.target.value)}
                placeholder={t("rend.presetPlaceholder")}
              />
              <span className="text-sm text-muted-foreground">
                {t("rend.presetHint")}
              </span>
            </div>
          </div>

          {/* Why the hardware encoders above are or are not offered. A
              permission problem is called out first and loudest because it is
              the only one on this list the user can fix in one command. */}
          {diagnostics.length > 0 && (
            <div
              className={cn(
                "flex gap-2 rounded border px-2 py-1.5",
                permissionBlocked ? "border-down/50 bg-down/5" : "border-border bg-background",
              )}
            >
              <ShieldAlert
                className={cn(
                  "mt-0.5 h-3.5 w-3.5 shrink-0",
                  permissionBlocked ? "text-down" : "text-muted-foreground",
                )}
              />
              <div className="flex min-w-0 flex-col gap-0.5">
                <span
                  className={cn(
                    "text-sm font-medium",
                    permissionBlocked ? "text-down" : "text-foreground",
                  )}
                >
                  {permissionBlocked
                    ? t("rend.gpuUnopenable") : t("rend.hardwareScanFound")}
                </span>
                {diagnostics.map((d) => (
                  <span
                    key={d.text}
                    className={cn(
                      "text-sm",
                      d.permission ? "text-down" : "text-muted-foreground",
                    )}
                  >
                    {d.text}
                  </span>
                ))}
                {permissionBlocked && (
                  <span className="text-sm text-muted-foreground">
                    {t("rend.permissionHint")}
                  </span>
                )}
              </div>
            </div>
          )}

          {encoder?.codec === "hevc" && (
            <div className="rounded border border-warn/50 bg-warn/5 px-2 py-1.5 text-sm text-warn">
            {t("rend.hevcWarning")}
            </div>
          )}

          {/* The honest part. x264 at 4K60 in real time is not achievable on
              most machines, and finding that out mid-stream is miserable. */}
          <div className={cn("flex gap-2 rounded border px-2 py-1.5", COST_CLASSES[cost.tone].box)}>
            <Cpu
              className={cn("mt-0.5 h-3.5 w-3.5 shrink-0", COST_CLASSES[cost.tone].icon)}
            />
            <div className="flex min-w-0 flex-col gap-0.5">
              <span className={cn("text-sm font-medium", COST_CLASSES[cost.tone].title)}>
                {cost.headline}
              </span>
              <span className="text-sm text-muted-foreground">{cost.detail}</span>
              {notes.map((n) => (
                <span key={n} className="text-sm text-muted-foreground">
                  {n}
                </span>
              ))}
            </div>
          </div>

          <div className="flex flex-col gap-1">
            <Label htmlFor="rend-note">{t("rend.note")}</Label>
            <Textarea
              id="rend-note"
              rows={2}
              value={form.note}
              onChange={(e) => set("note", e.target.value)}
              placeholder={t("rend.notePlaceholder")}
            />
          </div>

          {/* WHO A SAVE RESTARTS. Named when the live list is in hand,
              counted when only the database's is, and neither claimed as the
              other -- an editor that says nothing because the socket is quiet
              is an editor that has hidden the consequence, not one that has
              none. */}
          {editing && consequence.kind !== "none" && (
            <div className="flex flex-col gap-1 rounded border border-border bg-background p-2">
              <span className="text-sm uppercase tracking-wider text-muted-foreground">
                {consequence.kind === "unknown"
                  ? t("rend.usersUnknown")
                  : t(
                      (consequence.kind === "named" ? consequence.users.length : consequence.total) === 1
                        ? "rend.feedsDestinationsOne"
                        : "rend.feedsDestinationsMany",
                      {
                        count:
                          consequence.kind === "named" ? consequence.users.length : consequence.total,
                      },
                    )}
              </span>
              {consequence.kind === "named" && (
                <div className="flex flex-wrap gap-1">
                  {consequence.users.map((d) => (
                    <DestChip key={d.id} dest={d} />
                  ))}
                </div>
              )}
              {consequence.kind === "counted" && (
                <span className="text-sm text-muted-foreground">{t("rend.usersNotNamed")}</span>
              )}
              {consequence.kind !== "unknown" && consequence.enabled > 0 && (
                <span className="text-sm text-warn">
                  {t(consequence.enabled === 1 ? "rend.saveRestartsOne" : "rend.saveRestartsMany", {
                    count: consequence.enabled,
                  })}
                </span>
              )}
            </div>
          )}
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button onClick={save} disabled={busy || !nameOk || !bitrateOk || !maxrateOk}>
            {busy && <Loader2 className="animate-spin" />}
            {editing ? t("common.save") : t("hooks.create")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** Deleting a rendition never deletes a destination — they fall back to
 *  passthrough and are handed the source unchanged, which is exactly the
 *  situation the rendition existed to avoid. So the consequence is spelled out
 *  by name before the button, not after it. */
function DeleteRenditionDialog({
  rendition,
  users,
  counts,
  onOpenChange,
  onDeleted,
}: {
  rendition: Rendition | null;
  /** null when the live list has not arrived. NOT `[]`: this dialog's whole
   *  job is the sentence "N destinations fall back to passthrough", and an
   *  empty array told the operator the opposite of it. */
  users: DestStatus[] | null;
  counts: { destinations: number; enabledDestinations: number } | null;
  onOpenChange: (open: boolean) => void;
  onDeleted: () => void;
}) {
  const t = useT();
  const [busy, setBusy] = useState(false);

  const remove = async () => {
    if (!rendition) return;
    setBusy(true);
    try {
      const res = await api.deleteRendition(rendition.id);
      toast.success(t("rend.deleted", { name: rendition.name }));
      if (res.warning) toast.warning(res.warning, { duration: 10000 });
      onDeleted();
      onOpenChange(false);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("rend.deleteFailed"));
    } finally {
      setBusy(false);
    }
  };

  const consequence = deleteConsequence(users, counts);

  return (
    <Dialog open={rendition !== null} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{t("rend.deleteRenditionTitle", { name: rendition?.name ?? "" })}</DialogTitle>
          <DialogDescription>
            {consequence.kind === "none"
              ? t("rend.deleteNoDestinations")
              : consequence.kind === "unknown"
                ? t("rend.deleteUsersUnknown")
                : t(
                    (consequence.kind === "named" ? consequence.users.length : consequence.total) === 1
                      ? "rend.deleteFallbackOne"
                      : "rend.deleteFallbackMany",
                    {
                      count:
                        consequence.kind === "named" ? consequence.users.length : consequence.total,
                    },
                  )}
          </DialogDescription>
        </DialogHeader>

        {consequence.kind === "counted" && (
          <p className="text-sm text-muted-foreground">{t("rend.usersNotNamed")}</p>
        )}

        {consequence.kind !== "none" && consequence.kind !== "unknown" && (
          <div className="flex flex-col gap-1.5">
            {consequence.kind === "named" && (
              <div className="flex flex-wrap gap-1">
                {consequence.users.map((d) => (
                  <DestChip key={d.id} dest={d} />
                ))}
              </div>
            )}
            {consequence.enabled > 0 && (
              <p className="rounded border border-warn/50 bg-warn/5 px-2 py-1.5 text-sm text-warn">
                {consequence.enabled === 1
                  ? t("i18nSweep.enabledOne", { count: consequence.enabled })
                  : t("i18nSweep.enabledMany", { count: consequence.enabled })}
              </p>
            )}
          </div>
        )}

        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button variant="destructive" onClick={remove} disabled={busy}>
            {busy && <Loader2 className="animate-spin" />}
            {t("i18nSweep.deleteRendition")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
