/* The model behind CompareTable: which cells carry a glyph, and a check that
 * every row is the label plus one cell per column. */

export type Glyph = "y" | "n" | "q";
export interface Col {
  key: string;
  name: string;
  poly?: boolean;
}

const GLYPHS: Record<string, Glyph> = { yes: "y", no: "n", "not assessed": "q" };

/** A glyph only for a cell that is exactly yes, no or not assessed. */
export function glyphFor(cell: string): Glyph | null {
  return GLYPHS[cell.trim().toLowerCase()] ?? null;
}

export function columnModel(cols: Col[], rows: string[][]) {
  return {
    cols,
    rows: rows.map(([label, ...cells]) => {
      if (cells.length !== cols.length) {
        throw new Error(
          `compare table row "${label}" has ${cells.length} cells for ${cols.length} columns`,
        );
      }
      return {
        label,
        cells: cells.map((text, i) => ({ col: cols[i].key, text, glyph: glyphFor(text) })),
      };
    }),
  };
}

/** The chooser's status wording; the component renders it and the script keeps it. */
export function statusText(names: string[]): string {
  if (names.length < 2) return `Showing ${names[0] ?? ""}`;
  return `Showing ${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
}
