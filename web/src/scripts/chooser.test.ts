// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { initChooser, statusText } from "./chooser";

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
      <input type="checkbox" data-chooser-col="p" data-name="polyemesis" checked disabled>
      <input type="checkbox" data-chooser-col="a" data-name="Restreamer" checked>
      <input type="checkbox" data-chooser-col="b" data-name="restream.io" checked>
      <span role="status"></span>
    </fieldset>
    ${Array.from({ length: tables }, (_, i) => table(`t${i}`)).join("")}`;
  const root = document.querySelector<HTMLElement>("[data-chooser]")!;
  const box = (key: string) => root.querySelector<HTMLInputElement>(`[data-chooser-col="${key}"]`)!;
  const hiddenIn = (scope: ParentNode, key: string) =>
    [...scope.querySelectorAll<HTMLElement>(`[data-col="${key}"]`)].map((e) => e.hidden);
  const click = (key: string) => box(key).click();
  return { root, box, hiddenIn, click, status: () => root.querySelector("[role=status]")!.textContent };
}

describe("statusText", () => {
  it("names one, two and three or more products", () => {
    expect(statusText([])).toBe("Showing ");
    expect(statusText(["polyemesis"])).toBe("Showing polyemesis");
    expect(statusText(["polyemesis", "Restreamer"])).toBe("Showing polyemesis and Restreamer");
    expect(statusText(["polyemesis", "Restreamer", "restream.io"])).toBe(
      "Showing polyemesis, Restreamer and restream.io",
    );
  });
});

describe("initChooser", () => {
  it("shows the full state on start", () => {
    const p = page();
    initChooser(p.root);
    expect(p.status()).toBe("Showing polyemesis, Restreamer and restream.io");
    expect(document.querySelector(".cmp")!.classList.contains("narrow")).toBe(false);
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

  it("applies to every table on a page with two", () => {
    const p = page(2);
    initChooser(p.root);
    p.click("a");
    p.click("b");
    for (const t of document.querySelectorAll(".cmp")) {
      expect(t.classList.contains("narrow")).toBe(true);
      expect(p.hiddenIn(t, "a")).toEqual([true, true]);
    }
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
