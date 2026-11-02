import * as React from "react";
import * as LabelPrimitive from "@radix-ui/react-label";
import { cn } from "@/lib/utils";

/* --text-sm, which the type scale names for "table cells, form labels" — the
 * console default. It was an 11px literal, one pixel under the smallest step
 * the scale defines, on the element that says what an operator is about to
 * type into. A field's own label is not the place to save a pixel: uppercase
 * at 11px is where l/1/I stop being distinguishable in a word, and the cost of
 * misreading a label is filling in the wrong field.
 *
 * SENTENCE CASE, 12px/500 (2026-11 review). It was uppercase with wide
 * tracking on every field of every form, which turned "Spell these words my
 * way" into a shouted eyebrow and was the single most repeated generated tell
 * in the console. Caps stay where they mark a kind of thing rather than name a
 * field: table headers, menu group labels, status pills and meter scales, all
 * at --tracking-caps. The catalogue already writes labels in sentence case;
 * the uppercase was only ever this class. */
const Label = React.forwardRef<
  React.ElementRef<typeof LabelPrimitive.Root>,
  React.ComponentPropsWithoutRef<typeof LabelPrimitive.Root>
>(({ className, ...props }, ref) => (
  <LabelPrimitive.Root
    ref={ref}
    className={cn(
      "text-sm font-medium text-muted-foreground",
      "peer-disabled:cursor-not-allowed peer-disabled:opacity-70",
      className,
    )}
    {...props}
  />
));
Label.displayName = LabelPrimitive.Root.displayName;

export { Label };
