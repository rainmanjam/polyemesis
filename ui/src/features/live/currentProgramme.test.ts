import { describe, expect, it } from "vitest";
import { resolveProgramme } from "./currentProgramme";

describe("resolveProgramme", () => {
  it("keeps what the operator was looking at", () => {
    expect(resolveProgramme([1, 2, 3], 2)).toBe(2);
  });

  it("falls back to the server's first source, which is display order", () => {
    // Same programme the server would have defaulted to -- so on installs where
    // the old behaviour was right this changes what the request SAYS, not what
    // it gets.
    expect(resolveProgramme([7, 8], null)).toBe(7);
  });

  it("discards a remembered programme the server no longer lists", () => {
    // A deleted programme would otherwise 409 on every poll, and the operator
    // would see a dead console with nothing saying why.
    expect(resolveProgramme([4, 5], 99)).toBe(4);
  });

  it("answers null on an install with no sources", () => {
    // The setup wizard's state. Null is honest and the routes accept it:
    // with no sources there is no ambiguity to refuse.
    expect(resolveProgramme([], 3)).toBeNull();
    expect(resolveProgramme([], null)).toBeNull();
  });
});
