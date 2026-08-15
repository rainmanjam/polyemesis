import type { TranslationKey } from "./i18n";

/* ==========================================================================
   The onboarding tour, as DATA
   ==========================================================================

   The steps live here and not inside Tour.tsx because a tour's failure mode is
   silent. A selector that stops matching does not throw, does not warn and does
   not log: driver.js highlights nothing and moves on, and the first report comes
   from a user months later. The only thing that catches that is a test, and a
   test can only read this if the steps are data rather than JSX.

   ui/src/lib/tour-drift.test.ts is that test. It reads the array below, and for
   every step it opens the file named in `owner` and requires the anchor to be
   wired there. Which is why `owner` is a field and not a comment.

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
   `presence: "always"` step below is anchored to page chrome: a sidebar link, a
   page-header button, or a settings panel that renders whatever is configured.

   Exactly ONE step is `presence: "whenConfigured"`, and it earns that by being
   the only step whose subject genuinely does not exist yet on a first run: the
   publish URL of a source the operator has not created. Tour.tsx sets
   `skipMissingElement`, so it is dropped on an empty install and shown on a
   configured one -- which is what a replay from Settings usually is.

   The skip path is therefore NOT a safety net that never fires. It fires on
   every first run, which is the run this whole feature exists for, and
   tour-drift.test.ts asserts the arrangement rather than trusting it: at least
   one step must be conditional and the step BEFORE it must be unconditional, so
   that dropping it never leaves the teaching without a home.
   ========================================================================== */

/** One step. `element` is always an attribute selector on `data-tour`, which is
 *  what makes the drift guard mechanical: it can derive the attribute value from
 *  the selector and go looking for it. */
export type TourStep = {
  /** Stable across releases; used in test failures and nowhere else. */
  id: string;
  /** Where the tour must be for this step's anchor to exist.
   *
   *  Search params are allowed and are load-bearing for the last step:
   *  SettingsPage drives its tab from `?tab=`, so the tour can land directly on
   *  the panel rather than telling the operator to go and find it. */
  route: string;
  /** The CSS selector. `[data-tour="…"]` and nothing else — see ANCHOR_RE in
   *  tour-drift.test.ts, which enforces the shape rather than trusting it. */
  element: string;
  /** The component that renders the anchor, relative to `ui/src`. The drift
   *  guard reads this file; a wrong path fails loudly rather than silently
   *  checking the wrong thing. */
  owner: string;
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

export const TOUR_STEPS: TourStep[] = [
  {
    // The sidebar is rendered by AppLayout on every route, so this anchor is
    // present wherever the tour starts. The route is pinned to "/" anyway, so a
    // replay from Settings begins in the same place as a first run.
    id: "sources-nav",
    route: "/",
    element: '[data-tour="nav-sources"]',
    owner: "components/AppLayout.tsx",
    titleKey: "tour.sourcesNav.title",
    bodyKey: "tour.sourcesNav.body",
    side: "right",
    presence: "always",
  },
  {
    // The PageHeader action on SourcesPage, which renders during the load and
    // whatever the source count turns out to be — including zero, which is the
    // first-run state now that nothing is seeded.
    //
    // THIS is the step that carries #384's first point. It has to, because the
    // step after it is conditional: an operator on a fresh install must reach
    // the end of THIS popover knowing where the publish address lives, without
    // depending on the next one rendering at all.
    id: "add-source",
    route: "/sources",
    element: '[data-tour="add-source"]',
    owner: "pages/SourcesPage.tsx",
    titleKey: "tour.addSource.title",
    bodyKey: "tour.addSource.body",
    side: "bottom",
    presence: "always",
  },
  {
    // The one conditional step. Absent on a first run, present on any install
    // with a source — which is what a replay from Settings usually is, and the
    // case where showing the real address beats describing it.
    //
    // The anchor spans the publish URLs AND the token block, because
    // publishUrls is empty until an ingest mode is chosen and a wrapper around
    // the URLs alone would be zero-height on a source that has one.
    id: "publish-url",
    route: "/sources",
    element: '[data-tour="source-publish-urls"]',
    owner: "pages/SourcesPage.tsx",
    titleKey: "tour.publishUrl.title",
    bodyKey: "tour.publishUrl.body",
    side: "top",
    presence: "whenConfigured",
  },
  {
    // Still on /sources, deliberately: the sidebar is there too, and bouncing
    // back to the dashboard between two steps is motion with no information in
    // it.
    id: "routing-nav",
    route: "/sources",
    element: '[data-tour="nav-routing"]',
    owner: "components/AppLayout.tsx",
    titleKey: "tour.routingNav.title",
    bodyKey: "tour.routingNav.body",
    side: "right",
    presence: "always",
  },
  {
    // The PageHeader action, which renders whatever the destination count is.
    // The empty state has a second button with the same label; that one is
    // gone the moment a destination exists, which would break a replay.
    id: "add-destination",
    route: "/",
    element: '[data-tour="add-destination"]',
    owner: "pages/Dashboard.tsx",
    titleKey: "tour.addDestination.title",
    bodyKey: "tour.addDestination.body",
    side: "bottom",
    presence: "always",
  },
  {
    // The NAV entry, not the <Experimental> block on the Renditions page: that
    // block is conditional on the selected encoder being one of the four whose
    // flags are unconfirmed, so on a first-run install with no rendition and no
    // probe it does not render at all. The badge is explained in the copy
    // instead, which is honest about where the step is pointing.
    id: "renditions-nav",
    route: "/",
    element: '[data-tour="nav-renditions"]',
    owner: "components/AppLayout.tsx",
    titleKey: "tour.renditionsNav.title",
    bodyKey: "tour.renditionsNav.body",
    side: "right",
    presence: "always",
  },
  {
    // SecuritySettings renders unconditionally inside the security tab, and the
    // data-directory row inside it is not behind any condition — `system` may
    // be null, in which case the path shows an em dash and the sentence
    // underneath, which is the half this step is actually about, still renders.
    id: "data-directory",
    route: "/settings?tab=security",
    element: '[data-tour="data-directory"]',
    owner: "pages/SettingsPage.tsx",
    titleKey: "tour.dataDirectory.title",
    bodyKey: "tour.dataDirectory.body",
    side: "top",
    presence: "always",
  },
];
