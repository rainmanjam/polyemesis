import * as React from "react";
import * as PopoverPrimitive from "@radix-ui/react-popover";
import { cn } from "@/lib/utils";

const Popover = PopoverPrimitive.Root;
const PopoverTrigger = PopoverPrimitive.Trigger;
const PopoverAnchor = PopoverPrimitive.Anchor;

const PopoverContent = React.forwardRef<
  React.ElementRef<typeof PopoverPrimitive.Content>,
  React.ComponentPropsWithoutRef<typeof PopoverPrimitive.Content>
>(({ className, align = "start", sideOffset = 6, ...props }, ref) => (
  <PopoverPrimitive.Portal>
    <PopoverPrimitive.Content
      ref={ref}
      align={align}
      sideOffset={sideOffset}
      // Matched to DropdownMenuContent so the two float alike; wider, because
      // this holds a sentence rather than a list of one-word commands.
      className={cn(
        // ELEVATION: overlay. --popover + --shadow-overlay, the same pairing
        // Dialog, DropdownMenu, Select and Tooltip carry — this was shadow-xl,
        // chosen by eye next to Dialog's shadow-2xl, so two things floating
        // over the same page sat at two different heights for no reason.
        "z-50 w-72 max-w-[calc(100vw-2rem)] rounded-md border border-border-strong bg-popover p-2.5 shadow-overlay",
        // duration-quick: the spec's popover step. It adds to what is on
        // screen rather than replacing it, so it is faster than a dialog.
        "duration-quick data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0",
        className,
      )}
      {...props}
    />
  </PopoverPrimitive.Portal>
));
PopoverContent.displayName = PopoverPrimitive.Content.displayName;

export { Popover, PopoverTrigger, PopoverContent, PopoverAnchor };
