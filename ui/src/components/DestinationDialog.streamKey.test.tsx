// @vitest-environment jsdom

import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

/* #610: THE KEY FIELD IS CLEARED WHEN THE TRANSPORT LEAVES RTMP.
 *
 * Retyping an existing RTMP destination to srt, file or audio left the key on
 * the row. The field renders only for RTMP, so nothing on screen showed it
 * again; save omits an untouched key, so nothing sent a clear either. The
 * destination then published to the bare URL with NO CREDENTIAL, and the key sat
 * in the database un-rotatable and still returned in full by GET /destinations.
 *
 * db.Validate now REFUSES that combination. This file pins the second rung: the
 * dialog clears the field, so an operator cannot produce the state the validator
 * rejects and never meets an error naming a field the form does not show.
 *
 * THE PAYLOAD ASSERTION IS THE ONE THAT MATTERS. Clearing the input is not
 * enough on its own — save only sends the key when it differs from the loaded
 * one, so a test that only checked the input would pass on a dialog that cleared
 * the box and told the server nothing.
 *
 * NO REAL KEY. The sentinel below is synthetic; a fixture with a plausible key
 * in it ends up in CI output, which is the class of leak this issue is about.
 */

const SENTINEL = "SENTINEL-ui-stranded-4b7d0e";

const updateDestination = vi.fn();

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
      listServices: () => Promise.resolve({ services: [] }),
      services: () => Promise.resolve({}),
      updateDestination: (id: unknown, body: unknown) => updateDestination(id, body),
    },
  };
});

import { DestinationDialog, keyAfterKindChange } from "./DestinationDialog";

/** An RTMP destination with a key on it, which is the state the bug starts from. */
const rtmpWithKey = {
  id: 9,
  name: "Main",
  kind: "rtmp",
  platform: "custom",
  url: "rtmp://ingest.example/live",
  streamKey: SENTINEL,
  audioBitrate: 160,
  sourceId: 1,
};

/* Radix's Select is built on pointer capture, which jsdom does not implement.
 * Without these three the trigger never opens and the test would be measuring
 * jsdom rather than the dialog. */
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

function openDialog(destination: unknown) {
  return render(
    <DestinationDialog
      open
      onOpenChange={() => {}}
      destination={destination as never}
      onSaved={() => {}}
    />,
  );
}

/** Drive the transport <Select> the way an operator does. */
async function pickTransport(label: RegExp) {
  const trigger = screen.getAllByRole("combobox")[0];
  // Keyboard rather than pointer: Radix's trigger opens on pointerdown only
  // after a pointer-capture dance jsdom cannot complete, and ArrowDown is a
  // route a real operator has anyway.
  fireEvent.keyDown(trigger, { key: "ArrowDown" });
  const option = await screen.findByRole("option", { name: label });
  fireEvent.click(option);
}

async function save() {
  fireEvent.click(screen.getByRole("button", { name: /^Save$/ }));
  await waitFor(() => expect(updateDestination).toHaveBeenCalled());
  return updateDestination.mock.calls[0][1] as Record<string, unknown>;
}

describe("the rule", () => {
  it("clears the key when the transport leaves rtmp", () => {
    expect(keyAfterKindChange("rtmp", "srt", SENTINEL)).toBe("");
    expect(keyAfterKindChange("rtmp", "file", SENTINEL)).toBe("");
    // "audio" is deliberately absent: the UI's DestKind is rtmp | srt | file,
    // so this dialog cannot produce an audio destination at all. The Go side
    // covers that kind, where it IS reachable — the API accepts it.
  });

  it("keeps the key when the transport does not change", () => {
    // The preset picker calls this with the kind unchanged, halfway through a
    // destination whose key is already typed.
    expect(keyAfterKindChange("rtmp", "rtmp", SENTINEL)).toBe(SENTINEL);
    // And for a kind that carries no key either, which is the assertion that
    // makes "an unchanged transport never touches the key" a rule about the
    // CHANGE rather than a coincidence of which kinds carry a key. Found by
    // deleting the `to === from` guard and watching every other test pass.
    expect(keyAfterKindChange("file", "file", SENTINEL)).toBe(SENTINEL);
  });

  it("keeps the key when the transport comes back to rtmp", () => {
    expect(keyAfterKindChange("srt", "rtmp", SENTINEL)).toBe(SENTINEL);
  });
});

describe("the destination dialog's stream key", () => {
  beforeEach(() => updateDestination.mockReset().mockResolvedValue({ warnings: [] }));
  afterEach(cleanup);

  it("is sent as empty when the transport is retyped away from rtmp", async () => {
    openDialog(rtmpWithKey);
    await waitFor(() => expect(screen.getByDisplayValue(SENTINEL)).toBeTruthy());

    await pickTransport(/Local file/);
    // The field is gone, which is the half the bug relied on.
    expect(screen.queryByDisplayValue(SENTINEL)).toBeNull();

    const body = await save();
    expect(body.kind).toBe("file");
    expect(body.streamKey).toBe("");
  });

  it("leaves the key alone when the transport is not touched", async () => {
    openDialog(rtmpWithKey);
    await waitFor(() => expect(screen.getByDisplayValue(SENTINEL)).toBeTruthy());

    fireEvent.change(screen.getByLabelText(/^Name$/i), { target: { value: "Renamed" } });

    const body = await save();
    expect(body.kind).toBe("rtmp");
    // Omitted, not cleared: an untouched key must not be resent, because the
    // pre-announce sweep may have replaced it since the dialog opened.
    expect("streamKey" in body).toBe(false);
  });
});
