// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { DelayCard, DuckingCard, LoudnessCard, MusicRightsCard } from "./RoutingPage";
import { TooltipProvider } from "@/components/ui/tooltip";
import type { Ducking, TrackAnnotation, TrackRole } from "@/lib/types";

/* Three controls on the routing editor that told the operator nothing, or the
 * wrong thing, about what they were about to do to a live destination. */

const noop = () => {};

afterEach(cleanup);

describe("DelayCard", () => {
  it("names the two delay directions in words, not in translation keys", () => {
    // DELAY_LABEL holds TranslationKeys and they were rendered bare, so the one
    // control that decides WHICH stream is held back offered
    // "route.audioLaterThanVideo" and "route.videoLaterThanAudio". Guessing
    // delays the wrong stream, and saving restarts a live destination.
    render(<DelayCard delayMs={0} videoDelayMs={0} onChange={noop} />);
    const trigger = screen.getByLabelText("Delay direction");
    expect(trigger.textContent).toBe("Audio later than video");
    expect(trigger.textContent).not.toContain("route.");
  });
});

describe("LoudnessCard", () => {
  it("says why the Target box is greyed while a preset is selected", () => {
    // -16 LUFS is one of the named presets, so the Target NumberField is
    // bare-disabled with nothing adjacent connecting it to the select above.
    render(
      <LoudnessCard loudness={{ targetLufs: -16 }} normalize="auto" onChange={noop} />,
    );
    expect(screen.getByTestId("target-locked").textContent).toContain("Custom");
  });

  it("says nothing of the kind once the target is the operator's own", () => {
    // -23.5 matches no preset, so the select is on Custom and the box is live.
    render(
      <LoudnessCard loudness={{ targetLufs: -23.5 }} normalize="auto" onChange={noop} />,
    );
    expect(screen.queryByTestId("target-locked")).toBeNull();
  });
});

describe("DuckingCard", () => {
  const ann = (track: number, role: string) => ({ track, role }) as TrackAnnotation;
  const duck: Ducking = { trigger: [0], target: [1] };

  it("stays on screen for a stored duck the mix cannot show", () => {
    // THE BUG. Excluding the mic role leaves one track in the mix, the old
    // guard returned null below two, and the whole card went with it — while
    // the stored duck kept compiling and kept pulling the music down, with no
    // switch anywhere to turn it off.
    render(
      <DuckingCard
        ducking={duck}
        mixedTracks={[1]}
        allTracks={[0, 1]}
        annotations={[ann(0, "mic"), ann(1, "music")]}
        onChange={noop}
      />,
    );
    expect(screen.getByLabelText("Enable ducking")).toBeTruthy();
  });

  it("keeps the Off switch reachable even with nothing left in the mix to duck", () => {
    render(
      <DuckingCard
        ducking={duck}
        mixedTracks={[]}
        allTracks={[0, 1]}
        annotations={[]}
        onChange={noop}
      />,
    );
    expect(screen.getByLabelText("Enable ducking")).toBeTruthy();
    // And says so, rather than drawing an empty row of chips.
    expect(screen.getByTestId("duck-no-target")).toBeTruthy();
  });

  it("offers nothing where no duck exists and none could be created", () => {
    const { container } = render(
      <DuckingCard
        ducking={null}
        mixedTracks={[0]}
        allTracks={[0]}
        annotations={[]}
        onChange={noop}
      />,
    );
    expect(container.innerHTML).toBe("");
  });
});

describe("MusicRightsCard", () => {
  // The badge states what is being done, so it must agree with the sentence under it.
  const card = (excludeRoles: TrackRole[], musicTracks: number[]) =>
    render(
      <TooltipProvider>
        <MusicRightsCard
          platformName="Twitch"
          excludeRoles={excludeRoles}
          musicTracks={musicTracks}
          onExcludeRoles={noop}
        />
      </TooltipProvider>,
    );

  it("does not say music is excluded while no track is marked as music", () => {
    // THE BUG (docs/media/02-routing.png): setting on, nothing marked, and the
    // card showed "music excluded" over "No ingest track is marked as music yet".
    card(["music"], []);
    expect(screen.queryByText("music excluded")).toBeNull();
    expect(screen.getByText("music exclusion on, nothing to exclude yet")).toBeTruthy();
    expect(screen.getByText(/No ingest track is marked as music yet/)).toBeTruthy();
  });

  it("says music is excluded once a track is marked as music", () => {
    card(["music"], [1]);
    expect(screen.getByText("music excluded")).toBeTruthy();
    expect(screen.getByText(/is marked as music and is not being sent/)).toBeTruthy();
  });

  it("says music is allowed when the setting is off, marked track or not", () => {
    card([], [1]);
    expect(screen.getByText("music allowed")).toBeTruthy();
    cleanup();
    card([], []);
    expect(screen.getByText("music allowed")).toBeTruthy();
    expect(screen.queryByText("music excluded")).toBeNull();
  });
});
