import { describe, expect, it } from "vitest";
import { isSelectableUpload, uploadNotice } from "./upload-verdict";
import type { MediaFile, MediaVerdict } from "./types";

/** THE FOUR STATES, from the client's side.
 *
 *  The server now records "inspected and refused" as its own state instead of
 *  discarding it, and these two functions are what the Library row and the
 *  playlist editor read. Both used to key on `verified` plus the PRESENCE of a
 *  reason string, which answered a different question: "no record at all" was
 *  inferred from an empty reason, and a refusal — which does carry a reason —
 *  would have been lumped in with an uninspected file and told the operator to
 *  upload it again. Re-sending the same bytes earns the same refusal, so that
 *  advice cannot work; saying so is the point of the state.
 */

const file = (outcome: MediaVerdict, unverifiedReason = ""): MediaFile => ({
  name: "show-abcd1234.ts",
  origin: "uploaded",
  bytes: 1,
  modified: "2026-08-11T00:00:00Z",
  pullUrl: "file://uploads/show-abcd1234.ts",
  verified: outcome === "verified",
  outcome,
  unverifiedReason,
});

describe("isSelectableUpload", () => {
  it("does not offer an upload the server inspected and refused", () => {
    expect(isSelectableUpload(file("refused", "not media"))).toBe(false);
  });

  it("does not offer an upload nothing has inspected", () => {
    expect(isSelectableUpload(file("unverified", "the inspection was cut short"))).toBe(
      false,
    );
  });

  // THE TWO CONTROLS. Without them a filter that returned false for everything
  // — which offers the operator an empty list and no explanation — would
  // satisfy both assertions above.
  it("offers an inspected and accepted upload", () => {
    expect(isSelectableUpload(file("verified"))).toBe(true);
  });

  // Every upload an install stored before verdicts existed has no record.
  // Refusing those would strand media an operator has had for a year, so they
  // stay selectable and the normalise worker re-checks them at the moment of
  // use. This is the state that must not be folded in with the two above.
  it("still offers an upload with no record at all", () => {
    expect(isSelectableUpload(file("unrecorded"))).toBe(true);
  });
});

describe("uploadNotice", () => {
  it("says a refused file was refused, not that it was never checked", () => {
    const n = uploadNotice(file("refused", "this file carries no video or audio stream"));
    expect(n?.label).toBe("Refused");
    expect(n?.tone).toBe("refused");
    expect(n?.detail).toContain("this file carries no video or audio stream");
    // The assertion the state exists for: the uninspected remedy must not be
    // offered for a file the server read.
    expect(n?.detail).not.toContain("Upload it again");
    expect(n?.detail).toContain("sending it again will not change that");
  });

  it("keeps the uninspected wording, and its remedy, for an uninspected file", () => {
    const n = uploadNotice(
      file("unverified", "the inspection was cut short before it finished"),
    );
    expect(n?.label).toBe("Not checked");
    expect(n?.tone).toBe("warn");
    expect(n?.detail).toContain("Upload it again");
  });

  it("explains a file that predates verdicts without inventing a reason", () => {
    const n = uploadNotice(file("unrecorded"));
    expect(n?.label).toBe("Not checked");
    expect(n?.detail).toContain("stored before uploads were checked");
    // It has no reason to quote, so it must not quote an empty one.
    expect(n?.detail).not.toContain("undefined");
    expect(n?.detail.startsWith(".")).toBe(false);
  });

  it("says nothing at all about a file that passed", () => {
    expect(uploadNotice(file("verified"))).toBeNull();
  });
});
