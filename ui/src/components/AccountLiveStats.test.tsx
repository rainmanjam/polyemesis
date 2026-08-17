import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";

import { ViewerReadoutLine } from "./AccountLiveStats";
import { viewerReadout } from "@/lib/viewerCount";
import type { AccountStats } from "@/lib/types";

/* THE ASSERTION THAT MATTERS: a live stream whose viewer count was withheld
 * must not read as an audience of nobody.
 *
 * The unit test next to viewerReadout proves the branch is taken. This proves
 * what a person actually SEES, which is the thing that was wrong — a correct
 * discriminated union still renders a zero if the component writes
 * `{stats.viewerCount}` into the markup.
 *
 * Rendered with renderToStaticMarkup rather than a DOM library: vitest runs
 * this suite in `environment: "node"`, there is no jsdom in the tree, and the
 * question here is what text comes out, which a string answers exactly. */

/** Everything a person would read, with the markup taken away.
 *
 *  Tag-stripping is the point: class names carry digits (`text-[10px]`) and a
 *  test that searched the raw HTML for "0" would pass or fail on Tailwind. Only
 *  what lands between the tags is what an operator sees. */
function visibleText(node: React.ReactElement): string {
  return renderToStaticMarkup(node)
    .replace(/<[^>]*>/g, " ")
    .replace(/\s+/g, " ")
    .trim();
}

function render(res: AccountStats): string {
  return visibleText(<ViewerReadoutLine readout={viewerReadout(res)} />);
}

describe("ViewerReadoutLine", () => {
  it("renders nothing a person would read as zero when a live count is absent", () => {
    // live:true, and the viewerCount key ABSENT — the exact payload YouTube
    // produces when the owner has hidden their count, and the one where a false
    // zero tells a streamer with an audience that nobody is watching.
    const text = render({ supported: true, stats: { live: true, source: "/streams" } });

    // The stream is live and it says so.
    expect(text).toContain("Live");
    // And it says the number was not given, in words.
    expect(text).toContain("Viewer count not reported");

    // NOT A ZERO, AND NOT ANYTHING ELSE THAT READS AS ONE. No digit at all
    // appears: not 0, not a "0 viewers", not a count of any kind.
    expect(text).not.toMatch(/\d/);
    // A dash standing where the number goes is the same lie in punctuation —
    // "—" beside "Live" reads as "none" to everyone who has used a dashboard.
    expect(text).not.toMatch(/[—–]/);
    expect(text).not.toMatch(/\bnone\b/i);
  });

  it("renders a reported zero as 0", () => {
    // The mirror of the case above, and the reason "just never show a number
    // unless it is above zero" is not the fix. Kick documents 0 as a real value
    // its API sends; when the platform says zero, the panel says zero.
    const text = render({
      supported: true,
      stats: { live: true, viewerCount: 0, source: "/streams" },
    });

    expect(text).toContain("Live");
    expect(text).toContain("0");
    expect(text).not.toContain("not reported");
  });

  it("renders a real count", () => {
    const text = render({
      supported: true,
      stats: { live: true, viewerCount: 1312, source: "/streams" },
    });

    expect(text).toContain("Live");
    // Grouped for legibility by Intl, so the assertion is on the digits rather
    // than on one locale's separator.
    expect(text.replace(/[^\d]/g, "")).toContain("1312");
    expect(text).not.toContain("not reported");
  });

  it("renders offline as offline, with no count at all", () => {
    const text = render({ supported: true, stats: { live: false, source: "/streams" } });

    expect(text).toContain("Offline");
    // Offline is a normal answer, not a zero-viewer broadcast.
    expect(text).not.toMatch(/\d/);
    expect(text).not.toContain("Live");
  });

  it("shows the server's reason when the platform cannot be asked", () => {
    // 200 with supported:false, not a 404 and not an error: "we cannot ask" and
    // "the account is gone" are different problems with different fixes, and
    // the sentence names the platform so an operator stops waiting for a number
    // that is never coming.
    const text = render({
      supported: false,
      reason: "polyemesis does not read a viewer count from facebook",
    });

    expect(text).toContain("polyemesis does not read a viewer count from facebook");
    expect(text).toContain("No viewer count");
    expect(text).not.toMatch(/\d/);
    expect(text).not.toContain("Live");
  });

  it("says so when the read itself failed, rather than going quiet", () => {
    const text = visibleText(<ViewerReadoutLine readout={{ kind: "unreadable" }} />);
    expect(text).toContain("Could not read the viewer count.");
    expect(text).not.toMatch(/\d/);
  });

  it("does not animate its arrival", () => {
    // docs/DESIGN-SYSTEM.md:104 names the viewer count as the example: a count
    // changes because reality changed, and easing it in adds latency to the one
    // number the operator is watching. Asserted against the raw markup, since
    // this one IS about the class names.
    const html = renderToStaticMarkup(
      <ViewerReadoutLine readout={{ kind: "count", count: 1312 }} />,
    );
    expect(html).not.toMatch(/animate-|transition|duration-|\banimation\b/);
  });

  it("paints the two neutral cases with no signal colour", () => {
    // The five saturated tokens mean the state of a destination.
    // "polyemesis cannot ask this platform" and "the poll failed" are
    // properties of the READ, so colouring either amber would put them in a
    // vocabulary that already means something else — Experimental.tsx's rule.
    const unsupported = renderToStaticMarkup(
      <ViewerReadoutLine
        readout={{ kind: "unsupported", reason: "polyemesis does not read a viewer count from x" }}
      />,
    );
    const unreadable = renderToStaticMarkup(<ViewerReadoutLine readout={{ kind: "unreadable" }} />);
    for (const html of [unsupported, unreadable]) {
      expect(html).not.toMatch(/text-live|text-warn|text-down|text-armed/);
      expect(html).not.toMatch(/bg-live|bg-warn|bg-down|bg-armed/);
    }
  });

  it("uses tokens for its colours rather than a hand-written class", () => {
    // DestinationCard.tsx:302 records what a hand-written class costs: there is
    // no `text-ok` token, so a healthy backup rendered invisible. Everything
    // here resolves through toneForState/toneBadge, which can only produce the
    // five that exist.
    const html = renderToStaticMarkup(<ViewerReadoutLine readout={{ kind: "count", count: 7 }} />);
    expect(html).not.toMatch(/text-ok|bg-ok|text-green|text-red|text-success/);
    expect(html).toMatch(/text-live/);
  });
});
