// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { translate } from "@/lib/i18n";
import type { ActivitySourcesResponse } from "@/lib/types";
import { ActivitySourceChips } from "./ActivitySourceChips";
import { kindCounts } from "./eventText";

afterEach(cleanup);

const data = (over: Partial<ActivitySourcesResponse> = {}): ActivitySourcesResponse => ({
  enabled: true, kinds: [], platforms: ["twitch"], sources: [],
  stats: { received: 10, delivered: 8, invalid: 0, deduped: 2, disabled: 0 }, ...over,
});

describe("ActivitySourceChips", () => {
  it("shows one neutral line when no platform source exists yet", () => {
    render(<ActivitySourceChips data={data()} failed={false} />);
    expect(screen.getByText(translate("en", "activity.sources.none"))).toBeTruthy();
    expect(screen.queryByRole("list")).toBeNull();
    expect(screen.getByText("Received 10 · delivered 8 · duplicates 2")).toBeTruthy();
  });

  it("draws a chip per source with state, counts and its own detail", () => {
    render(
      <ActivitySourceChips
        failed={false}
        data={data({
          sources: [
            {
              platform: "twitch", account: "chan", state: "degraded", detail: "Token needs a refresh", transport: "own", events: 7,
              kinds: [
                { kind: "follow", state: "on" },
                { kind: "tip", state: "on" },
                { kind: "raid", state: "unavailable", detail: "needs Affiliate" },
                { kind: "gift", state: "reconnect" },
                { kind: "poll", state: "off" },
                { kind: "stream", state: "unknown" },
              ],
            },
            { platform: "kick", state: "live", transport: "chat", events: 0, kinds: [] },
          ],
        })}
      />,
    );
    const chips = screen.getAllByRole("listitem");
    expect(chips).toHaveLength(2);
    expect(chips[0].textContent).toContain("Twitch");
    expect(chips[0].textContent).toContain("chan");
    expect(chips[0].textContent).toContain("degraded");
    expect(chips[0].textContent).toContain("2 on · 1 unavailable · 1 reconnect");
    expect(chips[0].textContent).toContain("Token needs a refresh");
    expect(chips[0].textContent).toContain("7 events");
    expect(chips[1].getAttribute("data-source-state")).toBe("live");
    // The chips are read-only: every word (state, counts, detail) is visible
    // text in reading order, asserted above, so a screen reader reaches it
    // without hover or focus. A tab stop per chip would add stops that do
    // nothing (Sonar typescript:S6845), so there is none.
    expect(chips.some((c) => c.hasAttribute("tabindex"))).toBe(false);
  });

  it("renders nothing while loading and a note when the read failed", () => {
    const { container, rerender } = render(<ActivitySourceChips data={null} failed={false} />);
    expect(container.textContent).toBe("");
    rerender(<ActivitySourceChips data={null} failed />);
    expect(screen.getByText(translate("en", "activity.sources.failed"))).toBeTruthy();
  });

  it("counts only on, unavailable and reconnect", () => {
    expect(kindCounts({ platform: "x", state: "live", transport: "own", events: 0, kinds: [{ kind: "tip", state: "off" }] })).toEqual({ on: 0, unavailable: 0, reconnect: 0 });
  });
});
