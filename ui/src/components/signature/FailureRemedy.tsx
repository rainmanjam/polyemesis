import { Link } from "react-router";
import { FileText, Pencil, SlidersHorizontal } from "lucide-react";
import { Button } from "@/components/ui/button";
import { processLogHref, remedyFor } from "@/lib/failureRemedy";
import type { FailureCause } from "@/lib/types";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/** What to do about a failure cause, and the one control that does it (A9).
 *
 *  Shared by the destination card and the Monitoring page so the two never
 *  give different advice for the same cause. The edit action needs a handler
 *  because the destination dialog lives on the dashboard; where there is none
 *  (Monitoring), the sentence still says what to check and the log is
 *  offered instead. The log is a link by default, or `onShowLog` when the log
 *  is already on the page. */
export function FailureRemedy({
  cause,
  process,
  onEdit,
  onShowLog,
  className,
}: {
  cause: FailureCause;
  /** The supervised process name, e.g. "dest:3", for the log link. */
  process: string;
  onEdit?: () => void;
  onShowLog?: () => void;
  className?: string;
}) {
  const t = useT();
  const r = remedyFor(cause);
  const action = r.action === "edit" && !onEdit ? "log" : r.action;
  return (
    <div className={cn("flex flex-col items-start gap-1", className)}>
      <p className="text-[11px] leading-snug">{t(r.text)}</p>
      {action === "edit" && (
        <Button size="sm" variant="outline" onClick={onEdit}>
          <Pencil /> {t("reason.action.edit")}
        </Button>
      )}
      {action === "renditions" && (
        <Button size="sm" variant="outline" asChild>
          <Link to="/renditions">
            <SlidersHorizontal /> {t("reason.action.renditions")}
          </Link>
        </Button>
      )}
      {action === "log" &&
        (onShowLog ? (
          <Button size="sm" variant="outline" onClick={onShowLog}>
            <FileText /> {t("reason.action.log")}
          </Button>
        ) : (
          <Button size="sm" variant="outline" asChild>
            <Link to={processLogHref(process)}>
              <FileText /> {t("reason.action.log")}
            </Link>
          </Button>
        ))}
    </div>
  );
}
