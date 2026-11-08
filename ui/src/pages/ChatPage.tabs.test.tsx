// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, useLocation } from "react-router";
import { translate } from "@/lib/i18n";

/* Chat | Activity. Chat stays the default and its page is untouched; Activity
 * is a second tab kept in the URL. The feed hook and the Activity tab itself
 * are mocked: both open sockets, and what is under test is the strip. */

vi.mock("@/hooks/useChatFeed", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/useChatFeed")>()),
  useChatFeed: () => ({
    messages: [], statuses: [], limits: [], stats: null, configured: true, connected: true,
    loading: false, stored: false, error: "", frameError: false, reload: vi.fn(), remove: vi.fn(),
  }),
}));
vi.mock("@/features/activity/ActivityTab", () => ({
  ActivityTab: () => <div data-testid="activity-tab">activity tab</div>,
}));

const { ChatPage } = await import("./ChatPage");

beforeEach(() => {
  if (!Element.prototype.scrollTo) Element.prototype.scrollTo = function () {};
});
afterEach(cleanup);

function Where() {
  const l = useLocation();
  return <output data-testid="where">{l.pathname + l.search}</output>;
}
const renderAt = (url: string) =>
  render(
    <MemoryRouter initialEntries={[url]}>
      <ChatPage />
      <Where />
    </MemoryRouter>,
  );
const tab = (name: string) => screen.getByRole("tab", { name });

describe("ChatPage tabs", () => {
  it("opens on Chat, with the Activity tab not mounted", () => {
    renderAt("/chat");
    expect(tab(translate("en", "activity.tab.chat")).getAttribute("aria-selected")).toBe("true");
    expect(tab(translate("en", "activity.tab.activity")).getAttribute("aria-selected")).toBe("false");
    expect(screen.queryByTestId("activity-tab")).toBeNull();
    // the chat panel is the visible one
    expect(document.getElementById("chat-panel-chat")!.className).toContain("grid");
    expect(screen.getByRole("tablist", { name: translate("en", "activity.tabsLabel") })).toBeTruthy();
  });

  it("opens on Activity from ?tab=activity", () => {
    renderAt("/chat?tab=activity");
    expect(tab(translate("en", "activity.tab.activity")).getAttribute("aria-selected")).toBe("true");
    expect(screen.getByTestId("activity-tab")).toBeTruthy();
    // chat stays mounted, only hidden, so its scrollback and socket survive
    expect(document.getElementById("chat-panel-chat")!.className).toBe("hidden");
  });

  it("keeps the tab in the URL, and Chat leaves the URL clean", () => {
    renderAt("/chat?x=1");
    fireEvent.click(tab(translate("en", "activity.tab.activity")));
    expect(screen.getByTestId("where").textContent).toBe("/chat?x=1&tab=activity");
    expect(screen.getByTestId("activity-tab")).toBeTruthy();
    fireEvent.click(tab(translate("en", "activity.tab.chat")));
    expect(screen.getByTestId("where").textContent).toBe("/chat?x=1");
    expect(screen.queryByTestId("activity-tab")).toBeNull();
  });

  it("treats an unknown tab value as Chat", () => {
    renderAt("/chat?tab=nonsense");
    expect(tab(translate("en", "activity.tab.chat")).getAttribute("aria-selected")).toBe("true");
  });
});
