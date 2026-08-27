import { Checkbox } from "@/components/ui/checkbox";
import { Slider } from "@/components/ui/slider";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
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
import { gainPct } from "@/lib/format";
import { cn } from "@/lib/utils";
import { trackChipTitle } from "@/lib/trackLabels";
import {
  MAX_LABEL_LEN,
  MAX_LANG_TAG_LEN,
  ROLE_LABEL,
  TRACK_ROLES,
  type Levels,
  type SourceTrack,
  type TrackAnnotation,
  type TrackRole,
  type TrackSel,
} from "@/lib/types";
import { AlertTriangle, VolumeX } from "lucide-react";

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
}: TrackRowsProps) {
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
            <div className="grid grid-cols-[auto_7.5rem_1fr_9rem] items-center gap-3">
              <Checkbox
                id={`${idPrefix}-track-${t.index}`}
                checked={sel.enabled}
                onCheckedChange={(v) => update(t.index, { enabled: v === true })}
                aria-label={`Include track ${t.index + 1}`}
              />

              <label
                htmlFor={`${idPrefix}-track-${t.index}`}
                className="min-w-0 cursor-pointer select-none"
              >
                <div className="flex items-center gap-1.5">
                  <span className="text-[12px] font-semibold leading-tight">
                    Track {t.index + 1}
                  </span>
                  {role !== "" && (
                    <Badge variant={excluded ? "warn" : "outline"} className="shrink-0">
                      {ROLE_LABEL[role]}
                    </Badge>
                  )}
                </div>
                <div className="truncate font-mono text-[10px] leading-tight text-muted-foreground">
                  {t.layout}
                  {t.channels > 2 && " ↓ st"}
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
                  <div className="font-mono text-[10px] text-subtle-foreground">
                    {TRACK_SIGNAL_TEXT[signal as Exclude<TrackSignal, "meter">]}
                  </div>
                )}
                <div className="mt-0.5 flex min-w-0 items-center gap-1.5">
                  {label && (
                    <span className="truncate text-[10px] text-muted-foreground">{label}</span>
                  )}
                  {ann.denoise && (
                    <Badge variant="outline" className="shrink-0">
                      <VolumeX className="h-2.5 w-2.5" /> denoise
                    </Badge>
                  )}
                  {duckTrigger.includes(t.index) && (
                    <Badge variant="armed" className="shrink-0" title="Speech here ducks the target tracks.">
                      ducks
                    </Badge>
                  )}
                  {duckTarget.includes(t.index) && (
                    <Badge variant="armed" className="shrink-0" title="Pushed down while a trigger track is speaking.">
                      ducked
                    </Badge>
                  )}
                </div>
              </div>

              <div className="flex items-center gap-2">
                <Slider
                  value={[sel.gain]}
                  min={0}
                  max={2}
                  step={0.05}
                  disabled={!sel.enabled}
                  onValueChange={([v]) => update(t.index, { gain: v })}
                  aria-label={`Track ${t.index + 1} gain`}
                  className={cn(!sel.enabled && "opacity-40")}
                />
                <span
                  className={cn(
                    "tnum w-10 shrink-0 text-right font-mono text-[11px]",
                    sel.enabled ? "text-foreground" : "text-subtle-foreground",
                    // Boost above unity is where clipping starts; flag it.
                    sel.enabled && sel.gain > 1 && "text-warn",
                  )}
                >
                  {gainPct(sel.gain)}
                </span>
              </div>
            </div>

            {excluded && (
              <Tooltip>
                <TooltipTrigger asChild>
                  <div className="flex w-fit cursor-help items-center gap-1.5 text-[10px] text-warn">
                    <AlertTriangle className="h-3 w-3 shrink-0" />
                    <span>
                      Not sent — this destination excludes the “{ROLE_LABEL[role]}” role.
                    </span>
                  </div>
                </TooltipTrigger>
                <TooltipContent className="max-w-72">
                  The exclusion follows the role, not the track number. Move this audio to
                  another track and it stays out of this destination, as long as the role
                  moves with it.
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
                  <SelectTrigger className="h-6 text-[10px]" aria-label={`Track ${t.index + 1} role`}>
                    <SelectValue placeholder="No role" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value={NO_ROLE}>No role</SelectItem>
                    {TRACK_ROLES.map((r) => (
                      <SelectItem key={r} value={r}>
                        {ROLE_LABEL[r]}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>

                <Input
                  value={ann.label ?? ""}
                  maxLength={MAX_LABEL_LEN}
                  placeholder={t.title || "Label — e.g. “Guest mic (Zoom)”"}
                  onChange={(e) => onAnnotate(t.index, { label: e.target.value })}
                  className="h-6 text-[10px]"
                  aria-label={`Track ${t.index + 1} label`}
                />

                <Input
                  value={ann.language ?? ""}
                  maxLength={MAX_LANG_TAG_LEN}
                  placeholder={t.language || "lang"}
                  onChange={(e) => onAnnotate(t.index, { language: e.target.value })}
                  className="h-6 text-[10px]"
                  aria-label={`Track ${t.index + 1} language`}
                />

                <label className="flex shrink-0 items-center gap-1.5 text-[10px] text-muted-foreground">
                  <Switch
                    checked={ann.denoise === true}
                    onCheckedChange={(v) => onAnnotate(t.index, { denoise: v })}
                    aria-label={`Denoise track ${t.index + 1}`}
                  />
                  denoise
                </label>
              </div>
            )}
          </div>
        );
      })}

      {!probed && (
        <div className="mt-1 flex items-start gap-1.5 text-[11px] text-muted-foreground">
          <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0 text-warn" />
          <span>
            No stream is arriving yet, so this shows the default six stereo tracks. The real
            layout is detected automatically once the encoder connects.
          </span>
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
              "tnum flex h-4 w-4 items-center justify-center rounded-[3px] font-mono text-[9px] font-bold",
              on
                ? "bg-primary text-primary-foreground"
                : "bg-muted text-subtle-foreground",
            )}
          >
            {i + 1}
          </span>
        );
      })}
      {list.length === 0 && (
        <Badge variant="down" className="ml-1">
          no audio
        </Badge>
      )}
    </div>
  );
}
