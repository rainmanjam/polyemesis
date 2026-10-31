// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";

import type { PlatformAccount, PlatformCreds, Settings, SetupGuide } from "@/lib/types";

/* THE SETTINGS REDESIGN, FROM THE OPERATOR'S SIDE (settings-council Phase 1 + 2).
 *
 * Each block below is one council finding, driven through the whole page the
 * way an operator meets it. Every assertion here fails on the page as it was
 * before the redesign: there was no Overview, no grouped rail, no Advanced
 * switch, no platform rows, no stored verdict, and the secret.key note sat at
 * the bottom of the transport card. */

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
        <Route path="/system" element={<Where />} />
        <Route path="*" element={<Where />} />
      </Routes>
    </MemoryRouter>,
  );
}

const panel = async () => (await screen.findAllByRole("tabpanel"))[0];

beforeEach(() => {
  settings = baseSettings();
  sources = [{ id: 1, name: "Main", isDefault: true }];
  guides = [];
  creds = [];
  accounts = [];
  window.localStorage.clear();
});

afterEach(cleanup);

/* ------------------------------------------------- R3, R5: the Overview */

describe("the Overview (settings-council R3, R5)", () => {
  it("is where an install with no source lands, with the setup checklist on it", async () => {
    sources = [];
    open("/settings");
    expect(await screen.findByTestId("settings-overview")).toBeTruthy();
    expect(await screen.findByTestId("setup-checklist")).toBeTruthy();
    expect(screen.getByText(T("settingsUx.readyTitle"))).toBeTruthy();
  });

  it("does not move an install that already has sources off the Ingest tab", async () => {
    open("/settings");
    await waitFor(() => expect(screen.getByRole("tab", { name: T("set.ingest") }).getAttribute("aria-selected")).toBe("true"));
    expect(screen.queryByTestId("settings-overview")).toBeNull();
  });

  it("draws one tile per area, and says when a read failed rather than claiming zero", async () => {
    sources = [];
    open("/settings?tab=overview");
    const overview = await screen.findByTestId("settings-overview");
    await waitFor(() => expect(within(overview).getByText(T("settingsUx.tilePlatforms", { connected: 0, total: 0 }))).toBeTruthy());
    expect(within(overview).getByText(T("settingsUx.tileNoSource"))).toBeTruthy();
    expect(within(overview).getByText(T("settingsUx.tileFailoverOff"))).toBeTruthy();
    expect(within(overview).getByText(T("settingsUx.tileNotBackedUp"))).toBeTruthy();
  });

  it("opens an area from its tile", async () => {
    sources = [];
    open("/settings?tab=overview");
    fireEvent.click(await screen.findByRole("button", { name: T("settingsUx.openArea", { area: T("set.tabSecurity") }) }));
    await waitFor(() => expect(screen.getByTestId("where").textContent).toBe("/settings?tab=security"));
  });
});

/* ------------------------------------------- R6: grouped rail, old links */

describe("the grouped rail keeps every old link (settings-council R6)", () => {
  it.each([
    ["ingest", "set.ingest"],
    ["pipeline", "set.tabPipeline"],
    ["platforms", "set.tabPlatforms"],
    ["overlays", "set.tabOverlays"],
    ["obs", "obs.tab"],
    ["security", "set.tabSecurity"],
    ["danger", "danger.title"],
  ] as const)("?tab=%s still opens its own panel", async (tab, label) => {
    open(`/settings?tab=${tab}`);
    await waitFor(() =>
      expect(screen.getByRole("tab", { name: T(label) }).getAttribute("aria-selected")).toBe("true"),
    );
  });

  it("sends ?tab=debug to the System page, as it did before", async () => {
    open("/settings?tab=debug");
    await waitFor(() => expect(screen.getByTestId("where").textContent).toBe("/system"));
  });

  it("opens the Overview for a tab value no build ever had", async () => {
    open("/settings?tab=nonsense");
    expect(await screen.findByTestId("settings-overview")).toBeTruthy();
  });

  it("groups the tabs under labelled task headings, outside the tab lists", async () => {
    open("/settings?tab=ingest");
    const streamOut = await screen.findByRole("tablist", { name: T("settingsUx.groupStreamOut") });
    expect(within(streamOut).getAllByRole("tab").map((t) => t.textContent)).toEqual([
      T("set.tabPlatforms"),
      T("set.tabOverlays"),
      T("obs.tab"),
    ]);
    // The heading names the list; it is not one of its tabs.
    expect(within(streamOut).queryByText(T("settingsUx.groupStreamOut"))).toBeNull();
  });
});

/* ----------------------------------------- R4, R7: Pipeline and Advanced */

const advancedCard = (key: string) => document.querySelector<HTMLElement>(`[data-advanced-card="${key}"]`);

describe("Pipeline groups and the Show advanced switch (settings-council R4, R7)", () => {
  it("hides expert cards at their defaults, says which, and keeps them mounted", async () => {
    open("/settings?tab=pipeline");
    await screen.findByText(T("settingsUx.groupRoutine"));
    expect(advancedCard("set.mqtt")?.hidden).toBe(true);
    expect(advancedCard("set.preview")?.hidden).toBe(true);
    // Mounted, so the reachability walk and the search still find the control.
    expect(document.getElementById("mq-enabled")).not.toBeNull();
    expect(screen.getByTestId("advanced-hidden").textContent).toContain(T("set.mqtt"));
    // Failover is never in the Advanced group: the dashboard links to #failover.
    expect(document.getElementById("failover")).not.toBeNull();
    expect(document.getElementById("failover")?.closest("[hidden]")).toBeNull();
  });

  it("shows them once the switch is on, and remembers the switch", async () => {
    open("/settings?tab=pipeline");
    await screen.findByText(T("settingsUx.groupRoutine"));
    fireEvent.click(screen.getByRole("switch", { name: T("settingsUx.showAdvanced") }));
    expect(advancedCard("set.mqtt")?.hidden).toBe(false);
    expect(screen.queryByTestId("advanced-hidden")).toBeNull();
    expect(window.localStorage.getItem("polyemesis.settings.showAdvanced")).toBe("1");
  });

  it("never hides a card whose saved value is not the default", async () => {
    settings.preview.enabled = false;
    open("/settings?tab=pipeline");
    await screen.findByText(T("settingsUx.groupRoutine"));
    expect(advancedCard("set.preview")?.hidden).toBe(false);
    expect(advancedCard("set.mqtt")?.hidden).toBe(true);
  });

  it("opens a hidden advanced card when the search lands on one of its fields", async () => {
    open("/settings?tab=pipeline");
    await screen.findByText(T("settingsUx.groupRoutine"));
    const box = screen.getByRole("combobox", { name: T("settings.search") });
    fireEvent.change(box, { target: { value: T("set.updateInterval") } });
    fireEvent.keyDown(box, { key: "Enter" });
    await waitFor(() => expect(advancedCard("set.audioMeters")?.hidden).toBe(false));
    // Only that card: the switch is still off.
    expect(advancedCard("set.mqtt")?.hidden).toBe(true);
  });

  it("names the plain-language label for the silent track (settings-council R11)", async () => {
    open("/settings?tab=pipeline");
    expect(await screen.findByLabelText("Add silence when the source has no audio")).toBeTruthy();
    expect(screen.queryByText("Synthesise a silent track")).toBeNull();
  });
});

/* -------------------------------------------- R8, R13: Ingest scope */

describe("Ingest says what it edits (settings-council R8, R13)", () => {
  it("names the programme the form writes to, and marks the ports install-wide", async () => {
    open("/settings?tab=ingest");
    const p = await panel();
    await waitFor(() =>
      expect(within(p).getAllByText(T("settingsUx.scopeProgramme", { name: "Main" })).length).toBeGreaterThan(0),
    );
    expect(within(p).getAllByText(T("settingsUx.scopeInstall")).length).toBeGreaterThan(0);
  });

  it("links degraded-link thresholds to Sources instead of a second editor", async () => {
    open("/settings?tab=ingest");
    const p = await panel();
    await waitFor(() => expect(within(p).getByText(T("settingsUx.degradedOnSources"))).toBeTruthy());
    expect(document.getElementById("ingest-degraded-enabled")).toBeNull();
  });
});

/* ------------------------------------------------ R9: secret.key first */

describe("Security leads with the secret.key backup (settings-council R9)", () => {
  it("puts the data directory card first, with the tour's anchor on it", async () => {
    open("/settings?tab=security");
    const p = await panel();
    const first = await within(p).findByText(T("settingsUx.secretKeyTitle"));
    const titles = [...p.querySelectorAll("[data-slot=card-title]")].map((n) => n.textContent);
    expect(titles[0]).toBe(first.textContent);
    expect(p.querySelector('[data-tour="data-directory"]')?.closest("details")).toBeNull();
  });

  it("remembers the backup acknowledgement in this browser", async () => {
    open("/settings?tab=security");
    fireEvent.click(await screen.findByRole("checkbox", { name: T("settingsUx.backedUp") }));
    expect(window.localStorage.getItem("polyemesis.settings.secretKeyBackedUp")).toBe("1");
  });

  it("shortens the data path but keeps all of it in the title", async () => {
    open("/settings?tab=security");
    const p = await panel();
    await waitFor(() => expect(p.querySelector("code[title]")).not.toBeNull());
    const code = p.querySelector<HTMLElement>("code[title]")!;
    expect(code.title).toBe("/var/lib/polyemesis/a/very/long/path/for/the/data/dir");
    expect(code.textContent).toContain("…");
  });
});

/* -------------------------------------- R1, R2, R10, R16: platform rows */

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

describe("Platforms as rows (settings-council R1, R2, R10, R16)", () => {
  it("lists connected platforms first, one status and one button per row", async () => {
    guides = [guide("twitch", "Twitch"), guide("youtube", "YouTube")];
    creds = [{ platform: "youtube", clientId: "id", hasSecret: true, updatedAt: "" } as PlatformCreds];
    accounts = [{ id: 7, platform: "youtube", accountName: "My channel", accountRef: "UC1" } as PlatformAccount];
    open("/settings?tab=platforms");
    const connected = await screen.findByTestId("platforms-connected");
    await waitFor(() => expect(within(connected).getByText("YouTube")).toBeTruthy());
    expect(within(connected).getByText(T("settingsUx.pillConnected"))).toBeTruthy();
    const add = screen.getByTestId("platforms-add");
    expect(within(add).getByText("Twitch")).toBeTruthy();
    expect(within(add).getByText(T("settingsUx.pillNotSetUp"))).toBeTruthy();
    // No credential form is drawn until a row is opened.
    expect(screen.queryByLabelText(T("set.clientId"))).toBeNull();
  });

  it("opens the platform's card under its row, with the setup guide in plain words", async () => {
    guides = [guide("twitch", "Twitch")];
    open("/settings?tab=platforms");
    fireEvent.click(await screen.findByRole("button", { name: `${T("settingsUx.setUp")}: Twitch` }));
    expect(await screen.findByText(T("settingsUx.setupGuide", { n: 3 }))).toBeTruthy();
    expect(screen.queryByText("Step-by-step setup")).toBeNull();
    // Nothing stored yet: the app-credentials layer opens on its own.
    expect(screen.getByText(T("settingsUx.appCredentials")).closest("details")?.open).toBe(true);
  });

  it("shows the verdict the last check reached after a reload", async () => {
    guides = [guide("twitch", "Twitch")];
    creds = [
      {
        platform: "twitch",
        clientId: "id",
        hasSecret: true,
        updatedAt: "",
        lastCheck: { state: "rejected", method: "client_credentials", detail: "Refused by Twitch.", checkedAt: "2026-10-01T10:00:00Z" },
      } as PlatformCreds,
    ];
    open("/settings?tab=platforms");
    expect(await screen.findByText(T("settingsUx.pillCredsRejected"))).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: `${T("settingsUx.manage")}: Twitch` }));
    expect(await screen.findByText("Refused by Twitch.")).toBeTruthy();
    // A refused pair keeps the credential layer open.
    expect(screen.getByText(T("settingsUx.appCredentials")).closest("details")?.open).toBe(true);
  });

  it("shows one warning: the redirect problem that applies now, ahead of the standing note", async () => {
    guides = [guide("twitch", "Twitch", { note: "Standing caveat.", redirectWarnings: ["This origin will be refused."] })];
    open("/settings?tab=platforms");
    fireEvent.click(await screen.findByRole("button", { name: `${T("settingsUx.setUp")}: Twitch` }));
    const warnings = await screen.findAllByTestId("platform-warning");
    expect(warnings).toHaveLength(1);
    expect(warnings[0].textContent).toContain("This origin will be refused.");
  });

  it("scopes the developer-app banner instead of leading with it as a requirement", async () => {
    open("/settings?tab=platforms");
    expect(await screen.findByText(T("settingsUx.platformsBanner"))).toBeTruthy();
    expect(screen.queryByText(T("set.whyDevApp"), { selector: "[data-slot=card-title]" })).toBeNull();
  });

  it("says why Save credentials is greyed, in words beside it", async () => {
    guides = [guide("twitch", "Twitch")];
    creds = [{ platform: "twitch", clientId: "id", hasSecret: true, updatedAt: "" } as PlatformCreds];
    open("/settings?tab=platforms");
    fireEvent.click(await screen.findByRole("button", { name: `${T("settingsUx.manage")}: Twitch` }));
    const save = await screen.findByRole("button", { name: new RegExp(T("i18nSweep.saveCredentials")) });
    expect(save.hasAttribute("disabled")).toBe(true);
    const reason = document.getElementById(save.getAttribute("aria-describedby") ?? "");
    expect(reason?.textContent).toBe(T("settingsUx.saveCredsNeedsBoth"));
  });

  it("keeps a working pair's credential layer closed, and Show advanced opens it", async () => {
    guides = [guide("twitch", "Twitch")];
    creds = [{ platform: "twitch", clientId: "id", hasSecret: true, updatedAt: "" } as PlatformCreds];
    open("/settings?tab=platforms");
    fireEvent.click(await screen.findByRole("button", { name: `${T("settingsUx.manage")}: Twitch` }));
    expect(screen.getByText(T("settingsUx.appCredentials")).closest("details")?.open).toBe(false);
    cleanup();
    window.localStorage.setItem("polyemesis.settings.showAdvanced", "1");
    open("/settings?tab=platforms");
    fireEvent.click(await screen.findByRole("button", { name: `${T("settingsUx.manage")}: Twitch` }));
    expect(screen.getByText(T("settingsUx.appCredentials")).closest("details")?.open).toBe(true);
  });

  it("keeps the capability table closed until asked for", async () => {
    open("/settings?tab=platforms");
    const show = await screen.findByRole("button", { name: T("settingsUx.showTable") });
    expect(document.getElementById("platform-capability-table")).toBeNull();
    fireEvent.click(show);
    expect(document.getElementById("platform-capability-table")).not.toBeNull();
    fireEvent.click(screen.getByRole("button", { name: T("settingsUx.hideTable") }));
    expect(document.getElementById("platform-capability-table")).toBeNull();
  });
});

describe("the hidden-advanced line (settings-council R7)", () => {
  it("turns the switch on from the line that names what is hidden", async () => {
    open("/settings?tab=pipeline");
    const line = await screen.findByTestId("advanced-hidden");
    fireEvent.click(within(line).getByRole("button", { name: T("settingsUx.showThem") }));
    expect(screen.queryByTestId("advanced-hidden")).toBeNull();
    expect(advancedCard("set.processLogs")?.hidden).toBe(false);
  });

  it("opens the transport details with the switch on", async () => {
    window.localStorage.setItem("polyemesis.settings.showAdvanced", "1");
    open("/settings?tab=security");
    const summary = await screen.findByText(T("settingsUx.tlsDetails"));
    expect(summary.closest("details")?.open).toBe(true);
  });

  it("copies the full data path, not the shortened one", async () => {
    const writeText = vi.fn(() => Promise.resolve());
    Object.assign(navigator, { clipboard: { writeText } });
    open("/settings?tab=security");
    const p = await panel();
    await waitFor(() => expect(p.querySelector("code[title]")).not.toBeNull());
    const card = p.querySelector('[data-tour="data-directory"]') as HTMLElement;
    fireEvent.click(within(card).getByRole("button", { name: T("set.copy") }));
    expect(writeText).toHaveBeenCalledWith("/var/lib/polyemesis/a/very/long/path/for/the/data/dir");
  });
});

