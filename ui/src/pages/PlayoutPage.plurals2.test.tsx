// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { VariantsCard } from "./PlayoutPage";
import { setLanguage } from "@/lib/i18n";
import type { PlayoutSettings, PlayoutVariantStatus } from "@/lib/types";

/* The viewer badge on a ladder rung. Rendered in French, where the noun takes
 * the count: one viewer on a rung read "1 spectateurs". */

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
    variants: [{ name: "rung1", enabled: true, renditionId: null, audioTrack: 0 }],
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

function rung(viewers: number) {
  render(
    <VariantsCard play={play()} variants={status(viewers)} renditions={[]} busy={false} onSave={vi.fn()} />,
  );
}

afterEach(() => {
  cleanup();
  setLanguage("en");
});

describe("VariantsCard: stats.watching on a rung (fr)", () => {
  it("says 1 spectateur, not 1 spectateurs, for one viewer on the rung", () => {
    setLanguage("fr");
    rung(1);
    expect(screen.getByText("1 spectateur")).toBeTruthy();
    expect(screen.queryByText("1 spectateurs")).toBeNull();
  });

  it("says 2 spectateurs for two viewers on the rung", () => {
    setLanguage("fr");
    rung(2);
    expect(screen.getByText("2 spectateurs")).toBeTruthy();
  });
});
