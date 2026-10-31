import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";

const badgeVariants = cva(
  "inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-sm font-semibold uppercase tracking-wider",
  {
    variants: {
      variant: {
        default: "border-border-strong bg-secondary text-secondary-foreground",
        outline: "border-border-strong text-muted-foreground",
        // Signal variants. These are the only saturated colours in the kit.
        live: "border-live/40 bg-live-dim text-live",
        warn: "border-warn/40 bg-warn-dim text-warn",
        // No dim fill under the red: --down on --down-dim measures 3.88:1,
        // below the 4.5:1 a 10px label needs (B2). On the card it is 4.55:1,
        // and the border keeps it reading as a badge.
        down: "border-down/60 text-down",
        armed: "border-armed/40 bg-armed-dim text-armed",
      },
    },
    defaultVariants: { variant: "default" },
  },
);

export interface BadgeProps
  extends React.HTMLAttributes<HTMLSpanElement>,
    VariantProps<typeof badgeVariants> {}

function Badge({ className, variant, ...props }: BadgeProps) {
  return <span className={cn(badgeVariants({ variant }), className)} {...props} />;
}

export { Badge, badgeVariants };
