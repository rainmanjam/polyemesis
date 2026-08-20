import type { DestStatus, SourceView } from "@/lib/types";
import { sourceChoices } from "@/lib/destinationSource";

/** One programme's destinations, in the order the dashboard should draw them. */
export interface DestinationGroup {
  /** The programme's id. Undefined for the one group that has no programme —
   *  see `orphans` below. */
  sourceId?: number;
  /** What to put in the heading. */
  name: string;
  destinations: DestStatus[];
  /** True for the group holding destinations whose programme is not in the
   *  source list. Callers render this differently: it is a problem, not a
   *  heading. */
  orphans?: boolean;
}

export interface DestinationLayout {
  /** Whether to draw headings at all. False on a single-programme install,
   *  where every heading would say the same word. */
  grouped: boolean;
  groups: DestinationGroup[];
}

/**
 * How the dashboard's destination area is divided.
 *
 * ONE PROGRAMME MEANS NO HEADINGS. A heading repeated above every card is a
 * label that distinguishes nothing, and it costs a line of vertical space on
 * the screen an operator watches during a broadcast. `grouped: false` with a
 * single group is the honest shape for that: the caller draws exactly what it
 * drew before.
 *
 * A DESTINATION IS NEVER DROPPED. If a destination names a programme that is
 * not in the source list — a source deleted in another tab, a status snapshot
 * that arrived before the source list did, a row whose source_id survived a
 * restore its programme did not — it goes in a final group flagged `orphans`
 * rather than being filtered out. Silently omitting a card is the worst
 * available outcome: the destination is still configured, may still be
 * running, and the operator would have no way to find it. This is the same
 * rule the audit applied seventeen times over: an absence must not be able to
 * read as an answer.
 *
 * ORDER FOLLOWS THE SOURCE LIST, not the alphabet, so the dashboard reads in
 * the same order as the Sources page. Within a group the caller's order is
 * preserved untouched — the dashboard lets an operator move destinations by
 * hand, and re-sorting them here would silently undo that.
 */
export function destinationLayout(
  destinations: readonly DestStatus[],
  sources: readonly SourceView[],
): DestinationLayout {
  const choices = sourceChoices(sources);

  if (choices.length < 2) {
    return { grouped: false, groups: [{ name: "", destinations: [...destinations] }] };
  }

  const groups: DestinationGroup[] = choices.map((c) => ({
    sourceId: c.id,
    name: c.name,
    destinations: [],
  }));
  const byId = new Map(groups.map((g) => [g.sourceId as number, g]));
  const orphans: DestStatus[] = [];

  for (const d of destinations) {
    const g = d.sourceId == null ? undefined : byId.get(d.sourceId);
    if (g) g.destinations.push(d);
    else orphans.push(d);
  }

  // Empty programmes keep their heading. A source with no destinations is a
  // fact worth showing -- it is the state an operator has to notice to fix,
  // and hiding the heading would make "no destinations yet" look identical to
  // "no such programme".
  if (orphans.length > 0) {
    groups.push({ name: "", destinations: orphans, orphans: true });
  }
  return { grouped: true, groups };
}

/** Whether the layout would draw a heading for this group. */
export function hasHeading(layout: DestinationLayout, group: DestinationGroup): boolean {
  return layout.grouped && !group.orphans;
}
