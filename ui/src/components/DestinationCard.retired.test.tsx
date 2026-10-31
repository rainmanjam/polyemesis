// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { DestinationCard } from "./DestinationCard";
import type { DestStatus } from "@/lib/types";

/* A destination on a platform that has shut down. Trovo ceased live streaming
 * on 30 June 2026; a row saved before that is kept (its settings are the
 * operator's) and switched off by the server. The card has to say WHY it is
 * off, in the operator's language, and must not offer a Start the server will
 * only refuse. */
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

/* A freshly drawn card holds Start for a moment by design (useSettledAction,
 * #506), so "disabled" right after render proves nothing. Let that guard run
 * out first; what is still disabled after it is disabled for the reason. */
async function settle() {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(5000);
  });
}

const dest = (over: Partial<DestStatus> = {}): DestStatus =>
  ({
    id: 7,
    name: "Old Trovo channel",
    kind: "rtmp",
    platform: "trovo",
    enabled: false,
    summary: "",
    tracks: null,
    filterComplex: "",
    normalization: "off",
    warnings: null,
    ...over,
  }) as DestStatus;

function draw(over: Partial<DestStatus>, onStart = () => {}) {
  const noop = () => {};
  return render(
    <MemoryRouter>
      <DestinationCard
        dest={dest(over)}
        onStart={onStart}
        onStop={noop}
        onRestart={noop}
        onEdit={noop}
        onDelete={noop}
        onRefreshKey={noop}
        onMoveEarlier={noop}
        onMoveLater={noop}
        canMoveEarlier={false}
        canMoveLater={false}
      />
    </MemoryRouter>,
  );
}

describe("a destination on a retired platform", () => {
  it("says Trovo shut down on 30 June 2026", () => {
    draw({});
    expect(screen.getByTestId("dest-retired").textContent).toContain(
      "Trovo shut down on 30 June 2026",
    );
  });

  it("greys out Start and says why, so the refusal never has to arrive", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const start = vi.fn();
    draw({}, start);
    await settle();
    const btn = screen.getByRole("button", { name: /Start/ });
    expect(btn).toHaveProperty("disabled", true);
    expect(btn.getAttribute("title")).toContain("Trovo shut down on 30 June 2026");
    fireEvent.click(btn);
    expect(start).not.toHaveBeenCalled();
  });

  it("leaves a live platform's card alone", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const start = vi.fn();
    draw({ platform: "youtube", name: "YouTube main" }, start);
    await settle();
    expect(screen.queryByTestId("dest-retired")).toBeNull();
    const btn = screen.getByRole("button", { name: /Start/ });
    expect(btn).toHaveProperty("disabled", false);
    expect(btn.getAttribute("title")).not.toContain("shut down");
    fireEvent.click(btn);
    expect(start).toHaveBeenCalledTimes(1);
  });
});
