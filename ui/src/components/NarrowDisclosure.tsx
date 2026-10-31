import { useId, useState, type ReactNode } from "react";
import { ChevronDown } from "lucide-react";

import { cn } from "@/lib/utils";

/** A section that is a closed disclosure on a phone and is not there at all on
 *  a desktop (app-council A87).
 *
 *  NOT A <details>. On a desktop this must leave the page exactly as it was,
 *  and on the dashboard that includes the laned layout, where Chat and the
 *  Pipeline card are separate cells of a three-column grid. A <details> would
 *  be the grid item instead of them, and a <details> with no <summary> draws
 *  the browser's own "Details" label. So when `narrow` is false both wrappers
 *  are `display: contents` -- no box, no layout -- and there is no toggle.
 *
 *  THE CHILDREN NEVER MOVE IN THE TREE. The toggle occupies a fixed slot that
 *  is empty on a desktop, so crossing the breakpoint (an iPad turned on its
 *  side) does not remount what is inside: the chat feed keeps its connection
 *  and the stream-info composer keeps what the operator was typing. Collapsed
 *  is `hidden`, not unmounted, for the same reason. */
export function NarrowDisclosure({
  narrow,
  label,
  className,
  children,
}: {
  narrow: boolean;
  label: string;
  className?: string;
  children: ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const id = useId();
  return (
    <div className={narrow ? cn("flex min-w-0 flex-col gap-2", className) : "contents"}>
      {narrow && (
        <button
          type="button"
          aria-expanded={open}
          aria-controls={id}
          onClick={() => setOpen((o) => !o)}
          className="flex w-full items-center justify-between gap-2 rounded-md border border-border bg-card px-3 py-2 text-left text-sm font-medium hover:bg-card-raised focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <span>{label}</span>
          <ChevronDown
            aria-hidden="true"
            className={cn("h-4 w-4 shrink-0 text-muted-foreground transition-transform", open && "rotate-180")}
          />
        </button>
      )}
      <div
        id={id}
        hidden={narrow && !open}
        className={narrow ? (open ? "flex min-w-0 flex-col gap-3" : "hidden") : "contents"}
      >
        {children}
      </div>
    </div>
  );
}
