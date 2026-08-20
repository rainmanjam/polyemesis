// @vitest-environment jsdom
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

/* AN ICON THAT DOES NOT PARSE IS NOT A DIFFERENT MARK, IT IS NO MARK.
 *
 * ui/public/favicon.svg shipped unparseable. Its comment explaining why the app
 * and the website must draw the same logo contained a pair of hyphens, which
 * XML forbids inside a comment. SVG is XML: strict, with none of the error
 * recovery an HTML parser gives, so browsers, xmllint and librsvg all rejected
 * the entire file. The browser then dropped the tab icon and fell back to a
 * stale one while the header kept drawing correctly from BrandMark.tsx, so the
 * two surfaces disagreed -- which is indistinguishable, to anyone looking at
 * it, from the three-logo bug that comment was written to prevent.
 *
 * BrandMark.test.tsx did not catch it and structurally could not. Every
 * assertion there reads these files with a REGEX, and its parity check strips
 * comments before comparing -- correct, since the drawing is what has to match
 * between app and site. Nothing anywhere asked whether the bytes we serve are a
 * document at all.
 *
 * So this parses them, with a parser, and covers every icon we ship rather than
 * only the one that broke. jsdom's DOMParser follows the XML spec here: a
 * malformed document comes back with <parsererror> as its root element instead
 * of throwing, so the assertion has to look at what was returned.
 */

const SHIPPED = [
  ["app favicon", "../../public/favicon.svg"],
  ["app icon sprite", "../../public/icons.svg"],
  ["site favicon", "../../../web/public/favicon.svg"],
] as const;

describe("shipped icons", () => {
  it.each(SHIPPED)("%s is well-formed XML, which is what makes it render", (_name, rel) => {
    const svg = readFileSync(new URL(rel, import.meta.url), "utf8");
    const doc = new DOMParser().parseFromString(svg, "image/svg+xml");
    expect(doc.querySelector("parsererror")).toBeNull();
    expect(doc.documentElement.nodeName).toBe("svg");
  });
});
