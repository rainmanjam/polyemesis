// @ts-check
/* Puts a stable id on every h2, h3 and h4 of a documentation page.
 *
 * The "On this page" list (doc-headings.ts) links to `#<slug>` computed from the
 * markdown source. Whatever id the markdown processor happens to emit today is
 * therefore not good enough: it is the processor's business and can change with
 * an upgrade, and the list and the page would drift apart without a sound. So
 * the ids are ours, made by the same uniqueSlugger() the list uses, and any id
 * already on the heading is overwritten.
 *
 * THIS IS A FACTORY, NOT A PLAIN OBJECT, ON PURPOSE. The slugger counts repeats,
 * and the count belongs to one document. satteri calls a plugin factory once per
 * compile, so each document gets a fresh slugger; a module-level one would carry
 * "overview" from one page into the next and number its first Overview
 * `overview-1`.
 *
 * Changing this plugin changes the markdown pipeline's output: Astro's content
 * cache is keyed on the source file, so delete node_modules/.astro before
 * rebuilding (see the note at hastPlugins in astro.config.mjs).
 */
import { uniqueSlugger } from "./heading-slug.mjs";

/** Concatenated text under a node, whatever the inline markup around it.
 *  @param {any} node @returns {string} */
function textOf(node) {
  if (node.type === "text") return String(node.value);
  const kids = Array.isArray(node.children) ? node.children : [];
  return kids.map(textOf).join("");
}

/** @returns {import("satteri").HastPluginDefinition} */
export const hastHeadingIds = () => {
  const slug = uniqueSlugger();
  return {
    name: "polyemesis:heading-ids",
    element: {
      filter: ["h2", "h3", "h4"],
      visit(node, ctx) {
        const id = slug(textOf(node));
        // A heading with no letters or digits has nothing to link to; leave it
        // without an id and let the build's id check name it.
        if (id !== "") ctx.setProperty(node, "id", id);
      },
    },
  };
};
