import * as React from "react";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import { cn } from "@/lib/utils";

const Dialog = DialogPrimitive.Root;
const DialogTrigger = DialogPrimitive.Trigger;
const DialogPortal = DialogPrimitive.Portal;
const DialogClose = DialogPrimitive.Close;

const DialogOverlay = React.forwardRef<
  React.ElementRef<typeof DialogPrimitive.Overlay>,
  React.ComponentPropsWithoutRef<typeof DialogPrimitive.Overlay>
>(({ className, ...props }, ref) => (
  <DialogPrimitive.Overlay
    ref={ref}
    className={cn(
      // duration-settle, matching the content it dims for. The two used to
      // run at tw-animate-css's 0.15s default, so the scrim and the dialog it
      // belongs to were free to drift apart the moment either was given a
      // duration of its own.
      "fixed inset-0 z-50 bg-black/70 duration-settle data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0",
      className,
    )}
    {...props}
  />
));
DialogOverlay.displayName = DialogPrimitive.Overlay.displayName;

const DialogContent = React.forwardRef<
  React.ElementRef<typeof DialogPrimitive.Content>,
  React.ComponentPropsWithoutRef<typeof DialogPrimitive.Content>
>(({ className, children, ...props }, ref) => (
  <DialogPortal>
    <DialogOverlay />
    <DialogPrimitive.Content
      ref={ref}
      className={cn(
        "fixed left-1/2 top-1/2 z-50 grid w-full max-w-lg -translate-x-1/2 -translate-y-1/2 gap-3",
        // ELEVATION: overlay — surface --popover, shadow --shadow-overlay.
        //
        // This was shadow-2xl, which is Tailwind's own scale rather than this
        // app's: four of the kit's floating surfaces each picked a shadow by
        // eye (2xl here, xl on menu/select/tooltip/popover), so "how far off
        // the page is a dialog" had four answers and no way to change them
        // together. The Elevation table in docs/DESIGN-SYSTEM.md is the one
        // answer, and it is expressed surface-first because a dark UI reads
        // depth from lightness long before it reads it from shadow.
        "max-h-[90vh] overflow-y-auto rounded-lg border border-border-strong bg-popover p-4 shadow-overlay",
        // duration-settle: a dialog is the spec's slowest step, because it
        // replaces what the operator was looking at rather than adding to it.
        "duration-settle data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 data-[state=closed]:zoom-out-95 data-[state=open]:zoom-in-95",
        className,
      )}
      {...props}
    >
      {children}
      {/* THE ONE ICON-ONLY CONTROL IN THE KIT, and the title is why it is
          allowed to stay one.

          The design system's rule is that a control carries a text label,
          because this is not an application where a mis-click is cheap. A
          dialog's corner X is the case where a visible label genuinely does
          not fit — it sits over the content, not beside it — so it takes the
          floor instead: an accessible name for a screen reader (the sr-only
          span, which is what Radix names the button from) AND hover text for
          everyone else. The same asymmetry Button's tooltip derivation exists
          to close: sr-only text produces no tooltip, so a sighted operator got
          nothing from a glyph a reader was given a word for. */}
      <DialogPrimitive.Close
        title="Close"
        className="absolute right-3 top-3 rounded-sm text-muted-foreground opacity-70 transition-opacity hover:opacity-100 focus:outline-none"
      >
        <X className="h-4 w-4" />
        <span className="sr-only">Close</span>
      </DialogPrimitive.Close>
    </DialogPrimitive.Content>
  </DialogPortal>
));
DialogContent.displayName = DialogPrimitive.Content.displayName;

const DialogHeader = ({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) => (
  <div className={cn("flex flex-col gap-1 pr-6", className)} {...props} />
);
DialogHeader.displayName = "DialogHeader";

const DialogFooter = ({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) => (
  <div className={cn("flex flex-col-reverse gap-2 sm:flex-row sm:justify-end", className)} {...props} />
);
DialogFooter.displayName = "DialogFooter";

/* THE TWO STEPS OF THE SCALE A DIALOG IS ENTITLED TO.
 *
 * A dialog is the one place in this console that asks to be READ rather than
 * scanned: it is asking for a decision, and the operator has stopped watching
 * the meters to make it. The type scale says so — --text-lg for the title (the
 * card-title step) and --text-base for the copy, which the scale names "body
 * prose, dialog copy" in as many words.
 *
 * These were `text-sm` and `text-[11px]`: a title the same size as a table
 * cell, over copy smaller than any other prose in the app. Nothing was wrong
 * with them individually; together they said a dialog is a dense readout,
 * which is the one thing it is not. */
const DialogTitle = React.forwardRef<
  React.ElementRef<typeof DialogPrimitive.Title>,
  React.ComponentPropsWithoutRef<typeof DialogPrimitive.Title>
>(({ className, ...props }, ref) => (
  <DialogPrimitive.Title ref={ref} className={cn("text-lg font-semibold tracking-tight", className)} {...props} />
));
DialogTitle.displayName = DialogPrimitive.Title.displayName;

const DialogDescription = React.forwardRef<
  React.ElementRef<typeof DialogPrimitive.Description>,
  React.ComponentPropsWithoutRef<typeof DialogPrimitive.Description>
>(({ className, ...props }, ref) => (
  <DialogPrimitive.Description ref={ref} className={cn("text-base text-muted-foreground", className)} {...props} />
));
DialogDescription.displayName = DialogPrimitive.Description.displayName;

export {
  Dialog, DialogPortal, DialogOverlay, DialogTrigger, DialogClose,
  DialogContent, DialogHeader, DialogFooter, DialogTitle, DialogDescription,
};
