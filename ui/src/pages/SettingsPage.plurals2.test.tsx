// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";

import type { PlatformAccount, PlatformCreds, Settings, SetupGuide } from "@/lib/types";

/* A platform's setup guide reads one as one. The catalogue has no plural
 * rules, so a guide of exactly one step has its own key: it was titled
 * "How to register the app (1 steps)". Harness from SettingsPage.ux.test.tsx. */

function baseSettings(): Settings {
  return {
    display: { timeZone: "" },
    ingest: {
      mode: "srt",
      srt: { passphrase: "", latencyMs: 120 },
      rtmp: { app: "live", streamKey: "" },
    },
    listeners: { srtPort: 6000, rtmpPort: 1935 },
    synth: { silenceOnVideoOnly: true },
    recording: { enabled: false, segmentSeconds: 600, maxGb: 50, maxAgeHours: 168, minFreeGb: 5 },
    preview: { enabled: true, segmentSeconds: 1, videoHeight: 360, videoKbps: 800, idleTimeoutSeconds: 30 },
    meters: { enabled: true, intervalMs: 100 },
    logging: { persistProcessLogs: true, maxFileMb: 8, maxFiles: 3 },
    destinations: { staggerMs: 0 },
    alerts: { retryAttempts: 4 },
    failover: { enabled: false },
    mqtt: { enabled: false },
  } as Settings;
}

let settings = baseSettings();
let sources: { id: number; name: string; isDefault: boolean }[] = [];
let guides: SetupGuide[] = [];
let creds: PlatformCreds[] = [];
let accounts: PlatformAccount[] = [];

vi.mock("@/hooks/useLiveData", () => ({
  useIngestLive: () => false,
  useLiveData: () => ({ source: null, bitrate: [] }),
}));

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      getSettings: () => Promise.resolve(settings),
      listSources: () => Promise.resolve(sources),
      system: () => Promise.resolve({ version: "test", ffmpeg: {}, dataDir: "/var/lib/polyemesis" }),
      putSettings: (next: Settings) => Promise.resolve(next),
      automodMatrix: () => Promise.resolve({ platforms: [], actions: [], checkers: [], cells: [] }),
      completeTour: () => Promise.resolve(),
      listDestinations: () => Promise.resolve([]),
      platformGuides: () => Promise.resolve(guides),
      listCreds: () => Promise.resolve(creds),
      listAccounts: () => Promise.resolve(accounts),
      listOverlays: () => Promise.resolve({ overlays: [] }),
      getObs: () => Promise.reject(new Error("not in this test")),
      listPresets: () => Promise.resolve([]),
      tlsStatus: () => Promise.resolve({ mode: "selfsigned", configured: "auto", certificate: null }),
      listTokens: () => Promise.resolve([]),
      connectUrl: (p: string) => `/api/v1/oauth/${p}/start`,
      accountStats: () => Promise.resolve({ supported: false, reason: "test" }),
      listObs: () => Promise.resolve([]),
    },
  };
});

vi.mock("sonner", () => ({
  toast: { success: () => {}, error: () => {}, warning: () => {}, info: () => {} },
}));

const { SettingsPage } = await import("./SettingsPage");
const { setLanguage, translate } = await import("@/lib/i18n");
const T = (key: Parameters<typeof translate>[1], vars?: Record<string, string | number>) =>
  translate("en", key, vars);

function open(at: string) {
  return render(
    <MemoryRouter initialEntries={[at]}>
      <Routes>
        <Route path="/settings" element={<SettingsPage />} />
        <Route path="*" element={null} />
      </Routes>
    </MemoryRouter>,
  );
}

function guide(steps: string[]): SetupGuide {
  return {
    platform: "twitch" as SetupGuide["platform"],
    name: "Twitch",
    consoleUrl: "",
    redirectPath: "https://box/api/v1/oauth/twitch/callback",
    steps,
    scopes: null,
    supported: true,
  } as SetupGuide;
}

beforeEach(() => {
  settings = baseSettings();
  sources = [{ id: 1, name: "Main", isDefault: true }];
  guides = [];
  creds = [];
  accounts = [];
  window.localStorage.clear();
});

afterEach(() => {
  cleanup();
  setLanguage("en");
});

async function openTwitch() {
  open("/settings?tab=platforms");
  fireEvent.click(await screen.findByRole("button", { name: `${T("settingsUx.setUp")}: Twitch` }));
}

describe("SettingsPage: the setup guide's step count", () => {
  it("says How to register the app (1 step) for a one-step guide", async () => {
    guides = [guide(["one"])];
    await openTwitch();
    expect(await screen.findByText("How to register the app (1 step)")).toBeTruthy();
    expect(screen.queryByText("How to register the app (1 steps)")).toBeNull();
  });

  it("says How to register the app (2 steps) for a two-step guide", async () => {
    guides = [guide(["one", "two"])];
    await openTwitch();
    expect(await screen.findByText("How to register the app (2 steps)")).toBeTruthy();
  });
});
