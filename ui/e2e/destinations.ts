import type { Page } from "@playwright/test";

/* Shared fixture plumbing for the two specs that need a destination to exist.
 *
 * Both of them create one, drive it and delete it, and both used to carry their
 * own copy of the CSRF dance. The copies were not the problem; the CLEANUP was.
 * A test that fails part-way leaves its row behind, and the next run finds two
 * cards with the same name and dies on a strict-mode violation naming neither
 * the assertion nor the leftover -- so a single real failure poisons every
 * subsequent run until someone works out why. Measured, while mutation-testing
 * this branch.
 *
 * So creation purges first. It is not tidiness: it is what keeps a failing test
 * failing for its own reason on the second run. */

/** Runs a fetch inside the page so it carries the session cookie and CSRF
 *  token, the way every other spec in this suite reaches the API. */
export async function apiFetch<T>(
  page: Page,
  method: string,
  path: string,
  body?: unknown,
): Promise<T> {
  return page.evaluate(
    async ({ m, p, b }) => {
      const match = document.cookie.match(/(?:^|;\s*)polyemesis_csrf=([^;]+)/);
      const csrf = match ? decodeURIComponent(match[1]) : "";
      const res = await fetch(p, {
        method: m,
        credentials: "same-origin",
        headers: {
          ...(b === undefined ? {} : { "Content-Type": "application/json" }),
          ...(csrf ? { "X-CSRF-Token": csrf } : {}),
        },
        body: b === undefined ? undefined : JSON.stringify(b),
      });
      if (!res.ok) throw new Error(`${m} ${p} failed (${res.status})`);
      return res.status === 204 ? null : await res.json();
    },
    { m: method, p: path, b: body },
  );
}

export interface DestinationEnvelope {
  destination: {
    id: number;
    name: string;
    backupIngestWanted?: boolean;
    compliance?: { facebookPrivacy?: string };
    facebook?: {
      crosspost?: { pageId: string; createPost?: boolean }[];
      donateCharityId?: string;
    };
  };
}

/** Deletes every destination carrying this name, so a row abandoned by an
 *  earlier failure cannot make the next run fail for the wrong reason. */
export async function purgeByName(page: Page, name: string) {
  // The list endpoint WRAPS each row -- {destination, routing} -- and reading
  // `row.name` off the envelope silently matched nothing, so the purge ran and
  // deleted none of them. Measured: the second run still died on two cards with
  // the same name.
  const rows = await apiFetch<{ destination: { id: number; name: string } }[]>(
    page,
    "GET",
    "/api/v1/destinations",
  );
  for (const row of rows ?? []) {
    const d = row.destination;
    if (d?.name === name) await apiFetch(page, "DELETE", `/api/v1/destinations/${d.id}`);
  }
}

/** The programme this install has, for the creates that must name one.
 *
 *  Every create now names its source: the server used to fill an omitted
 *  sourceId with the first one, which on a multi-source install attached a
 *  destination to a programme nobody chose and nothing displayed. These specs
 *  run against a single-source fixture, so "the only one" is the honest answer
 *  -- but it is READ BACK rather than assumed to be 1, because an id that is
 *  only ever right while nothing has been deleted is the same assumption the
 *  server stopped making. */
export async function onlySourceId(page: Page): Promise<number> {
  const rows = await apiFetch<{ id: number }[]>(page, "GET", "/api/v1/sources");
  if (!rows?.length) throw new Error("no source to attach to; the fixture creates one in setup");
  return rows[0].id;
}

export async function createFacebookDestination(page: Page, name: string) {
  await purgeByName(page, name);
  const { destination } = await apiFetch<DestinationEnvelope>(page, "POST", "/api/v1/destinations", {
    sourceId: await onlySourceId(page),
    name,
    kind: "rtmp",
    platform: "facebook",
    url: "rtmps://live-api.facebook.com:443/rtmp/",
    streamKey: "e2e-key",
  });
  return destination;
}

/** Cleanup that cannot mask the failure it is cleaning up after.
 *
 * A `finally` that throws replaces the assertion error with its own, and the
 * report then names a closed page rather than the control that never rendered.
 * The row leaking is recoverable -- purgeByName above recovers from it. */
export async function removeDestination(page: Page, id: number) {
  try {
    await apiFetch(page, "DELETE", `/api/v1/destinations/${id}`);
  } catch {
    /* see above */
  }
}
