// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

vi.mock("@/lib/api", () => ({
  api: {
    listJobs: () =>
      Promise.resolve({ jobs: [{ id: 7, result: { path: "/data/clips/exports/teaser.mp4" } }], stats: {}, paused: false }),
  },
}));

import { OfflineCardSettings, QoEStats } from "./PlayoutPage";

afterEach(cleanup);

describe("OfflineCardSettings", () => {
  it("offers finished clip exports and saves the whole card on Save", async () => {
    const saves: unknown[] = [];
    render(
      <OfflineCardSettings
        offlineMessage=""
        vodUrl=""
        offlineClipJob={0}
        onSave={async (b) => {
          saves.push(b);
        }}
      />,
    );
    const clip = (await screen.findByLabelText("Looping clip")) as HTMLSelectElement;
    await waitFor(() => expect(screen.getByRole("option", { name: "#7 teaser.mp4" })).toBeTruthy());
    fireEvent.change(clip, { target: { value: "7" } });
    fireEvent.change(screen.getByLabelText("Latest VOD link"), { target: { value: "https://example.com/v" } });
    fireEvent.change(screen.getByLabelText("Message"), { target: { value: "Back Sunday" } });
    expect(saves).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Save offline card" }));
    await waitFor(() =>
      expect(saves).toEqual([{ offlineMessage: "Back Sunday", vodUrl: "https://example.com/v", offlineClipJob: 7 }]),
    );
  });
});

describe("QoEStats", () => {
  it("is absent until a player has reported", () => {
    const { container } = render(
      <QoEStats
        qoe={{ beacons: 0, sessions: 0, startups: 0, startupMs: 0, rebuffers: 0, rebufferMs: 0, errors: {}, untracked: 0, active: 0, byVariant: {}, startupHistogram: [], sessionsWithErrors: 0 }}
      />,
    );
    expect(container.textContent).toBe("");
  });

  it("shows mean startup, rebuffers, median latency and errors", () => {
    render(
      <QoEStats
        qoe={{
          beacons: 9, sessions: 2, startups: 2, startupMs: 1800, rebuffers: 3, rebufferMs: 900,
          errors: { network: 1, media: 2 }, untracked: 0, active: 2, byVariant: {}, latencyMsMedian: 8000,
          startupHistogram: [0, 1, 2, 2, 2, 2, 2], sessionsWithErrors: 1,
        }}
      />,
    );
    expect(screen.getByText("0.9 s")).toBeTruthy();
    expect(screen.getByText("8.0 s")).toBeTruthy();
    expect(screen.getAllByText("3")).toHaveLength(2); // three rebuffers, three errors
  });
});
