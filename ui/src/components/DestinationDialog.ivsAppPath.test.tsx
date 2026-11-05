// @vitest-environment jsdom

import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

/* Kick's dashboard prints its Amazon IVS ingest with no /app, and the server
 * now refuses a save that introduces that URL. The dialog says so AT THE FIELD,
 * while it is typed, and offers the URL that works -- before this, the only
 * word about it was a ten-second toast as the dialog closed. Offered, never
 * applied: the field keeps what was typed until the button is pressed. */

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
    },
  };
});

import { DestinationDialog } from "./DestinationDialog";

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

afterEach(cleanup);

const host = "fa723fc1b171.global-contribute.live-video.net";

function openKick(url: string) {
  render(
    <DestinationDialog
      open
      onOpenChange={() => {}}
      destination={
        {
          id: 3, name: "Kick", kind: "rtmp", platform: "kick", url,
          streamKey: "sk_placeholder_key", audioBitrate: 160, sourceId: 1,
        } as never
      }
      onSaved={() => {}}
    />,
    { wrapper: MemoryRouter },
  );
  fireEvent.click(screen.getByTestId("dest-step-2"));
}

describe("the URL field and an Amazon IVS ingest without /app", () => {
  // Mutation: drop the `kind === "rtmp" && ivsFix` block. Observed: FAIL, no
  // dest-url-ivs-app element.
  it("names the working URL at the field and fills it in only when asked", () => {
    openKick(`rtmps://${host}/`);
    const hint = screen.getByTestId("dest-url-ivs-app");
    expect(hint.textContent).toContain(`rtmps://${host}:443/app`);
    const field = screen.getByLabelText("RTMP URL") as HTMLInputElement;
    expect(field.value).toBe(`rtmps://${host}/`);

    fireEvent.click(screen.getByRole("button", { name: "Use the corrected URL" }));
    expect(field.value).toBe(`rtmps://${host}:443/app`);
    expect(screen.queryByTestId("dest-url-ivs-app")).toBeNull();
  });

  it("says nothing about a URL that already has /app", () => {
    openKick(`rtmps://${host}:443/app`);
    expect(screen.queryByTestId("dest-url-ivs-app")).toBeNull();
  });
});
