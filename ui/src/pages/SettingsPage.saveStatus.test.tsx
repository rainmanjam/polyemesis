// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";

import type { PlatformAccount, PlatformCreds, Settings, SetupGuide } from "@/lib/types";

/* app-council A95 ON SETTINGS: ONE SAVE MODEL.
 *
 * Playout, Clips, Routing and Recordings say "Saved" beside the Save button
 * (the kit's SaveStatus). Settings, the largest form surface, confirmed every
 * save with a toast that vanished, and nothing beside the button changed. Each
 * tab's Save now draws SaveStatus: Saved on success, "Not saved. Try again."
 * on a refusal (the toast still carries the server's reason), and no success
 * toast. Each test fails on the old page, which had no status line at all. */

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
    failover: { enabled: false },
    mqtt: { enabled: false },
  } as Settings;
}

let settings = baseSettings();
let sources: { id: number; name: string; isDefault: boolean }[] = [];
let guides: SetupGuide[] = [];
let creds: PlatformCreds[] = [];
let accounts: PlatformAccount[] = [];
let refuse = false;

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
      putSettings: (next: Settings) => (refuse ? Promise.reject(new Error("refused by the server")) : Promise.resolve(next)),
      putCreds: (platform: string, clientId: string) => {
        if (refuse) return Promise.reject(new Error("refused by the server"));
        // Stored, as the server would: the reload after a save lists it.
        creds = [{ platform, clientId, hasSecret: true, updatedAt: "" } as PlatformCreds];
        return Promise.resolve({ platform, clientId, hasSecret: true, check: null });
      },
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

const toasts = vi.hoisted(() => ({ success: [] as string[], error: [] as string[] }));
vi.mock("sonner", () => ({
  toast: {
    success: (m: string) => toasts.success.push(m),
    error: (m: string) => toasts.error.push(m),
    warning: () => {},
    info: () => {},
  },
}));

const { SettingsPage } = await import("./SettingsPage");
const { translate } = await import("@/lib/i18n");
const T = (key: Parameters<typeof translate>[1], vars?: Record<string, string | number>) =>
  translate("en", key, vars);

function open(at: string) {
  return render(
    <MemoryRouter initialEntries={[at]}>
      <Routes>
        <Route path="/settings" element={<SettingsPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

const statusNear = (button: HTMLElement) => {
  const row = button.parentElement as HTMLElement;
  return row.querySelector("[data-save-state]") as HTMLElement | null;
};

beforeEach(() => {
  settings = baseSettings();
  sources = [{ id: 1, name: "Main", isDefault: true }];
  guides = [];
  creds = [];
  accounts = [];
  refuse = false;
  toasts.success.length = 0;
  toasts.error.length = 0;
  window.localStorage.clear();
});

afterEach(cleanup);

describe("Settings saves show the kit SaveStatus, not a success toast (A95)", () => {
  it("Ingest: Save says Saved beside the button", async () => {
    open("/settings?tab=ingest");
    const save = await screen.findByRole("button", { name: new RegExp(T("set.saveIngest")) });
    fireEvent.click(save);
    await waitFor(() => expect(statusNear(save)?.getAttribute("data-save-state")).toBe("saved"), { timeout: 5000 });
    expect(statusNear(save)?.textContent).toBe(T("save.saved"));
    expect(toasts.success).toEqual([]);
  });

  it("Ingest: a refused save says so beside the button, and the toast carries the reason", async () => {
    refuse = true;
    open("/settings?tab=ingest");
    const save = await screen.findByRole("button", { name: new RegExp(T("set.saveIngest")) });
    fireEvent.click(save);
    await waitFor(() => expect(statusNear(save)?.getAttribute("data-save-state")).toBe("failed"), { timeout: 5000 });
    expect(statusNear(save)?.textContent).toBe(T("save.failed"));
    expect(toasts.error).toContain("refused by the server");
  });

  it("Listener ports on an install with no source: Save says Saved", async () => {
    sources = [];
    open("/settings?tab=ingest");
    const save = await screen.findByRole("button", { name: new RegExp(T("set.saveListeners")) });
    fireEvent.change(screen.getByLabelText(T("set.srtPort")), { target: { value: "6001" } });
    expect(statusNear(save)?.getAttribute("data-save-state")).toBe("dirty");
    fireEvent.click(save);
    await waitFor(() => expect(statusNear(save)?.getAttribute("data-save-state")).toBe("saved"), { timeout: 5000 });
    expect(toasts.success).toEqual([]);
  });

  it("Pipeline: the tab's one Save says Saved, or Not saved when refused", async () => {
    open("/settings?tab=pipeline");
    const stagger = (await screen.findByLabelText(T("set.stagger"))) as HTMLInputElement;
    const save = screen.getByRole("button", { name: new RegExp(T("set.savePipeline")) }) as HTMLButtonElement;
    // The tab reseeds its draft from the stored settings in an effect, which a
    // slow run (CI's coverage pass) can flush after the edit. Re-apply the edit
    // until it holds and Save is enabled, so the click never lands on a
    // disabled button.
    const edit = async (value: string) =>
      waitFor(
        () => {
          if (stagger.value !== value) fireEvent.change(stagger, { target: { value } });
          expect(stagger.value).toBe(value);
          expect(save.disabled).toBe(false);
        },
        { timeout: 5000 },
      );

    await edit("250");
    fireEvent.click(save);
    await waitFor(() => expect(statusNear(save)?.getAttribute("data-save-state")).toBe("saved"), { timeout: 5000 });
    expect(toasts.success).toEqual([]);

    refuse = true;
    await edit("500");
    fireEvent.click(save);
    await waitFor(() => expect(statusNear(save)?.getAttribute("data-save-state")).toBe("failed"), { timeout: 5000 });
  });

  it("Platforms: Save credentials says Saved beside the button", async () => {
    guides = [guide("twitch", "Twitch")];
    open("/settings?tab=platforms");
    fireEvent.click(await screen.findByRole("button", { name: `${T("settingsUx.setUp")}: Twitch` }));
    // By id: the labels are per platform (lib/platformCredentialLabels).
    await waitFor(() => expect(document.getElementById("cid-twitch")).not.toBeNull());
    fireEvent.change(document.getElementById("cid-twitch") as HTMLElement, { target: { value: "an-id" } });
    fireEvent.change(document.getElementById("cs-twitch") as HTMLElement, { target: { value: "not-a-real-one" } });
    const save = screen.getByRole("button", { name: new RegExp(T("i18nSweep.saveCredentials")) });
    expect(statusNear(save)?.getAttribute("data-save-state")).toBe("dirty");
    fireEvent.click(save);
    await waitFor(() => expect(statusNear(save)?.getAttribute("data-save-state")).toBe("saved"), { timeout: 5000 });
    expect(toasts.success).toEqual([]);
  });
});

function guide(platform: string, name: string, extra: Partial<SetupGuide> = {}): SetupGuide {
  return {
    platform: platform as SetupGuide["platform"],
    name,
    consoleUrl: "",
    redirectPath: `https://box/api/v1/oauth/${platform}/callback`,
    steps: ["one", "two", "three"],
    scopes: null,
    supported: true,
    ...extra,
  } as SetupGuide;
}
