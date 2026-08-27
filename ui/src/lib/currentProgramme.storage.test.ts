// @vitest-environment jsdom

/* THE STORAGE HALF. `resolveProgramme` is pure and was tested; these two were
   not, which is the wrong way round for a pair where one is arithmetic and the
   other touches an API that THROWS. In a private window, or a browser set to
   block site data, every call below raises. A console that will not render
   because it could not read a preference is a far worse failure than one that
   forgot it, and that is the property these pin. */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { rememberProgramme, rememberedProgramme } from "./currentProgramme";

const KEY = "polyemesis.currentProgramme";

describe("rememberedProgramme", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    window.localStorage.clear();
  });

  it("answers null when nothing has been remembered", () => {
    expect(rememberedProgramme()).toBeNull();
  });

  it("returns what was remembered", () => {
    rememberProgramme(12);
    expect(rememberedProgramme()).toBe(12);
  });

  it("refuses a stored value that is not a usable id", () => {
    // Ids are positive integers. Anything else in that key is corruption or
    // another product's leftover; naming it in a request would 409 every poll
    // and leave a dead console with nothing saying why.
    for (const junk of ["", "abc", "0", "-3", "1.5", "NaN"]) {
      window.localStorage.setItem(KEY, junk);
      expect(rememberedProgramme(), `stored ${JSON.stringify(junk)}`).toBeNull();
    }
  });

  it("forgets, rather than throwing, when storage is unreadable", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("The operation is insecure.");
    });
    expect(() => rememberedProgramme()).not.toThrow();
    expect(rememberedProgramme()).toBeNull();
  });
});

describe("rememberProgramme", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    window.localStorage.clear();
  });

  it("clears the key when there is no programme to remember", () => {
    // Not "writes null": the string "null" would come back as junk on the next
    // read and be discarded, which works, but leaves the browser carrying a
    // value that means nothing.
    rememberProgramme(9);
    rememberProgramme(null);
    expect(window.localStorage.getItem(KEY)).toBeNull();
  });

  it("stays quiet when storage refuses the write", () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("QuotaExceededError");
    });
    expect(() => rememberProgramme(4)).not.toThrow();
  });
});
