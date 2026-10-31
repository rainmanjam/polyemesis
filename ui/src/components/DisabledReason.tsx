import { Info } from "lucide-react";

/* WHY A CONTROL IS DISABLED, SHOWN WHERE THE EYE ALREADY IS (A28, A61).
 *
 * A title tooltip on a disabled control never appears: the Button primitive
 * sets pointer-events-none when disabled, and a disabled button is not
 * focusable, so keyboard and touch users got no reason at all. The reason is
 * therefore printed as visible text beside the control and linked to it with
 * aria-describedby. Visible to everyone, announced to screen readers, and
 * present whether the operator hovers, taps or tabs.
 *
 * Usage (disabledDescribedBy lives in lib/disabledReason.ts):
 *
 *   const reasonId = useId(); // never a literal: two controls on a page must not share an id
 *   <Button disabled={!canPublish} aria-describedby={disabledDescribedBy(reasonId, canPublish ? null : why)}>
 *     Publish
 *   </Button>
 *   <DisabledReason id={reasonId} reason={canPublish ? null : why} />
 *
 * The reason says what would unlock the control ("Add a destination first"),
 * not what is wrong with the operator. It renders nothing when there is no
 * reason, so an enabled control costs no space. */

export interface DisabledReasonProps {
  id: string;
  /** The translated sentence, or null/empty when the control is enabled. */
  reason: string | null | undefined;
  className?: string;
}

export function DisabledReason({ id, reason, className }: Readonly<DisabledReasonProps>) {
  if (!reason) return null;
  return (
    <p id={id} className={`flex items-start gap-1.5 text-sm text-muted-foreground ${className ?? ""}`.trim()}>
      <Info aria-hidden="true" className="mt-0.5 h-3.5 w-3.5 shrink-0" />
      <span>{reason}</span>
    </p>
  );
}
