import { expect, test, type Page } from "@playwright/test";

import { watchConsole } from "./console";
import { createFacebookDestination, removeDestination } from "./destinations";

/* ===========================================================================
   What the chrome and the destination cards SAY about a live system.

   These three claims used to be asserted in Go, by reading AppLayout.tsx,
   useLiveData.ts and DestinationCard.tsx as text:

     internal/db/ingest_header_drift_test.go matched the literal string
       `ingestLive ? "live" : toneForState(ingest?.state)`
     ...and matched `source?.probed` and `bitrate` inside useIngestLive's body,
     internal/db/facebook_ui_drift_test.go matched the href template and
       `dest.backupProcess.state` inside their JSX blocks.

   Matching a ternary's source text says nothing about what a browser puts on
   screen. It is also brittle in the wrong direction: reformatting that line
   turns the guard red while the header is perfectly correct, and rewriting the
   header to consult the process again -- the exact regression -- could keep it
   green as long as the characters survived somewhere.

   The reason those claims were stuck in Go was that the state they describe
   cannot be produced by this suite: it never streams, so ingest is never live,
   and no backup feed ever runs. The way through is to stub the LIVE PAYLOAD
   rather than the component. The real AppLayout, the real useIngestLive, the
   real DestinationCard all render; only the JSON they are handed is ours. Each
   stub PATCHES the server's own response rather than inventing one, so a field
   that changes shape breaks these tests instead of being papered over.

   The WebSocket is intercepted and left unconnected. Without that the server
   republishes real status every two seconds and overwrites the stub a moment
   after the first paint, which is a flake rather than a failure.

   Presence is never the assertion. Each test asserts what the chrome SAYS, and
   pairs the interesting case with its opposite so a page that renders an error
   boundary -- or one that says "Live" unconditionally -- fails.
   =========================================================================== */

/* AN INTERCEPTOR THAT NEVER MATCHES FAILS OPEN, AND THAT IS THE HAZARD.
   These stubs were pinned with globs ending at the path -- a double-star,
   then `/api/v1/status` -- which match a URL with no query string and nothing
   else. When the client began NAMING A
   PROGRAMME (`/status?source=3`), every one of them silently stopped matching:
   the stubs vanished, the muted socket reconnected for real and republished
   live status over the seed, and these tests began asserting against whatever
   the server happened to say.

   Three of them went red, which is survivable. One went GREEN -- "no bytes
   arriving reads Offline", passing because an unstubbed console also reads
   Offline -- and a test that passes for the wrong reason is the exact thing
   this file was written to prevent.

   So: match the path with OR WITHOUT a query, and keep a registry that fails
   the test when an interceptor was never hit. Silence is no longer one of the
   available outcomes. */
const scoped = (path: string) => new RegExp(`/api/v1/${path}(\\?|$)`);

type Interceptor = { what: string; hits: number };
const registered: Interceptor[] = [];

function track(what: string): Interceptor {
  const seen = { what, hits: 0 };
  registered.push(seen);
  return seen;
}

/** Waits until every interceptor this test registered has actually served a
 *  request.
 *
 *  Needed because several of these assertions EXPECT THE SAME VALUE THE FIRST
 *  PAINT ALREADY SHOWS -- the chrome reads "Offline" before any data arrives,
 *  which is also the correct answer when no bytes are flowing. Asserting it
 *  straight after `goto` therefore passes in milliseconds, before the stubs
 *  have been read, and would go on passing if they were never read at all.
 *  That is not a slow test, it is an empty one. */
async function seeded() {
  await expect
    .poll(() => registered.filter((i) => i.hits === 0).map((i) => i.what), {
      timeout: 15_000,
      message: "the page never fetched what this test stubbed",
    })
    .toEqual([]);
}

test.afterEach(() => {
  const missed = registered.filter((i) => i.hits === 0).map((i) => i.what);
  registered.length = 0;
  expect(
    missed,
    "an interceptor this test registered never matched a request, so the page " +
      "was reading the real server and the assertions above were made against " +
      "data the test did not control",
  ).toEqual([]);
});

/** Silences the live socket so the REST seed below is the only thing the page
 *  is told. The handler deliberately never calls connectToServer(). */
async function muteSocket(page: Page) {
  const seen = track("the live WebSocket");
  await page.routeWebSocket(scoped("ws"), () => {
    seen.hits++;
  });
}

/** Rewrites GET /status through the real handler, so everything not named here
 *  is the server's own answer. */
async function patchStatus(page: Page, patch: (status: Record<string, unknown>) => void) {
  const seen = track("GET /status");
  await page.route(scoped("status"), async (route) => {
    seen.hits++;
    const res = await route.fetch();
    const body = (await res.json()) as Record<string, unknown>;
    patch(body);
    return route.fulfill({ response: res, json: body });
  });
}

/** The bitrate series the chrome's "am I on air" answer is derived from.
 *  useIngestLive reads the last five samples only, so a series shorter than
 *  that is entirely recent. */
async function patchBitrate(page: Page, kbps: number) {
  const seen = track("GET /stats");
  await page.route(scoped("stats"), async (route) => {
    seen.hits++;
    const res = await route.fetch();
    const body = (await res.json()) as Record<string, unknown>;
    body.bitrate = [1, 2, 3].map((i) => ({ t: new Date(Date.now() - i * 1000).toISOString(), kbps }));
    return route.fulfill({ response: res, json: body });
  });
}

async function patchSource(page: Page, probed: boolean) {
  const seen = track("GET /source");
  await page.route(scoped("source"), async (route) => {
    seen.hits++;
    const res = await route.fetch();
    const body = (await res.json()) as Record<string, unknown>;
    body.probed = probed;
    return route.fulfill({ response: res, json: body });
  });
}

const ingestStatus = (page: Page) => page.getByTestId("chrome-ingest-status");

/* The header's ingest indicator must not decide health from the ingest PROCESS.
 *
 * SRT has no ingest child. engine.reconcileIngest returns early for it on
 * purpose -- srtserver delivers datagrams straight into the hub, and a second
 * thing on that socket crash-loops behind a listener that was working fine.
 * So `status.ingest` is null for SRT, `stateLabel(undefined)` is "Offline", and
 * every healthy SRT install said its ingest was down in the most prominent
 * status in the application chrome. It was found in a screenshot, not by a
 * test, and twice nearly dismissed as an artefact of the capture harness.
 *
 * That state is exactly reproducible here: ingest null, source probed, bytes
 * arriving. */
test.describe("the chrome answers 'am I on air' from arriving bytes", () => {
  test("a probed source with bytes arriving reads Live even with no ingest process", async ({
    page,
  }) => {
    const console_ = watchConsole(page);
    await muteSocket(page);
    await patchStatus(page, (s) => {
      // The SRT shape: no ingest child, by design.
      s.ingest = null;
    });
    await patchSource(page, true);
    await patchBitrate(page, 4200);

    await page.goto("/");
    await expect(page.locator("nav")).toBeVisible();
    await seeded();

    await expect(
      ingestStatus(page),
      "the chrome reports a healthy SRT ingest as down. An SRT source has no " +
        "ingest child by design, so a header reading the process state calls " +
        "every working SRT install Offline in the first thing an operator looks at",
    ).toHaveText("Live");

    expect(console_.errors, "the chrome logged errors").toEqual([]);
  });

  // The control, and it carries its own claim: useIngestLive must keep deriving
  // from the RELAY. If it were repointed at status.ingest.progress the test
  // above would still pass on some other transport, and this one is what says
  // the answer tracks the bytes rather than being pinned on.
  test("the same source with no bytes arriving reads Offline", async ({ page }) => {
    const console_ = watchConsole(page);
    await muteSocket(page);
    await patchStatus(page, (s) => {
      s.ingest = null;
    });
    await patchSource(page, true);
    await patchBitrate(page, 0);

    await page.goto("/");
    await expect(page.locator("nav")).toBeVisible();
    await seeded();

    await expect(
      ingestStatus(page),
      "the chrome says the stream is up when no bytes are arriving. A false " +
        "'live' is worse than a missed one: it trains the operator to stop " +
        "reading the indicator",
    ).toHaveText("Offline");

    expect(console_.errors, "the chrome logged errors").toEqual([]);
  });

  // The other half of the derivation. The probe survives the publisher going
  // away, and the bitrate series briefly reads zero between reconnects, so both
  // signals are required and each needs its own case.
  test("bytes arriving at a source that has never probed still reads Offline", async ({ page }) => {
    const console_ = watchConsole(page);
    await muteSocket(page);
    await patchStatus(page, (s) => {
      s.ingest = null;
    });
    await patchSource(page, false);
    await patchBitrate(page, 4200);

    await page.goto("/");
    await expect(page.locator("nav")).toBeVisible();
    await seeded();

    await expect(
      ingestStatus(page),
      "the chrome calls a source live before anything has been probed, so " +
        "arriving noise that no decoder accepted is reported as a broadcast",
    ).toHaveText("Offline");

    expect(console_.errors, "the chrome logged errors").toEqual([]);
  });
});

/* The destination card's redundancy and broadcast reporting.
 *
 * A backup nobody can see is worse than no backup: the operator believes they
 * have redundancy. A backup feed only exists while one is running, which this
 * suite never does, so the card's state is stubbed onto the destination the
 * server really reports. */
test.describe("the destination card reports the backup feed and the broadcast", () => {
  test("a dead backup reads differently from a healthy one", async ({ page }) => {
    const console_ = watchConsole(page);
    await page.goto("/");
    await expect(page.locator("nav")).toBeVisible();
    await seeded();
    const created = await createFacebookDestination(page, "e2e backup card");

    try {
      await muteSocket(page);

      // Healthy first.
      await patchStatus(page, (s) => {
        const dests = s.destinations as Record<string, unknown>[];
        for (const d of dests) {
          if (d.id !== created.id) continue;
          d.backupProcess = { name: "backup", kind: "backup", state: "running", pid: 1, restarts: 0, uptimeSec: 90, progress: {} };
        }
      });
      await page.goto("/");
      const healthy = page.getByText("backup feed:").locator("..");
      await expect(
        healthy,
        "the card does not report the backup feed's state at all, so an operator " +
          "who enabled redundancy has no way to know whether they got it",
      ).toContainText("Live");

      // Dead. The two states must not be told apart by colour alone -- that is
      // the bug the card's own comment records, where a healthy backup rendered
      // in a class Tailwind never emitted.
      await page.unroute("**/api/v1/status");
      await patchStatus(page, (s) => {
        const dests = s.destinations as Record<string, unknown>[];
        for (const d of dests) {
          if (d.id !== created.id) continue;
          d.backupProcess = { name: "backup", kind: "backup", state: "failed", pid: 0, restarts: 7, uptimeSec: 0, progress: {} };
        }
      });
      await page.goto("/");
      await expect(
        page.getByText("backup feed:").locator(".."),
        "a backup that has been dead for an hour reads identically to a healthy " +
          "one, so the operator believes they have redundancy they do not have",
      ).toContainText("Failed");
    } finally {
      await removeDestination(page, created.id);
    }

    expect(console_.errors, "the destination card logged errors").toEqual([]);
  });

  test("a backup that was asked for and not created says why", async ({ page }) => {
    const console_ = watchConsole(page);
    await page.goto("/");
    await expect(page.locator("nav")).toBeVisible();
    await seeded();
    const created = await createFacebookDestination(page, "e2e backup error card");

    const why = "Facebook returned no backup ingest URL for this broadcast.";
    try {
      await muteSocket(page);
      await patchStatus(page, (s) => {
        const dests = s.destinations as Record<string, unknown>[];
        for (const d of dests) {
          if (d.id !== created.id) continue;
          d.backupError = why;
        }
      });
      await page.goto("/");
      await expect(
        page.getByText(why),
        "an operator who enabled redundancy and did not get it is never told why",
      ).toBeVisible();
    } finally {
      await removeDestination(page, created.id);
    }

    expect(console_.errors, "the destination card logged errors").toEqual([]);
  });

  // A public event page is created on the operator's behalf; giving them no way
  // to reach it is half a feature. The HREF is the assertion, not the link: a
  // link that renders and points nowhere useful is the same missing feature.
  //
  // MUTATION: DestinationCard.tsx, drop the interpolation from the href --
  // `https://facebook.com/${dest.facebookBroadcastId}` becomes
  // `https://facebook.com/`. The <a> still renders, still carries the exact
  // text "Scheduled Facebook broadcast", so both a presence assertion and any
  // source-text grep for the link keep passing; only the destination is wrong,
  // which is the failure an operator actually meets. Measured: FAIL on the
  // toHaveAttribute below, Expected "https://facebook.com/10203040506070" /
  // Received "https://facebook.com/". The other 6 tests in this file passed.
  // Restored from a file copy; `git diff --stat` empty.
  test("the card links to the scheduled Facebook broadcast", async ({ page }) => {
    const console_ = watchConsole(page);
    await page.goto("/");
    await expect(page.locator("nav")).toBeVisible();
    await seeded();
    const created = await createFacebookDestination(page, "e2e broadcast link card");

    try {
      await muteSocket(page);
      await patchStatus(page, (s) => {
        const dests = s.destinations as Record<string, unknown>[];
        for (const d of dests) {
          if (d.id !== created.id) continue;
          d.facebookBroadcastId = "10203040506070";
        }
      });
      await page.goto("/");
      await expect(
        page.getByRole("link", { name: "Scheduled Facebook broadcast" }),
        "the card does not link to the broadcast polyemesis created, so the " +
          "operator has no way to reach the event page or to see that it is dead",
      ).toHaveAttribute("href", "https://facebook.com/10203040506070");
    } finally {
      await removeDestination(page, created.id);
    }

    expect(console_.errors, "the destination card logged errors").toEqual([]);
  });
});
