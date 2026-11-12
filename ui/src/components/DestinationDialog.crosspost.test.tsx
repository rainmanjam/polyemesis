// @vitest-environment jsdom
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

/* The Facebook cross-post rows are an editable list with no id of their own.
 * Deleting a middle row must keep the other rows' inputs holding their own
 * page ids, and the saved destination must carry only the page ids. */

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
      platformGuides: () => Promise.resolve([]),
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
  name: "Facebook main",
  kind: "rtmp",
  platform: "facebook",
  url: "rtmps://live-api-s.facebook.com:443/rtmp/",
  streamKey: "k",
  sourceId: 1,
  enabled: false,
  ...over,
});

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

// The Facebook box sits on the last wizard step; editing can save from any step.
async function toLastStep() {
  for (let i = 0; i < 3; i++) {
    fireEvent.click(await screen.findByRole("button", { name: en["addDest.next"] }));
  }
}

async function save() {
  await waitFor(() => expect(screen.getByRole("button", { name: en["common.save"] })).toBeTruthy());
  fireEvent.click(screen.getByRole("button", { name: en["common.save"] }));
  await waitFor(() => expect(updates.length).toBe(1), { timeout: 4000 });
  return updates[0];
}

describe("Facebook cross-post rows keep their identity when a middle row is removed", () => {
  afterEach(() => {
    cleanup();
    updates.length = 0;
  });

  it("keeps the last row's input node and value after deleting the middle row", async () => {
    open(stored({ facebook: { crosspost: [{ pageId: "111" }, { pageId: "222" }, { pageId: "333" }] } }));
    await toLastStep();
    const placeholder = en["dest.fbCrosspostPageId"];
    const inputs = (await screen.findAllByPlaceholderText(placeholder)) as HTMLInputElement[];
    expect(inputs.map((i) => i.value)).toEqual(["111", "222", "333"]);
    const lastNode = inputs[2];

    fireEvent.click(screen.getAllByRole("button", { name: en["dest.fbCrosspostRemove"] })[1]);

    // With index keys React would unmount the row-3 node and reuse it for row 2.
    expect(lastNode.isConnected).toBe(true);
    const after = screen.getAllByPlaceholderText(placeholder) as HTMLInputElement[];
    expect(after.map((i) => i.value)).toEqual(["111", "333"]);
    expect(after[1]).toBe(lastNode);
  });

  it("saves only the page ids, with no row keys, after a middle delete", async () => {
    open(stored({ facebook: { crosspost: [{ pageId: "111" }, { pageId: "222" }, { pageId: "333" }] } }));
    await toLastStep();
    await screen.findAllByPlaceholderText(en["dest.fbCrosspostPageId"]);
    fireEvent.click(screen.getAllByRole("button", { name: en["dest.fbCrosspostRemove"] })[1]);
    const body = await save();
    expect((body.facebook as { crosspost: unknown }).crosspost).toEqual([{ pageId: "111" }, { pageId: "333" }]);
  });
});
