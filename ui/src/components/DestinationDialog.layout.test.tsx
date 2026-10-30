// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

/* The add-destination dialog, first impression.
 *
 * It was one max-w-md column whose four-sentence description used "routing
 * profile" and "rendition" before either was explained. A first-time operator
 * reads that before they have chosen a platform. The dialog now states plainly
 * what the form is for, in a wider column that fits the sections it holds.
 *
 * jsdom, because the dialog is a Radix portal and the title and description are
 * only reachable through the rendered tree.
 */

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      ...actual.api,
      listSources: () => Promise.resolve([{ id: 1, name: "Main", publishUrls: {}, isDefault: true }]),
      listAccounts: () => Promise.resolve([]),
      listRenditions: () => Promise.resolve([]),
      listPlatformPresets: () => Promise.resolve([]),
      services: () => Promise.resolve({}),
    },
  };
});

import { DestinationDialog } from "./DestinationDialog";

afterEach(cleanup);

function open() {
  return render(
    <DestinationDialog open onOpenChange={() => {}} destination={null as never} onSaved={() => {}} />,
    { wrapper: MemoryRouter },
  );
}

describe("DestinationDialog, first impression", () => {
  it("is wide enough for its sections, not a narrow single column", async () => {
    open();
    const dialog = await screen.findByRole("dialog");
    expect(dialog.className).toContain("max-w-2xl");
    expect(dialog.className).not.toContain("max-w-md");
  });

  it("says in plain words what the form is for, before it names any field", async () => {
    open();
    await waitFor(() => expect(screen.getByRole("dialog")).toBeTruthy());
    expect(
      screen.getByText(
        "Choose where this programme goes. Audio and video default to sensible settings, so you usually only need the platform and its key. Open the audio and video sections only if you need to change them.",
      ),
    ).toBeTruthy();
  });

  it("does not open with the jargon a newcomer has not met yet", async () => {
    open();
    const dialog = await screen.findByRole("dialog");
    const lead = dialog.textContent?.slice(0, 400) ?? "";
    expect(lead).not.toContain("routing profile");
    expect(lead).not.toContain("rendition");
  });
});

describe("DestinationDialog steps (A6)", () => {
  it("opens on the platform step and moves with Next and Back", async () => {
    open();
    await waitFor(() => expect(screen.getByTestId("dest-step-1").getAttribute("aria-current")).toBe("step"));
    fireEvent.click(screen.getByRole("button", { name: "Next" }));
    expect(screen.getByTestId("dest-step-2").getAttribute("aria-current")).toBe("step");
    fireEvent.click(screen.getByRole("button", { name: "Back" }));
    expect(screen.getByTestId("dest-step-1").getAttribute("aria-current")).toBe("step");
  });

  it("creates from the last step, and an unnamed save goes back to step 1 and says why", async () => {
    open();
    fireEvent.click(screen.getByTestId("dest-step-4"));
    fireEvent.click(await screen.findByRole("button", { name: "Create" }));
    expect(screen.getByTestId("dest-step-1").getAttribute("aria-current")).toBe("step");
    expect(screen.getByRole("alert").textContent).toContain("Give the destination a name first");
  });
});
