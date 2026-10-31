import { useId, type KeyboardEvent } from "react";
import { disabledDescribedBy } from "@/lib/disabledReason";
import { ShieldAlert, ShieldCheck, Minus } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { StatusDot } from "@/components/signature/StatusDot";
import { useT } from "@/lib/i18n";
import {
  cellState,
  musicReachingVod,
  musicRights,
  type CellState,
  type MusicRights,
  type OverviewDest,
} from "@/lib/routeOverview";
import type { SignalTone } from "@/lib/signal";
import { cn } from "@/lib/utils";

/* ===========================================================================
   A4: the destinations x tracks overview.

   Tracks run across, destinations run down. A filled dot is a routed track, a
   hollow one is not routed, and a music track that reaches a VOD mix on a
   destination that does not keep music out is amber and says VOD in words.

   Only the SELECTED row is editable here, because that row's routing is the
   draft the Simple and Mix views edit. Clicking a cell in another row selects
   that destination first; a matrix-mode row is read-only, since its routing is
   a gain grid rather than an on or off per track.
   =========================================================================== */

export interface OverviewRow extends Omit<OverviewDest, "enabled" | "state"> {
  tone: SignalTone;
  /** editable: this is the selected destination. select: clicking opens it.
   *  matrix: its routing is a gain grid, so the cells are read-only. */
  kind: "editable" | "select" | "matrix";
}

export interface OverviewTrack {
  index: number;
  title?: string;
}

interface RouteOverviewProps {
  rows: OverviewRow[];
  tracks: OverviewTrack[];
  /** Track indices whose role is music. */
  music: number[];
  onToggle: (track: number) => void;
  onSelect: (id: number) => void;
  onFix: (id: number) => void;
}

export function RouteOverview({ rows, tracks, music, onToggle, onSelect, onFix }: RouteOverviewProps) {
  const t = useT();
  const dests: OverviewDest[] = rows.map((r) => ({ ...r, enabled: true }));
  const hit = musicReachingVod(dests, music)[0];
  const hitRow = hit ? rows.find((r) => r.id === hit.destId) : undefined;
  const hitTrack = hit ? tracks.find((tr) => tr.index === hit.track) : undefined;

  const onKeyDown = (e: KeyboardEvent<HTMLTableElement>) => {
    const el = e.target as HTMLElement;
    const r = Number(el.dataset.row);
    const c = Number(el.dataset.col);
    if (Number.isNaN(r) || Number.isNaN(c)) return;
    const next: Record<string, [number, number]> = {
      ArrowUp: [r - 1, c],
      ArrowDown: [r + 1, c],
      ArrowLeft: [r, c - 1],
      ArrowRight: [r, c + 1],
    };
    const step = next[e.key];
    if (!step) return;
    e.preventDefault();
    const target = e.currentTarget.querySelector<HTMLElement>(
      `[data-cell="${step[0]}-${step[1]}"]`,
    );
    target?.focus();
  };

  return (
    <div className="flex flex-col gap-3">
      <div className="overflow-x-auto rounded-md border border-border">
        <table className="w-full border-collapse text-sm" onKeyDown={onKeyDown}>
          <thead>
            <tr className="text-left text-xs text-muted-foreground">
              <th scope="col" className="px-2 py-1.5 font-semibold">
                {t("routing.overview.destination")}
              </th>
              <th scope="col" className="px-2 py-1.5 font-semibold">
                {t("routing.overview.musicRights")}
              </th>
              {tracks.map((tr) => (
                <th key={tr.index} scope="col" className="px-1 py-1.5 text-center font-semibold">
                  <span className="block max-w-[7rem] truncate" title={tr.title}>
                    {tr.title ?? t("routing.overview.trackLabel", { n: tr.index + 1 })}
                  </span>
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((row, ri) => {
              const d: OverviewDest = { ...row, enabled: true };
              return (
                <tr
                  key={row.id}
                  className={cn("border-t border-border", row.kind === "editable" && "bg-primary-dim/30")}
                >
                  <th scope="row" className="px-2 py-1.5 text-left font-medium">
                    <span className="flex items-center gap-2">
                      <StatusDot tone={row.tone} size="sm" />
                      <span className="truncate">{row.name}</span>
                    </span>
                  </th>
                  <td className="px-2 py-1.5">
                    <RightsChip state={musicRights(d, music)} />
                  </td>
                  {tracks.map((tr, ci) => (
                    <td key={tr.index} className="px-1 py-1.5 text-center">
                      <OverviewCell
                        row={row}
                        dest={d}
                        trackIndex={tr.index}
                        trackName={tr.title ?? t("routing.overview.trackLabel", { n: tr.index + 1 })}
                        music={music}
                        data-row={ri}
                        data-col={ci}
                        onToggle={onToggle}
                        onSelect={onSelect}
                      />
                    </td>
                  ))}
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>

      {hit && hitRow && hitTrack && (
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-warn/40 bg-warn-dim/25 px-3 py-2">
          <p className="flex items-start gap-2 text-sm">
            <ShieldAlert className="mt-0.5 h-4 w-4 shrink-0 text-warn" aria-hidden />
            <span>
              <strong className="font-semibold">{t("routing.overview.fixLead")}</strong>{" "}
              {t("routing.overview.fixSummary", {
                destination: hitRow.name,
                track: hitTrack.title ?? t("routing.overview.trackLabel", { n: hitTrack.index + 1 }),
              })}
            </span>
          </p>
          {hitRow.kind === "editable" ? (
            <Button variant="outline" size="sm" onClick={() => onFix(hitRow.id)}>
              {t("routing.overview.fix", { destination: hitRow.name })}
            </Button>
          ) : (
            <Button variant="outline" size="sm" onClick={() => onSelect(hitRow.id)}>
              {t("routing.overview.selectToFix", { destination: hitRow.name })}
            </Button>
          )}
        </div>
      )}
    </div>
  );
}

function RightsChip({ state }: { state: MusicRights }) {
  const t = useT();
  if (state === "reachesVod") {
    return (
      <span className="inline-flex items-center gap-1 text-xs font-medium text-warn">
        <ShieldAlert className="h-3.5 w-3.5" aria-hidden />
        {t("routing.overview.rights.reachesVod")}
      </span>
    );
  }
  if (state === "blocked") {
    return (
      <span className="inline-flex items-center gap-1 text-xs text-primary">
        <ShieldCheck className="h-3.5 w-3.5" aria-hidden />
        {t("routing.overview.rights.blocked")}
      </span>
    );
  }
  return (
    <span className="inline-flex items-center gap-1 text-xs text-muted-foreground">
      <Minus className="h-3.5 w-3.5" aria-hidden />
      {t("routing.overview.rights.none")}
    </span>
  );
}

function OverviewCell({
  row,
  dest,
  trackIndex,
  trackName,
  music,
  onToggle,
  onSelect,
  ...position
}: {
  row: OverviewRow;
  dest: OverviewDest;
  trackIndex: number;
  trackName: string;
  music: number[];
  onToggle: (track: number) => void;
  onSelect: (id: number) => void;
} & { "data-row": number; "data-col": number }) {
  const t = useT();
  // A28: a matrix cell is read-only, and the reason is announced through the
  // cell, not only in a title that a disabled button never shows.
  const matrixReasonId = useId();
  const state: CellState = cellState(dest, trackIndex, music);
  // The live mix and the VOD mix are separate, so an amber cell still says what
  // the live mix does on this track: clicking it toggles that, not the archive.
  const liveRouted = dest.routed.includes(trackIndex);
  const liveText = liveRouted ? t("routing.overview.cellRouted") : t("routing.overview.cellOff");
  const stateText =
    state === "musicVod"
      ? `${liveText}, ${t("routing.overview.cellMusicVod")}`
      : state === "routed"
        ? t("routing.overview.cellRouted")
        : t("routing.overview.cellOff");
  const onClick =
    row.kind === "editable"
      ? () => onToggle(trackIndex)
      : row.kind === "select"
        ? () => onSelect(row.id)
        : undefined;
  return (
    <button
      type="button"
      data-cell={`${position["data-row"]}-${position["data-col"]}`}
      data-row={position["data-row"]}
      data-col={position["data-col"]}
      data-state={state}
      aria-label={t("routing.overview.cellLabel", {
        track: trackName,
        destination: row.name,
        state: stateText,
      })}
      aria-pressed={state !== "off"}
      disabled={row.kind === "matrix"}
      aria-describedby={disabledDescribedBy(
        matrixReasonId,
        row.kind === "matrix" ? t("routing.overview.matrixOnly") : null,
      )}
      onClick={onClick}
      className={cn(
        "relative inline-flex h-9 w-14 flex-col items-center justify-center rounded-md border transition-colors",
        "focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-70",
        state === "musicVod" && "border-warn bg-warn-dim/40",
        state === "routed" && "border-primary/50 bg-primary/10",
        state === "off" && "border-border bg-transparent",
      )}
    >
      {row.kind === "matrix" && (
        <span id={matrixReasonId} className="sr-only">
          {t("routing.overview.matrixOnly")}
        </span>
      )}
      <span className="flex items-center gap-1">
        <span
          aria-hidden
          className={cn(
            "h-2.5 w-2.5 rounded-full border",
            liveRouted ? "border-primary bg-primary" : "border-muted-foreground bg-transparent",
          )}
        />
        {state === "musicVod" && (
          <Badge variant="warn" className="px-1 py-0 text-xs leading-none">
            {t("routing.overview.vodTag")}
          </Badge>
        )}
      </span>
    </button>
  );
}
