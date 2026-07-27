import { Suspense, lazy, useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { Copy, Plus, Radio } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { PageHeader } from "@/components/AppLayout";
import { DestinationCard } from "@/components/DestinationCard";
import { DestinationDialog } from "@/components/DestinationDialog";
import { StatusDot } from "@/components/signature/StatusDot";
import { Stat } from "@/components/signature/Stat";
import { useLiveData } from "@/hooks/useLiveData";
import { api } from "@/lib/api";
import { duration, kbps } from "@/lib/format";
import { labelForState, toneBadge, toneForState } from "@/lib/signal";
import type { Destination, SystemInfo } from "@/lib/types";

// hls.js is a few hundred kilobytes that only the preview needs, and the
// preview is off entirely for some installs. Load it alongside the dashboard
// rather than ahead of it.
const PreviewPlayer = lazy(() =>
  import("@/components/PreviewPlayer").then((m) => ({ default: m.PreviewPlayer })),
);

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
