import { useCallback, useId, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { channelLabels } from "@/lib/channels";
import type { MatrixCell, SourceTrack } from "@/lib/types";
import { RotateCcw, Undo2 } from "lucide-react";
import { useT } from "@/lib/i18n";
import { gainDbLabel, gainState } from "@/lib/matrixGain";

/* ===========================================================================
   Advanced mode: the full mix matrix.

   Every source channel of every track gets a column; the destination's L and R
   get a row. Each cell is a gain from 0.0 to 2.0. This subsumes simple mode and
   is what makes "take only the rear channels of the 5.1 track" or "pan the mic
   hard left" expressible at all.

   A cell is a small number input plus a fill bar. Sliders in a 2 x 36 grid
   would be unusable, and a drag-to-set surface would be imprecise for values
   that must often be exactly 0, 0.707 or 1.

   Arrow keys walk the grid the way a spreadsheet does, because tabbing through
   seventy-odd cells to reach one is not editing, it is commuting.
   =========================================================================== */

const MAX_GAIN = 2;

/** Arrow key to [row, column] step. Rows are the two output channels, so
 *  up and down are really "the other one". */
const NAV_STEP: Record<string, [number, number]> = {
  ArrowUp: [-1, 0],
  ArrowDown: [1, 0],
  ArrowLeft: [0, -1],
  ArrowRight: [0, 1],
};

interface MixMatrixProps {
  tracks: SourceTrack[];
  cells: MatrixCell[];
  onChange: (next: MatrixCell[]) => void;
}

export function MixMatrix({ tracks, cells, onChange }: MixMatrixProps) {
  const t = useT();
  /** What the last Clear removed, and the empty list it handed back. Undo is
   *  offered only while `cells` is still that very list: an edit, a preset, a
   *  discard or a switch to another destination all replace it, and then the
   *  offer is gone rather than writing old cells over new ones (A14). */
  const [cleared, setCleared] = useState<{ removed: MatrixCell[]; now: MatrixCell[] } | null>(
    null,
  );
  const undoAvailable = cleared !== null && cells === cleared.now;
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
    setCleared(null);
    onChange(next);
  };

  const clearAll = () => {
    const empty: MatrixCell[] = [];
    if (cells.length > 0) setCleared({ removed: cells, now: empty });
    onChange(empty);
  };

  const undoClear = () => {
    if (!cleared || !undoAvailable) return;
    onChange(cleared.removed);
    setCleared(null);
  };

  const totalCols = tracks.reduce((n, t) => n + t.channels, 0);

  /** Column index of each track's first channel, so a cell knows its position
   *  in the flattened grid rather than only within its own track. */
  const trackColStart = useMemo(() => {
    const starts: number[] = [];
    let n = 0;
    for (const t of tracks) {
      starts.push(n);
      n += t.channels;
    }
    return starts;
  }, [tracks]);

  const gridRef = useRef<HTMLTableElement>(null);

  const focusCell = useCallback(
    (out: number, col: number) => {
      // Edges do not wrap: landing on the far side of the matrix after one
      // arrow press loses your place more than it saves a keystroke.
      if (out < 0 || out > 1 || col < 0 || col >= totalCols) return;
      const el = gridRef.current?.querySelector<HTMLInputElement>(
        `[data-cell="${out}-${col}"]`,
      );
      if (!el) return;
      el.focus();
      el.select();
    },
    [totalCols],
  );

  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-between">
        <div className="text-sm text-muted-foreground">
          {totalCols} source channel{totalCols === 1 ? "" : "s"} → 2 output channels.
          Gain 0–2 per cell; 0 removes the connection.
        </div>
        <div className="flex items-center gap-1">
          {undoAvailable && (
            <Button variant="ghost" size="sm" onClick={undoClear}>
              <Undo2 /> {t("routing.matrix.undoClear")}
            </Button>
          )}
          <Button variant="ghost" size="sm" onClick={clearAll}>
            <RotateCcw /> Clear
          </Button>
        </div>
      </div>

      <div className="overflow-x-auto rounded-md border border-border bg-card">
        <table ref={gridRef} className="w-full border-collapse">
          <thead>
            {/* Track grouping header, so a 5.1 track's six columns read as one
                unit rather than six unrelated channels. */}
            <tr>
              <th className="sticky left-0 z-10 bg-card px-2 py-1 text-left text-sm font-semibold uppercase tracking-wider text-muted-foreground">
                {t("i18nSweep.matrixOut")}
              </th>
              {tracks.map((t, ti) => (
                <th
                  key={t.index}
                  colSpan={t.channels}
                  className={cn(
                    "border-b border-border px-1 py-1 text-center text-sm font-semibold uppercase tracking-wider text-muted-foreground",
                    ti > 0 && "border-l border-border-strong",
                  )}
                >
                  Track {t.index + 1}
                  <span className="ml-1 font-mono normal-case text-muted-foreground">
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
                      "border-b border-border px-1 pb-1 text-center font-mono text-sm font-normal text-muted-foreground",
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
                <td className="sticky left-0 z-10 bg-card px-2 py-1 font-mono text-sm font-semibold">
                  {out === 0 ? "L" : "R"}
                </td>
                {tracks.map((t, ti) =>
                  Array.from({ length: t.channels }, (_, ch) => {
                    const g = gainAt(t.index, ch, out);
                    const col = trackColStart[ti] + ch;
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
                          out={out}
                          col={col}
                          onChange={(v) => setGain(t.index, ch, out, v)}
                          onNavigate={focusCell}
                          // NAMED WHEN THE INGEST NAMES IT. A matrix of "Track 3
                          // channel 1" cells asks an operator to remember which
                          // track the commentary was on while they read a grid
                          // whose whole job is deciding what each output hears.
                          label={
                            `Track ${t.index + 1}${t.title ? ` (${t.title})` : ""}` +
                            ` channel ${ch + 1} to ${out === 0 ? "left" : "right"}`
                          }
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
  out,
  col,
  onChange,
  onNavigate,
  label,
}: {
  value: number;
  out: number;
  col: number;
  onChange: (v: number) => void;
  onNavigate: (out: number, col: number) => void;
  label: string;
}) {
  const t = useT();
  const stateId = useId();
  const active = value > 0;
  const state = gainState(value);
  // The fill bar makes the matrix scannable: you read the routing shape from
  // the pattern of lit cells before reading a single number.
  const fill = Math.min(1, value / MAX_GAIN);
  // A15: the state is a word and a dB reading, not only a tint, so the grid can
  // be read without colour. Off reads as a word; everything else reads in dB.
  const stateText = state === "off" ? t("routing.matrix.stateOff") : gainDbLabel(value);

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    const step = NAV_STEP[e.key];
    if (!step) return;
    // Up and down would otherwise nudge the number and left and right would
    // move the caret, neither of which helps in a grid this wide. Arriving at
    // a cell selects its value, so typing still replaces rather than appends.
    e.preventDefault();
    onNavigate(out + step[0], col + step[1]);
  };

  return (
    <div
      data-state={state}
      className={cn(
        "relative flex h-12 w-[4.5rem] flex-col overflow-hidden rounded-[3px] border transition-colors",
        "focus-within:ring-2 focus-within:ring-ring",
        state === "unity" && "border-primary/60 bg-primary/10",
        state === "custom" && "border-primary/40",
        state === "off" && "border-border bg-muted/40",
      )}
    >
      {active && (
        <div
          className="absolute inset-y-0 left-0 bg-primary/25"
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
        aria-describedby={stateId}
        // THE SAME SENTENCE, ON HOVER. It was already written for a screen
        // reader and reached nobody using a mouse -- the same asymmetry the
        // icon buttons had, in a grid where every cell is an unlabelled box of
        // digits and the row and column headers scroll out of view.
        title={label}
        data-cell={`${out}-${col}`}
        onKeyDown={onKeyDown}
        onChange={(e) => {
          const v = Number.parseFloat(e.target.value);
          onChange(Number.isFinite(v) ? v : 0);
        }}
        className={cn(
          "tnum relative h-6 w-full bg-transparent text-center font-mono text-sm outline-none",
          "[appearance:textfield] [&::-webkit-inner-spin-button]:appearance-none [&::-webkit-outer-spin-button]:appearance-none",
          active ? "text-foreground" : "text-muted-foreground",
        )}
      />
      <span
        id={stateId}
        className={cn(
          "tnum relative block truncate text-center font-mono text-xs leading-none",
          active ? "text-foreground/80" : "text-muted-foreground",
        )}
      >
        {stateText}
      </span>
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
      <span className="text-sm uppercase tracking-wider text-muted-foreground">Fill</span>
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
