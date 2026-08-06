import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useParams } from "react-router";
import { toast } from "sonner";
import {
  AlertTriangle,
  Clock,
  Gauge,
  Loader2,
  Music,
  Save,
  ShieldCheck,
  Tags,
  Waves,
  Wand2,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { PageHeader } from "@/components/AppLayout";
import { TrackRows } from "@/components/signature/TrackRows";
import { MixMatrix } from "@/components/signature/MixMatrix";
import { FilterString } from "@/components/signature/FilterString";
import { useLiveData, useSourceTracks } from "@/hooks/useLiveData";
import { ApiError, api, type DestinationWithRouting } from "@/lib/api";
import { cn } from "@/lib/utils";
import {
  DEFAULT_DUCK_ATTACK_MS,
  DEFAULT_DUCK_RATIO,
  DEFAULT_DUCK_RELEASE_MS,
  DEFAULT_DUCK_THRESHOLD_DB,
  DEFAULT_LOUDNESS_LRA,
  DEFAULT_TRUE_PEAK_DB,
  LUFS_BROADCAST,
  LUFS_PODCAST,
  LUFS_STREAMING,
  MAX_DELAY_MS,
  MAX_DUCK_RATIO,
  MAX_DUCK_THRESHOLD_DB,
  MAX_LOUDNESS_LRA,
  MAX_TARGET_LUFS,
  MAX_TRUE_PEAK_DB,
  MIN_DELAY_MS,
  MIN_DUCK_RATIO,
  MIN_DUCK_THRESHOLD_DB,
  MIN_LOUDNESS_LRA,
  MIN_TARGET_LUFS,
  MIN_TRUE_PEAK_DB,
  ROLE_LABEL,
  TRACK_ROLES,
  type Destination,
  type Ducking,
  type Loudness,
  type MatrixCell,
  type MusicDecision,
  type MusicPolicyChoice,
  type NormalizeMode,
  type PolicyPlatform,
  type Preset,
  type PresetOpts,
  type RoutingProfile,
  type RoutingResult,
  type TrackAnnotation,
  type TrackRole,
  type TrackSel,
} from "@/lib/types";
import { useT } from "@/lib/i18n";

const NORMALIZE_LABEL: Record<NormalizeMode, string> = {
  auto: "Auto (limit when 2+ tracks)",
  off: "Off",
  limiter: "Limiter (−0.4 dBFS ceiling)",
  loudnorm: "Loudness (EBU R128)",
};

/** The named loudness targets, plus the two ends of the range. Every value
 *  here is a starting point, never a default: the right number depends
 *  entirely on where the stream is going. */
const LOUDNESS_PRESETS: { lufs: number; label: string }[] = [
  { lufs: LUFS_STREAMING, label: "YouTube, Twitch, Spotify" },
  { lufs: LUFS_PODCAST, label: "Podcast / spoken word" },
  { lufs: LUFS_BROADCAST, label: "EBU R128 broadcast" },
];

const LOUDNESS_OFF = "__off__";
const LOUDNESS_CUSTOM = "__custom__";

/** Clip protection modes a loudness target can actually arm. `off` and
 *  `limiter` are decisions the operator already made, and routing never
 *  overrides them — so a target set alongside one is inert, and the editor has
 *  to say so rather than imply a compliance it is not delivering. */
const LOUDNESS_ARMS: NormalizeMode[] = ["auto", "loudnorm"];

/** `delayMs` is signed and a signed number alone is ambiguous, so the editor
 *  splits it into a direction and a magnitude. */
type DelayDirection = "audio" | "video";

const DELAY_LABEL: Record<DelayDirection, string> = {
  audio: "Audio later than video",
  video: "Video later than audio",
};

export function RoutingPage() {
  const t = useT();
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
    cleanTrack: 1,
    language: "",
    musicPolicy: "",
  });
  const [annotations, setAnnotations] = useState<TrackAnnotation[]>([]);
  const [annotating, setAnnotating] = useState(false);
  // Whether the server accepted an annotation write. Null means "not tried
  // yet"; false means this build cannot store them and the editor keeps them
  // in the browser rather than refusing to show the roles at all.
  const [annotationsStored, setAnnotationsStored] = useState<boolean | null>(null);
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

  // ---- seed the annotations from whatever the server knows ----
  // Once only: after that the editor owns them, or a websocket source update
  // would type over the operator mid-word.
  const annotationsSeeded = useRef(false);
  useEffect(() => {
    if (annotationsSeeded.current || !source) return;
    annotationsSeeded.current = true;
    if (source.annotations?.length) {
      setAnnotations(source.annotations);
      setAnnotationsStored(true);
    }
  }, [source]);

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

  // ---- annotations ----
  //
  // Written straight through rather than gathered behind the Save button: they
  // describe the SOURCE, so they are not this destination's to hold hostage.
  const annotationsEdited = useRef(false);
  const annotate = useCallback((track: number, next: Partial<TrackAnnotation>) => {
    annotationsEdited.current = true;
    setAnnotations((prev) => {
      const existing = prev.some((a) => a.track === track);
      const merged = existing
        ? prev.map((a) => (a.track === track ? { ...a, ...next } : a))
        : [...prev, { track, ...next }];
      // Drop annotations that describe nothing, so an operator who clears a
      // label does not leave a row behind for the server to store.
      return merged
        .filter((a) => a.role || a.label || a.language || a.denoise)
        .sort((a, b) => a.track - b.track);
    });
  }, []);

  useEffect(() => {
    if (!annotationsEdited.current) return;
    const t = window.setTimeout(() => {
      api
        .putAnnotations(annotations)
        .then(() => setAnnotationsStored(true))
        .catch((err) => {
          // A build without the route is not a failure worth a toast on every
          // keystroke — it is a capability this server does not have.
          if (err instanceof ApiError && (err.status === 404 || err.status === 405)) {
            setAnnotationsStored(false);
            return;
          }
          toast.error(err instanceof Error ? err.message : "Could not save track roles.");
        });
    }, 500);
    return () => window.clearTimeout(t);
  }, [annotations]);

  const save = async () => {
    if (!selected || !profile) return;
    setSaving(true);
    try {
      // Every optional field goes out explicitly, including the nulls: the
      // server decodes over the stored row, so an omitted `loudness` would
      // leave the old target in place instead of clearing it.
      await api.updateDestination(selected.id, { profile: wireProfile(profile) });
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
      toast.success(t("route.presetApplied"));
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Could not apply the preset.");
    }
  };

  const trackOptions = useMemo(
    () => tracks.map((t) => ({ value: String(t.index), label: `Track ${t.index + 1}` })),
    [tracks],
  );

  // The compiler's own answer to "which tracks reach this destination", which
  // is the only one that survives a role exclusion.
  const mixedTracks = useMemo(() => compiled?.tracks ?? [], [compiled]);

  const excludeRoles = useMemo(() => profile?.excludeRoles ?? [], [profile]);

  const musicTracks = useMemo(
    () => annotations.filter((a) => a.role === "music").map((a) => a.track),
    [annotations],
  );

  // The music-rights table, read off the presets it generated. There is no
  // separate endpoint for it, and deriving both from the same rows is what
  // stops the badge and the preset button from ever disagreeing.
  const policyFor = useCallback(
    (dest: Destination): Preset | undefined => {
      // A local recording is `file` whatever platform the row claims: where the
      // bytes land is the only thing a rights policy cares about.
      const plat: PolicyPlatform = dest.kind === "file" ? "file" : dest.platform;
      return presets.find((p) => p.platform === plat);
    },
    [presets],
  );

  const platformPresets = useMemo(() => presets.filter((p) => p.platform), [presets]);
  const mixPresets = useMemo(() => presets.filter((p) => !p.platform), [presets]);
  const selectedPolicy = useMemo(
    () => (selected ? policyFor(selected) : undefined),
    [selected, policyFor],
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
        <PageHeader title={t("route.title")} />
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
        title={t("route.audioRouting")}
        subtitle={t("route.audioRoutingDesc")}
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
            <CardTitle>{t("route.destinations")}</CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-0.5">
            {list.map(({ destination: d, routing }) => {
              const pol = policyFor(d)?.policy;
              const guarded = d.profile.excludeRoles?.includes("music") ?? false;
              return (
                <button
                  key={d.id}
                  onClick={() => navigate(`/routing/${d.id}`)}
                  className={`flex flex-col items-start gap-0.5 rounded-md px-2 py-1.5 text-left transition-colors ${
                    d.id === selected?.id
                      ? "bg-primary-dim text-foreground"
                      : "text-muted-foreground hover:bg-accent hover:text-foreground"
                  }`}
                >
                  <span className="flex w-full items-center gap-1 truncate text-[12px] font-medium">
                    <span className="truncate">{d.name}</span>
                    {guarded && <Music className="ml-auto h-3 w-3 shrink-0 text-live" />}
                    {!guarded && pol?.exclude && (
                      <Music className="ml-auto h-3 w-3 shrink-0 text-warn" />
                    )}
                  </span>
                  <span className="truncate font-mono text-[10px] text-muted-foreground">
                    {routing?.summary ?? "—"}
                  </span>
                </button>
              );
            })}
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
              <CardContent className="flex flex-col gap-3">
                <PresetRow
                  presets={mixPresets}
                  opts={presetOpts}
                  setOpts={setPresetOpts}
                  trackOptions={trackOptions}
                  onApply={applyPreset}
                />
                {platformPresets.length > 0 && (
                  <div className="flex flex-col gap-1.5 border-t border-border pt-3">
                    <div className="text-[10px] uppercase tracking-wider text-muted-foreground">
                      Platform defaults
                    </div>
                    <PresetRow
                      presets={platformPresets}
                      opts={presetOpts}
                      setOpts={setPresetOpts}
                      trackOptions={trackOptions}
                      onApply={applyPreset}
                      showMusicPolicy
                    />
                  </div>
                )}
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
                      <TabsTrigger value="simple">{t("route.simple")}</TabsTrigger>
                      <TabsTrigger value="matrix">{t("route.mixMatrix")}</TabsTrigger>
                    </TabsList>

                    <div className="flex items-center gap-2">
                      {profile.mode === "simple" && (
                        <Button
                          variant={annotating ? "secondary" : "ghost"}
                          size="sm"
                          onClick={() => setAnnotating((a) => !a)}
                        >
                          <Tags /> Label tracks
                        </Button>
                      )}
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
                      annotations={annotations}
                      onAnnotate={annotate}
                      annotating={annotating}
                      excludeRoles={excludeRoles}
                      duckTrigger={profile.ducking?.trigger ?? []}
                      duckTarget={profile.ducking?.target ?? []}
                    />
                    {annotating && annotationsStored === false && (
                      <p className="mt-2 text-[10px] text-warn">
            {t("route.localOnlyNote")}
                      </p>
                    )}
                    {annotating && annotationsStored !== false && (
                      <p className="mt-2 text-[10px] text-muted-foreground">
            {t("route.rolesNote")}
                      </p>
                    )}
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

            {/* ---------- music rights ---------- */}
            <MusicRightsCard
              platformName={selectedPolicy?.name ?? ""}
              decision={selectedPolicy?.policy}
              excludeRoles={excludeRoles}
              musicTracks={musicTracks}
              onExcludeRoles={(roles) => patch({ excludeRoles: roles.length ? roles : null })}
            />

            {/* ---------- loudness, delay, ducking ---------- */}
            <div className="grid gap-3 xl:grid-cols-2">
              <LoudnessCard
                loudness={profile.loudness ?? null}
                normalize={profile.normalize}
                onChange={(l) => patch({ loudness: l })}
              />
              <DelayCard
                delayMs={profile.delayMs ?? 0}
                videoDelayMs={compiled?.videoDelayMs ?? 0}
                onChange={(ms) => patch({ delayMs: ms })}
              />
            </div>

            <DuckingCard
              ducking={profile.ducking ?? null}
              mixedTracks={mixedTracks}
              allTracks={tracks.map((t) => t.index)}
              annotations={annotations}
              onChange={(d) => patch({ ducking: d })}
            />

            {/* result */}
            <Card>
              <CardHeader className="flex-row items-center justify-between">
                <CardTitle>{t("route.result")}</CardTitle>
                {compiled && (
                  <div className="flex flex-wrap items-center justify-end gap-1.5">
                    <Badge variant="armed">{compiled.summary}</Badge>
                    {compiled.normalization !== "off" && (
                      <Badge variant="outline">{compiled.normalization}</Badge>
                    )}
                    {(profile.delayMs ?? 0) !== 0 && (
                      <Badge variant="outline">
                        {describeDelay(profile.delayMs ?? 0, compiled.videoDelayMs ?? 0)}
                      </Badge>
                    )}
                    {profile.ducking && <Badge variant="outline">ducking</Badge>}
                    {excludeRoles.length > 0 && (
                      <Badge variant="warn">
                        excludes {excludeRoles.map((r) => ROLE_LABEL[r as Exclude<TrackRole, "">]).join(", ")}
                      </Badge>
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
                  {(compiled?.videoDelayMs ?? 0) > 0 &&
                    ` Video is held back ${compiled?.videoDelayMs} ms at the input to let the audio run ahead.`}
                </p>
              </CardContent>
            </Card>
          </div>
        )}
      </div>
    </div>
  );
}

/* ==========================================================================
   Presets
   ========================================================================== */

interface PresetRowProps {
  presets: Preset[];
  opts: PresetOpts;
  setOpts: (update: (o: PresetOpts) => PresetOpts) => void;
  trackOptions: { value: string; label: string }[];
  onApply: (id: string) => void;
  showMusicPolicy?: boolean;
}

function PresetRow({
  presets,
  opts,
  setOpts,
  trackOptions,
  onApply,
  showMusicPolicy = false,
}: PresetRowProps) {
  const t = useT();
  return (
    <div className="flex flex-wrap items-start gap-2">
      {presets.map((p) => (
        <div key={p.id} className="flex w-40 flex-col gap-1">
          <Button
            variant="outline"
            size="sm"
            className="justify-start"
            onClick={() => onApply(p.id)}
            title={p.description}
          >
            {p.name}
          </Button>
          {p.policy?.exclude && (
            <Badge variant="warn" className="w-fit">
              no music
            </Badge>
          )}
          {p.needsMusicTrack && !p.platform && (
            <TrackPick
              label={t("route.roleMusic")}
              value={opts.musicTrack}
              options={trackOptions}
              onChange={(v) => setOpts((o) => ({ ...o, musicTrack: v }))}
            />
          )}
          {p.needsMicTrack && (
            <TrackPick
              label={t("route.roleMic")}
              value={opts.micTrack}
              options={trackOptions}
              onChange={(v) => setOpts((o) => ({ ...o, micTrack: v }))}
            />
          )}
          {p.needsSurroundTrack && (
            <TrackPick
              label={t("route.role51")}
              value={opts.surroundTrack}
              options={trackOptions}
              onChange={(v) => setOpts((o) => ({ ...o, surroundTrack: v }))}
            />
          )}
          {p.needsCleanTrack && (
            <TrackPick
              label={t("route.roleClean")}
              value={opts.cleanTrack}
              options={trackOptions}
              onChange={(v) => setOpts((o) => ({ ...o, cleanTrack: v }))}
            />
          )}
          {p.needsLanguage && (
            <Input
              value={opts.language ?? ""}
              placeholder={t("route.langPlaceholder")}
              onChange={(e) => setOpts((o) => ({ ...o, language: e.target.value }))}
              className="h-6 text-[10px]"
              aria-label={t("route.commentaryLanguage")}
            />
          )}
          {showMusicPolicy && p.policy?.exclude && (
            <Select
              value={opts.musicPolicy || "default"}
              onValueChange={(v) =>
                setOpts((o) => ({
                  ...o,
                  musicPolicy: (v === "default" ? "" : v) as MusicPolicyChoice,
                }))
              }
            >
              <SelectTrigger className="h-6 text-[10px]" aria-label={t("route.musicPolicy")}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="default">{t("route.platformDefault")}</SelectItem>
                <SelectItem value="exclude">{t("route.excludeMusic")}</SelectItem>
                <SelectItem value="keep">{t("route.keepMusic")}</SelectItem>
              </SelectContent>
            </Select>
          )}
        </div>
      ))}
    </div>
  );
}

function TrackPick({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: number;
  options: { value: string; label: string }[];
  onChange: (v: number) => void;
}) {
  return (
    <Select value={String(value)} onValueChange={(v) => onChange(Number(v))}>
      <SelectTrigger className="h-6 text-[10px]" aria-label={`${label} track`}>
        <span className="mr-1 text-subtle-foreground">{label}</span>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {options.map((o) => (
          <SelectItem key={o.value} value={o.value}>
            {o.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

/* ==========================================================================
   Music rights

   The whole value of the feature is that the guarantee is VISIBLE. A silent
   exclusion is worth nothing to someone deciding whether to press Go Live, and
   a platform that will mute the VOD has to say so where the decision is made.
   ========================================================================== */

function MusicRightsCard({
  platformName,
  decision,
  excludeRoles,
  musicTracks,
  onExcludeRoles,
}: {
  platformName: string;
  decision?: MusicDecision;
  excludeRoles: TrackRole[];
  musicTracks: number[];
  onExcludeRoles: (roles: TrackRole[]) => void;
}) {
  const t = useT();
  const excluded = excludeRoles.includes("music");
  const platformWants = decision?.exclude ?? false;
  const reason = decision?.reason || "this platform's music policy";
  const who = platformName || "This destination's platform";

  const toggleRole = (role: TrackRole, on: boolean) => {
    const next = on
      ? [...excludeRoles.filter((r) => r !== role), role]
      : excludeRoles.filter((r) => r !== role);
    onExcludeRoles(next);
  };

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between">
        <CardTitle className="flex items-center gap-1.5">
          <ShieldCheck className="h-3.5 w-3.5" /> Music rights
        </CardTitle>
        <Tooltip>
          <TooltipTrigger asChild>
            <span className="cursor-help">
              {excluded ? (
                <Badge variant="live">
                  <Music className="h-2.5 w-2.5" /> music excluded
                </Badge>
              ) : platformWants ? (
                <Badge variant="down">
                  <Music className="h-2.5 w-2.5" /> music is being sent
                </Badge>
              ) : (
                <Badge variant="outline">
                  <Music className="h-2.5 w-2.5" /> music allowed
                </Badge>
              )}
            </span>
          </TooltipTrigger>
          <TooltipContent className="max-w-80 leading-relaxed">
            {excluded ? (
              <>
                Any ingest track marked with the “Music” role is dropped before this
                destination’s mix is built.{" "}
                {platformWants &&
                  `${who} is widely observed to act on recorded music — ${reason} — which is why this is on by default. `}
                Nothing else about the mix changes.
              </>
            ) : platformWants ? (
              <>
                {who} is widely observed to mute the VOD, block the replay or strike the
                channel over recorded music ({reason}). polyemesis is not enforcing that here:
                this destination is configured to carry music. Turn the switch on to keep it
                out.
              </>
            ) : (
              <>
                No music policy is known for this destination, so nothing is filtered. The
                policy table is a convenience default and never legal advice — the operator
                decides.
              </>
            )}
          </TooltipContent>
        </Tooltip>
      </CardHeader>
      <CardContent className="flex flex-col gap-2.5">
        <label className="flex items-center gap-2.5">
          <Switch
            checked={excluded}
            onCheckedChange={(v) => toggleRole("music", v)}
            aria-label={t("route.keepMusicOut")}
          />
          <span className="text-[12px]">{t("route.keepMusicOut")}</span>
          {excluded !== platformWants && decision && (
            <Badge variant="outline">operator override</Badge>
          )}
        </label>

        <p className="text-[10px] leading-relaxed text-muted-foreground">
          {excluded ? (
            musicTracks.length > 0 ? (
              <>
                <span className="text-live">
                  {musicTracks.map((t) => `Track ${t + 1}`).join(", ")}
                </span>{" "}
                {musicTracks.length === 1 ? "is" : "are"} marked as music and{" "}
                {musicTracks.length === 1 ? "is" : "are"} not being sent. The exclusion follows
                the role, so moving the music to another track keeps working.
              </>
            ) : (
              <>
                No ingest track is marked as music yet. Open{" "}
                <span className="text-foreground">{t("route.labelTracks")}</span> above and set one to
                “Music” — this destination will drop it from that moment on, without another
                edit here.
              </>
            )
          ) : (
            <>{t("route.everyTrackReaches")}</>
          )}
        </p>

        <div className="flex flex-wrap items-center gap-1.5 border-t border-border pt-2">
          <span className="mr-1 text-[10px] uppercase tracking-wider text-muted-foreground">
            Also exclude
          </span>
          {TRACK_ROLES.filter((r) => r !== "music").map((r) => {
            const on = excludeRoles.includes(r);
            return (
              <button
                key={r}
                onClick={() => toggleRole(r, !on)}
                aria-pressed={on}
                className={cn(
                  "rounded border px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wider transition-colors",
                  on
                    ? "border-warn/40 bg-warn-dim text-warn"
                    : "border-border text-subtle-foreground hover:border-border-strong hover:text-muted-foreground",
                )}
              >
                {ROLE_LABEL[r]}
              </button>
            );
          })}
        </div>
      </CardContent>
    </Card>
  );
}

/* ==========================================================================
   Loudness
   ========================================================================== */

function LoudnessCard({
  loudness,
  normalize,
  onChange,
}: {
  loudness: Loudness | null;
  normalize: NormalizeMode;
  onChange: (l: Loudness | null) => void;
}) {
  const t = useT();
  const named = loudness && LOUDNESS_PRESETS.some((p) => p.lufs === loudness.targetLufs);
  const value = !loudness ? LOUDNESS_OFF : named ? String(loudness.targetLufs) : LOUDNESS_CUSTOM;
  // A target under `off` or `limiter` is stored but inert — routing never
  // overrides a clip-protection mode the operator chose by hand.
  const inert = loudness !== null && !LOUDNESS_ARMS.includes(normalize);

  const select = (v: string) => {
    if (v === LOUDNESS_OFF) return onChange(null);
    if (v === LOUDNESS_CUSTOM) {
      return onChange({ targetLufs: loudness?.targetLufs ?? LUFS_PODCAST, ...rest(loudness) });
    }
    onChange({ targetLufs: Number(v), ...rest(loudness) });
  };

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between">
        <CardTitle className="flex items-center gap-1.5">
          <Gauge className="h-3.5 w-3.5" /> Loudness target
        </CardTitle>
        {loudness && (
          <Badge variant={inert ? "warn" : "armed"} className="tnum">
            {loudness.targetLufs} LUFS
          </Badge>
        )}
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        <Select value={value} onValueChange={select}>
          <SelectTrigger aria-label={t("route.loudnessTarget")}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={LOUDNESS_OFF}>{t("route.noTarget")}</SelectItem>
            {LOUDNESS_PRESETS.map((p) => (
              <SelectItem key={p.lufs} value={String(p.lufs)}>
                {p.lufs} LUFS — {p.label}
              </SelectItem>
            ))}
            <SelectItem value={LOUDNESS_CUSTOM}>{t("route.custom")}</SelectItem>
          </SelectContent>
        </Select>

        {loudness && (
          <div className="grid grid-cols-3 gap-2">
            <NumberField
              label={t("route.target")}
              unit="LUFS"
              value={loudness.targetLufs}
              min={MIN_TARGET_LUFS}
              max={MAX_TARGET_LUFS}
              step={0.5}
              disabled={value !== LOUDNESS_CUSTOM}
              onChange={(n) => onChange({ ...loudness, targetLufs: n })}
            />
            <NumberField
              label={t("route.truePeak")}
              unit="dBTP"
              value={loudness.truePeakDb ?? 0}
              placeholder={String(DEFAULT_TRUE_PEAK_DB)}
              min={MIN_TRUE_PEAK_DB}
              max={MAX_TRUE_PEAK_DB}
              step={0.1}
              onChange={(n) => onChange({ ...loudness, truePeakDb: n })}
            />
            <NumberField
              label={t("route.range")}
              unit="LU"
              value={loudness.rangeLu ?? 0}
              placeholder={String(DEFAULT_LOUDNESS_LRA)}
              min={MIN_LOUDNESS_LRA}
              max={MAX_LOUDNESS_LRA}
              step={1}
              onChange={(n) => onChange({ ...loudness, rangeLu: n })}
            />
          </div>
        )}

        {inert && (
          <div className="flex items-start gap-1.5 rounded border border-warn/30 bg-warn-dim px-2 py-1.5 text-[10px] leading-relaxed text-warn">
            <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
            <span>
              Clip protection is set to “{NORMALIZE_LABEL[normalize]}”, which polyemesis never
              overrides — you chose it. This target is stored but not applied. Switch clip
              protection to Auto or Loudness to arm it.
            </span>
          </div>
        )}

        <p className="text-[10px] leading-relaxed text-muted-foreground">
          {loudness ? (
            "Measured over the whole programme, not moment to moment: quiet passages stay quiet. These are the numbers the platforms' own normalizers aim at, so hitting one is what stops the platform turning you down."
          ) : normalize === "loudnorm" ? (
            <>
              Clip protection is set to Loudness, which normalises to a fixed{" "}
              <span className="tnum">{LUFS_PODCAST} LUFS</span> — the value that shipped before
              targets were configurable. Choosing one above replaces it.
            </>
          ) : (
            "Without a target the mix goes out at whatever level the ingest produced, with only the clip protection above applied."
          )}
        </p>
      </CardContent>
    </Card>
  );
}

/** Everything but the target, so switching between named targets keeps the
 *  true-peak and range the operator set. */
function rest(l: Loudness | null): Partial<Loudness> {
  return l ? { truePeakDb: l.truePeakDb, rangeLu: l.rangeLu } : {};
}

/* ==========================================================================
   Delay
   ========================================================================== */

function DelayCard({
  delayMs,
  videoDelayMs,
  onChange,
}: {
  delayMs: number;
  videoDelayMs: number;
  onChange: (ms: number) => void;
}) {
  const t = useT();
  const direction: DelayDirection = delayMs < 0 ? "video" : "audio";
  const magnitude = Math.abs(delayMs);
  const max = direction === "audio" ? MAX_DELAY_MS : -MIN_DELAY_MS;

  const setDirection = (d: DelayDirection) => {
    const capped = Math.min(magnitude, d === "audio" ? MAX_DELAY_MS : -MIN_DELAY_MS);
    onChange(d === "audio" ? capped : -capped);
  };

  const setMagnitude = (n: number) => {
    const clamped = Math.max(0, Math.min(Math.round(n), max));
    onChange(direction === "audio" ? clamped : -clamped);
  };

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between">
        <CardTitle className="flex items-center gap-1.5">
          <Clock className="h-3.5 w-3.5" /> A/V delay
        </CardTitle>
        {delayMs !== 0 && (
          <Badge variant="armed" className="tnum">
            {describeDelay(delayMs, videoDelayMs)}
          </Badge>
        )}
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        <div className="grid grid-cols-[minmax(0,1fr)_7rem] gap-2">
          <Select value={direction} onValueChange={(v) => setDirection(v as DelayDirection)}>
            <SelectTrigger aria-label={t("route.delayDirection")}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="audio">{DELAY_LABEL.audio}</SelectItem>
              <SelectItem value="video">{DELAY_LABEL.video}</SelectItem>
            </SelectContent>
          </Select>
          <NumberField
            label={t("route.offset")}
            unit="ms"
            value={magnitude}
            min={0}
            max={max}
            step={10}
            onChange={setMagnitude}
          />
        </div>

        <p className="text-[10px] leading-relaxed text-muted-foreground">
          {delayMs === 0 ? (
            "Audio and video go out together."
          ) : direction === "audio" ? (
            <>
              Audio is held back <span className="tnum text-foreground">{magnitude} ms</span>, so
              a sound lands after the picture that made it. Up to {MAX_DELAY_MS / 1000} s —
              enough for lip-sync repair or a moderation delay in front of a live audience.
            </>
          ) : (
            <>
              Video is held back <span className="tnum text-foreground">{magnitude} ms</span> at
              the input so the audio runs ahead of it. Capped at {-MIN_DELAY_MS / 1000} s:
              every millisecond costs downstream video buffering, and this direction is only
              ever lip-sync repair.
            </>
          )}
        </p>
      </CardContent>
    </Card>
  );
}

function describeDelay(delayMs: number, videoDelayMs: number): string {
  if (delayMs > 0) return `audio +${delayMs} ms`;
  if (delayMs < 0) return `video +${videoDelayMs || -delayMs} ms`;
  return "in sync";
}

/* ==========================================================================
   Ducking
   ========================================================================== */

function DuckingCard({
  ducking,
  mixedTracks,
  allTracks,
  annotations,
  onChange,
}: {
  ducking: Ducking | null;
  mixedTracks: number[];
  allTracks: number[];
  annotations: TrackAnnotation[];
  onChange: (d: Ducking | null) => void;
}) {
  const t = useT();
  // A duck needs something to push down and something to push it down with.
  // Below two tracks in the mix there is no second group, so the controls
  // would only be able to describe an impossible graph.
  if (mixedTracks.length < 2) return null;

  const enable = (on: boolean) => {
    if (!on) return onChange(null);
    // Seed from the roles when they exist: mic ducks music is the case this
    // feature was built for, and it is one click when we already know which is
    // which.
    const mics = annotations.filter((a) => a.role === "mic" || a.role === "commentary");
    const beds = annotations.filter((a) => a.role === "music" || a.role === "game");
    const trigger = mics.map((a) => a.track).filter((t) => allTracks.includes(t));
    const target = beds.map((a) => a.track).filter((t) => mixedTracks.includes(t));
    onChange({
      trigger: trigger.length ? trigger : [mixedTracks[0]],
      target: target.length ? target : [mixedTracks[1]],
    });
  };

  const toggle = (group: "trigger" | "target", track: number) => {
    if (!ducking) return;
    const other = group === "trigger" ? "target" : "trigger";
    const on = ducking[group].includes(track);
    onChange({
      ...ducking,
      [group]: on
        ? ducking[group].filter((t) => t !== track)
        : [...ducking[group], track].sort((a, b) => a - b),
      // Trigger and target must be disjoint; claiming a track for one side
      // releases it from the other rather than failing validation later.
      [other]: on ? ducking[other] : ducking[other].filter((t) => t !== track),
    });
  };

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between">
        <CardTitle className="flex items-center gap-1.5">
          <Waves className="h-3.5 w-3.5" /> Ducking
        </CardTitle>
        <label className="flex items-center gap-2">
          <span className="text-[10px] uppercase tracking-wider text-muted-foreground">
            {ducking ? "on" : "off"}
          </span>
          <Switch
            checked={ducking !== null}
            onCheckedChange={enable}
            aria-label={t("route.enableDucking")}
          />
        </label>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {!ducking ? (
          <p className="text-[10px] leading-relaxed text-muted-foreground">
            {t("route.duckingNote")}
          </p>
        ) : (
          <>
            <div className="flex flex-col gap-1.5">
              <div className="text-[10px] uppercase tracking-wider text-muted-foreground">
                When these speak
              </div>
              <div className="flex flex-wrap gap-1.5">
                {allTracks.map((t) => (
                  <TrackChip
                    key={t}
                    track={t}
                    annotation={annotations.find((a) => a.track === t)}
                    active={ducking.trigger.includes(t)}
                    tone="armed"
                    onClick={() => toggle("trigger", t)}
                  />
                ))}
              </div>
              <p className="text-[10px] text-muted-foreground">
            {t("route.duckingTriggerNote")}
              </p>
            </div>

            <div className="flex flex-col gap-1.5">
              <div className="text-[10px] uppercase tracking-wider text-muted-foreground">
                Push these down
              </div>
              <div className="flex flex-wrap gap-1.5">
                {mixedTracks.map((t) => (
                  <TrackChip
                    key={t}
                    track={t}
                    annotation={annotations.find((a) => a.track === t)}
                    active={ducking.target.includes(t)}
                    tone="warn"
                    onClick={() => toggle("target", t)}
                  />
                ))}
              </div>
            </div>

            <div className="grid grid-cols-2 gap-2 border-t border-border pt-2 sm:grid-cols-4">
              <NumberField
                label={t("route.threshold")}
                unit="dB"
                value={ducking.thresholdDb ?? 0}
                placeholder={String(DEFAULT_DUCK_THRESHOLD_DB)}
                min={MIN_DUCK_THRESHOLD_DB}
                max={MAX_DUCK_THRESHOLD_DB}
                step={1}
                onChange={(n) => onChange({ ...ducking, thresholdDb: n })}
              />
              <NumberField
                label={t("route.ratio")}
                unit=":1"
                value={ducking.ratio ?? 0}
                placeholder={String(DEFAULT_DUCK_RATIO)}
                min={MIN_DUCK_RATIO}
                max={MAX_DUCK_RATIO}
                step={0.5}
                onChange={(n) => onChange({ ...ducking, ratio: n })}
              />
              <NumberField
                label={t("route.attack")}
                unit="ms"
                value={ducking.attackMs ?? 0}
                placeholder={String(DEFAULT_DUCK_ATTACK_MS)}
                min={0}
                max={2000}
                step={5}
                onChange={(n) => onChange({ ...ducking, attackMs: n })}
              />
              <NumberField
                label={t("route.release")}
                unit="ms"
                value={ducking.releaseMs ?? 0}
                placeholder={String(DEFAULT_DUCK_RELEASE_MS)}
                min={0}
                max={9000}
                step={10}
                onChange={(n) => onChange({ ...ducking, releaseMs: n })}
              />
            </div>
            <p className="text-[10px] text-muted-foreground">
              Blank uses the defaults: {DEFAULT_DUCK_THRESHOLD_DB} dB, {DEFAULT_DUCK_RATIO}:1,{" "}
              {DEFAULT_DUCK_ATTACK_MS} ms attack, {DEFAULT_DUCK_RELEASE_MS} ms release — about
              12 dB of reduction, fast enough to catch a word’s first syllable and slow enough
              not to chatter between them.
            </p>
          </>
        )}
      </CardContent>
    </Card>
  );
}

function TrackChip({
  track,
  annotation,
  active,
  tone,
  onClick,
}: {
  track: number;
  annotation?: TrackAnnotation;
  active: boolean;
  tone: "armed" | "warn";
  onClick: () => void;
}) {
  const name = annotation?.label || (annotation?.role ? ROLE_LABEL[annotation.role as Exclude<TrackRole, "">] : "");
  return (
    <button
      onClick={onClick}
      aria-pressed={active}
      className={cn(
        "flex items-center gap-1.5 rounded border px-2 py-1 text-[11px] transition-colors",
        active
          ? tone === "armed"
            ? "border-armed/40 bg-armed-dim text-armed"
            : "border-warn/40 bg-warn-dim text-warn"
          : "border-border text-subtle-foreground hover:border-border-strong hover:text-muted-foreground",
      )}
    >
      <span className="tnum font-mono font-semibold">{track + 1}</span>
      {name && <span className="max-w-24 truncate">{name}</span>}
    </button>
  );
}

/* ==========================================================================
   Shared numeric field

   Every value here is either a real number or "use the default", and the two
   have to be distinguishable — which is why an empty box means the default
   rather than zero.
   ========================================================================== */

function NumberField({
  label,
  unit,
  value,
  placeholder,
  min,
  max,
  step,
  disabled,
  onChange,
}: {
  label: string;
  unit: string;
  value: number;
  placeholder?: string;
  min: number;
  max: number;
  step: number;
  disabled?: boolean;
  onChange: (n: number) => void;
}) {
  return (
    <label className="flex flex-col gap-1">
      <span className="flex items-baseline justify-between text-[10px] uppercase tracking-wider text-muted-foreground">
        {label}
        <span className="normal-case tracking-normal text-subtle-foreground">{unit}</span>
      </span>
      <Input
        type="number"
        value={value === 0 && placeholder !== undefined ? "" : value}
        placeholder={placeholder}
        min={min}
        max={max}
        step={step}
        disabled={disabled}
        onChange={(e) => onChange(e.target.value === "" ? 0 : Number(e.target.value))}
        className="h-7"
      />
    </label>
  );
}

/* ==========================================================================
   Wire format

   The server decodes a PUT over the stored row, so an omitted optional field
   keeps its old value. Clearing a loudness target therefore has to travel as
   an explicit null, not as an absence.
   ========================================================================== */

function wireProfile(p: RoutingProfile): RoutingProfile {
  return {
    ...p,
    loudness: p.loudness ?? null,
    ducking: p.ducking ?? null,
    excludeRoles: p.excludeRoles?.length ? p.excludeRoles : null,
    delayMs: p.delayMs ?? 0,
  };
}
