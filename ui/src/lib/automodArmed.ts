import type { AutomodAction, AutomodChecker } from "./types";

/* What the automod matrix is allowed to say about itself.
 *
 * Two defects, one root: the card had TWO sources of truth for the same fact
 * and rendered them side by side.
 *
 *   - The collapsed line per platform read `view.summary[platform]`, which is
 *     the SERVER'S count from one fetch at mount. The cells below it, and the
 *     irreversible-action banner above it, read the operator's DRAFT. So the
 *     card could print "nothing automatic" directly under "An irreversible
 *     action is armed" over a freshly ticked ban -- and, mid-raid, could keep
 *     reading "5 automatic actions" after everything had been disarmed. The
 *     collapsed line is the ONLY thing an operator sees without expanding
 *     sixty cells, which is what makes a stale one worth this file.
 *
 *   - The "Flag for review" row offered twelve switches with one possible
 *     outcome. Findings are recorded before the matrix is consulted
 *     (chat/automod.go recordAutomod logs every finding in the verdict), and
 *     the worker returns immediately for ActionFlag, so the cell changes
 *     nothing observable either way. The server already knows this: Summary
 *     skips ActionFlag (automod/matrix.go:220), which is why the count and the
 *     switches disagreed about what "armed" even means.
 *
 * The count is a plain function so it can be tested as a decision, which is
 * this repo's way of testing something that lives in a page -- and so the two
 * places that need it (the collapsed line, and the row's operability) cannot
 * drift apart the way the summary and the draft did. */

/** The one action that is on for everybody, always, and cannot be switched.
 *
 *  It is not "an automatic action" in the sense an operator means when they ask
 *  what this thing will do to their chat: it changes nothing an audience sees.
 *  Mirrors `automod.ActionFlag` and the exclusion in `Matrix.Summary`. */
export const ALWAYS_ON_ACTION: AutomodAction = "flag";

/** Whether a row's cells are switches at all.
 *
 *  A CONTROL: the flag row's twelve switches are not rendered disabled, they
 *  are not rendered as switches. A disabled switch still says "this could be
 *  turned on"; a switch that silently does nothing is the failure this file's
 *  neighbour already refuses for an unavailable cell. */
export function isOperable(action: AutomodAction): boolean {
  return action !== ALWAYS_ON_ACTION;
}

/** How many automatic actions are armed for one platform, counted the way the
 *  server counts them: operable actions only, and only where the platform can
 *  actually perform it.
 *
 *  `available` is asked LAST for the same reason `Matrix.Allows` consults the
 *  capability gate last: a stored cell left behind by a capability that has
 *  since gone away must not be counted as something that will happen. */
export function armedCount(args: {
  platform: string;
  actions: AutomodAction[];
  checkers: AutomodChecker[];
  /** Cells the operator has switched on, keyed "platform/action/checker". */
  on: Record<string, boolean> | undefined;
  available: (key: string) => boolean;
}): number {
  let n = 0;
  for (const action of args.actions) {
    if (!isOperable(action)) continue;
    for (const checker of args.checkers) {
      const key = `${args.platform}/${action}/${checker}`;
      if (args.on?.[key] && args.available(key)) n += 1;
    }
  }
  return n;
}
