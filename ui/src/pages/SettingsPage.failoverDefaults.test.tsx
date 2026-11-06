// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";

import { WIRE } from "@/lib/contract/wire";
import type { PlatformAccount, PlatformCreds, Settings, SetupGuide } from "@/lib/types";

/* THE FAILOVER FIELDS A SETTINGS DOCUMENT OMITS read the server's defaults.
 *
 * The form's fallback for a missing grace period and stable-return time used to
 * be typed in the page (10 s and 60 s) while db.DefaultSettings says 5 s and
 * 30 s. They come from the generated contract now, and this fails on any
 * literal that is not the server's. */

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
    // db.DefaultSettings' values, so every advanced card starts at default.
    preview: { enabled: true, segmentSeconds: 1, videoHeight: 360, videoKbps: 800, idleTimeoutSeconds: 30 },
    meters: { enabled: true, intervalMs: 100 },
    logging: { persistProcessLogs: true, maxFileMb: 8, maxFiles: 3 },
    destinations: { staggerMs: 0 },
    alerts: { retryAttempts: 4 },
    failover: { enabled: true },
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
      system: () => Promise.resolve({ version: "test", ffmpeg: {}, dataDir: "/var/lib/polyemesis/a/very/long/path/for/the/data/dir" }),
      putSettings: (next: Settings) => Promise.resolve(next),
      automodMatrix: () => Promise.resolve({ platforms: [], actions: [], checkers: [], cells: [] }),
      completeTour: () => Promise.resolve(),
      listDestinations: () => Promise.resolve([]),
      platformGuides: () => Promise.resolve(guides),
      listCreds: () => Promise.resolve(creds),
      listAccounts: () => Promise.resolve(accounts),
      listOverlays: () => Promise.resolve({ overlays: [] }),
      // Refused, which both the OBS tab and the Overview tile must survive.
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
const { translate } = await import("@/lib/i18n");
const T = (key: Parameters<typeof translate>[1], vars?: Record<string, string | number>) =>
  translate("en", key, vars);

function Where() {
  const loc = useLocation();
  return <span data-testid="where">{loc.pathname + loc.search}</span>;
}

function open(at: string) {
  return render(
    <MemoryRouter initialEntries={[at]}>
      <Routes>
        <Route path="/settings" element={<><SettingsPage /><Where /></>} />
        <Route path="*" element={<Where />} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  settings = baseSettings();
  sources = [{ id: 1, name: "Main", isDefault: true }];
  guides = [];
  creds = [];
  accounts = [];
  window.localStorage.clear();
});

afterEach(cleanup);

describe("failover fields the document omits", () => {
  it("show the contract's grace period, not a literal typed in the page", async () => {
    open("/settings?tab=pipeline");
    const grace = (await screen.findByLabelText(T("set.gracePeriod"))) as HTMLInputElement;
    expect(grace.value).toBe(String(WIRE.defaults.failoverGraceSeconds));
    expect(grace.value).toBe("5");
  });

  it("show the contract's stable-return time when returning automatically", async () => {
    settings = { ...baseSettings(), failover: { enabled: true, return: "auto" } } as Settings;
    open("/settings?tab=pipeline");
    const stable = (await screen.findByLabelText(T("set.returnAfter"))) as HTMLInputElement;
    expect(stable.value).toBe(String(WIRE.defaults.failoverReturnStableSeconds));
    expect(stable.value).toBe("30");
  });
});
