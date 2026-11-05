import { describe, expect, it } from "vitest";

import { mutationWarnings } from "./api";

// What a delete could not take with it reaches the operator; the reconcile
// sentence, which the transport toasts on its own, is not said twice.
describe("mutationWarnings", () => {
  it("returns the handler's warnings", () => {
    expect(mutationWarnings({ status: "deleted", warnings: ["a", "b"] })).toEqual(["a", "b"]);
  });
  it("drops the reconcile sentence the server appended last", () => {
    expect(mutationWarnings({ warnings: ["a", "reconcile"], reconcileFailed: true })).toEqual(["a"]);
  });
  it("is empty for a 204 and for a body without warnings", () => {
    expect(mutationWarnings(undefined)).toEqual([]);
    expect(mutationWarnings({ status: "deleted" })).toEqual([]);
  });
});
