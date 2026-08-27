/** How the dashboard's top row is arranged, decided once.
 *
 *  TWO FACTS THAT MUST AGREE, RETURNED TOGETHER. The row's column count and
 *  whether the side cards are stacked are the same decision seen from two
 *  places: three columns with the cards still stacked leaves an empty third
 *  column, and two columns with them unstacked pushes the pipeline out of the
 *  grid entirely. Written as two independent expressions in the JSX they would
 *  eventually disagree -- one is a Tailwind string and the other a boolean
 *  prop, they sit two hundred lines apart, and the branch you see depends on
 *  how many programmes the install has, so an editor working on a
 *  single-programme box would never render the other arrangement.
 *
 *  Returning both from one call is the device: there is no way to change the
 *  columns without passing this file, and the test below fails if they stop
 *  describing the same layout.
 */
export interface TopRowLayout {
  /** Grid template for the row holding preview/ingest and the side cards. */
  gridClass: string;
  /** Whether the side cards share one cell (true) or take a cell each. */
  sideStacked: boolean;
  /** How many columns `gridClass` declares at the `lg` breakpoint. */
  columns: number;
}

/**
 * @param laned Whether the dashboard is drawing per-programme lanes, which is
 *   true on an install with two or more programmes. Lanes carry their own
 *   preview, so the top row has none -- see #614 for what that left behind.
 */
export function topRowLayout(laned: boolean): TopRowLayout {
  // WITHOUT A PREVIEW THE LEFT COLUMN IS SHORT. Stacking chat above the
  // pipeline beside it made the row as tall as the stack and left ~400px of
  // empty page under the Ingest card. Side by side, the row is about half as
  // tall, the dead space is gone, and the destination area moves UP.
  if (laned) {
    return {
      gridClass: "lg:grid-cols-[minmax(0,1fr)_20rem_20rem]",
      sideStacked: false,
      columns: 3,
    };
  }
  // With a preview, the left column is the tallest thing on the page and the
  // stack beside it costs nothing.
  return {
    gridClass: "lg:grid-cols-[minmax(0,1fr)_20rem]",
    sideStacked: true,
    columns: 2,
  };
}
