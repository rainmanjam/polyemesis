/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { chatActionSupport, moderationColumnSaysYes } from "./chatModeration";

/* THE TWO TABLES HAVE TO KEEP AGREEING, and the last time nobody checked, they
 * did not.
 *
 * chatModeration.ts says which chat adapter implements Deleter, Banner and
 * Hider. internal/chat/chat.go says the same thing in the only way that is
 * actually load-bearing: compile-time interface assertions, which is what
 * Hub.Delete, Hub.Ban and Hub.Hide type-assert at runtime before they refuse.
 * A capability claimed here and absent there is a live, enabled button that
 * always errors; a capability withheld here and present there is a control the
 * operator cannot find. Both are worse than the honest answer.
 *
 * The Go file is the reference because it cannot lie: `var _ Banner =
 * (*FacebookAdapter)(nil)` does not compile unless FacebookAdapter has Ban and
 * Unban. This test reads those lines as text and pins the TypeScript mirror to
 * them.
 *
 * Deliberately NOT a check of names in types.ts. That is the shape of guard
 * that let the original defect through: every field involved existed and was
 * spelled correctly, and no operator could reach a working control.
 */

const ROOT = new URL("../../../", import.meta.url).pathname;

/** The platforms Go asserts implement one optional moderation interface.
 *
 *  Reads `_ Deleter = (*TwitchAdapter)(nil)` and friends out of chat.go and
 *  lowercases the adapter prefix, which is the platform id the UI uses. */
function adaptersImplementing(iface: "Deleter" | "Banner" | "Hider"): string[] {
  const src = readFileSync(join(ROOT, "internal/chat/chat.go"), "utf8");
  const re = new RegExp(`_\\s+${iface}\\s*=\\s*\\(\\*(\\w+)Adapter\\)\\(nil\\)`, "g");
  const found = new Set<string>();
  for (const m of src.matchAll(re)) found.add(m[1].toLowerCase());
  return [...found].sort();
}

/** Every platform either table has an opinion about. */
const PLATFORMS = ["youtube", "twitch", "kick", "facebook", "rumble"];

describe("the UI's per-action moderation table against internal/chat", () => {
  it("finds the assertions at all, so a silent zero cannot pass this file", () => {
    // Without this, a renamed file or a reworded assertion would make every
    // check below compare an empty set to an empty set and report success.
    expect(adaptersImplementing("Deleter").length).toBeGreaterThan(0);
    expect(adaptersImplementing("Banner").length).toBeGreaterThan(0);
    expect(adaptersImplementing("Hider").length).toBeGreaterThan(0);
  });

  it("offers delete exactly where an adapter implements Deleter", () => {
    const go = adaptersImplementing("Deleter");
    for (const p of PLATFORMS) {
      expect(chatActionSupport(p, "delete").ok, `delete on ${p}`).toBe(go.includes(p));
    }
  });

  it("offers timeout and ban exactly where an adapter implements Banner", () => {
    const go = adaptersImplementing("Banner");
    for (const p of PLATFORMS) {
      // Both route through Hub.Ban, so both stand or fall on the same assertion.
      expect(chatActionSupport(p, "ban").ok, `ban on ${p}`).toBe(go.includes(p));
      expect(chatActionSupport(p, "timeout").ok, `timeout on ${p}`).toBe(go.includes(p));
    }
  });

  it("offers an upstream hide exactly where an adapter implements Hider", () => {
    const go = adaptersImplementing("Hider");
    for (const p of PLATFORMS) {
      expect(chatActionSupport(p, "hideUpstream").ok, `hide on ${p}`).toBe(go.includes(p));
    }
  });

  it("never withholds the local hide, which asks no platform anything", () => {
    // Hub.HideLocally builds a runner on the spot when none is attached, so it
    // is the one action a disconnected or unsupported platform cannot refuse.
    for (const p of [...PLATFORMS, "custom", "trovo"]) {
      expect(chatActionSupport(p, "hideLocal").ok, `local hide on ${p}`).toBe(true);
    }
  });
});

describe("the gap the per-action table exists to close", () => {
  it("keeps answering no to Facebook bans while the moderation column says yes", () => {
    // THE WHOLE DEFECT, pinned as a fixture. Facebook's capability column is a
    // summary for the setup page and it is correct: polyemesis can moderate
    // Facebook, by deleting and hiding comments. Gating a BAN button on that
    // summary is what produced a control that was enabled and always failed.
    expect(moderationColumnSaysYes("facebook")).toBe(true);
    expect(chatActionSupport("facebook", "ban").ok).toBe(false);
    expect(chatActionSupport("facebook", "delete").ok).toBe(true);
  });

  it("says why, in a sentence that names the alternative", () => {
    // A disabled control with no explanation is only marginally better than a
    // broken one, so the reason is part of the contract rather than a nicety.
    expect(chatActionSupport("facebook", "ban").reason).toMatch(/no chat ban API/);
    expect(chatActionSupport("facebook", "ban").reason).toMatch(/Facebook page itself/);
    expect(chatActionSupport("twitch", "hideUpstream").reason).toMatch(/Only Facebook/);
  });

  it("names Rumble properly rather than echoing the lowercase id", () => {
    // Rumble is outside the ChatPlatform union (types.ts:234) and still arrives
    // at runtime, so platformNoun passes it through unchanged and the menu
    // printed "rumble publishes no moderation API".
    const r = chatActionSupport("rumble", "delete");
    expect(r.ok).toBe(false);
    expect(r.reason).toMatch(/^Rumble /);
  });
});
