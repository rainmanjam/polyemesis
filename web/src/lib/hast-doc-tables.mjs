// @ts-check
/* Gives a markdown table the scroll container and the pinned first column the
 * site's hand-written tables already have.
 *
 * These are not decorative. PLATFORMS.md has an eight-column capability matrix
 * and API.md a six; on a phone they are several times the width of the screen.
 * global.css already carries both fixes and states why at length: without a
 * scroll container the whole DOCUMENT moves sideways instead of the table, and
 * without a pinned first column, swiping to reach the answer takes the question
 * off the left edge and leaves a grid of bare Yes and No against nothing.
 *
 * A markdown table gets no wrapper and no classes, so it would have had neither
 * -- and it is precisely the content those fixes were measured against. Reusing
 * `.scroll-hint` and `.cmp` rather than writing a docs-only variant, for the same
 * reason the code fences go through CodeBlock: a second treatment of one problem
 * is how the first one rots.
 */

/** @type {import("satteri").HastPluginDefinition} */
export const hastDocTables = {
  name: "polyemesis:doc-tables",
  element: {
    filter: ["table"],
    visit(node, ctx) {
      const parent = ctx.parent(node);
      const cls = parent && "properties" in parent ? parent.properties?.className : undefined;
      if (Array.isArray(cls) && cls.includes("doc-table")) return;

      // `.cmp` is what the sticky-first-column and edge-shadow rules key off, and
      // it belongs on the table. The scroll container and the fade hint go on the
      // wrapper, which is how comparison.astro nests them too.
      // `setProperty` on a hast node sets an HTML property, not a field on the
      // node -- passing "properties" here writes `properties="[object Object]"`
      // into the markup, which is what the first version of this did.
      ctx.setProperty(node, "className", ["cmp"]);
      ctx.wrapNode(node, {
        type: "element",
        tagName: "div",
        properties: { className: ["doc-table", "scroll-hint"] },
        children: [],
      });
    },
  },
};
