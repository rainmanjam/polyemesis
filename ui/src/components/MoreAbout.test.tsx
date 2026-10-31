// @vitest-environment jsdom

import { readFileSync } from "node:fs";
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { MoreAbout } from "./MoreAbout";
import en from "@/lib/i18n/en.json";

/* app-council A84: an explanation is one plain line by default, and the full
 * paragraph is one click away.
 *
 * Two halves. The component: the lead is visible, the long text sits inside a
 * <details> that starts closed. And the adoption: each paragraph the council
 * named as shown inline by default on a routine screen is now rendered only as
 * the body of a MoreAbout with a lead, so it cannot quietly go back to being
 * the first thing an operator reads. */

afterEach(cleanup);

describe("MoreAbout", () => {
  it("shows the lead line and keeps the long text in a closed disclosure", () => {
    const { container } = render(
      <MoreAbout lead="Set both or neither.">A long paragraph about mux queues.</MoreAbout>,
    );
    expect(screen.getByText("Set both or neither.")).toBeTruthy();
    const details = container.querySelector("details");
    expect(details).not.toBeNull();
    expect(details?.open).toBe(false);
    expect(details?.textContent).toContain("A long paragraph about mux queues.");
    // The lead is outside the disclosure, so it is read without a click.
    expect(details?.textContent).not.toContain("Set both or neither.");
  });

  it("without a lead renders only the disclosure, the Settings card pattern", () => {
    const { container } = render(<MoreAbout>Body</MoreAbout>);
    expect(container.firstElementChild?.tagName).toBe("DETAILS");
  });
});

const source = (rel: string) => readFileSync(new URL(rel, import.meta.url), "utf8");

/* [file, long key, lead key]. The long key must appear as MoreAbout's body with
 * that lead, and nowhere as a bare inline paragraph. */
const ADOPTED: Array<[string, string, string]> = [
  ["./DestinationDialog.tsx", "dest.muxQueuePair", "dest.muxQueuePairLead"],
  ["./DestinationDialog.tsx", "addDest.multitrackHelp", "addDest.multitrackLead"],
  ["./DestinationDialog.tsx", "addDest.giveUpHelp", "addDest.giveUpLead"],
  ["../pages/SettingsPage.tsx", "set.slateEncoderNote", "set.slateEncoderLead"],
  ["../pages/SettingsPage.tsx", "set.synthesiseNote", "set.synthesiseLead"],
  ["../pages/SettingsPage.tsx", "set.ebGpuWhy", "set.ebGpuLead"],
  ["../pages/MonitoringPage.tsx", "mon.expertIntro", "mon.expertLead"],
];

describe("the long explanations named by A84 sit behind MoreAbout", () => {
  it.each(ADOPTED)("%s renders %s only behind its one-line lead", (file, long, lead) => {
    const text = source(file);
    const at = text.indexOf(`"${long}"`);
    expect(at, `${long} is not rendered in ${file}`).toBeGreaterThan(-1);
    // Exactly one use, and it is inside <MoreAbout lead={t("<lead>")}>…</MoreAbout>.
    expect(text.split(`"${long}"`).length - 1).toBe(1);
    const open = text.lastIndexOf("<MoreAbout", at);
    const close = text.indexOf("</MoreAbout>", at);
    expect(open, `${long} is not inside a MoreAbout`).toBeGreaterThan(-1);
    expect(text.slice(open, at)).toContain(`lead={t("${lead}")}`);
    expect(text.slice(open, at)).not.toContain("</MoreAbout>");
    expect(close).toBeGreaterThan(at);
  });

  it("each lead is one sentence-sized line, well under the long text it fronts", () => {
    const cat = en as Record<string, string>;
    for (const [, long, lead] of ADOPTED) {
      expect(cat[lead], lead).toBeTruthy();
      expect(cat[lead].length, lead).toBeLessThanOrEqual(130);
      expect(cat[lead].length * 2, lead).toBeLessThan(cat[long].length);
    }
  });
});
