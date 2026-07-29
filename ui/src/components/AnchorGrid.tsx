import { useRef } from "react";
import { Label } from "@/components/ui/label";
import type { OverlayAnchor } from "@/lib/types";

// A 3x3 grid for choosing where an overlay sits, replacing a nine-item dropdown.
//
// The dropdown was worse in three specific ways rather than merely less pretty:
// it cost two clicks instead of one, it hid eight of the nine options until
// opened, and it asked the operator to read "middle-left" and picture it. The
// control's own shape is the information here -- a 3x3 arrangement of a thing
// that positions something in a 3x3 space needs no labels to be understood.
//
// Nine cells is the whole reason this is worth a component: it is small enough
// to show every option at once, which is the case a select is worst at.

const ANCHORS: { key: OverlayAnchor; label: string }[] = [
  { key: "top-left", label: "Top left" },
  { key: "top-center", label: "Top centre" },
  { key: "top-right", label: "Top right" },
  { key: "middle-left", label: "Middle left" },
  { key: "center", label: "Centre" },
  { key: "middle-right", label: "Middle right" },
  { key: "bottom-left", label: "Bottom left" },
  { key: "bottom-center", label: "Bottom centre" },
  { key: "bottom-right", label: "Bottom right" },
];

export function AnchorGrid({
  value,
  onChange,
  disabled,
  label = "Position",
}: {
  value: OverlayAnchor;
  onChange: (v: OverlayAnchor) => void;
  disabled?: boolean;
  label?: string;
}) {
  const cells = useRef<(HTMLButtonElement | null)[]>([]);
  const index = Math.max(
    0,
    ANCHORS.findIndex((a) => a.key === value),
  );

  // Arrow keys move in two dimensions, because the control is two-dimensional.
  // A radiogroup that only responded to Left/Right would make the bottom row
  // reachable only by cycling through the middle one, which is exactly the
  // tedium the dropdown had.
  const move = (from: number, dx: number, dy: number) => {
    const col = from % 3;
    const row = Math.floor(from / 3);
    const nextCol = Math.min(2, Math.max(0, col + dx));
    const nextRow = Math.min(2, Math.max(0, row + dy));
    const to = nextRow * 3 + nextCol;
    if (to === from) return;
    onChange(ANCHORS[to].key);
    cells.current[to]?.focus();
  };

  return (
    <div className="flex flex-col gap-1">
      <Label>{label}</Label>
      <div
        role="radiogroup"
        aria-label={label}
        className="grid w-fit grid-cols-3 gap-0.5 rounded-md border border-border bg-muted p-0.5"
      >
        {ANCHORS.map((a, i) => {
          const selected = a.key === value;
          return (
            <button
              key={a.key}
              ref={(el) => {
                cells.current[i] = el;
              }}
              type="button"
              role="radio"
              aria-checked={selected}
              // The name is what a screen reader announces, so it carries the
              // words the dropdown used to show. Sighted users get the
              // position from the grid; unsighted users get it from here.
              aria-label={a.label}
              title={a.label}
              disabled={disabled}
              // Roving tabindex: one tab stop for the whole group, then arrows
              // within it. Nine tab stops for one setting would be worse than
              // the select this replaces.
              tabIndex={selected ? 0 : -1}
              onClick={() => onChange(a.key)}
              onKeyDown={(e) => {
                const d: Record<string, [number, number]> = {
                  ArrowLeft: [-1, 0],
                  ArrowRight: [1, 0],
                  ArrowUp: [0, -1],
                  ArrowDown: [0, 1],
                };
                const step = d[e.key];
                if (!step) return;
                e.preventDefault();
                move(i, step[0], step[1]);
              }}
              className={[
                "size-6 rounded-sm transition-colors",
                "focus-visible:ring-ring focus-visible:ring-2 focus-visible:outline-none",
                "disabled:cursor-not-allowed disabled:opacity-50",
                selected
                  ? "bg-primary"
                  : "bg-card-raised hover:bg-border-strong",
              ].join(" ")}
            >
              {/* A dot rather than a filled cell, so the selected cell reads as
                  "the overlay goes here" rather than as a pressed button. */}
              <span
                aria-hidden
                className={[
                  "mx-auto block size-1.5 rounded-full",
                  selected ? "bg-primary-foreground" : "bg-subtle-foreground",
                ].join(" ")}
              />
            </button>
          );
        })}
      </div>
      <span className="text-muted-foreground text-[10px]">{ANCHORS[index].label}</span>
    </div>
  );
}
