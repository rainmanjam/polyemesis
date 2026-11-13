import { describe, expect, it, vi } from "vitest";

import { hastHeadingIds } from "./hast-heading-ids.mjs";

/** An element with a text child, or the given children. */
function el(tagName, children, properties = {}) {
  return { type: "element", tagName, properties, children };
}
const text = (value) => ({ type: "text", value });
const heading = (tag, ...children) => el(tag, children.map((c) => (typeof c === "string" ? text(c) : c)));

/** A fresh plugin instance for one document, and a runner for its visitor. */
function document() {
  const plugin = hastHeadingIds({});
  const setProperty = vi.fn();
  return {
    plugin,
    visit(node) {
      plugin.element.visit(node, { setProperty });
    },
    ids() {
      return setProperty.mock.calls.map((c) => [c[1], c[2]]);
    },
    setProperty,
  };
}

describe("hastHeadingIds", () => {
  it("is a named plugin that visits h2, h3 and h4 only", () => {
    const plugin = hastHeadingIds({});
    expect(plugin.name).toBe("polyemesis:heading-ids");
    expect(plugin.element.filter).toEqual(["h2", "h3", "h4"]);
  });

  it("gives an h2 the id its text slugs to", () => {
    const doc = document();
    doc.visit(heading("h2", "Simple mode"));
    expect(doc.setProperty).toHaveBeenCalledWith(expect.anything(), "id", "simple-mode");
  });

  it("numbers a repeated heading within one document", () => {
    const doc = document();
    doc.visit(heading("h3", "Notes"));
    doc.visit(heading("h3", "Notes"));
    expect(doc.ids()).toEqual([
      ["id", "notes"],
      ["id", "notes-1"],
    ]);
  });

  it("reads the text of nested elements, like inline code and links", () => {
    const doc = document();
    doc.visit(heading("h2", "The ", el("code", [text("-reset-admin")]), " flag"));
    doc.visit(heading("h2", el("a", [el("em", [text("Install")])]), " first"));
    expect(doc.ids().map((i) => i[1])).toEqual(["the--reset-admin-flag", "install-first"]);
  });

  it("starts a new document with a clean count", () => {
    const first = document();
    first.visit(heading("h2", "Overview"));
    const second = document();
    second.visit(heading("h2", "Overview"));
    expect(second.ids()).toEqual([["id", "overview"]]);
  });

  it("overwrites an id that is already there", () => {
    const doc = document();
    const node = el("h2", [text("Simple mode")], { id: "user-content-simple-mode" });
    doc.visit(node);
    expect(doc.setProperty).toHaveBeenCalledWith(node, "id", "simple-mode");
  });

  it("leaves a heading with nothing to slug without an id", () => {
    const doc = document();
    doc.visit(heading("h2", "()"));
    expect(doc.setProperty).not.toHaveBeenCalled();
  });

  it("never subscribes to an h1, which is the page title", () => {
    expect(hastHeadingIds({}).element.filter).not.toContain("h1");
  });

  it("copes with a node that has no children field", () => {
    const doc = document();
    doc.visit({ type: "element", tagName: "h2", properties: {} });
    expect(doc.setProperty).not.toHaveBeenCalled();
  });
});
