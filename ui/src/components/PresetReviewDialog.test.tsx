// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import { PresetReviewDialog, type PresetReview } from "./PresetReviewDialog";
import type { RoutingProfile } from "@/lib/types";

/* THE PRESET IS SHOWN BEFORE IT LANDS (A14). A preset rewrites the whole mix,
 * so the operator sees the tracks it keeps, drops and adds, and the
 * destination it changes, with Back to edit as a real way out. */
afterEach(cleanup);

const profile = (enabled: boolean[]): RoutingProfile => ({
  mode: "simple",
  tracks: enabled.map((on, track) => ({ track, enabled: on, gain: 1 })),
  matrix: null,
  normalize: "auto",
  sampleRate: 48000,
});

const review = (resolve: (ok: boolean) => void): PresetReview => ({
  presetId: "mic-only",
  mix: "live",
  before: profile([true, true, true]),
  after: profile([true, false, false]),
  resolve,
});

describe("PresetReviewDialog", () => {
  it("lists tracks in, tracks leaving and the destination changed", () => {
    render(<PresetReviewDialog review={review(() => {})} presetName="Mic only" destinationName="Twitch" live />);
    const box = screen.getByTestId("preset-review").textContent ?? "";
    expect(box).toContain("Apply “Mic only” to Twitch?");
    expect(box).toMatch(/Tracks in the mix\s*1/);
    expect(box).toMatch(/Tracks leaving\s*2, 3/);
    expect(box).toMatch(/Destination changed\s*Twitch/);
    expect(box).toContain("Twitch is enabled");
  });

  it("Back to edit declines and Apply accepts", () => {
    const resolve = vi.fn();
    render(<PresetReviewDialog review={review(resolve)} presetName="Mic only" destinationName="Twitch" live={false} />);
    fireEvent.click(screen.getByRole("button", { name: "Back to edit" }));
    expect(resolve).toHaveBeenLastCalledWith(false);
    fireEvent.click(screen.getByRole("button", { name: "Apply preset" }));
    expect(resolve).toHaveBeenLastCalledWith(true);
  });
});
