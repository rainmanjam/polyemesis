import { Suspense, lazy, useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { Copy, Megaphone, Plus, Radio } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { PageHeader } from "@/components/AppLayout";
import { DestinationCard } from "@/components/DestinationCard";
import { DestinationDialog } from "@/components/DestinationDialog";
import { StatusDot } from "@/components/signature/StatusDot";
import { Stat } from "@/components/signature/Stat";
import { useLiveData } from "@/hooks/useLiveData";
import { api } from "@/lib/api";
import { duration, kbps } from "@/lib/format";
import { labelForState, toneBadge, toneForState } from "@/lib/signal";
import type { SignalTone } from "@/lib/signal";
import type { Destination, SystemInfo } from "@/lib/types";

// hls.js is a few hundred kilobytes that only the preview needs, and the
// preview is off entirely for some installs. Load it alongside the dashboard
// rather than ahead of it.
const PreviewPlayer = lazy(() =>
  import("@/components/PreviewPlayer").then((m) => ({ default: m.PreviewPlayer })),
);

// ---------------------------------------------------------- go-live composer
//
// Set the title, description and category once and push them to every
// connected account. The shapes below mirror internal/api/metadata.go; they
// live here rather than in lib/types.ts because nothing else renders them.

type MetaField = "title" | "description" | "category";
type MetaState = "pending" | "ok" | "partial" | "error";

interface MetaCaps {
  fields: MetaField[];
  categoryLabel?: string;
  categoryHint?: string;
  titleMax?: number;
  descriptionMax?: number;
}

interface MetaTarget {
  accountId: number;
  platform: string;
  accountName: string;
  caps: MetaCaps;
}

interface MetaOutcome {
  accountId: number;
  platform: string;
  accountName: string;
  state: MetaState;
  message?: string;
  applied: MetaField[];
  skipped?: MetaField[];
  target?: string;
  category?: string;
  warnings?: string[];
}

interface MetaJob {
  id: string;
  done: boolean;
  results: MetaOutcome[];
  metadata: { title: string; description: string; category: string };
}

/** The double-submit CSRF token, read the way lib/api.ts reads it. */
function csrfToken(): string {
  const match = document.cookie.match(/(?:^|;\s*)polyemesis_csrf=([^;]+)/);
  return match ? decodeURIComponent(match[1]) : "";
}

async function metaFetch<T>(path: string, body?: unknown): Promise<T> {
  const headers = new Headers();
  if (body !== undefined) {
    headers.set("Content-Type", "application/json");
    headers.set("X-CSRF-Token", csrfToken());
  }
  const resp = await fetch("/api/v1" + path, {
    method: body === undefined ? "GET" : "POST",
    headers,
    credentials: "same-origin",
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await resp.text();
  const parsed: unknown = text ? JSON.parse(text) : null;
  if (!resp.ok) {
    const msg =
      parsed && typeof parsed === "object" && "error" in parsed
        ? String((parsed as { error: unknown }).error)
        : `request failed (${resp.status})`;
    throw new Error(msg);
  }
  return parsed as T;
}

// A push is reported per platform, never as one boolean, so each state needs
// its own place in the signal language: still working, done, done with
// something left undone, and refused.
const metaTone: Record<MetaState, SignalTone> = {
  pending: "armed",
  ok: "live",
  partial: "warn",
  error: "down",
};

const metaLabel: Record<MetaState, string> = {
  pending: "Pushing",
  ok: "Updated",
  partial: "Partial",
  error: "Failed",
};

function GoLiveComposer() {
  const [targets, setTargets] = useState<MetaTarget[] | null>(null);
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [category, setCategory] = useState("");
  const [job, setJob] = useState<MetaJob | null>(null);
  const [pushing, setPushing] = useState(false);

  useEffect(() => {
    let live = true;
    metaFetch<{ targets: MetaTarget[]; last?: MetaJob }>("/metadata")
      .then((data) => {
        if (!live) return;
        setTargets(data.targets);
        // Restoring the last push means a reloaded tab still shows which
        // platforms took the title and which did not.
        if (data.last) {
          setJob(data.last);
          setTitle(data.last.metadata.title);
          setDescription(data.last.metadata.description);
          setCategory(data.last.metadata.category);
        }
      })
      .catch(() => setTargets([]));
    return () => {
      live = false;
    };
  }, []);

  // The push is a job precisely so a slow platform API cannot hold the page,
  // so the page has to poll it back.
  const jobId = job?.id;
  const jobDone = job?.done ?? true;
  useEffect(() => {
    if (!jobId || jobDone) return;
    let live = true;
    const timer = window.setInterval(() => {
      metaFetch<MetaJob>(`/metadata/push/${jobId}`)
        .then((next) => {
          if (live) setJob(next);
        })
        .catch(() => {});
    }, 1200);
    return () => {
      live = false;
      window.clearInterval(timer);
    };
  }, [jobId, jobDone]);

  const accepts = useCallback(
    (field: MetaField) => (targets ?? []).filter((t) => t.caps.fields.includes(field)),
    [targets],
  );

  // The counter shows the tightest limit among the platforms being pushed to,
  // because that is the one that will refuse first.
  const titleMax = useMemo(() => {
    const limits = (targets ?? []).map((t) => t.caps.titleMax ?? 0).filter((n) => n > 0);
    return limits.length > 0 ? Math.min(...limits) : 0;
  }, [targets]);

  const overLimit = useMemo(
    () =>
      (targets ?? []).filter((t) => {
        const max = t.caps.titleMax ?? 0;
        return max > 0 && title.length > max;
      }),
    [targets, title],
  );

  const categoryHint = (targets ?? []).find((t) => t.caps.categoryHint)?.caps.categoryHint ?? "";
  const noDescription = (targets ?? []).filter((t) => !t.caps.fields.includes("description"));

  const push = async () => {
    setPushing(true);
    try {
      const started = await metaFetch<MetaJob>("/metadata/push", { title, description, category });
      setJob(started);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Could not push the metadata.");
    } finally {
      setPushing(false);
    }
  };

  if (targets === null) return null;

  const empty = !title.trim() && !description.trim() && !category.trim();
  const busy = pushing || (job !== null && !job.done);

  return (
    <Card className="mt-4">
      <CardHeader className="flex-row items-center justify-between">
        <CardTitle className="flex items-center gap-2">
          <Megaphone className="h-3.5 w-3.5 text-muted-foreground" />
          Go live
        </CardTitle>
        <span className="font-mono text-[10px] text-muted-foreground">
          {targets.length === 1 ? "1 account" : `${targets.length} accounts`}
        </span>
      </CardHeader>

      {targets.length === 0 ? (
        <CardContent>
          <p className="text-[12px] text-muted-foreground">
            Connect a YouTube or Twitch account in Settings → Platforms to set your stream title,
            description and category on every platform at once.
          </p>
        </CardContent>
      ) : (
        <CardContent className="grid gap-4 lg:grid-cols-2">
          {/* ---------- the one form ---------- */}
          <div className="flex flex-col gap-2.5">
            <div className="flex flex-col gap-1">
              <div className="flex items-baseline justify-between">
                <Label htmlFor="golive-title">Title</Label>
                {titleMax > 0 && (
                  <span
                    className={`tnum font-mono text-[10px] ${
                      overLimit.length > 0 ? "text-down" : "text-muted-foreground"
                    }`}
                  >
                    {title.length}/{titleMax}
                  </span>
                )}
              </div>
              <Input
                id="golive-title"
                value={title}
                placeholder="Friday night set"
                onChange={(e) => setTitle(e.target.value)}
              />
              {overLimit.length > 0 && (
                <p className="text-[10px] text-down">
                  Too long for {overLimit.map((t) => t.platform).join(", ")}; that platform will be
                  reported as failed and the others still pushed.
                </p>
              )}
            </div>

            <div className="flex flex-col gap-1">
              <Label htmlFor="golive-description">Description</Label>
              <Textarea
                id="golive-description"
                value={description}
                rows={3}
                placeholder="What this stream is."
                onChange={(e) => setDescription(e.target.value)}
              />
              {noDescription.length > 0 && (
                <p className="text-[10px] text-muted-foreground">
                  {noDescription.map((t) => t.platform).join(", ")} has no description field, so
                  this is skipped there rather than failed.
                </p>
              )}
            </div>

            <div className="flex flex-col gap-1">
              <Label htmlFor="golive-category">Category</Label>
              <Input
                id="golive-category"
                value={category}
                placeholder="Music"
                onChange={(e) => setCategory(e.target.value)}
              />
              {categoryHint && (
                <p className="text-[10px] text-muted-foreground">
                  {categoryHint} Type the name — polyemesis looks up the id.
                </p>
              )}
            </div>

            <div className="flex items-center gap-2">
              <Button size="sm" onClick={push} disabled={empty || busy}>
                <Megaphone /> {busy ? "Pushing…" : "Push to platforms"}
              </Button>
              <span className="text-[10px] text-muted-foreground">
                Applies to {accepts("title").length === 1 ? "the connected account" : "every connected account"}.
              </span>
            </div>
          </div>

          {/* ---------- what each platform did ---------- */}
          <div aria-live="polite" className="flex flex-col gap-1.5">
            {job === null ? (
              <p className="text-[11px] text-muted-foreground">
                Results appear here, one row per platform.
              </p>
            ) : (
              job.results.map((res) => {
                const tone = metaTone[res.state];
                return (
                  <div
                    key={res.accountId}
                    className="flex flex-col gap-0.5 rounded border border-border px-2 py-1.5"
                  >
                    <div className="flex items-center justify-between gap-2">
                      <div className="flex min-w-0 items-center gap-2">
                        <StatusDot tone={tone} size="sm" />
                        <span className="truncate text-[11px] font-medium">{res.platform}</span>
                        <span className="truncate font-mono text-[10px] text-muted-foreground">
                          {res.accountName}
                        </span>
                      </div>
                      <Badge variant={toneBadge[tone]}>{metaLabel[res.state]}</Badge>
                    </div>

                    {res.applied.length > 0 && (
                      <p className="text-[10px] text-muted-foreground">
                        Set {res.applied.join(", ")}
                        {res.category && ` — category “${res.category}”`}
                        {res.target && ` on ${res.target}`}
                      </p>
                    )}
                    {res.skipped && res.skipped.length > 0 && (
                      <p className="text-[10px] text-muted-foreground">
                        Not supported here: {res.skipped.join(", ")}
                      </p>
                    )}
                    {res.message && <p className="text-[10px] text-down">{res.message}</p>}
                    {res.warnings?.map((warn) => (
                      <p key={warn} className="text-[10px] text-warn">
                        {warn}
                      </p>
                    ))}
                  </div>
                );
              })
            )}
          </div>
        </CardContent>
      )}
    </Card>
  );
}

export function Dashboard() {
  const { status } = useLiveData();
  const [system, setSystem] = useState<SystemInfo | null>(null);
  const [settingsPreview, setSettingsPreview] = useState(true);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editing, setEditing] = useState<Destination | null>(null);
  const [busyId, setBusyId] = useState<number | null>(null);
  const [refreshKey, setRefreshKey] = useState(0);
  const [pending, setPending] = useState<number[] | null>(null);
  const [moveNote, setMoveNote] = useState("");

  useEffect(() => {
    api.system().then(setSystem).catch(() => {});
    api
      .getSettings()
      .then((s) => setSettingsPreview(s.preview.enabled))
      .catch(() => {});
  }, [refreshKey]);

  const act = useCallback(
    async (id: number, fn: () => Promise<unknown>, label: string) => {
      setBusyId(id);
      try {
        await fn();
      } catch (err) {
        toast.error(err instanceof Error ? err.message : `Could not ${label}.`);
      } finally {
        setBusyId(null);
      }
    },
    [],
  );

  const openEdit = async (id: number) => {
    try {
      const { destination } = await api.getDestination(id);
      setEditing(destination);
      setDialogOpen(true);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Could not load the destination.");
    }
  };

  const remove = async (id: number, name: string) => {
    if (!window.confirm(`Delete "${name}"? This cannot be undone.`)) return;
    await act(id, () => api.deleteDestination(id), "delete the destination");
    toast.success("Destination deleted.");
    setRefreshKey((k) => k + 1);
  };

  const ingest = status?.ingest;
  const ingestTone = toneForState(ingest?.state);
  const source = status?.source;
  const live = status?.destinations;
  const renditions = status?.renditions ?? [];

  // The socket republishes status on a two-second cadence, so a move applied
  // only on the server would leave the card sitting still after the click.
  // Hold the requested order locally until the server's own order agrees.
  const destinations = useMemo(() => {
    const rows = live ?? [];
    if (!pending) return rows;
    const rank = new Map(pending.map((id, i) => [id, i]));
    return [...rows].sort(
      (a, b) =>
        (rank.get(a.id) ?? Number.MAX_SAFE_INTEGER) - (rank.get(b.id) ?? Number.MAX_SAFE_INTEGER),
    );
  }, [live, pending]);

  // The override is only worth keeping while the server still lists the same
  // destinations in a different order. Once it agrees — or once one is added or
  // deleted underneath it — it can only mis-sort, so drop it.
  useEffect(() => {
    if (!pending || !live) return;
    const ids = live.map((d) => d.id);
    const awaitingServer =
      ids.length === pending.length &&
      ids.every((id) => pending.includes(id)) &&
      ids.some((id, i) => id !== pending[i]);
    if (!awaitingServer) setPending(null);
  }, [live, pending]);

  const move = async (id: number, delta: -1 | 1) => {
    const ids = destinations.map((d) => d.id);
    const from = ids.indexOf(id);
    const to = from + delta;
    if (from < 0 || to < 0 || to >= ids.length) return;
    [ids[from], ids[to]] = [ids[to], ids[from]];

    setPending(ids);
    setMoveNote(`${destinations[from].name} moved to position ${to + 1} of ${ids.length}.`);
    try {
      await api.reorderDestinations(ids);
    } catch (err) {
      setPending(null);
      setMoveNote("");
      toast.error(err instanceof Error ? err.message : "Could not reorder the destinations.");
    }
  };

  const copyIngest = async () => {
    if (!system?.ingestUrl) return;
    try {
      await navigator.clipboard.writeText(system.ingestUrl);
      toast.success("Ingest URL copied.");
    } catch {
      toast.error("Clipboard is unavailable on this origin.");
    }
  };

  return (
    <div className="p-3">
      <PageHeader
        title="Dashboard"
        subtitle="Ingest once, fan out with shared video and per-destination audio."
        actions={
          <Button
            size="sm"
            onClick={() => {
              setEditing(null);
              setDialogOpen(true);
            }}
          >
            <Plus /> Add destination
          </Button>
        }
      />

      <div className="grid gap-3 lg:grid-cols-[minmax(0,1fr)_20rem]">
        {/* ---------- preview + ingest ---------- */}
        <div className="flex flex-col gap-3">
          <Suspense
            fallback={
              <div className="aspect-video w-full rounded-md border border-border bg-black" />
            }
          >
            <PreviewPlayer active={settingsPreview} />
          </Suspense>

          <Card>
            <CardHeader className="flex-row items-center justify-between">
              <CardTitle className="flex items-center gap-2">
                <StatusDot tone={ingestTone} />
                Ingest
              </CardTitle>
              <Badge variant={toneBadge[ingestTone]}>{labelForState(ingest?.state)}</Badge>
            </CardHeader>
            <CardContent className="flex flex-col gap-3">
              <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
                <Stat
                  label="Bitrate"
                  value={ingest?.state === "running" ? kbps(ingest.progress?.bitrateKbps ?? 0) : "—"}
                />
                <Stat
                  label="Uptime"
                  value={ingest?.state === "running" ? duration(ingest.uptimeSec) : "—"}
                />
                <Stat label="Audio tracks" value={source?.tracks?.length ?? 0} />
                <Stat
                  label="Reconnects"
                  value={ingest?.restarts ?? 0}
                  tone={(ingest?.restarts ?? 0) > 0 ? "warn" : "muted"}
                />
              </div>

              {source?.video && (
                <div className="font-mono text-[10px] text-muted-foreground">
                  {source.video.codec} {source.video.width}×{source.video.height}
                  {source.video.frameRate > 0 && ` @ ${source.video.frameRate.toFixed(2)}fps`}
                </div>
              )}

              <div className="flex items-center gap-2 rounded border border-border bg-background px-2 py-1.5">
                <Radio className="h-3 w-3 shrink-0 text-muted-foreground" />
                <code className="min-w-0 flex-1 truncate font-mono text-[10px] text-muted-foreground">
                  {system?.ingestUrl ?? "…"}
                </code>
                <Button variant="ghost" size="icon-sm" onClick={copyIngest} aria-label="Copy ingest URL">
                  <Copy />
                </Button>
              </div>

              {ingest?.lastError && ingest.state !== "running" && (
                <div className="rounded border border-down/30 bg-down-dim px-2 py-1 text-[10px] text-down">
                  {ingest.lastError}
                </div>
              )}

              {system && !system.ffmpeg.hasLibsrt && (
                <div className="rounded border border-warn/30 bg-warn-dim px-2 py-1 text-[10px] text-warn">
                  This FFmpeg build has no SRT support, so multi-track SRT ingest will not work.
                  Install a build with <code className="font-mono">--enable-libsrt</code>, or switch
                  the ingest to RTMP in Settings.
                </div>
              )}
            </CardContent>
          </Card>
        </div>

        {/* ---------- side stats ---------- */}
        <div className="flex flex-col gap-3">
          <Card>
            <CardHeader>
              <CardTitle>Pipeline</CardTitle>
            </CardHeader>
            <CardContent className="flex flex-col gap-1.5">
              {(
                [
                  ["Recorder", status?.recorder, "disabled"],
                  // The preview encoder is started on demand and stopped again
                  // when nobody is watching, so having no process is the normal
                  // idle state rather than a fault or a disabled feature.
                  ["Preview", status?.preview, settingsPreview ? "idle" : "disabled"],
                  ["Meters", status?.meters, "disabled"],
                ] as const
              ).map(([label, proc, absent]) => {
                const tone = proc ? toneForState(proc.state) : "idle";
                return (
                  <div key={label} className="flex items-center justify-between">
                    <div className="flex items-center gap-2">
                      <StatusDot tone={tone} size="sm" />
                      <span className="text-[11px]">{label}</span>
                    </div>
                    <span className="font-mono text-[10px] text-muted-foreground">
                      {proc ? labelForState(proc.state) : absent}
                    </span>
                  </div>
                );
              })}
              <div className="mt-1 flex items-center justify-between border-t border-border pt-1.5">
                <span className="text-[11px] text-muted-foreground">Relay subscribers</span>
                <span className="tnum font-mono text-[10px]">
                  {status?.relay.subscribers?.length ?? 0}
                </span>
              </div>

              {/* The shared encode tier, with the ref count that decides
                  whether each one runs. A rendition nobody enabled has no
                  process on purpose, so it reads as idle rather than as a
                  fault, and the destination count is the whole economic
                  argument: three platforms on one tier is still one encode. */}
              <div className="mt-1 flex flex-col gap-1 border-t border-border pt-1.5">
                <span className="text-[11px] text-muted-foreground">Renditions</span>
                {renditions.length === 0 ? (
                  <span className="font-mono text-[10px] text-muted-foreground">
                    none — every destination is on passthrough
                  </span>
                ) : (
                  renditions.map((r) => (
                    <div key={r.id} className="flex items-center justify-between gap-2">
                      <div className="flex min-w-0 items-center gap-2">
                        <StatusDot
                          tone={r.consumers === 0 ? "idle" : toneForState(r.process?.state)}
                          size="sm"
                        />
                        <span className="truncate text-[11px]">{r.name}</span>
                      </div>
                      <span className="tnum shrink-0 font-mono text-[10px] text-muted-foreground">
                        {r.consumers === 1 ? "1 dest" : `${r.consumers} dests`}
                      </span>
                    </div>
                  ))
                )}
              </div>
            </CardContent>
          </Card>
        </div>
      </div>

      <GoLiveComposer />

      {/* ---------- destinations ---------- */}
      <div className="mt-4">
        <div className="mb-2 flex items-center justify-between">
          <h2 className="text-[13px] font-semibold tracking-tight">
            Destinations
            <span className="ml-1.5 font-mono text-[11px] font-normal text-muted-foreground">
              {destinations.length}
            </span>
          </h2>
        </div>

        {/* A card that jumps position is invisible to a screen reader unless
            the move is announced. */}
        <output aria-live="polite" className="sr-only">
          {moveNote}
        </output>

        {destinations.length === 0 ? (
          <Card>
            <CardContent className="flex flex-col items-center gap-2 py-8 text-center">
              <p className="text-[12px] text-muted-foreground">
                No destinations yet. Add one, then choose which audio tracks it receives.
              </p>
              <Button
                size="sm"
                onClick={() => {
                  setEditing(null);
                  setDialogOpen(true);
                }}
              >
                <Plus /> Add destination
              </Button>
            </CardContent>
          </Card>
        ) : (
          <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
            {destinations.map((d, i) => (
              <DestinationCard
                key={d.id}
                dest={d}
                busy={busyId === d.id}
                canMoveEarlier={i > 0}
                canMoveLater={i < destinations.length - 1}
                onMoveEarlier={() => move(d.id, -1)}
                onMoveLater={() => move(d.id, 1)}
                onStart={() => act(d.id, () => api.startDestination(d.id), "start the destination")}
                onStop={() => act(d.id, () => api.stopDestination(d.id), "stop the destination")}
                onRestart={() =>
                  act(d.id, () => api.restartDestination(d.id), "restart the destination")
                }
                onEdit={() => openEdit(d.id)}
                onDelete={() => remove(d.id, d.name)}
                onRefreshKey={async () => {
                  await act(
                    d.id,
                    async () => {
                      await api.refreshStreamKey(d.id);
                      toast.success("Stream key refreshed from the platform.");
                    },
                    "refresh the stream key",
                  );
                }}
              />
            ))}
          </div>
        )}
      </div>

      <DestinationDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        destination={editing}
        onSaved={() => setRefreshKey((k) => k + 1)}
      />
    </div>
  );
}
