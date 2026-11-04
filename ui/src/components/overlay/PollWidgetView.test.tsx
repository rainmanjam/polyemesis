// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { PollWidgetView } from "./PollWidgetView";
import type { OverlayStyle, PollDisplay } from "@/lib/overlay";

/* The on-stream poll widget's status line says the vote count in every state.
 * The catalogue has no plural rules, so count 1 has its own key: the widget
 * used to put "1 votes" on stream. */

afterEach(cleanup);

const style: OverlayStyle = {
  preset: "classic", font: "sans", fontSize: 24, textColor: "#ffffff", backgroundColor: "#000000",
  backgroundOpacity: 0, authorColors: true, fadeSeconds: 0, maxLines: 12, showPlatform: true, showBadges: true,
} as OverlayStyle;

const NOW = Date.parse("2026-11-04T12:00:00Z");

const poll = (total: number, state: PollDisplay["state"]): PollDisplay => ({
  id: "p1",
  question: "Next map?",
  options: [
    { label: "Dust", votes: total },
    { label: "Mirage", votes: 0 },
  ],
  total,
  state,
  endsAt: new Date(NOW + 30_000).toISOString(),
});

const draw = (total: number, state: PollDisplay["state"]) =>
  render(<PollWidgetView poll={poll(total, state)} style={style} now={NOW} />);

describe("the poll widget's vote count", () => {
  it("says 1 vote while the poll is open", () => {
    draw(1, "open");
    expect(screen.getByText("1 vote · 30s left")).toBeTruthy();
  });

  it("says 2 votes while the poll is open", () => {
    draw(2, "open");
    expect(screen.getByText("2 votes · 30s left")).toBeTruthy();
  });

  it("says 1 vote while the last votes are counted", () => {
    draw(1, "closing");
    expect(screen.getByText("1 vote · counting the last votes")).toBeTruthy();
  });

  it("says 2 votes while the last votes are counted", () => {
    draw(2, "closing");
    expect(screen.getByText("2 votes · counting the last votes")).toBeTruthy();
  });

  it("says 1 vote once the poll has closed", () => {
    draw(1, "ended");
    expect(screen.getByText("Poll closed · 1 vote")).toBeTruthy();
  });

  it("says 2 votes once the poll has closed", () => {
    draw(2, "ended");
    expect(screen.getByText("Poll closed · 2 votes")).toBeTruthy();
  });
});
