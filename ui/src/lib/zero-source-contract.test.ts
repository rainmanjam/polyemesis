/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

/* An install with no source has no engine, so every live endpoint is served by
 * a nil *engine.Engine. What it answers with is a contract between two
 * languages, and this file pins the half TypeScript cannot check.
 *
 * The specific lie this exists to catch: `Status.renditions` and
 * `Status.destinations` are declared NON-NULLABLE here, so every screen that
 * renders them maps over them without a guard. A Go nil slice marshals to
 * `null`, and `null.map` is a blank page with a stack trace in the console --
 * on the very first load of a fresh install, which is the one moment the
 * operator has no other screen to fall back to. Nothing in a typecheck notices,
 * because the type says it cannot happen.
 *
 * The EXECUTABLE half lives in Go, where the values are:
 * internal/engine/nil_receiver_reads_test.go marshals a zero-source Status and
 * asserts the arrays, and internal/api/zero_source_reads_test.go drives the
 * real route and the WebSocket burst. This file asserts the two ends still
 * describe the same shape -- it is the same technique, and the same limitation,
 * as service-registry-drift.test.ts one layer up.
 */

const ROOT = new URL("../../../", import.meta.url).pathname;

function read(rel: string): string {
  return readFileSync(join(ROOT, rel), "utf8");
}

/** The body of one interface in types.ts, as text. */
function interfaceBody(name: string): string {
  const src = read("ui/src/lib/types.ts");
  const start = src.indexOf(`export interface ${name} {`);
  expect(start, `no interface ${name} in types.ts`).toBeGreaterThan(-1);
  const end = src.indexOf("\n}", start);
  expect(end, `interface ${name} is unterminated`).toBeGreaterThan(start);
  return src.slice(start, end);
}

/** The line declaring one field, without its trailing comment. */
function fieldLine(iface: string, field: string): string {
  const line = interfaceBody(iface)
    .split("\n")
    .find((l) => new RegExp(`^\\s{2}${field}\\??:`).test(l));
  expect(line, `no field ${field} on ${iface}`).toBeDefined();
  return (line as string).trim();
}

/** The opening tag of the `<tag ...>` whose attributes contain `needle`.
 *
 *  Written as a scan rather than a regexp so that an assertion can be made
 *  about ONE element rather than about the file. A guard phrased as "this
 *  spelling appears nowhere" only ever catches the spelling it was written
 *  against; a guard phrased as "this element carries no such attribute, under
 *  any name" survives the rename that a later refactor will reach for. The
 *  scan tracks `{}` depth because a handler's `() =>` contains a `>` that a
 *  naive search for the tag's end would stop at, silently truncating the text
 *  every assertion below is made over -- which is the same failure mode in a
 *  new place. */
function jsxTagWith(src: string, tag: string, needle: string): string {
  for (let at = src.indexOf(`<${tag}`); at !== -1; at = src.indexOf(`<${tag}`, at + 1)) {
    let depth = 0;
    let end = -1;
    for (let i = at; i < src.length && end === -1; i++) {
      if (src[i] === "{") depth++;
      else if (src[i] === "}") depth--;
      else if (src[i] === ">" && depth === 0) end = i;
    }
    expect(end, `<${tag}> near offset ${at} is never closed`).toBeGreaterThan(-1);
    const text = src.slice(at, end + 1);
    if (text.includes(needle)) return text;
  }
  throw new Error(`no <${tag}> whose attributes contain ${needle}`);
}

describe("the status the dashboard renders on an install with no source", () => {
  it("is declared as arrays the UI may map over without a guard", () => {
    for (const field of ["renditions", "destinations"]) {
      const line = fieldLine("Status", field);
      expect(line, `Status.${field} is optional; the pages that render it do not check`)
        .not.toMatch(/^\w+\?:/);
      expect(line, `Status.${field} admits null; then every .map over it is a crash`)
        .not.toMatch(/\bnull\b/);
      expect(line, `Status.${field} is no longer an array`).toMatch(/\[\]/);
    }
  });

  it("is served as empty arrays by the nil-receiver branch, not as the bare zero value", () => {
    const src = read("internal/engine/status.go");
    const guard = src.slice(
      src.indexOf("func (e *Engine) Status() Status {"),
      src.indexOf("e.mu.RLock()", src.indexOf("func (e *Engine) Status() Status {")),
    );
    expect(guard, "Engine.Status has no nil-receiver branch at all, so an install " +
      "with no source panics before it can answer anything").toContain("if e == nil");
    /* `return Status{}` would satisfy the Go compiler, pass any test that only
     * checks for a 200, and hand the UI two nulls. */
    expect(guard, "the nil-receiver Status does not name an empty renditions slice")
      .toMatch(/Renditions:\s*\[\]RenditionStatus\{\}/);
    expect(guard, "the nil-receiver Status does not name an empty destinations slice")
      .toMatch(/Destinations:\s*\[\]DestStatus\{\}/);
    /* loudness is not on the Status interface here YET, and that is exactly why
     * it is asserted in Go rather than skipped: the field is already on the
     * wire, it has no omitempty, and both GET /api/v1/loudness and
     * Engine.Loudness normalise it to []. Whoever adds it to types.ts will
     * declare it non-nullable to match what a running server sends, and would
     * inherit a null from this one branch on the first load of a fresh install.
     */
    expect(guard, "the nil-receiver Status does not name an empty loudness slice, so it " +
      "sends null for a field every other producer sends [] for")
      .toMatch(/Loudness:\s*\[\]meters\.Report\{\}/);
  });
});

describe("the setup status, the only thing a browser can read before signing in", () => {
  it("carries the source count on both sides", () => {
    const handler = read("internal/api/handlers.go");
    const setup = handler.slice(
      handler.indexOf("func (s *Server) handleSetupStatus("),
      handler.indexOf("func (s *Server) handleSetup("),
    );
    expect(setup, "handleSetupStatus no longer reports how many sources exist; the " +
      "empty state has nothing to key off and a fresh install looks like a broken one")
      .toContain(`body["sources"]`);

    const client = read("ui/src/lib/api.ts");
    const call = client.slice(client.indexOf("setupStatus: () =>"));
    expect(call.slice(0, 200), "the setupStatus client type does not declare sources, " +
      "so the field arrives and TypeScript says it is not there").toMatch(/sources\?:\s*number/);
  });
});

/* The screens an operator meets on an install with no source.
 *
 * TEXT, not a rendered DOM, and the limitation is the one this file already
 * accepts for the Go side: there is no component harness in this project, and
 * inventing one for three branches would be a larger change than the branches.
 * What these catch is the failure that actually happened during review -- a
 * branch deleted, or its condition inverted, leaving a page that renders a
 * pipeline for a programme that does not exist and invites the operator to
 * click the one control that cannot work.
 *
 * The half a DOM test would add is whether the card LOOKS right; the half this
 * has is whether the decision is still being made at all, and only the second
 * one has ever been wrong here.
 */
describe("the screens that meet an install with no source", () => {
  it("the dashboard draws the empty state instead of an empty pipeline", () => {
    const src = read("ui/src/pages/Dashboard.tsx");
    expect(src, "the dashboard no longer reads how many sources exist, so it cannot " +
      "tell a fresh install from a running one").toContain("await api.setupStatus()");
    expect(src, "the dashboard renders its pipeline unconditionally again: a preview of " +
      "a stream nobody is sending, an ingest URL no encoder can publish to, and an Add " +
      "destination button that answers 503").toMatch(/sourceCount === 0/);
    expect(src, "the dashboard no longer renders NoProgrammeYet").toContain("<NoProgrammeYet");
  });

  it("the dashboard keeps re-reading the count rather than reading it once", () => {
    const src = read("ui/src/pages/Dashboard.tsx");
    /* Status events are published BY an engine, so an install that loses its
     * last source stops sending them and the last frame stays on screen. Every
     * control that bumps refreshKey is inside the pipeline the empty state
     * replaces, so a count read once per refreshKey is a count that can never
     * be re-read once it matters. */
    expect(src, "the dashboard reads the source count once and never again, so a tab " +
      "left open while the last source went away goes on drawing a publish URL and a " +
      "track count for a programme that no longer exists")
      .toMatch(/setInterval\(readSourceCount/);
  });

  it("the dashboard treats the refusal as an empty state rather than a fault", () => {
    const src = read("ui/src/pages/Dashboard.tsx");
    /* THE RED TOAST. Every destination control is behind requireSource, so a
     * source that goes away turns each of them into a 503 -- and
     * `toast.error("Could not start the destination")` is a fault report for a
     * state in which nothing has failed. */
    expect(src, "the destination controls no longer branch on the no-source code, so a " +
      "503 that means \"this install has nothing yet\" is drawn as a failure")
      .toContain("isNoSource(err)");
    /* AND IT ASKS RATHER THAN ASSUMING. requireSource refuses on "no engine is
     * running", which is also the state of an install whose sources are all
     * present and whose engines failed to start. `setSourceCount(0)` there told
     * that operator to create a source they already have, on a screen with no
     * way back to the one that could have explained it. */
    expect(src, "the dashboard forces the count to zero from the refusal instead of " +
      "re-reading it, so an install whose engines did not start is told it has no source")
      .not.toMatch(/isNoSource\(err\)\)\s*\{\s*setSourceCount\(0\)/);
    expect(src, "the refusal branch does not re-read the count")
      .toMatch(/isNoSource\(err\)\)\s*\{\s*const n = await readSourceCount\(\)/);
  });

  it("the destination dialog does not raise a fault toast for an empty install", () => {
    const src = read("ui/src/components/DestinationDialog.tsx");
    /* The one control that survives on the dashboard long enough to be clicked
     * after the last source has gone. A bare toast.error there reinstates the
     * scarlet "this install has no source yet" the empty state exists to
     * replace. */
    expect(src, "the destination dialog red-toasts the no-source refusal again")
      .toContain("isNoSource(err)");
  });

  it("the settings page does not open the tab it cannot save", () => {
    const src = read("ui/src/pages/SettingsPage.tsx");
    /* The ingest belongs to the source row. With no source there is nothing to
     * write it through to, the server refuses a change to it, and this was the
     * DEFAULT tab -- so the first settings page a first-time operator opened
     * was a form that could not be saved. */
    expect(src, "the settings page defaults to the ingest tab unconditionally again")
      .not.toMatch(/params\.get\("tab"\)\s*\?\?\s*"ingest"/);
    expect(src, "the settings page's default tab does not consider the source count")
      .toMatch(/params\.get\("tab"\)\s*\?\?\s*\(sourceCount === 0/);
    /* And the badges that must NOT go with the form: docs/INSTALL.md sends a
     * first-time operator to this tab to read whether FFmpeg has SRT, and that
     * operator has not created a source yet by definition. */
    expect(src, "the FFmpeg capability badges are no longer rendered on the zero-source " +
      "ingest tab, so the check docs/INSTALL.md sends a first-time operator to make is " +
      "on a screen they cannot reach yet").toMatch(/<FfmpegBadges system=\{system\} \/>[\s\S]*<FfmpegBadges/);
  });

  it("the settings page keeps the install-wide controls it can still save", () => {
    const src = read("ui/src/pages/SettingsPage.tsx");
    /* THE PORT. settings.listeners is install-wide, PUT /settings carries no
     * requireSource, and the server binds a changed port with no source at all
     * -- so a first install whose 1935 is already taken has to be able to move
     * it. The listener card lived inside IngestSettings, which the zero-source
     * branch replaces wholesale, and that left the only port control in the
     * product unreachable on exactly the boot that logs the bind failure. */
    expect(src, "the zero-source ingest tab no longer offers the listener ports, so a " +
      "fresh install whose port is already taken cannot change it anywhere in the UI")
      .toMatch(/<ListenerPortsOnly/);
    expect(src, "the listener port inputs are gone from the file entirely")
      .toMatch(/id="listener-rtmp"/);
  });

  /* THE BRANCHES THAT REPLACED `.catch(() => {})`.
   *
   * Both were added because an empty catch hid a real regression: a settings
   * read that fails leaves the page spinning for ever with no message, and a
   * source count that fails must NOT read as zero or it replaces the ingest
   * form on an install with several. Neither was pinned by anything, so
   * reverting either one -- including back to the exact empty catch the plan
   * named -- passed vitest, tsc and oxlint. */
  it("the settings page's failed reads are branches rather than empty catches", () => {
    const src = read("ui/src/pages/SettingsPage.tsx");
    expect(src, "the settings read swallows its failure again, so the page spins for " +
      "ever and says nothing").toMatch(/api\.getSettings\(\)\.then\(setSettings\)\.catch\(\(\) => setLoadFailed\(true\)\)/);
    expect(src, "a source count that could not be read is being reported as zero, which " +
      "replaces the ingest form with \"create a source\" on an install that has several")
      .toMatch(/\.catch\(\(\) => setSourceCount\(null\)\)/);
    expect(src, "the system read no longer records that it resolved, so `system === null` " +
      "means both \"could not read\" and \"not yet\" and the FFmpeg card is a titled box " +
      "with nothing in it").toMatch(/\.finally\(\(\) => setSystemResolved\(true\)\)/);
    expect(src, "the FFmpeg badges render nothing for an unreadable system, which inside " +
      "a card is a heading with no content and no message")
      .toMatch(/if \(!system\) \{\s*return <span[^>]*>\{t\("set\.ffmpegUnknown"\)\}/);
  });

  it("the sources page says what to do rather than showing nothing", () => {
    const src = read("ui/src/pages/SourcesPage.tsx");
    /* Every refusal on every other screen names this page, so a blank area
     * under a heading is the worst thing it can be: it reads as "the thing you
     * were sent to do is already done, or broken". */
    expect(src, "the sources page renders nothing at all when the list is empty, which " +
      "is where every other screen's refusal sends the operator")
      .toMatch(/sources\.length === 0 \?/);
    expect(src, "the sources page no longer renders NoProgrammeYet").toContain("<NoProgrammeYet");
  });

  it("lets the operator delete their only source, and says what that leaves", () => {
    const src = read("ui/src/pages/SourcesPage.tsx");
    /* The store refused this delete until the guard came off, so the button
     * was disabled on the only source and the title said why. Both halves
     * matter and only the first one is visible: re-adding `disabled` puts an
     * operator back in front of a control that works, greyed out, with a
     * sentence explaining a rule the server no longer has.
     *
     * ASSERTED OVER THE ELEMENT, NOT OVER THE FILE, and deliberately: the
     * first version of this pinned the literal `disabled={onlyOne}`, which is
     * the ONE spelling that can no longer be written, because the same commit
     * deleted the `onlyOne` prop. It would have caught a straight revert and
     * nothing else -- `disabled={sources.length === 1}` under a renamed prop is
     * what a later change would actually write, and it sailed through. Both
     * assertions here are about the delete button and the card that renders it,
     * so any name for the same rule fails them. */
    const del = jsxTagWith(src, "Button", "onClick={onDelete}");
    expect(del, "the delete control is disabled again on the only source, for a delete " +
      "the store now accepts").not.toMatch(/\bdisabled\b/);
    const card = jsxTagWith(src, "SourceCard", "source={s}");
    expect(card, "the source card is being told again how many sources there are, which " +
      "it has no remaining use for except to withhold the delete")
      .not.toMatch(/sources\.length/);
    /* And the confirmation carries the consequence. Deleting the last source
     * leaves the install with no programme at all -- a different outcome from
     * every other delete on this page, and the one screen that can say so
     * before it happens rather than after.
     *
     * The condition is pinned beside the key rather than the key alone: a
     * mention of `sources.deleteLastDescription` somewhere in the file proves
     * only that the string is still imported, not that the branch selecting it
     * is still the last-source branch. */
    const confirm = jsxTagWith(src, "ConfirmDestructive", "open={deleting !== null}");
    expect(confirm, "the delete confirmation no longer distinguishes the last source, so " +
      "the one delete that empties the install reads exactly like the others")
      .toMatch(/sources\.length === 1[\s\S]*?sources\.deleteLastDescription/);
  });

  /* The dialog and the toast are one sentence told twice, a second apart, and
   * they used to disagree. The confirmation promised that renditions go with
   * the source -- they do, `renditions.source_id` is ON DELETE CASCADE -- while
   * the toast afterwards named destinations alone, so the operator who wanted
   * to keep a 720p encode for a replacement source was told nothing had been
   * lost that they would have to build again. */
  it("says the same thing after the delete as it did before it", () => {
    const en = JSON.parse(read("ui/src/lib/i18n/en.json")) as Record<string, string>;
    for (const key of ["sources.deleted", "sources.deletedLast"]) {
      expect(en[key], `${key} confirms a delete that took the renditions too without ` +
        "naming them, contradicting the dialog the operator read a second earlier")
        .toMatch(/renditions/i);
    }
    /* And the asymmetry the dialog exists to name survives the delete: an
     * install with nothing left is not the same outcome as one with three
     * sources still running, and the toast is the only thing on screen at the
     * moment it becomes true. */
    const src = read("ui/src/pages/SourcesPage.tsx");
    expect(src, "the post-delete toast is the same neutral sentence whether or not the " +
      "install still has a programme").toMatch(/wasOnly \? "sources\.deletedLast" : "sources\.deleted"/);
    expect(src, "the toast branches on something other than the source count this render " +
      "was drawn from, which is the only place that still knows there was one")
      .toMatch(/const wasOnly = sources\.length === 1;/);
  });
});
