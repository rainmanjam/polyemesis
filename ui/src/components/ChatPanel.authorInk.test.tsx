// @vitest-environment jsdom

import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";

import { ChatTimeline } from "./ChatPanel";
import { applyTheme } from "@/lib/theme";
import type { ChatMessage } from "@/lib/types";

/* A viewer's name is painted in the colour their PLATFORM gave them, chosen for
 * a dark chat window. Pure yellow on the light theme's white card is about 1.1:1,
 * so the light theme darkens it (lib/authorColor.ts) -- and follows a live
 * theme switch without the messages being re-fetched. */

beforeAll(() => {
  if (!Element.prototype.scrollTo) Element.prototype.scrollTo = () => {};
});
beforeEach(() => document.documentElement.removeAttribute("data-theme"));
afterEach(() => cleanup());

const yellow = {
  id: "m1",
  platform: "twitch",
  author: { id: "u1", name: "sunny", color: "#ffff00" },
  text: "hello",
  at: "",
} as ChatMessage;

const nameColour = () => screen.getByRole("button", { name: /sunny/ }).style.color;

describe("a chat author's own colour", () => {
  it("is painted as given on the dark theme", () => {
    applyTheme("dark");
    render(<ChatTimeline messages={[yellow]} />);
    expect(nameColour()).toBe("rgb(255, 255, 0)");
  });

  it("is darkened to stay readable on the light theme", () => {
    applyTheme("light");
    render(<ChatTimeline messages={[yellow]} />);
    expect(nameColour()).not.toBe("rgb(255, 255, 0)");
    expect(nameColour()).toMatch(/^rgb\(/);
  });

  it("follows a theme change on screen, with the same message", async () => {
    applyTheme("dark");
    render(<ChatTimeline messages={[yellow]} />);
    await act(async () => {
      applyTheme("light");
      await Promise.resolve();
    });
    expect(nameColour()).not.toBe("rgb(255, 255, 0)");
  });
});
