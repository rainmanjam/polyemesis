import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";

const badgeVariants = cva(
  "inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-sm font-semibold uppercase tracking-caps",
  {
    variants: {
      variant: {
        default: "border-border-strong bg-secondary text-secondary-foreground",
        outline: "border-border-strong text-muted-foreground",
        // Signal variants. These are the only saturated colours in the kit.
        live: "border-ok/40 bg-ok-dim text-ok",
        warn: "border-warn/40 bg-warn-dim text-warn",
        // No dim fill, and the border keeps it reading as a badge. The fill was
        // dropped when the old red measured 3.88:1 on --down-dim (B2); the
        // 2026-11 coral reads 5.19:1 there and 6.18:1 on --card, so either is
        // AA now. Restoring the fill is a visual decision for the status PR.
        down: "border-down/60 text-down",
        // Neutral since 2026-11 (--idle was cyan --armed); the hollow ring in
        // StatusDot, not a hue, is what tells it from stopped.
        armed: "border-idle/40 bg-idle-dim text-idle",
      },
    },
    defaultVariants: { variant: "default" },
  },
);

export interface BadgeProps
  extends React.HTMLAttributes<HTMLSpanElement>,
    VariantProps<typeof badgeVariants> {}

function Badge({ className, variant, ...props }: Readonly<BadgeProps>) {
  return <span className={cn(badgeVariants({ variant }), className)} {...props} />;
}

export { Badge, badgeVariants };
