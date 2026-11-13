/* One place that scrolls a window or an element, smoothly or not.
 *
 * "instant" is the right behaviour when motion is off ("auto" defers to the
 * stylesheet, a second answer to the question the gate already answers), but an
 * engine that does not know the value throws a TypeError from scrollTo. Falling
 * back to the two-number form scrolls there too, and is instant by nature. */
export interface Scrollable {
  scrollTo(options: ScrollToOptions): void;
  scrollTo(x: number, y: number): void;
}

type Position = { left?: number; top?: number };

/* The axis a caller did not name stays where it is. */
const here = (t: Scrollable, axis: "x" | "y"): number => {
  const o = t as unknown as { scrollX?: number; scrollY?: number; scrollLeft?: number; scrollTop?: number };
  return (axis === "x" ? (o.scrollX ?? o.scrollLeft) : (o.scrollY ?? o.scrollTop)) ?? 0;
};

export function scrollToPosition(target: Scrollable, pos: Position, smooth: boolean): void {
  try {
    target.scrollTo({ ...pos, behavior: smooth ? "smooth" : "instant" });
  } catch {
    target.scrollTo(pos.left ?? here(target, "x"), pos.top ?? here(target, "y"));
  }
}
