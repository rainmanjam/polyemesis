import type { PreviewTile } from "@/hooks/usePreviewTiles";

/** The props that decide what one PreviewPlayer shows. */
export interface PreviewPanePlayer {
  /** Which programme to load. Undefined means the default source's alias, which
   *  is what a caller with no id in hand has always got. */
  sourceId?: number;
  outputLive?: boolean;
  ingestLive?: boolean;
  onAir?: string;
  /** Only present once BOTH numbers are real. A half-measured source has to
   *  fall back to 16:9 rather than collapse the tile to a zero-height box. */
  aspect?: { width: number; height: number };
}

/** One tile: its identity, its caption, and what to play in it. */
export interface PreviewPane {
  /** The source's id, and the list key. Undefined only in the one case where
   *  there is no telemetry yet and so nothing to tell apart. */
  id?: number;
  /** The programme's name, shown under the tile when there is more than one. */
  label?: string;
  player: PreviewPanePlayer;
}

export interface PreviewLayout {
  /** More than one programme exists, so the dashboard draws a captioned grid
   *  rather than the single full-width player it drew before this feature. */
  grid: boolean;
  /** Never empty. With no telemetry it holds one pane with nothing set, which
   *  is exactly the unqualified player the page rendered before per-source
   *  previews existed -- an install that has not answered /previews yet must
   *  not lose its preview. */
  panes: PreviewPane[];
}

const aspectOf = (t: {
  width?: number;
  height?: number;
}): { width: number; height: number } | undefined =>
  t.width && t.height ? { width: t.width, height: t.height } : undefined;

/**
 * What the dashboard's preview area shows for a given set of source telemetry.
 *
 * WHY THIS IS NOT WRITTEN INLINE IN Dashboard.tsx. It is the whole decision the
 * per-source preview makes -- grid or single player, which programme each tile
 * plays, whether a measured geometry is usable, what a tile is called -- and
 * every branch of it lived in JSX on a page with no tests, which is a page test
 * or nothing. It is a plain function of a plain list, so it is neither.
 *
 * The single-pane fallback is the load-bearing part. `tiles` is empty on the
 * first paint of every load and stays empty on any install whose /previews poll
 * fails, and BOTH must keep rendering the ordinary preview rather than a blank
 * grid.
 */
export function previewLayout(tiles: readonly PreviewTile[]): PreviewLayout {
  if (tiles.length > 1) {
    return {
      grid: true,
      panes: tiles.map((t) => ({
        id: t.id,
        label: t.name,
        player: {
          sourceId: t.id,
          outputLive: t.outputLive,
          ingestLive: t.ingestLive,
          onAir: t.onAir,
          aspect: aspectOf(t),
        },
      })),
    };
  }

  const only = tiles[0];
  return {
    grid: false,
    panes: [
      {
        id: only?.id,
        label: only?.name,
        player: {
          sourceId: only?.id,
          outputLive: only?.outputLive,
          ingestLive: only?.ingestLive,
          onAir: only?.onAir,
          aspect: only ? aspectOf(only) : undefined,
        },
      },
    ],
  };
}
