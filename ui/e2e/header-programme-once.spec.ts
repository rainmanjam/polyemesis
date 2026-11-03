import { expect, test, type Page } from "@playwright/test";

/* ON A PHONE THE HEADER NAMES THE PROGRAMME ONCE (2026-11 review).
 *
 * Below sm, a multi-programme header printed the programme twice: in the
 * on-air chip beside the status dot ("● Main Pr…") and in the programme
 * switcher beside it. The switcher is the one that can be acted on, so it
 * keeps the name; the chip keeps its dot and its state words, and carries
 * the programme for screen readers only.
 *
 * The suite runs against a one-source install, where there is no switcher at
 * all, so the source list is answered with that source plus a second one. */

// A real-length name, the demo install's, so the widths below are the ones
// an operator with that programme sees.
const PROGRAMME = "Main Programme";

let firstId = 0;

async function twoProgrammes(page: Page) {
  await page.route(/\/api\/v1\/sources$/, async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const res = await route.fetch();
    const rows = (await res.json()) as Record<string, unknown>[];
    firstId = Number(rows[0].id);
    const first = { ...rows[0], name: `${PROGRAMME} — Studio A` };
    const second = { ...rows[0], id: Number(rows[0].id) + 1000, name: "Second Programme — Studio B", isDefault: false };
    await route.fulfill({ response: res, json: [first, second, ...rows.slice(1)] });
  });
}

/** Visible text in the header that names the programme, one entry per element. */
async function namesInHeader(page: Page): Promise<string[]> {
  return page.$$eval("header *", (els, name) => {
    const out: string[] = [];
    for (const el of els as HTMLElement[]) {
      const own = Array.from(el.childNodes)
        .filter((n) => n.nodeType === Node.TEXT_NODE)
        .map((n) => n.textContent ?? "")
        .join("");
      // The truncated chip read "Main Pr…": a prefix of the name counts.
      const shown = own.replace(/…$/, "").trim();
      if (shown.length < 4 || !(name as string).startsWith(shown.slice(0, Math.min(shown.length, (name as string).length)))) continue;
      const r = el.getBoundingClientRect();
      const cs = getComputedStyle(el);
      const hidden =
        r.width < 2 || r.height < 2 || cs.visibility === "hidden" || cs.display === "none" ||
        // sr-only: clipped to a 1px box.
        (r.width <= 1 && r.height <= 1) || cs.clip === "rect(0px, 0px, 0px, 0px)";
      if (!hidden) out.push(shown);
    }
    return out;
  }, PROGRAMME);
}

for (const width of [360, 390]) {
  test(`at ${width}px the header shows the programme name once, in the switcher`, async ({ page }) => {
    await twoProgrammes(page);
    await page.setViewportSize({ width, height: 800 });
    await page.goto("/");
    const switcher = page.getByTestId("programme-switcher");
    await expect(switcher).toBeVisible();
    await expect(switcher).toContainText(PROGRAMME);

    const shown = await namesInHeader(page);
    expect(shown, `programme name visible in the header at ${width}px`).toHaveLength(1);

    // The on-air chip keeps its dot and still names the programme for a
    // screen reader.
    const chip = page.getByTestId("mobile-on-air");
    await expect(chip).toBeVisible();
    await expect(chip).toContainText(PROGRAMME);
    // ...and only for a screen reader. Checked on the markup as well as on
    // what is visible: at 360px a chip printing the name is squeezed to its
    // dot, so the measurement above alone would let the duplicate back in.
    const printed = await chip.evaluate((el) =>
      Array.from(el.children)
        .filter((c) => !c.classList.contains("sr-only"))
        .map((c) => c.textContent ?? "")
        .join(" "),
    );
    expect(printed).not.toContain(PROGRAMME);
    // The dot survives the squeeze.
    const dot = await chip.evaluate((el) => (el.firstElementChild as HTMLElement).getBoundingClientRect().width);
    expect(dot).toBeGreaterThan(0);
  });
}

test("at 1440px the desktop header is unchanged: the switcher names the programme", async ({ page }) => {
  await twoProgrammes(page);
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/");
  await expect(page.getByTestId("programme-switcher")).toContainText(`${PROGRAMME} — Studio A`);
  await expect(page.getByTestId("mobile-on-air")).toBeHidden();
});

/* NO ELLIPSIS IN THE CHIP, AT ANY PHONE WIDTH.
 *
 * At 360px the chip read "O…". Where the full words do not fit the header's
 * room, it prints the short form whole ("Off"; "3 live" is already short in
 * English). Measured off air and on air; on air is three running
 * destinations on the selected programme, written into the status the page
 * reads (REST and socket), since this suite never streams. */

const LIVE = 3;

function dressStatus(data: Record<string, unknown> | null) {
  const running = Array.from({ length: LIVE }, (_, i) => ({
    id: 900_000 + i,
    sourceId: firstId,
    enabled: true,
    process: { name: `dest:${900_000 + i}`, state: "running", restarts: 0, uptimeSec: 60 },
  }));
  return { ...(data ?? {}), destinations: running };
}

async function onAir(page: Page) {
  await page.route(/\/api\/v1\/status(\?|$)/, async (route) => {
    const res = await route.fetch();
    const body = res.ok() ? await res.json().catch(() => ({})) : {};
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(dressStatus(body)) });
  });
  await page.routeWebSocket(/\/api\/v1\/ws/, (ws) => {
    const server = ws.connectToServer();
    server.onMessage((m) => {
      try {
        const msg = JSON.parse(String(m)) as { type?: string; data?: Record<string, unknown> };
        if (msg.type === "status") return ws.send(JSON.stringify({ ...msg, data: dressStatus(msg.data ?? null) }));
      } catch {
        /* not JSON: pass it through */
      }
      ws.send(m);
    });
    ws.onMessage((m) => server.send(m));
  });
}

interface ChipRead {
  text: string;
  fit: string | null;
  clipped: boolean;
  chipClipped: boolean;
  overlapsNext: boolean;
  headerOverflows: boolean;
}

async function readChip(page: Page): Promise<ChipRead> {
  return page.getByTestId("mobile-on-air").evaluate((chip) => {
    const words = chip.querySelector<HTMLElement>('[data-testid="mobile-on-air-words"]');
    const next = chip.nextElementSibling as HTMLElement | null;
    return {
      text: words?.textContent ?? "",
      fit: words?.getAttribute("data-fit") ?? null,
      clipped: !words || words.scrollWidth > words.clientWidth + 1,
      chipClipped: (chip as HTMLElement).scrollWidth > (chip as HTMLElement).clientWidth + 1,
      overlapsNext: !!next && chip.getBoundingClientRect().right > next.getBoundingClientRect().left + 0.5,
      // A chip that will not shrink can still push the language and sign-out
      // buttons off the right edge instead.
      headerOverflows: (() => {
        const h = chip.closest("header") as HTMLElement;
        return h.scrollWidth > h.clientWidth + 1;
      })(),
    };
  });
}

// What the chip prints at each width, in English, beside a "Main Programme"
// switcher. 360 and 390 have room for "Off air"; 320 does not, and prints
// "Off". "3 live" is its own short form.
const EXPECT: Record<number, { off: string; live: string }> = {
  320: { off: "Off", live: `${LIVE} live` },
  360: { off: "Off air", live: `${LIVE} live` },
  390: { off: "Off air", live: `${LIVE} live` },
};

for (const width of [320, 360, 390]) {
  for (const state of ["off", "live"] as const) {
    test(`at ${width}px the ${state === "off" ? "off-air" : "live"} chip prints its words whole`, async ({ page }) => {
      await twoProgrammes(page);
      if (state === "live") await onAir(page);
      await page.setViewportSize({ width, height: 800 });
      await page.goto("/settings");
      const words = page.getByTestId("mobile-on-air-words");
      await expect(words).toHaveText(EXPECT[width][state]);

      const r = await readChip(page);
      expect(r.text, "the chip's words carry an ellipsis").not.toContain("…");
      expect(r.clipped, `the chip's words are cut at ${width}px`).toBe(false);
      expect(r.chipClipped, `the chip itself is cut at ${width}px`).toBe(false);
      expect(r.overlapsNext, `the chip runs into the logo at ${width}px`).toBe(false);
      expect(r.headerOverflows, `the header overflows at ${width}px`).toBe(false);
      // Off air has two forms; "3 live" is its own short form, so either fit
      // prints the same words and only the off-air reading says which.
      if (state === "off") expect(r.fit).toBe(EXPECT[width].off === "Off air" ? "full" : "short");

      // The whole sentence, programme included, for a screen reader and in
      // the title.
      const chip = page.getByTestId("mobile-on-air");
      const sentence = state === "off" ? `${PROGRAMME} — Studio A: Off air` : `${PROGRAMME} — Studio A: ${LIVE} live`;
      await expect(chip.locator(".sr-only")).toHaveText(sentence);
      await expect(chip).toHaveAttribute("title", sentence);

      // On air is the tally lamp (row 10): the dot is painted --tally, not
      // the --ok green, compared as computed colours in the real page.
      if (state === "live") {
        const paint = await page.getByTestId("mobile-on-air-dot").evaluate((dot) => {
          const probe = (v: string) => {
            const p = document.createElement("span");
            p.style.backgroundColor = `var(${v})`;
            document.body.append(p);
            const c = getComputedStyle(p).backgroundColor;
            p.remove();
            return c;
          };
          return { dot: getComputedStyle(dot).backgroundColor, tally: probe("--tally"), ok: probe("--ok") };
        });
        expect(paint.dot).toBe(paint.tally);
        expect(paint.dot).not.toBe(paint.ok);
      }
    });
  }
}

test("no ellipsis in the chip at any width from 320 to 430px", async ({ page }) => {
  await twoProgrammes(page);
  await page.goto("/settings");
  for (let width = 320; width <= 430; width += 10) {
    await page.setViewportSize({ width, height: 800 });
    await expect(page.getByTestId("mobile-on-air-words")).toHaveText(/^(Off air|Off)$/);
    const r = await readChip(page);
    expect(r.text, `ellipsis at ${width}px`).not.toContain("…");
    expect(r.clipped, `words cut at ${width}px`).toBe(false);
    expect(r.overlapsNext, `chip runs into the logo at ${width}px`).toBe(false);
    expect(r.headerOverflows, `header overflows at ${width}px`).toBe(false);
  }
});

