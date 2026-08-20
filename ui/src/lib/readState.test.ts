/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { failedRead, mayClaim, okRead, pendingRead, readFailed, rowsOf } from "./readState";

/* A failed read must never be STORED AS THE SAME VALUE as a successful empty
 * one. Three cards did exactly that, behind `.catch(() => {})`. */

describe("mayClaim", () => {
  it("refuses to let a failed read produce an empty state", () => {
    expect(mayClaim(failedRead<string[]>())).toBe(false);
    expect(mayClaim(pendingRead<string[]>())).toBe(false);
  });

  it("is true only once the server has actually answered", () => {
    const s = okRead<string[]>([]);
    expect(mayClaim(s)).toBe(true);
    // And an answered-empty read still has an empty value: "there are none" is
    // a legitimate answer, it just has to have been given.
    expect(mayClaim(s) && s.value).toEqual([]);
  });
});

describe("rowsOf / readFailed", () => {
  it("contributes no rows for a read that did not answer", () => {
    expect(rowsOf(failedRead<number[]>())).toEqual([]);
    expect(rowsOf(pendingRead<number[]>())).toEqual([]);
    expect(rowsOf(okRead([1, 2]))).toEqual([1, 2]);
  });

  it("distinguishes failed from pending, so the notice is not shown while loading", () => {
    expect(readFailed(failedRead())).toBe(true);
    expect(readFailed(pendingRead())).toBe(false);
    expect(readFailed(okRead(1))).toBe(false);
  });
});

/* AND THAT THE THREE CARDS ACTUALLY ASK. */
const ROOT = new URL("../../../", import.meta.url).pathname;
const read = (p: string) => readFileSync(join(ROOT, p), "utf8");

describe("SettingsPage: the API token list", () => {
  const src = () => read("ui/src/pages/SettingsPage.tsx");

  it("does not render “No tokens yet.” for a read that failed", () => {
    // `api.listTokens().then(setTokens).catch(() => {})` left `tokens` at [],
    // which this card renders as "No tokens yet." -- to somebody auditing who
    // can administer the box.
    expect(src()).not.toContain("api.listTokens().then(setTokens).catch(() => {})");
    expect(src()).toContain("setTokens(failedRead())");
    expect(src()).toContain('{t("set.tokensUnread")}');
    expect(src()).toContain("readFailed(tokens)");
  });

  it("asks before revoking, with the token's name typed", () => {
    // Revoking is irreversible in the strongest sense the app has: the
    // plaintext is gone everywhere. Removing a platform credential in the same
    // file already required typing; this was one click.
    expect(src()).toContain("confirmRevoke.ask(t)");
    expect(src()).not.toContain("onClick={() => revoke(t)}");
    const dialog = src().slice(src().indexOf("open={confirmRevoke.open}"));
    expect(dialog.slice(0, 600)).toContain("requireTyping");
  });
});

describe("SettingsPage: the platform credential reads", () => {
  const src = () => read("ui/src/pages/SettingsPage.tsx");

  it("does not draw an unconfigured install over a configured one", () => {
    expect(src()).not.toContain("api.platformGuides().then(setGuides).catch(() => {})");
    expect(src()).not.toContain("api.listCreds().then(setCreds).catch(() => {})");
    expect(src()).not.toContain("api.listAccounts().then(setAccounts).catch(() => {})");
    expect(src()).toContain("setCreds(failedRead())");
    expect(src()).toContain('{t("set.platformReadFailed")}');
    // The setup forms are withheld until the creds read has answered -- an
    // empty form over a working credential is the whole failure.
    expect(src()).toContain("const credsUsable = mayClaim(creds);");
    expect(src()).toContain("{credsUsable &&");
  });
});

describe("AutomationPage: one failed read, four empty lists", () => {
  const src = () => read("ui/src/pages/AutomationPage.tsx");

  it("says the lists could not be read rather than showing them empty", () => {
    expect(src()).toContain("setLoadFailed(true)");
    expect(src()).toContain('{t("auto.rulesUnread")}');
    expect(src()).toContain('{t("auto.schedulesUnread")}');
    expect(src()).toContain('{t("auto.deliveryUnread")}');
  });

  it("refreshes all four reads, not the two that made the healthy half look healthy", () => {
    // The old refresher re-read only /schedules and /schedules/runs, so
    // Alerts stayed empty until a browser reload while Schedules repopulated.
    expect(src()).not.toContain('autoApi.get<AlertRule[]>("/alerts/rules").then');
    expect(src()).toContain("const t = window.setInterval(() => load(true), 15000);");
  });
});
