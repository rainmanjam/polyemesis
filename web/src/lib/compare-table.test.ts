import { describe, expect, it } from "vitest";
import { columnModel, glyphFor } from "./compare-table";

describe("glyphFor", () => {
  it("maps yes, no and not assessed, ignoring case and padding", () => {
    expect(glyphFor("Yes")).toBe("y");
    expect(glyphFor(" no ")).toBe("n");
    expect(glyphFor("Not Assessed")).toBe("q");
  });

  it("gives no glyph to anything else", () => {
    expect(glyphFor("—")).toBeNull();
    expect(glyphFor("")).toBeNull();
    expect(glyphFor("No built-in")).toBeNull();
    expect(glyphFor("Yes: Twitch Enhanced Broadcasting, needs a GPU")).toBeNull();
  });
});

describe("columnModel", () => {
  const cols = [
    { key: "p", name: "polyemesis", poly: true },
    { key: "b", name: "Restreamer" },
  ];

  it("pairs each cell with its column key, text and glyph", () => {
    const m = columnModel(cols, [["Recording", "Yes", "No built-in"]]);
    expect(m.cols).toEqual(cols);
    expect(m.rows).toEqual([
      {
        label: "Recording",
        cells: [
          { col: "p", text: "Yes", glyph: "y" },
          { col: "b", text: "No built-in", glyph: null },
        ],
      },
    ]);
  });

  it("throws when a row is not the label plus one cell per column", () => {
    expect(() => columnModel(cols, [["Recording", "Yes"]])).toThrow(/Recording/);
    expect(() => columnModel(cols, [["Recording", "Yes", "No", "x"]])).toThrow(/3/);
  });
});
