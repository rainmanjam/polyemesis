import type { DestStatus, SourceView } from "@/lib/types";
import type { PreviewTile } from "@/hooks/usePreviewTiles";
import type { PreviewPane } from "@/lib/previewLayout";
import { previewLayout } from "@/lib/previewLayout";
import { destinationLayout, type DestinationGroup } from "@/lib/destinationGroups";

/** One programme, with everything on the dashboard that belongs to it. */
export interface SourceLane {
  sourceId: number;
  name: string;
  /** What to show in this lane's preview. Absent when the programme has
   *  reported no telemetry yet — the lane still exists. */
  pane?: PreviewPane;
  destinations: DestStatus[];
}

export interface LaneLayout {
  /** Whether to draw lanes at all. False with fewer than two programmes, where
   *  a lane is a box drawn around the whole page. */
  laned: boolean;
  lanes: SourceLane[];
  /** Destinations naming a programme the server did not list. Never dropped;
   *  drawn after the lanes with a sentence rather than a heading. */
  orphans: DestStatus[];
}

/**
 * The dashboard, divided by programme.
 *
 * WHY LANES RATHER THAN HEADINGS. Headings (the previous step) put a programme's
 * name above its destinations, which tells an operator which is which but
 * leaves the preview for that programme somewhere else on the page entirely --
 * so answering "is THIS one on air, and where is it going?" means reading a
 * name in two places and trusting they match. A lane answers it by position:
 * the picture and the destinations that carry it are the same box. Poka-yoke
 * calls this a contact method -- the shape prevents the mistake rather than
 * labelling it.
 *
 * COMPOSED, NOT REIMPLEMENTED. previewLayout and destinationLayout already own
 * the awkward rules -- what a half-measured geometry does, what happens to a
 * destination whose programme is missing, what a single-programme install looks
 * like. Restating any of them here would be a second copy to keep in step, and
 * the first thing to drift would be the orphan handling, which is the one that
 * loses a card.
 *
 * ONE PROGRAMME MEANS NO LANES, for the same reason it means no headings: a
 * lane around the only programme is a box drawn around the whole page.
 */
export function laneLayout(
  tiles: readonly PreviewTile[],
  destinations: readonly DestStatus[],
  sources: readonly SourceView[],
): LaneLayout {
  const dests = destinationLayout(destinations, sources);
  const preview = previewLayout(tiles);

  if (!dests.grouped) {
    return { laned: false, lanes: [], orphans: [] };
  }

  const paneOf = new Map<number, PreviewPane>();
  for (const p of preview.panes) if (p.id != null) paneOf.set(p.id, p);

  const named = dests.groups.filter((g): g is DestinationGroup & { sourceId: number } =>
    !g.orphans && g.sourceId != null,
  );

  return {
    laned: true,
    lanes: named.map((g) => ({
      sourceId: g.sourceId,
      name: g.name,
      pane: paneOf.get(g.sourceId),
      destinations: g.destinations,
    })),
    orphans: dests.groups.find((g) => g.orphans)?.destinations ?? [],
  };
}
