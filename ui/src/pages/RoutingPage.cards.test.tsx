// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { DelayCard, DuckingCard, LoudnessCard } from "./RoutingPage";
import type { Ducking, TrackAnnotation } from "@/lib/types";

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
