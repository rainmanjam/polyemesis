import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";

const badgeVariants = cva(
  "inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wider",
  {
    variants: {
      variant: {
        default: "border-border-strong bg-secondary text-secondary-foreground",
        outline: "border-border-strong text-muted-foreground",
        // Signal variants. These are the only saturated colours in the kit.
        live: "border-live/40 bg-live-dim text-live",
        warn: "border-warn/40 bg-warn-dim text-warn",
        down: "border-down/40 bg-down-dim text-down",
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
