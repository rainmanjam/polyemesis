import * as React from "react";
import { cn } from "@/lib/utils";

/* ELEVATION: card — surface --card, and NO SHADOW, because the border carries
 * it. That is the design system's second level, and the absence is the part
 * worth stating: on a near-black surface a drop shadow has almost nothing to
 * darken, so a card that reaches for one gets a smudge instead of depth while
 * a one-pixel border reads immediately. Depth here comes from lightness —
 * --surface behind, --card here, --card-raised for a hovered row — and shadow
 * is reserved for the two levels that actually leave the page.
 *
 * A caller that adds a shadow to a Card is asking for the `raised` level and
 * should say so with --shadow-raised rather than picking one off Tailwind's
 * scale. See the Elevation table in docs/DESIGN-SYSTEM.md. */
const Card = React.forwardRef<HTMLDivElement, React.HTMLAttributes<HTMLDivElement>>(
  ({ className, ...props }, ref) => (
    <div ref={ref} className={cn("rounded-lg border border-border bg-card", className)} {...props} />
  ),
);
Card.displayName = "Card";

/* Tight padding throughout: this is a console, not a marketing page. */
const CardHeader = React.forwardRef<HTMLDivElement, React.HTMLAttributes<HTMLDivElement>>(
  ({ className, ...props }, ref) => (
    <div ref={ref} className={cn("flex flex-col gap-0.5 px-3 py-2.5", className)} {...props} />
  ),
);
CardHeader.displayName = "CardHeader";

/* text-lg is THE card-title step, named as such by the type scale, and it is a
 * real jump from the 13px literal that was here. That literal was the problem
 * rather than the size: 13px is not a step of the scale, so a card title was
 * the same size as body text with more weight, and a page of cards had no
 * structure to scan — every card announced itself exactly as loudly as its own
 * contents. A title one step above the copy under it is what makes a column of
 * cards skimmable. */
const CardTitle = React.forwardRef<HTMLDivElement, React.HTMLAttributes<HTMLDivElement>>(
  ({ className, ...props }, ref) => (
    <div ref={ref} className={cn("text-lg font-semibold leading-tight tracking-tight", className)} {...props} />
  ),
);
CardTitle.displayName = "CardTitle";

const CardDescription = React.forwardRef<HTMLDivElement, React.HTMLAttributes<HTMLDivElement>>(
  ({ className, ...props }, ref) => (
    <div ref={ref} className={cn("text-tiny text-muted-foreground", className)} {...props} />
  ),
);
CardDescription.displayName = "CardDescription";

const CardContent = React.forwardRef<HTMLDivElement, React.HTMLAttributes<HTMLDivElement>>(
  ({ className, ...props }, ref) => <div ref={ref} className={cn("px-3 pb-3", className)} {...props} />,
);
CardContent.displayName = "CardContent";

const CardFooter = React.forwardRef<HTMLDivElement, React.HTMLAttributes<HTMLDivElement>>(
  ({ className, ...props }, ref) => (
    <div ref={ref} className={cn("flex items-center gap-2 border-t border-border px-3 py-2", className)} {...props} />
  ),
);
CardFooter.displayName = "CardFooter";

export { Card, CardHeader, CardFooter, CardTitle, CardDescription, CardContent };
