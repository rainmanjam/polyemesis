import { Checkbox } from "@/components/ui/checkbox";
import { Slider } from "@/components/ui/slider";
import { Badge } from "@/components/ui/badge";
import { AudioMeter, channelLabels } from "./AudioMeter";
import { gainPct } from "@/lib/format";
import { cn } from "@/lib/utils";
import type { Levels, SourceTrack, TrackSel } from "@/lib/types";
import { AlertTriangle } from "lucide-react";

/* ===========================================================================
   Simple mode: one row per ingest track, with a checkbox and a gain slider.

   The design goal is that a glance answers "what audio does this platform
   get?". Selected rows are lit and carry a live meter; unselected rows are
   dimmed to near-invisible so the selected set reads as a shape, not a list
   you have to parse.
   =========================================================================== */

interface TrackRowsProps {
  tracks: SourceTrack[];
  selection: TrackSel[];
  levels?: Levels | null;
  probed: boolean;
  onChange: (next: TrackSel[]) => void;
}

export function TrackRows({ tracks, selection, levels, probed, onChange }: TrackRowsProps) {
  const selFor = (index: number): TrackSel =>
    selection.find((s) => s.track === index) ?? { track: index, enabled: false, gain: 1 };

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
        const peak = levels?.peak?.[t.index] ?? [];
        const rms = levels?.rms?.[t.index] ?? [];
        const hasLevels = peak.length > 0;

        return (
          <div
            key={t.index}
            className={cn(
              "grid grid-cols-[auto_5.5rem_1fr_9rem] items-center gap-3 rounded-md border px-2.5 py-2 transition-colors",
              sel.enabled
                ? "border-primary/35 bg-primary-dim/30"
                : "border-border bg-card opacity-55 hover:opacity-80",
            )}
          >
            <Checkbox
              id={`track-${t.index}`}
              checked={sel.enabled}
              onCheckedChange={(v) => update(t.index, { enabled: v === true })}
              aria-label={`Include track ${t.index + 1}`}
            />

            <label htmlFor={`track-${t.index}`} className="cursor-pointer select-none">
              <div className="text-[12px] font-semibold leading-tight">
                Track {t.index + 1}
              </div>
              <div className="font-mono text-[10px] leading-tight text-muted-foreground">
                {t.layout}
                {t.channels > 2 && " ↓ st"}
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
                <div className="font-mono text-[10px] text-subtle-foreground">
                  {probed ? "no signal" : "waiting for stream"}
                </div>
              )}
              {t.title && (
                <div className="mt-0.5 truncate text-[10px] text-muted-foreground">{t.title}</div>
              )}
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
 *  "what is this platform hearing?" must still be immediate. */
export function TrackSummary({
  tracks,
  className,
}: {
  tracks: number[] | null;
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
            title={on ? `Track ${i + 1} is included` : `Track ${i + 1} is excluded`}
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
