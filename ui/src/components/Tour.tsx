import { useCallback, useEffect, useState } from "react";
import { useNavigate } from "react-router";
import { driver, type Driver } from "driver.js";
import { Compass, X } from "lucide-react";

import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import { useT, type Translator } from "@/lib/i18n";
import { TOUR_STEPS } from "@/lib/tourSteps";

/* ==========================================================================
   The onboarding tour
   ==========================================================================

   OFFERED, NOT LAUNCHED. A modal that seizes the screen the instant an account
   is created is the thing people complain about, and the operator who already
   knows this product is the one most likely to meet it. So the first run of a
   new install gets a dismissible strip under the header, and Settings carries a
   control to start the tour at any time afterwards.

   DISMISSING AND FINISHING WRITE THE SAME THING, on purpose. Both mean "do not
   offer this again", the replay control exists for anyone who changes their
   mind, and there is no state in which the difference between them would change
   what the UI does. The write goes to the SERVER (POST /api/v1/tour/complete)
   and not to localStorage — see lib/types.ts TourState.

   driver.js is imported for its behaviour and NOT for its stylesheet: the
   popover is themed from the app's own tokens in index.css, under
   `.polyemesis-tour`. Importing driver.js/dist/driver.css would drop a second
   design system into a console that already has one.
   ========================================================================== */

/** Builds and starts the driver, navigating between steps as it goes.
 *
 *  NAVIGATION IS THE WHOLE COMPLICATION HERE. Three of the six steps anchor to
 *  the sidebar, which AppLayout renders on every route; the other three anchor
 *  to a page, so the router has to be somewhere specific before the element
 *  exists. driver.js's own next/previous handlers cannot know that, so they are
 *  replaced: navigate first, then move.
 *
 *  Overriding onNextClick means driver.js no longer advances by itself — the
 *  handler owns that, which is why every branch below ends in a move or a
 *  destroy. `waitForElement` then covers the gap between `navigate()` returning
 *  and React committing the new route: driver.js watches the DOM for the
 *  selector and drives the step when it appears.
 *
 *  `skipMissingElement` is the empty-install guarantee, and it is not a safety
 *  net that never fires: nothing is seeded on a first run, so the publish-URL
 *  step has no source to point at and IS dropped on every first run. Without
 *  it, driver.js highlights the page background — a dimmed screen with a hole
 *  cut out of nothing.
 *
 *  `waitForElement` is set PER STEP from `presence` rather than globally, and
 *  the difference is the whole reason that field exists. A step that must be
 *  there gets a grace period, because a route change plus a fetch is slower
 *  than a render. A step that is legitimately absent gets zero, because a
 *  global wait would freeze the popover for the full timeout on exactly the
 *  install this tour is for before finally skipping. */
function startTour(
  navigate: (to: string) => void,
  t: Translator,
  onFinished: () => void,
): Driver {
  const go = (index: number) => {
    const step = TOUR_STEPS[index];
    if (step) navigate(step.route);
  };

  const d = driver({
    showProgress: true,
    progressText: t("tour.progress"),
    nextBtnText: t("tour.next"),
    prevBtnText: t("tour.previous"),
    doneBtnText: t("tour.done"),
    popoverClass: "polyemesis-tour",
    // A tour is not a modal. Escape closes it, the overlay closes it, and the
    // close button is always offered.
    allowClose: true,
    // No global wait — every step sets its own from `presence` below. A global
    // one would apply the "always" grace period to the conditional step too,
    // which is the frozen-popover-on-every-first-run bug.
    waitForElement: 0,
    skipMissingElement: true,
    // The highlighted control stays inert while the popover is up. Clicking
    // "Add destination" mid-tour would open a dialog over the popover and leave
    // the operator in two flows at once.
    disableActiveInteraction: true,
    steps: TOUR_STEPS.map((step) => ({
      element: step.element,
      // 3s for a step that has to be there; a short grace for one that
      // legitimately may not be. driver.js watches the DOM with a
      // MutationObserver until the deadline, then applies skipMissingElement.
      //
      // The short value is a TRADE and it is worth naming. Too long and every
      // first-run tour — the run this feature exists for — stalls on a step
      // that was never going to appear. Too short and an operator who clicks
      // Next the instant the previous popover renders, on a configured install
      // whose source list is still in flight, loses a step they should have
      // seen. It is set at the cheap end because the costs are not symmetric:
      // the step before this one already carries the teaching, by design, so a
      // wrong skip loses a nicety while a wrong wait is a frozen popover.
      waitForElement: step.presence === "always" ? 3000 : 800,
      popover: {
        title: t(step.titleKey),
        description: t(step.bodyKey),
        side: step.side,
        align: "start",
      },
    })),
    onNextClick: (_el, _step, { driver: self }) => {
      const next = (self.getActiveIndex() ?? 0) + 1;
      go(next);
      self.moveNext();
    },
    onPrevClick: (_el, _step, { driver: self }) => {
      const prev = (self.getActiveIndex() ?? 0) - 1;
      go(prev);
      self.movePrevious();
    },
    // Every exit lands here — the done button, the close button, Escape and a
    // click on the overlay — which is what makes this the one place completion
    // is recorded.
    onDestroyed: onFinished,
  });

  go(0);
  d.drive();
  return d;
}

/** The tour as a hook, for the two controls in this file that start it.
 *
 *  Returns a stable callback. Deliberately NOT exported: the driver instance is
 *  not exposed either, and for the same reason — nothing outside this file needs
 *  to drive the tour, and two callers able to start it would be two overlays on
 *  screen at once. Anything that wants a start button imports one of the two
 *  components below. */
function useStartTour(onFinished?: () => void): () => void {
  const navigate = useNavigate();
  const t = useT();
  return useCallback(() => {
    startTour(navigate, t, () => onFinished?.());
  }, [navigate, t, onFinished]);
}

/** The Settings replay control.
 *
 *  Separate from the offer below because it answers a different question: the
 *  offer is "you have never seen this", and this is "show me that again". It is
 *  unconditional — an operator who took the tour on day one and is looking for
 *  the thing about secret.key on day ninety should not have to have dismissed
 *  something to find it. */
export function TourReplayButton() {
  const t = useT();
  // Replaying also records completion. Usually that is a no-op — the server
  // takes the first write and ignores the rest — but it covers the operator who
  // never touched the strip and came looking here instead: they have now seen
  // the tour, and the offer has nothing left to offer them. The strip itself is
  // in AppLayout and does not hear about this, so it stays up until the next
  // load; showing a valid offer once more is a smaller wrong than a component
  // registry existing to synchronise two buttons.
  const start = useStartTour(
    useCallback(() => {
      api.completeTour().catch(() => {});
    }, []),
  );
  return (
    <Button size="sm" variant="outline" onClick={start}>
      <Compass className="h-3 w-3" /> {t("tour.replay")}
    </Button>
  );
}

/** The first-run offer: a dismissible strip, not a modal.
 *
 *  Renders nothing until the server has answered, and nothing at all once the
 *  tour has been completed or dismissed. A failed read is treated as "already
 *  seen": an offer that appears because a request failed is worse than one that
 *  does not appear, since the replay control in Settings covers the miss and
 *  nothing covers a strip that reappears on every network blip. */
export function TourOffer() {
  const t = useT();
  const [visible, setVisible] = useState(false);

  useEffect(() => {
    let live = true;
    api
      .tourState()
      .then((s) => {
        if (live) setVisible(!s.completed);
      })
      .catch(() => {
        // Deliberately silent. This is a first-run nicety; a toast about it
        // would be the tour's first act being an error message.
      });
    return () => {
      live = false;
    };
  }, []);

  // Hides the strip immediately and records the decision. The local state is
  // not waiting on the request: the operator has said what they want, and a
  // strip that lingers until a round trip finishes reads as a broken button.
  const finish = useCallback(() => {
    setVisible(false);
    api.completeTour().catch(() => {
      // Same reasoning as the read. The worst case is the offer appearing once
      // more on the next load, which is recoverable by dismissing it again.
    });
  }, []);

  const start = useStartTour(finish);

  if (!visible) return null;

  return (
    <div className="flex flex-wrap items-center gap-2 border-b border-border bg-primary-dim px-3 py-1.5">
      <Compass className="h-3.5 w-3.5 shrink-0 text-primary" />
      <span className="min-w-0 flex-1 text-[11px]">
        <strong className="font-semibold">{t("tour.offerTitle")}</strong>{" "}
        <span className="text-muted-foreground">{t("tour.offerBody")}</span>
      </span>
      <Button size="sm" onClick={start}>
        {t("tour.offerStart")}
      </Button>
      <Button
        size="icon-sm"
        variant="ghost"
        onClick={finish}
        aria-label={t("tour.offerDismissAria")}
        title={t("tour.offerDismissAria")}
      >
        <X />
      </Button>
    </div>
  );
}
