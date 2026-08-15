import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useNavigate, useParams } from "react-router";
import { toast } from "sonner";
import {
  AlertTriangle,
  Archive,
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
import { Experimental, ExperimentalBadge } from "@/components/Experimental";
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
  type Levels,
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
  type SourceTrack,
  type TrackAnnotation,
  type TrackRole,
  type TrackSel,
} from "@/lib/types";
import { useT, type TranslationKey } from "@/lib/i18n";

const NORMALIZE_LABEL: Record<NormalizeMode, TranslationKey> = {
  auto: "route.autoLimitWhen2Tracks",
  off: "route.normalizeOff",
  limiter: "route.limiter04DbfsCeiling",
  loudnorm: "route.normalizeLoudnorm",
};

/** The named loudness targets, plus the two ends of the range. Every value
 *  here is a starting point, never a default: the right number depends
 *  entirely on where the stream is going. */
const LOUDNESS_PRESETS: { lufs: number; label: TranslationKey }[] = [
  { lufs: LUFS_STREAMING, label: "route.youtubeTwitchSpotify" },
  { lufs: LUFS_PODCAST, label: "route.podcastSpokenWord" },
  { lufs: LUFS_BROADCAST, label: "route.lufsBroadcast" },
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

const DELAY_LABEL: Record<DelayDirection, TranslationKey> = {
  audio: "route.audioLaterThanVideo",
  video: "route.videoLaterThanAudio",
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
  // The SECOND mix. Null is the normal state and the default, here and in the
  // database — see db.Destination.VODProfile, which is a nil pointer on every
  // destination that has not asked for one.
  const [vodProfile, setVodProfile] = useState<RoutingProfile | null>(null);
  // Each editor compiles itself and reports only its error back, because the
  // only thing this page does with a compile result that the editor cannot is
  // refuse to save. Raw setState functions are passed down deliberately: they
  // are referentially stable, and an inline arrow in that prop would re-run the
  // editor's compile effect on every render.
  const [liveError, setLiveError] = useState<string>("");
  const [vodError, setVodError] = useState<string>("");
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

  // ---- what survives switching the second mix off ----
  //
  // Off is a destructive edit with no undo: the profile is dropped and turning
  // the switch back on re-seeds from the LIVE mix, so an operator who mis-clicks
  // loses the exclusions, the gains and the loudness target they set. This holds
  // the last non-null profile so the switch can put it back, which is cheaper
  // than a confirmation dialog and better than one — nothing is lost, so nothing
  // has to be confirmed. It is per DESTINATION and is cleared on a switch: the
  // stash is an undo for the toggle, not a clipboard between destinations.
  const stashedVod = useRef<RoutingProfile | null>(null);

  // ---- what applyPresetTo re-checks AFTER its await ----
  //
  // A closure captures values, not state, so reading `selected` or `vodProfile`
  // inside an async callback reads what they were when the button was clicked —
  // which is exactly the question being asked. These are written at the same
  // moments the state they mirror is.
  const selectedIdRef = useRef<number | null>(null);
  const vodEnabledRef = useRef(false);
  // One counter per mix, so a live-mix preset and a second-mix preset in flight
  // at the same time do not cancel each other.
  const presetSeq = useRef<{ live: number; vod: number }>({ live: 0, vod: 0 });

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
        toast.error(err instanceof Error ? err.message : t("route.couldNotLoadDestinations"));
        setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [id, navigate, t]);

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
      setVodProfile(null);
      selectedIdRef.current = null;
      vodEnabledRef.current = false;
      stashedVod.current = null;
      return;
    }
    const found = list.find((d) => d.destination.id === Number(id));
    if (found) {
      setSelected(found.destination);
      setProfile(structuredClone(found.destination.profile));
      // `?? null` rather than a bare read: the server omits the key entirely
      // when there is no second mix (`json:"vodProfile,omitempty"`), so the
      // absent case arrives as undefined and has to normalise to the same null
      // the switch and the save path both work in.
      const vod = found.destination.vodProfile
        ? structuredClone(found.destination.vodProfile)
        : null;
      setVodProfile(vod);
      selectedIdRef.current = found.destination.id;
      vodEnabledRef.current = vod !== null;
      // A stash from the destination we just left is not an undo for this one.
      stashedVod.current = null;
      setDirty(false);
    }
  }, [id, list]);

  const patch = useCallback((next: Partial<RoutingProfile>) => {
    setProfile((p) => (p ? { ...p, ...next } : p));
    setDirty(true);
  }, []);

  const patchVod = useCallback((next: Partial<RoutingProfile>) => {
    setVodProfile((p) => (p ? { ...p, ...next } : p));
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
    const timer = window.setTimeout(() => {
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
          toast.error(err instanceof Error ? err.message : t("route.couldNotSaveTrackRoles"));
        });
    }, 500);
    return () => window.clearTimeout(timer);
  }, [annotations, t]);

  const save = async () => {
    if (!selected || !profile) return;
    setSaving(true);
    try {
      // Every optional field goes out explicitly, including the nulls: the
      // server decodes over the stored row, so an omitted `loudness` would
      // leave the old target in place instead of clearing it.
      //
      // `vodProfile` is the same rule one level up, and it is the reason
      // switching the second mix OFF actually clears it. api.handleUpdate
      // decodes the body over the existing db.Destination, so an ABSENT
      // vodProfile leaves the stored pointer alone and the operator would watch
      // a delete undo itself on the next load; an explicit `null` sets the
      // pointer to nil, which marshalVODProfile stores as the empty string.
      await api.updateDestination(selected.id, {
        profile: wireProfile(profile),
        vodProfile: vodProfile ? wireProfile(vodProfile) : null,
      });
      toast.success(
        selected.enabled
          ? `Routing saved. "${selected.name}" is restarting with the new mix.`
          : "Routing saved.",
      );
      setDirty(false);
      setList(await api.listDestinations());
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("route.couldNotSaveTheRouting"));
    } finally {
      setSaving(false);
    }
  };

  // Shared by both mixes. `mix` says which profile asked, so an archive preset
  // can be dropped on the second mix without the first one moving — which is
  // most of the point of having a second mix at all.
  //
  // EVERYTHING INTERESTING HERE IS IN THE WINDOW BETWEEN THE ASK AND THE ANSWER.
  // This is a network round trip started by a click, and three things can change
  // inside it. Applying the answer regardless — which is what this did — is not
  // a cosmetic race:
  //
  //   - THE SECOND MIX CAN BE SWITCHED OFF. Click a VOD preset, switch the mix
  //     off before the answer lands, and it comes back on with setDirty(true).
  //     Save then PUTs a vodProfile where the operator chose null, which is the
  //     one guarantee this whole editor exists to make.
  //   - THE DESTINATION CAN CHANGE. The answer would be written into whichever
  //     mix is on screen now, and marked dirty, so a Save the operator does not
  //     associate with the click persists it.
  //   - A SECOND PRESET CAN BE CLICKED. Nothing orders two responses, so the
  //     slower first one can land last and win.
  //
  // RESOLVES, NEVER REJECTS. On success it hands back the routing the endpoint
  // already compiled for this profile; on a discarded answer or a failed request
  // it hands back null. That return is the fix for the OTHER half of this
  // function's history: it used to drop the compiled result, which left the
  // Result card showing the previous graph, the previous warnings and a stale
  // red error — with Save disabled underneath them — until the editor's own
  // debounce came round 180 ms and a round trip later. The editor still owns
  // `compiled`; this only lets it stop showing an answer it knows is out of date.
  const applyPresetTo = useCallback(
    async (mix: "live" | "vod", presetId: string): Promise<RoutingResult | null> => {
      const destAtClick = selectedIdRef.current;
      const seq = (presetSeq.current[mix] += 1);
      try {
        const res = await api.applyPreset(presetId, presetOpts);
        if (seq !== presetSeq.current[mix]) return null;
        if (selectedIdRef.current !== destAtClick) return null;
        if (mix === "vod" && !vodEnabledRef.current) return null;
        if (mix === "live") setProfile(res.profile);
        else setVodProfile(res.profile);
        setDirty(true);
        toast.success(t("route.presetApplied"));
        return res.routing;
      } catch (err) {
        toast.error(err instanceof Error ? err.message : t("route.couldNotApplyThePreset"));
        return null;
      }
    },
    [presetOpts, t],
  );

  const applyPreset = useCallback(
    (presetId: string) => applyPresetTo("live", presetId),
    [applyPresetTo],
  );

  const applyVodPreset = useCallback(
    (presetId: string) => applyPresetTo("vod", presetId),
    [applyPresetTo],
  );

  const trackOptions = useMemo(
    () => tracks.map((t) => ({ value: String(t.index), label: `Track ${t.index + 1}` })),
    [tracks],
  );

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

  // Everything both editors need and neither owns. Bundled rather than passed
  // as fifteen separate props so that adding a control to the shared editor is
  // one change at each end instead of two per call site.
  const ctx: EditorContext = useMemo(
    () => ({
      tracks,
      levels,
      probed: source?.probed ?? false,
      annotations,
      onAnnotate: annotate,
      annotating,
      onAnnotatingChange: setAnnotating,
      annotationsStored,
      mixPresets,
      platformPresets,
      presetOpts,
      setPresetOpts,
      trackOptions,
      musicPlatformName: selectedPolicy?.name ?? "",
      musicDecision: selectedPolicy?.policy,
      musicTracks,
    }),
    [
      tracks,
      levels,
      source?.probed,
      annotations,
      annotate,
      annotating,
      annotationsStored,
      mixPresets,
      platformPresets,
      presetOpts,
      trackOptions,
      selectedPolicy,
      musicTracks,
    ],
  );

  // Twitch RTMP is the one combination where the second mix depends on a
  // negotiation, and it is the ONLY thing gated on the platform. The editor
  // itself is not: `routing.CompilePair` -> engine/destinations.go ->
  // ffmpeg.secondAudioMap is a real, correct two-mix egress for every other
  // target, so gating the control on Twitch would hide a working feature.
  const twitchRtmp = selected?.kind === "rtmp" && selected?.platform === "twitch";

  // The engine refuses the pair at PLAN time when this holds — see
  // engine.vodNeedsNegotiation and noteVODWithoutMultitrack — so the editor
  // must say the second mix is not being sent rather than let it read as live.
  const vodBlockedByToggle = twitchRtmp && !!vodProfile && !selected?.multitrack;

  // A compile error in a mix that is not configured must not be able to block
  // the save: `vodError` can hold the last message from before the switch was
  // turned off.
  const blockingError = liveError || (vodProfile ? vodError : "");

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
            <Button size="sm" onClick={save} disabled={!dirty || saving || !!blockingError}>
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
            <ProfileEditor
              idPrefix="live"
              ctx={ctx}
              profile={profile}
              onPatch={patch}
              onApplyPreset={applyPreset}
              onCompileError={setLiveError}
              allowAnnotating
              footer={(compiled) => (
                <>
                  Video is copied without re-encoding. Only this audio graph is applied, and only
                  this destination restarts when you save.
                  {(compiled?.videoDelayMs ?? 0) > 0 &&
                    ` Video is held back ${compiled?.videoDelayMs} ms at the input to let the audio run ahead.`}
                </>
              )}
            />

            {/* ---------- the second (VOD) mix ---------- */}
            <SecondMixCard
              profile={vodProfile}
              liveProfile={profile}
              restorable={stashedVod.current}
              onChange={(next) => {
                // Switching OFF stashes what is being dropped, so switching
                // back on is an UNDO rather than a fresh copy of the live mix.
                // See stashedVod.
                if (next === null && vodProfile) stashedVod.current = vodProfile;
                setVodProfile(next);
                vodEnabledRef.current = next !== null;
                setDirty(true);
              }}
              twitchRtmp={twitchRtmp}
              multitrack={selected.multitrack ?? false}
              blockedByToggle={vodBlockedByToggle}
              probed={ctx.probed}
            />

            {vodProfile && (
              <ProfileEditor
                idPrefix="vod"
                ctx={ctx}
                profile={vodProfile}
                onPatch={patchVod}
                onApplyPreset={applyVodPreset}
                onCompileError={setVodError}
                // "This graph IS the second audio track" is a statement about
                // OUTPUT, and there are three separate ways for it to be false.
                // It used to be the else-branch of one of them, which meant it
                // was asserted on the Twitch path where the negotiation decides
                // — contradicting the hedge SecondMixCard prints two cards
                // above. The unconditional sentence is reserved for the case
                // where nothing is conditional: a probed ingest, off Twitch.
                footer={() => {
                  if (vodBlockedByToggle) {
                    return (
                      <>
                        This is the graph the second audio track WOULD be built from. It is not
                        being emitted while Enhanced Broadcasting is off for this destination —
                        see the note above — so nothing here reaches Twitch yet. It is still
                        compiled and still saved.
                      </>
                    );
                  }
                  // engine/destinations.go refuses the pair on the provisional
                  // path for EVERY platform, not just Twitch: a provisional
                  // compile already runs on a guessed channel layout, and a
                  // second guessed mix on top of it doubles what is approximate.
                  if (!ctx.probed) {
                    return (
                      <>
                        This is the graph the second audio track WOULD be built from. The ingest
                        has not been probed, so the live mix is running on a guessed channel
                        layout and the engine does not add a second guessed mix on top of it —
                        nothing here is being emitted yet, on any platform. It returns by itself
                        on the first reconcile after a probe succeeds, which is the same moment
                        the live mix stops being provisional. It is still compiled and still
                        saved.
                      </>
                    );
                  }
                  if (twitchRtmp) {
                    return (
                      <>
                        This is the graph the second audio track would be built from. Whether it
                        is sent is decided at go-live by the Enhanced Broadcasting negotiation —
                        where Twitch does not grant it, this destination publishes the live mix
                        alone and this graph is not emitted. The answer appears on this
                        destination's card. Either way the video is copied without re-encoding
                        and both mixes are built from the same ingest in one FFmpeg process.
                      </>
                    );
                  }
                  return (
                    <>
                      This graph is the SECOND audio track. The first is the live mix above, the
                      video is copied without re-encoding either way, and both are built from the
                      same ingest in one FFmpeg process.
                    </>
                  );
                }}
              />
            )}
          </div>
        )}
      </div>
    </div>
  );
}

/* ==========================================================================
   The profile editor

   ONE EDITOR, TWO INSTANCES. `vodProfile` is a `*routing.Profile` — the very
   same type as a destination's primary `Profile` — so a second editor written
   beside this one would be two implementations of one thing, and the two would
   diverge on the first control either of them gained. Everything below the
   destination picker is therefore this component, rendered once for the live
   mix and once more for the second mix when there is one.

   WHY THE PROPS ARE SHAPED LIKE THIS. Two things had to change to make the
   block reusable rather than merely movable:

     - IDS HAD TO STOP BEING CONSTANTS. `id="norm"` was fine while there was one
       editor on the page and becomes a duplicate DOM id the moment there are
       two — at which point the `<label for>` points at whichever came first and
       clicking the second editor's label moves the first editor's control.
       Hence `idPrefix`, which every id here is built from.

     - EACH INSTANCE COMPILES ITSELF. The filter string under the controls is
       the honest part of this editor: it comes back from the same Go code that
       will run, not from a TypeScript reimplementation. A second mix that
       borrowed the first one's compile would be showing a graph that is not
       its own. So the debounce, the request and the result live here, and only
       the error travels back up — because refusing to save is the only thing
       the page can do with it that this component cannot.
   ========================================================================== */

/** What both editors need and neither owns: the ingest, the annotations, the
 *  presets and the platform's music policy are properties of the SOURCE and the
 *  DESTINATION, not of either mix. */
interface EditorContext {
  tracks: SourceTrack[];
  levels: Levels | null;
  probed: boolean;
  annotations: TrackAnnotation[];
  onAnnotate: (track: number, next: Partial<TrackAnnotation>) => void;
  annotating: boolean;
  onAnnotatingChange: (on: boolean) => void;
  annotationsStored: boolean | null;
  mixPresets: Preset[];
  platformPresets: Preset[];
  presetOpts: PresetOpts;
  setPresetOpts: (update: (o: PresetOpts) => PresetOpts) => void;
  trackOptions: { value: string; label: string }[];
  musicPlatformName: string;
  musicDecision?: MusicDecision;
  musicTracks: number[];
}

function ProfileEditor({
  idPrefix,
  ctx,
  profile,
  onPatch,
  onApplyPreset,
  onCompileError,
  allowAnnotating = false,
  footer,
}: {
  /** Namespaces every DOM id this editor emits. Must differ between instances. */
  idPrefix: string;
  ctx: EditorContext;
  profile: RoutingProfile;
  onPatch: (next: Partial<RoutingProfile>) => void;
  /** Applies the preset to THIS editor's profile and resolves with the routing
   *  the endpoint compiled for it, or null when the answer was discarded (a
   *  stale destination, a mix switched off, a superseded click) or the request
   *  failed. Must not reject: a null is how "nothing happened" is reported. */
  onApplyPreset: (presetId: string) => Promise<RoutingResult | null>;
  /** Called with "" when this mix compiles and with the message when it does
   *  not. MUST be referentially stable — a raw setState function is ideal —
   *  because it is a dependency of the compile effect below. */
  onCompileError: (message: string) => void;
  /** Track roles describe the SOURCE, so one editor owns the labelling UI and
   *  the other reads the result. Two buttons toggling one flag would look like
   *  two independent controls that mysteriously move together. */
  allowAnnotating?: boolean;
  footer: (compiled: RoutingResult | null) => ReactNode;
}) {
  const t = useT();
  const [compiled, setCompiled] = useState<RoutingResult | null>(null);
  const [compileError, setCompileError] = useState<string>("");

  // ---- compile on every edit, debounced ----
  //
  // The debounce cancels a request not yet SENT. `cancelled` discards the
  // answer to one already in flight, which is a different failure: two edits a
  // few hundred milliseconds apart put two requests on the wire, and nothing
  // guarantees they come back in order. Without this, a slow first response
  // landing after a fast second one paints a filter string that is not the one
  // the controls describe — and the whole claim this panel makes is that the
  // graph shown is the graph that will run.
  const debounceRef = useRef<number | undefined>(undefined);
  useEffect(() => {
    let cancelled = false;
    window.clearTimeout(debounceRef.current);
    debounceRef.current = window.setTimeout(() => {
      api
        .compileRouting(profile)
        .then((res) => {
          if (cancelled) return;
          setCompiled(res.routing);
          setCompileError("");
          onCompileError("");
        })
        .catch((err) => {
          if (cancelled) return;
          const msg = err instanceof Error ? err.message : t("route.couldNotCompileThisRouting");
          setCompiled(null);
          setCompileError(msg);
          onCompileError(msg);
        });
    }, 180);
    return () => {
      cancelled = true;
      window.clearTimeout(debounceRef.current);
    };
  }, [profile, t, onCompileError]);

  // An editor that unmounts — the second mix being switched off — must not
  // leave its last error behind, or the Save button stays disabled by a mix
  // that no longer exists.
  useEffect(() => () => onCompileError(""), [onCompileError]);

  // A preset changes this profile from outside the edit path, and the effect
  // above will not answer for another 180 ms plus a round trip. Until it does,
  // the Result card shows the graph, the warnings and the red compile error of
  // the profile the preset just REPLACED — and Save stays disabled by an error
  // belonging to a profile that no longer exists. Applying a preset to fix an
  // invalid mix is exactly when that is most visible and most wrong.
  //
  // The endpoint has already compiled the profile it is handing back, so take
  // that answer. This does NOT introduce a second source of truth: it writes
  // this editor's own `compiled`, the same state the effect writes, and the
  // effect overwrites it with the same result a moment later. The page keeps no
  // copy — it only passes the routing through to whichever editor asked.
  const handleApplyPreset = useCallback(
    (presetId: string) => {
      void onApplyPreset(presetId).then((routing) => {
        if (!routing) return;
        setCompiled(routing);
        setCompileError("");
        onCompileError("");
      });
    },
    [onApplyPreset, onCompileError],
  );

  const excludeRoles = profile.excludeRoles ?? [];
  // The compiler's own answer to "which tracks reach this destination", which
  // is the only one that survives a role exclusion.
  const mixedTracks = compiled?.tracks ?? [];

  return (
    <>
      {/* presets */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-1.5">
            <Wand2 className="h-3.5 w-3.5" /> Presets
          </CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <PresetRow
            presets={ctx.mixPresets}
            opts={ctx.presetOpts}
            setOpts={ctx.setPresetOpts}
            trackOptions={ctx.trackOptions}
            onApply={handleApplyPreset}
          />
          {ctx.platformPresets.length > 0 && (
            <div className="flex flex-col gap-1.5 border-t border-border pt-3">
              <div className="text-[10px] uppercase tracking-wider text-muted-foreground">
                Platform defaults
              </div>
              <PresetRow
                presets={ctx.platformPresets}
                opts={ctx.presetOpts}
                setOpts={ctx.setPresetOpts}
                trackOptions={ctx.trackOptions}
                onApply={handleApplyPreset}
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
            onValueChange={(v) => onPatch({ mode: v as "simple" | "matrix" })}
          >
            <div className="flex flex-wrap items-center justify-between gap-2">
              <TabsList>
                <TabsTrigger value="simple">{t("route.simple")}</TabsTrigger>
                <TabsTrigger value="matrix">{t("route.mixMatrix")}</TabsTrigger>
              </TabsList>

              <div className="flex items-center gap-2">
                {allowAnnotating && profile.mode === "simple" && (
                  <Button
                    variant={ctx.annotating ? "secondary" : "ghost"}
                    size="sm"
                    onClick={() => ctx.onAnnotatingChange(!ctx.annotating)}
                  >
                    <Tags /> Label tracks
                  </Button>
                )}
                <Label htmlFor={`${idPrefix}-norm`} className="whitespace-nowrap">
                  Clip protection
                </Label>
                <Select
                  value={profile.normalize}
                  onValueChange={(v) => onPatch({ normalize: v as NormalizeMode })}
                >
                  <SelectTrigger id={`${idPrefix}-norm`} className="w-56">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {(Object.keys(NORMALIZE_LABEL) as NormalizeMode[]).map((n) => (
                      <SelectItem key={n} value={n}>
                        {t(NORMALIZE_LABEL[n])}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            </div>

            <TabsContent value="simple">
              <TrackRows
                idPrefix={idPrefix}
                tracks={ctx.tracks}
                selection={profile.tracks ?? []}
                levels={ctx.levels}
                probed={ctx.probed}
                onChange={(next: TrackSel[]) => onPatch({ tracks: next })}
                annotations={ctx.annotations}
                onAnnotate={ctx.onAnnotate}
                annotating={allowAnnotating && ctx.annotating}
                excludeRoles={excludeRoles}
                duckTrigger={profile.ducking?.trigger ?? []}
                duckTarget={profile.ducking?.target ?? []}
              />
              {allowAnnotating && ctx.annotating && ctx.annotationsStored === false && (
                <p className="mt-2 text-[10px] text-warn">{t("route.localOnlyNote")}</p>
              )}
              {allowAnnotating && ctx.annotating && ctx.annotationsStored !== false && (
                <p className="mt-2 text-[10px] text-muted-foreground">{t("route.rolesNote")}</p>
              )}
            </TabsContent>

            <TabsContent value="matrix">
              <MixMatrix
                tracks={ctx.tracks}
                cells={profile.matrix ?? []}
                onChange={(next: MatrixCell[]) => onPatch({ matrix: next })}
              />
            </TabsContent>
          </Tabs>
        </CardContent>
      </Card>

      {/* ---------- music rights ---------- */}
      <MusicRightsCard
        platformName={ctx.musicPlatformName}
        decision={ctx.musicDecision}
        excludeRoles={excludeRoles}
        musicTracks={ctx.musicTracks}
        onExcludeRoles={(roles) => onPatch({ excludeRoles: roles.length ? roles : null })}
      />

      {/* ---------- loudness, delay, ducking ---------- */}
      <div className="grid gap-3 xl:grid-cols-2">
        <LoudnessCard
          loudness={profile.loudness ?? null}
          normalize={profile.normalize}
          onChange={(l) => onPatch({ loudness: l })}
        />
        <DelayCard
          delayMs={profile.delayMs ?? 0}
          videoDelayMs={compiled?.videoDelayMs ?? 0}
          onChange={(ms) => onPatch({ delayMs: ms })}
        />
      </div>

      <DuckingCard
        ducking={profile.ducking ?? null}
        mixedTracks={mixedTracks}
        allTracks={ctx.tracks.map((tr) => tr.index)}
        annotations={ctx.annotations}
        onChange={(d) => onPatch({ ducking: d })}
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
                  excludes{" "}
                  {excludeRoles.map((r) => ROLE_LABEL[r as Exclude<TrackRole, "">]).join(", ")}
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
          <p className="text-[10px] text-muted-foreground">{footer(compiled)}</p>
        </CardContent>
      </Card>
    </>
  );
}

/* ==========================================================================
   The second (VOD) mix

   The switch, and the whole of what the operator is told about whether the
   second mix is actually going anywhere.

   IT IS NOT GATED ON TWITCH. `routing.CompilePair` -> engine/destinations.go ->
   ffmpeg.secondAudioMap is a real two-mix egress and it is correct for every
   non-Twitch target, so hiding the control off Twitch would hide a feature that
   works. What IS gated on Twitch is the EXPLANATION, because Twitch is the one
   platform where the second track depends on a negotiation.

   IT MUST NEVER READ AS LIVE WHEN IT IS NOT. Two states in the engine say the
   second mix is being dropped, and each has its own sentence here:

     - Enhanced Broadcasting is off on a Twitch RTMP destination. The engine
       refuses the pair at PLAN time (engine.planDestinations, and
       noteVODWithoutMultitrack is the operator's copy for it) — nothing is
       negotiated, nothing is tried, and the second track is simply not sent.
       That is a definite fact this page knows before go-live, so it is stated
       here as one.
     - Enhanced Broadcasting is on and Twitch refuses. That is not knowable
       until go-live and this page does not pretend to know it; the copy points
       at the destination card, which carries the answer once it exists.
   ========================================================================== */

function SecondMixCard({
  profile,
  liveProfile,
  restorable,
  onChange,
  twitchRtmp,
  multitrack,
  blockedByToggle,
  probed,
}: {
  profile: RoutingProfile | null;
  /** Seeds a newly enabled second mix when there is nothing to restore. */
  liveProfile: RoutingProfile;
  /** The profile this switch dropped last time it was turned off, if any. */
  restorable: RoutingProfile | null;
  onChange: (next: RoutingProfile | null) => void;
  twitchRtmp: boolean;
  multitrack: boolean;
  blockedByToggle: boolean;
  /** Whether the ingest has been probed. An unprobed one drops the second mix
   *  on every platform — see engine/destinations.go's provisional path. */
  probed: boolean;
}) {
  const enable = (on: boolean) => {
    if (!on) return onChange(null);
    // RESTORE FIRST. Switching off drops the whole profile, and re-seeding from
    // the live mix would silently discard the exclusions, gains and loudness
    // target the operator set — a destructive edit with no undo, from a switch
    // that reads as reversible. Putting the last one back makes it reversible
    // in fact, which is better than a confirmation dialog: there is nothing to
    // confirm if nothing is lost.
    //
    // Otherwise seed from the live mix rather than from a blank profile. A
    // second mix is defined by how it DIFFERS from the first — drop the music,
    // keep the commentary — so starting from a copy means the first edit is the
    // actual decision, and an operator who switches this on and changes nothing
    // gets two identical tracks rather than a silent one.
    onChange(structuredClone(restorable ?? liveProfile));
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-1.5">
          <Archive className="h-3.5 w-3.5" /> Second (VOD) audio mix
          {twitchRtmp && <ExperimentalBadge />}
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        {/* THE SWITCH DESCRIBES THE CONFIGURATION, NOT THE WIRE. "This
            destination carries a second audio track" was the first wording and
            it is a claim about delivery — which is false whenever the engine
            refuses the pair at plan time, three lines below its own "not being
            sent" alert. Whether a second track is sent is answered by the
            states underneath; this label answers only whether a second mix
            exists to send. */}
        <div className="flex h-9 items-center gap-2">
          <Switch id="vod-enabled" checked={profile !== null} onCheckedChange={enable} />
          <Label htmlFor="vod-enabled" className="text-[11px] text-muted-foreground">
            {profile !== null
              ? "A second mix is configured for this destination"
              : "One audio mix (the normal case)"}
          </Label>
        </div>

        <span className="text-[10px] text-muted-foreground">
          A whole second mix of the same ingest, intended for a second audio track alongside the
          live one. The usual reason is an archive that must not carry licensed music while the
          live mix does. Off is the normal state.
          {profile === null && restorable !== null && (
            <> Switching this back on restores the mix you just turned off.</>
          )}
        </span>

        {/* State 0: not being sent, on every platform, and nothing on the
            destination card says so — engine/destinations.go's provisional
            branch drops the pair without setting a note, unlike the Twitch
            one. It is first because it outranks the others: an unprobed ingest
            is not a Twitch question. */}
        {!probed && profile !== null && (
          <div className="flex items-start gap-1.5 rounded border border-warn/30 bg-warn-dim px-2 py-1.5 text-[11px] text-warn">
            <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
            <span>
              This second mix is <strong>not being sent yet</strong>, on any platform. The ingest
              has not been probed, so the live mix is compiled on a guessed channel layout and
              the engine does not add a second guessed mix on top of one it is already calling
              approximate. It comes back by itself on the first reconcile after a probe succeeds.
              Everything below is still saved.
            </span>
          </div>
        )}

        {/* State 1: definitely not being sent, and this page knows why. */}
        {blockedByToggle && (
          <div className="flex items-start gap-1.5 rounded border border-warn/30 bg-warn-dim px-2 py-1.5 text-[11px] text-warn">
            <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
            <span>
              This second mix is <strong>not being sent</strong>. The ordinary Twitch RTMP ingest
              carries one audio track, and Enhanced Broadcasting is switched off for this
              destination — so the engine refuses the pair before the broadcast starts and
              publishes the live mix alone. Switch Enhanced Broadcasting on in this destination's
              settings to negotiate an ingest that takes it. Everything below is still saved.
            </span>
          </div>
        )}

        {/* State 2: it depends on a negotiation nobody has made yet. Stated as
            a dependency, never as a success — the answer does not exist until
            go-live, and it lands on the destination card. */}
        {twitchRtmp && multitrack && profile !== null && probed && (
          <span className="text-[10px] text-muted-foreground">
            Whether this second track is actually sent is decided at go-live: Twitch grants
            Enhanced Broadcasting only to a client with a GPU it supports, and where it is not
            granted this destination publishes the live mix alone to the ordinary Twitch ingest.
            What was decided appears on this destination's card once it goes live — it is not
            known here and this page does not claim it.
          </span>
        )}

        {/* Off Twitch there is no negotiation and nothing conditional: the
            generic two-mix egress is what runs. Said plainly so the Twitch
            caveats above are not read as applying everywhere. */}
        {!twitchRtmp && profile !== null && probed && (
          <span className="text-[10px] text-muted-foreground">
            This destination is not a Twitch RTMP one, so nothing is negotiated: both mixes are
            built in the same FFmpeg process and published as two audio tracks. Whether the far
            end accepts a second track is a property of that endpoint — an RTMP ingest that takes
            one track will typically ignore or reject the second.
          </span>
        )}

        {twitchRtmp && (
          <Experimental>
            On Twitch this depends on Enhanced Broadcasting. The negotiation runs against
            ingest.twitch.tv and succeeds — polyemesis's own tests reach the live endpoint on
            every run and watch Twitch grant a VOD audio track and mint a key. What has never
            been observed is a broadcast published through that key, so no second audio track has
            been seen arriving at Twitch. The mix you configure here is stored and compiled
            regardless, and on a non-Twitch destination it does not depend on any of this.
          </Experimental>
        )}
      </CardContent>
    </Card>
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
  const who = platformName || t("route.thisDestinationSPlatform");

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
                {p.lufs} LUFS — {t(p.label)}
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
              Clip protection is set to “{t(NORMALIZE_LABEL[normalize])}”, which polyemesis never
              overrides — you chose it. This target is stored but not applied. Switch clip
              protection to Auto or Loudness to arm it.
            </span>
          </div>
        )}

        <p className="text-[10px] leading-relaxed text-muted-foreground">
          {loudness ? (
            t("route.measuredOverTheWholeProgramme")
          ) : normalize === "loudnorm" ? (
            <>
              Clip protection is set to Loudness, which normalises to a fixed{" "}
              <span className="tnum">{LUFS_PODCAST} LUFS</span> — the value that shipped before
              targets were configurable. Choosing one above replaces it.
            </>
          ) : (
            t("route.withoutATargetTheMix")
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
            t("route.audioAndVideoGoOut")
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
            {ducking ? t("route.on") : t("clips.off")}
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
