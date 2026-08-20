/// <reference types="node" />
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import { deleteConsequence } from "./renditionUsers";
import type { DestStatus } from "./types";

const dest = (id: number, enabled: boolean): DestStatus =>
  ({ id, name: `d${id}`, enabled }) as DestStatus;

describe("deleteConsequence", () => {
  it("never reports 'nothing selects this' from a socket that has not spoken", () => {
    // THE BUG. Both dialogs were handed `[]` before the first snapshot and
    // branched on length === 0 into "Nothing selects this rendition, so
    // deleting it changes no destination." Three enabled destinations then
    // dropped to passthrough mid-broadcast.
    const c = deleteConsequence(null, { destinations: 3, enabledDestinations: 3 });
    expect(c).toEqual({ kind: "counted", total: 3, enabled: 3 });
  });

  it("names the destinations once the live list is in hand", () => {
    const users = [dest(1, true), dest(2, false)];
    expect(deleteConsequence(users, { destinations: 2, enabledDestinations: 1 })).toEqual({
      kind: "named",
      users,
      enabled: 1,
    });
  });

  it("reports none only from evidence: an empty live list, or a zero row count", () => {
    expect(deleteConsequence([], { destinations: 0, enabledDestinations: 0 })).toEqual({
      kind: "none",
    });
    // The REST counts are a row count out of the database rather than a socket
    // that may simply not have spoken, so a zero there IS evidence.
    expect(deleteConsequence(null, { destinations: 0, enabledDestinations: 0 })).toEqual({
      kind: "none",
    });
  });

  it("claims nothing when neither the live list nor the stored counts are known", () => {
    expect(deleteConsequence(null, null)).toEqual({ kind: "unknown" });
  });
});

/* AND THAT BOTH DIALOGS ACTUALLY ASK.
 *
 * The function is only half the fix: the page has to stop handing them `[]` for
 * "not known", which is the substitution the whole finding is about. */
const ROOT = new URL("../../../", import.meta.url).pathname;
const page = () => readFileSync(join(ROOT, "ui/src/pages/RenditionsPage.tsx"), "utf8");

describe("RenditionsPage, wired to deleteConsequence", () => {
  it("hands both dialogs null rather than an empty list before the snapshot", () => {
    const src = page();
    expect(src).toContain("users={status && editing ? (usersById.get(editing.id) ?? []) : null}");
    expect(src).toContain("users={status && deleting ? (usersById.get(deleting.id) ?? []) : null}");
    // The substitution that produced "deleting it changes no destination" over
    // three enabled destinations.
    expect(src).not.toContain("users={editing ? (usersById.get(editing.id) ?? []) : []}");
    expect(src).not.toContain("users={deleting ? (usersById.get(deleting.id) ?? []) : []}");
  });

  it("gives both dialogs the stored counts to fall back on", () => {
    const src = page();
    expect(src).toContain("counts={editing ? countsFor(editing.id) : null}");
    expect(src).toContain("counts={deleting ? countsFor(deleting.id) : null}");
  });

  it("routes both dialogs' sentences through the verdict, never through a bare length", () => {
    const src = page();
    // Two call sites: the editor and the delete dialog.
    expect(src.match(/const consequence = deleteConsequence\(users, counts\);/g)).toHaveLength(2);
    // `users.length` on its own is the branch that read "not known" as "none".
    // The card's own guarded `users ? users.length : view.destinations` is a
    // different expression and stays.
    expect(src).not.toMatch(/[^.]\busers\.length === 0/);
  });
});
