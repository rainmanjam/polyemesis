// @ts-check
/* Turns a ```mermaid fence into a container the client renderer can find.
 *
 * WHY THIS IS NOT `<pre class="mermaid">`, which is what mermaid's own docs
 * tell you to write. Two guards in scripts/check-build.mjs forbid it, and both
 * are right:
 *
 *   1. `/<pre\s+class="[^"]+"/` fails the build on ANY hand-styled <pre>. That
 *      ban is what the whole docs change is organised around -- three different
 *      code-block treatments once shipped on one site -- and a diagram is not a
 *      reason to punch a hole in it.
 *
 *   2. Every <pre> on the site must pair with a CodeBlock copy button, counted
 *      rather than parsed. A <pre> holding diagram SOURCE would need a Copy
 *      button to satisfy that count, offering to copy mermaid source to somebody
 *      looking at a picture.
 *
 * So the <pre> is REPLACED rather than decorated. A `<div class="mermaid">` is
 * not a code block, is not counted as one, and mermaid is perfectly happy to be
 * pointed at it -- `querySelector` is a documented init option precisely so the
 * container is the caller's choice.
 *
 * THE SOURCE IS LEFT AS A TEXT NODE, not escaped or pre-rendered. Mermaid parses
 * textContent, and hast serialises a text node with the entity escaping HTML
 * needs, so `-->` in an edge label survives the round trip instead of closing a
 * comment. Rendering to SVG at BUILD time was the other option and was rejected:
 * mermaid needs a DOM, so it would mean puppeteer in the build, and the site
 * would gain a browser download to draw six boxes.
 *
 * IF THE RENDERER NEVER RUNS -- script blocked, JS off, an old browser -- the
 * source stays visible as text rather than vanishing. That is deliberate: a
 * diagram that fails to a blank space is indistinguishable from a page that
 * forgot to include it, and the source of a flowchart is still readable prose.
 */

/** The class the client script looks for. Kept here rather than inlined twice,
 *  because the renderer and this plugin have to agree and they live in
 *  different files. */
export const MERMAID_CLASS = "mermaid";

/** @param {unknown} v */
function classList(v) {
  if (Array.isArray(v)) return v.map(String);
  if (typeof v === "string") return v.split(/\s+/);
  return [];
}

/** Concatenates the text under a node. A fence is normally one text node, but
 *  nothing guarantees that, and a diagram silently truncated at the first
 *  newline would be a confusing way to find out.
 *  @param {any} node @returns {string} */
function textOf(node) {
  if (!node) return "";
  if (node.type === "text") return String(node.value ?? "");
  const kids = Array.isArray(node.children) ? node.children : [];
  return kids.map(textOf).join("");
}

/** The `<code class="language-mermaid">` inside a `<pre>`, or null.
 *  @param {any} node */
function mermaidCode(node) {
  const kids = Array.isArray(node.children) ? node.children : [];
  for (const kid of kids) {
    if (kid.type !== "element" || kid.tagName !== "code") continue;
    if (classList(kid.properties?.className).includes("language-mermaid")) return kid;
  }
  return null;
}

/** True when this `<pre>` holds a mermaid fence.
 *
 *  Exported so hast-code-block.mjs can decline the same node without
 *  duplicating the shape test. The two plugins both filter "pre" and the
 *  ordering between them is satteri's business, not something to rely on: if
 *  the wrapper ran first it would put a Copy button on a diagram, and the guard
 *  that would have caught it is the <pre> count this plugin exists to keep
 *  balanced. Making the skip explicit costs one import and removes the
 *  dependence on plugin order entirely.
 *  @param {any} node */
export function isMermaidPre(node) {
  return node?.type === "element" && node.tagName === "pre" && mermaidCode(node) !== null;
}

/** @type {import("satteri").HastPluginDefinition} */
export const hastMermaid = {
  name: "polyemesis:mermaid",
  element: {
    filter: ["pre"],
    visit(node, ctx) {
      const code = mermaidCode(node);
      if (!code) return;

      /* THROUGH ctx.replaceNode RATHER THAN BY ASSIGNING node.tagName. The
         first version did the latter and TypeScript rejected it outright: hast
         nodes are readonly to a plugin here, because satteri collects edits
         into a command buffer and replays them instead of letting a visitor
         mutate the tree underneath its own traversal. An in-place write would
         have been ignored or applied at a moment the traversal did not expect,
         and `// @ts-check` on this file is what turned that into a build error
         rather than a diagram that silently never appeared. */
      ctx.replaceNode(node, {
        type: "element",
        tagName: "div",
        properties: {
          className: [MERMAID_CLASS],
          // The renderer flips this to "done" once a diagram has been drawn, so
          // a second pass -- a view transition, a re-run -- cannot hand mermaid
          // its own SVG output back as source.
          "data-mermaid": "pending",
        },
        children: [{ type: "text", value: textOf(code).replace(/\n+$/, "") }],
      });
    },
  },
};
