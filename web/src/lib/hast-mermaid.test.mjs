import { describe, expect, it, vi } from "vitest";

import { MERMAID_CLASS, hastMermaid, isMermaidPre } from "./hast-mermaid.mjs";

/* THE FIRST UNIT TESTS ON THIS SITE, AND WHAT MADE THEM WORTH ADDING.
 *
 * web/ is verified by scripts/check-build.mjs, which runs against dist/ after
 * every build and is a genuinely good harness -- it is what caught this
 * plugin's first version, by counting 21 <pre> against 19 Copy buttons. That
 * check is not being replaced.
 *
 * What it cannot do is tell you WHY the count is wrong, and it only runs on a
 * full build of 38 pages. This plugin decides, per fence, whether a reader sees
 * a diagram or forty lines of `flowchart TD`, and the decision is a handful of
 * pure predicates over a hast node. Those are worth pinning directly, at the
 * point where a mistake is one line rather than a page count.
 *
 * THE ASSERTION THAT MATTERS MOST IS THE NEGATIVE ONE. If isMermaidPre ever
 * returned true for an ordinary fence, hast-code-block.mjs would decline it,
 * that block would lose its Copy button AND its `.code` box, and it would ship
 * as the bare hand-styled <pre> that the whole check-build ban exists to
 * prevent. One over-eager predicate is all that stands between here and there.
 */

/** A hast `<pre><code class="language-X">source</code></pre>`. */
function preWith(lang, ...textNodes) {
  return {
    type: "element",
    tagName: "pre",
    properties: {},
    children: [
      {
        type: "element",
        tagName: "code",
        properties: lang === null ? {} : { className: [`language-${lang}`] },
        children: textNodes.map((value) => ({ type: "text", value })),
      },
    ],
  };
}

/** Runs the plugin's visitor and returns whatever it asked to replace the node
 *  with, or null if it declined. */
function visitWith(node) {
  const replaceNode = vi.fn();
  hastMermaid.element.visit(node, { replaceNode });
  return replaceNode.mock.calls.length ? replaceNode.mock.calls[0][1] : null;
}

describe("isMermaidPre", () => {
  it("recognises a mermaid fence", () => {
    expect(isMermaidPre(preWith("mermaid", "flowchart TD"))).toBe(true);
  });

  it("leaves every other fence alone, so it keeps its Copy button", () => {
    // The negative case, and the expensive one. A true here strips a code block
    // of the markup that makes it a code block.
    for (const lang of ["bash", "yaml", "json", "console", "go", "ts"]) {
      expect(isMermaidPre(preWith(lang, "echo hi"))).toBe(false);
    }
  });

  it("leaves a fence with no language alone", () => {
    expect(isMermaidPre(preWith(null, "plain"))).toBe(false);
  });

  it("does not match a language that merely starts with the word", () => {
    // `language-mermaidish` is not mermaid. The class test is an exact list
    // membership rather than a prefix, and this is what says so.
    expect(isMermaidPre(preWith("mermaidish", "x"))).toBe(false);
  });

  it("is false for anything that is not a <pre>", () => {
    expect(isMermaidPre({ type: "element", tagName: "div", children: [] })).toBe(false);
    expect(isMermaidPre({ type: "text", value: "mermaid" })).toBe(false);
    expect(isMermaidPre(null)).toBe(false);
    expect(isMermaidPre(undefined)).toBe(false);
  });

  it("is false for a <pre> with no code child at all", () => {
    expect(isMermaidPre({ type: "element", tagName: "pre", properties: {}, children: [] })).toBe(false);
  });
});

describe("the mermaid visitor", () => {
  it("replaces the <pre> with a div the renderer can find", () => {
    const out = visitWith(preWith("mermaid", "flowchart TD\n  a --> b"));

    expect(out).not.toBeNull();
    // A DIV, NOT A <pre>. Both check-build guards depend on this: any
    // `<pre class="...">` fails the build outright, and every surviving <pre>
    // must pair with a Copy button.
    expect(out.tagName).toBe("div");
    expect(out.properties.className).toEqual([MERMAID_CLASS]);
    expect(out.properties["data-mermaid"]).toBe("pending");
  });

  it("carries the diagram source through as text", () => {
    const src = "flowchart TD\n  a --> b";
    const out = visitWith(preWith("mermaid", src));

    expect(out.children).toEqual([{ type: "text", value: src }]);
  });

  it("keeps `-->` as an arrow rather than losing it to escaping", () => {
    // A text node, deliberately: hast serialises one with the escaping HTML
    // needs, so an edge arrow survives instead of being read as markup. If this
    // were ever emitted as raw HTML, `-->` would close a comment and take the
    // rest of the diagram with it.
    const out = visitWith(preWith("mermaid", "a -- no --> b"));
    expect(out.children[0].value).toContain("-->");
  });

  it("joins a fence that arrives as several text nodes", () => {
    // Nothing guarantees one text node per fence, and a diagram silently
    // truncated at the first newline would be a confusing way to discover that.
    const out = visitWith(preWith("mermaid", "flowchart TD\n", "  a --> b\n", "  b --> c"));
    expect(out.children[0].value).toBe("flowchart TD\n  a --> b\n  b --> c");
  });

  it("strips the trailing newlines a fence always ends with", () => {
    const out = visitWith(preWith("mermaid", "flowchart TD\n  a --> b\n\n"));
    expect(out.children[0].value).toBe("flowchart TD\n  a --> b");
  });

  it("declines every other fence, replacing nothing", () => {
    // The counterpart to the negative case above, at the mutation rather than
    // the predicate: a code block must reach hast-code-block.mjs untouched.
    expect(visitWith(preWith("bash", "echo hi"))).toBeNull();
    expect(visitWith(preWith(null, "plain"))).toBeNull();
  });
});
