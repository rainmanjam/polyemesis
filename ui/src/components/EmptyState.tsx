import { useId } from "react";
import type { LucideIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

/* ONE EMPTY STATE (A83).
 *
 * Before this, Routing showed one muted sentence with no button, Clips showed
 * "No clips yet.", and Library showed a full dropzone. Each was a different
 * answer to the same question: there is nothing here yet, and here is what to
 * do about it. This is the one shape for that answer.
 *
 * Three parts, in the order the operator reads them:
 *   - title: what is missing, in a few words ("No destinations yet")
 *   - body:  one sentence on why it matters or what it will do
 *   - action: the single primary step that fixes it, when there is one
 *
 * Every string is passed in already translated. The component owns layout, not
 * copy, so a page cannot quietly ship an English sentence through it. Keep
 * `body` to one line; a paragraph belongs in the page, not in an empty state. */

export interface EmptyStateAction {
  label: string;
  onClick: () => void;
  /** Disabled with its reason shown beside it (see DisabledReason), never a
   *  tooltip. The reason is the caller's, already translated. */
  disabled?: boolean;
  disabledReason?: string;
  /** Accessible name when the visible label is not enough on its own. */
  ariaLabel?: string;
}

export interface EmptyStateProps {
  icon?: LucideIcon;
  title: string;
  body?: string;
  action?: EmptyStateAction;
  className?: string;
}

export function EmptyState({ icon: Icon, title, body, action, className }: Readonly<EmptyStateProps>) {
  const generatedId = useId();
  const reasonId = action?.disabled && action.disabledReason ? `${generatedId}-reason` : undefined;
  return (
    <div
      className={cn(
        "flex flex-col items-center gap-2 rounded-md border border-dashed border-border px-4 py-6 text-center",
        className,
      )}
    >
      {Icon && <Icon aria-hidden="true" className="h-5 w-5 text-muted-foreground" />}
      <p className="text-base font-semibold text-foreground">{title}</p>
      {body && <p className="max-w-prose text-sm text-muted-foreground">{body}</p>}
      {action && (
        <>
          <Button
            variant="default"
            onClick={action.onClick}
            disabled={action.disabled}
            aria-label={action.ariaLabel}
            aria-describedby={reasonId}
            className="mt-1"
          >
            {action.label}
          </Button>
          {reasonId && (
            <p id={reasonId} className="text-sm text-muted-foreground">
              {action.disabledReason}
            </p>
          )}
        </>
      )}
    </div>
  );
}
