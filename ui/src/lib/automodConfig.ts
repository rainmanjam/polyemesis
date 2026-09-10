import type { TranslationKey } from "./i18n";
import type {
  AutomodAction,
  AutomodChecker,
  AutomodRule,
  AutomodSettings,
} from "./types";

/* ===========================================================================
   The decisions the automod editor makes, as plain functions.

   They live here rather than inside the components for the reason armedCount
   does: a decision that lives in JSX can only be tested by rendering, and the
   two places that need it — the matrix, which decides whether a cell is a
   switch at all, and the editor, which decides whether a field is complete —
   must not drift apart. They already did once, between the server's summary
   and the operator's draft, and lib/automodArmed.ts exists because of it.
   =========================================================================== */

/** The vocabulary, once.
 *
 *  Both cards name the same six actions and the same three checkers, and a
 *  second copy of either would let the matrix and the editor disagree about
 *  what "Time out" means — the drift this whole area has been bitten by. The
 *  ENGLISH TEXT IS UNCHANGED from the literals the matrix used to hold, because
 *  those strings are accessible names the behaviour suite matches on. */
export const ACTION_KEYS: Record<AutomodAction, TranslationKey> = {
  flag: "automod.actionFlag",
  hide_local: "automod.actionHideLocal",
  hide: "automod.actionHide",
  delete: "automod.actionDelete",
  timeout: "automod.actionTimeout",
  ban: "automod.actionBan",
};

export const CHECKER_KEYS: Record<AutomodChecker, TranslationKey> = {
  rules: "automod.checkerRules",
  history: "automod.checkerHistory",
  model: "automod.checkerModel",
};

/** Whether a checker exists at all on the server, and why not when it does not.
 *
 *  THIS IS THE ONE THAT LETS AN OPERATOR ARM A BAN OVER NOTHING. `automod.New`
 *  takes each checker as a pointer and a nil one "simply contributes nothing"
 *  (internal/automod/engine.go). Nil is the DEFAULT for two of the three: the
 *  rules checker is nil until a rule is written, and the model is nil unless
 *  `Model.Enabled`. Every cell in those columns was still a live switch, so
 *  "Ban / Model" — permission for a language model to permanently remove a
 *  viewer — could be armed, saved, and shown as armed, over a checker that
 *  cannot produce a finding.
 *
 *  Rendered exactly the way an unavailable platform is: inert, with the reason.
 *  Not disabled, and never an unticked box. A greyed switch still says "this
 *  could be turned on", and an unticked one says "this channel is protected and
 *  the protection is off" when the truth is that there is no protection to
 *  switch. */
export type CheckerReadiness =
  | { ready: true }
  | { ready: false; reason: TranslationKey };

const READY: CheckerReadiness = { ready: true };

export function checkerReady(
  a: AutomodSettings | undefined,
  checker: AutomodChecker,
): CheckerReadiness {
  if (checker === "rules") {
    // A DISABLED rule counts as no rule, because Rule.Match returns false for
    // one — the checker is constructed and finds nothing, which is the same
    // outcome as not existing and must read the same way on screen.
    const usable = (a?.rules ?? []).some(
      (r) => r.enabled && r.pattern.trim() !== "",
    );
    return usable ? READY : { ready: false, reason: "automod.noRules" };
  }
  if (checker === "model") {
    const m = a?.model;
    if (!m?.enabled) return { ready: false, reason: "automod.modelOff" };
    // modelConfigFrom copies the stored endpoint over DefaultModelConfig's,
    // including an empty one, so a blank field is a POST to "" — an error on
    // every call, and the model path fails open. The checker exists and can
    // never find anything, which is worth saying differently from "off".
    if (m.endpoint.trim() === "") {
      return { ready: false, reason: "automod.modelNoEndpoint" };
    }
    return READY;
  }
  // History is built from DefaultHistoryLimits on every save, so it is never
  // nil and never unconfigured. A checker this build does not know is left
  // alone deliberately: the server sent it, the server knows whether it is
  // wired, and guessing "not configured" here would render a working column
  // inert with a reason that is not true.
  return READY;
}

/** Everything wrong with one rule, in the operator's terms.
 *
 *  The two the SERVER refuses are here because a 400 arrives after the save
 *  button, attached to the whole document, with no way for a form to know which
 *  of six patterns it meant. Catching them at the keystroke is the difference
 *  between "fix this field" and "something about your settings was wrong".
 *
 *  It does not replace the server's answer, and `serverRejection` below exists
 *  because it cannot: Go compiles RE2 and this compiles JavaScript's engine.
 *  A backreference or a lookahead is valid here and refused there, so a pattern
 *  can pass every check on this page and still come back 400 — which is why the
 *  refusal has to render against the field too. */
export type RuleProblem = {
  field: "name" | "pattern" | "timeout";
  key: TranslationKey;
  /** The engine's own message, for the patterns it can explain. */
  detail?: string;
};

export function ruleProblems(rule: AutomodRule): RuleProblem[] {
  const out: RuleProblem[] = [];
  if (rule.name.trim() === "") {
    out.push({ field: "name", key: "automod.nameEmpty" });
  }
  if (rule.pattern.trim() === "") {
    out.push({ field: "pattern", key: "automod.patternEmpty" });
  } else {
    try {
      // WITHOUT the "(?i)" the server prepends. It is an inline flag group,
      // which RE2 accepts and JavaScript does not, so compiling the exact
      // string the server compiles would report every valid pattern as broken.
      new RegExp(rule.pattern);
    } catch (e) {
      out.push({
        field: "pattern",
        key: "automod.patternBad",
        detail: e instanceof Error ? e.message : String(e),
      });
    }
  }
  // The server's own words: "a timeout of zero seconds is a permanent ban on
  // every platform". It refuses the save; this says so before the save, beside
  // the field that has to change.
  if (rule.action === "timeout" && !(rule.timeoutSeconds && rule.timeoutSeconds > 0)) {
    out.push({ field: "timeout", key: "automod.timeoutZero" });
  }
  return out;
}

/** Which rule a settings rejection is about, if it is about one.
 *
 *  Both refusals NewRuleSet can produce name the rule first — `rule %q: ...`
 *  from Compile, and `rule %q asks for a timeout...` — so the name is how a
 *  document-wide 400 is turned back into a field. Nothing else in the settings
 *  document produces a message of that shape, and a rejection that names no
 *  rule is left alone rather than pinned to an arbitrary row: attributing an
 *  unrelated failure to a pattern would send an operator to rewrite a regex
 *  that was never the problem. */
export function serverRejection(err: string | null | undefined): string | null {
  const m = /rule "((?:[^"\\]|\\.)*)"/.exec(err ?? "");
  return m ? m[1] : null;
}

/** The id a new rule gets.
 *
 *  Max + 1 rather than length + 1: ids are what a finding carries back
 *  (`Finding.RuleID`), so reusing the id of a deleted rule would relabel old
 *  moderation records as the work of a rule that never made them. */
export function nextRuleId(rules: readonly AutomodRule[]): number {
  return rules.reduce((max, r) => Math.max(max, r.id), 0) + 1;
}

/** The floor the server will accept, matching automod.ParseConfidence.
 *
 *  A number outside it is NOT refused by the server — it logs and silently uses
 *  0.8 — so this page is the only place an operator can be told. Both ends
 *  destroy the checker in opposite directions and both are silent: 0 acts on
 *  every opinion the model has, and anything above 1 is above every verdict it
 *  can return, so it never acts again. */
export const MIN_CONFIDENCE = 0.01;
export const MAX_CONFIDENCE = 1;

export function confidenceOutOfRange(v: number): boolean {
  return !(v >= MIN_CONFIDENCE && v <= MAX_CONFIDENCE);
}
