/* One entry of an in-page index: the id of a section and the words shown for
 * it. `depth` 3 nests under the h2 above it. Task 5 adds the pure helpers
 * (`stageFor` and friends) to this file. */
export interface IndexItem {
  id: string;
  label: string;
  depth?: 2 | 3;
}
