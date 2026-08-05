import { expect, test } from "@playwright/test";

/* ===========================================================================
   Edge cases against a REAL deployment, not the container fixture.

   Everything here is reachable over BASE_URL alone -- no docker exec, no
   filesystem access, no assumption about how the server was installed. That
   constraint is the point: it is what lets the file run against the binary
   install on a remote host, which is where these were first exercised.

   Each case is a boundary the UI cannot be trusted to enforce on its own,
   because the UI is not the only client. The API is the security surface.
   =========================================================================== */

const API = "/api/v1";

/** Names this file creates. Anything matching is removed when the file ends. */
const PROBE = "e2e-edge-probe";

/* These tests write, and the install they write to is shared and long-lived --
 * on a real deployment it is someone's server, not a container thrown away
 * afterwards. Clean up rather than leaving a trail of probe rows behind. */
test.afterAll(async ({ playwright, baseURL }) => {
  const ctx = await playwright.request.newContext({
    baseURL,
    ignoreHTTPSErrors: true,
    storageState: "e2e/.auth/state.json",
  });
  try {
    const res = await ctx.get(`${API}/sources`);
    if (!res.ok()) return;
    const sources = (await res.json()) as Array<{ id?: number; name?: string }>;
    const csrf = (await ctx.storageState()).cookies.find((c) => c.name === "polyemesis_csrf");
    for (const s of sources) {
      if (!s.id || !s.name?.includes(PROBE)) continue;
      await ctx
        .delete(`${API}/sources/${s.id}`, {
          headers: csrf ? { "X-CSRF-Token": decodeURIComponent(csrf.value) } : {},
        })
        .catch(() => undefined);
    }
  } finally {
    await ctx.dispose();
  }
});

/* --------------------------------------------------------------- unauthenticated

   A fresh context with no storage state. `test.use` inside a describe replaces
   the session the config injects, so these run as a signed-out stranger. */
test.describe("unauthenticated access", () => {
  test.use({ storageState: { cookies: [], origins: [] } });

  test("health is public, because a container healthcheck runs before login", async ({
    request,
  }) => {
    const res = await request.get(`${API}/health`);
    expect(res.status()).toBe(200);
    expect(await res.json()).toEqual({ status: "ok" });
  });

  for (const path of [
    "sources",
    "destinations",
    "settings",
    "recordings",
    "clips",
    "hooks",
    "auth/tokens",
    "media",
  ]) {
    test(`GET ${API}/${path} is refused without a session`, async ({ request }) => {
      const res = await request.get(`${API}/${path}`);
      expect(res.status()).toBe(401);
    });
  }

  test("a mutating call is refused without a session", async ({ request }) => {
    const res = await request.post(`${API}/sources`, {
      data: { name: "should-never-exist" },
    });
    expect(res.status()).toBeGreaterThanOrEqual(400);
    expect(res.status()).toBeLessThan(500);
  });

  test("a wrong password does not mint a session cookie", async ({ request }) => {
    const res = await request.post(`${API}/auth/login`, {
      data: { username: "admin", password: "definitely-not-the-password" },
    });
    expect(res.status()).toBeGreaterThanOrEqual(400);

    const setCookie = res.headersArray().filter((h) => h.name.toLowerCase() === "set-cookie");
    const session = setCookie.find((h) => h.value.startsWith("polyemesis_session="));
    // A failed login that still sets a session cookie would be the whole ball
    // game, so this asserts the absence rather than trusting the status code.
    expect(session, "failed login must not set a session cookie").toBeUndefined();
  });

  test("setup cannot be re-run once an admin exists", async ({ request }) => {
    const res = await request.post(`${API}/setup`, {
      data: { password: "attacker-chosen-password-123", confirm: "attacker-chosen-password-123" },
    });
    // Whatever the code, it must not be a success: a replayable setup endpoint
    // is an unauthenticated admin-takeover.
    expect(res.status(), "setup must not succeed a second time").toBeGreaterThanOrEqual(400);
  });
});

/* ------------------------------------------------------------------ session cookie */

test.describe("session cookie hardening", () => {
  test("the session cookie is HttpOnly, Secure and SameSite over TLS", async ({ page }) => {
    await page.goto("/");
    const cookies = await page.context().cookies();

    const session = cookies.find((c) => c.name === "polyemesis_session");
    expect(session, "no session cookie present").toBeTruthy();
    // XSS must not be able to read the JWT.
    expect(session!.httpOnly, "session cookie must be HttpOnly").toBe(true);
    expect(session!.sameSite, "session cookie must carry SameSite").not.toBe("None");

    const isHttps = new URL(page.url()).protocol === "https:";
    if (isHttps) {
      expect(session!.secure, "over TLS the session cookie must be Secure").toBe(true);
    }

    const csrf = cookies.find((c) => c.name === "polyemesis_csrf");
    expect(csrf, "no CSRF cookie present").toBeTruthy();
    // Deliberately readable: the SPA has to echo it back in a header. Asserted
    // so that "fixing" it to HttpOnly breaks loudly here instead of breaking
    // every write in the app.
    expect(csrf!.httpOnly, "CSRF cookie is double-submit and must be readable").toBe(false);
  });
});

/* -------------------------------------------------------------------------- CSRF */

test.describe("CSRF double-submit", () => {
  test("a write with a valid session but no CSRF header is refused", async ({ page }) => {
    await page.goto("/");
    const status = await page.evaluate(async (api) => {
      const res = await fetch(`${api}/sources`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: "csrf-probe-no-header" }),
      });
      return res.status;
    }, API);
    expect(status, "a cookie-authenticated write with no CSRF token must fail").toBeGreaterThanOrEqual(400);
  });

  test("a write with a MISMATCHED CSRF token is refused", async ({ page }) => {
    await page.goto("/");
    const status = await page.evaluate(async (api) => {
      const res = await fetch(`${api}/sources`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-CSRF-Token": "not-the-token-in-the-cookie",
        },
        body: JSON.stringify({ name: "csrf-probe-bad-token" }),
      });
      return res.status;
    }, API);
    expect(status, "a forged CSRF token must fail").toBeGreaterThanOrEqual(400);
  });

  test("a write WITH the matching CSRF token succeeds", async ({ page }) => {
    await page.goto("/");
    // The control case. Without it the two tests above pass on a server that
    // refuses every write for some unrelated reason, and prove nothing.
    const result = await page.evaluate(
      async ({ api, name }) => {
        const token = document.cookie
          .split("; ")
          .find((c) => c.startsWith("polyemesis_csrf="))
          ?.split("=")[1];
        const res = await fetch(`${api}/sources`, {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            "X-CSRF-Token": decodeURIComponent(token ?? ""),
          },
          body: JSON.stringify({ name }),
        });
        return { status: res.status, body: await res.text() };
      },
      { api: API, name: `${PROBE}-csrf-good` },
    );
    expect(result.status, `write with a good token failed: ${result.body}`).toBeLessThan(400);
  });
});

/* --------------------------------------------------------------------- injection */

test.describe("stored input is escaped, not interpreted", () => {
  // Carries the probe prefix so afterAll can find and remove it; the payload
  // is what matters and is unaffected by the leading text.
  const XSS = `${PROBE}-<img src=x onerror="window.__xss=1">`;

  test("a source name carrying markup renders as text", async ({ page }) => {
    await page.goto("/sources");

    const created = await page.evaluate(
      async ({ api, name }) => {
        const token = document.cookie
          .split("; ")
          .find((c) => c.startsWith("polyemesis_csrf="))
          ?.split("=")[1];
        const res = await fetch(`${api}/sources`, {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            "X-CSRF-Token": decodeURIComponent(token ?? ""),
          },
          body: JSON.stringify({ name }),
        });
        return { status: res.status, body: await res.text() };
      },
      { api: API, name: XSS },
    );

    // If the server rejects the name outright that is a perfectly good answer;
    // the point is only that it must never come back as live markup.
    if (created.status >= 400) {
      test.info().annotations.push({
        type: "note",
        description: `server refused the markup name with ${created.status} — also acceptable`,
      });
      return;
    }

    await page.reload();
    await page.waitForLoadState("networkidle");
    const executed = await page.evaluate(() => (window as never as { __xss?: number }).__xss);
    expect(executed, "the payload executed — stored XSS").toBeUndefined();
  });
});

/* ------------------------------------------------------------------ routing edges */

test.describe("router boundaries", () => {
  test("an unknown UI route falls back to the SPA, not a 404 page", async ({ request }) => {
    const res = await request.get("/this-route-does-not-exist");
    expect(res.status()).toBe(200);
    expect(res.headers()["content-type"]).toContain("text/html");
  });

  test("an unknown API route does NOT fall back to HTML", async ({ request }) => {
    // The SPA fallback must stop at /api. Serving index.html for a mistyped API
    // path turns a 404 into a 200 full of HTML, and every client parses it as
    // JSON and reports something unrelated.
    const res = await request.get(`${API}/no-such-endpoint`);
    expect(res.headers()["content-type"] ?? "", "an API 404 must not be HTML").not.toContain(
      "text/html",
    );
  });
});
