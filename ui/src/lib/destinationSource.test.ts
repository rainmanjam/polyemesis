import { describe, expect, it } from "vitest";

import {
  initialSourceValue,
  sourceChoices,
  sourceIdForSave,
  sourceIsChosen,
  sourceNameById,
} from "./destinationSource";
import { asSourceId } from "@/lib/types";

const two = [
  { id: asSourceId(1), name: "Main" },
  { id: asSourceId(2), name: "Studio B" },
];

describe("sourceChoices", () => {
  it("gives a nameless source something to be picked by", () => {
    const got = sourceChoices([
      { id: 7, name: "" },
      { id: 8, name: "   " },
    ] as never);
    // Two blank rows in a dropdown cannot be told apart, and picking the wrong
    // programme is the mistake this whole picker exists to prevent.
    expect(got.map((c) => c.name)).toEqual(["Source 7", "Source 8"]);
  });
});

describe("initialSourceValue", () => {
  it("starts on nothing when there is a real choice to make", () => {
    // THE ONE THAT MATTERS. The server used to pick the first source when a
    // request named none; preselecting sources[0] here would move that same
    // silent choice into the UI, where it would look like a decision somebody
    // made.
    expect(initialSourceValue(null, two)).toBe("");
  });

  it("starts on the only programme when there is only one", () => {
    // Not the same case: there is nothing to choose, so demanding a click is
    // ceremony rather than safety.
    expect(initialSourceValue(null, [two[0]])).toBe("1");
  });

  it("starts on the destination's own programme when editing", () => {
    expect(initialSourceValue({ sourceId: asSourceId(2) }, two)).toBe("2");
  });

  it("does not offer to move a destination just because another source exists", () => {
    // A destination on source 2 must not open showing source 1, whatever the
    // order the list arrives in.
    expect(initialSourceValue({ sourceId: asSourceId(2) }, [two[1], two[0]])).toBe("2");
  });
});

describe("sourceIsChosen", () => {
  it("refuses a blank", () => {
    expect(sourceIsChosen("", two)).toBe(false);
  });

  it("refuses an id that is not in the list", () => {
    // A programme deleted in another tab while this dialog sat open. Caught
    // here it is a choice that is no longer available; sent to the server it is
    // a foreign-key failure reported as a failed save.
    expect(sourceIsChosen("99", two)).toBe(false);
  });

  it("refuses values that are not ids at all", () => {
    for (const bad of ["0", "-1", "abc", "1.5e400", "NaN"]) {
      expect(sourceIsChosen(bad, two)).toBe(false);
    }
  });

  it("accepts one that is in the list", () => {
    expect(sourceIsChosen("2", two)).toBe(true);
  });
});

describe("sourceIdForSave", () => {
  it("hands back a number only when it is safe to send", () => {
    expect(sourceIdForSave("2", two)).toBe(2);
  });

  it("hands back null rather than a value the server will refuse", () => {
    // The server refuses a create naming no source, so null must never reach a
    // payload. This is the second line of that, after the disabled save.
    expect(sourceIdForSave("", two)).toBeNull();
    expect(sourceIdForSave("99", two)).toBeNull();
  });
});

describe("sourceNameById", () => {
  it("is empty when there is only one programme", () => {
    // A badge on every card reading the same word is noise: a glance per card
    // spent answering a question nobody has on a single-source install.
    expect(sourceNameById([{ id: 1, name: "Main" }] as never).size).toBe(0);
  });

  it("is empty when there are none at all", () => {
    expect(sourceNameById([] as never).size).toBe(0);
  });

  it("names each programme once there is a choice to disambiguate", () => {
    const got = sourceNameById(two as never);
    expect(got.get(asSourceId(1))).toBe("Main");
    expect(got.get(asSourceId(2))).toBe("Studio B");
    expect(got.size).toBe(2);
  });

  it("gives a nameless programme the same stand-in the picker uses", () => {
    // Or the card and the picker would disagree about what the same source is
    // called, which is worse than either label alone.
    const got = sourceNameById([{ id: 7, name: "" }, { id: 8, name: "x" }] as never);
    expect(got.get(asSourceId(7))).toBe("Source 7");
  });

  it("answers nothing for a programme it does not know", () => {
    // The card renders no badge on a miss rather than the word "undefined",
    // which is what happens to a destination whose source was just deleted.
    expect(sourceNameById(two as never).get(asSourceId(999))).toBeUndefined();
  });
});
