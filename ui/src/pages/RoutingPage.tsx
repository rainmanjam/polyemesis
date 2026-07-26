import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { toast } from "sonner";
import { AlertTriangle, Loader2, Save, Wand2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { PageHeader } from "@/components/AppLayout";
import { TrackRows } from "@/components/signature/TrackRows";
import { MixMatrix } from "@/components/signature/MixMatrix";
import { FilterString } from "@/components/signature/FilterString";
import { useLiveData, useSourceTracks } from "@/hooks/useLiveData";
import { api, type DestinationWithRouting } from "@/lib/api";
import type {
  Destination,
  MatrixCell,
  NormalizeMode,
  Preset,
  PresetOpts,
  RoutingProfile,
  RoutingResult,
  TrackSel,
} from "@/lib/types";

const NORMALIZE_LABEL: Record<NormalizeMode, string> = {
  auto: "Auto (limit when 2+ tracks)",
  off: "Off",
  limiter: "Limiter (−0.4 dBFS ceiling)",
  loudnorm: "Loudness (EBU R128, −16 LUFS)",
};

export function RoutingPage() {
  const { id } = useParams();
  const navigate = useNavigate();
  const { levels, source } = useLiveData();
  const tracks = useSourceTracks();

  const [list, setList] = useState<DestinationWithRouting[]>([]);
  const [selected, setSelected] = useState<Destination | null>(null);
  const [profile, setProfile] = useState<RoutingProfile | null>(null);
  const [compiled, setCompiled] = useState<RoutingResult | null>(null);
  const [compileError, setCompileError] = useState<string>("");
  const [presets, setPresets] = useState<Preset[]>([]);
  const [presetOpts, setPresetOpts] = useState<PresetOpts>({
    musicTrack: 0,
    micTrack: 2,
    surroundTrack: 0,
  });
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [loading, setLoading] = useState(true);

  // ---- load destinations & presets ----
  useEffect(() => {
    let cancelled = false;
    Promise.all([api.listDestinations(), api.listPresets()])
      .then(([dests, p]) => {
        if (cancelled) return;
        setList(dests);
        setPresets(p.presets);
        setPresetOpts(p.defaults);
        setLoading(false);
        if (!id && dests.length > 0) {
          navigate(`/routing/${dests[0].destination.id}`, { replace: true });
        }
      })
      .catch((err) => {
        toast.error(err instanceof Error ? err.message : "Could not load destinations.");
        setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [id, navigate]);

  // ---- select the destination from the route ----
  useEffect(() => {
    if (!id) {
      setSelected(null);
      setProfile(null);
      return;
    }
    const found = list.find((d) => d.destination.id === Number(id));
    if (found) {
      setSelected(found.destination);
      setProfile(structuredClone(found.destination.profile));
      setDirty(false);
    }
  }, [id, list]);

  // ---- compile on every edit, debounced ----
  // This is what makes the editor honest: the filter string under the controls
  // is produced by the same Go code that will run, not reimplemented in TS.
  const debounceRef = useRef<number | undefined>(undefined);
  useEffect(() => {
    if (!profile) return;
    window.clearTimeout(debounceRef.current);
    debounceRef.current = window.setTimeout(() => {
      api
        .compileRouting(profile)
        .then((res) => {
          setCompiled(res.routing);
          setCompileError("");
        })
        .catch((err) => {
          setCompiled(null);
          setCompileError(err instanceof Error ? err.message : "Could not compile this routing.");
        });
    }, 180);
    return () => window.clearTimeout(debounceRef.current);
  }, [profile]);

  const patch = useCallback((next: Partial<RoutingProfile>) => {
    setProfile((p) => (p ? { ...p, ...next } : p));
    setDirty(true);
  }, []);

  const save = async () => {
    if (!selected || !profile) return;
    setSaving(true);
    try {
      await api.updateDestination(selected.id, { profile });
      toast.success(
        selected.enabled
          ? `Routing saved. "${selected.name}" is restarting with the new mix.`
          : "Routing saved.",
      );
      setDirty(false);
      setList(await api.listDestinations());
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Could not save the routing.");
    } finally {
      setSaving(false);
    }
  };

  const applyPreset = async (presetId: string) => {
    try {
      const res = await api.applyPreset(presetId, presetOpts);
      setProfile(res.profile);
      setCompiled(res.routing);
      setCompileError("");
      setDirty(true);
      toast.success("Preset applied. Review it, then save.");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Could not apply the preset.");
    }
  };

  const trackOptions = useMemo(
    () => tracks.map((t) => ({ value: String(t.index), label: `Track ${t.index + 1}` })),
    [tracks],
  );

  if (loading) {
    return (
      <div className="flex h-full items-center justify-center">
        <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
      </div>
    );
  }

  if (list.length === 0) {
    return (
      <div className="p-3">
        <PageHeader title="Routing" />
        <Card>
          <CardContent className="py-8 text-center text-[12px] text-muted-foreground">
            Add a destination first — routing is configured per destination.
          </CardContent>
        </Card>
      </div>
    );
  }

  return (
    <div className="p-3">
      <PageHeader
        title="Audio routing"
        subtitle="Choose exactly which ingest tracks each destination receives."
        actions={
          <>
            {dirty && <Badge variant="warn">unsaved</Badge>}
            <Button size="sm" onClick={save} disabled={!dirty || saving || !!compileError}>
              {saving ? <Loader2 className="animate-spin" /> : <Save />}
              Save
            </Button>
          </>
        }
      />

      <div className="grid gap-3 lg:grid-cols-[13rem_minmax(0,1fr)]">
        {/* ---------- destination picker ---------- */}
        <Card className="h-fit">
          <CardHeader>
            <CardTitle>Destinations</CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-0.5">
            {list.map(({ destination: d, routing }) => (
              <button
                key={d.id}
                onClick={() => navigate(`/routing/${d.id}`)}
                className={`flex flex-col items-start gap-0.5 rounded-md px-2 py-1.5 text-left transition-colors ${
                  d.id === selected?.id
                    ? "bg-primary-dim text-foreground"
                    : "text-muted-foreground hover:bg-accent hover:text-foreground"
                }`}
              >
                <span className="truncate text-[12px] font-medium">{d.name}</span>
                <span className="truncate font-mono text-[10px] text-muted-foreground">
                  {routing?.summary ?? "—"}
                </span>
              </button>
            ))}
          </CardContent>
        </Card>

        {/* ---------- editor ---------- */}
        {selected && profile && (
          <div className="flex flex-col gap-3">
            {/* presets */}
            <Card>
              <CardHeader>
                <CardTitle className="flex items-center gap-1.5">
                  <Wand2 className="h-3.5 w-3.5" /> Presets
                </CardTitle>
              </CardHeader>
              <CardContent className="flex flex-wrap items-end gap-2">
                {presets.map((p) => (
                  <div key={p.id} className="flex flex-col gap-1">
                    <Button variant="outline" size="sm" onClick={() => applyPreset(p.id)} title={p.description}>
                      {p.name}
                    </Button>
                    {(p.needsMusicTrack || p.needsMicTrack || p.needsSurroundTrack) && (
                      <Select
                        value={String(
                          p.needsMusicTrack
                            ? presetOpts.musicTrack
                            : p.needsMicTrack
                              ? presetOpts.micTrack
                              : presetOpts.surroundTrack,
                        )}
                        onValueChange={(v) =>
                          setPresetOpts((o) => ({
                            ...o,
                            ...(p.needsMusicTrack
                              ? { musicTrack: Number(v) }
                              : p.needsMicTrack
                                ? { micTrack: Number(v) }
                                : { surroundTrack: Number(v) }),
                          }))
                        }
                      >
                        <SelectTrigger className="h-6 w-28 text-[10px]">
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          {trackOptions.map((o) => (
                            <SelectItem key={o.value} value={o.value}>
                              {o.label}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    )}
                  </div>
                ))}
              </CardContent>
            </Card>

            {/* simple / advanced */}
            <Card>
              <CardContent className="pt-3">
                <Tabs
                  value={profile.mode}
                  onValueChange={(v) => patch({ mode: v as "simple" | "matrix" })}
                >
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <TabsList>
                      <TabsTrigger value="simple">Simple</TabsTrigger>
                      <TabsTrigger value="matrix">Mix matrix</TabsTrigger>
                    </TabsList>

                    <div className="flex items-center gap-2">
                      <Label htmlFor="norm" className="whitespace-nowrap">
                        Clip protection
                      </Label>
                      <Select
                        value={profile.normalize}
                        onValueChange={(v) => patch({ normalize: v as NormalizeMode })}
                      >
                        <SelectTrigger id="norm" className="w-56">
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          {(Object.keys(NORMALIZE_LABEL) as NormalizeMode[]).map((n) => (
                            <SelectItem key={n} value={n}>
                              {NORMALIZE_LABEL[n]}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </div>
                  </div>

                  <TabsContent value="simple">
                    <TrackRows
                      tracks={tracks}
                      selection={profile.tracks ?? []}
                      levels={levels}
                      probed={source?.probed ?? false}
                      onChange={(next: TrackSel[]) => patch({ tracks: next })}
                    />
                  </TabsContent>

                  <TabsContent value="matrix">
                    <MixMatrix
                      tracks={tracks}
                      cells={profile.matrix ?? []}
                      onChange={(next: MatrixCell[]) => patch({ matrix: next })}
                    />
                  </TabsContent>
                </Tabs>
              </CardContent>
            </Card>

            {/* result */}
            <Card>
              <CardHeader className="flex-row items-center justify-between">
                <CardTitle>Result</CardTitle>
                {compiled && (
                  <div className="flex items-center gap-1.5">
                    <Badge variant="armed">{compiled.summary}</Badge>
                    {compiled.normalization !== "off" && (
                      <Badge variant="outline">{compiled.normalization}</Badge>
                    )}
                  </div>
                )}
              </CardHeader>
              <CardContent className="flex flex-col gap-2">
                {compileError && (
                  <div className="flex items-start gap-1.5 rounded border border-down/30 bg-down-dim px-2 py-1.5 text-[11px] text-down">
                    <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
                    <span>{compileError}</span>
                  </div>
                )}
                {compiled?.warnings?.map((w) => (
                  <div
                    key={w}
                    className="flex items-start gap-1.5 rounded border border-warn/30 bg-warn-dim px-2 py-1.5 text-[11px] text-warn"
                  >
                    <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
                    <span>{w}</span>
                  </div>
                ))}
                {compiled && <FilterString value={compiled.filterComplex} />}
                <p className="text-[10px] text-muted-foreground">
                  Video is copied without re-encoding. Only this audio graph is applied, and only
                  this destination restarts when you save.
                </p>
              </CardContent>
            </Card>
          </div>
        )}
      </div>
    </div>
  );
}
