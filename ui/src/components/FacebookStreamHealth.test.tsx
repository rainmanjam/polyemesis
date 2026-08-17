import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";

import { FacebookStreamHealthPanel } from "./FacebookStreamHealth";
import { formatHealthValue } from "@/lib/stream-health";
import type { HealthState } from "@/hooks/useFacebookStreamHealth";

/* THIS PANE HAD NO RENDER TEST AND ITS "NOT REPORTED" COULD BE MUTATED TO 0
 * WITHOUT FAILING ANYTHING.
 *
 * The unit tests beside stream-health.ts prove the helpers behave. They cannot
 * prove what a person sees, and that is where the defect this whole round is
 * about actually lands: a correct helper still shows a zero if the component
 * writes `{row.value}` into the markup, or if somebody "tidies" the words away.
 *
 * The consequence is specific and expensive. Facebook describing an ingest
 * stream while sending no numbers with it is normal — it happens for the first
 * four seconds of every broadcast, by Facebook's own documented timeout. A 0
 * there tells a streamer with a healthy 6 Mbps feed that their encoder has
 * stopped, and their next move is to restart something that was working.
 *
 * renderToStaticMarkup rather than a DOM library, matching
 * AccountLiveStats.test.tsx: this suite runs in `environment: "node"`, there is
 * no jsdom in the tree, and the question is what text comes out. */

/** Everything a person would read, markup removed.
 *
 *  Tag-stripping is load-bearing: class names carry digits (`text-[10px]`,
 *  `text-[11px]`), so a test searching raw HTML for "0" would pass or fail on
 *  Tailwind rather than on the measurement. */
function visibleText(node: React.ReactElement): string {
  return renderToStaticMarkup(node)
    .replace(/<[^>]*>/g, " ")
    .replace(/\s+/g, " ")
    .trim();
}

const render = (state: HealthState) =>
  visibleText(<FacebookStreamHealthPanel state={state} />);

describe("FacebookStreamHealthPanel", () => {
  it("shows words, not a zero, for a stream Facebook described with no numbers", () => {
    // The four-second case: Facebook names the ingest stream and has not yet
    // measured it. Nothing is wrong and nothing should look wrong.
    const out = render({ kind: "ok", streams: [{ id: "s-1", health: {} }] });

    // No digit anywhere is the assertion that matters: any digit in this state
    // is a number Facebook did not send.
    expect(out).not.toMatch(/\d/);
    // NO DASH ASSERTION HERE, DELIBERATELY, AND THE REASON IS WORTH KEEPING.
    // AccountLiveStats.test.tsx forbids "—" outright because its output is
    // three words and any dash in it would be standing in for a number. This
    // pane's output is a paragraph -- it explains that Twitch, YouTube and Kick
    // publish no ingest numbers at all -- and that sentence contains a
    // legitimate, space-delimited em dash. Two attempts at a positional regex
    // both failed on correct prose, because once the markup is flattened there
    // is nothing left to distinguish "— as a value" from "— in a sentence".
    // A rule that cannot be stated precisely is better dropped than weakened
    // until it passes: the digit assertion above is the one with teeth, and it
    // is exact.
    expect(out.toLowerCase()).not.toContain("none");
    // And it must actually say something, rather than passing by rendering
    // nothing at all.
    expect(out.toLowerCase()).toContain("not reported");
  });

  it("shows a measurement Facebook DID send, including a genuine zero", () => {
    // The inverse, and equally required. A zero Facebook actually measured is
    // the most useful number on a stalled ingest — hiding it behind "not
    // reported" would be the same lie pointed the other way.
    const out = render({
      kind: "ok",
      streams: [{ id: "s-1", health: { video_bitrate: 0, framerate: 30 } }],
    });

    expect(out).toContain("0");
    expect(out).toContain("30");
  });

  it("never prints a real, small measurement as 0", () => {
    // formatHealthValue used to round to two places and strip trailing zeros,
    // so 0.001 became "0.00" became "0" — a live measurement rendered as the
    // number that means dead. Asserted at the helper AND here, because the
    // helper being right does not prove the component calls it.
    expect(formatHealthValue(0.001)).not.toBe("0");
    expect(formatHealthValue(0.004)).not.toBe("0");

    const out = render({
      kind: "ok",
      streams: [{ id: "s-1", health: { video_bitrate: 0.001 } }],
    });
    // The pane must not claim zero for a stream that is measurably alive.
    expect(out).not.toMatch(/(^|\s)0(\s|$)/);
  });

  it("states an empty stream list rather than rendering an empty box", () => {
    // A scheduled broadcast has no ingest yet and an ended one has none any
    // more. Silence would read as a broken pane.
    expect(render({ kind: "ok", streams: [] }).length).toBeGreaterThan(0);
  });

  it("says the platform publishes none, without implying polyemesis failed", () => {
    const out = render({ kind: "unsupported" }).toLowerCase();
    expect(out.length).toBeGreaterThan(0);
    // Facebook is the only platform here that publishes encoder health at all,
    // so this state is a fact about the platform. It must not read as an error.
    expect(out).not.toContain("error");
    expect(out).not.toContain("failed");
  });
});
