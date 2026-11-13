/* The product chooser above a comparison table: one checkbox per product.
 * Unchecking one hides that column (the `hidden` attribute on every header and
 * cell carrying its `data-col`, in every table on the page). Without script the
 * chooser stays hidden and every column shows. */

export function statusText(names: string[]): string {
  if (names.length < 2) return `Showing ${names[0] ?? ""}`;
  return `Showing ${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
}

export function initChooser(root: HTMLElement): () => void {
  const doc = root.ownerDocument;
  const boxes = [...root.querySelectorAll<HTMLInputElement>("input[data-chooser-col]")];
  const status = root.querySelector<HTMLElement>('[role="status"]');

  const update = () => {
    for (const box of boxes) {
      for (const el of doc.querySelectorAll<HTMLElement>(`[data-col="${box.dataset.chooserCol}"]`)) {
        el.hidden = !box.checked;
      }
    }
    const shown = boxes.filter((b) => b.checked);
    for (const table of doc.querySelectorAll("table.cmp")) {
      table.classList.toggle("narrow", shown.length <= 2);
    }
    if (status) status.textContent = statusText(shown.map((b) => b.dataset.name ?? ""));
  };

  root.addEventListener("change", update);
  update();
  return () => root.removeEventListener("change", update);
}
