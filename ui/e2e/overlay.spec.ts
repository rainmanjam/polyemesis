import { expect, test, type Browser, type Page } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";

/* ===========================================================================
   The chat overlay as OBS sees it: a sessionless page at 1920x1080 whose
   background must be transparent, so the scene shows through everywhere but
   the chat lines.

   The overlay itself (its key, its config, the page) is the REAL server's. Only
   the display stream is stubbed, for the reason chat-moderation.spec.ts gives:
   chat arrives from a platform socket and cannot be seeded over the API. What
   the stream carries -- and does not -- is proven in Go, through the real chat
   hub, automod wiring and gate (internal/api/overlays_test.go).

   Real OBS is not driven here. Headless Chromium with a transparent page
   background is the same engine (CEF) OBS's browser source embeds; what that
   does not cover is OBS's own compositing.
   =========================================================================== */

const API = "/api/v1";
const SHOTS = process.env.E2E_SHOTS ?? "";

async function csrf(page: Page): Promise<string> {
  await page.goto("/");
  const c = (await page.context().cookies()).find((x) => x.name === "polyemesis_csrf");
  if (!c) throw new Error("no CSRF cookie: is the setup project's session loaded?");
  return c.value;
}

async function createOverlay(page: Page, name: string, platforms: string[]) {
  const token = await csrf(page);
  const list = await page.request.get(`${API}/overlays`);
  expect(list.ok()).toBeTruthy();
  const { presets } = await list.json();
  const res = await page.request.post(`${API}/overlays`, {
    headers: { "X-CSRF-Token": token },
    data: { name, platforms, style: { ...presets.bubble, fadeSeconds: 0 } },
  });
  expect(res.status()).toBe(201);
  const o = (await res.json()) as { id: number; obsKey: string };
  return { ...o, remove: async () => {
    await page.request.delete(`${API}/overlays/${o.id}`, { headers: { "X-CSRF-Token": token } });
  } };
}

function sse(lines: { id: string; platform: string; name: string; text: string }[]): string {
  return lines
    .map((l, i) => {
      const env = { specVersion: "1", id: `e2e-${i}`, type: "chat.display", at: new Date().toISOString(), data: { ...l, at: new Date().toISOString() } };
      return `id: e2e-${i}\nevent: chat.display\ndata: ${JSON.stringify(env)}\n\n`;
    })
    .join("");
}

/** A fresh, signed-OUT browser at OBS's canvas size: an overlay has no session. */
async function obsContext(browser: Browser) {
  return browser.newContext({
    storageState: { cookies: [], origins: [] },
    viewport: { width: 1920, height: 1080 },
    ignoreHTTPSErrors: true,
  });
}

/** Alpha of a set of pixels in a PNG, decoded by the browser's own canvas. */
async function alphaAt(page: Page, png: Buffer, points: [number, number][]): Promise<number[]> {
  return page.evaluate(
    async ({ b64, pts }) => {
      const img = new Image();
      img.src = `data:image/png;base64,${b64}`;
      await img.decode();
      const c = document.createElement("canvas");
      c.width = img.width;
      c.height = img.height;
      const ctx = c.getContext("2d")!;
      ctx.drawImage(img, 0, 0);
      return pts.map(([x, y]) => ctx.getImageData(x, y, 1, 1).data[3]);
    },
    { b64: png.toString("base64"), pts: points },
  );
}

/** The share of fully transparent pixels, sampled on a grid. */
async function transparentShare(page: Page, png: Buffer): Promise<number> {
  return page.evaluate(async (b64) => {
    const img = new Image();
    img.src = `data:image/png;base64,${b64}`;
    await img.decode();
    const c = document.createElement("canvas");
    c.width = img.width;
    c.height = img.height;
    const ctx = c.getContext("2d")!;
    ctx.drawImage(img, 0, 0);
    const d = ctx.getImageData(0, 0, c.width, c.height).data;
    let clear = 0;
    let n = 0;
    for (let i = 3; i < d.length; i += 4 * 97) {
      n++;
      if (d[i] === 0) clear++;
    }
    return clear / n;
  }, png.toString("base64"));
}

function saveShot(name: string, png: Buffer) {
  if (!SHOTS) return;
  mkdirSync(SHOTS, { recursive: true });
  writeFileSync(join(SHOTS, name), png);
}

test.describe("chat overlay", () => {
  test("renders chat over a transparent background at 1920x1080, with no session", async ({ page, browser }) => {
    const o = await createOverlay(page, "e2e-overlay-twitch", ["twitch"]);
    const ctx = await obsContext(browser);
    const obs = await ctx.newPage();
    await obs.route(
      (url) => url.pathname === `${API}/overlay/events`,
      (route) =>
        route.fulfill({
          status: 200,
          headers: { "content-type": "text/event-stream" },
          body: sse([
            { id: "l1", platform: "twitch", name: "pixelpanda", text: "first line on stream" },
            { id: "l2", platform: "twitch", name: "nightowl_tv", text: "second line on stream" },
          ]),
        }),
    );
    let sawAuthMe = false;
    obs.on("request", (r) => {
      if (r.url().includes("/auth/me") || r.url().includes("/api/v1/ws")) sawAuthMe = true;
    });
    await obs.goto(`/overlay#key=${encodeURIComponent(o.obsKey)}`);
    await expect(obs.locator(".overlay-line").first()).toBeVisible();

    // Every layer the console paints is transparent here.
    const bgs = await obs.evaluate(() =>
      [document.documentElement, document.body, document.getElementById("root")!].map(
        (e) => getComputedStyle(e).backgroundColor,
      ),
    );
    for (const bg of bgs) expect(bg).toBe("rgba(0, 0, 0, 0)");

    const png = await obs.screenshot({ omitBackground: true });
    saveShot("overlay-1920x1080.png", png);
    const [topLeft, topRight, centre] = await alphaAt(obs, png, [[5, 5], [1914, 5], [960, 300]]);
    expect([topLeft, topRight, centre]).toEqual([0, 0, 0]);
    // The lines themselves are opaque somewhere, and the page is mostly clear.
    const share = await transparentShare(obs, png);
    expect(share).toBeGreaterThan(0.9);
    expect(share).toBeLessThan(1);
    expect(sawAuthMe).toBe(false);

    await ctx.close();
    await o.remove();
  });

  test("draws nothing at all for a key that is not valid", async ({ browser }) => {
    const ctx = await obsContext(browser);
    const obs = await ctx.newPage();
    const refused = obs.waitForResponse((r) => r.url().endsWith(`${API}/overlay/config`));
    await obs.goto("/overlay#key=pov_not-a-real-key");
    expect((await refused).status()).toBe(401);
    await obs.waitForTimeout(500);
    await expect(obs.locator(".overlay-line")).toHaveCount(0);
    const png = await obs.screenshot({ omitBackground: true });
    saveShot("overlay-invalid-key.png", png);
    expect(await transparentShare(obs, png)).toBe(1);
    await ctx.close();
  });

  test("the dock is readable on a solid background", async ({ page, browser }) => {
    const o = await createOverlay(page, "e2e-overlay-dock", []);
    const ctx = await browser.newContext({ storageState: { cookies: [], origins: [] }, viewport: { width: 420, height: 640 } });
    const dock = await ctx.newPage();
    await dock.route(
      (url) => url.pathname === `${API}/overlay/events`,
      (route) =>
        route.fulfill({
          status: 200,
          headers: { "content-type": "text/event-stream" },
          body: sse([
            { id: "d1", platform: "youtube", name: "Mara K.", text: "dock line one" },
            { id: "d2", platform: "twitch", name: "pixelpanda", text: "dock line two" },
          ]),
        }),
    );
    await dock.goto(`/overlay/dock#key=${encodeURIComponent(o.obsKey)}`);
    await expect(dock.locator('[data-overlay-mode="dock"]')).toBeVisible();
    await expect(dock.locator(".overlay-line").first()).toBeVisible();
    saveShot("dock.png", await dock.screenshot());
    await ctx.close();
    await o.remove();
  });
});
