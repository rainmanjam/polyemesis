// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import type { Settings } from "@/lib/types";

/* FOUR SETTINGS THE SERVER READS AND NO OPERATOR COULD WRITE.
 *
 * Every one of them was already stored, already sent, and already consumed --
 * the engine reads them, the validator bounds them, docs/HOT-RELOAD.md lists
 * them. What was missing was the last hop: an input on a page. A field in
 * types.ts is not a control, and that is exactly the gap these tests exist to
 * hold shut, so each one drives the RENDERED page and asserts the value reaches
 * the PUT rather than asserting that a key exists:
 *
 *   #775 display.timeZone         -- lib/format.ts has drawn every clock in the
 *                                    console through it since it was written,
 *                                    and api.ts pushes it in on every read and
 *                                    every save. With no producer an install
 *                                    could only ever read UTC.
 *   #781 chat.purgeMinutes        -- when a deleted message actually leaves the
 *                                    database, which is a subject-access answer.
 *   #781 chat.youtubeQuotaReserve -- the slice held back so a moderator can
 *                                    still SPEAK after a day of polling.
 *   #779 failover.slate.{videoKbps,encoder,preset}
 *                                 -- all three land in an FFmpeg argv.
 *   #782 multitrack.gpus[].sharedSystemMemory
 *                                 -- the sixth field of six, sent to Twitch at
 *                                    go-live beside the five that had inputs.
 *
 * The whole page is rendered rather than a card, because "reachable" is a claim
 * about the page: a control mounted behind a tab nobody can open, or under a
 * switch that is off, is still unreachable. Getting to it here the way an
 * operator does is the assertion.
 *
 * Plain functions rather than `vi.fn()` for the API, following
 * ClipsPage.readState.test.tsx. */

const put: Settings[] = [];

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
    recording: {
      enabled: false,
      segmentSeconds: 600,
      maxGb: 50,
      maxAgeHours: 168,
      minFreeGb: 5,
    },
    preview: {
      enabled: true,
      segmentSeconds: 2,
      videoHeight: 360,
      videoKbps: 800,
      idleTimeoutSeconds: 30,
    },
    meters: { enabled: true, intervalMs: 100 },
    logging: { persistProcessLogs: true, maxFileMb: 10, maxFiles: 5 },
    destinations: { staggerMs: 0 },
    chat: {
      retentionHours: 2,
      keepMessages: 2000,
      purgeMinutes: 5,
      historyMessages: 500,
      youtubeQuotaUnits: 10000,
      youtubeQuotaReserve: 200,
    },
    alerts: { retryAttempts: 4 },
    // Failover ON and the slate ON, because the slate's fields are rendered
    // under both switches. An operator with failover off cannot reach them and
    // does not need to; one with a slate configured is exactly who #779 is
    // about.
    failover: {
      enabled: true,
      graceSeconds: 10,
      return: "manual",
      slate: { enabled: true, imagePath: "", color: "black" },
    },
    // One declared GPU, for the same reason: the per-GPU fields exist only
    // inside a row, and "Add a GPU" is the operator's way to one.
    multitrack: { gpus: [{ model: "NVIDIA GeForce RTX 4070", vendorId: 4318 }] },
  } as Settings;
}

let settings = baseSettings();

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      getSettings: () => Promise.resolve(settings),
      listSources: () => Promise.resolve([{ id: 1, name: "Studio" }]),
      system: () => Promise.resolve({ version: "test", ffmpeg: {} }),
      putSettings: (next: Settings) => {
        put.push(next);
        return Promise.resolve(next);
      },
      automodMatrix: () =>
        Promise.resolve({ platforms: [], actions: [], checkers: [], cells: [] }),
      completeTour: () => Promise.resolve(),
    },
  };
});

vi.mock("sonner", () => ({
  toast: { success: () => {}, error: () => {}, warning: () => {}, info: () => {} },
}));

const { SettingsPage } = await import("./SettingsPage");
const { translate } = await import("@/lib/i18n");

const T = (key: Parameters<typeof translate>[1]) => translate("en", key);

beforeEach(() => {
  settings = baseSettings();
  put.length = 0;
});

afterEach(cleanup);

/** The Pipeline tab, mounted the way the router mounts it. */
async function openPipeline() {
  render(
    <MemoryRouter initialEntries={["/settings?tab=pipeline"]}>
      <SettingsPage />
    </MemoryRouter>,
  );
  await waitFor(() => expect(screen.getByText(T("set.chatHistory"))).toBeTruthy());
}

/** Type into a field found by its label, exactly as an operator would. */
function type(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

/** Press the tab's one Save and return the document it PUT. */
async function save(): Promise<Settings> {
  const button = screen.getByRole("button", { name: T("set.savePipeline") });
  expect((button as HTMLButtonElement).disabled).toBe(false);
  fireEvent.click(button);
  await waitFor(() => expect(put.length).toBe(1));
  return put[0];
}

describe("#775 the display time zone", () => {
  it("is an input on the page, and what is typed into it reaches the save", async () => {
    await openPipeline();
    type(T("set.displayTimeZone"), "Australia/Sydney");
    expect((await save()).display?.timeZone).toBe("Australia/Sydney");
  });

  it("says so when this browser cannot resolve the zone, and does not block the save", async () => {
    await openPipeline();
    type(T("set.displayTimeZone"), "Mars/Olympus_Mons");
    expect(screen.getByText(T("set.displayTimeZoneUnknown"))).toBeTruthy();
    // Warned, not refused: the browser's zone database and the server's are
    // different databases, so the Save has to stay live. The server is what
    // actually decides.
    expect((await save()).display?.timeZone).toBe("Mars/Olympus_Mons");
  });

  it("says nothing of the kind about a zone that resolves, or about an empty field", async () => {
    await openPipeline();
    expect(screen.queryByText(T("set.displayTimeZoneUnknown"))).toBeNull();
    type(T("set.displayTimeZone"), "Europe/London");
    // Empty is UTC rather than unknown -- lib/format.ts makes the same call,
    // and a default install marked as broken would be a worse bug than the one
    // being fixed.
    expect(screen.queryByText(T("set.displayTimeZoneUnknown"))).toBeNull();
  });
});

describe("#781 the chat fields that had no control", () => {
  it("sends a changed purge interval", async () => {
    await openPipeline();
    type(T("set.chatPurge"), "60");
    expect((await save()).chat?.purgeMinutes).toBe(60);
  });

  it("sends a changed YouTube quota reserve", async () => {
    await openPipeline();
    type(T("set.chatYouTubeReserve"), "500");
    expect((await save()).chat?.youtubeQuotaReserve).toBe(500);
  });

  it("carries every sibling chat field through an edit to one of them", async () => {
    // chatFrom() exists for this: a handler that rebuilt the object field by
    // field would drop whichever field it predates, and the save would come
    // back refused for something the operator never touched.
    await openPipeline();
    type(T("set.chatPurge"), "7");
    const chat = (await save()).chat;
    expect(chat).toEqual({
      retentionHours: 2,
      keepMessages: 2000,
      purgeMinutes: 7,
      historyMessages: 500,
      youtubeQuotaUnits: 10000,
      youtubeQuotaReserve: 200,
    });
  });

  it("offers the reserve no further than half the allowance, which is where the server refuses", async () => {
    await openPipeline();
    const reserve = screen.getByLabelText(T("set.chatYouTubeReserve")) as HTMLInputElement;
    expect(reserve.max).toBe("5000");
    // And it follows the allowance rather than being a constant, so an operator
    // granted a million units is not held to the default project's half.
    type(T("set.chatYouTubeQuota"), "1000000");
    expect(
      (screen.getByLabelText(T("set.chatYouTubeReserve")) as HTMLInputElement).max,
    ).toBe("500000");
  });
});

describe("#779 the standby slate's own encode", () => {
  it("sends the bitrate, the encoder and the preset", async () => {
    await openPipeline();
    type(T("set.slateBitrate"), "2500");
    type(T("set.slateEncoder"), "h264_nvenc");
    type(T("set.slatePreset"), "p4");
    const slate = (await save()).failover?.slate;
    expect(slate?.videoKbps).toBe(2500);
    expect(slate?.encoder).toBe("h264_nvenc");
    expect(slate?.preset).toBe("p4");
  });

  it("keeps the image and the colour it did not touch", async () => {
    // The three new fields spread the existing slate rather than rebuilding it,
    // for the reason chatFrom() exists: an edit to one must not silently blank
    // the others.
    await openPipeline();
    type(T("set.slateEncoder"), "libx264");
    const slate = (await save()).failover?.slate;
    expect(slate?.enabled).toBe(true);
    expect(slate?.color).toBe("black");
  });
});

describe("#782 the declared GPU's shared system memory", () => {
  it("is an input beside the other five, and reaches the save", async () => {
    await openPipeline();
    type("Shared system memory (bytes)", "17179869184");
    const gpu = (await save()).multitrack?.gpus?.[0];
    expect(gpu?.sharedSystemMemory).toBe(17179869184);
    // The five that always had inputs are still on the same declaration: this
    // is one object sent to Twitch, and a shared-memory edit that dropped the
    // vendor ID would be refused by name at go-live.
    expect(gpu?.model).toBe("NVIDIA GeForce RTX 4070");
    expect(gpu?.vendorId).toBe(4318);
  });
});
