import { useState } from "react";
import { toast } from "sonner";
import { RotateCcw } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import { useT } from "@/lib/i18n";
import type { RecordingOffload } from "@/lib/types";

const errText = (err: unknown, fallback: string) =>
  err instanceof Error && err.message ? err.message : fallback;

/** The one summary a Library row shows for its uploads. Exported for the test. */
export type OffloadSummary = "stuck" | "failed" | "uploading" | "verified";

export function summarise(o: RecordingOffload): OffloadSummary {
  const states = o.targets.map((x) => x.state);
  if (states.includes("stuck")) return "stuck";
  if (states.includes("failed")) return "failed";
  if (states.some((s) => s === "pending" || s === "uploading")) return "uploading";
  return "verified";
}

/** The Library row's upload state, with Retry now for one that stopped
 *  uploading. Offload copies only: the local file is never touched. */
export function OffloadBadge({
  recordingId,
  offload,
  onChanged,
}: Readonly<{ recordingId: number; offload?: RecordingOffload; onChanged?: () => void }>) {
  const t = useT();
  const [busy, setBusy] = useState(false);
  if (!offload || offload.targets.length === 0) return null;
  const kind = summarise(offload);
  const lastError = offload.targets.find((x) => x.lastError)?.lastError ?? "";
  const failedCount = offload.targets.filter((x) => x.state === "failed" || x.state === "stuck").length;

  const retry = async () => {
    setBusy(true);
    try {
      await api.retryRecordingOffload(recordingId);
      toast.success(t("offload.retried"));
      onChanged?.();
    } catch (err) {
      toast.error(errText(err, t("offload.actionFailed")));
    } finally {
      setBusy(false);
    }
  };

  const badge = {
    stuck: <Badge variant="down" title={lastError}>{t(failedCount === 1 ? "offload.stuckOne" : "offload.stuck", { n: failedCount })}</Badge>,
    failed: <Badge variant="warn" title={lastError}>{t(failedCount === 1 ? "offload.failedOne" : "offload.failed", { n: failedCount })}</Badge>,
    uploading: <Badge variant="outline">{t("offload.uploading")}</Badge>,
    verified: <Badge variant="default">{t("offload.verified")}</Badge>,
  }[kind];

  return (
    <span className="flex items-center gap-0.5" data-testid="offload-badge" data-state={kind}>
      {badge}
      {(kind === "stuck" || kind === "failed") && (
        <Button
          variant="ghost"
          size="icon-sm"
          disabled={busy}
          aria-label={t("offload.retryNow")}
          onClick={() => void retry()}
        >
          <RotateCcw />
        </Button>
      )}
    </span>
  );
}
