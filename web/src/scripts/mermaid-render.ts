/* Renders the diagrams that lib/hast-mermaid.mjs left as source.
 *
 * LOADED DYNAMICALLY, AND THAT IS THE POINT OF THE GUARD BELOW. Mermaid is a
 * parser, a layout engine and a renderer; it is by a wide margin the largest
 * thing this site could ship. A static import would put it in the bundle of
 * every documentation page, and 22 of the 23 have no diagram on them. The
 * `querySelectorAll` runs first and returns early, so a page without a diagram
 * never fetches the chunk at all.
 *
 * THEMED FROM THE SITE'S OWN TOKENS RATHER THAN A MERMAID PRESET. This site is
 * dark-only -- `color-scheme: dark` in global.css, ink #0b0d11 -- and every
 * mermaid preset is built for a white page. Left alone, `mermaid.initialize()`
 * would drop a lavender-on-white flowchart into the middle of a dark document,
 * which is the same "fourth treatment" problem hast-code-block.mjs was written
 * to avoid, arrived at from a different direction. The variables are read from
 * the live stylesheet so a token change moves the diagrams with it instead of
 * leaving a second copy of the palette here to drift.
 */

const NODES = "[data-mermaid='pending']";

/** Reads a custom property off :root, with a fallback for the case where this
 *  runs before the stylesheet has applied. */
function token(name: string, fallback: string): string {
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return v || fallback;
}

async function render(): Promise<void> {
  const targets = document.querySelectorAll<HTMLElement>(NODES);
  if (targets.length === 0) return;

  const { default: mermaid } = await import("mermaid");

  const ink = token("--color-ink", "#0b0d11");
  const surface = token("--color-raised", "#171d27");
  const line = token("--color-line-strong", "#2a3444");
  const fg = token("--color-fg", "#edf2f8");
  const muted = token("--color-muted", "#9ba9ba");
  const primary = token("--color-primary", "#5b7fc7");

  mermaid.initialize({
    startOnLoad: false,
    // "base" is the only theme that honours every variable below; the named
    // presets hard-code most of them.
    theme: "base",
    // A diagram is a figure, not an animation. global.css carries a
    // prefers-reduced-motion block and check-build.mjs asserts it exists;
    // mermaid's own transitions are off for the same reason the rest of the
    // site's are restrained.
    themeVariables: {
      darkMode: true,
      background: ink,
      primaryColor: surface,
      primaryTextColor: fg,
      primaryBorderColor: line,
      secondaryColor: surface,
      tertiaryColor: ink,
      lineColor: muted,
      textColor: fg,
      mainBkg: surface,
      nodeBorder: line,
      clusterBkg: ink,
      clusterBorder: line,
      titleColor: fg,
      edgeLabelBackground: ink,
      fontFamily: token("--font-sans", "system-ui, sans-serif"),
      fontSize: "14px",
      nodeTextColor: fg,
      // Decision diamonds, so a branch reads as a branch.
      labelBackground: ink,
      activeTaskBorderColor: primary,
    },
    flowchart: { htmlLabels: true, curve: "basis", useMaxWidth: true },
    // The source in these documents is written by hand and reviewed; it is not
    // user input. Left at the default rather than raised, because nothing here
    // needs it and a diagram is not worth widening what markup can reach.
    securityLevel: "strict",
  });

  for (const [i, el] of Array.from(targets).entries()) {
    const source = el.textContent ?? "";
    try {
      const { svg } = await mermaid.render(`mermaid-${i}`, source);
      /* innerHTML, and the two things that make it the right call rather than a
         shortcut. The string is mermaid's OWN SVG output, not content from
         anywhere; and `securityLevel: "strict"` above runs mermaid's DOMPurify
         pass over every label before it gets here. The input is a fenced block
         in a markdown file in this repository, which is reviewed like the rest
         of it -- there is no path from a reader to this string. Anything laxer
         on securityLevel would change that, which is why it is pinned. */
      el.innerHTML = svg;
      el.dataset.mermaid = "done";
    } catch (err) {
      /* LEAVE THE SOURCE ON THE PAGE. A diagram that fails to a blank space is
         indistinguishable from a page that forgot to include it, and mermaid
         throws on a syntax error in a way that is otherwise invisible outside
         devtools -- the build does not parse these. The source of a flowchart
         is still readable prose, so the failure degrades to something a reader
         can use rather than to nothing. */
      el.dataset.mermaid = "failed";
      console.error("mermaid: could not render a diagram on this page", err);
    }
  }
}

void render();

/* A MODULE, NOT A SCRIPT, AND `astro check` IS WHAT INSISTS.
 *
 * Everything above is side effects plus one dynamic `import()`, and a dynamic
 * import does not make a file a module -- TypeScript needs a top-level `import`
 * or `export` for that. Without this the build fails with "is not a module" the
 * moment anything imports the file, which the unit tests beside it do.
 *
 * Empty on purpose: nothing here is meant to be called from elsewhere. The
 * entry point is the import itself, from DocPage.astro. */
export {};
