/* Calls `load` once, the first time `el` is within `rootMargin` of the viewport.
 * Returns a disposer. A rejected load is swallowed: one section failing to load
 * must not surface as an unhandled rejection or stop the others. */
export function whenNear(el: Element, load: () => Promise<void>, rootMargin = "100% 0px"): () => void {
  let done = false;
  const io = new IntersectionObserver(
    (entries) => {
      if (done || !entries.some((e) => e.isIntersecting)) return;
      done = true;
      io.disconnect();
      load().catch(() => {});
    },
    { rootMargin },
  );
  io.observe(el);
  return () => {
    done = true;
    io.disconnect();
  };
}
