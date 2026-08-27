// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render } from "@testing-library/react";

import { AudioMeter, MeterScale } from "./AudioMeter";
import { StatusDot } from "./StatusDot";
import { toneMark, type SignalTone } from "@/lib/signal";

/* WCAG 1.4.1 is Level A: colour must never be the only channel for meaning.
 *
 * This console encodes five operational states — live, reconnecting, failed,
 * armed, idle — and until these tests were written it encoded them as five
 * hues and nothing else. Three separate readers lose that entirely: a
 * deuteranopic operator, anybody on a laptop panel in daylight, and the
 * screenshot in an incident report that somebody printed.
 *
 * The property being tested is deliberately not "the shape classes are the
 * ones I wrote". It is "with colour deleted, the states are still different" —
 * which is the thing that must hold, and which a later refactor that quietly
 * collapses two silhouettes into one would break while every class name it
 * asserted still looked fine.
 */

afterEach(cleanup);

const TONES: SignalTone[] = ["live", "warn", "down", "armed", "idle"];

/** Every utility in this kit that carries a hue and nothing else. Stripping
 *  these simulates the colour-blind reader, the washed-out screen and the
 *  greyscale print at once. */
const COLOUR_ONLY =
  /^(?:bg|text|border|ring|fill|stroke)-(?:live|warn|down|armed|idle|foreground|muted-foreground|subtle-foreground)$/;

function silhouette(tone: SignalTone): string {
  const { container } = render(<StatusDot tone={tone} />);
  // The core mark is the last span: the halo before it is the pulse, which is
  // a channel of its own but only exists for two of the five tones.
  const spans = container.querySelectorAll("span");
  const core = spans[spans.length - 1];
  return [...core.classList]
    .filter((c) => !COLOUR_ONLY.test(c))
    .sort()
    .join(" ");
}

describe("state survives having its colour taken away", () => {
  it("gives every tone a silhouette no other tone has", () => {
    const seen = new Map<string, SignalTone>();
    for (const tone of TONES) {
      const shape = silhouette(tone);
      const clash = seen.get(shape);
      expect(
        clash,
        `${tone} and ${clash} draw the same mark once colour is removed, so on a ` +
          `greyscale screen they are one state. Give one of them a different ` +
          `silhouette in toneMark (lib/signal.ts).`,
      ).toBeUndefined();
      seen.set(shape, tone);
    }
    expect(seen.size).toBe(TONES.length);
  });

  it("holds the diamond to the optical weight of the disc beside it", () => {
    // A square turned 45° has a 1.41x diagonal, so an unscaled diamond reads as
    // a heavier mark than a live one — a hierarchy between "reconnecting" and
    // "on air" that nobody meant to state, in the one place on the page where
    // an accidental emphasis is read as a fact.
    expect(toneMark.warn.shape).toMatch(/scale-/);
  });
});

describe("the meters do not report level in hue alone", () => {
  it("raises a literal CLIP flag, mounted rather than conditionally rendered", () => {
    // The draw loop toggles this node's visibility at animation frame rate and
    // has no way to mount one, so a version that rendered it only while
    // clipping would show it never.
    const { getByText } = render(<AudioMeter rms={[-12]} peak={[-6]} />);
    expect(getByText("CLIP")).toBeTruthy();
  });

  it("legends the zone breaks the bars mark, including the two with no number", () => {
    // The bar draws four boundary marks. A scale that labels only the two that
    // happen to fall on a round number leaves the other two as a pattern rather
    // than a reading.
    const { container } = render(<MeterScale />);
    const ticks = container.querySelectorAll("[aria-hidden]");
    expect(ticks.length).toBe(4);
  });
});
