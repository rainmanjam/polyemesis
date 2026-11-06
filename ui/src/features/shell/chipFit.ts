/* Measuring for the phone on-air chip (components/MobileOnAirChip.tsx).
 * Plain functions over laid-out elements, so they can be tested with stubbed
 * geometry where jsdom has none. */

/** An item's natural width: what it occupies now plus whatever it is hiding
 *  behind an ellipsis, capped at its own max-width. A programme switcher
 *  squeezed to "Main Progra…" still wants its whole label. */
export function naturalWidth(el: HTMLElement): number {
  let hidden = Math.max(0, el.scrollWidth - el.clientWidth);
  for (const d of Array.from(el.querySelectorAll<HTMLElement>("*"))) {
    hidden = Math.max(hidden, d.scrollWidth - d.clientWidth);
  }
  const max = Number.parseFloat(getComputedStyle(el).maxWidth);
  return Math.min(el.offsetWidth + hidden, Number.isFinite(max) ? max : Infinity);
}

/** Whether the chip's full words fit beside everything else in its row.
 *
 *  The room is the row's content width minus every other visible item at its
 *  natural width and the gaps between them; the full words are read off an
 *  invisible yardstick, so the answer does not depend on which form is on
 *  screen. */
export function chipFits(chip: HTMLElement, measure: HTMLElement): boolean {
  const row = chip.parentElement;
  if (!row) return true;
  const cs = getComputedStyle(row);
  const gap = Number.parseFloat(cs.columnGap) || 0;
  const others = Array.from(row.children).filter(
    (c): c is HTMLElement => c !== chip && c instanceof HTMLElement && c.offsetWidth > 0,
  );
  const taken = others.reduce((sum, c) => sum + naturalWidth(c), 0) + gap * others.length;
  const padding = (Number.parseFloat(cs.paddingLeft) || 0) + (Number.parseFloat(cs.paddingRight) || 0);
  const room = row.clientWidth - padding - taken;
  const dot = chip.firstElementChild instanceof HTMLElement ? chip.firstElementChild.offsetWidth : 0;
  const chipGap = Number.parseFloat(getComputedStyle(chip).columnGap) || 0;
  return dot + chipGap + measure.offsetWidth <= room;
}
