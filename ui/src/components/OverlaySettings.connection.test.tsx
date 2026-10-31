// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import { OverlayConnection } from "./OverlaySettings";
import type { ChatOverlay } from "@/lib/overlay";

/* app-council A98. An overlay draws nothing when it fails, so a broken OBS
 * source looked exactly like an idle one from the console. Settings now says
 * whether a display stream is open, or when one last was, in words. */

const overlay = (over: Partial<ChatOverlay>): ChatOverlay =>
  ({
    id: 1,
    name: "Main",
    obsKey: "pov_example",
    platforms: [],
    style: {},
    createdAt: "2026-01-01T00:00:00Z",
    keyRotatedAt: "2026-01-01T00:00:00Z",
    ...over,
  }) as ChatOverlay;

afterEach(cleanup);

describe("OverlayConnection (app-council A98)", () => {
  it("says how many sources are connected now", () => {
    render(<OverlayConnection overlay={overlay({ connected: 2 })} onCheck={vi.fn()} />);
    expect(screen.getByText("Connected now: 2")).toBeTruthy();
  });

  it("says when it was last connected once nothing is", () => {
    render(
      <OverlayConnection overlay={overlay({ connected: 0, lastSeen: "2026-01-01T10:00:00Z" })} onCheck={vi.fn()} />,
    );
    expect(screen.getByText(/^Not connected\. Last connected/)).toBeTruthy();
  });

  it("says it has never connected, with what to check, and offers to look again", () => {
    const onCheck = vi.fn();
    render(<OverlayConnection overlay={overlay({ connected: 0 })} onCheck={onCheck} />);
    expect(screen.getByText(/check the URL in its browser source/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Check again" }));
    expect(onCheck).toHaveBeenCalledTimes(1);
  });

  it("says nothing for a server too old to report it, rather than claiming disconnected", () => {
    const { container } = render(<OverlayConnection overlay={overlay({})} onCheck={vi.fn()} />);
    expect(container.textContent).toBe("");
  });
});
