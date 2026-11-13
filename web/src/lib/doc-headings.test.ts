import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

import { extractHeadings } from "./doc-headings";

describe("extractHeadings", () => {
  it("reads ## and ### lines with their depth, text and slug", () => {
    expect(extractHeadings("# Title\n\n## Simple mode\n\ntext\n\n### Two mixes\n")).toEqual([
      { depth: 2, text: "Simple mode", slug: "simple-mode" },
      { depth: 3, text: "Two mixes", slug: "two-mixes" },
    ]);
  });

  it("skips lines inside a fenced code block", () => {
    const md = "## Real\n\n```bash\n## not a heading\n```\n\n## After\n";
    expect(extractHeadings(md).map((h) => h.text)).toEqual(["Real", "After"]);
  });

  it("is not fooled by a tilde fence, or a fence closed by a longer run", () => {
    const md = "~~~\n## inside tilde\n~~~\n\n````md\n```\n## inside nested\n```\n````\n\n## Out\n";
    expect(extractHeadings(md).map((h) => h.text)).toEqual(["Out"]);
  });

  it("does not close a fence on a different marker", () => {
    const md = "```\n~~~\n## still inside\n```\n## Out\n";
    expect(extractHeadings(md).map((h) => h.text)).toEqual(["Out"]);
  });

  it("reduces inline code ticks to their text", () => {
    expect(extractHeadings("## The `-reset-admin` flag\n")).toEqual([
      { depth: 2, text: "The -reset-admin flag", slug: "the--reset-admin-flag" },
    ]);
  });

  it("reduces emphasis markers and links to their text", () => {
    const [a, b, c] = extractHeadings("## **Bold** and _soft_ and *star*\n## [Install](INSTALL.md) first\n## ![img](a.png) pic\n");
    expect(a.text).toBe("Bold and soft and star");
    expect(b.text).toBe("Install first");
    expect(c.text).toBe("img pic");
  });

  it("removes a closing run of #", () => {
    expect(extractHeadings("## Closing ##\n### Three ###   \n").map((h) => h.text)).toEqual(["Closing", "Three"]);
  });

  it("ignores h1, h4 and deeper, and # lines with no space", () => {
    const md = "# One\n#### Four\n##### Five\n##NoSpace\n    ## indented code\n";
    expect(extractHeadings(md)).toEqual([]);
  });

  it("allows up to three spaces of indent", () => {
    expect(extractHeadings("   ## Indented\n").map((h) => h.text)).toEqual(["Indented"]);
  });

  it("numbers repeated headings the way the page does", () => {
    const slugs = extractHeadings("## Notes\n### Notes\n## Notes\n").map((h) => h.slug);
    expect(slugs).toEqual(["notes", "notes-1", "notes-2"]);
  });

  it("copes with CRLF line endings", () => {
    expect(extractHeadings("## One\r\n\r\n## Two\r\n").map((h) => h.text)).toEqual(["One", "Two"]);
  });

  it("returns nothing for a document with no h2 or h3", () => {
    expect(extractHeadings("Just text\n")).toEqual([]);
    expect(extractHeadings("")).toEqual([]);
  });

  it("decodes the entities a heading can carry, so the slug matches the page", () => {
    const [a, b, c, d] = extractHeadings(
      "## Tom &amp; Jerry\n## &lt;b&gt; &quot;x&quot; &apos;y&apos;\n## It&#39;s &#x41;&#65;\n## &nbsp; &bogus;\n",
    );
    expect(a).toEqual({ depth: 2, text: "Tom & Jerry", slug: "tom--jerry" });
    expect(b.text).toBe('<b> "x" \'y\'');
    expect(c.text).toBe("It's AA");
    expect(d.text).toBe("&nbsp; &bogus;");
  });

  it("reads only top-level ATX headings: setext, block quote, list and HTML block headings give no entry", () => {
    const md = [
      "Setext two",
      "----------",
      "",
      "Setext one",
      "==========",
      "",
      "> ## In a quote",
      "",
      "- ## In a list",
      "",
      "<div>",
      "## In an html block?",
      "</div>",
    ].join("\n");
    expect(extractHeadings(md).map((h) => h.text)).toEqual([]);
    // ...and the block ends at the blank line, so a heading after it is read.
    expect(extractHeadings("<div>\n## inside\n\n## after\n").map((h) => h.text)).toEqual(["after"]);
  });

  it("does not mistake an inline tag at the start of a line for an HTML block", () => {
    expect(extractHeadings("<kbd>Ctrl</kbd> opens it\n## Real heading\n").map((h) => h.text)).toEqual(["Real heading"]);
    expect(extractHeadings('<a href="x">link</a> text\n### Also real\n').map((h) => h.text)).toEqual(["Also real"]);
  });

  it("skips block-level HTML: type 6 tags, pre, script, style and comments", () => {
    expect(extractHeadings("<div>\n## inside\n</div>\n\n## After\n").map((h) => h.text)).toEqual(["After"]);
    for (const open of ["</div>", "<pre>", "<script>", "<style>", "<!-- note", "<H2>", "<hr/>", "<table>"]) {
      expect(extractHeadings(`${open}\n## inside\n\n## after\n`).map((h) => h.text)).toEqual(["after"]);
    }
  });

  it("reads the real docs/AUDIO-ROUTING.md", () => {
    const md = readFileSync(new URL("../../../docs/AUDIO-ROUTING.md", import.meta.url), "utf8");
    const headings = extractHeadings(md);
    expect(headings[0]).toEqual({ depth: 2, text: "Simple mode", slug: "simple-mode" });
    const slugs = headings.map((h) => h.slug);
    expect(new Set(slugs).size).toBe(slugs.length);
    expect(slugs.every((s) => s.length > 0)).toBe(true);
  });
});
