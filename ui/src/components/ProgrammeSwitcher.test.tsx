// @vitest-environment jsdom

import { beforeAll, afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

/* #638: THE CONSOLE REMEMBERED ITS OWN DEFAULT AND NOTHING COULD SAY OTHERWISE.
 *
 * `rememberProgramme` was called from exactly one place — LiveDataProvider,
 * with the value resolveProgramme had just picked. So the persistence worked
 * perfectly and only ever stored the fallback. An operator with a horizontal
 * and a vertical programme watched one of them for the life of the install,
 * with the other unmetered and nothing on screen saying it existed.
 *
 * These pin the control that closes it, and the two cases where it must NOT
 * appear — a picker with one option is furniture, and one rendered before the
 * programme has resolved is a button that silently does nothing.
 */

import { LiveDataContext, type LiveData } from "@/hooks/useLiveData";
import { ProgrammeSwitcher } from "./ProgrammeSwitcher";

const selectProgramme = vi.fn();

function ctx(over: Partial<LiveData>): LiveData {
  return {
    programme: 1,
    programmeKnown: true,
    sourceCount: 2,
    programmes: [
      { id: 1, name: "Horizontal" },
      { id: 2, name: "Vertical" },
    ],
    selectProgramme,
    refreshSources: async () => {},
    connected: true,
    snapshotKnown: true,
    status: null,
    source: null,
    levels: null,
    system: null,
    bitrate: [],
    logs: [],
    recordingsRevision: 0,
    frameError: null,
    clearLogs: () => {},
    ...over,
  } as LiveData;
}

function show(over: Partial<LiveData> = {}) {
  return render(
    <LiveDataContext.Provider value={ctx(over)}>
      <ProgrammeSwitcher />
    </LiveDataContext.Provider>,
  );
}

beforeAll(() => {
  (window as unknown as { ResizeObserver?: unknown }).ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
  window.HTMLElement.prototype.hasPointerCapture = () => false;
  window.HTMLElement.prototype.releasePointerCapture = () => {};
  window.HTMLElement.prototype.setPointerCapture = () => {};
  window.HTMLElement.prototype.scrollIntoView = () => {};
});

afterEach(() => {
  cleanup();
  selectProgramme.mockReset();
});

describe("the programme switcher", () => {
  it("names the programme the console is following", () => {
    show();
    expect(screen.getByTestId("programme-switcher").textContent).toContain("Horizontal");
  });

  it("offers the others, and choosing one is remembered", async () => {
    show();
    fireEvent.click(screen.getByTestId("programme-switcher"));
    fireEvent.click(await screen.findByText("Vertical"));
    // selectProgramme is what writes through rememberProgramme. Asserting the
    // call rather than localStorage keeps this about the control; the provider
    // test below owns the persistence and the ghost-id rule.
    expect(selectProgramme).toHaveBeenCalledWith(2);
  });

  it("renders nothing when there is only one programme", () => {
    show({ programmes: [{ id: 1, name: "Main" }], sourceCount: 1 });
    // A control offering one option is furniture, and in the chrome it would
    // occupy a slot on every screen of every single-source install to say
    // nothing at all.
    expect(screen.queryByTestId("programme-switcher")).toBeNull();
  });

  it("renders nothing before the programme has resolved", () => {
    // programmeKnown false means the question is unanswered. A picker here
    // would be a control that does nothing, which is how #606 kept coming back
    // after the client was fixed.
    show({ programmeKnown: false });
    expect(screen.queryByTestId("programme-switcher")).toBeNull();
  });
});
