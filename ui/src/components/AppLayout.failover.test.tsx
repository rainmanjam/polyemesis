// @vitest-environment jsdom
//
// THE HEADER MUST NOT CALL THE INGEST "OFFLINE" WHILE THE BACKUP IS ON AIR.
//
// A real install, primary encoder dropped, standby carrying the show: the
// dashboard said "ON AIR 6/6 destinations live" and, in the very same header,
// "Ingest Offline". True of the primary, and a contradiction of everything else
// on the page -- an operator reads it as "the stream is down".

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { translate } from "@/lib/i18n";

let failoverActive: "primary" | "backup" = "backup";

vi.mock("@/hooks/useLiveData", () => ({
  useLiveData: () => ({
    status: {
      destinations: [],
      renditions: [],
      failover: {
        active: failoverActive,
        switchedAt: "2026-09-09T10:00:00Z",
        switches: 1,
        primaryLive: false,
        backupLive: true,
        backupEnabled: true,
        slateEnabled: true,
      },
    },
    connected: true,
    frameError: false,
    source: null,
    bitrate: [],
    levels: null,
    system: null,
    logs: [],
    programme: null,
    programmeKnown: true,
    snapshotKnown: true,
    sourceCount: 0,
    programmes: [],
    selectProgramme: () => {},
    recordingsRevision: 0,
  }),
  useIngestLive: () => false,
}));

import { AppLayout } from "./AppLayout";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

function chip(): string {
  render(
    <MemoryRouter>
      <AppLayout username="admin" onSignOut={() => {}} />
    </MemoryRouter>,
  );
  return screen.getByTestId("chrome-ingest-status").textContent ?? "";
}

describe("the header ingest chip during a failover", () => {
  it("says the backup is live rather than Offline while the backup is on air", () => {
    failoverActive = "backup";
    const text = chip();
    expect(text).not.toBe("Offline");
    expect(text).toBe(translate("en", "chrome.ingestBackup"));
  });

  it("still says Stopped when nothing is on air from the backup", () => {
    // Stopped, not Offline: an absent ingest uses the console's one word for
    // "not running" (app-council A91).
    failoverActive = "primary";
    expect(chip()).toBe("Stopped");
  });
});
