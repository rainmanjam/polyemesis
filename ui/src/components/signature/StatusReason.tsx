import { timestamp } from "@/lib/format";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";

export interface StatusReasonProps {
  /** Why the row is not a pass, in the words the server reported. */
  reason: string;
  /** The step that failed, already translated by the caller. Null means the
   *  server did not say, and the line says so instead of going quiet. */
  step?: string | null;
  /** When this reading was taken, as an ISO time. Null, or a Go zero time, means
   *  it was not recorded, and the line says so. */
  asOf?: string | null;
  className?: string;
}

/** The reason, failing step and as-of time for any non-pass status (B1).
 *
 *  Three lines, not a dotted run on one line: the reason is the sentence an
 *  operator reads, and the step and the time are the two facts they check
 *  next. Unknowns are printed as unknowns. That is deliberate, and it is the
 *  rule's whole point: a missing line reads as "nothing to say". */
export function StatusReason({ reason, step, asOf, className }: StatusReasonProps) {
  const t = useT();
  const when = asOf ? timestamp(asOf) : "";
  return (
    <div className={cn("flex min-w-0 flex-col gap-0.5 leading-snug", className)}>
      <p className="break-words">{reason}</p>
      <p className="text-muted-foreground">
        {step ? t("foundation.status.step", { step }) : t("foundation.status.stepUnknown")}
      </p>
      <p className="text-muted-foreground">
        {when ? t("foundation.status.asOf", { time: when }) : t("foundation.status.asOfUnknown")}
      </p>
    </div>
  );
}
