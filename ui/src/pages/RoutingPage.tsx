import { useCallback, useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";
import { EmptyState } from "@/components/EmptyState";
import { saveStateOf } from "@/lib/saveModel";
import { useNavigate, useParams } from "react-router";
import { toast } from "sonner";
import {
  AlertTriangle,
  Archive,
  ChevronDown,
  Clock,
  Code2,
  Gauge,
  Loader2,
  Music,
  ShieldCheck,
  SlidersHorizontal,
  Tags,
  Volume2,
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
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { PageHeader } from "@/components/AppLayout";
import { Experimental, ExperimentalBadge } from "@/components/Experimental";
import { TrackRows } from "@/components/signature/TrackRows";
import { MixMatrix } from "@/components/signature/MixMatrix";
import { RoutingSaveBar } from "@/components/signature/RoutingSaveBar";
import { routingChanges } from "@/lib/routingDiff";
import { PresetReviewDialog, type PresetReview } from "@/components/PresetReviewDialog";
import { FilterString } from "@/components/signature/FilterString";
import { useLiveData, useSourceTracks } from "@/hooks/useLiveData";
import { ApiError, api, type DestinationWithRouting } from "@/lib/api";
import { cn } from "@/lib/utils";
import { musicRailMark, musicRailReason, railTextClass } from "@/lib/musicRail";
import { duckSeed } from "@/lib/duckSeed";
import { storedMixes } from "@/lib/storedMixes";
import { readVodStash, writeVodStash } from "@/lib/vodStash";
import { isLevelOnlySave } from "@/lib/saveImpact";
import { autoApi } from "@/lib/autoApi";
import { outputState, reportKey, sourcePeak, type LoudnessFeed } from "@/lib/outputStrip";
import { OutputStrip } from "@/components/signature/OutputStrip";
import {
  carryMutes,
  destinationLiveBlock,
  setMuted,
} from "@/lib/liveTrackLevel";
import { RouteOverview, type OverviewRow, type OverviewTrack } from "@/components/signature/RouteOverview";
import { routedTracks, toggleTrackSelection } from "@/lib/routeOverview";
import { toneForState } from "@/lib/signal";
import {
  layoutTrackIndices,
  presetOptsFromRoles,
  presetPicks,
  roleLayoutFor,
  type RoleLayout,
} from "@/lib/presetTrackDefaults";
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
  TRACK_ROLES,
  type Destination,
  type DestinationId,
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
  ROLE_LABEL_KEY,
} from "@/lib/types";
import { useT, useTN, type Translator, type TranslationKey } from "@/lib/i18n";

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
  const { connected, levels, source, status, programme } = useLiveData();
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
  // The pickers the operator set by hand. Kept apart from presetOpts (the
  // server's defaults) so neither a re-read of the defaults nor a later
  // annotation can move them. See lib/presetTrackDefaults.ts.
  const [presetPicked, setPresetPicked] = useState<Partial<PresetOpts>>({});
  const [annotations, setAnnotations] = useState<TrackAnnotation[]>([]);
  // The programme `annotations` was seeded from, so the preset role defaults
  // can tell whether it describes the destination being edited. See
  // roleLayoutFor.
  const [annotationsFrom, setAnnotationsFrom] = useState<number | null>(null);
  const [annotating, setAnnotating] = useState(false);
  // Whether the server accepted an annotation write. Null means "not tried
  // yet"; false means this build cannot store them and the editor keeps them
  // in the browser rather than refusing to show the roles at all.
  const [annotationsStored, setAnnotationsStored] = useState<boolean | null>(null);
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  // app-council A95: the save model's other two facts, per destination, so a
  // "Saved" or a failure never follows the operator to another destination.
  // Derived against the selection below rather than reset in an effect.
  const [savedFor, setSavedFor] = useState<DestinationId | null>(null);
  const [failedFor, setFailedFor] = useState<DestinationId | null>(null);
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

  // The preset waiting on the review dialog (A14). applyPresetTo reads the
  // draft it will replace from draftRef, after its await.
  const [presetReview, setPresetReview] = useState<PresetReview | null>(null);

  // ---- what applyPresetTo re-checks AFTER its await ----
  //
  // A closure captures values, not state, so reading `selected` or `vodProfile`
  // inside an async callback reads what they were when the button was clicked —
  // which is exactly the question being asked. These are written at the same
  // moments the state they mirror is.
  const selectedIdRef = useRef<DestinationId | null>(null);
  const vodEnabledRef = useRef(false);
  // One counter per mix, so a live-mix preset and a second-mix preset in flight
  // at the same time do not cancel each other.
  const presetSeq = useRef<{ live: number; vod: number }>({ live: 0, vod: 0 });

  // A save that lands while the operator keeps editing writes the list, and
  // writing the list re-runs the selection effect, which would reseed the draft
  // and throw the newer edit away. This names the one destination whose next
  // reseed is skipped: the one on screen, whose draft still needs a Save.
  const keepDraftFor = useRef<DestinationId | null>(null);

  // The drafts as last rendered, for Save to compare after its awaits. A
  // closure holds the values from the click, which is exactly what is compared
  // AGAINST; this is the other side.
  const draftRef = useRef<{ profile: RoutingProfile | null; vod: RoutingProfile | null; dirty: boolean }>({
    profile: null,
    vod: null,
    dirty: false,
  });
  useEffect(() => {
    draftRef.current = { profile, vod: vodProfile, dirty };
  }, [profile, vodProfile, dirty]);

  // The live editor's latest compile and the draft it was compiled from. The
  // save bar compares it, levels masked, with the stored graph to say whether
  // Save restarts the destination or only moves levels (lib/saveImpact). A
  // compile of an older draft is never used: identity with `profile` decides.
  const [liveGraph, setLiveGraph] = useState<{
    profile: RoutingProfile;
    graph: string | null;
  } | null>(null);
  const noteLiveGraph = useCallback(
    (p: RoutingProfile, graph: string | null) => setLiveGraph({ profile: p, graph }),
    [],
  );

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
    setAnnotationsFrom(source.id);
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
    const keep = keepDraftFor.current;
    keepDraftFor.current = null;
    if (found && keep === found.destination.id) {
      // A save answered while the draft had moved on (see save): only the
      // stored row moves, and the newer edit stays pending.
      setSelected(found.destination);
      return;
    }
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
      // A stash from the destination we just left is not an undo for this one;
      // this one's own, from earlier in the tab, is (A14).
      stashedVod.current = readVodStash(found.destination.id);
      setDirty(false);
    }
  }, [id, list]);

  // An edit to the draft. It makes the draft dirty and retires a "Saved" or a
  // failure that described the draft before it (the useSaveModel rule).
  const markDirty = useCallback(() => {
    setDirty(true);
    setSavedFor(null);
    setFailedFor(null);
  }, []);

  const patch = useCallback((next: Partial<RoutingProfile>) => {
    setProfile((p) => (p ? { ...p, ...next } : p));
    markDirty();
  }, [markDirty]);

  const patchVod = useCallback((next: Partial<RoutingProfile>) => {
    setVodProfile((p) => (p ? { ...p, ...next } : p));
    markDirty();
  }, [markDirty]);

  // ---- annotations ----
  //
  // Written straight through rather than gathered behind the Save button: they
  // describe the SOURCE, so they are not this destination's to hold hostage.

  // WHICH PROGRAMME these labels describe, and the reason the autosave below is
  // allowed to exist at all (#497).
  //
  // The editor is opened on a DESTINATION, and a destination carries exactly
  // one programme, so the answer is never ambiguous from here. It used not to
  // be sent: the PUT named no source, the server resolved its default engine,
  // and typing a label while looking at programme 2 rewrote programme 1's
  // ingest and restarted its live destinations — no click, no confirmation,
  // just a 500 ms timer. Null only for a legacy row that predates sources, and
  // the server refuses rather than guessing for those.
  const editedSourceId = selected?.sourceId ?? null;

  const annotationsEdited = useRef(false);
  // The programme the pending edit was made against. Compared before the
  // autosave fires, so an edit typed while looking at programme 1 can never be
  // saved onto programme 2 because the operator switched destination inside the
  // debounce window (#497). Without it, adding the source id to the request
  // would only have moved the mis-scoped write rather than prevented it.
  const editedSourceRef = useRef<number | null>(null);
  const annotate = useCallback((track: number, next: Partial<TrackAnnotation>) => {
    annotationsEdited.current = true;
    editedSourceRef.current = editedSourceId;
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
  }, [editedSourceId]);

  useEffect(() => {
    if (!annotationsEdited.current) return;
    // The edit belongs to a programme that is no longer on screen: the operator
    // switched destination inside the debounce window. Saving it now would
    // write one programme's labels onto another's ingest, which is #497 with
    // the source id present and simply wrong.
    if (editedSourceRef.current !== editedSourceId) return;
    const timer = window.setTimeout(() => {
      api
        .putAnnotations(annotations, editedSourceId)
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
    // editedSourceId is a dependency, not just an argument: switching to a
    // destination on a different programme cancels a save that was already
    // pending for the old one, rather than letting it land under the new id.
    return () => window.clearTimeout(timer);
  }, [annotations, editedSourceId, t]);

  const save = async () => {
    if (!selected || !profile) return;
    // What Save sends. An edit made while the request is out replaces one of
    // these objects, and is not in what the server now holds.
    const sentProfile = profile;
    const sentVod = vodProfile;
    setSaving(true);
    setFailedFor(null);
    setSavedFor(null);
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
        !selected.enabled
          ? t("route.saved")
          : levelOnly
            ? t("route.savedLive", { name: selected.name })
            : t("route.savedRestarting", { name: selected.name }),
      );
      setSavedFor(selected.id);
      const fresh = await api.listDestinations();
      // Compared after the LAST await: an edit can land during the PUT or the
      // list read. If the draft is still the one that was sent, the stored row
      // is now the draft and the list write reseeds it. If it moved on, the
      // reseed would throw the newer edit away, so it is skipped and the form
      // stays dirty: those edits still need a Save.
      const now = draftRef.current;
      if (now.profile !== sentProfile || now.vod !== sentVod) {
        keepDraftFor.current = selected.id;
      } else {
        setDirty(false);
      }
      setList(fresh);
    } catch (err) {
      // The draft is kept (dirty stays true) so a retry needs no retyping, and
      // SaveStatus keeps saying so after the toast has gone.
      setFailedFor(selected.id);
      toast.error(err instanceof Error ? err.message : t("route.couldNotSaveTheRouting"));
    } finally {
      setSaving(false);
    }
  };

  // Throws away the pending edit and puts back what the server holds (A7). The
  // same reads the destination-select effect does, so a discard and a fresh
  // selection cannot disagree about what "stored" means.
  const discardChanges = () => {
    const found = list.find((d) => d.destination.id === selected?.id);
    if (!found) return;
    // A preset still in flight would land after this and re-dirty the form, so
    // every outstanding apply is retired here, the same way a destination switch
    // retires it (A7).
    presetSeq.current = { live: presetSeq.current.live + 1, vod: presetSeq.current.vod + 1 };
    const { profile: stored, vodProfile: storedVod } = storedMixes(found.destination);
    setProfile(stored);
    setVodProfile(storedVod);
    vodEnabledRef.current = storedVod !== null;
    stashedVod.current = null;
    writeVodStash(found.destination.id, null);
    setDirty(false);
    setSavedFor(null);
    setFailedFor(null);
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
  // The edited destination's programme's own layout, read when the editor
  // moves to a destination on a programme it has not read. The page's
  // annotations and tracks are the header programme's; a preset is applied
  // against the destination's (#497), so its role defaults must be too.
  const [destLayout, setDestLayout] = useState<RoleLayout | null>(null);
  useEffect(() => {
    if (editedSourceId === null) return;
    let cancelled = false;
    api
      .source(editedSourceId)
      .then((s) => {
        if (cancelled) return;
        setDestLayout({
          sourceId: editedSourceId,
          annotations: s.annotations ?? [],
          tracks: layoutTrackIndices(s.tracks),
        });
      })
      // No layout, no role defaults: the server's slots stand. A failed read
      // is not worth a toast on a page that works without it.
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [editedSourceId]);
  // What each preset picker shows and a preset is applied with: the server's
  // defaults, replaced by the track annotated with the picker's role ON THE
  // EDITED DESTINATION'S PROGRAMME, with the operator's own picks over both.
  // lib/presetTrackDefaults.ts.
  const shownPresetOpts = useMemo(() => {
    const local: RoleLayout | null =
      source && annotationsFrom === source.id
        ? { sourceId: source.id, annotations, tracks: tracks.map((tr) => tr.index) }
        : null;
    const roles = roleLayoutFor(editedSourceId, local, destLayout);
    return presetOptsFromRoles(presetOpts, roles.annotations, roles.tracks, presetPicked);
  }, [presetOpts, annotations, annotationsFrom, source, tracks, editedSourceId, destLayout, presetPicked]);
  // PresetRow's setter, recording whatever the edit changed as a pick.
  const pickPresetOpts = useCallback(
    (update: (o: PresetOpts) => PresetOpts) => {
      const picked = presetPicks(shownPresetOpts, update(shownPresetOpts));
      setPresetPicked((prev) => ({ ...prev, ...picked }));
    },
    [shownPresetOpts],
  );

  const applyPresetTo = useCallback(
    async (mix: "live" | "vod", presetId: string): Promise<RoutingResult | null> => {
      const destAtClick = selectedIdRef.current;
      const seq = (presetSeq.current[mix] += 1);
      try {
        // Against THIS destination's programme: a preset picks track indices,
        // and the default programme's layout is a different ingest (#497).
        const res = await api.applyPreset(presetId, shownPresetOpts, editedSourceId);
        if (seq !== presetSeq.current[mix]) return null;
        if (selectedIdRef.current !== destAtClick) return null;
        if (mix === "vod" && !vodEnabledRef.current) return null;
        // A preset arrives unmuted; a mute the operator set stays (carryMutes).
        const before = mix === "live" ? draftRef.current.profile : draftRef.current.vod;
        const after = mix === "live" ? carryMutes(res.profile, before) : res.profile;
        // SHOWN BEFORE IT LANDS (A14). What goes in, what comes out and which
        // destination changes, with Back to edit; nothing is written until the
        // operator says so, and even then only into the draft Save applies.
        const ok = await new Promise<boolean>((resolve) =>
          setPresetReview({ presetId, mix, before, after, resolve }),
        );
        setPresetReview(null);
        if (!ok) return null;
        if (seq !== presetSeq.current[mix]) return null;
        if (selectedIdRef.current !== destAtClick) return null;
        const wasDirty = draftRef.current.dirty;
        if (mix === "live") setProfile(after);
        else setVodProfile(after);
        markDirty();
        // Undo puts back the very draft the preset replaced, dirty flag and all,
        // and only while the same destination is still selected.
        toast.success(t("route.presetApplied"), {
          action: {
            label: t("routing.matrix.undo"),
            onClick: () => {
              if (selectedIdRef.current !== destAtClick) return;
              presetSeq.current = { ...presetSeq.current, [mix]: presetSeq.current[mix] + 1 };
              if (mix === "live") setProfile(before);
              else setVodProfile(before);
              setDirty(wasDirty);
            },
          },
        });
        return res.routing;
      } catch (err) {
        toast.error(err instanceof Error ? err.message : t("route.couldNotApplyThePreset"));
        return null;
      }
    },
    [shownPresetOpts, editedSourceId, t, markDirty],
  );

  const applyPreset = useCallback(
    (presetId: string) => applyPresetTo("live", presetId),
    [applyPresetTo],
  );

  const applyVodPreset = useCallback(
    (presetId: string) => applyPresetTo("vod", presetId),
    [applyPresetTo],
  );

  const storedRow = useMemo(
    () => list.find((r) => r.destination.id === selected?.id)?.destination ?? null,
    [list, selected?.id],
  );

  const storedRouting = useMemo(
    () => list.find((r) => r.destination.id === selected?.id)?.routing ?? null,
    [list, selected?.id],
  );
  // What Save will do to a running destination: a level-only change applies
  // live where the server supports it; anything else restarts it.
  const levelOnly = isLevelOnlySave({
    storedGraph: storedRouting?.filterComplex,
    draftGraph: liveGraph && liveGraph.profile === profile ? liveGraph.graph : null,
    secondMixChanged:
      JSON.stringify(vodProfile ?? null) !== JSON.stringify(storedRow?.vodProfile ?? null),
    storedProfile: storedRow?.profile,
    draftProfile: profile,
  });

  // What the pending save moves, for the save bar's scope line and live diff.
  const pendingChanges = useMemo(
    () =>
      dirty
        ? routingChanges({
            stored: storedRow?.profile,
            draft: profile,
            storedSecond: storedRow?.vodProfile ?? null,
            draftSecond: vodProfile ?? null,
          })
        : [],
    [dirty, storedRow, profile, vodProfile],
  );

  // ---- A10: the destination's output, measured after routing ----
  //
  // Polled like the Meters page polls it: the analyser publishes about once a
  // second and one card is not worth a socket subscription. Scoped to the
  // destination's own programme. `loudNow` advances on every attempt, failed or
  // not, so a feed that stops answering ages into "stale" instead of freezing
  // its last reading as live.
  const [loudFeed, setLoudFeed] = useState<LoudnessFeed | null>(null);
  const [loudNow, setLoudNow] = useState(() => Date.now());
  // Per destination: the last frame seen and when, on this browser's clock, it
  // arrived. Staleness is judged against this, never the server's `at`, so a
  // skewed clock cannot make every reading stale or a dead one live.
  const [loudSeen, setLoudSeen] = useState<Record<number, { key: string; at: number }>>({});
  const loudSourceId = selected?.sourceId ?? null;
  const hasSelection = selected !== null;
  useEffect(() => {
    if (!hasSelection) return;
    let cancelled = false;
    const read = () =>
      autoApi
        .loudness<LoudnessFeed>(loudSourceId)
        .then((f) => {
          if (cancelled) return;
          const now = Date.now();
          setLoudFeed(f);
          setLoudNow(now);
          setLoudSeen((prev) => {
            const next: Record<number, { key: string; at: number }> = {};
            for (const r of f.reports ?? []) {
              const key = reportKey(r);
              const was = prev[r.destinationId];
              next[r.destinationId] = was && was.key === key ? was : { key, at: now };
            }
            return next;
          });
        })
        .catch(() => {
          if (!cancelled) setLoudNow(Date.now());
        });
    void read();
    const timer = window.setInterval(read, 2000);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [hasSelection, loudSourceId]);

  // The source stage: the ingest meter over the tracks this destination sends
  // as stored (what is running), muted ones left out because a muted track's
  // silence at the output is the operator's, not a fault. Only from a live
  // meter feed of THIS destination's programme: the socket carries one.
  const stripSource = useMemo(() => {
    if (!connected || !storedRow || !storedRouting) return null;
    if (storedRow.sourceId != null && programme != null && storedRow.sourceId !== programme) return null;
    const muted = new Set((storedRow.profile.tracks ?? []).filter((s) => s.muted).map((s) => s.track));
    return sourcePeak(levels?.peak, (storedRouting.tracks ?? []).filter((t) => !muted.has(t)));
  }, [connected, storedRow, storedRouting, programme, levels]);

  // A26: mute and gain are draft edits now, so the only thing that stops them
  // is a destination with no volume stage at all: audio copy forwards its
  // audio untouched. Said once, above the rows, and every Mute is greyed.
  const muteBlocked = useMemo(() => {
    if (!storedRow) return null;
    const key = destinationLiveBlock(storedRow);
    return key ? t(key) : null;
  }, [storedRow, t]);

  const trackOptions = useMemo(
    () => tracks.map((tr) => ({ value: String(tr.index), label: t("routing.overview.trackLabel", { n: tr.index + 1 }) })),
    [tracks, t],
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
      sourceId: editedSourceId,
      tracks,
      levels,
      probed: source?.probed ?? false,
      // The METER FEED's liveness, which `probed` does not describe: probed is
      // the ingest layout. Without it every track reads "no signal" whenever
      // the meters are off, on the page where that reading decides whether a
      // track goes in the mix.
      meterFeedLive: connected,
      annotations,
      onAnnotate: annotate,
      annotating,
      onAnnotatingChange: setAnnotating,
      annotationsStored,
      mixPresets,
      platformPresets,
      presetOpts: shownPresetOpts,
      setPresetOpts: pickPresetOpts,
      trackOptions,
      musicPlatformName: selectedPolicy?.name ?? "",
      musicDecision: selectedPolicy?.policy,
      musicTracks,
      muteBlocked,
    }),
    [
      editedSourceId,
      tracks,
      levels,
      source?.probed,
      connected,
      annotations,
      annotate,
      annotating,
      annotationsStored,
      mixPresets,
      platformPresets,
      shownPresetOpts,
      pickPresetOpts,
      trackOptions,
      selectedPolicy,
      musicTracks,
      muteBlocked,
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

  /* A4: the Overview tab. Its rows read the same routing the editor edits: the
     live draft for the selected destination, and each other destination's stored
     mixes. A cell edit goes through `patch`, the path the Simple view uses, so the
     save bar and the dirty state treat both the same. */
  const [view, setView] = useState<"overview" | "editor">("editor");
  const overviewRows = useMemo<OverviewRow[]>(
    () =>
      list.map(({ destination: d }) => {
        const isSel = d.id === selected?.id;
        const stored = storedMixes(d);
        const live = isSel && profile ? profile : stored.profile;
        const vodMix = isSel ? vodProfile : stored.vodProfile;
        return {
          id: d.id,
          name: d.name,
          tone: d.enabled
            ? toneForState(status?.destinations?.find((x) => x.id === d.id)?.process?.state)
            : "idle",
          kind: !isSel ? "select" : live.mode === "matrix" ? "matrix" : "editable",
          routed: routedTracks(live),
          vodBound: vodMix !== null,
          vodRouted: vodMix ? routedTracks(vodMix) : [],
          guardedMusic: (live.excludeRoles ?? []).includes("music"),
          vodGuardedMusic: (vodMix?.excludeRoles ?? []).includes("music"),
        };
      }),
    [list, selected, profile, vodProfile, status],
  );
  const overviewTracks = useMemo<OverviewTrack[]>(
    () =>
      ctx.tracks.map((tr) => ({
        index: tr.index,
        title: ctx.annotations.find((a) => a.track === tr.index)?.label || tr.title || undefined,
      })),
    [ctx.tracks, ctx.annotations],
  );
  const toggleCell = useCallback(
    (track: number) => {
      patch({ tracks: toggleTrackSelection(profile?.tracks ?? [], track) });
    },
    [profile, patch],
  );
  /* The fix acts on the VOD mix: that is the archive the risk reaches, and each
     mix keeps its own guard. Muting the live mix would not clear the amber cell. */
  const keepMusicOutOfVod = useCallback(() => {
    if (!vodProfile) return;
    const roles = [...new Set([...(vodProfile.excludeRoles ?? []), "music" as TrackRole])];
    patchVod({ excludeRoles: roles });
  }, [vodProfile, patchVod]);

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
        {/* app-council A83: the kit's empty state. Destinations are added
            on the Dashboard, so the action goes there. */}
        <EmptyState
          title={t("dash.noDestinationsTitle")}
          body={t("route.noDestinations")}
          action={{ label: t("route.goToDashboard"), onClick: () => navigate("/") }}
        />
      </div>
    );
  }

  // A28: why Save changes is greyed, as visible text linked to it. A compile
  // error is named too: the mix it belongs to may sit inside the collapsed
  // Advanced section, out of view.
  const saveReason = !dirty
    ? t("route.nothingToSave")
    : blockingError
      ? blockingError
      : null;
  // app-council A95 + A26: the kit's save model, and ONE of it. Every edit,
  // mute and gain included, waits in the draft for Save changes. The draft
  // itself stays in this page's own state rather than in useSaveModel: the
  // hook never reseeds a dirty draft when `stored` moves (lib/saveModel.ts),
  // while this page must reseed on a destination switch and skip exactly one
  // reseed after a save that an edit overtook (keepDraftFor).
  const saveState = saveStateOf({
    dirty,
    saving,
    failed: selected !== null && failedFor === selected.id,
    justSaved: selected !== null && savedFor === selected.id,
  });
  return (
    <div className="p-3">
      <PageHeader
        title={t("route.audioRouting")}
        subtitle={t("route.audioRoutingDesc")}
      />

      <Tabs value={view} onValueChange={(v) => setView(v as "overview" | "editor")} className="mb-3">
        <TabsList>
          <TabsTrigger value="overview">{t("routing.overview.tab")}</TabsTrigger>
          <TabsTrigger value="editor">{t("routing.overview.editor")}</TabsTrigger>
        </TabsList>
      </Tabs>

      {view === "overview" && (
        <Card className="mb-3">
          <CardContent className="pt-3">
            <RouteOverview
              rows={overviewRows}
              tracks={overviewTracks}
              music={musicTracks}
              onToggle={toggleCell}
              onSelect={(id) => navigate(`/routing/${id}`)}
              onFix={(id) => {
                if (id === selected?.id) keepMusicOutOfVod();
              }}
            />
          </CardContent>
        </Card>
      )}

      <div
        className={cn(
          "grid gap-3 lg:grid-cols-[16rem_minmax(0,1fr)]",
          view === "overview" && "hidden",
        )}
      >
        {/* ---------- destination picker ---------- */}
        <Card className="h-fit">
          <CardHeader>
            <CardTitle>{t("route.destinations")}</CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-0.5">
            {list.map(({ destination: d, routing }) => {
              const pol = policyFor(d)?.policy;
              const guarded = d.profile.excludeRoles?.includes("music") ?? false;
              // A word, not a colour. See lib/musicRail.ts.
              const mark = musicRailMark(guarded, pol?.exclude ?? false);
              // The reason behind the word, on hover and to a screen reader (B1).
              const markReason = musicRailReason(guarded, pol?.exclude ?? false);
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
                  <span className="flex w-full items-center gap-1 text-[12px] font-medium">
                    <span className="min-w-0 break-words">{d.name}</span>
                    {mark && (
                      <span
                        title={markReason ? t(markReason) : undefined}
                        className={cn(
                          "ml-auto flex shrink-0 items-center gap-1 text-sm",
                          railTextClass(mark.tone, d.id === selected?.id),
                        )}
                      >
                        <Music className="h-3 w-3 shrink-0" />
                        {t(mark.label)}
                        {/* The reason, as text in the button: it is part of the
                            accessible name, which a title attribute is not. */}
                        <span className="sr-only">{markReason ? t(markReason) : ""}</span>
                      </span>
                    )}
                  </span>
                  <span
                    className={cn(
                      "break-words font-mono text-sm",
                      railTextClass("muted", d.id === selected?.id),
                    )}
                  >
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
            <OutputStrip
              state={outputState(loudFeed, selected.id, loudNow, loudSeen[selected.id]?.at ?? 0)}
              source={stripSource}
            />
            <ProfileEditor
              idPrefix="live"
              ctx={ctx}
              profile={profile}
              onPatch={patch}
              onApplyPreset={applyPreset}
              onCompileError={setLiveError}
              onCompiledGraph={noteLiveGraph}
              allowAnnotating
              footer={(compiled) => (
                <>
                  {t("route.copyFooter")}
                  {(compiled?.videoDelayMs ?? 0) > 0 &&
                    ` ${t("route.videoHeldBack", { ms: compiled?.videoDelayMs ?? 0 })}`}
                </>
              )}
              // A27: the second mix is an advanced decision, so it sits at the
              // end of the live editor's collapsed Advanced section. Mounted
              // while collapsed: its compile error must keep blocking Save.
              advancedExtra={
                <>
                {/* ---------- the second (VOD) mix ---------- */}
                <SecondMixCard
                  profile={vodProfile}
                  liveProfile={profile}
                  restorable={stashedVod.current}
                  onChange={(next) => {
                    // Switching OFF stashes what is being dropped, so switching
                    // back on is an UNDO rather than a fresh copy of the live mix.
                    // See stashedVod.
                    if (next === null && vodProfile) {
                      stashedVod.current = vodProfile;
                      writeVodStash(selected.id, vodProfile);
                    }
                    setVodProfile(next);
                    vodEnabledRef.current = next !== null;
                    markDirty();
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
                    nested
                    // "This graph IS the second audio track" is a statement about
                    // OUTPUT, and there are three separate ways for it to be false.
                    // It used to be the else-branch of one of them, which meant it
                    // was asserted on the Twitch path where the negotiation decides
                    // — contradicting the hedge SecondMixCard prints two cards
                    // above. The unconditional sentence is reserved for the case
                    // where nothing is conditional: a probed ingest, off Twitch.
                    footer={() => {
                      if (vodBlockedByToggle) {
                        return <>{t("route.vodFooterBlocked")}</>;
                      }
                      // engine/destinations.go refuses the pair on the provisional
                      // path for EVERY platform, not just Twitch: a provisional
                      // compile already runs on a guessed channel layout, and a
                      // second guessed mix on top of it doubles what is approximate.
                      if (!ctx.probed) {
                        return <>{t("route.vodFooterUnprobed")}</>;
                      }
                      if (twitchRtmp) {
                        return <>{t("route.vodFooterTwitch")}</>;
                      }
                      return <>{t("route.vodFooterSecond")}</>;
                    }}
                  />
                )}
                </>
              }
            />

          </div>
        )}
      </div>

      {/* ---------- pending change (A7) ---------- */}
      <RoutingSaveBar
        dirty={dirty}
        saving={saving}
        blocked={!!blockingError}
        destinationName={selected?.name ?? ""}
        restarts={!!selected?.enabled}
        levelOnly={levelOnly}
        changes={pendingChanges}
        saveState={saveState}
        reason={saveReason}
        onSave={save}
        onDiscard={discardChanges}
      />

      <PresetReviewDialog
        review={presetReview}
        presetName={(() => {
          const p = presets.find((x) => x.id === presetReview?.presetId);
          return p ? presetText(t, p).name : "";
        })()}
        destinationName={selected?.name ?? ""}
        live={!!selected?.enabled}
      />
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
  /** The programme this editor's destination carries, and what every request
   *  it makes must name. Null for a destination row that predates sources; the
   *  server refuses rather than compiling against the wrong ingest (#497). */
  sourceId: number | null;
  tracks: SourceTrack[];
  levels: Levels | null;
  probed: boolean;
  /** Whether the socket carrying `levels` is up. See lib/trackSignal. */
  meterFeedLive: boolean;
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
  /** Why no Mute can apply on this destination (audio copy), translated, or
   *  null. Holds for both mixes: it is the destination that has no volume stage. */
  muteBlocked: string | null;
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
  onCompiledGraph,
  advancedExtra,
  nested = false,
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
  /** Called with each compile of `profile`: its graph, or null when it did not
   *  compile. Must be referentially stable, like onCompileError. */
  onCompiledGraph?: (profile: RoutingProfile, graph: string | null) => void;
  /** Track roles describe the SOURCE, so one editor owns the labelling UI and
   *  the other reads the result. Two buttons toggling one flag would look like
   *  two independent controls that mysteriously move together. */
  allowAnnotating?: boolean;
  footer: (compiled: RoutingResult | null) => ReactNode;
  /** Rendered at the end of this editor's Advanced section: the live mix's
   *  editor carries the second (VOD) mix there (A27). */
  advancedExtra?: ReactNode;
  /** True for an editor that already sits inside another's Advanced section
   *  (the second mix): its own advanced cards are shown inline rather than
   *  behind a second disclosure inside the first. */
  nested?: boolean;
}) {
  const t = useT();
  const [compiled, setCompiled] = useState<RoutingResult | null>(null);
  const [compileError, setCompileError] = useState<string>("");
  // A27: the generated FILTER_COMPLEX string is for the operator who asks for
  // it, not printed under every edit.
  const [showGraph, setShowGraph] = useState(false);
  const graphId = useId();

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
        // Named, so the filter string under the editor is compiled against the
        // ingest THIS destination reads. Unnamed it came off the server's
        // default engine, and the panel's whole claim — that the graph shown is
        // the graph that will run — was false on a multi-source install (#497).
        .compileRouting(profile, ctx.sourceId)
        .then((res) => {
          if (cancelled) return;
          setCompiled(res.routing);
          setCompileError("");
          onCompileError("");
          onCompiledGraph?.(profile, res.routing.filterComplex);
        })
        .catch((err) => {
          if (cancelled) return;
          const msg = err instanceof Error ? err.message : t("route.couldNotCompileThisRouting");
          setCompiled(null);
          setCompileError(msg);
          onCompileError(msg);
          onCompiledGraph?.(profile, null);
        });
    }, 180);
    return () => {
      cancelled = true;
      window.clearTimeout(debounceRef.current);
    };
  }, [profile, ctx.sourceId, t, onCompileError, onCompiledGraph]);

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

  // The three starters, in this order when the server has them; anything it
  // does not offer is skipped rather than drawn as a dead tile.
  const starters = STARTER_PRESETS.flatMap((id) => ctx.mixPresets.filter((p) => p.id === id));
  const recipes = ctx.mixPresets.filter((p) => !STARTER_PRESETS.includes(p.id));

  const excludeRoles = profile.excludeRoles ?? [];
  // The compiler's own answer to "which tracks reach this destination", which
  // is the only one that survives a role exclusion.
  const mixedTracks = compiled?.tracks ?? [];

  // Loudness, A/V delay, ducking, the compiled result and (for the live mix)
  // the second mix: the advanced half of the editor (A27).
  const advancedBody = (
    <>
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

      {/* result: the compiler's summary, and the graph on request */}
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
                  {describeDelay(t, profile.delayMs ?? 0, compiled.videoDelayMs ?? 0)}
                </Badge>
              )}
              {profile.ducking && <Badge variant="outline">{t("route.badgeDucking")}</Badge>}
              {excludeRoles.length > 0 && (
                <Badge variant="warn">
                  {t("route.badgeExcludes", {
                    roles: excludeRoles.map((r) => t(ROLE_LABEL_KEY[r as Exclude<TrackRole, "">])).join(", "),
                  })}
                </Badge>
              )}
            </div>
          )}
        </CardHeader>
        <CardContent className="flex flex-col gap-2">
          {compiled && (
            <div className="flex flex-col gap-2">
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="w-fit"
                aria-expanded={showGraph}
                aria-controls={graphId}
                onClick={() => setShowGraph((v) => !v)}
              >
                <Code2 /> {t(showGraph ? "routingUx.hideGraph" : "routingUx.showGraph")}
              </Button>
              <div id={graphId}>
                {showGraph && <FilterString value={compiled.filterComplex} />}
              </div>
            </div>
          )}
          <p className="text-sm text-muted-foreground">{footer(compiled)}</p>
        </CardContent>
      </Card>

      {advancedExtra}
    </>
  );

  return (
    <>
      {/* presets: three starters in view, the rest a click away (A14), so the
          editor and Music rights are not pushed below the fold by a wall of
          equal-weight tiles. Every preset goes through the review dialog. */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-1.5">
            <Wand2 className="h-3.5 w-3.5" /> {t("routing.presets.title")}
          </CardTitle>
        </CardHeader>
        <CardContent className="flex flex-wrap items-start gap-2">
          <PresetRow
            presets={starters}
            opts={ctx.presetOpts}
            setOpts={ctx.setPresetOpts}
            trackOptions={ctx.trackOptions}
            onApply={handleApplyPreset}
          />
          {(recipes.length > 0 || ctx.platformPresets.length > 0) && (
            <Popover>
              <PopoverTrigger asChild>
                <Button variant="ghost" size="sm" data-testid="more-recipes">
                  <ChevronDown /> {t("routing.presets.more")}
                </Button>
              </PopoverTrigger>
              <PopoverContent align="start" className="flex w-[min(44rem,90vw)] flex-col gap-3">
                {recipes.length > 0 && (
                  <PresetRow
                    presets={recipes}
                    opts={ctx.presetOpts}
                    setOpts={ctx.setPresetOpts}
                    trackOptions={ctx.trackOptions}
                    onApply={handleApplyPreset}
                  />
                )}
                {ctx.platformPresets.length > 0 && (
                  <div className="flex flex-col gap-1.5 border-t border-border pt-3">
                    <div className="text-sm uppercase tracking-wider text-muted-foreground">
                      {t("routing.presets.platformDefaults")}
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
              </PopoverContent>
            </Popover>
          )}
        </CardContent>
      </Card>

      {/* ---------- mute and gain (A26, A27) ----------
          Which tracks go, how loud, and which are muted: all draft edits that
          wait for Save changes, like everything else on this page. */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-1.5">
            <Volume2 className="h-3.5 w-3.5" /> {t("routingUx.muteGainTitle")}
          </CardTitle>
        </CardHeader>
        <CardContent>
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
                    <Tags /> {t("route.labelTracks")}
                  </Button>
                )}
                <Label htmlFor={`${idPrefix}-norm`} className="whitespace-nowrap">
                  {t("route.clipProtection")}
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
              <MuteGainNote blocked={ctx.muteBlocked} />
              <TrackRows
                idPrefix={idPrefix}
                tracks={ctx.tracks}
                selection={profile.tracks ?? []}
                levels={ctx.levels}
                probed={ctx.probed}
                meterFeedLive={ctx.meterFeedLive}
                onChange={(next: TrackSel[]) => onPatch({ tracks: next })}
                annotations={ctx.annotations}
                onAnnotate={ctx.onAnnotate}
                annotating={allowAnnotating && ctx.annotating}
                excludeRoles={excludeRoles}
                duckTrigger={profile.ducking?.trigger ?? []}
                duckTarget={profile.ducking?.target ?? []}
                draftMute={{ disabledReason: ctx.muteBlocked }}
              />
              {allowAnnotating && ctx.annotating && ctx.annotationsStored === false && (
                <p className="mt-2 text-sm text-warn">{t("route.localOnlyNote")}</p>
              )}
              {allowAnnotating && ctx.annotating && ctx.annotationsStored !== false && (
                <p className="mt-2 text-sm text-muted-foreground">{t("route.rolesNote")}</p>
              )}
            </TabsContent>

            <TabsContent value="matrix">
              <MixMatrix
                tracks={ctx.tracks}
                cells={profile.matrix ?? []}
                onChange={(next: MatrixCell[]) => onPatch({ matrix: next })}
                muted={(profile.tracks ?? []).filter((s) => s.muted).map((s) => s.track)}
                onMute={(track, m) => onPatch({ tracks: setMuted(profile.tracks ?? [], track, m) })}
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

      {/* Problems with this mix stay in view whatever is collapsed: a compile
          error is why Save changes is refused, and a warning is something the
          operator should read before saving. */}
      {(compileError || (compiled?.warnings?.length ?? 0) > 0) && (
        <div className="flex flex-col gap-2">
          {compileError && (
            <div className="flex items-start gap-1.5 rounded border border-down/30 bg-down-dim px-2 py-1.5 text-sm text-down">
              <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
              <span>{compileError}</span>
            </div>
          )}
          {compiled?.warnings?.map((w) => (
            <div
              key={w}
              className="flex items-start gap-1.5 rounded border border-warn/30 bg-warn-dim px-2 py-1.5 text-sm text-warn"
            >
              <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
              <span>{w}</span>
            </div>
          ))}
        </div>
      )}

      {nested ? (
        advancedBody
      ) : (
        /* ---------- advanced (A27) ----------
           Collapsed by default, and native <details> so keyboard and screen
           readers get the disclosure for free. Its children stay MOUNTED while
           it is closed: the second mix's editor lives in here, and unmounting
           it would clear its compile error and re-enable Save over a mix that
           does not compile. */
        <details className="group rounded-md border border-border bg-card" data-testid="routing-advanced">
          <summary className="flex cursor-pointer list-none items-center gap-1.5 px-3 py-2.5 text-sm">
            <ChevronDown className="h-3.5 w-3.5 shrink-0 -rotate-90 transition-transform group-open:rotate-0" aria-hidden="true" />
            <SlidersHorizontal className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
            <span className="font-semibold">{t("routingUx.advanced")}</span>
            <span className="text-muted-foreground">· {t("routingUx.advancedSummary")}</span>
          </summary>
          <div className="flex flex-col gap-3 border-t border-border p-3">{advancedBody}</div>
        </details>
      )}
    </>
  );
}

/** What the operator is told about mute and gain (A26): that they wait for
 *  Save changes like every other edit, or, once for the whole destination, why
 *  no Mute can apply at all. */
function MuteGainNote({ blocked }: { blocked: string | null }) {
  const t = useT();
  if (blocked) {
    return (
      <div
        className="mb-2 flex items-start gap-1.5 rounded border border-warn/30 bg-warn-dim px-2 py-1.5 text-sm text-warn"
        data-testid="live-level-notice"
      >
        <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
        <span>{blocked}</span>
      </div>
    );
  }
  return <p className="mb-2 text-sm text-muted-foreground">{t("routingUx.muteGainHint")}</p>;
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
  const t = useT();
  const tn = useTN();
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
          <Archive className="h-3.5 w-3.5" /> {t("route.vodTitle")}
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
          <Label htmlFor="vod-enabled" className="text-sm text-muted-foreground">
            {profile !== null ? t("route.vodConfigured") : t("route.vodOneMix")}
          </Label>
        </div>

        <span className="text-sm text-muted-foreground">
          {t("route.vodNote")}
          {profile === null && restorable !== null && <> {t("route.vodRestores")}</>}
        </span>

        {/* State 0: not being sent, on every platform, and nothing on the
            destination card says so — engine/destinations.go's provisional
            branch drops the pair without setting a note, unlike the Twitch
            one. It is first because it outranks the others: an unprobed ingest
            is not a Twitch question. */}
        {!probed && profile !== null && (
          <div className="flex items-start gap-1.5 rounded border border-warn/30 bg-warn-dim px-2 py-1.5 text-sm text-warn">
            <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
            <span>
              {tn("route.vodUnprobedAlert", { b: <strong>{t("route.vodNotSentYet")}</strong> })}
            </span>
          </div>
        )}

        {/* State 1: definitely not being sent, and this page knows why. */}
        {blockedByToggle && (
          <div className="flex items-start gap-1.5 rounded border border-warn/30 bg-warn-dim px-2 py-1.5 text-sm text-warn">
            <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
            <span>
              {tn("route.vodBlockedAlert", { b: <strong>{t("route.vodNotSent")}</strong> })}
            </span>
          </div>
        )}

        {/* State 2: it depends on a negotiation nobody has made yet. Stated as
            a dependency, never as a success — the answer does not exist until
            go-live, and it lands on the destination card. */}
        {twitchRtmp && multitrack && profile !== null && probed && (
          <span className="text-sm text-muted-foreground">{t("route.vodTwitchDepends")}</span>
        )}

        {/* Off Twitch there is no negotiation and nothing conditional: the
            generic two-mix egress is what runs. Said plainly so the Twitch
            caveats above are not read as applying everywhere. */}
        {!twitchRtmp && profile !== null && probed && (
          <span className="text-sm text-muted-foreground">{t("route.vodNotTwitch")}</span>
        )}

        {twitchRtmp && (
          <Experimental>{t("route.vodExperimental")}</Experimental>
        )}
      </CardContent>
    </Card>
  );
}

/* ==========================================================================
   Presets
   ========================================================================== */

/** The presets shown without opening "More recipes": everything, no music and
 *  mic only, the three a first-time operator reaches for (A14). The ids are
 *  internal/routing/presets.go's. */
const STARTER_PRESETS: readonly string[] = ["everything", "no-music", "mic-only"];

/** The built-in mix presets' names and descriptions in the catalogue. The
 *  server sends English (internal/routing/presets.go); a preset it adds that
 *  is not listed here, and every platform preset, shows as sent. */
const PRESET_TEXT: Record<string, { name: TranslationKey; description: TranslationKey }> = {
  everything: { name: "route.preset.everything", description: "route.preset.everythingDesc" },
  "no-music": { name: "route.preset.noMusic", description: "route.preset.noMusicDesc" },
  "mic-only": { name: "route.preset.micOnly", description: "route.preset.micOnlyDesc" },
  "surround-downmix": { name: "route.preset.surround", description: "route.preset.surroundDesc" },
  "commentary-language": { name: "route.preset.commentary", description: "route.preset.commentaryDesc" },
  "clean-feed": { name: "route.preset.cleanFeed", description: "route.preset.cleanFeedDesc" },
  "except-music": { name: "route.preset.exceptMusic", description: "route.preset.exceptMusicDesc" },
};

function presetText(t: Translator, p: Preset): { name: string; description: string } {
  const k = PRESET_TEXT[p.id];
  return k ? { name: t(k.name), description: t(k.description) } : { name: p.name, description: p.description };
}

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
            title={presetText(t, p).description}
          >
            {presetText(t, p).name}
          </Button>
          {p.policy?.exclude && (
            <Badge variant="warn" className="w-fit">
              {t("route.railNoMusic")}
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
              className="h-6 text-sm"
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
              <SelectTrigger className="h-6 text-sm" aria-label={t("route.musicPolicy")}>
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
  const t = useT();
  return (
    <Select value={String(value)} onValueChange={(v) => onChange(Number(v))}>
      <SelectTrigger className="h-6 text-sm" aria-label={t("route.trackPickAria", { label })}>
        <span className="mr-1 text-muted-foreground">{label}</span>
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
  const tn = useTN();
  const excluded = excludeRoles.includes("music");
  const platformWants = decision?.exclude ?? false;
  const reason = decision?.reason || t("route.musicPolicyFallback");
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
          <ShieldCheck className="h-3.5 w-3.5" /> {t("routing.overview.musicRights")}
        </CardTitle>
        <Tooltip>
          <TooltipTrigger asChild>
            <span className="cursor-help">
              {excluded ? (
                <Badge variant="live">
                  <Music className="h-2.5 w-2.5" /> {t("route.musicExcluded")}
                </Badge>
              ) : platformWants ? (
                <Badge variant="down">
                  <Music className="h-2.5 w-2.5" /> {t("route.musicBeingSent")}
                </Badge>
              ) : (
                <Badge variant="outline">
                  <Music className="h-2.5 w-2.5" /> {t("route.musicAllowed")}
                </Badge>
              )}
            </span>
          </TooltipTrigger>
          <TooltipContent className="max-w-80 leading-relaxed">
            {excluded ? (
              <>
                {t("route.musicTipExcluded")}{" "}
                {platformWants && <>{t("route.musicTipExcludedWhy", { who, reason })} </>}
                {t("route.musicTipNothingElse")}
              </>
            ) : platformWants ? (
              <>{t("route.musicTipSent", { who, reason })}</>
            ) : (
              <>{t("route.musicTipUnknown")}</>
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
            <Badge variant="outline">{t("route.operatorOverride")}</Badge>
          )}
        </label>

        <p className="text-sm leading-relaxed text-muted-foreground">
          {excluded ? (
            musicTracks.length > 0 ? (
              <>
                {tn(
                  musicTracks.length === 1
                    ? "route.musicTracksExcludedOne"
                    : "route.musicTracksExcludedMany",
                  {
                    tracks: (
                      <span className="text-live">
                        {musicTracks
                          .map((n) => t("routing.overview.trackLabel", { n: n + 1 }))
                          .join(", ")}
                      </span>
                    ),
                  },
                )}
              </>
            ) : (
              <>
                {tn("route.noMusicTrack", {
                  labelTracks: <span className="text-foreground">{t("route.labelTracks")}</span>,
                })}
              </>
            )
          ) : (
            <>{t("route.everyTrackReaches")}</>
          )}
        </p>

        <div className="flex flex-wrap items-center gap-1.5 border-t border-border pt-2">
          <span className="mr-1 text-sm uppercase tracking-wider text-muted-foreground">
            {t("route.alsoExclude")}
          </span>
          {TRACK_ROLES.filter((r) => r !== "music").map((r) => {
            const on = excludeRoles.includes(r);
            return (
              <button
                key={r}
                onClick={() => toggleRole(r, !on)}
                aria-pressed={on}
                className={cn(
                  "rounded border px-1.5 py-0.5 text-sm font-semibold uppercase tracking-wider transition-colors",
                  on
                    ? "border-warn/40 bg-warn-dim text-warn"
                    : "border-border text-muted-foreground hover:border-border-strong hover:text-muted-foreground",
                )}
              >
                {t(ROLE_LABEL_KEY[r])}
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

/* Exported for its test: the Target box is greyed by the preset select
 * above it, and nothing on screen used to connect the two. */
export function LoudnessCard({
  loudness,
  normalize,
  onChange,
}: {
  loudness: Loudness | null;
  normalize: NormalizeMode;
  onChange: (l: Loudness | null) => void;
}) {
  const t = useT();
  const tn = useTN();
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
          <Gauge className="h-3.5 w-3.5" /> {t("route.loudnessTarget")}
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

        {/* WHY THE TARGET BOX IS GREY. It is bare-disabled whenever the select
            above is on a named preset, with nothing on screen connecting the
            two -- and a greyed number field with no reason reads as broken.
            A WARNING rather than a control: letting a keystroke silently switch
            the select to Custom would change the operator's chosen preset as a
            side effect of a typo, which is the larger harm. */}
        {loudness && value !== LOUDNESS_CUSTOM && (
          <p className="text-sm text-muted-foreground" data-testid="target-locked">
            {t("route.targetFollowsPreset")}
          </p>
        )}

        {inert && (
          <div className="flex items-start gap-1.5 rounded border border-warn/30 bg-warn-dim px-2 py-1.5 text-sm leading-relaxed text-warn">
            <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
            <span>{t("route.loudnessInert", { mode: t(NORMALIZE_LABEL[normalize]) })}</span>
          </div>
        )}

        <p className="text-sm leading-relaxed text-muted-foreground">
          {loudness ? (
            t("route.measuredOverTheWholeProgramme")
          ) : normalize === "loudnorm" ? (
            tn("route.loudnormFixed", {
              lufs: <span className="tnum">{LUFS_PODCAST} LUFS</span>,
            })
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

/* Exported for its test: this dropdown decides WHICH stream is delayed, and
 * rendered its own translation keys at the operator. */
export function DelayCard({
  delayMs,
  videoDelayMs,
  onChange,
}: {
  delayMs: number;
  videoDelayMs: number;
  onChange: (ms: number) => void;
}) {
  const t = useT();
  const tn = useTN();
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
          <Clock className="h-3.5 w-3.5" /> {t("route.avDelay")}
        </CardTitle>
        {delayMs !== 0 && (
          <Badge variant="armed" className="tnum">
            {describeDelay(t, delayMs, videoDelayMs)}
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
              {/* THROUGH t(). DELAY_LABEL holds TranslationKeys, and rendered
                  bare this dropdown offered "route.audioLaterThanVideo" and
                  "route.videoLaterThanAudio" -- so the one control that decides
                  WHICH stream is delayed had to be guessed at, and guessing
                  wrong restarts a live destination on save. */}
              <SelectItem value="audio">{t(DELAY_LABEL.audio)}</SelectItem>
              <SelectItem value="video">{t(DELAY_LABEL.video)}</SelectItem>
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

        <p className="text-sm leading-relaxed text-muted-foreground">
          {delayMs === 0 ? (
            t("route.audioAndVideoGoOut")
          ) : direction === "audio" ? (
            tn("route.delayAudioDesc", {
              ms: <span className="tnum text-foreground">{magnitude} ms</span>,
              max: MAX_DELAY_MS / 1000,
            })
          ) : (
            tn("route.delayVideoDesc", {
              ms: <span className="tnum text-foreground">{magnitude} ms</span>,
              max: -MIN_DELAY_MS / 1000,
            })
          )}
        </p>
      </CardContent>
    </Card>
  );
}

function describeDelay(t: Translator, delayMs: number, videoDelayMs: number): string {
  if (delayMs > 0) return t("route.delayBadgeAudio", { ms: delayMs });
  if (delayMs < 0) return t("route.delayBadgeVideo", { ms: videoDelayMs || -delayMs });
  return t("route.inSync");
}

/* ==========================================================================
   Ducking
   ========================================================================== */

/* Exported for its test: the card used to vanish -- Off switch included --
 * from destinations whose stored duck was still compiling. */
export function DuckingCard({
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
  // What turning this on would create, or null when nothing valid can be.
  // See lib/duckSeed.ts, which is also where the old "< 2 tracks in the mix"
  // guard is argued down: it hid the card, and with it the Off switch, from
  // destinations whose stored duck was still compiling and still ducking.
  const seed = duckSeed(mixedTracks, allTracks, annotations);

  // A stored duck is always reachable. Only CREATION is gated.
  if (!ducking && !seed) return null;

  const enable = (on: boolean) => {
    if (!on) return onChange(null);
    if (!seed) return;
    onChange(seed);
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
          <Waves className="h-3.5 w-3.5" /> {t("route.duckingTitle")}
        </CardTitle>
        <label className="flex items-center gap-2">
          <span className="text-sm uppercase tracking-wider text-muted-foreground">
            {ducking ? t("route.on") : t("clips.off")}
          </span>
          <Switch
            checked={ducking !== null}
            onCheckedChange={enable}
            disabled={ducking === null && seed === null}
            aria-label={t("route.enableDucking")}
          />
        </label>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {!ducking ? (
          <p className="text-sm leading-relaxed text-muted-foreground">
            {t("route.duckingNote")}
          </p>
        ) : (
          <>
            <div className="flex flex-col gap-1.5">
              <div className="text-sm uppercase tracking-wider text-muted-foreground">
                {t("route.duckWhenSpeak")}
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
              <p className="text-sm text-muted-foreground">
            {t("route.duckingTriggerNote")}
              </p>
            </div>

            <div className="flex flex-col gap-1.5">
              <div className="text-sm uppercase tracking-wider text-muted-foreground">
                {t("route.duckPushDown")}
              </div>
              {mixedTracks.length === 0 && (
                <p className="text-sm text-warn" data-testid="duck-no-target">
                  {t("route.duckingNoTargetInMix")}
                </p>
              )}
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
            <p className="text-sm text-muted-foreground">
              {t("route.duckDefaults", {
                threshold: DEFAULT_DUCK_THRESHOLD_DB,
                ratio: DEFAULT_DUCK_RATIO,
                attack: DEFAULT_DUCK_ATTACK_MS,
                release: DEFAULT_DUCK_RELEASE_MS,
              })}
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
  const t = useT();
  const name = annotation?.label || (annotation?.role ? t(ROLE_LABEL_KEY[annotation.role as Exclude<TrackRole, "">]) : "");
  return (
    <button
      onClick={onClick}
      aria-pressed={active}
      className={cn(
        "flex items-center gap-1.5 rounded border px-2 py-1 text-sm transition-colors",
        active
          ? tone === "armed"
            ? "border-armed/40 bg-armed-dim text-armed"
            : "border-warn/40 bg-warn-dim text-warn"
          : "border-border text-muted-foreground hover:border-border-strong hover:text-muted-foreground",
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
      <span className="flex items-baseline justify-between text-sm uppercase tracking-wider text-muted-foreground">
        {label}
        <span className="normal-case tracking-normal text-muted-foreground">{unit}</span>
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
    // Every track's mute and every cell's polarity go out as an explicit
    // boolean. An absent key is not "off" to a server that merges the arrays
    // over the stored ones by position: a cell list re-sorted by an edit would
    // hand one cell's stored `invert: true` to whichever cell now sits there.
    tracks: p.tracks ? p.tracks.map((s) => ({ ...s, muted: s.muted === true })) : p.tracks,
    matrix: p.matrix ? p.matrix.map((c) => ({ ...c, invert: c.invert === true })) : p.matrix,
    loudness: p.loudness ?? null,
    ducking: p.ducking ?? null,
    excludeRoles: p.excludeRoles?.length ? p.excludeRoles : null,
    delayMs: p.delayMs ?? 0,
  };
}
