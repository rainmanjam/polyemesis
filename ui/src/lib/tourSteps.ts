import type { TranslationKey } from "./i18n";

/* ==========================================================================
   The onboarding tour, as DATA
   ==========================================================================

   The steps live here and not inside Tour.tsx because a tour's failure mode is
   silent. A selector that stops matching does not throw, does not warn and does
   not log: driver.js highlights nothing and moves on, and the first report comes
   from a user months later. The only thing that catches that is a test, and a
   test can only read this if the steps are data rather than JSX.

   ui/src/lib/tour-drift.test.ts is that test. It reads the table below, and for
   every step it opens the file named in the `owner` column and requires the
   anchor to be wired there. Which is why the owner is a column and not a
   comment.

   WHAT THE TOUR IS FOR, since a tour that labels the navigation is worse than no
   tour -- it costs a click and teaches nothing the sidebar already says. Every
   step below is a thing that is NOT visible from the console:

     1. The publish address exists on a page nothing points at. install.sh
        prints it once; by the time anyone opens the UI that terminal is gone.
     2. Routing is the product. A destination with no profile is plain
        restreaming, which every other tool does.
     3. The EXPERIMENTAL badge names one specific unverified claim, and reads as
        a warning to anybody who has not been told that.
     4. secret.key is a separate file from the database, and restoring one
        without the other fails in a way nothing on screen explains.

   ===== WHAT AN EMPTY INSTALL MEANS HERE =====

   Nothing is seeded. No sources, no destinations, no renditions, no chat
   accounts -- see #387, which removed the source that used to be created at
   first open. So "the anchor exists" cannot lean on any row existing, and every
   `"always"` step below is anchored to page chrome: a sidebar link, a
   page-header button, or a settings panel that renders whatever is configured.

   Exactly ONE step is `"whenConfigured"`, and it earns that by being the only
   step whose subject genuinely does not exist yet on a first run: the publish
   URL of a source the operator has not created. Tour.tsx sets
   `skipMissingElement`, so it is dropped on an empty install and shown on a
   configured one -- which is what a replay from Settings usually is.

   The skip path is therefore NOT a safety net that never fires. It fires on
   every first run, which is the run this whole feature exists for, and
   tour-drift.test.ts asserts the arrangement rather than trusting it: at least
   one step must be conditional and the step BEFORE it must be unconditional, so
   that dropping it never leaves the teaching without a home.

   ===== WHY A TABLE, AND WHAT IS DERIVED FROM IT =====

   Six things about a step are decisions somebody has to make. The other three
   fields of `TourStep` were restatements of two of those six in a fixed
   wrapper -- `[data-tour="…"]` around the anchor, and `tour.<id>.title` /
   `tour.<id>.body` around the id -- and spelling them out per step bought
   nothing but three more places to mistype, in a file whose entire job is to be
   the thing a test can trust.

   Nothing is given up by deriving them. The one selector shape ANCHOR_RE
   insists on in tour-drift.test.ts is now the only shape this file is capable
   of producing, and an id the English catalogue has no copy for is still a
   compile error, because `TourCopyId` is read back out of en.json rather than
   declared. What does go away is the possibility of a step's id and its own
   copy keys disagreeing -- which nothing checked, because the keys were the
   only evidence of what the copy was supposed to be.
   ========================================================================== */

/** One step, as the rest of the app sees it. `element` is always an attribute
 *  selector on `data-tour`, which is what makes the drift guard mechanical: it
 *  can derive the attribute value from the selector and go looking for it. */
export type TourStep = {
  /** Stable across releases; used in test failures and nowhere else. Also names
   *  this step's copy in the catalogue -- see `titleKey` below. */
  id: string;
  /** Where the tour must be for this step's anchor to exist.
   *
   *  Search params are allowed and are load-bearing for the last step:
   *  SettingsPage drives its tab from `?tab=`, so the tour can land directly on
   *  the panel rather than telling the operator to go and find it. */
  route: string;
  /** The CSS selector, built from the table's `anchor` column. `[data-tour="…"]`
   *  and nothing else -- see ANCHOR_RE in tour-drift.test.ts, which still
   *  enforces the shape rather than trusting it, and which is now guarding the
   *  template below rather than seven hand-written literals. */
  element: string;
  /** The component that renders the anchor, relative to `ui/src`. The drift
   *  guard reads this file; a wrong path fails loudly rather than silently
   *  checking the wrong thing. */
  owner: string;
  /** Built from `id`. The pairing is not a convention anybody has to remember:
   *  `TourCopyId` admits only the ids en.json has both halves of. */
  titleKey: TranslationKey;
  bodyKey: TranslationKey;
  /** Which side of the anchor the popover sits on. Passed straight through to
   *  driver.js, which falls back on its own when there is no room. */
  side: "top" | "right" | "bottom" | "left";
  /** Whether the anchor is in the DOM on an install where nothing is
   *  configured.
   *
   *  `"always"` is a promise: the element is page chrome and renders with an
   *  empty database. `"whenConfigured"` says the step depends on a row the
   *  operator has to create, and accepts being skipped when they have not.
   *
   *  It is not decoration. Tour.tsx turns it into driver.js's `waitForElement`:
   *  an "always" step gets a grace period, because a route change plus a fetch
   *  is slower than a render; a "whenConfigured" step gets ZERO, because
   *  waiting four seconds for something that is legitimately absent is four
   *  seconds of a frozen popover on every first run. */
  presence: "always" | "whenConfigured";
};

/** The step ids the English catalogue has copy for.
 *
 *  Read out of en.json in BOTH directions on purpose. A step needs a title and
 *  a body, and half a pair is a popover with a blank in it -- so an id is only
 *  admissible here if `tour.<id>.title` and `tour.<id>.body` both exist. This
 *  is what carries the compile-time check that the hand-written `titleKey` and
 *  `bodyKey` fields used to carry. */
type TitledId<K> = K extends `tour.${infer Name}.title` ? Name : never;
type BodiedId<K> = K extends `tour.${infer Name}.body` ? Name : never;
type TourCopyId = Extract<TitledId<TranslationKey>, BodiedId<TranslationKey>>;

/** One row of the table below: id, route, anchor, owner, side, presence.
 *
 *  A named tuple and not an object, because these are read as a table. The
 *  question you actually come here with is a COLUMN question -- does every nav
 *  step sit on the right, is more than one step conditional, do two steps in a
 *  row cross routes -- and a column is only scannable if the rows line up.
 *
 *  The column types are picked so that no two of them are interchangeable: a
 *  route starts with `/`, an owner ends in `.tsx`, and id, side and presence
 *  are each a closed union. A row written in the wrong order therefore does not
 *  compile, which is the one hazard a positional table would otherwise carry
 *  over an object per step. */
type StepRow = [
  id: TourCopyId,
  route: `/${string}`,
  /** The `data-tour` attribute VALUE, not a selector -- the selector is built
   *  from it, and building it here is what keeps ANCHOR_RE satisfiable. */
  anchor: string,
  owner: `${string}.tsx`,
  side: TourStep["side"],
  presence: TourStep["presence"],
];

const STEP_ROWS: StepRow[] = [
  // The sidebar is rendered by AppLayout on every route, so this anchor is
  // present wherever the tour starts. The route is pinned to "/" anyway, so a
  // replay from Settings begins in the same place as a first run.
  ["sourcesNav", "/", "nav-sources", "components/AppLayout.tsx", "right", "always"],

  // The PageHeader action on SourcesPage, which renders during the load and
  // whatever the source count turns out to be — including zero, which is the
  // first-run state now that nothing is seeded.
  //
  // THIS is the step that carries #384's first point. It has to, because the
  // step after it is conditional: an operator on a fresh install must reach the
  // end of THIS popover knowing where the publish address lives, without
  // depending on the next one rendering at all.
  ["addSource", "/sources", "add-source", "pages/SourcesPage.tsx", "bottom", "always"],

  // The one conditional step. Absent on a first run, present on any install
  // with a source — which is what a replay from Settings usually is, and the
  // case where showing the real address beats describing it.
  //
  // The anchor spans the publish URLs AND the token block, because publishUrls
  // is empty until an ingest mode is chosen and a wrapper around the URLs alone
  // would be zero-height on a source that has one.
  ["publishUrl", "/sources", "source-publish-urls", "pages/SourcesPage.tsx", "top", "whenConfigured"],

  // Still on /sources, deliberately: the sidebar is there too, and bouncing
  // back to the dashboard between two steps is motion with no information in it.
  ["routingNav", "/sources", "nav-routing", "components/AppLayout.tsx", "right", "always"],

  // The PageHeader action, which renders whatever the destination count is. The
  // empty state has a second button with the same label; that one is gone the
  // moment a destination exists, which would break a replay.
  ["addDestination", "/", "add-destination", "pages/Dashboard.tsx", "bottom", "always"],

  // The NAV entry, not the <Experimental> block on the Renditions page: that
  // block is conditional on the selected encoder being one of the four whose
  // flags are unconfirmed, so on a first-run install with no rendition and no
  // probe it does not render at all. The badge is explained in the copy
  // instead, which is honest about where the step is pointing.
  ["renditionsNav", "/", "nav-renditions", "components/AppLayout.tsx", "right", "always"],

  // SecuritySettings renders unconditionally inside the security tab, and the
  // data-directory row inside it is not behind any condition — `system` may be
  // null, in which case the path shows an em dash and the sentence underneath,
  // which is the half this step is actually about, still renders.
  ["dataDirectory", "/settings?tab=security", "data-directory", "pages/SettingsPage.tsx", "top", "always"],
];

export const TOUR_STEPS: TourStep[] = STEP_ROWS.map(
  ([id, route, anchor, owner, side, presence]) => ({
    id,
    route,
    element: `[data-tour="${anchor}"]`,
    owner,
    titleKey: `tour.${id}.title`,
    bodyKey: `tour.${id}.body`,
    side,
    presence,
  }),
);
