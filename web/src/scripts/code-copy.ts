/* The Copy button's behaviour, in a module rather than in CodeBlock.astro.
 *
 * It moved here when the docs started rendering their code fences through the
 * same markup: 114 of those blocks are built by a rehype plugin, not by the
 * component, so a `<script>` living inside the component would have shipped on
 * a page that has one hand-written block and been absent from the page with
 * forty. Both importers pull this module and Astro emits it once.
 *
 * Delegated from the document rather than bound per button, which the
 * per-component version could afford and this cannot: /docs/api has 40-odd of
 * these, and 40 listeners to do one thing is 39 too many. */
export {};

document.addEventListener("click", async (event) => {
  const target = event.target;
  if (!(target instanceof Element)) return;
  const btn = target.closest<HTMLButtonElement>("[data-code-copy]");
  if (!btn) return;

  const block = btn.closest(".code");
  const code = block?.querySelector("code");
  const text = code?.textContent ?? "";
  if (!text) return;

  try {
    await navigator.clipboard.writeText(text);
  } catch {
    // Clipboard is refused on insecure origins and by some privacy settings.
    // Selecting the text is the honest fallback: the reader still gets the
    // command in one gesture, and nothing claims a copy that did not happen.
    if (code) {
      const range = document.createRange();
      range.selectNodeContents(code);
      const sel = window.getSelection();
      sel?.removeAllRanges();
      sel?.addRange(range);
    }
    return;
  }

  const was = btn.textContent;
  btn.textContent = "Copied";
  btn.dataset.copied = "true";
  window.setTimeout(() => {
    btn.textContent = was;
    delete btn.dataset.copied;
  }, 1600);
});
