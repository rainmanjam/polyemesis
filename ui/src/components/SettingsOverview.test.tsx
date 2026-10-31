// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import type { Settings } from "@/lib/types";

/* Two things the round-2 review saw on the live Overview.
 *
 * 1. The Security tile's lines were keyed by their text. While the TLS and
 *    token reads were both pending the tile had two identical "Reading…"
 *    lines, React kept a stale node for the duplicate key, and the settled
 *    tile showed four lines from a three-line array, a stale "Reading…" first.
 *
 * 2. The readiness strip said "Ready to stream: Yes" in green as soon as a
 *    source and a destination existed, directly above a checklist reading
 *    "2 of 5 done" and still waiting for a signal. */

type Deferred<T> = { promise: Promise<T>; resolve: (v: T) => void };
function deferred<T>(): Deferred<T> {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}

let tls = deferred<unknown>();
let tokens = deferred<unknown[]>();
let destinations: unknown[] = [];
let signal = false;

vi.mock("@/hooks/useLiveData", () => ({
  useIngestLive: () => signal,
  useLiveData: () => ({ source: null, bitrate: [] }),
}));

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      listDestinations: () => Promise.resolve(destinations),
      listCreds: () => Promise.resolve([]),
      listAccounts: () => Promise.resolve([]),
      platformGuides: () => Promise.resolve([]),
      listOverlays: () => Promise.resolve({ overlays: [] }),
      getObs: () => Promise.reject(new Error("not in this test")),
      tlsStatus: () => tls.promise,
      listTokens: () => tokens.promise,
    },
  };
});

const { SettingsOverview } = await import("./SettingsOverview");
const { translate } = await import("@/lib/i18n");

const settings = {
  ingest: { mode: "srt", srt: { passphrase: "", latencyMs: 120 }, rtmp: { app: "live", streamKey: "" } },
  listeners: { srtPort: 6000, rtmpPort: 1935 },
  destinations: { staggerMs: 0 },
  failover: { enabled: false },
} as unknown as Settings;

function draw() {
  return render(
    <MemoryRouter>
      <SettingsOverview
        settings={settings}
        system={null}
        sourceCount={1}
        defaultSourceName="Main"
        showAdvanced
        onOpen={() => {}}
      />
    </MemoryRouter>,
  );
}

afterEach(() => {
  cleanup();
  tls = deferred();
  tokens = deferred();
  destinations = [];
  signal = false;
  vi.restoreAllMocks();
});

describe("the Overview tiles", () => {
  it("leaves no stale Reading… line once two pending reads in one tile settle", async () => {
    const errors = vi.spyOn(console, "error").mockImplementation(() => {});
    draw();
    const reading = translate("en", "settingsUx.reading");
    await waitFor(() => expect(screen.getAllByText(reading).length).toBeGreaterThanOrEqual(2));

    await act(async () => {
      tls.resolve({ mode: "selfsigned", configured: "auto", certificate: null });
      tokens.resolve([]);
    });

    await waitFor(() => expect(screen.getByText(translate("en", "settingsUx.tileTokens", { n: 0 }))).toBeTruthy());
    expect(screen.queryByText(reading)).toBeNull();
    // React reports the duplicate key it was given; there must be none.
    const dup = errors.mock.calls.filter((c) => String(c[0]).includes("same key"));
    expect(dup).toEqual([]);
  });
});

describe("the readiness strip agrees with the checklist", () => {
  it("does not say Ready while the checklist still waits for a signal", async () => {
    destinations = [{ id: 1 }];
    signal = false;
    draw();
    const strip = await screen.findByRole("status");
    await waitFor(() =>
      expect(strip.textContent).toContain(translate("en", "settingsUx.readyNotYet", { done: 2, total: 5 })),
    );
    expect(strip.textContent).not.toContain(translate("en", "settingsUx.readyYes"));
  });

  it("says Ready once every step before Go live is done", async () => {
    destinations = [{ id: 1 }];
    signal = true;
    draw();
    const strip = await screen.findByRole("status");
    await waitFor(() => expect(strip.textContent).toContain(translate("en", "settingsUx.readyYes")));
  });
});
