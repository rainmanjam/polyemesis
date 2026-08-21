import { describe, expect, it } from "vitest";
import { api } from "./api";
import { asDestinationId, asSourceId } from "./types";

/* #12: SourceId and DestinationId used to both be bare `number`, so they were
 * mutually assignable with no type error -- the UI-side mirror of #478/#479,
 * where the Go side made the same shape of mistake. `api.deleteDestination`
 * takes a DestinationId now, `api.deleteSource` takes a SourceId, and a value
 * minted as one must not satisfy the other.
 *
 * THIS IS A COMPILE-TIME GUARANTEE, SO THE CHECK IS `npx tsc -b --noEmit`,
 * not this file's assertions. Every marked line below is wrong on purpose;
 * `@ts-expect-error` only compiles clean when the line above it is a genuine
 * type error. Revert the branding -- make `SourceId`/`DestinationId` plain
 * aliases for `number` again -- and these stop being errors, so `tsc -b`
 * fails with "Unused '@ts-expect-error' directive" instead. Mutation-checked
 * by hand: reverting lib/types.ts's brand declarations reproduces exactly
 * that failure, restored afterwards. See fix-ui.md. */
// Type-checked at its definition regardless of whether it ever runs, and it
// deliberately never does -- calling api.deleteDestination/deleteSource for
// real would fire an actual fetch with nothing listening on the other end.
// TS still catches the two swaps below on every `tsc -b`, which is the whole
// check; `void` below only silences "declared but never called".
function idsAreNotInterchangeable(sourceId: ReturnType<typeof asSourceId>, destinationId: ReturnType<typeof asDestinationId>) {
  // @ts-expect-error a SourceId must not satisfy a DestinationId parameter
  api.deleteDestination(sourceId);
  // @ts-expect-error a DestinationId must not satisfy a SourceId parameter
  api.deleteSource(destinationId);
}
void idsAreNotInterchangeable;

describe("SourceId and DestinationId are not mutually assignable (#12)", () => {
  it("mints ids that carry the number they were given", () => {
    // The compile-time claim lives in idsAreNotInterchangeable above and is
    // exercised by `tsc -b`, not by this assertion. This one only proves the
    // two casts above are a meaningful swap -- distinct ids, not the same
    // value under two names -- so the file is not vacuous under `vitest run`
    // alone.
    expect(Number(asSourceId(1))).toBe(1);
    expect(Number(asDestinationId(2))).toBe(2);
  });
});
