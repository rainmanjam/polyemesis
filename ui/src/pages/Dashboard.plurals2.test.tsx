// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";

/* THE STOP ALL TOAST READS ONE AS ONE.
 *
 * The catalogue has no plural rules, so the report after a Stop all has its own
 * key when the server answered for a single destination. It read "All 1
 * destinations stopped." and "0 of 1 destinations stopped; the rest are listed
 * below." -- "the rest" of one destination being nothing at all.
 *
 * Rendered as the whole Dashboard, through the dialog's confirm, so the toast
 * is the one the operator actually sees. Harness from
 * Dashboard.stopImpact.test.tsx. */

const toasts = vi.hoisted(() => [] as { kind: string; message: string }[]);

vi.mock("sonner", async (importOriginal) => {
  const actual = await importOriginal<typeof import("sonner")>();
  const push = (kind: string) => (message: unknown) => {
    toasts.push({ kind, message: String(message) });
    return kind;
  };
  const toast = Object.assign(push("default"), {
    success: push("success"),
    warning: push("warning"),
    error: push("error"),
    info: push("info"),
    message: push("message"),
    loading: push("loading"),
    dismiss: () => {},
  });
  return { ...actual, toast };
});

import { Dashboard } from "./Dashboard";
import { LiveDataContext, type LiveData } from "@/hooks/useLiveData";
import { setLanguage, translate } from "@/lib/i18n";
import type { BulkDestResult, Status } from "@/lib/types";

const originalFetch = globalThis.fetch;

function row(id: number, name: string, platform: string) {
  return {
    id, name, platform, kind: "rtmp", enabled: true, sourceId: 1, summary: "", tracks: null,
    filterComplex: "", normalization: "off", warnings: null, process: { state: "running" },
  };
}

function status(): Status {
  return {
    renditions: [],
    destinations: [
      row(1, "Twitch", "twitch"),
      row(2, "Kick", "kick"),
    ],
    relay: { port: 0, subscribers: [], rxPackets: 0, rxBytes: 0, txPackets: 0, dropped: 0, tsPackets: 0, tsLost: 0, discontinuities: 0, lossPercent: 0 },
    source: { id: 1, name: "Main", probed: true, tracks: [] },
  } as unknown as Status;
}

function live(): LiveData {
  return {
    programme: 1,
    programmeKnown: true,
    programmes: [{ id: 1, name: "Main" }],
    connected: true,
    snapshotKnown: true,
    status: status(),
    source: { id: 1, name: "Main", probed: true, tracks: [] },
    levels: null,
    system: null,
    bitrate: [],
    logs: [],
  } as unknown as LiveData;
}

const routes: Record<string, unknown> = {
  "/api/v1/settings": { preview: { enabled: false }, recording: { enabled: false }, meters: { enabled: false }, failover: { enabled: false } },
  "/api/v1/system": { ingestUrl: "rtmp://box/live", ffmpeg: { hasLibsrt: true } },
  "/api/v1/setup": { needsSetup: false, minPasswordChars: 8, sources: 1 },
  "/api/v1/sources": [{ id: 1, name: "Main" }],
  "/api/v1/metadata": { targets: [] },
  "/api/v1/metadata/broadcast-window": { accounts: [] },
};

/** What POST /destinations/stop-all answers in each test. */
let stopResults: BulkDestResult[] = [];
let stopAllCalls = 0;

const stopped = (id: number, name: string): BulkDestResult =>
  ({ id, name, platform: "twitch", outcome: "stopped", state: "stopped" });
const refused = (id: number, name: string): BulkDestResult =>
  ({ id, name, platform: "twitch", outcome: "failed", state: "running", message: "the process did not exit" });

beforeEach(() => {
  if (!Element.prototype.scrollTo) Element.prototype.scrollTo = () => {};
  stopAllCalls = 0;
  globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(typeof input === "string" ? input : input.toString());
    const path = url.split("?")[0];
    const json = (body: unknown) =>
      ({ ok: true, status: 200, text: async () => JSON.stringify(body) }) as unknown as Response;
    if (path === "/api/v1/destinations/stop-impact") {
      return json({ destinations: stopResults.length, youtubeBroadcasts: 0 });
    }
    if (path === "/api/v1/destinations/stop-all" && init?.method === "POST") {
      stopAllCalls++;
      return json({ action: "stop", results: stopResults });
    }
    return json(routes[path] ?? {});
  }) as unknown as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = originalFetch;
  vi.restoreAllMocks();
  cleanup();
  setLanguage("en");
  toasts.length = 0;
  stopResults = [];
});

/** Opens the toolbar's Stop all, waits for the server's count, confirms, and
 *  returns the toast the report raised. */
async function stopAllAndToast(): Promise<{ kind: string; message: string }> {
  render(
    <MemoryRouter>
      <LiveDataContext.Provider value={live()}>
        <Dashboard />
      </LiveDataContext.Provider>
    </MemoryRouter>,
  );
  const named = await screen.findAllByRole("button", { name: new RegExp(translate("en", "dash.stopAll")) });
  fireEvent.click(named.find((b) => !b.closest('[data-testid="on-air-bar"]')) as HTMLElement);
  const dialog = await screen.findByRole("dialog");
  const confirm = () =>
    within(dialog).getByRole("button", { name: translate("en", "dash.stopAll") }) as HTMLButtonElement;
  await waitFor(() => expect(confirm().disabled).toBe(false));
  fireEvent.click(confirm());
  await waitFor(() => expect(stopAllCalls).toBe(1));
  await waitFor(() => expect(toasts.some((x) => x.kind === "success" || x.kind === "warning")).toBe(true));
  return toasts.find((x) => x.kind === "success" || x.kind === "warning") as { kind: string; message: string };
}

describe("Dashboard: the Stop all toast reads one as one (en)", () => {
  it("says 1 destination stopped. when the one destination stopped", async () => {
    setLanguage("en");
    stopResults = [stopped(1, "Twitch")];
    const toast = await stopAllAndToast();

    expect(toast).toEqual({ kind: "success", message: "1 destination stopped." });
    expect(toast.message).not.toBe("All 1 destinations stopped.");
  });

  it("says All 2 destinations stopped. when both stopped", async () => {
    setLanguage("en");
    stopResults = [stopped(1, "Twitch"), stopped(2, "Kick")];
    const toast = await stopAllAndToast();

    expect(toast).toEqual({ kind: "success", message: "All 2 destinations stopped." });
  });

  it("says 0 of 1 destination stopped; it is listed below. when the one destination refused", async () => {
    setLanguage("en");
    stopResults = [refused(1, "Twitch")];
    const toast = await stopAllAndToast();

    expect(toast).toEqual({ kind: "warning", message: "0 of 1 destination stopped; it is listed below." });
    expect(toast.message).not.toBe("0 of 1 destinations stopped; the rest are listed below.");
  });

  it("says 1 of 2 destinations stopped; the rest are listed below. when one of two refused", async () => {
    setLanguage("en");
    stopResults = [stopped(1, "Twitch"), refused(2, "Kick")];
    const toast = await stopAllAndToast();

    expect(toast).toEqual({ kind: "warning", message: "1 of 2 destinations stopped; the rest are listed below." });
  });
});
