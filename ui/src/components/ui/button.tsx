import * as React from "react";
import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";

/* THE FLOOR UNDER EVERY SIZE BELOW IS IN rem, NOT IN SPACING STEPS.
 *
 * Compact density (see the DENSITY block in index.css) works by rescaling
 * Tailwind's --spacing, which is what `h-7` and every other size here is
 * measured in — so it shrinks the things an operator aims at along with the
 * padding around them. 0.75 of a 28px control is 21px, and a miss on this
 * product's densest page lands on a card carrying Start and Stop.
 *
 * A literal 1.5rem is invisible at comfortable density, where the smallest
 * control is already 28px, and becomes the floor at compact. Spelled with
 * min-h/min-w rather than by giving compact its own size variants, because a
 * second set of sizes is a second set to keep in step. It is on Button alone:
 * Switch and Checkbox are drawn to a fixed geometry that a min-height would
 * pull apart.
 *
 * The icon size is spelled the same way and for the same reason as the type
 * scale: 0.875rem is exactly what size-3.5 resolves to at comfortable density,
 * so nothing moves today, and at compact the glyph keeps its size while the
 * padding around it gives way. Compacting a console means compressing the
 * whitespace, not the content — an icon is the label on half these buttons. */
const buttonVariants = cva(
  "inline-flex min-h-[1.5rem] min-w-[1.5rem] items-center justify-center gap-1.5 whitespace-nowrap rounded-md text-[12px] font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1 focus-visible:ring-offset-surface disabled:pointer-events-none disabled:opacity-40 [&_svg]:pointer-events-none [&_svg]:size-[0.875rem] [&_svg]:shrink-0",
  {
    variants: {
      variant: {
        default: "bg-primary text-primary-foreground hover:bg-primary/85",
        // "live" and "down" are signal colours: only ever on actions that
        // start or stop a stream.
        live: "bg-live text-background hover:bg-live/85",
        destructive: "bg-destructive text-destructive-foreground hover:bg-destructive/85",
        outline: "border border-border-strong bg-transparent hover:bg-accent hover:text-accent-foreground",
        secondary: "bg-secondary text-secondary-foreground hover:bg-card-raised",
        ghost: "hover:bg-accent hover:text-accent-foreground text-muted-foreground",
        link: "text-primary underline-offset-4 hover:underline",
      },
      size: {
        default: "h-8 px-3",
        sm: "h-7 px-2.5 text-[11px]",
        lg: "h-9 px-4",
        icon: "h-8 w-8",
        "icon-sm": "h-7 w-7",
      },
    },
    defaultVariants: { variant: "default", size: "default" },
  },
);

export interface ButtonProps
  extends React.ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof buttonVariants> {
  asChild?: boolean;
}

/* AN ICON BUTTON'S NAME IS ALSO ITS TOOLTIP.
 *
 * Every icon-only button in this codebase already carries an aria-label -- the
 * accessible name is not the gap. The gap is that aria-label produces NO hover
 * text, so the sighted operator moving a mouse over a row of six identical
 * glyphs gets nothing, while a screen-reader user gets a full sentence. Of the
 * icon buttons here, exactly one had also been given a `title`, which is what
 * "remember to write it twice" reliably produces.
 *
 * So the second copy is derived rather than remembered. There is one place to
 * put the words, and a button cannot be given an accessible name without also
 * getting the tooltip. An explicit `title` still wins, for the cases where the
 * hover text should differ from the announced one.
 *
 * Only for the icon sizes. A button with a visible label does not want a
 * tooltip repeating the word already printed on it. */
function iconOnly(size: ButtonProps["size"]): boolean {
  return size === "icon" || size === "icon-sm";
}

const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant, size, asChild = false, ...props }, ref) => {
    const Comp = asChild ? Slot : "button";
    const title =
      props.title ?? (iconOnly(size) ? props["aria-label"] : undefined);
    return (
      <Comp
        className={cn(buttonVariants({ variant, size, className }))}
        ref={ref}
        {...props}
        title={title}
      />
    );
  },
);
Button.displayName = "Button";

export { Button, buttonVariants };
