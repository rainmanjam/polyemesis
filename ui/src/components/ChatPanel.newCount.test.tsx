// @vitest-environment jsdom

import { afterEach, beforeAll, describe, expect, it } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";

import { ChatTimeline } from "./ChatPanel";
import { setLanguage } from "@/lib/i18n";
import type { ChatMessage } from "@/lib/types";

/* The "new messages" button that appears while the chat is scrolled up. English
 * "new" does not change with the count, so these render in Spanish and
 * Portuguese, where it does: the button used to read "1 nuevos". */

beforeAll(() => {
  if (!Element.prototype.scrollTo) Element.prototype.scrollTo = () => {};
});

afterEach(() => {
  cleanup();
  setLanguage("en");
});

const msg = (n: number): ChatMessage =>
  ({ id: `m${n}`, platform: "twitch", author: { name: `viewer${n}` }, text: `hello ${n}`, at: "" }) as ChatMessage;

/** Renders, scrolls the log up off the bottom, then adds `extra` messages. */
function scrolledUpThenGrow(extra: number) {
  const first = [msg(1), msg(2)];
  const { rerender } = render(<ChatTimeline messages={first} />);
  const log = screen.getByRole("log");
  // jsdom lays nothing out, so the log is placed 500px above its bottom by hand.
  Object.defineProperty(log, "scrollHeight", { configurable: true, value: 1000 });
  Object.defineProperty(log, "clientHeight", { configurable: true, value: 300 });
  log.scrollTop = 200;
  act(() => {
    fireEvent.scroll(log);
  });
  const more = Array.from({ length: extra }, (_, i) => msg(3 + i));
  rerender(<ChatTimeline messages={[...first, ...more]} />);
}

describe("ChatTimeline: the new-messages count reads one as one", () => {
  it("says 1 nuevo, not 1 nuevos, for one new message (es)", () => {
    setLanguage("es");
    scrolledUpThenGrow(1);
    expect(screen.getByRole("button", { name: "1 nuevo" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "1 nuevos" })).toBeNull();
  });

  it("says 2 nuevos for two new messages (es)", () => {
    setLanguage("es");
    scrolledUpThenGrow(2);
    expect(screen.getByRole("button", { name: "2 nuevos" })).toBeTruthy();
  });

  it("agrees with the feminine mensagens in Portuguese (1 nova, 2 novas)", () => {
    setLanguage("pt-BR");
    scrolledUpThenGrow(1);
    expect(screen.getByRole("button", { name: "1 nova" })).toBeTruthy();
    cleanup();
    scrolledUpThenGrow(2);
    expect(screen.getByRole("button", { name: "2 novas" })).toBeTruthy();
  });
});
