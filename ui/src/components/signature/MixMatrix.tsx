import { useMemo } from "react";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { channelLabels } from "./AudioMeter";
import type { MatrixCell, SourceTrack } from "@/lib/types";
import { RotateCcw } from "lucide-react";

/* ===========================================================================
   Advanced mode: the full mix matrix.

   Every source channel of every track gets a column; the destination's L and R
   get a row. Each cell is a gain from 0.0 to 2.0. This subsumes simple mode and
   is what makes "take only the rear channels of the 5.1 track" or "pan the mic
   hard left" expressible at all.

   Interaction is deliberately keyboard-and-drag free: a cell is a small
   number input plus a fill bar. Sliders in a 2 x 36 grid would be unusable,
   and a drag-to-set surface would be imprecise for values that must often be
   exactly 0, 0.707 or 1.
   =========================================================================== */

const MAX_GAIN = 2;

interface MixMatrixProps {
  tracks: SourceTrack[];
  cells: MatrixCell[];
  onChange: (next: MatrixCell[]) => void;
}

export function MixMatrix({ tracks, cells, onChange }: MixMatrixProps) {
  /** Fast lookup keyed by track/channel/out. */
  const index = useMemo(() => {
    const m = new Map<string, number>();
    for (const c of cells) m.set(`${c.track}:${c.channel}:${c.out}`, c.gain);
    return m;
  }, [cells]);

  const gainAt = (track: number, channel: number, out: number) =>
    index.get(`${track}:${channel}:${out}`) ?? 0;

  const setGain = (track: number, channel: number, out: number, gain: number) => {
    const clamped = Math.max(0, Math.min(MAX_GAIN, gain));
    const key = `${track}:${channel}:${out}`;
    const without = cells.filter((c) => `${c.track}:${c.channel}:${c.out}` !== key);
    // A zero cell carries no information, so it is dropped rather than stored.
    // That keeps the saved profile and the generated filter minimal.
    const next = clamped > 0 ? [...without, { track, channel, out, gain: clamped }] : without;
    next.sort((a, b) => a.track - b.track || a.out - b.out || a.channel - b.channel);
    onChange(next);
  };

  const clearAll = () => onChange([]);

  const totalCols = tracks.reduce((n, t) => n + t.channels, 0);

  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-between">
        <div className="text-[11px] text-muted-foreground">
          {totalCols} source channel{totalCols === 1 ? "" : "s"} → 2 output channels.
          Gain 0–2 per cell; 0 removes the connection.
        </div>
        <Button variant="ghost" size="sm" onClick={clearAll}>
          <RotateCcw /> Clear
        </Button>
      </div>

      <div className="overflow-x-auto rounded-md border border-border bg-card">
        <table className="w-full border-collapse">
          <thead>
            {/* Track grouping header, so a 5.1 track's six columns read as one
                unit rather than six unrelated channels. */}
            <tr>
              <th className="sticky left-0 z-10 bg-card px-2 py-1 text-left text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
                Out
              </th>
              {tracks.map((t, ti) => (
                <th
                  key={t.index}
                  colSpan={t.channels}
                  className={cn(
                    "border-b border-border px-1 py-1 text-center text-[10px] font-semibold uppercase tracking-wider text-muted-foreground",
                    ti > 0 && "border-l border-border-strong",
                  )}
                >
                  Track {t.index + 1}
                  <span className="ml-1 font-mono normal-case text-subtle-foreground">
                    {t.layout}
                  </span>
                </th>
              ))}
            </tr>
            <tr>
              <th className="sticky left-0 z-10 bg-card" />
              {tracks.map((t, ti) =>
                channelLabels(t.channels).map((label, ci) => (
                  <th
                    key={`${t.index}-${ci}`}
                    className={cn(
                      "border-b border-border px-1 pb-1 text-center font-mono text-[10px] font-normal text-subtle-foreground",
                      ti > 0 && ci === 0 && "border-l border-border-strong",
                    )}
                  >
                    {label}
                  </th>
                )),
              )}
            </tr>
          </thead>
          <tbody>
            {[0, 1].map((out) => (
              <tr key={out}>
                <td className="sticky left-0 z-10 bg-card px-2 py-1 font-mono text-[11px] font-semibold">
                  {out === 0 ? "L" : "R"}
                </td>
                {tracks.map((t, ti) =>
                  Array.from({ length: t.channels }, (_, ch) => {
                    const g = gainAt(t.index, ch, out);
                    return (
                      <td
                        key={`${t.index}-${ch}-${out}`}
                        className={cn(
                          "p-0.5",
                          ti > 0 && ch === 0 && "border-l border-border-strong",
                        )}
                      >
                        <MatrixCellInput
                          value={g}
                          onChange={(v) => setGain(t.index, ch, out, v)}
                          label={`Track ${t.index + 1} channel ${ch + 1} to ${out === 0 ? "left" : "right"}`}
                        />
                      </td>
                    );
                  }),
                )}
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <MatrixQuickFills tracks={tracks} onApply={onChange} />
    </div>
  );
}

function MatrixCellInput({
  value,
  onChange,
  label,
}: {
  value: number;
  onChange: (v: number) => void;
  label: string;
}) {
  const active = value > 0;
  // The fill bar makes the matrix scannable: you read the routing shape from
  // the pattern of lit cells before reading a single number.
  const fill = Math.min(1, value / MAX_GAIN);

  return (
    <div
      className={cn(
        "relative h-7 w-11 overflow-hidden rounded-[3px] border transition-colors",
        active ? "border-primary/40" : "border-border bg-muted/40",
      )}
    >
      {active && (
        <div
          className="absolute inset-y-0 left-0 bg-primary/30"
          style={{ width: `${fill * 100}%` }}
          aria-hidden
        />
      )}
      <input
        type="number"
        inputMode="decimal"
        min={0}
        max={MAX_GAIN}
        step={0.05}
        value={active ? Number(value.toFixed(4)) : 0}
        aria-label={label}
        onChange={(e) => {
          const v = Number.parseFloat(e.target.value);
          onChange(Number.isFinite(v) ? v : 0);
        }}
        className={cn(
          "tnum relative h-full w-full bg-transparent text-center font-mono text-[11px] outline-none",
          "[appearance:textfield] [&::-webkit-inner-spin-button]:appearance-none [&::-webkit-outer-spin-button]:appearance-none",
          active ? "text-foreground" : "text-subtle-foreground",
        )}
      />
    </div>
  );
}

/** One-click starting points, because typing 72 cells by hand is nobody's idea
 *  of a routing editor. */
function MatrixQuickFills({
  tracks,
  onApply,
}: {
  tracks: SourceTrack[];
  onApply: (cells: MatrixCell[]) => void;
}) {
  const identity = () => {
    const out: MatrixCell[] = [];
    for (const t of tracks) {
      if (t.channels === 1) {
        out.push({ track: t.index, channel: 0, out: 0, gain: 1 });
        out.push({ track: t.index, channel: 0, out: 1, gain: 1 });
      } else {
        out.push({ track: t.index, channel: 0, out: 0, gain: 1 });
        out.push({ track: t.index, channel: 1, out: 1, gain: 1 });
      }
    }
    onApply(out);
  };

  const firstTrackOnly = () => {
    const t = tracks[0];
    if (!t) return;
    const out: MatrixCell[] =
      t.channels === 1
        ? [
            { track: t.index, channel: 0, out: 0, gain: 1 },
            { track: t.index, channel: 0, out: 1, gain: 1 },
          ]
        : [
            { track: t.index, channel: 0, out: 0, gain: 1 },
            { track: t.index, channel: 1, out: 1, gain: 1 },
          ];
    onApply(out);
  };

  const rearsOnly = () => {
    const surround = tracks.find((t) => t.channels >= 6);
    if (!surround) return;
    // 5.1 channel order is FL FR FC LFE BL BR, so the rears are 4 and 5.
    onApply([
      { track: surround.index, channel: 4, out: 0, gain: 1 },
      { track: surround.index, channel: 5, out: 1, gain: 1 },
    ]);
  };

  const hasSurround = tracks.some((t) => t.channels >= 6);

  return (
    <div className="flex flex-wrap items-center gap-1.5">
      <span className="text-[10px] uppercase tracking-wider text-muted-foreground">Fill</span>
      <Button variant="outline" size="sm" onClick={identity}>
        All tracks 1:1
      </Button>
      <Button variant="outline" size="sm" onClick={firstTrackOnly}>
        Track 1 only
      </Button>
      {hasSurround && (
        <Button variant="outline" size="sm" onClick={rearsOnly}>
          Surround rears only
        </Button>
      )}
    </div>
  );
}
