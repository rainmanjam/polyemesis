import { useCallback, useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import { CloudDownload, Loader2, RotateCcw } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { api, ApiError } from "@/lib/api";
import { useT } from "@/lib/i18n";
import type { RecordingOffload } from "@/lib/types";

const errText = (err: unknown, fallback: string) =>
  err instanceof Error && err.message ? err.message : fallback;

/** The one summary a Library row shows for its uploads. Exported for the test. */
export type OffloadSummary = "inStorage" | "fetching" | "fetchFailed" | "stuck" | "failed" | "uploading" | "verified";

export function summarise(o: RecordingOffload): OffloadSummary {
  if (o.fetch?.state === "fetching") return "fetching";
  if (o.evicted) return o.fetch?.state === "failed" ? "fetchFailed" : "inStorage";
  const states = o.targets.map((x) => x.state);
  if (states.includes("stuck")) return "stuck";
  if (states.includes("failed")) return "failed";
  if (states.some((s) => s === "pending" || s === "uploading")) return "uploading";
  return "verified";
}

/** The Library row's upload state, with Fetch back for a recording move mode
 *  removed and Retry now for one that stopped uploading. */
export function OffloadBadge({
  recordingId,
  offload,
  onChanged,
}: Readonly<{ recordingId: number; offload?: RecordingOffload; onChanged?: () => void }>) {
  const t = useT();
  const [busy, setBusy] = useState(false);
  if (!offload) return null;
  const kind = summarise(offload);
  const lastError = offload.targets.find((x) => x.lastError)?.lastError ?? offload.fetch?.error ?? "";
  const failedCount = offload.targets.filter((x) => x.state === "failed" || x.state === "stuck").length;

  const act = async (fn: () => Promise<unknown>, okKey: "offload.fetchStarted" | "offload.retried") => {
    setBusy(true);
    try {
      await fn();
      toast.success(t(okKey));
      onChanged?.();
    } catch (err) {
      toast.error(errText(err, t("offload.actionFailed")));
    } finally {
      setBusy(false);
    }
  };

  const badge = {
    inStorage: <Badge variant="armed" title={t("offload.inStorageTitle")}>{t("offload.inStorage")}</Badge>,
    fetching: (
      <Badge variant="live">
        <Loader2 className="h-2.5 w-2.5 animate-spin" /> {t("offload.fetching")}
      </Badge>
    ),
    fetchFailed: <Badge variant="down" title={lastError}>{t("offload.fetchFailedShort")}</Badge>,
    stuck: <Badge variant="down" title={lastError}>{t("offload.stuck", { n: failedCount })}</Badge>,
    failed: <Badge variant="warn" title={lastError}>{t("offload.failed", { n: failedCount })}</Badge>,
    uploading: <Badge variant="outline">{t("offload.uploading")}</Badge>,
    verified: <Badge variant="default">{t("offload.verified")}</Badge>,
  }[kind];

  return (
    <span className="flex items-center gap-0.5" data-testid="offload-badge" data-state={kind}>
      {badge}
      {(kind === "inStorage" || kind === "fetchFailed") && (
        <Button
          variant="ghost"
          size="icon-sm"
          disabled={busy}
          aria-label={t("offload.fetch")}
          onClick={() => void act(() => api.fetchRecording(recordingId), "offload.fetchStarted")}
        >
          <CloudDownload />
        </Button>
      )}
      {(kind === "stuck" || kind === "failed") && (
        <Button
          variant="ghost"
          size="icon-sm"
          disabled={busy}
          aria-label={t("offload.retryNow")}
          onClick={() => void act(() => api.retryRecordingOffload(recordingId), "offload.retried")}
        >
          <RotateCcw />
        </Button>
      )}
    </span>
  );
}

/** How the clip editor waits for an offloaded recording: it asks for the
 *  fetch, then reads the recording's offload state every two seconds until the
 *  file is local (onReady) or the fetch has failed after its three attempts,
 *  when it shows the error and a Retry button. */
export function OffloadFetchNotice({ recordingId, onReady }: Readonly<{ recordingId: number; onReady: () => void }>) {
  const t = useT();
  const [error, setError] = useState("");
  const [round, setRound] = useState(0);
  const ready = useRef(onReady);
  useEffect(() => {
    ready.current = onReady;
  }, [onReady]);

  const start = useCallback(async () => {
    try {
      await api.fetchRecording(recordingId);
    } catch (err) {
      // 409 "already on local disk": nothing to wait for.
      if (err instanceof ApiError && err.status === 409) {
        ready.current();
        return false;
      }
      setError(errText(err, t("offload.actionFailed")));
      return false;
    }
    return true;
  }, [recordingId, t]);

  useEffect(() => {
    let stop = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const poll = async () => {
      try {
        const { recording } = await api.libraryRecording(recordingId);
        const o = recording.offload;
        if (!o?.evicted) {
          ready.current();
          return;
        }
        if (o.fetch?.state === "failed") {
          setError(o.fetch.error ?? t("offload.actionFailed"));
          return;
        }
      } catch {
        /* a failed read is retried with the next tick */
      }
      if (!stop) timer = setTimeout(() => void poll(), 2000);
    };
    setError("");
    void start().then((go) => {
      if (go && !stop) timer = setTimeout(() => void poll(), 1000);
    });
    return () => {
      stop = true;
      if (timer) clearTimeout(timer);
    };
  }, [recordingId, round, start, t]);

  return (
    <div className="flex flex-col gap-2 rounded-md border border-border p-3" data-testid="offload-fetch-notice">
      {error ? (
        <>
          <p className="text-sm text-down">{t("offload.fetchFailed", { error })}</p>
          <div>
            <Button size="sm" variant="secondary" onClick={() => setRound((r) => r + 1)}>
              <RotateCcw /> {t("offload.retry")}
            </Button>
          </div>
        </>
      ) : (
        <p className="flex items-center gap-1.5 text-sm text-muted-foreground">
          <Loader2 className="h-3.5 w-3.5 animate-spin" /> {t("offload.clipWaiting")}
        </p>
      )}
    </div>
  );
}
