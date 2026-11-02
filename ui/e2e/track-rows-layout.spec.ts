import { expect, test, type Page } from "@playwright/test";
import { apiFetch, onlySourceId, purgeByName, removeDestination, type DestinationEnvelope } from "./destinations";

/* ROUTING'S TRACK ROWS DO NOT DRAW ON TOP OF THEMSELVES (2026-11 review).
 *
 * At 390px the Mute-and-gain row squeezed five columns into a phone: the
 * meter's L/R labels and its two dB readouts were drawn over the gain slider,
 * the role chip was crushed into the label pile, and "stereo · en" was cut to
 * "ster…". jsdom has no layout, so this is measured in a real browser.
 *
 * This suite never streams (scripts/acceptance-browser.sh publishes no ingest
 * ports), so a probed source and moving meters are supplied in the page: the
 * REST source read and the live socket's `source` frames are dressed with two
 * stereo tracks and their roles, and `levels` frames are sent every 200ms.
 * Every other frame passes through from the real server untouched. */

const NAME = "e2e track-row layout";

const TRACKS = [
  { index: 0, channels: 2, codec: "aac", layout: "stereo", language: "en" },
  { index: 1, channels: 2, codec: "aac", layout: "stereo", language: "es" },
];
const ANNOTATIONS = [
  { track: 0, role: "mic", label: "Host mic", language: "en" },
  { track: 1, role: "commentary", label: "Co-host", language: "es" },
];
const LEVELS = JSON.stringify({
  type: "levels",
  data: {
    peak: [
      [-27.1, -27.1],
      [-27.1, -27.1],
    ],
    rms: [
      [-30, -30],
      [-30, -30],
    ],
  },
});

function dress(src: Record<string, unknown> | null) {
  return { ...(src ?? {}), probed: true, tracks: TRACKS, annotations: ANNOTATIONS };
}

async function fakeAudio(page: Page) {
  await page.route(/\/api\/v1\/source(\?|$)/, async (route) => {
    const res = await route.fetch();
    const body = res.ok() ? await res.json().catch(() => ({})) : {};
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(dress(body)) });
  });
  await page.routeWebSocket(/\/api\/v1\/ws/, (ws) => {
    const server = ws.connectToServer();
    server.onMessage((m) => {
      try {
        const msg = JSON.parse(String(m)) as { type?: string; data?: Record<string, unknown> };
        if (msg.type === "source") return ws.send(JSON.stringify({ ...msg, data: dress(msg.data ?? null) }));
        if (msg.type === "levels") return;
      } catch {
        /* not JSON: pass it through as it came */
      }
      ws.send(m);
    });
    ws.onMessage((m) => server.send(m));
    const timer = setInterval(() => ws.send(LEVELS), 200);
    ws.onClose(() => clearInterval(timer));
  });
}

const PARTS = ["name", "role", "meta", "meter-labels", "meter-db", "slider", "pct", "mute"] as const;
/** Measured one by one as well as by their columns: the L and R readouts can
 *  collide with each other inside a column that collides with nothing. */
const EACH = [
  { part: "meter-label", sel: '[data-part="meter-labels"] > div' },
  { part: "meter-db-readout", sel: '[data-part="meter-db"] > button' },
];

interface Box {
  part: string;
  x: number;
  y: number;
  w: number;
  h: number;
  clipped: boolean;
  spills: boolean;
  sh: number;
  ch: number;
}

async function rowBoxes(page: Page): Promise<Box[][]> {
  return page.$$eval(
    '[data-part="track-grid"]',
    (grids, arg) => {
      const { parts, each } = arg as { parts: string[]; each: { part: string; sel: string }[] };
      const box = (part: string, el: HTMLElement) => {
        const r = el.getBoundingClientRect();
        return {
          part,
          x: r.x,
          y: r.y,
          w: r.width,
          h: r.height,
          // An ellipsis: the text is wider than the box that shows it.
          clipped: el.scrollWidth > el.clientWidth + 1,
          // Text taller than its box spills onto whatever is beside it: a
          // 12px readout in a 7px meter row is drawn over its neighbour's.
          spills: el.scrollHeight > el.clientHeight + 1,
          sh: el.scrollHeight,
          ch: el.clientHeight,
        };
      };
      return grids.map((g) => [
        ...parts.flatMap((part) => {
          const el = g.querySelector<HTMLElement>(`[data-part="${part}"]`);
          return el ? [box(part, el)] : [];
        }),
        ...each.flatMap(({ part, sel }) =>
          Array.from(g.querySelectorAll<HTMLElement>(sel)).map((el, i) => box(`${part}-${i}`, el)),
        ),
      ]);
    },
    { parts: PARTS as unknown as string[], each: EACH },
  );
}

function overlaps(a: Box, b: Box): boolean {
  const e = 0.5;
  return a.x + a.w - e > b.x && b.x + b.w - e > a.x && a.y + a.h - e > b.y && b.y + b.h - e > a.y;
}

let destId = 0;

test.beforeEach(async ({ page }) => {
  await page.goto("/");
  await purgeByName(page, NAME);
  const { destination } = await apiFetch<DestinationEnvelope>(page, "POST", "/api/v1/destinations", {
    sourceId: await onlySourceId(page),
    name: NAME,
    kind: "rtmp",
    platform: "custom",
    url: "rtmp://127.0.0.1:1/live",
    streamKey: "e2e-layout",
  });
  destId = destination.id;
  await fakeAudio(page);
});

test.afterEach(async ({ page }) => {
  await removeDestination(page, destId);
});

// The two phone widths the review names, then a sweep up to the desktop the
// console is drawn for, so a width between the two layouts is covered too.
for (const width of [360, 390, 430, 520, 640, 768, 900, 1024, 1280, 1440]) {
  test(`at ${width}px no part of a track row overlaps another, and nothing is cut to an ellipsis`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.goto(`/routing/${destId}`);
    const grids = page.locator('[data-part="track-grid"]');
    await expect(grids).toHaveCount(TRACKS.length);
    // Every part present before measuring: the role chip from the annotations,
    // the dB readouts from the first levels frame.
    for (const part of PARTS) {
      await expect(grids.first().locator(`[data-part="${part}"]`)).toBeVisible();
    }
    await expect(grids.first().locator('[data-part="meter-db"] button').first()).toHaveText(/dB/);

    for (const boxes of await rowBoxes(page)) {
      const top = boxes.filter((b) => !/-\d+$/.test(b.part));
      expect(top.map((b) => b.part).sort()).toEqual([...PARTS].sort());
      for (const b of boxes) {
        expect(b.w, `${b.part} has no width`).toBeGreaterThan(0);
      }
      for (const b of boxes.filter((x) => x.part === "role" || x.part === "meta")) {
        expect(b.clipped, `${b.part} is cut to an ellipsis at ${width}px`).toBe(false);
      }
      for (const b of boxes.filter((x) => /^meter-(label|db-readout)-\d+$/.test(x.part))) {
        expect(b.spills, `${b.part} is ${b.sh}px of text in a ${b.ch}px meter row at ${width}px`).toBe(false);
      }
      // A column against its own contents is not a collision.
      const within = (a: string, b: string) =>
        (a === "meter-labels" && b.startsWith("meter-label-")) ||
        (a === "meter-db" && b.startsWith("meter-db-readout-"));
      const hits: string[] = [];
      for (let i = 0; i < boxes.length; i++) {
        for (let j = i + 1; j < boxes.length; j++) {
          const [a, b] = [boxes[i].part, boxes[j].part];
          if (within(a, b) || within(b, a)) continue;
          if (overlaps(boxes[i], boxes[j])) hits.push(`${a} × ${b}`);
        }
      }
      expect(hits, `overlapping parts at ${width}px`).toEqual([]);
    }
  });
}
