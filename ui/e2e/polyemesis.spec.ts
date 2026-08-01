import { expect, test, type Page } from "@playwright/test";
import { watchConsole } from "./console";

/* ===========================================================================
   Browser end-to-end, against the shipped container.

   Every case here exists because of a bug that actually happened, or a guard
   added in response to one. None of them are reachable by `tsc`: each is a
   thing that compiles perfectly and is wrong at runtime, which is precisely
   the class this project had no automated coverage for at all.

   The install is shared and mutated in order, so the tests are serial by
   design (see playwright.config.ts).
   =========================================================================== */

/** The session comes from auth.setup.ts, so a test only has to navigate. */
async function signIn(page: Page) {
  await page.goto("/");
  await expect(page.locator("nav")).toBeVisible();
}

test.describe("first run and access control", () => {
  test("an authenticated session lands on the console", async ({ page }) => {
    await signIn(page);
    await expect(page.getByRole("navigation")).toBeVisible();
  });

  test("setup cannot be replayed once an admin exists", async ({ request }) => {
    // An exposed port must not be a takeover. This is asserted through the API
    // rather than the UI because the UI stops offering the form -- and "the
    // button is gone" is not the same guarantee as "the endpoint refuses".
    const res = await request.post("/api/v1/setup", {
      data: { username: "intruder", password: "Intruder!9xzq" },
      failOnStatusCode: false,
    });
    expect(res.status()).toBeGreaterThanOrEqual(400);
  });
});

test.describe("navigation", () => {
  // Every route, because a page that throws still renders its neighbours: the
  // nav stays up and only the panel is broken, so a human clicking around can
  // easily miss it.
  const routes = [
    "/", "/sources", "/meters", "/routing", "/renditions", "/playout",
    "/library", "/recordings", "/clips", "/chat", "/jobs", "/automation",
    "/monitoring", "/settings",
  ];

  for (const route of routes) {
    test(`${route} renders without a console error`, async ({ page }) => {
      const { errors } = watchConsole(page);
      await signIn(page);
      await page.goto(route);
      await expect(page.locator("main")).toBeVisible();
      // Deep-linked, not clicked: this also exercises the Go router's SPA
      // fallback, which a click-through navigation never touches.
      await page.reload();
      await expect(page.locator("main")).toBeVisible();
      expect(errors, `console errors on ${route}`).toEqual([]);
    });
  }
});

test.describe("i18n", () => {
  // The React Router v8 migration and thirteen new catalogues were each
  // verified once by hand. This is what stops the next change undoing them
  // silently.
  for (const [code, needle] of [
    ["es", /Panel de control|Medidores de audio/],
    ["ja", /ダッシュボード|オーディオ/],
  ] as const) {
    test(`switching to ${code} translates the nav and sets <html lang>`, async ({ page }) => {
      await signIn(page);
      await page.evaluate((c) => localStorage.setItem("polyemesis.language", c), code);
      await page.reload();
      await expect(page.locator("html")).toHaveAttribute("lang", code);
      await expect(page.locator("nav")).toContainText(needle);
      // Put it back, so a later test does not read a translated label.
      await page.evaluate(() => localStorage.setItem("polyemesis.language", "en"));
    });
  }
});

test.describe("sources", () => {
  test("a new source is ENABLED and lands on a free port", async ({ page }) => {
    // Two bugs in one assertion. A source created disabled was refused with
    // "source disabled" and nothing on screen explained it; a source created on
    // a taken port was displayed as configured and silently received nothing.
    await signIn(page);
    await page.goto("/sources");

    const before = await page.locator('[role="switch"]').count();
    await page.getByRole("button", { name: /add source/i }).click();
    await page.locator("#src-name").fill("E2E Vertical");
    await page.getByRole("button", { name: "Add", exact: true }).click();

    const card = page.locator("div").filter({ hasText: /^E2E Vertical/ }).first();
    await expect(card).toBeVisible();
    // Enabled by default.
    const sw = page.locator('[role="switch"]').nth(before);
    await expect(sw).toHaveAttribute("data-state", "checked");

    // What has to be distinct is no longer a PORT. Sources share one SRT
    // listener and are told apart by their publish token, so the token is the
    // thing that makes a second programme reachable -- and two sources sharing
    // one would make the second unreachable exactly as a shared port used to.
    //
    // Read from the API rather than the DOM: the token is deliberately rendered
    // masked behind an explicit reveal, and a test that clicked through that
    // would be asserting on the masking rather than on the tokens.
    const tokens = await page.evaluate(async () => {
      const res = await fetch("/api/v1/sources", { credentials: "include" });
      const rows = (await res.json()) as Array<{ token?: string }>;
      return rows.map((r) => r.token ?? "");
    });
    expect(tokens.length, "expected a token per source").toBeGreaterThan(1);
    expect(tokens.filter((t) => t === "").length, "a source with no token has no address").toBe(0);
    expect(
      new Set(tokens).size,
      "two sources cannot share a publish token; one would be unreachable",
    ).toBe(tokens.length);
  });

  test("listener port inputs refuse an out-of-range value at the widget", async ({ page }) => {
    // The ports moved to Settings when they stopped being per-source: there is
    // one SRT listener and one RTMP listener for the whole install. Bounds at
    // the widget still matter -- port 0 is not an error to the kernel, it means
    // "any free port", so a listener bound to it comes up, reports itself
    // listening, and is reachable at an address nobody was told.
    await signIn(page);
    await page.goto("/settings");
    for (const id of ["#listener-srt", "#listener-rtmp"]) {
      const port = page.locator(id);
      await expect(port).toBeVisible();
      await expect(port).toHaveAttribute("min", "1");
      await expect(port).toHaveAttribute("max", "65535");
    }
  });

  test("editing a port does NOT commit on blur", async ({ page }) => {
    // This is the one that dropped broadcasts: every blur wrote through and
    // restarted the ingest, so tabbing out of a field was an outage.
    await signIn(page);
    await page.goto("/sources");

    const port = page.locator('input[type="number"]').first();
    await port.fill("6042");
    await port.blur();

    await expect(page.getByRole("button", { name: /^Apply/ })).toBeVisible();
    await expect(page.getByRole("button", { name: /discard/i })).toBeVisible();

    // Discard must actually revert rather than merely hide the row.
    await page.getByRole("button", { name: /discard/i }).click();
    await expect(page.getByRole("button", { name: /^Apply/ })).toBeHidden();
    await expect(port).not.toHaveValue("6042");
  });

  test("delete requires typing the source name, and the WRONG name keeps it locked", async ({
    page,
  }) => {
    // The contact method: the input only fits the intended target, so a
    // mis-click on the wrong row cannot complete.
    await signIn(page);
    await page.goto("/sources");

    await page.getByRole("button", { name: "Delete E2E Vertical" }).click();
    const confirm = page.getByRole("button", { name: /delete source/i });
    await expect(confirm).toBeDisabled();

    // The blast radius is shown as counts, not prose.
    await expect(page.getByText(/this also removes/i)).toBeVisible();
    await expect(page.getByText(/destinations/i).first()).toBeVisible();

    const field = page.locator("#confirm-subject");
    await field.fill("Main");
    await expect(confirm, "the wrong name must not unlock the delete").toBeDisabled();

    await field.fill("E2E Vertical");
    await expect(confirm).toBeEnabled();
    await confirm.click();
    // The CARD specifically. Matching the bare text also finds the dialog
    // heading and the confirm field, which are part of the dialog being
    // dismissed rather than evidence the source survived.
    await expect(page.locator("span.truncate").filter({ hasText: "E2E Vertical" })).toHaveCount(0);
  });
});

test.describe("saving", () => {
  test("a source rename persists across a reload", async ({ page }) => {
    // The Sources page once sent server-computed fields back in its PUT, so
    // every save 400'd: the control flipped, then silently reverted. A toast
    // assertion would not have caught it -- only reading the value back does.
    await signIn(page);
    await page.goto("/sources");

    const sw = page.locator('[role="switch"]').first();
    const was = await sw.getAttribute("data-state");
    await sw.click();
    await page.waitForTimeout(1200);
    await page.reload();

    const now = await page.locator('[role="switch"]').first().getAttribute("data-state");
    expect(now, "the toggle reverted, so the save did not reach the server").not.toBe(was);

    // Leave the install as it was found.
    await page.locator('[role="switch"]').first().click();
    await page.waitForTimeout(800);
  });
});

test.describe("anchor grid", () => {
  // The 3x3 anchor picker replaced a nine-item select, and the thing a select
  // gave for free was keyboard operation. Nine buttons in a grid do not: they
  // need a roving tabindex and two-dimensional arrow keys, both hand-written
  // and neither visible in a screenshot.
  //
  // Asserted through aria-checked rather than a class, because that is what a
  // screen reader and the form both read. A styling regression is cosmetic; an
  // aria-checked regression means the control reports the wrong position to
  // everything that is not a sighted mouse user.
  //
  // Self-sufficient on purpose: it opens the CREATE dialog and fills in what
  // reveals the grid, rather than requiring a rendition to already exist. A
  // test that skips when the fixture is missing is a test that silently stops
  // running.
  test("arrow keys move the selection in two dimensions", async ({ page }) => {
    await signIn(page);
    await page.goto("/renditions");

    await page.getByRole("button", { name: "New rendition" }).first().click();
    await expect(page.getByRole("dialog")).toBeVisible();

    // The overlay geometry only appears once the rendition has a fixed size AND
    // an image, because the overlay is scaled as a percentage of the output.
    // That gate is deliberate -- see RenditionOverlay.problems -- so the test
    // satisfies it rather than working around it.
    await page.locator("#rend-overlay-image").fill("overlays/logo.png");

    const group = page.getByRole("radiogroup", { name: "Position" }).first();
    await expect(group).toBeVisible();

    // Nine, always. Showing every option at once is the whole reason this
    // replaced a select.
    await expect(group.getByRole("radio")).toHaveCount(9);
    await expect(group.getByRole("radio", { checked: true })).toHaveCount(1);

    // EXACT labels after each press, not merely "something changed".
    //
    // The first version of this asserted only that the selection differed after
    // ArrowUp + ArrowLeft, and it passed against a build with vertical movement
    // REMOVED: ArrowUp did nothing, ArrowLeft still moved bottom-right to
    // bottom-centre, the label differed, and the test reported success while
    // the capability it exists to prove was broken. Caught by mutation.
    const selected = () => group.getByRole("radio", { checked: true });
    await expect(selected()).toHaveAttribute("aria-label", "Bottom right");

    // Vertical, asserted on its own so nothing else can satisfy it.
    await selected().focus();
    await page.keyboard.press("ArrowUp");
    await expect(selected()).toHaveAttribute("aria-label", "Middle right");

    // Then horizontal, from the row the previous press moved to.
    await page.keyboard.press("ArrowLeft");
    await expect(selected()).toHaveAttribute("aria-label", "Centre");

    // Edges clamp rather than wrap. Wrapping would mean an operator holding a
    // key sails past the corner they were aiming for.
    await page.keyboard.press("ArrowUp");
    await page.keyboard.press("ArrowUp");
    await expect(selected()).toHaveAttribute("aria-label", "Top centre");

    // Exactly one throughout, or the form has two answers for one setting.
    await expect(group.getByRole("radio", { checked: true })).toHaveCount(1);
  });
});

test.describe("sidebar collapse", () => {
  // The nav is the app shell, so a mistake here is visible on every page. These
  // pin the three decisions that are easy to undo by accident: the label leaves
  // the DOM, the preference survives a reload, and the shortcut does not fire
  // while somebody is typing.

  // useInnerText, throughout this block, on purpose: toContainText defaults to
  // textContent, which reads straight through display:none. A first pass of
  // this suite failed against a correctly-collapsing sidebar because the label
  // spans are still in the DOM -- only their CSS visibility changes -- so the
  // default matcher saw every label concatenated regardless of collapsed
  // state. innerText is what actually distinguishes display:none (leaves the
  // rendered text) from sr-only (does not), which is the property this block
  // means to pin.
  const innerText = { useInnerText: true };

  test("collapsing removes the labels and expanding brings them back", async ({ page }) => {
    await signIn(page);
    await expect(page.locator("nav")).toContainText("Dashboard", innerText);

    await page.getByRole("button", { name: "Toggle navigation" }).click();
    await expect(page.locator("nav")).not.toContainText("Dashboard", innerText);

    await page.getByRole("button", { name: "Toggle navigation" }).click();
    await expect(page.locator("nav")).toContainText("Dashboard", innerText);
  });

  test("the collapsed state survives a reload", async ({ page }) => {
    await signIn(page);
    await page.getByRole("button", { name: "Toggle navigation" }).click();
    await expect(page.locator("nav")).not.toContainText("Dashboard", innerText);

    await page.reload();
    await expect(page.locator("nav")).not.toContainText("Dashboard", innerText);

    // No cleanup here on purpose. Each test gets its own fresh browser context
    // (see the comment on this in auth.setup.ts) seeded from the ONE
    // storageState snapshot playwright.config.ts points every test at --
    // e2e/.auth/state.json, captured before any test ran. localStorage is part
    // of that snapshot, but writes made during this test never reach the file
    // on disk, so the next test's context reads the same unmodified snapshot
    // regardless of what this one left behind. A reset write here would be
    // dead code with nothing downstream to read it.
  });

  test("the shortcut toggles it, and not while typing", async ({ page }) => {
    await signIn(page);
    await page.goto("/chat");
    await expect(page.locator("nav")).toContainText("Dashboard", innerText);

    await page.locator("body").press("ControlOrMeta+b");
    await expect(page.locator("nav")).not.toContainText("Dashboard", innerText);
    await page.locator("body").press("ControlOrMeta+b");
    await expect(page.locator("nav")).toContainText("Dashboard", innerText);

    // The case the guard exists for. The chat composer is a textarea; the
    // sidebar must not move while somebody is writing a message into it.
    const composer = page.locator("textarea").first();
    await composer.click();
    await composer.press("ControlOrMeta+b");
    await expect(page.locator("nav")).toContainText("Dashboard", innerText);
  });

  // Regression guard for a defect found by driving a real browser: the map
  // over NAV used to return a bare <NavLink> when expanded and a
  // <Tooltip><TooltipTrigger asChild> wrapping the SAME <NavLink> when
  // collapsed -- same `key`, but a different element type at the same array
  // position, so React unmounted and remounted the anchor on every toggle
  // instead of reconciling it. A keyboard user tabbed to a link, pressed
  // Ctrl/Cmd+B, and document.activeElement became <body>. Tooltip/TooltipTrigger
  // now mount unconditionally (only TooltipContent is gated on navCollapsed),
  // so the trigger's element type never changes and this must keep passing.
  test("toggling the sidebar with the keyboard shortcut does not drop focus", async ({
    page,
  }) => {
    await signIn(page);
    const routingLink = page.getByRole("link", { name: "Routing" });
    await routingLink.focus();
    await expect(routingLink).toBeFocused();

    await page.keyboard.press("ControlOrMeta+b");
    await expect(page.locator("nav")).not.toContainText("Dashboard", innerText);
    await expect(routingLink).toBeFocused();

    await page.keyboard.press("ControlOrMeta+b");
    await expect(page.locator("nav")).toContainText("Dashboard", innerText);
    await expect(routingLink).toBeFocused();
  });

  // Regression guard for a defect found by driving a real browser: the label
  // span is display:none and the icon is aria-hidden while collapsed, so
  // without an aria-label on the NavLink itself the collapsed rail is
  // fourteen unnamed links -- a WCAG 4.1.2 failure that no text-content
  // assertion above would ever catch, because it is about the accessibility
  // tree, not visible or innerText content.
  test("collapsed links keep their accessible names", async ({ page }) => {
    await signIn(page);
    await page.getByRole("button", { name: "Toggle navigation" }).click();
    await expect(page.locator("nav")).not.toContainText("Dashboard", innerText);

    await expect(page.getByRole("link", { name: "Dashboard" })).toBeVisible();
  });

  // Regression guard for a defect found by driving a real browser: react-router's
  // NavLink accepts a FUNCTION className, but Radix Slot (what TooltipTrigger
  // asChild uses underneath) concatenates className as a string, so the
  // function got stringified into the class attribute before Router ever
  // called it -- every utility on it lost, including `flex` itself. The
  // anchor still passed every text-based assertion above because the
  // stringified source happens to tokenise into mostly-real class names; only
  // a real computed-style check catches the anchor silently falling back to
  // its default `display: block`.
  test("a collapsed nav link still computes display: flex", async ({ page }) => {
    await signIn(page);
    await page.getByRole("button", { name: "Toggle navigation" }).click();
    await expect(page.locator("nav")).not.toContainText("Dashboard", innerText);

    const dashboardLink = page.getByRole("link", { name: "Dashboard" });
    await expect(dashboardLink).toHaveCSS("display", "flex");
  });
});
