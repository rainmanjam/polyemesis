import { describe, expect, it } from "vitest";

import { schedulesUsing } from "./destinationUsage";
import { ruleReach } from "./automodConfig";

/* DELETE CONFIRMATIONS COUNT WHAT STILL USES THE THING (A18). */
describe("schedulesUsing", () => {
  it("counts the schedules that name the destination, and no others", () => {
    const list = [{ destinationIds: [1, 2] }, { destinationIds: [2] }, { destinationIds: null }, { destinationIds: [3] }];
    expect(schedulesUsing(list, 2)).toBe(2);
    expect(schedulesUsing(list, 9)).toBe(0);
  });
});

describe("ruleReach", () => {
  const automod = {
    enabled: true,
    platformEnabled: { twitch: true, youtube: false },
    on: { "twitch/delete/rules": true, "youtube/delete/rules": true, "kick/delete/rules": true, "kick/ban/rules": true, "twitch/delete/model": true },
  };

  it("counts platforms where the rules checker acts with this rule's action", () => {
    // youtube is switched off as a platform; the model cell is another checker.
    expect(ruleReach(automod, { action: "delete", enabled: true })).toBe(2);
    expect(ruleReach(automod, { action: "ban", enabled: true })).toBe(1);
  });

  it("is zero for a disabled rule or with automod off", () => {
    expect(ruleReach(automod, { action: "delete", enabled: false })).toBe(0);
    expect(ruleReach({ ...automod, enabled: false }, { action: "delete", enabled: true })).toBe(0);
  });
});
