import { describe, expect, it } from "vitest";

import { api } from "./api";

/* submitRecordingJob took `kind` as a bare string, so a typo compiled and only
 * failed as a 400 at runtime (poka-yoke audit 2026-11-04). The kind is now a
 * union; the line below must NOT compile, which `tsc -b` checks: if the
 * parameter goes back to `string`, the directive is unused and the build fails. */
describe("submitRecordingJob's kind", () => {
  it("is a union a typo does not fit", () => {
    const misspelt = () =>
      // @ts-expect-error -- "transcrbe" is not a RecordingJobKind.
      api.submitRecordingJob(1, "transcrbe");
    expect(typeof misspelt).toBe("function");
  });
});
