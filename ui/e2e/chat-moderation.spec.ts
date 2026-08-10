import { expect, test, type Page } from "@playwright/test";

/* ===========================================================================
   Chat search, the right-click menu, and the link out to the platform.

   These are the first tests in this suite to STUB a route, and the reason is
   specific rather than convenient: every assertion here needs chat messages to
   exist, and a real chat message requires a live Twitch or YouTube account
   connected to the install. There is no way to seed one over the API — the
   messages arrive from a platform socket — so a test driving the real endpoint
   would assert against whatever happened to be in the scrollback, which is
   usually nothing.

   What is stubbed is deliberately narrow: the chat READS, and nothing else.
   The moderation calls are left pointing at the real API so a request that
   would 404 or send the wrong parameters still fails here. The stub decides
   what the pane displays; it does not decide whether the buttons work.

   The server-side behaviour these lean on — the LIKE escaping, the platform
   filter, the truncation flag — is covered where it belongs, in Go, in
   internal/db/chat_search_test.go and internal/api/chat_search_test.go.
   =========================================================================== */

const API = "/api/v1";

async function signIn(page: Page) {
  await page.goto("/");
  await expect(page.locator("nav")).toBeVisible();
}

function message(over: Record<string, unknown> = {}) {
  return {
    id: "m1",
    platform: "twitch",
    account: "acct",
    channel: "somestreamer",
    author: { id: "u-heckler", name: "Heckler" },
    text: "this is the comment to find",
    at: new Date("2026-08-05T12:00:00Z").toISOString(),
    ...over,
  };
}

/** Stubs the two chat reads so the pane has something to render. */
async function stubChat(page: Page, opts: { messages?: unknown[]; search?: unknown[] } = {}) {
  const live = opts.messages ?? [message()];

  // Matched on the PATH, not by glob. The overview is requested as
  // `/chat?limit=300`, and a `**/chat` glob does not match a URL with a query
  // string — which fails as "no messages rendered", several steps away from the
  // cause.
  await page.route(
    (url) => url.pathname === `${API}/chat`,
    (route) =>
    route.fulfill({
      json: {
        configured: true,
        statuses: [
          {
            platform: "twitch",
            account: "acct",
            state: "live",
            channel: "somestreamer",
            canSend: true,
            received: live.length,
            sent: 0,
            restarts: 0,
          },
        ],
        messages: live,
        limits: [{ platform: "twitch", maxChars: 500 }],
      },
    }),
  );

  await page.route(`**${API}/chat/search?**`, (route) => {
    const q = new URL(route.request().url()).searchParams.get("q") ?? "";
    return route.fulfill({
      json: {
        query: q,
        messages: opts.search ?? [],
        truncated: false,
        retentionNote:
          "Searches this server's own retained scrollback. No platform offers an API for chat history.",
      },
    });
  });
}

test.describe("chat search", () => {
  test("a query replaces the live timeline with server-side results", async ({ page }) => {
    await stubChat(page, {
      messages: [message({ text: "unrelated live chatter" })],
      search: [message({ id: "m2", text: "the comment I was looking for" })],
    });
    await signIn(page);
    await page.goto("/chat");

    const live = page.getByText("unrelated live chatter");
    await expect(live).toBeVisible();

    await page.getByLabel("Search chat history").fill("looking");

    // Results REPLACE the timeline rather than filtering it: these rows came
    // from the database and letting live messages append underneath them would
    // present two different things as one list.
    await expect(page.getByText("the comment I was looking for")).toBeVisible();
    await expect(live).toBeHidden();
  });

  test("the search reaches the server rather than filtering what is on screen", async ({
    page,
  }) => {
    await stubChat(page, { search: [message({ id: "m2", text: "from the database" })] });
    await signIn(page);
    await page.goto("/chat");

    // The whole point of the feature. A client-side filter would never issue
    // this request, and would silently be unable to find anything that had
    // scrolled out of the pane.
    const req = page.waitForRequest((r) => r.url().includes("/chat/search"));
    await page.getByLabel("Search chat history").fill("database");
    expect(new URL((await req).url()).searchParams.get("q")).toBe("database");
  });

  test("an empty result says it searched a bounded history", async ({ page }) => {
    await stubChat(page, { search: [] });
    await signIn(page);
    await page.goto("/chat");

    await page.getByLabel("Search chat history").fill("nothing matches this");

    // The one outcome an operator can misread as evidence. "No matches" alone
    // invites "then they never said it", which is a claim about a purged table
    // and not about a person.
    await expect(page.getByText("No matches in the retained scrollback.")).toBeVisible();
    await expect(page.getByText(/No platform offers an API for chat history/)).toBeVisible();
  });

  test("clearing the box returns to the live timeline", async ({ page }) => {
    await stubChat(page, {
      messages: [message({ text: "unrelated live chatter" })],
      search: [message({ id: "m2", text: "search hit" })],
    });
    await signIn(page);
    await page.goto("/chat");

    const box = page.getByLabel("Search chat history");
    await box.fill("hit");
    await expect(page.getByText("search hit")).toBeVisible();

    // Escape, because a moderator who has to find a small × while chat scrolls
    // has been given a worse tool than they had before.
    await box.press("Escape");
    await expect(page.getByText("unrelated live chatter")).toBeVisible();
  });
});

test.describe("the right-click menu", () => {
  test("right-click on a message offers the quick actions", async ({ page }) => {
    await stubChat(page);
    await signIn(page);
    await page.goto("/chat");

    await page.getByText("this is the comment to find").click({ button: "right" });

    await expect(page.getByRole("menuitem", { name: /View history/ })).toBeVisible();
    await expect(page.getByRole("menuitem", { name: "Time out 10 min" })).toBeVisible();
    await expect(page.getByRole("menuitem", { name: /Ban permanently/ })).toBeVisible();
  });

  test("double-click opens the same menu, for pointers with no right button", async ({ page }) => {
    await stubChat(page);
    await signIn(page);
    await page.goto("/chat");

    await page.getByText("this is the comment to find").dblclick();
    await expect(page.getByRole("menuitem", { name: "Time out 1 hour" })).toBeVisible();
  });

  test("a timeout issues the moderation call with the duration in seconds", async ({ page }) => {
    await stubChat(page);
    await signIn(page);
    await page.goto("/chat");

    await page.getByText("this is the comment to find").click({ button: "right" });

    // Deliberately NOT stubbed: this asserts the request polyemesis actually
    // sends. Seconds on the wire always — the server converts for Kick, which
    // counts in minutes, and a UI that sent minutes would time somebody out for
    // sixty times too long.
    const req = page.waitForRequest((r) => r.url().includes("/chat/bans"));
    await page.getByRole("menuitem", { name: "Time out 10 min" }).click();

    const params = new URL((await req).url()).searchParams;
    expect(params.get("seconds")).toBe("600");
    expect(params.get("userId")).toBe("u-heckler");
    expect(params.get("platform")).toBe("twitch");
  });

  test("a permanent ban does not fire from the menu", async ({ page }) => {
    await stubChat(page);
    await signIn(page);
    await page.goto("/chat");

    let banned = false;
    await page.route(`**${API}/chat/bans**`, (route) => {
      banned = true;
      return route.fulfill({ json: { detail: "banned" } });
    });

    await page.getByText("this is the comment to find").click({ button: "right" });
    await page.getByRole("menuitem", { name: /Ban permanently/ }).click();

    // It opens the card, which confirms. Friction has to be proportional to the
    // consequence, and the one irreversible action must not be a single click
    // on a menu that opened under the cursor.
    await expect(page.getByRole("dialog")).toBeVisible();
    expect(banned, "a right-click must not permanently ban anyone on its own").toBe(false);
  });
});

test.describe("the link out to the platform", () => {
  test("Twitch offers the moderator viewer card in a new window", async ({ page, context }) => {
    await stubChat(page);
    await signIn(page);
    await page.goto("/chat");

    await page.getByText("this is the comment to find").click({ button: "right" });

    const popup = context.waitForEvent("page");
    await page.getByRole("menuitem", { name: /Open moderator card on Twitch/ }).click();

    // A separate window, not a tab: the point is to act there while still
    // watching chat here.
    const url = (await popup).url();
    expect(url).toContain("/popout/somestreamer/viewercard/heckler");
  });

  test("a platform with no moderator view says so instead of implying one", async ({ page }) => {
    await stubChat(page, {
      messages: [
        message({
          platform: "youtube",
          author: { id: "UCabc123", name: "Heckler" },
          text: "a youtube comment",
        }),
      ],
    });
    await signIn(page);
    await page.goto("/chat");

    await page.getByText("a youtube comment").click({ button: "right" });

    // "Open channel", never "Open moderator card". YouTube publishes no
    // per-viewer chat history, and a label that promised one would send a
    // moderator to a channel page and let them conclude the viewer is clean.
    await expect(page.getByRole("menuitem", { name: "Open channel on YouTube" })).toBeVisible();
    await expect(page.getByRole("menuitem", { name: /moderator card/ })).toBeHidden();
  });
});
