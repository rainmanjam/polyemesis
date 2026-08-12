import { describe, expect, it } from "vitest";
import { canReverify, isSelectableUpload, uploadNotice } from "./upload-verdict";
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

/** #202. The re-check control, and the states it is and is not offered for.
 *
 *  This is the sibling of uploadNotice's split and it draws the SAME line: a
 *  refusal is a statement about the file, and every affordance suggesting that
 *  looking again will change it is the "upload it again" advice in a new
 *  spelling. The two functions disagreeing is the failure this test exists for
 *  — a row that says "sending it again will not change that" above a button
 *  offering to try is worse than either half alone.
 */
describe("canReverify", () => {
  it("offers a re-check for a file the server never managed to inspect", () => {
    // The state the whole feature exists for. Before #202 this row said "Not
    // checked" and offered nothing but re-uploading, which is impossible for a
    // file the operator no longer has locally.
    expect(canReverify(file("unverified", "the inspection was cut short"))).toBe(true);
  });

  it("offers a re-check for a file stored before verdicts existed", () => {
    // Every install has these. They are the largest population of files nobody
    // has read, and they are usable today precisely because nothing refuses
    // them — so a way to find out what they are is the point.
    expect(canReverify(file("unrecorded"))).toBe(true);
  });

  it("does not offer a re-check for a file the server inspected and refused", () => {
    expect(canReverify(file("refused", "polyemesis does not accept this container"))).toBe(
      false,
    );
  });

  it("does not offer a re-check for a file that passed", () => {
    expect(canReverify(file("verified"))).toBe(false);
  });

  // THE CONTROL, and without it a function returning false for everything
  // passes three of the four assertions above.
  it("splits the four states rather than answering one way", () => {
    const answers = (["verified", "unverified", "refused", "unrecorded"] as const).map(
      (o) => canReverify(file(o)),
    );
    expect(new Set(answers).size).toBe(2);
  });

  // The two functions must key on the same field and agree about `refused`.
  // uploadNotice tells the operator re-sending cannot help; a button beside it
  // offering to look again contradicts that in the same row.
  it("never offers a re-check where uploadNotice says looking again cannot help", () => {
    const refused = file("refused", "this file carries no video or audio stream");
    expect(uploadNotice(refused)?.detail).toContain("sending it again will not change that");
    expect(canReverify(refused)).toBe(false);
  });
});
