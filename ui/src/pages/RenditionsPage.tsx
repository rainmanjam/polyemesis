import { useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { Cpu, Layers, Loader2, Pencil, Plus, RotateCw, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
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
import { StatusDot } from "@/components/signature/StatusDot";
import { Stat } from "@/components/signature/Stat";
import { useLiveData } from "@/hooks/useLiveData";
import { api } from "@/lib/api";
import { duration, kbps } from "@/lib/format";
import { cn } from "@/lib/utils";
import { labelForState, toneBadge, toneForState, type SignalTone } from "@/lib/signal";
import type {
  DestStatus,
  EncoderInfo,
  Rendition,
  RenditionBounds,
  RenditionPreset,
  RenditionStatus,
  RenditionView,
  VideoStream,
} from "@/lib/types";

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
const FALLBACK_DISCLAIMER = "Starting point — verify current limits with the platform.";

/** The sizes worth one click. "Keep source" is 0×0, the sentinel that means
 *  "do not scale", and it stays first because it is the cheapest answer. */
const SIZES = [
  { key: "source", label: "Keep source size", width: 0, height: 0 },
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
function sizeLabel(r: { width: number; height: number }): string {
  if (r.width === 0 && r.height === 0) return "source size";
  if (r.width === 0) return `${r.height}p, aspect kept`;
  if (r.height === 0) return `${r.width} wide, aspect kept`;
  return `${r.width}×${r.height}`;
}

/** What this encode is likely to cost, said plainly.
 *
 *  Deliberately vague where being precise would be a lie: no percentage, no
 *  core count, no promise. What it will do is refuse to let someone pick x264
 *  at 4K60 without reading that it is not going to keep up. */
function encodeCost(
  r: { width: number; height: number; fps: number },
  encoder: EncoderInfo | undefined,
  src: VideoStream | null | undefined,
  cores?: number,
): Cost {
  const { width, height } = effectiveSize(r, src);
  const fps = effectiveFps(r.fps, src);
  const hardware = encoder?.hardware ?? false;

  if (width <= 0 || height <= 0 || fps <= 0) {
    return {
      tone: "muted",
      pixelRate: 0,
      headline: "Cost cannot be estimated yet",
      detail:
        "Nothing has probed the source, so “keep source” has no numbers behind it. Start the ingest and this fills in.",
    };
  }

  const rate = width * height * fps;
  const label = `${width}×${height} at ${fps} fps`;
  const mpps = `${Math.round(rate / 1e6)} MP/s`;
  const machine = cores && cores > 0 ? ` This machine reports ${cores} logical cores.` : "";

  if (hardware) {
    return {
      tone: rate >= SOFTWARE_UNREALISTIC_PIXELS ? "warn" : "muted",
      pixelRate: rate,
      headline: `Offloaded to ${encoder?.name ?? "hardware"}`,
      detail:
        `${label} (${mpps}) runs on the GPU's fixed-function encoder, so it costs little CPU. ` +
        "Quality and rate control vary by driver, and every chip has a ceiling of its own — " +
        "check the output before a show that matters.",
    };
  }

  if (rate >= SOFTWARE_UNREALISTIC_PIXELS) {
    return {
      tone: "down",
      pixelRate: rate,
      headline: "Software encoding this in real time is beyond most machines",
      detail:
        `${label} is ${mpps} for a single encode. x264 at this size usually loses the race with ` +
        "real time, and a rendition that falls behind drops frames for every destination reading " +
        `it — which you find out mid-stream.${machine} Choose a hardware encoder, or a smaller output.`,
    };
  }

  if (rate >= SOFTWARE_BUSY_PIXELS) {
    return {
      tone: "warn",
      pixelRate: rate,
      headline: "Expect several cores to be busy",
      detail:
        `${label} is ${mpps} on ${encoder?.name ?? "a software encoder"}, which is a heavy ` +
        `real-time encode.${machine} Watch the encoder's speed once it is live: below 1.0× it is ` +
        "falling behind and the destinations under it will start dropping frames.",
    };
  }

  return {
    tone: "muted",
    pixelRate: rate,
    headline: "Modest software encode",
    detail:
      `${label} is ${mpps}, within reach of most machines.${machine} It is still one real encode — ` +
      "shared by every destination that selects it, but not free.",
  };
}

/** Things that are worth saying about this rendition against this source, and
 *  are not opinions: upscaling and frame duplication are simply waste. */
function sourceNotes(
  r: { width: number; height: number; fps: number },
  src: VideoStream | null | undefined,
): string[] {
  if (!src || src.width <= 0 || src.height <= 0) return [];
  const notes: string[] = [];
  const { width, height } = effectiveSize(r, src);
  if (height > src.height || width > src.width) {
    notes.push(
      `The source is ${src.width}×${src.height}. Scaling up costs CPU and adds no detail.`,
    );
  }
  if (src.frameRate > 0 && r.fps > Math.round(src.frameRate)) {
    notes.push(
      `The source runs at ${src.frameRate.toFixed(2)} fps, so ${r.fps} fps only duplicates frames.`,
    );
  }
  return notes;
}

/** How a rendition should read at a glance.
 *
 *  No process is the normal state for a rendition nothing has selected — the
 *  ref count is doing its job — so it must not read as a failure. */
function renditionSignal(
  live: RenditionStatus | undefined,
  enabledUsers: number,
): { tone: SignalTone; label: string } {
  if (live?.process) {
    return { tone: toneForState(live.process.state), label: labelForState(live.process.state) };
  }
  if (live?.error) return { tone: "down", label: "Error" };
  if (enabledUsers === 0) return { tone: "idle", label: "Idle" };
  return { tone: "warn", label: "Starting" };
}

export function RenditionsPage() {
  const { status, system } = useLiveData();
  const [views, setViews] = useState<RenditionView[]>([]);
  const [encoders, setEncoders] = useState<EncoderInfo[]>([]);
  const [defaultEncoder, setDefaultEncoder] = useState("libx264");
  const [presets, setPresets] = useState<RenditionPreset[]>([]);
  const [disclaimer, setDisclaimer] = useState(FALLBACK_DISCLAIMER);
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
          toast.error(err instanceof Error ? err.message : "Could not load the renditions."),
        )
        .finally(() => setLoading(false)),
    [],
  );

  // The encoder list and the presets are properties of the install, not of the
  // data, so they are fetched once rather than on every refresh.
  useEffect(() => {
    api
      .listEncoders()
      .then((e) => {
        setEncoders(e.encoders);
        setDefaultEncoder(e.default);
      })
      .catch(() => setEncoders([]));
    api
      .renditionPresets()
      .then((p) => {
        setPresets(p.presets);
        setDisclaimer(p.disclaimer || FALLBACK_DISCLAIMER);
        setBounds(p.bounds ?? FALLBACK_BOUNDS);
      })
      .catch(() => {});
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

  const passthrough = (status?.destinations ?? []).filter((d) => d.renditionId == null);
  const running = (status?.renditions ?? []).filter((r) => r.process?.state === "running").length;
  const sourceVideo = status?.source.video ?? null;

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
      toast.success(`${r.name} is restarting. Its destinations reconnect with it.`);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Could not restart the encode.");
    } finally {
      setBusyId(null);
    }
  };

  return (
    <div className="p-3">
      <PageHeader
        title="Renditions"
        subtitle="One shared video encode, many destinations. Audio is never touched here."
        actions={
          <>
            {running > 0 && (
              <Badge variant="live">
                <StatusDot tone="live" size="sm" />
                {running} encoding
              </Badge>
            )}
            <Button size="sm" onClick={openCreate}>
              <Plus /> New rendition
            </Button>
          </>
        }
      />

      <Card className="mb-3">
        <CardHeader>
          <CardTitle className="flex items-center gap-1.5">
            <Layers className="h-3.5 w-3.5" /> How this works
          </CardTitle>
          <CardDescription>
            A rendition re-encodes <strong className="font-semibold">video only</strong> and copies
            every audio track through it untouched. Destinations still do{" "}
            <code className="font-mono">-c:v copy</code> plus their own routing graph, so
            per-destination audio keeps working on top of a shared picture and no audio is ever
            encoded twice. Three destinations that all need 1080p60 cost one encode, not three.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <div className="flex flex-wrap items-center gap-2 rounded border border-border bg-background px-2 py-1.5">
            <Badge variant="outline">passthrough</Badge>
            <span className="text-[11px] text-muted-foreground">
              Destinations with no rendition take the source untouched — no process, no CPU. That is
              the default, and where every destination starts.
            </span>
            <span className="tnum ml-auto font-mono text-[11px]">{passthrough.length}</span>
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
              No renditions. Every destination is on passthrough, which is exactly right until one
              of them cannot take the source — a 4K60 ingest that Twitch, Kick or X will not accept,
              say. Add a rendition and point those destinations at it.
            </p>
            <Button size="sm" onClick={openCreate}>
              <Plus /> New rendition
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
        encoders={encoders}
        defaultEncoder={defaultEncoder}
        presets={presets}
        disclaimer={disclaimer}
        bounds={bounds}
        source={sourceVideo}
        cores={system?.numCpu}
        users={editing ? (usersById.get(editing.id) ?? []) : []}
        onSaved={loadRenditions}
      />

      <DeleteRenditionDialog
        rendition={deleting}
        users={deleting ? (usersById.get(deleting.id) ?? []) : []}
        onOpenChange={(open) => !open && setDeleting(null)}
        onDeleted={loadRenditions}
      />
    </div>
  );
}

/** One destination, as a chip. Disabled rows are drawn too: they still select
 *  this rendition, and deleting it affects them the moment they are enabled. */
function DestChip({ dest }: { dest: DestStatus }) {
  const tone: SignalTone = dest.enabled ? toneForState(dest.process?.state) : "idle";
  return (
    <span
      className="inline-flex items-center gap-1.5 rounded border border-border bg-background px-1.5 py-0.5 text-[11px]"
      title={dest.enabled ? labelForState(dest.process?.state) : "Disabled"}
    >
      <StatusDot tone={tone} size="sm" />
      <span className="max-w-40 truncate">{dest.name}</span>
    </span>
  );
}

function RenditionCard({
  view,
  live,
  users,
  encoder,
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
  source: VideoStream | null;
  cores?: number;
  busy: boolean;
  onEdit: () => void;
  onRestart: () => void;
  onDelete: () => void;
}) {
  const r = view.rendition;
  const total = users ? users.length : view.destinations;
  const enabled = users ? users.filter((d) => d.enabled).length : view.enabledDestinations;
  const signal = renditionSignal(live, enabled);
  const cost = encodeCost(r, encoder, source, cores);
  const proc = live?.process;

  return (
    <Card>
      <CardHeader className="flex-row items-start justify-between gap-2">
        <div className="min-w-0">
          <CardTitle className="flex items-center gap-2">
            <StatusDot tone={signal.tone} />
            <span className="truncate">{r.name}</span>
          </CardTitle>
          <CardDescription className="font-mono">
            {sizeLabel(r)}
            {r.fps > 0 ? ` · ${r.fps} fps` : " · source fps"} · {r.encoder}
          </CardDescription>
        </div>
        <Badge variant={toneBadge[signal.tone]}>{signal.label}</Badge>
      </CardHeader>

      <CardContent className="flex flex-col gap-2.5">
        <div className="grid grid-cols-3 gap-2">
          <Stat label="Target" value={kbps(r.videoBitrate)} />
          <Stat
            label="Live"
            value={proc?.state === "running" ? kbps(proc.progress?.bitrateKbps ?? 0) : "—"}
            tone={proc?.state === "running" ? "live" : "muted"}
          />
          <Stat
            label="Speed"
            value={proc?.state === "running" ? `${(proc.progress?.speed ?? 0).toFixed(2)}×` : "—"}
            // Below real time the encode is losing the race, and every
            // destination under it inherits the stutter.
            tone={
              proc?.state === "running" && (proc.progress?.speed ?? 0) < 0.98 ? "warn" : "muted"
            }
          />
          <Stat label="GOP" value={`${r.gopSeconds}s`} tone="muted" />
          <Stat label="Preset" value={r.preset} tone="muted" />
          <Stat
            label="Uptime"
            value={proc?.state === "running" ? duration(proc.uptimeSec) : "—"}
            tone="muted"
          />
        </div>

        {r.note && <p className="text-[11px] text-muted-foreground">{r.note}</p>}

        <div>
          <div className="mb-1 flex items-center justify-between">
            <span className="text-[9px] uppercase tracking-wider text-muted-foreground">
              Feeds
            </span>
            <span className="tnum font-mono text-[10px] text-muted-foreground">
              {enabled} of {total} enabled
            </span>
          </div>
          {total === 0 ? (
            <p className="text-[11px] text-muted-foreground">
              No destination selects this yet, so nothing is being encoded. Pick it on a destination
              to start the encode.
            </p>
          ) : users ? (
            <div className="flex flex-wrap gap-1">
              {users.map((d) => (
                <DestChip key={d.id} dest={d} />
              ))}
            </div>
          ) : (
            <p className="text-[11px] text-muted-foreground">
              Waiting for the first live snapshot.
            </p>
          )}
        </div>

        {live?.error && (
          <div className="rounded border border-down/30 bg-down-dim px-2 py-1 text-[10px] text-down">
            {live.error}
          </div>
        )}
        {proc?.lastError && proc.state !== "running" && (
          <div className="rounded border border-down/30 bg-down-dim px-2 py-1 text-[10px] text-down">
            {proc.lastError}
          </div>
        )}

        {/* Only the verdict worth interrupting for. Everything else the cost
            model has to say lives in the editor, next to the controls. */}
        {cost.tone === "down" && (
          <div className="rounded border border-warn/30 bg-warn-dim px-2 py-1 text-[10px] text-warn">
            {cost.headline}. {cost.detail}
          </div>
        )}

        <div className="flex items-center gap-1 border-t border-border pt-2">
          <Button variant="outline" size="sm" onClick={onEdit}>
            <Pencil /> Edit
          </Button>
          <Button
            variant="ghost"
            size="sm"
            onClick={onRestart}
            disabled={busy || !proc}
            title={proc ? "Restart this encode and the destinations reading it" : "Not running"}
          >
            {busy ? <Loader2 className="animate-spin" /> : <RotateCw />} Restart
          </Button>
          <Button
            variant="ghost"
            size="icon-sm"
            className="ml-auto text-muted-foreground hover:text-down"
            onClick={onDelete}
            aria-label={`Delete ${r.name}`}
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
    encoder: defaultEncoder,
    preset: "veryfast",
    gopSeconds: 2,
    note: "",
  };
}

function sizeKeyFor(width: number, height: number): string {
  const match = SIZES.find((s) => s.width === width && s.height === height);
  return match ? match.key : "custom";
}

function RenditionDialog({
  open,
  onOpenChange,
  rendition,
  encoders,
  defaultEncoder,
  presets,
  disclaimer,
  bounds,
  source,
  cores,
  users,
  onSaved,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** The rendition being edited, or null to create one. */
  rendition: Rendition | null;
  encoders: EncoderInfo[];
  defaultEncoder: string;
  presets: RenditionPreset[];
  disclaimer: string;
  bounds: RenditionBounds;
  source: VideoStream | null;
  cores?: number;
  users: DestStatus[];
  onSaved: () => void;
}) {
  const editing = rendition !== null;
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
        encoder: rendition.encoder,
        preset: rendition.preset,
        gopSeconds: rendition.gopSeconds,
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
      // The preset names a software encoder because that is the one every
      // build has. If this machine's default is something else, respect it.
      encoder: encoders.some((e) => e.name === t.encoder && e.available)
        ? t.encoder
        : defaultEncoder,
      preset: t.preset,
      gopSeconds: t.gopSeconds,
      note: t.note,
    });
    setSizeKey(sizeKeyFor(t.width, t.height));
  };

  const chooseSize = (key: string) => {
    setSizeKey(key);
    const size = SIZES.find((s) => s.key === key);
    if (size) setForm((f) => ({ ...f, width: size.width, height: size.height }));
  };

  // Only encoders this FFmpeg registers are offered. The one exception is the
  // encoder already saved on the rendition being edited: the row was legal when
  // it was written, and silently swapping it under the user would be worse than
  // showing it greyed with the reason.
  const choices = useMemo(() => {
    const usable = encoders.filter((e) => e.available);
    if (editing && rendition && !usable.some((e) => e.name === rendition.encoder)) {
      const saved = encoders.find((e) => e.name === rendition.encoder);
      return saved ? [saved, ...usable] : usable;
    }
    return usable;
  }, [encoders, editing, rendition]);

  const encoder = encoders.find((e) => e.name === form.encoder);
  const cost = encodeCost(form, encoder, source, cores);
  const notes = sourceNotes(form, source);
  const enabledUsers = users.filter((d) => d.enabled).length;

  const nameOk = form.name.trim().length > 0;
  const bitrateOk =
    form.videoBitrate >= bounds.minBitrate && form.videoBitrate <= bounds.maxBitrate;

  const save = async () => {
    setBusy(true);
    try {
      const payload: Partial<Rendition> = {
        name: form.name.trim(),
        width: form.width,
        height: form.height,
        fps: form.fps,
        videoBitrate: form.videoBitrate,
        encoder: form.encoder,
        preset: form.preset.trim(),
        gopSeconds: form.gopSeconds,
        note: form.note.trim(),
      };
      if (rendition) {
        await api.updateRendition(rendition.id, payload);
        toast.success(
          enabledUsers > 0
            ? `${payload.name} saved. The encode and its ${enabledUsers} destination${enabledUsers === 1 ? "" : "s"} are restarting.`
            : `${payload.name} saved.`,
        );
      } else {
        await api.createRendition(payload);
        toast.success("Rendition created. Select it on a destination to start the encode.");
      }
      onSaved();
      onOpenChange(false);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Could not save the rendition.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{editing ? "Edit rendition" : "New rendition"}</DialogTitle>
          <DialogDescription>
            Video only. Every audio track is copied through untouched, and each destination applies
            its own routing on top — so this changes the picture for everyone that selects it and
            changes nobody's audio.
          </DialogDescription>
        </DialogHeader>

        <div className="flex flex-col gap-3">
          {presets.length > 0 && (
            <div className="flex flex-col gap-1.5 rounded border border-border bg-background p-2">
              <span className="text-[9px] uppercase tracking-wider text-muted-foreground">
                Starting points
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
              <span className="text-[10px] text-warn">{disclaimer}</span>
              <span className="text-[10px] text-muted-foreground">
                These seed the form and nothing more — edit anything before saving. Platform
                ceilings change and differ by partner status.
              </span>
            </div>
          )}

          <div className="flex flex-col gap-1">
            <Label htmlFor="rend-name">Name</Label>
            <Input
              id="rend-name"
              value={form.name}
              onChange={(e) => set("name", e.target.value)}
              placeholder="1080p60 for Twitch and Kick"
            />
          </div>

          <div className="grid grid-cols-2 gap-2">
            <div className="flex flex-col gap-1">
              <Label>Resolution</Label>
              <Select value={sizeKey} onValueChange={chooseSize}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {SIZES.map((s) => (
                    <SelectItem key={s.key} value={s.key}>
                      {s.label}
                    </SelectItem>
                  ))}
                  <SelectItem value="custom">Custom…</SelectItem>
                </SelectContent>
              </Select>
            </div>

            <div className="flex flex-col gap-1">
              <Label htmlFor="rend-fps">Frame rate</Label>
              <Input
                id="rend-fps"
                type="number"
                min={0}
                max={bounds.maxFps}
                value={form.fps}
                onChange={(e) => set("fps", Number(e.target.value))}
              />
              <span className="text-[10px] text-muted-foreground">
                fps, or 0 to keep the source's.
              </span>
            </div>
          </div>

          {sizeKey === "custom" && (
            <div className="grid grid-cols-2 gap-2">
              <div className="flex flex-col gap-1">
                <Label htmlFor="rend-w">Width</Label>
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
                <Label htmlFor="rend-h">Height</Label>
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
              <span className="col-span-2 text-[10px] text-muted-foreground">
                0 on an axis keeps the source's and preserves the aspect ratio — set height alone to
                rescale. Both must be even numbers: H.264 and HEVC have no odd-sized chroma plane.
              </span>
            </div>
          )}

          <div className="grid grid-cols-2 gap-2">
            <div className="flex flex-col gap-1">
              <Label htmlFor="rend-bitrate">Video bitrate</Label>
              <Input
                id="rend-bitrate"
                type="number"
                min={bounds.minBitrate}
                max={bounds.maxBitrate}
                step={100}
                value={form.videoBitrate}
                onChange={(e) => set("videoBitrate", Number(e.target.value))}
              />
              <span className={`text-[10px] ${bitrateOk ? "text-muted-foreground" : "text-down"}`}>
                kbps, video only — audio is copied and costs nothing here.
              </span>
            </div>

            <div className="flex flex-col gap-1">
              <Label htmlFor="rend-gop">Keyframe interval</Label>
              <Input
                id="rend-gop"
                type="number"
                min={bounds.minGopSeconds}
                max={bounds.maxGopSeconds}
                step={0.5}
                value={form.gopSeconds}
                onChange={(e) => set("gopSeconds", Number(e.target.value))}
              />
              <span className="text-[10px] text-muted-foreground">
                seconds. Most live platforms want 1–4; 2 is the common answer.
              </span>
            </div>
          </div>

          <div className="grid grid-cols-2 gap-2">
            <div className="flex flex-col gap-1">
              <Label>Encoder</Label>
              <Select value={form.encoder} onValueChange={(v) => set("encoder", v)}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {choices.map((e) => (
                    <SelectItem key={e.name} value={e.name}>
                      {e.name} · {e.hardware ? "hardware" : "software"}
                      {e.available ? "" : " · not on this machine"}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {choices.length === 0 && (
                <span className="text-[10px] text-warn">
                  This FFmpeg reported no usable video encoder.
                </span>
              )}
            </div>

            <div className="flex flex-col gap-1">
              <Label htmlFor="rend-preset">Encoder preset</Label>
              <Input
                id="rend-preset"
                value={form.preset}
                maxLength={32}
                className="font-mono"
                onChange={(e) => set("preset", e.target.value)}
                placeholder="veryfast"
              />
              <span className="text-[10px] text-muted-foreground">
                The encoder's own speed knob, and its vocabulary: veryfast for x264, p4 for nvenc.
              </span>
            </div>
          </div>

          {encoder?.codec === "hevc" && (
            <div className="rounded border border-warn/50 bg-warn/5 px-2 py-1.5 text-[10px] text-warn">
              This encoder produces HEVC. Most RTMP destinations accept H.264 only — pick it only
              for a destination that has told you it takes HEVC.
            </div>
          )}

          {/* The honest part. x264 at 4K60 in real time is not achievable on
              most machines, and finding that out mid-stream is miserable. */}
          <div className={cn("flex gap-2 rounded border px-2 py-1.5", COST_CLASSES[cost.tone].box)}>
            <Cpu
              className={cn("mt-0.5 h-3.5 w-3.5 shrink-0", COST_CLASSES[cost.tone].icon)}
            />
            <div className="flex min-w-0 flex-col gap-0.5">
              <span className={cn("text-[11px] font-medium", COST_CLASSES[cost.tone].title)}>
                {cost.headline}
              </span>
              <span className="text-[10px] text-muted-foreground">{cost.detail}</span>
              {notes.map((n) => (
                <span key={n} className="text-[10px] text-muted-foreground">
                  {n}
                </span>
              ))}
            </div>
          </div>

          <div className="flex flex-col gap-1">
            <Label htmlFor="rend-note">Note</Label>
            <Textarea
              id="rend-note"
              rows={2}
              value={form.note}
              onChange={(e) => set("note", e.target.value)}
              placeholder="What this tier is for."
            />
          </div>

          {editing && users.length > 0 && (
            <div className="flex flex-col gap-1 rounded border border-border bg-background p-2">
              <span className="text-[9px] uppercase tracking-wider text-muted-foreground">
                Feeds {users.length} destination{users.length === 1 ? "" : "s"}
              </span>
              <div className="flex flex-wrap gap-1">
                {users.map((d) => (
                  <DestChip key={d.id} dest={d} />
                ))}
              </div>
              {enabledUsers > 0 && (
                <span className="text-[10px] text-warn">
                  Saving restarts this encode, and with it the {enabledUsers} enabled destination
                  {enabledUsers === 1 ? "" : "s"} above. Their audio routing is untouched.
                </span>
              )}
            </div>
          )}
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button onClick={save} disabled={busy || !nameOk || !bitrateOk}>
            {busy && <Loader2 className="animate-spin" />}
            {editing ? "Save" : "Create"}
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
  onOpenChange,
  onDeleted,
}: {
  rendition: Rendition | null;
  users: DestStatus[];
  onOpenChange: (open: boolean) => void;
  onDeleted: () => void;
}) {
  const [busy, setBusy] = useState(false);

  const remove = async () => {
    if (!rendition) return;
    setBusy(true);
    try {
      const res = await api.deleteRendition(rendition.id);
      toast.success(`${rendition.name} deleted.`);
      if (res.warning) toast.warning(res.warning, { duration: 10000 });
      onDeleted();
      onOpenChange(false);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Could not delete the rendition.");
    } finally {
      setBusy(false);
    }
  };

  const enabled = users.filter((d) => d.enabled).length;

  return (
    <Dialog open={rendition !== null} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>Delete {rendition?.name}?</DialogTitle>
          <DialogDescription>
            {users.length === 0
              ? "Nothing selects this rendition, so deleting it changes no destination."
              : `${users.length} destination${users.length === 1 ? "" : "s"} fall back to passthrough and will be sent the source video unchanged. Check the source still fits what each platform accepts.`}
          </DialogDescription>
        </DialogHeader>

        {users.length > 0 && (
          <div className="flex flex-col gap-1.5">
            <div className="flex flex-wrap gap-1">
              {users.map((d) => (
                <DestChip key={d.id} dest={d} />
              ))}
            </div>
            {enabled > 0 && (
              <p className="rounded border border-warn/50 bg-warn/5 px-2 py-1.5 text-[10px] text-warn">
                {enabled} of them {enabled === 1 ? "is" : "are"} enabled and will restart
                immediately on the source video. None of them is deleted, and their audio routing is
                untouched.
              </p>
            )}
          </div>
        )}

        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button variant="destructive" onClick={remove} disabled={busy}>
            {busy && <Loader2 className="animate-spin" />}
            Delete rendition
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
