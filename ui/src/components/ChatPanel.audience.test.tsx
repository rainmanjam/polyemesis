// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { ChatStatusList } from "./ChatPanel";
import type { ChatStatus } from "@/lib/types";

/* Audience figures the chat transport carries (Rumble): shown when reported,
 * absent when not. A missing figure must not render as zero. */

afterEach(cleanup);

const status = (extra: Partial<ChatStatus>): ChatStatus => ({
  platform: "rumble",
  state: "live",
  since: "2026-11-01T10:00:00Z",
  received: 0,
  sent: 0,
  restarts: 0,
  canSend: false,
  ...extra,
});

describe("ChatStatusList audience line", () => {
  it("shows viewers, followers and subscribers when reported", () => {
    render(
      <ChatStatusList
        statuses={[status({ viewers: 19, followers: 1200, subscribers: 3 })]}
      />,
    );
    expect(
      screen.getByText(/Watching: 19 · Followers: 1,200 · Subscribers: 3/),
    ).toBeTruthy();
  });

  it("shows nothing for figures the platform did not report", () => {
    render(<ChatStatusList statuses={[status({ viewers: 0 })]} />);
    expect(screen.getByText("Watching: 0")).toBeTruthy();
    expect(screen.queryByText(/Followers/)).toBeNull();
  });

  it("renders no audience line when nothing was reported", () => {
    render(<ChatStatusList statuses={[status({})]} />);
    expect(screen.queryByText(/Watching/)).toBeNull();
  });
});
