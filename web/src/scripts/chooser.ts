/* The product chooser above a comparison table: one checkbox per product.
 * Unchecking one hides that column (the `hidden` attribute on every header and
 * cell carrying its `data-col`) and marks a table `narrow` when two or fewer of
 * its own columns show. A table with other column keys is left alone. The polyemesis
 * column is never hidden. Without script the chooser stays hidden and every
 * column shows. */
import { statusText } from "../lib/compare-table";

export function initChooser(root: HTMLElement): () => void {
  const doc = root.ownerDocument;
  const boxes = [...root.querySelectorAll<HTMLInputElement>("input[data-chooser-col]")];
  const keys = new Set(boxes.map((b) => b.dataset.chooserCol));
  const status = root.querySelector<HTMLElement>('[role="status"]');

  const update = () => {
    const off = new Set(boxes.filter((b) => !b.checked && b.dataset.poly === undefined).map((b) => b.dataset.chooserCol));
    for (const el of doc.querySelectorAll<HTMLElement>("[data-col]")) {
      el.hidden = off.has(el.dataset.col);
    }
    const shown = boxes.filter((b) => b.checked);
    // Narrow is decided per table, from its own visible product columns; a table
    // whose columns are not the chooser's (another page section's keys) is left alone.
    for (const table of doc.querySelectorAll("table.cmp")) {
      const cols = [...table.querySelectorAll<HTMLElement>("thead th[data-col]")];
      if (!cols.some((th) => keys.has(th.dataset.col))) continue;
      table.classList.toggle("narrow", cols.filter((th) => !th.hidden).length <= 2);
    }
    // Rewritten only when it changes, so loading the page announces nothing.
    const text = statusText(shown.map((b) => b.dataset.name ?? ""));
    if (status && status.textContent !== text) status.textContent = text;
  };

  root.addEventListener("change", update);
  update();
  return () => root.removeEventListener("change", update);
}
