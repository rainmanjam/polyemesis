import { describe, expect, it } from "vitest";

import { checkerReady } from "../automodConfig";
import type { TranslationKey } from "../i18n";
import type { AutomodChecker, AutomodSettings } from "../types";
import contract from "./automod-readiness.json";

/* The console's checkerReady and the server's checkerConfigured decide the same
 * thing about the same settings. internal/api's TestAutomodReadinessContract
 * runs each case through the server's rule and writes the verdict; this runs the
 * same settings through the console's. Where they disagreed, the console read
 * "nothing automatic" while the banner said a ban was armed. */
describe("checkerReady agrees with the server on every contract case", () => {
  it.each(contract.cases)("$name", (c) => {
    const verdict = checkerReady(c.settings as unknown as AutomodSettings, c.checker as AutomodChecker);
    if (c.ready) {
      expect(verdict).toEqual({ ready: true });
    } else {
      expect(verdict).toEqual({ ready: false, reason: `automod.${c.reason}` as TranslationKey });
    }
  });
});
