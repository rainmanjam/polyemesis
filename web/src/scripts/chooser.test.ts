// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { initChooser } from "./chooser";

afterEach(() => {
  document.body.innerHTML = "";
});

const table = (id: string) => `
  <table class="cmp" id="${id}">
    <thead><tr>
      <th scope="col">Capability</th>
      <th scope="col" data-col="p">polyemesis</th>
      <th scope="col" data-col="a">Restreamer</th>
      <th scope="col" data-col="b">restream.io</th>
    </tr></thead>
    <tbody><tr>
      <th scope="row">Row</th>
      <td data-col="p">Yes</td><td data-col="a">No</td><td data-col="b">No</td>
    </tr></tbody>
  </table>`;

function page(tables = 1) {
  document.body.innerHTML = `
    <fieldset data-chooser>
      <input type="checkbox" data-chooser-col="p" data-name="polyemesis" data-poly checked disabled>
      <input type="checkbox" data-chooser-col="a" data-name="Restreamer" checked>
      <input type="checkbox" data-chooser-col="b" data-name="restream.io" checked>
      <span role="status">Showing polyemesis, Restreamer and restream.io</span>
    </fieldset>
    ${Array.from({ length: tables }, (_, i) => table(`t${i}`)).join("")}`;
  const root = document.querySelector<HTMLElement>("[data-chooser]")!;
  const box = (key: string) => root.querySelector<HTMLInputElement>(`[data-chooser-col="${key}"]`)!;
  const hiddenIn = (scope: ParentNode, key: string) =>
    [...scope.querySelectorAll<HTMLElement>(`[data-col="${key}"]`)].map((e) => e.hidden);
  const click = (key: string) => box(key).click();
  return { root, box, hiddenIn, click, status: () => root.querySelector("[role=status]")!.textContent };
}

describe("initChooser", () => {
  it("shows the full state on start", () => {
    const p = page();
    initChooser(p.root);
    expect(p.status()).toBe("Showing polyemesis, Restreamer and restream.io");
    expect(document.querySelector(".cmp")!.classList.contains("narrow")).toBe(false);
  });

  it("leaves the status text node untouched when the wording would not change", () => {
    const p = page();
    const node = p.root.querySelector("[role=status]")!.firstChild;
    initChooser(p.root);
    expect(p.root.querySelector("[role=status]")!.firstChild).toBe(node);
    p.click("b");
    expect(p.status()).toBe("Showing polyemesis and Restreamer");
  });

  it("matches columns by key even when the key holds quotes and brackets", () => {
    const p = page();
    document.querySelector("th[data-col=b]")!.setAttribute("data-col", 'a"]b');
    const box = p.box("b");
    box.setAttribute("data-chooser-col", 'a"]b');
    initChooser(p.root);
    expect(() => box.click()).not.toThrow();
    expect(document.querySelector<HTMLElement>('th[data-col="a\\"]b"]')!.hidden).toBe(true);
    expect(p.hiddenIn(document, "a")).toEqual([false, false]);
  });

  it("never hides the polyemesis column, even if its box is unchecked", () => {
    const p = page();
    initChooser(p.root);
    p.box("p").checked = false;
    p.click("b");
    expect(p.hiddenIn(document, "p")).toEqual([false, false]);
    expect(p.hiddenIn(document, "b")).toEqual([true, true]);
  });

  it("hides every cell and header of an unchecked column and no other", () => {
    const p = page();
    initChooser(p.root);
    p.box("b").checked = false;
    p.box("b").dispatchEvent(new Event("change", { bubbles: true }));
    expect(p.hiddenIn(document, "b")).toEqual([true, true]);
    expect(p.hiddenIn(document, "a")).toEqual([false, false]);
    expect(p.hiddenIn(document, "p")).toEqual([false, false]);
    expect(document.querySelector<HTMLElement>('th[scope="row"]')!.hidden).toBe(false);
    expect(p.status()).toBe("Showing polyemesis and Restreamer");
  });

  it("restores a column when it is checked again", () => {
    const p = page();
    initChooser(p.root);
    p.click("b");
    p.click("b");
    expect(p.hiddenIn(document, "b")).toEqual([false, false]);
    expect(p.status()).toBe("Showing polyemesis, Restreamer and restream.io");
  });

  it("marks a table narrow when two or fewer columns show", () => {
    const p = page();
    initChooser(p.root);
    const narrow = () => document.querySelector(".cmp")!.classList.contains("narrow");
    p.click("a");
    expect(narrow()).toBe(true); // polyemesis and restream.io
    p.click("b");
    expect(narrow()).toBe(true); // polyemesis alone
    p.click("b");
    p.click("a");
    expect(narrow()).toBe(false);
  });

  it("hides the columns in every table it controls, and decides narrow table by table", () => {
    const p = page(2);
    initChooser(p.root);
    p.click("a");
    p.click("b");
    for (const t of document.querySelectorAll(".cmp")) {
      expect(t.classList.contains("narrow")).toBe(true);
      expect(p.hiddenIn(t, "a")).toEqual([true, true]);
    }
  });

  it("leaves a table whose columns are not the chooser's alone: neither hidden nor narrow", () => {
    const p = page();
    document.body.insertAdjacentHTML(
      "beforeend",
      `<table class="cmp" id="costs"><thead><tr><th scope="col">Item</th>
        <th data-col="costs-0">One</th><th data-col="costs-1">Two</th><th data-col="costs-2">Three</th>
      </tr></thead><tbody><tr><th scope="row">r</th><td data-col="costs-0">1</td><td data-col="costs-1">2</td><td data-col="costs-2">3</td></tr></tbody></table>`,
    );
    initChooser(p.root);
    p.click("a");
    p.click("b");
    const costs = document.getElementById("costs")!;
    expect(costs.classList.contains("narrow")).toBe(false);
    expect([...costs.querySelectorAll<HTMLElement>("[data-col]")].some((e) => e.hidden)).toBe(false);
    expect(document.getElementById("t0")!.classList.contains("narrow")).toBe(true);
  });

  it("stops listening once disposed", () => {
    const p = page();
    const dispose = initChooser(p.root);
    dispose();
    p.click("b");
    expect(p.hiddenIn(document, "b")).toEqual([false, false]);
    expect(p.status()).toBe("Showing polyemesis, Restreamer and restream.io");
  });

  it("tolerates a checkbox with no name", () => {
    const p = page();
    p.box("a").removeAttribute("data-name");
    initChooser(p.root);
    expect(p.status()).toBe("Showing polyemesis,  and restream.io");
  });

  it("works without a status element", () => {
    const p = page();
    p.root.querySelector("[role=status]")!.remove();
    initChooser(p.root);
    expect(() => p.click("b")).not.toThrow();
    expect(p.hiddenIn(document, "b")).toEqual([true, true]);
  });
});
