// @vitest-environment jsdom

import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

/* #3 in the editor: an X destination shows the X console, loads its stored go-live
 * title, and saves the edited title. Other platforms show no console. */

const updates = vi.hoisted(() => [] as Array<Record<string, unknown>>);

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
      platformGuides: () => Promise.resolve([{ platform: "x", livestreamGate: "closed" }]),
      services: () => Promise.resolve({}),
      updateDestination: (_id: number, body: Record<string, unknown>) => {
        updates.push(body);
        return Promise.resolve({ destination: { id: 9, ...body }, warnings: [] });
      },
    },
  };
});

import { DestinationDialog } from "./DestinationDialog";
import en from "@/lib/i18n/en.json";

const stored = (over: Record<string, unknown>) => ({
  id: 9,
  name: "X main",
  kind: "rtmp",
  platform: "x",
  url: "rtmps://a.live.x.com:443/x",
  streamKey: "k",
  sourceId: 1,
  enabled: false,
  goLiveTitle: "Sunday",
  ...over,
});

// Radix's Select needs these, which jsdom does not provide.
beforeAll(() => {
  window.HTMLElement.prototype.hasPointerCapture = () => false;
  window.HTMLElement.prototype.releasePointerCapture = () => {};
  window.HTMLElement.prototype.setPointerCapture = () => {};
  window.HTMLElement.prototype.scrollIntoView = () => {};
});

function open(destination: unknown) {
  return render(
    <DestinationDialog open onOpenChange={() => {}} destination={destination as never} onSaved={() => {}} />,
    { wrapper: MemoryRouter },
  );
}

describe("the X console in the destination editor", () => {
  afterEach(() => {
    cleanup();
    updates.length = 0;
  });

  it("loads the stored go-live title, shows the gate, and saves the edited title", async () => {
    open(stored({}));
    const input = (await waitFor(() => document.getElementById("x-go-live-title"))) as HTMLInputElement;
    expect(input.value).toBe("Sunday");
    await waitFor(() => expect(screen.getByText(en["wave3.x.gateClosed"])).toBeTruthy());
    fireEvent.change(input, { target: { value: "Sunday practice" } });
    fireEvent.click(screen.getByRole("button", { name: en["common.save"] }));
    await waitFor(() => expect(updates.length).toBe(1));
    expect(updates[0].goLiveTitle).toBe("Sunday practice");
  });

  // The go-live announcement (should_not_tweet, PROVISIONAL). Absent means the
  // default, the first go-live of a show only; the operator's choice is saved.
  // MUTATION: xGoLiveTweet left out of the save body. Observed: FAIL, the
  // body carried no xGoLiveTweet.
  it("loads the default go-live announcement and saves the operator's choice", async () => {
    open(stored({}));
    const trigger = (await waitFor(() => document.getElementById("x-go-live-tweet"))) as HTMLElement;
    expect(trigger.textContent).toContain(en["wave3.x.tweet.first"]);
    fireEvent.keyDown(trigger, { key: "ArrowDown" });
    fireEvent.click(await screen.findByRole("option", { name: en["wave3.x.tweet.never"] }));
    // The picker hides the rest of the dialog while it is open; wait for it to close.
    await waitFor(() => expect(trigger.textContent).toContain(en["wave3.x.tweet.never"]));
    fireEvent.click(await screen.findByRole("button", { name: en["common.save"] }));
    await waitFor(() => expect(updates.length).toBe(1));
    expect(updates[0].xGoLiveTweet).toBe("never");
  });

  // MUTATION: the stored value not loaded into the dialog. Observed: FAIL, the
  // picker showed the default.
  it("shows a stored every-go-live choice", async () => {
    open(stored({ xGoLiveTweet: "every" }));
    const trigger = (await waitFor(() => document.getElementById("x-go-live-tweet"))) as HTMLElement;
    expect(trigger.textContent).toContain(en["wave3.x.tweet.every"]);
  });

  // The preset a destination was made from travels with the save, so a platform
  // that saves as "custom" can still be found for its own encoder guidance.
  it("saves the preset the destination was made from", async () => {
    open(stored({ presetId: "x" }));
    await waitFor(() => document.getElementById("x-go-live-title"));
    await waitFor(() => expect(screen.getByText(en["wave3.x.gateClosed"])).toBeTruthy());
    fireEvent.click(screen.getByRole("button", { name: en["common.save"] }));
    await waitFor(() => expect(updates.length).toBe(1));
    expect(updates[0].presetId).toBe("x");
  });

  it("shows no X console for another platform", async () => {
    open(stored({ platform: "twitch", url: "rtmp://live.twitch.tv/app", goLiveTitle: undefined }));
    await waitFor(() => expect(screen.getByRole("button", { name: en["common.save"] })).toBeTruthy());
    expect(document.getElementById("x-go-live-title")).toBeNull();
    expect(document.getElementById("x-go-live-tweet")).toBeNull();
  });
});
