// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";

/* THE CONTROL THAT STOPS THE SERVER CHOOSING A PROGRAMME FOR YOU.
 *
 * db.CreateDestination used to fill an omitted source_id with the first source.
 * On a one-source install that is the only possible answer; on an install with
 * several it attached a destination to a programme nobody picked and nothing on
 * screen said which. The server now refuses, and this picker is how an operator
 * answers.
 *
 * THE ASSERTION THAT MATTERS IS THE ONE ABOUT NOTHING BEING SELECTED. A picker
 * that defaulted to the first programme would look like a working control and
 * reproduce the exact bug it was built to remove — a choice made by the machine,
 * wearing the costume of a choice made by a person. destinationSource.test.ts
 * pins that decision as a function; this pins that the dialog actually renders
 * it, which is the half a pure-function test cannot reach.
 *
 * jsdom, because the starting value is applied by an effect that waits for the
 * source list to arrive, and renderToStaticMarkup runs no effects.
 */

const listSources = vi.fn();

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      ...actual.api,
      listSources: () => listSources(),
      listAccounts: () => Promise.resolve([]),
      listRenditions: () => Promise.resolve([]),
      listPlatformPresets: () => Promise.resolve([]),
      services: () => Promise.resolve({}),
    },
  };
});

import { DestinationDialog } from "./DestinationDialog";

const two = [
  { id: 1, name: "Main", publishUrls: {}, isDefault: true },
  { id: 2, name: "Studio B", publishUrls: {}, isDefault: false },
];

function open(sources: unknown[], destination: unknown = null) {
  listSources.mockResolvedValue(sources);
  return render(
    <DestinationDialog
      open
      onOpenChange={() => {}}
      destination={destination as never}
      onSaved={() => {}}
    />,
  );
}

/** A stored destination, for the EDIT path. */
const saved = (sourceId: number) => ({
  id: 9,
  name: "Twitch",
  kind: "rtmp",
  platform: "custom",
  url: "rtmp://example.invalid/live",
  streamKey: "k",
  sourceId,
});

describe("the destination dialog's programme picker", () => {
  beforeEach(() => listSources.mockReset());
  afterEach(cleanup);

  it("asks which programme when there is more than one", async () => {
    open(two);
    await waitFor(() => expect(screen.getByTestId("source-picker")).toBeTruthy());
  });

  it("does not ask when there is only one, because the answer is not a choice", async () => {
    // DestinationDialog already rules this out for the multitrack switch:
    // "offering it elsewhere would be a control that cannot do anything."
    open([two[0]]);
    await waitFor(() => expect(screen.queryByText(/Name/i)).toBeTruthy());
    expect(screen.queryByTestId("source-picker")).toBeNull();
  });

  it("starts on nothing, so the first programme is not chosen by default", async () => {
    open(two);
    const trigger = await waitFor(() => screen.getByTestId("source-picker"));
    // A Radix trigger shows its placeholder while no value is set. If this ever
    // reads "Main", the machine has made the choice again.
    expect(trigger.textContent).not.toContain("Main");
    expect(trigger.textContent).not.toContain("Studio B");
  });

  it("says why the save is unavailable, next to the control", async () => {
    // DESIGN-SYSTEM.md: "A disabled control says why, next to itself. Never a
    // bare disabled state." Without this the save button is simply dead and the
    // reason is several fields away.
    open(two);
    await waitFor(() => expect(screen.getByTestId("source-picker")).toBeTruthy());
    expect(screen.getByText(/which programme/i)).toBeTruthy();
  });

  it("opens an existing destination on ITS programme, not the first", async () => {
    // The edit path, which the create tests above never reach. Opening a
    // destination that carries Studio B while showing Main would be an offer to
    // move it that nobody made -- and the operator would have to notice the
    // difference to decline it.
    open(two, saved(2));
    const trigger = await waitFor(() => screen.getByTestId("source-picker"));
    await waitFor(() => expect(trigger.textContent).toContain("Studio B"));
    expect(trigger.textContent).not.toContain("Main");
  });

  it("does not nag about a choice already made", async () => {
    // The "which programme" line is guidance for an unanswered question. On a
    // destination that already names one it is noise, and DESIGN-SYSTEM.md puts
    // the reason next to the disabled control precisely so it disappears when
    // the control is no longer blocking.
    open(two, saved(2));
    await waitFor(() => expect(screen.getByTestId("source-picker")).toBeTruthy());
    await waitFor(() => expect(screen.queryByText(/which programme/i)).toBeNull());
  });
});
