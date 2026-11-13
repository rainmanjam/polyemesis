/* One entry of an in-page index: the id of a section and the words shown for
 * it. `depth` 3 nests under the h2 above it. The pure helper that
 * picks the current section, `stageFor`, lives in scripts/home/journey.ts. */
export interface IndexItem {
  id: string;
  label: string;
  depth?: 2 | 3;
}
