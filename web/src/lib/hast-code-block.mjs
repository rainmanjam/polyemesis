// @ts-check
/* Renders every markdown code fence as the site's CodeBlock, not as a bare <pre>.
 *
 * THE BLOCKER THIS EXISTS FOR. check-build.mjs fails the build on any
 * hand-styled <pre>, and its comment says why: three different treatments --
 * radius 0, 4 and 8, three backgrounds, two type sizes -- once shipped for the
 * same kind of content on three pages of one site. Astro renders a fence through
 * Shiki, which emits `<pre class="astro-code" style="...">`, and there are 114
 * fenced blocks across the published documents. The first docs page with code
 * would have failed the build.
 *
 * WHY NOT A SHIKI TRANSFORMER, which was the other option on the table. A
 * transformer can delete the class attribute and satisfy the regex in about four
 * lines. It does not satisfy the RULE. Shiki's output still carries its own
 * background on the <pre> and its own token colours on every span, and nothing
 * else on this site has either -- so the docs would have become the fourth
 * treatment, arrived at by silencing the check that exists to catch exactly
 * that. Beating a guard's regex while breaking what the guard protects is the
 * worst available outcome, because it also removes the alarm.
 *
 * So `syntaxHighlight: false` in astro.config.mjs turns highlighting off, and
 * this wraps the plain `<pre><code>` in the markup CodeBlock.astro emits: a
 * `.code` box and a floating Copy button. A rendered fence and a hand-written
 * one are then the same object -- same radius, ground, size, wrapping and copy
 * affordance -- rather than two things that resemble each other.
 *
 * THE CLASS ON <pre> IS DELIBERATELY NOT STRIPPED HERE. If someone turns
 * highlighting back on, this will wrap a `<pre class="astro-code">` and
 * check-build will fail with the message it was written for. Stripping the class
 * would silence that: two backgrounds would ship, and the only thing that would
 * have noticed is the check that was quietly defeated.
 *
 * THE STYLES AND THE SCRIPT LIVE OUTSIDE THE COMPONENT NOW, and that is a
 * consequence rather than a preference: markup built here carries no Astro scope
 * attribute, so a scoped `<style>` in CodeBlock.astro would not reach it. See
 * `.code` in styles/global.css and scripts/code-copy.ts.
 */

import { isMermaidPre } from "./hast-mermaid.mjs";

/** @type {import("satteri").HastPluginDefinition} */
export const hastCodeBlock = {
  name: "polyemesis:code-block",
  element: {
    filter: ["pre"],
    visit(node, ctx) {
      /* A ```mermaid fence is a DIAGRAM, not a code block, and hast-mermaid.mjs
         turns it into a <div>. Declining it here rather than relying on plugin
         order: if this ran first it would wrap a diagram in a `.code` box and
         offer a Copy button for its source, and the <pre>-count guard in
         check-build.mjs -- which is what would otherwise catch the mistake --
         would be satisfied by exactly that wrapping. */
      if (isMermaidPre(node)) return;

      // Idempotent, so that a second pass -- or a <pre> already inside a
      // CodeBlock, which is how MDX content would arrive -- cannot nest boxes.
      const parent = ctx.parent(node);
      const cls = parent && "properties" in parent ? parent.properties?.className : undefined;
      if (Array.isArray(cls) && cls.includes("code")) return;

      /* THE BUTTON LANDS AFTER THE <pre>, WHICH CodeBlock.astro DOES NOT DO.
         `wrapNode` makes the wrapped node the wrapper's FIRST child and keeps
         the wrapper's declared children after it, so the order is [pre, button]
         rather than [button, pre]. Nothing depends on it: `.code-copy-float` is
         absolutely positioned, so it paints in the same corner either way, and
         the copy script finds its block with `closest`. For a keyboard reader
         the order is arguably the better one -- the code, then the action on it.

         Left as the API produces it rather than reordering CodeBlock.astro's
         template to match, because that component's labelled variant has a real
         header bar that must stay first, and moving only the other one would
         make the component harder to read to remove a difference nobody sees. */
      ctx.wrapNode(node, {
        type: "element",
        tagName: "div",
        properties: { className: ["code"] },
        children: [
          {
            type: "element",
            tagName: "button",
            properties: {
              type: "button",
              className: ["code-copy", "code-copy-float"],
              "data-code-copy": true,
              /* "code block" rather than CodeBlock.astro's "command", because
                 most of these are not commands: a YAML fragment, a JSON response
                 body, a log excerpt. The label is read aloud, so calling a
                 Prometheus scrape a command would be a small lie told out loud. */
              "aria-label": "Copy this code block",
            },
            children: [{ type: "text", value: "Copy" }],
          },
        ],
      });
    },
  },
};
