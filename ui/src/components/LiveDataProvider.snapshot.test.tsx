// @vitest-environment jsdom
//
// snapshotKnown MUST BE TRUE OF THE STATUS, NOT OF THE SOCKET.
//
// #663 gates the dashboard's decided empty states on "has a snapshot arrived",
// because `?.` on a null status is indistinguishable from a loaded status
// holding nothing. The first version tracked that with its own useState, set
// inside the socket's `status` case -- which made it a fact about the SOCKET.
//
// Status also arrives over REST, from the api.status() bootstrap. On any load
// where REST wins the race, or where a proxy blocks WebSockets, the flag stayed
// false while status was perfectly well known, and the dashboard hid REAL
// destinations behind a loading card indefinitely. That is worse than the bug
// it was fixing: the original showed the wrong empty state for a moment, this
// showed nothing at all, permanently.
//
// Found by the browser suite -- three tests in live-status-rendering.spec.ts
// that call muteSocket() on purpose, whose helper says "silences the live
// socket so the REST seed below is the only thing the page is told". This is
// that scenario at unit level, so the next change does not need a container
// build to find out.

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";

import { api } from "@/lib/api";
import { useLiveData } from "@/hooks/useLiveData";
import { LiveDataProvider } from "./LiveDataProvider";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

function Probe() {
  const { snapshotKnown, status } = useLiveData();
  return (
    <div>
      <span data-testid="known">{String(snapshotKnown)}</span>
      <span data-testid="dests">{status?.destinations?.length ?? -1}</span>
    </div>
  );
}

describe("snapshotKnown", () => {
  it("is false before anything has been said", async () => {
    // Never resolves: the state a page is in while the first request is in
    // flight, which is the whole reason the flag exists.
    vi.spyOn(api, "status").mockReturnValue(new Promise(() => {}) as ReturnType<typeof api.status>);
    vi.spyOn(api, "listSources").mockResolvedValue([]);
    render(
      <LiveDataProvider>
        <Probe />
      </LiveDataProvider>,
    );
    expect(screen.getByTestId("known").textContent).toBe("false");
  });

  it("becomes true when status arrives over REST with the socket silent", async () => {
    // THE REGRESSION. No socket frame is ever delivered here.
    vi.spyOn(api, "listSources").mockResolvedValue([]);
    vi.spyOn(api, "status").mockResolvedValue({
      destinations: [{ id: 1 }],
      renditions: [],
      relay: { subscribers: [] },
    } as unknown as Awaited<ReturnType<typeof api.status>>);

    render(
      <LiveDataProvider>
        <Probe />
      </LiveDataProvider>,
    );

    await waitFor(() =>
      expect(
        screen.getByTestId("known").textContent,
        "status arrived over REST and snapshotKnown stayed false, so the dashboard would hide real destinations behind a loading card for as long as the socket is silent",
      ).toBe("true"),
    );
    expect(screen.getByTestId("dests").textContent).toBe("1");
  });
});
