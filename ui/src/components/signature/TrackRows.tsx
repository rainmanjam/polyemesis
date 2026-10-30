import { useEffect, useRef, useState } from "react";
import { Checkbox } from "@/components/ui/checkbox";
import { Slider } from "@/components/ui/slider";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { AudioMeter } from "./AudioMeter";
import { channelLabels } from "@/lib/channels";
import { trackSignal, TRACK_SIGNAL_TEXT, type TrackSignal } from "@/lib/trackSignal";
import { useT } from "@/lib/i18n";
import { formatPeakDb } from "@/lib/meterScale";
import { gainPct } from "@/lib/format";
import { cn } from "@/lib/utils";
import { trackChipTitle } from "@/lib/trackLabels";
import {
  MAX_LABEL_LEN,
  MAX_LANG_TAG_LEN,
  ROLE_LABEL_KEY,
  TRACK_ROLES,
  type Levels,
  type SourceTrack,
  type TrackAnnotation,
  type TrackRole,
  type TrackSel,
} from "@/lib/types";
import { AlertTriangle, Volume2, VolumeX } from "lucide-react";
import { LIVE_MIN_GAIN } from "@/lib/liveTrackLevel";

/* ===========================================================================
   Simple mode: one row per ingest track, with a checkbox and a gain slider.

   The design goal is that a glance answers "what audio does this platform
   get?". Selected rows are lit and carry a live meter; unselected rows are
   dimmed to near-invisible so the selected set reads as a shape, not a list
   you have to parse.

   Roles are edited here rather than on a page of their own because the answer
   to "which one is the music?" is only obvious while you can see the meters
   move. The role badge stays visible even when the editor is closed: a
   destination can drop a track for its role, and a guarantee you cannot see is
   not a guarantee.
   =========================================================================== */

/** Sentinel for the Select: Radix treats "" as "no value chosen" and will not
 *  accept it as an item, but "no role" is a value the operator can pick. */
const NO_ROLE = "__none__";

/** How long a live gain is gathered before it is sent. Long enough that a run
 *  of arrow-key presses goes as one request; short enough to feel immediate. */
export const LIVE_GAIN_DEBOUNCE_MS = 150;

/** One row's live requests: at most one in flight, and the latest gain. */
interface RowQueue {
  pending?: { value: number; send: (track: number, gain: number) => Promise<void> };
  timer?: number;
  inFlight: boolean;
}

/** Live per-track mute and gain (I4).
 *
 *  Given only to the editor of the destination's LIVE mix: the route it calls
 *  writes the stored live profile, so the same control under the second (VOD)
 *  mix would silently change the live one. Absent, the rows are what they were:
 *  the slider edits the draft and Save sends it. */
export interface TrackRowsLive {
  /** Why this track cannot take a live level, already translated, or null.
   *  `inline` puts the reason under the row; false leaves it to a notice the
   *  page shows once, for a reason that holds for every row. */
  blocked: (track: number) => { reason: string; inline: boolean } | null;
  /** Each resolves once the server has answered, whatever it answered. */
  onMute: (track: number, muted: boolean) => Promise<void>;
  onGain: (track: number, gain: number) => Promise<void>;
}

interface TrackRowsProps {
  /** Namespaces the checkbox ids, and is REQUIRED rather than defaulted.
   *
   *  There are two of these on the routing page — the live mix and the second
   *  (VOD) one — and a constant `track-0` in both means the second editor's
   *  `<label for>` resolves to the FIRST checkbox in document order. Clicking
   *  "Track 1" under the VOD mix then toggles the track in the LIVE mix, which
   *  is a wrong edit to the wrong profile with nothing on screen saying so.
   *  No default, so a third instance cannot inherit the collision by omission. */
  idPrefix: string;
  tracks: SourceTrack[];
  selection: TrackSel[];
  levels?: Levels | null;
  probed: boolean;
  /** Whether the feed carrying `levels` is connected right now.
   *
   *  Separate from `probed`, which is the ingest LAYOUT and says nothing about
   *  whether anything is metering. Without it this component cannot tell a
   *  track nobody has measured from a track that is silent, and cannot tell a
   *  frozen last frame from live audio. Optional and defaulting to true so the
   *  read-only callers that never had a meter feed are unchanged. */
  meterFeedLive?: boolean;
  onChange: (next: TrackSel[]) => void;
  /** Source-side descriptions, keyed by track index. */
  annotations?: TrackAnnotation[];
  /** Absent means the roles are read-only — the editor renders badges only. */
  onAnnotate?: (track: number, patch: Partial<TrackAnnotation>) => void;
  /** Opens the per-track role/label/language/denoise line. */
  annotating?: boolean;
  /** Roles this destination refuses. A track carrying one is dropped before
   *  the mix is built, whatever its checkbox says. */
  excludeRoles?: TrackRole[];
  /** Tracks that cause a duck, and tracks pushed down by one. */
  duckTrigger?: number[];
  duckTarget?: number[];
  /** Live mute and gain; see TrackRowsLive. */
  live?: TrackRowsLive;
}

/** A15: the role chip is bounded by its column, cuts its text rather than
 *  wrapping into the meter, and keeps the full name on hover. */
export function RoleBadge({
  role,
  excluded,
}: {
  role: Exclude<TrackRole, "">;
  excluded: boolean;
}) {
  const t = useT();
  const name = t(ROLE_LABEL_KEY[role]);
  return (
    <Badge
      variant={excluded ? "warn" : "outline"}
      className="min-w-0 max-w-full truncate"
      title={name}
    >
      {name}
    </Badge>
  );
}

export function TrackRows({
  idPrefix,
  tracks,
  selection,
  levels,
  probed,
  meterFeedLive = true,
  onChange,
  annotations = [],
  onAnnotate,
  annotating = false,
  excludeRoles = [],
  duckTrigger = [],
  duckTarget = [],
  live,
}: TrackRowsProps) {
  const t = useT();
  const translate = useT();
  /* A10: the last measured peak per track, and when it was measured. Kept in a
     ref so a quiet row can say what it last did instead of going blank. Only a
     connected feed can add to it, so a frozen frame is never recorded as new. */
  const lastRef = useRef(new Map<number, { db: number; at: number }>());
  useEffect(() => {
    if (!meterFeedLive) return;
    const at = Date.now();
    for (const tr of tracks) {
      const peak = levels?.peak?.[tr.index] ?? [];
      if (peak.length === 0) continue;
      const db = Math.max(...peak);
      if (Number.isFinite(db)) lastRef.current.set(tr.index, { db, at });
    }
  }, [levels, tracks, meterFeedLive]);
  /* The slider's position while a live level is pending or in flight. The
     draft is not touched until the server has stored the level, so a refused
     change snaps back to what is stored instead of leaving the form dirty. It
     is cleared only once the row is idle: clearing it on the first of several
     answers would snap the thumb back while a later level is still out. */
  const [drag, setDrag] = useState<Record<number, number>>({});
  const [busy, setBusy] = useState<Record<number, boolean>>({});
  /* One live request per row at a time, and a gain gathered for
     LIVE_GAIN_DEBOUNCE_MS before it goes. The slider's keyboard commits on
     every keystroke; without this each one was its own POST, several in
     flight at once, and the first answer re-enabled Mute while the rest were
     still out. The send is captured at commit, so a level committed for one
     destination is never posted to the next one on screen. */
  const queues = useRef(new Map<number, RowQueue>());
  const queueFor = (index: number): RowQueue => {
    let q = queues.current.get(index);
    if (!q) queues.current.set(index, (q = { inFlight: false }));
    return q;
  };
  // Leaving the page does not drop a level the operator already set: a gain
  // still gathering goes now, to the destination it was set on (the send was
  // bound at commit). Its answer lands in the page's own state, not here.
  useEffect(() => {
    const all = queues.current;
    return () => {
      for (const [index, q] of all) {
        if (q.timer === undefined) continue;
        window.clearTimeout(q.timer);
        q.timer = undefined;
        // One still out sends the latest when it answers (finish), as before.
        if (q.inFlight || q.pending === undefined) continue;
        const { value, send } = q.pending;
        q.pending = undefined;
        void send(index, value);
      }
    };
  }, []);
  const idle = (q: RowQueue) => !q.inFlight && q.pending === undefined && q.timer === undefined;
  const finish = (index: number, q: RowQueue) => {
    q.inFlight = false;
    if (q.pending !== undefined && q.timer === undefined) {
      pump(index, q);
      return;
    }
    if (!idle(q)) return;
    setBusy(({ [index]: _done, ...rest }) => rest);
    setDrag(({ [index]: _gone, ...rest }) => rest);
  };
  // Sends the latest gathered gain, unless a request is already out: its
  // answer sends whatever is latest by then, once.
  const pump = (index: number, q: RowQueue) => {
    if (q.inFlight || q.pending === undefined) return;
    const { value, send } = q.pending;
    q.pending = undefined;
    q.inFlight = true;
    void send(index, value).finally(() => finish(index, q));
  };
  const commitGain = (index: number, value: number, send: TrackRowsLive["onGain"]) => {
    const q = queueFor(index);
    q.pending = { value, send };
    setBusy((b) => (b[index] ? b : { ...b, [index]: true }));
    if (q.timer !== undefined) window.clearTimeout(q.timer);
    q.timer = window.setTimeout(() => {
      q.timer = undefined;
      pump(index, q);
    }, LIVE_GAIN_DEBOUNCE_MS);
  };
  const pressMute = (index: number, work: () => Promise<void>) => {
    const q = queueFor(index);
    if (!idle(q)) return;
    q.inFlight = true;
    setBusy((b) => ({ ...b, [index]: true }));
    void work().finally(() => finish(index, q));
  };
  const selFor = (index: number): TrackSel =>
    selection.find((s) => s.track === index) ?? { track: index, enabled: false, gain: 1 };

  const annFor = (index: number): TrackAnnotation =>
    annotations.find((a) => a.track === index) ?? { track: index };

  const update = (index: number, patch: Partial<TrackSel>) => {
    const existing = selection.some((s) => s.track === index);
    const next = existing
      ? selection.map((s) => (s.track === index ? { ...s, ...patch } : s))
      : [...selection, { ...selFor(index), ...patch }];
    onChange(next.sort((a, b) => a.track - b.track));
  };

  return (
    <div className="flex flex-col gap-1">
      {tracks.map((t) => {
        const sel = selFor(t.index);
        const ann = annFor(t.index);
        const role = ann.role ?? "";
        const peak = levels?.peak?.[t.index] ?? [];
        const rms = levels?.rms?.[t.index] ?? [];
        /* Liveness is consulted BEFORE the levels, so a frozen last frame
           cannot be drawn as a live meter. LiveDataProvider now clears `levels`
           on close as well; this is the second of the two, and it is the one
           that survives somebody putting a stale frame back. */
        const signal = trackSignal({
          hasLevels: peak.length > 0,
          probed,
          feedLive: meterFeedLive,
        });
        const hasLevels = signal === "meter";
        // A role policy overrules the checkbox, so a checked-but-excluded row
        // has to read as "not sent" rather than as "sent".
        const excluded = role !== "" && excludeRoles.includes(role);
        const sending = sel.enabled && !excluded;
        const label = ann.label || t.title || "";
        const last = lastRef.current.get(t.index);
        const block = live ? live.blocked(t.index) : null;
        const liveOk = !!live && !block;
        const rowBusy = busy[t.index] === true;
        const shownGain = drag[t.index] ?? sel.gain;

        return (
          <div
            key={t.index}
            className={cn(
              "flex flex-col gap-2 rounded-md border px-2.5 py-2 transition-colors",
              excluded
                ? "border-warn/35 bg-warn-dim/25"
                : sending
                  ? "border-primary/35 bg-primary-dim/30"
                  : "border-border bg-card opacity-55 hover:opacity-80",
            )}
          >
            <div
              className={cn(
                "grid items-center gap-3",
                live
                  ? "grid-cols-[auto_minmax(0,8.5rem)_minmax(0,1fr)_9rem_auto]"
                  : "grid-cols-[auto_minmax(0,8.5rem)_minmax(0,1fr)_9rem]",
              )}
            >
              <Checkbox
                id={`${idPrefix}-track-${t.index}`}
                checked={sel.enabled}
                onCheckedChange={(v) => update(t.index, { enabled: v === true })}
                aria-label={translate("signature.track.include", { n: t.index + 1 })}
              />

              <label
                htmlFor={`${idPrefix}-track-${t.index}`}
                className="min-w-0 cursor-pointer select-none"
              >
                <div className="flex min-w-0 items-center gap-1.5">
                  <span className="shrink-0 text-[12px] font-semibold leading-tight">
                    {translate("lib.trackLabel", { n: t.index + 1 })}
                  </span>
                  {role !== "" && (
                    <RoleBadge role={role as Exclude<TrackRole, "">} excluded={excluded} />
                  )}
                  {sel.muted && (
                    <Badge variant="warn" className="shrink-0" data-testid={`muted-${t.index}`}>
                      <VolumeX className="h-2.5 w-2.5" /> {translate("route.live.mutedWord")}
                    </Badge>
                  )}
                </div>
                <div className="truncate font-mono text-sm leading-tight text-muted-foreground">
                  {t.layout}
                  {t.channels > 2 && ` ${translate("signature.track.downmix")}`}
                  {(ann.language || t.language) && ` · ${ann.language || t.language}`}
                </div>
              </label>

              <div className="min-w-0">
                {hasLevels ? (
                  <AudioMeter
                    rms={rms}
                    peak={peak}
                    labels={channelLabels(t.channels)}
                    barHeight={7}
                    barGap={2}
                  />
                ) : (
                  /* THREE STRINGS, NOT TWO. "no signal" was printed for every
                     track on a healthy ingest whenever the meters were off or
                     the first frame had not arrived — `probed` is the ingest
                     layout and says nothing about whether anything is
                     metering. On the page where routing is decided, "this
                     track carries nothing" is the reading that gets a track
                     left out of the mix. */
                  <>
                  <div className="font-mono text-sm text-muted-foreground">
                    {translate(TRACK_SIGNAL_TEXT[signal as Exclude<TrackSignal, "meter">])}
                  </div>
                  {last && (
                    <div className="font-mono text-xs text-muted-foreground">
                      {translate("routing.meters.lastPeak", {
                        db: formatPeakDb(last.db),
                        time: new Date(last.at).toLocaleTimeString(undefined, {
                          hour: "2-digit",
                          minute: "2-digit",
                          second: "2-digit",
                        }),
                      })}
                    </div>
                  )}
                  </>
                )}
                <div className="mt-0.5 flex min-w-0 items-center gap-1.5">
                  {label && (
                    <span className="truncate text-sm text-muted-foreground">{label}</span>
                  )}
                  {ann.denoise && (
                    <Badge variant="outline" className="shrink-0">
                      <VolumeX className="h-2.5 w-2.5" /> {translate("signature.track.denoise")}
                    </Badge>
                  )}
                  {duckTrigger.includes(t.index) && (
                    <Badge variant="armed" className="shrink-0" title={translate("signature.track.ducksTitle")}>
                      {translate("signature.track.ducks")}
                    </Badge>
                  )}
                  {duckTarget.includes(t.index) && (
                    <Badge variant="armed" className="shrink-0" title={translate("signature.track.duckedTitle")}>
                      {translate("signature.track.ducked")}
                    </Badge>
                  )}
                </div>
              </div>

              <div className="flex items-center gap-2">
                <Slider
                  value={[shownGain]}
                  min={liveOk ? LIVE_MIN_GAIN : 0}
                  max={2}
                  step={0.05}
                  // Not disabled while a level is in flight: disabling the
                  // focused control would drop keyboard focus to the page. The
                  // row's queue sends one level at a time, the latest.
                  disabled={!sel.enabled}
                  onValueChange={([v]) =>
                    liveOk
                      ? setDrag((d) => ({ ...d, [t.index]: v }))
                      : update(t.index, { gain: v })
                  }
                  onValueCommit={([v]) => {
                    if (!liveOk || !live) return;
                    // The route refuses zero: silence is the mute button.
                    commitGain(t.index, Math.max(v, LIVE_MIN_GAIN), live.onGain);
                  }}
                  aria-label={translate("signature.track.gainAria", { n: t.index + 1 })}
                  className={cn(!sel.enabled && "opacity-40")}
                />
                <span
                  className={cn(
                    "tnum w-10 shrink-0 text-right font-mono text-sm",
                    sel.enabled ? "text-foreground" : "text-muted-foreground",
                    // Boost above unity is where clipping starts; flag it.
                    sel.enabled && sel.gain > 1 && "text-warn",
                  )}
                >
                  {gainPct(shownGain)}
                </span>
              </div>

              {live && (
                <Button
                  type="button"
                  variant={sel.muted ? "secondary" : "ghost"}
                  size="sm"
                  aria-pressed={sel.muted === true}
                  aria-label={translate(
                    sel.muted ? "route.live.unmuteTrack" : "route.live.muteTrack",
                    { n: t.index + 1 },
                  )}
                  title={block?.reason}
                  disabled={!!block}
                  // Busy is announced, not enforced by `disabled`, which would
                  // drop keyboard focus mid-request; a press while busy is ignored.
                  aria-disabled={rowBusy || undefined}
                  aria-busy={rowBusy || undefined}
                  className={cn(sel.muted && "text-warn", rowBusy && "opacity-60")}
                  onClick={() => {
                    if (rowBusy) return;
                    const muted = !sel.muted;
                    pressMute(t.index, () => live.onMute(t.index, muted));
                  }}
                >
                  {sel.muted ? <VolumeX /> : <Volume2 />}
                  {translate(sel.muted ? "route.live.unmute" : "route.live.mute")}
                </Button>
              )}
            </div>

            {block?.inline && sel.enabled && (
              <div className="flex items-start gap-1.5 text-sm text-muted-foreground" data-testid={`live-block-${t.index}`}>
                <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0 text-warn" />
                <span>{block.reason}</span>
              </div>
            )}

            {excluded && (
              <Tooltip>
                <TooltipTrigger asChild>
                  <div className="flex w-fit cursor-help items-center gap-1.5 text-sm text-warn">
                    <AlertTriangle className="h-3 w-3 shrink-0" />
                    <span>
                      {translate("signature.track.excludedRole", { role: translate(ROLE_LABEL_KEY[role]) })}
                    </span>
                  </div>
                </TooltipTrigger>
                <TooltipContent className="max-w-72">
                  {translate("signature.track.excludedRoleHint")}
                </TooltipContent>
              </Tooltip>
            )}

            {annotating && onAnnotate && (
              <div className="grid grid-cols-[7rem_minmax(0,1fr)_5rem_auto] items-center gap-2 border-t border-border/70 pt-2">
                <Select
                  value={role === "" ? NO_ROLE : role}
                  onValueChange={(v) =>
                    onAnnotate(t.index, { role: v === NO_ROLE ? "" : (v as TrackRole) })
                  }
                >
                  <SelectTrigger className="h-6 text-sm" aria-label={translate("signature.track.roleAria", { n: t.index + 1 })}>
                    <SelectValue placeholder={translate("signature.track.noRole")} />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value={NO_ROLE}>{translate("signature.track.noRole")}</SelectItem>
                    {TRACK_ROLES.map((r) => (
                      <SelectItem key={r} value={r}>
                        {translate(ROLE_LABEL_KEY[r])}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>

                <Input
                  value={ann.label ?? ""}
                  maxLength={MAX_LABEL_LEN}
                  placeholder={t.title || translate("signature.track.labelPlaceholder")}
                  onChange={(e) => onAnnotate(t.index, { label: e.target.value })}
                  className="h-6 text-sm"
                  aria-label={translate("signature.track.labelAria", { n: t.index + 1 })}
                />

                <Input
                  value={ann.language ?? ""}
                  maxLength={MAX_LANG_TAG_LEN}
                  placeholder={t.language || translate("signature.track.langPlaceholder")}
                  onChange={(e) => onAnnotate(t.index, { language: e.target.value })}
                  className="h-6 text-sm"
                  aria-label={translate("signature.track.languageAria", { n: t.index + 1 })}
                />

                <label className="flex shrink-0 items-center gap-1.5 text-sm text-muted-foreground">
                  <Switch
                    checked={ann.denoise === true}
                    onCheckedChange={(v) => onAnnotate(t.index, { denoise: v })}
                    aria-label={translate("signature.track.denoiseAria", { n: t.index + 1 })}
                  />
                  {translate("signature.track.denoise")}
                </label>
              </div>
            )}
          </div>
        );
      })}

      {!probed && (
        <div className="mt-1 flex items-start gap-1.5 text-sm text-muted-foreground">
          <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0 text-warn" />
          <span>{t("meters.defaultTracksNote")}</span>
        </div>
      )}
    </div>
  );
}

/** Compact read-only rendering of which tracks a destination mixes.
 *  Used on destination cards, where space is tight but the answer to
 *  "what is this platform hearing?" must still be immediate.
 *
 *  `labels` NAMES THE NUMBERS. Six identical chips reading 1..6 say which
 *  tracks a platform gets and nothing about what those tracks ARE, so an
 *  operator checking that the podcast feed carries the mic and not the music
 *  had to hold the routing editor's ordering in their head while reading the
 *  dashboard. The names are the ones the editor shows, resolved by
 *  lib/trackLabels.ts, and they are OPTIONAL: absent, every chip keeps the
 *  wording it had, which is what a caller with no source snapshot -- or one
 *  describing a different programme -- must fall back to. */
export function TrackSummary({
  tracks,
  labels,
  className,
}: {
  tracks: number[] | null;
  labels?: (string | undefined)[] | null;
  className?: string;
}) {
  const t = useT();
  const list = tracks ?? [];
  return (
    <div className={cn("flex items-center gap-1", className)}>
      {Array.from({ length: 6 }, (_, i) => {
        const on = list.includes(i);
        return (
          <span
            key={i}
            title={trackChipTitle(i, on, labels?.[i])}
            className={cn(
              "tnum flex h-4 w-4 items-center justify-center rounded-[3px] font-mono text-sm font-bold",
              on
                ? "bg-primary text-primary-foreground"
                : "bg-muted text-muted-foreground",
            )}
          >
            {i + 1}
          </span>
        );
      })}
      {list.length === 0 && (
        <Badge variant="down" className="ml-1">
          {t("signature.track.noAudio")}
        </Badge>
      )}
    </div>
  );
}
