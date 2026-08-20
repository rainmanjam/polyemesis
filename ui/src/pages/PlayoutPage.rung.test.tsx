// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { VariantsCard } from "./PlayoutPage";
import type { PlayoutSettings, PlayoutVariantStatus } from "@/lib/types";

/* Removing a ladder rung dropped every viewer on it the instant an unlabelled
 * trash icon was clicked -- on an install where deleting a recording that can
 * be downloaded again demands typing its filename. */

const play = (): PlayoutSettings =>
  ({
    enabled: true,
    public: false,
    allowCrossOrigin: false,
    format: "hls",
    segmentSeconds: 4,
    playlistSegments: 6,
    dvrWindowSeconds: 0,
    maxDiskMb: 1024,
    audioKbps: 128,
    sessionIdleSeconds: 30,
    maxSessions: 100,
    variants: [
      { name: "rung1", enabled: true, renditionId: null, audioTrack: 0 },
      { name: "rung2", enabled: true, renditionId: null, audioTrack: 0 },
    ],
  }) as PlayoutSettings;

const status = (viewers: number): PlayoutVariantStatus[] => [
  {
    name: "rung1",
    audioTrack: 0,
    running: true,
    bandwidth: 3_000_000,
    playlist: "rung1.m3u8",
    viewers,
  },
];

afterEach(cleanup);

describe("VariantsCard", () => {
  it("does not remove a rung on the click itself", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    render(
      <VariantsCard
        play={play()}
        variants={status(37)}
        renditions={[]}
        busy={false}
        onSave={onSave}
      />,
    );

    fireEvent.click(screen.getByLabelText("Remove rung1"));

    expect(onSave).not.toHaveBeenCalled();
  });

  it("asks first, naming the rung and the viewers the row is already showing", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    render(
      <VariantsCard
        play={play()}
        variants={status(37)}
        renditions={[]}
        busy={false}
        onSave={onSave}
      />,
    );

    fireEvent.click(screen.getByLabelText("Remove rung1"));

    const dialog = await screen.findByRole("dialog");
    expect(dialog.textContent).toContain("rung1");
    // The count that makes this a decision rather than a click.
    expect(dialog.textContent).toContain("37");

    fireEvent.click(screen.getByRole("button", { name: "Remove rung" }));

    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
    const next = onSave.mock.calls[0][0] as PlayoutSettings;
    expect(next.variants.map((v) => v.name)).toEqual(["rung2"]);
  });

  it("leaves the ladder alone when the dialog is cancelled", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    render(
      <VariantsCard
        play={play()}
        variants={status(0)}
        renditions={[]}
        busy={false}
        onSave={onSave}
      />,
    );

    fireEvent.click(screen.getByLabelText("Remove rung2"));
    await screen.findByRole("dialog");
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));

    expect(onSave).not.toHaveBeenCalled();
  });
});
