// @vitest-environment jsdom

/* The first-run checklist's progress reads one as one. The catalogue has no
 * plural rules, so one step done has its own key: in Spanish it read
 * "1 de 5 hechos". Harness from SetupChecklist.test.tsx. */

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { SetupChecklist } from "./SetupChecklist";
import { setupChecklist } from "@/lib/setupChecklist";
import { setLanguage } from "@/lib/i18n";

afterEach(cleanup);
afterEach(() => setLanguage("en"));

function draw(input: Parameters<typeof setupChecklist>[0]) {
  const checklist = setupChecklist(input);
  if (!checklist) throw new Error("test wants a checklist");
  return render(
    <MemoryRouter>
      <SetupChecklist
        checklist={checklist}
        ingestUrl="rtmp://192.168.1.20:1935/live/studio-a"
        onCopyIngest={() => {}}
        onAddDestination={() => {}}
      />
    </MemoryRouter>,
  );
}

describe("SetupChecklist: the progress reads one as one (es)", () => {
  it("says 1 de 5 hecho with one step done", () => {
    setLanguage("es");
    // A source and nothing else: step 1 only.
    draw({ sourceCount: 1, signal: false, destinations: 0 });

    expect(screen.getByText("1 de 5 hecho")).toBeTruthy();
    expect(screen.getByRole("progressbar", { name: "1 de 5 hecho" })).toBeTruthy();
    expect(screen.queryByText("1 de 5 hechos")).toBeNull();
  });

  it("says 2 de 5 hechos with two steps done", () => {
    setLanguage("es");
    // A source and a destination, no signal yet: steps 1 and 4.
    draw({ sourceCount: 1, signal: false, destinations: 1 });

    expect(screen.getByText("2 de 5 hechos")).toBeTruthy();
    expect(screen.getByRole("progressbar", { name: "2 de 5 hechos" })).toBeTruthy();
  });
});
