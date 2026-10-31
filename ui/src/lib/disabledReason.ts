/* The aria-describedby link for a disabled control (see components/DisabledReason.tsx).
 * Kept out of the component file so that file exports only components. */

/** The aria-describedby value for a control: the reason's id while a reason
 *  is showing, otherwise undefined so an enabled control carries no stale link. */
export function disabledDescribedBy(id: string, reason: string | null | undefined): string | undefined {
  return reason ? id : undefined;
}
