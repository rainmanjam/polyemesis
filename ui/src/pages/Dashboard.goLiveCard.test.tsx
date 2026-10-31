// @vitest-environment jsdom

/* THE GO LIVE CARD WITH NO ACCOUNTS (A86).
 *
 * With no platform account connected the card is one sentence telling the
 * operator to connect one. The header used to add "0 accounts" beside it. That
 * figure reads as a fault next to "5 live" on the same page, and "accounts" is
 * not a word the operator has been using. The count shows only when there is
 * something to count. */

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { GoLiveComposer } from "./Dashboard";
import { translate } from "@/lib/i18n";

const originalFetch = globalThis.fetch;

afterEach(() => {
  cleanup();
  globalThis.fetch = originalFetch;
});

function serveNoAccounts() {
  globalThis.fetch = vi.fn(async (url: RequestInfo | URL) => {
    const path = String(url).replace(/^\/api\/v1/, "");
    const body =
      path === "/metadata"
        ? { targets: [], last: null }
        : path === "/metadata/broadcast-window"
          ? { accounts: [] }
          : {};
    return {
      ok: true,
      status: 200,
      headers: new Headers({ "Content-Type": "application/json" }),
      text: async () => JSON.stringify(body),
    } as unknown as Response;
  }) as unknown as typeof fetch;
}

describe("GoLiveComposer with no platform accounts (A86)", () => {
  it("says to connect an account once, and does not print a '0 accounts' figure beside it", async () => {
    serveNoAccounts();
    render(<GoLiveComposer />);

    expect(await screen.findByText(translate("en", "dash.connectPrompt"))).toBeTruthy();
    expect(screen.queryByText("0 accounts")).toBeNull();
  });
});

describe("GoLiveComposer's card is the stream's details, not the start (A7)", () => {
  it("titles the metadata card Stream info, so Go live names only the start button", async () => {
    // The card used to be titled Go live, the same words as the toolbar button
    // that starts destinations. It is the title, tags and category.
    serveNoAccounts();
    render(<GoLiveComposer />);

    expect(await screen.findByText(translate("en", "dash.streamInfo"))).toBeTruthy();
    expect(screen.queryByText(translate("en", "golive.title"))).toBeNull();
  });
});
