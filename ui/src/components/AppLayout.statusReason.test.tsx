// @vitest-environment jsdom
//
// B1 in the header: a non-pass ingest chip names its reason, its failing step
// and its as-of time, behind a control beside the chip rather than inside it.
// The chip's own text is pinned elsewhere (e2e/live-status-rendering.spec.ts).

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

let lastError: string | undefined = "listen tcp :1935: bind: address already in use";

vi.mock("@/hooks/useLiveData", () => ({
  useLiveData: () => ({
    status: {
      destinations: [],
      renditions: [],
      ingest: { name: "ingest", kind: "ingest", state: "failed", pid: 0, restarts: 2, uptimeSec: 0, lastError },
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

afterEach(() => cleanup());

function renderChrome() {
  render(
    <MemoryRouter>
      <AppLayout username="admin" onSignOut={() => {}} />
    </MemoryRouter>,
  );
}

describe("the header ingest chip's reason", () => {
  it("opens the reason, failing step and as-of line for a failed ingest", () => {
    lastError = "listen tcp :1935: bind: address already in use";
    renderChrome();
    fireEvent.click(screen.getByRole("button", { name: "Why the ingest is not live" }));
    expect(screen.getByText("listen tcp :1935: bind: address already in use")).toBeTruthy();
    expect(screen.getByText("Failing step: Ingest from the encoder")).toBeTruthy();
    expect(screen.getByText("Time of this reading not recorded")).toBeTruthy();
    // The chip itself still says only the state.
    expect(screen.getByTestId("chrome-ingest-status").textContent).toBe("Failed");
  });

  it("offers no reason control when the server gave no reason", () => {
    lastError = undefined;
    renderChrome();
    expect(screen.queryByRole("button", { name: "Why the ingest is not live" })).toBeNull();
  });
});
