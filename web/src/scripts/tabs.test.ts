// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { initTabs } from "./tabs";

function mount() {
  document.body.innerHTML = `
    <div role="tablist" id="tl">
      <button role="tab" data-key="a" aria-controls="pa">A</button>
      <button role="tab" data-key="b" aria-controls="pb">B</button>
      <button role="tab" data-key="c" aria-controls="pc">C</button>
    </div>
    <div id="pa">a</div><div id="pb">b</div><div id="pc">c</div>
    <div id="other">other</div>`;
  const tl = document.getElementById("tl")!;
  const tabs = [...tl.querySelectorAll<HTMLElement>('[role="tab"]')];
  const onSelect = vi.fn();
  const ctl = initTabs(tl, onSelect);
  return { tl, tabs, onSelect, ctl };
}
const key = (el: HTMLElement, k: string) =>
  el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true }));
const hidden = (id: string) => document.getElementById(id)!.hidden;

afterEach(() => {
  document.body.innerHTML = "";
});

describe("initTabs", () => {
  it.each([["altKey"], ["ctrlKey"], ["metaKey"]])("ignores an arrow key held with %s (Alt+Left is Back)", (mod) => {
    const { tabs, onSelect } = mount();
    tabs[0].focus();
    for (const k of ["ArrowLeft", "ArrowRight", "Home", "End"]) {
      const ev = new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true, [mod]: true });
      tabs[0].dispatchEvent(ev);
      expect(ev.defaultPrevented).toBe(false);
    }
    expect(onSelect).not.toHaveBeenCalled();
    expect(tabs[0].getAttribute("aria-selected")).toBe("true");
  });

  it("ArrowRight moves to the next tab and wraps", () => {
    const { tabs, onSelect } = mount();
    tabs[0].focus();
    key(tabs[0], "ArrowRight");
    expect(tabs[1].getAttribute("aria-selected")).toBe("true");
    expect(onSelect).toHaveBeenLastCalledWith("b");
    key(tabs[1], "ArrowRight");
    key(tabs[2], "ArrowRight");
    expect(tabs[0].getAttribute("aria-selected")).toBe("true");
    expect(onSelect).toHaveBeenLastCalledWith("a");
  });

  it("ArrowLeft wraps backwards", () => {
    const { tabs } = mount();
    key(tabs[0], "ArrowLeft");
    expect(tabs[2].getAttribute("aria-selected")).toBe("true");
  });

  it("Home and End jump", () => {
    const { tabs, onSelect } = mount();
    key(tabs[0], "End");
    expect(onSelect).toHaveBeenLastCalledWith("c");
    key(tabs[2], "Home");
    expect(onSelect).toHaveBeenLastCalledWith("a");
  });

  it("shows only the selected panel and never touches other elements", () => {
    const { tabs, ctl } = mount();
    ctl.select("b");
    expect(hidden("pa")).toBe(true);
    expect(hidden("pb")).toBe(false);
    expect(hidden("pc")).toBe(true);
    expect(hidden("other")).toBe(false);
    expect(tabs[1].tabIndex).toBe(0);
    expect(tabs[0].tabIndex).toBe(-1);
    expect(tabs[0].getAttribute("aria-selected")).toBe("false");
  });

  it("click selects", () => {
    const { tabs, onSelect } = mount();
    tabs[2].click();
    expect(onSelect).toHaveBeenLastCalledWith("c");
  });

  it("destroy removes listeners", () => {
    const { tabs, onSelect, ctl } = mount();
    ctl.destroy();
    onSelect.mockClear();
    key(tabs[0], "ArrowRight");
    tabs[1].click();
    expect(onSelect).not.toHaveBeenCalled();
  });
});
