import { describe, expect, it } from "vitest";

import {
  ACTION_KEYS,
  CHECKER_KEYS,
  checkerReady,
  confidenceOutOfRange,
  nextRuleId,
  ruleProblems,
  serverRejection,
} from "./automodConfig";
import en from "./i18n/en.json";
import type { AutomodRule, AutomodSettings } from "./types";

/* The decisions behind the automod editor, tested as decisions.
 *
 * The half that cannot be tested here — that a control actually RENDERS for
 * each of these — is AutomodConfig.test.tsx and AutomodMatrix.unconfigured
 * .test.tsx, deliberately: the defect being fixed is that twenty-seven stored
 * values had a type and no input, so a test that only asserts the shape of a
 * decision would reproduce exactly the gap it is meant to close. */

const model = (over: Partial<AutomodSettings["model"]> = {}) => ({
  enabled: false,
  endpoint: "",
  model: "gpt-4o-mini",
  hasApiKey: false,
  timeoutSeconds: 4,
  maxCallsPerHour: 500,
  action: "flag" as const,
  minConfidence: 0.8,
  instruction: "",
  ...over,
});

const settings = (over: Partial<AutomodSettings> = {}): AutomodSettings =>
  ({
    enabled: true,
    history: {},
    model: model(),
    ...over,
  }) as AutomodSettings;

const rule = (over: Partial<AutomodRule> = {}): AutomodRule => ({
  id: 1,
  name: "spam",
  enabled: true,
  pattern: "buy followers",
  action: "flag",
  ...over,
});

describe("checkerReady", () => {
  it("refuses the rules column while no rule is switched on", () => {
    // THE DEFECT. automod.New takes the rule set as a pointer and gets nil
    // when there is nothing to compile, so every cell in this column was a
    // switch over a checker that cannot produce a finding.
    expect(checkerReady(settings({ rules: [] }), "rules")).toEqual({
      ready: false,
      reason: "automod.noRules",
    });
    expect(checkerReady(settings(), "rules").ready).toBe(false);
  });

  it("counts a disabled rule as no rule", () => {
    // Rule.Match returns false for a disabled rule, so the checker exists and
    // finds nothing -- the same outcome, which has to read the same way.
    expect(
      checkerReady(settings({ rules: [rule({ enabled: false })] }), "rules")
        .ready,
    ).toBe(false);
    expect(
      checkerReady(settings({ rules: [rule({ pattern: "  " })] }), "rules")
        .ready,
    ).toBe(false);
    expect(checkerReady(settings({ rules: [rule()] }), "rules").ready).toBe(
      true,
    );
  });

  it("separates a model that is off from one with nowhere to ask", () => {
    // Two different fixes: one is a switch, the other is a field. Saying
    // "not configured" for both would send an operator to the wrong one.
    expect(checkerReady(settings(), "model")).toEqual({
      ready: false,
      reason: "automod.modelOff",
    });
    expect(
      checkerReady(settings({ model: model({ enabled: true }) }), "model"),
    ).toEqual({ ready: false, reason: "automod.modelNoEndpoint" });
    expect(
      checkerReady(
        settings({
          model: model({ enabled: true, endpoint: "https://api.example/v1" }),
        }),
        "model",
      ).ready,
    ).toBe(true);
  });

  it("leaves history alone, because it is built from defaults every save", () => {
    expect(checkerReady(settings(), "history").ready).toBe(true);
    expect(checkerReady(undefined, "history").ready).toBe(true);
  });
});

describe("ruleProblems", () => {
  it("names an uncompilable pattern rather than waiting for the 400", () => {
    const [p] = ruleProblems(rule({ pattern: "(unclosed[" }));
    expect(p.field).toBe("pattern");
    expect(p.key).toBe("automod.patternBad");
    expect(p.detail).toBeTruthy();
  });

  it("does not report the server's own case-insensitive prefix as broken", () => {
    // The server compiles "(?i)" + pattern. That is an inline flag group RE2
    // accepts and JavaScript refuses, so compiling the server's exact string
    // here would report every valid pattern in the list as an error.
    expect(ruleProblems(rule({ pattern: "buy followers" }))).toEqual([]);
  });

  it("refuses a timeout action with no duration, as the server does", () => {
    const problems = ruleProblems(rule({ action: "timeout" }));
    expect(problems.map((p) => p.key)).toContain("automod.timeoutZero");
    expect(
      ruleProblems(rule({ action: "timeout", timeoutSeconds: 600 })),
    ).toEqual([]);
  });

  it("asks for a name and a pattern", () => {
    expect(ruleProblems(rule({ name: " " }))[0].key).toBe("automod.nameEmpty");
    expect(ruleProblems(rule({ pattern: "" }))[0].key).toBe(
      "automod.patternEmpty",
    );
  });
});

describe("serverRejection", () => {
  it("finds the rule a settings 400 is about", () => {
    // Both refusals NewRuleSet can produce name the rule first.
    expect(
      serverRejection('rule "unclosed": error parsing regexp: missing closing ]'),
    ).toBe("unclosed");
    expect(
      serverRejection('rule "slow ban" asks for a timeout but carries no duration'),
    ).toBe("slow ban");
  });

  it("pins nothing when the failure is not about a rule", () => {
    // A save can fail for a listener port, a time zone, or the network. Any of
    // those pinned to a pattern sends an operator to rewrite a working regex.
    expect(serverRejection("an unresolvable display time zone was accepted")).toBeNull();
    // Including one that quotes something of its own. Settings validation is
    // full of %q, so "the first quoted word" would attribute a bad time zone
    // to a rule named after it -- and there is no such rule, so the message
    // would vanish from the page entirely.
    expect(
      serverRejection('display time zone "Mars/Olympus_Mons" could not be loaded'),
    ).toBeNull();
    expect(serverRejection(undefined)).toBeNull();
    expect(serverRejection("")).toBeNull();
  });
});

describe("nextRuleId", () => {
  it("never reuses the id of a deleted rule", () => {
    // Findings carry RuleID into the moderation record. Reusing an id would
    // relabel old records as the work of a rule that never made them.
    expect(nextRuleId([{ ...rule(), id: 7 }, { ...rule(), id: 3 }])).toBe(8);
    expect(nextRuleId([])).toBe(1);
  });
});

describe("confidenceOutOfRange", () => {
  it("catches both values that destroy the checker in opposite directions", () => {
    // 0 removes the floor and every opinion acts; 80, from reading the scale
    // as a percentage, is above every verdict the model can return.
    expect(confidenceOutOfRange(0)).toBe(true);
    expect(confidenceOutOfRange(80)).toBe(true);
    expect(confidenceOutOfRange(0.01)).toBe(false);
    expect(confidenceOutOfRange(0.8)).toBe(false);
    expect(confidenceOutOfRange(1)).toBe(false);
    // A blank number input reads NaN, which is not a floor either.
    expect(confidenceOutOfRange(Number.NaN)).toBe(true);
  });
});

describe("the vocabulary", () => {
  it("has a translated name for every action and checker", () => {
    // A key with no entry renders as a raw key at the operator, and the switch
    // it names is a permission to ban somebody.
    const catalogue = en as Record<string, string>;
    for (const key of [
      ...Object.values(ACTION_KEYS),
      ...Object.values(CHECKER_KEYS),
    ]) {
      expect(catalogue[key], `en.json has no ${key}`).toBeTruthy();
    }
  });

  it("keeps the English names the matrix's accessible labels are built from", () => {
    // These strings became keys when AutomodConfig needed the same six names.
    // The behaviour suite matches on them, so the English must not drift.
    const catalogue = en as Record<string, string>;
    expect(catalogue[ACTION_KEYS.ban]).toBe("Ban");
    expect(catalogue[ACTION_KEYS.delete]).toBe("Delete");
    expect(catalogue[ACTION_KEYS.flag]).toBe("Flag for review");
    expect(catalogue[CHECKER_KEYS.model]).toBe("Model");
    expect(catalogue[CHECKER_KEYS.rules]).toBe("Rules");
  });
});
