import * as React from "react";
import { cn } from "@/lib/utils";

const Input = React.forwardRef<HTMLInputElement, React.ComponentProps<"input">>(
  ({ className, type, ...props }, ref) => (
    <input
      type={type}
      ref={ref}
      className={cn(
        "flex h-8 w-full rounded-md border border-input bg-background px-2.5 py-1 text-[12px] transition-colors",
        "placeholder:text-subtle-foreground",
        "focus-visible:outline-none focus-visible:border-ring focus-visible:ring-1 focus-visible:ring-ring",
        "disabled:cursor-not-allowed disabled:opacity-50",
        // Numeric inputs are read as columns of figures; keep them tabular.
        (type === "number" || type === "text") && "tnum",
        className,
      )}
      {...props}
    />
  ),
);
Input.displayName = "Input";

export { Input };
