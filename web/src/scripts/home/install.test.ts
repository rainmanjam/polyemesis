// @vitest-environment jsdom

import { beforeEach, describe, expect, it } from "vitest";
import type { MotionGate } from "./motion";
import { initInstall } from "./install";

const gate = {} as MotionGate;

function page(): HTMLElement {
  document.body.innerHTML = `
    <section data-install>
      <div role="tablist" data-install-tabs>
        <button role="tab" data-key="docker" aria-controls="p-docker" aria-selected="true">Docker</button>
        <button role="tab" data-key="compose" aria-controls="p-compose" aria-selected="false">Compose</button>
        <button role="tab" data-key="script" aria-controls="p-script" aria-selected="false">Script</button>
      </div>
      <div id="p-docker" role="tabpanel"></div>
      <div id="p-compose" role="tabpanel"></div>
      <div id="p-script" role="tabpanel"></div>
    </section>`;
  return document.querySelector<HTMLElement>("[data-install]")!;
}

describe("initInstall", () => {
  let root: HTMLElement;
  beforeEach(() => {
    root = page();
  });

  it("shows only the first panel once script runs, and marks the section ready", () => {
    initInstall(root, gate);
    expect(root.dataset.ready).toBe("true");
    expect(document.getElementById("p-docker")!.hidden).toBe(false);
    expect(document.getElementById("p-compose")!.hidden).toBe(true);
    expect(document.getElementById("p-script")!.hidden).toBe(true);
  });

  it("switches panels on click", () => {
    initInstall(root, gate);
    root.querySelector<HTMLElement>('[data-key="compose"]')!.click();
    expect(document.getElementById("p-compose")!.hidden).toBe(false);
    expect(document.getElementById("p-docker")!.hidden).toBe(true);
  });

  it("leaves every panel readable and the section unready on teardown", () => {
    const stop = initInstall(root, gate);
    stop();
    expect(root.dataset.ready).toBeUndefined();
    for (const id of ["p-docker", "p-compose", "p-script"]) expect(document.getElementById(id)!.hidden).toBe(false);
  });

  it("does nothing without a tablist", () => {
    document.body.innerHTML = "<section data-install></section>";
    expect(() => initInstall(document.querySelector<HTMLElement>("[data-install]")!, gate)()).not.toThrow();
  });
});
