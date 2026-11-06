import { expect, test, type Page } from "@playwright/test";
import { mkdirSync } from "node:fs";
import { join } from "node:path";

import { watchConsole } from "./console";

/* LIGHT, DARK AND SYSTEM (src/lib/theme.ts).
 *
 * Run against the real server, because the parts worth proving are the ones a
 * unit test cannot reach: that the server's Content-Security-Policy lets
 * /theme-init.js run (an inline script would be blocked), that data-theme is
 * set before the bundle has loaded, and that the public player is NOT themed. */

const KEY = "polyemesis.theme";

/** The body's background as `rgb(r, g, b)`. The stylesheet writes oklch(), which
 *  getComputedStyle hands back as oklch()/oklab(); a 1x1 canvas converts any of
 *  them to the sRGB a screen shows. A transparent body stays "rgba(0, 0, 0, 0)". */
const bg = (page: Page) =>
  page.evaluate(() => {
    const css = getComputedStyle(document.body).backgroundColor;
    if (css === "rgba(0, 0, 0, 0)" || css === "transparent") return "rgba(0, 0, 0, 0)";
    const c = document.createElement("canvas");
    c.width = c.height = 1;
    const ctx = c.getContext("2d")!;
    ctx.fillStyle = css;
    ctx.fillRect(0, 0, 1, 1);
    const [r, g, b] = ctx.getImageData(0, 0, 1, 1).data;
    return `rgb(${r}, ${g}, ${b})`;
  });
const theme = (page: Page) => page.evaluate(() => document.documentElement.getAttribute("data-theme"));
/** The header's control (the nav drawer carries a second one, for phones). */
const toggle = (page: Page) => page.locator("header").getByTestId("theme-toggle");
const radio = (page: Page, name: string) => toggle(page).getByRole("radio", { name, exact: true });

/** Polls until the body is as light as `min` (or as dark as `max`): the colour
 *  fades over --motion-instant (90ms), so a read right after the click can still
 *  be the old colour. Returns the settled value. */
async function bodyLuminance(page: Page, want: { min?: number; max?: number }) {
  await expect
    .poll(async () => {
      const l = lum(await bg(page));
      return (want.min === undefined || l >= want.min) && (want.max === undefined || l <= want.max);
    })
    .toBe(true);
  return bg(page);
}

/** Luminance of an `rgb(r, g, b)` string: enough to say light from dark. */
function lum(css: string): number {
  const [r, g, b] = (css.match(/\d+(\.\d+)?/g) ?? ["0", "0", "0"]).slice(0, 3).map(Number);
  return (0.2126 * r + 0.7152 * g + 0.0722 * b) / 255;
}

test.describe("the console theme", () => {
  test("follows the system until the operator picks, live", async ({ page }) => {
    const { errors } = watchConsole(page);
    await page.emulateMedia({ colorScheme: "dark" });
    await page.goto("/");
    await expect(toggle(page)).toBeVisible();
    expect(await theme(page)).toBe("dark");
    await bodyLuminance(page, { max: 0.2 });
    await expect(radio(page, "System")).toHaveAttribute("aria-checked", "true");

    // The OS flips while the console is open: no reload.
    await page.emulateMedia({ colorScheme: "light" });
    await expect.poll(() => theme(page)).toBe("light");
    await bodyLuminance(page, { min: 0.85 });

    await page.emulateMedia({ colorScheme: "dark" });
    await expect.poll(() => theme(page)).toBe("dark");
    // The theme script is external, so the CSP let it run (and logged nothing).
    expect(errors, errors.join("\n")).toEqual([]);
  });

  test("the toggle flips the computed background, and the choice outlives a reload", async ({ page }) => {
    await page.emulateMedia({ colorScheme: "dark" });
    await page.goto("/");
    const dark = await bodyLuminance(page, { max: 0.2 });

    await radio(page, "Light").click();
    expect(await theme(page)).toBe("light");
    const light = await bodyLuminance(page, { min: 0.85 });
    expect(lum(light)).toBeGreaterThan(lum(dark) + 0.6);
    await expect(radio(page, "Light")).toHaveAttribute("aria-checked", "true");
    expect(await page.evaluate((k) => localStorage.getItem(k), KEY)).toBe("light");

    // Remembered over the OS (still dark), and set before the bundle paints.
    await page.reload({ waitUntil: "domcontentloaded" });
    expect(await theme(page)).toBe("light");
    await expect(radio(page, "Light")).toHaveAttribute("aria-checked", "true");

    await radio(page, "Dark").click();
    expect(await bodyLuminance(page, { max: 0.2 })).toBe(dark);

    // Back to System forgets the pick, so the OS is followed again.
    await radio(page, "System").click();
    expect(await page.evaluate((k) => localStorage.getItem(k), KEY)).toBeNull();
  });

  test("is operable from the keyboard, with a visible focus ring", async ({ page }) => {
    await page.emulateMedia({ colorScheme: "dark" });
    await page.goto("/");
    await radio(page, "System").focus();
    await page.keyboard.press("ArrowLeft"); // System -> Dark
    await expect(radio(page, "Dark")).toHaveAttribute("aria-checked", "true");
    await page.keyboard.press("Home"); // -> Light
    await expect.poll(() => theme(page)).toBe("light");
    const ring = await radio(page, "Light").evaluate((el) => {
      const cs = getComputedStyle(el);
      return { style: cs.outlineStyle, width: cs.outlineWidth };
    });
    expect(ring.style).toBe("solid");
    expect(ring.width).toBe("2px");
  });

  test("on a phone the control moves into the nav drawer, so the header keeps its room", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 800 });
    await page.emulateMedia({ colorScheme: "dark" });
    await page.goto("/");
    await expect(toggle(page)).toBeHidden();
    await page.getByRole("button", { name: "Toggle navigation" }).first().click();
    const drawer = page.locator("nav").getByTestId("theme-toggle");
    await expect(drawer).toBeVisible();
    await drawer.getByRole("radio", { name: "Light", exact: true }).click();
    await bodyLuminance(page, { min: 0.85 });
  });

  test("at 700px (sm..md, drawer open) exactly one theme control is visible", async ({ page }) => {
    await page.setViewportSize({ width: 700, height: 800 });
    await page.goto("/");
    await page.getByRole("button", { name: "Toggle navigation" }).first().click();
    await expect(page.getByTestId("theme-toggle").locator("visible=true")).toHaveCount(1);
    await expect(toggle(page)).toBeVisible();
  });

  test("never themes the public player, whatever the operator chose", async ({ page }) => {
    await page.emulateMedia({ colorScheme: "light" });
    await page.addInitScript((k) => localStorage.setItem(k, "light"), KEY);
    await page.goto("/watch");
    expect(await page.evaluate(() => document.documentElement.hasAttribute("data-theme"))).toBe(false);
    expect(lum(await bg(page))).toBeLessThan(0.2);
    await expect(page.getByTestId("theme-toggle")).toHaveCount(0);
  });

  test("never themes the OBS overlay page: transparent, no data-theme", async ({ page }) => {
    await page.emulateMedia({ colorScheme: "light" });
    await page.addInitScript((k) => localStorage.setItem(k, "light"), KEY);
    await page.goto("/overlay");
    expect(await page.evaluate(() => document.documentElement.hasAttribute("data-theme"))).toBe(false);
    expect(await bg(page)).toBe("rgba(0, 0, 0, 0)");
  });

  // Screenshots for the PR, written outside the repo. A no-op in CI.
  const SHOTS = process.env.THEME_SHOTS_DIR;
  test.describe("screenshots", () => {
    test.skip(!SHOTS, "set THEME_SHOTS_DIR to write them");
    // The meters with live levels: the harness can feed the console's own
    // WebSocket (routeWebSocket), so the canvases draw real bars in each theme.
    for (const mode of ["light", "dark"] as const) {
      test(`meters with live levels in ${mode}`, async ({ page }) => {
        mkdirSync(SHOTS!, { recursive: true });
        await page.setViewportSize({ width: 1440, height: 900 });
        await page.addInitScript(([k, m]) => localStorage.setItem(k, m), [KEY, mode]);
        await page.routeWebSocket(/\/ws(\?|$)/, (ws) => {
          const send = () =>
            ws.send(
              JSON.stringify({
                type: "levels",
                data: {
                  peak: [[-6, -7], [-14, -15], [-3, -2.5], [-24, -26], [-40, -41], [-1, -0.5]],
                  rms: [[-12, -13], [-20, -21], [-8, -9], [-30, -32], [-48, -49], [-5, -6]],
                },
              }),
            );
          send();
          setInterval(send, 200);
        });
        await page.goto("/meters");
        await expect(toggle(page)).toBeVisible();
        await page.waitForTimeout(1500);
        await page.screenshot({ path: join(SHOTS!, `meters-live-${mode}.png`) });
      });
    }
    for (const mode of ["light", "dark"] as const) {
      for (const [name, path] of [
        ["dashboard", "/"],
        ["sources", "/sources"],
        ["meters", "/meters"],
        ["routing", "/routing"],
        ["settings", "/settings"],
      ] as const) {
        test(`${name} in ${mode}`, async ({ page }) => {
          mkdirSync(SHOTS!, { recursive: true });
          await page.setViewportSize({ width: 1440, height: 900 });
          await page.addInitScript(([k, m]) => localStorage.setItem(k, m), [KEY, mode]);
          await page.goto(path);
          await expect(toggle(page)).toBeVisible();
          await page.waitForTimeout(800);
          await page.screenshot({ path: join(SHOTS!, `${name}-${mode}.png`) });
        });
      }
    }
  });
});
