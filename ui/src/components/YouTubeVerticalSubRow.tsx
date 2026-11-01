import { Button } from "@/components/ui/button";
import { StatusDot } from "@/components/signature/StatusDot";
import { useYouTubeVerticalHealth } from "@/hooks/useYouTubeVerticalHealth";
import { destinationStatus } from "@/lib/status";
import { toneForState } from "@/lib/signal";
import { useT } from "@/lib/i18n";
import type { DestStatus } from "@/lib/types";

/** The vertical half of a YouTube pair, drawn inside its horizontal card (#7).
 *
 *  It has its own state, its own start, stop and restart, and its own health
 *  line. A failure here is shown on this row and does not touch the horizontal
 *  half: each half is a separate FFmpeg process, and this row only reports what
 *  that process and YouTube's stream status say. */
export function YouTubeVerticalSubRow({
  horizontal,
  vertical,
  busy,
  onStart,
  onStop,
  onRestart,
}: Readonly<{
  horizontal: DestStatus;
  vertical: DestStatus;
  busy?: boolean;
  onStart: () => void;
  onStop: () => void;
  onRestart: () => void;
}>) {
  const t = useT();
  const state = vertical.process?.state;
  const running = state === "running";
  // Polled only while either half is on air. The server gates the same way, so
  // a stopped pair makes no YouTube call at all.
  const live = running || horizontal.process?.state === "running";
  const health = useYouTubeVerticalHealth(horizontal.id, live);
  const status = destinationStatus(vertical.enabled, state, vertical.delay?.phase);

  return (
    <div
      className="mt-2 flex flex-col gap-1 rounded-md border border-border/60 bg-muted/30 p-2"
      data-testid="youtube-vertical-subrow"
      data-vertical-id={vertical.id}
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          <StatusDot tone={toneForState(state)} size="sm" />
          <span className="text-xs font-medium">{t("wave3.vertical.subRow")}</span>
          <span className="truncate text-xs text-muted-foreground">{vertical.name}</span>
          <span className="text-xs">{t(status.labelKey)}</span>
        </div>
        <div className="flex gap-1.5">
          {running ? (
            <Button size="sm" variant="outline" onClick={onStop} disabled={busy}>
              {t("common.stop")}
            </Button>
          ) : (
            <Button size="sm" variant="outline" onClick={onStart} disabled={busy || !vertical.enabled}>
              {t("common.start")}
            </Button>
          )}
          <Button size="sm" variant="ghost" onClick={onRestart} disabled={busy || !vertical.enabled}>
            {t("common.restart")}
          </Button>
        </div>
      </div>
      {vertical.error && <p className="text-xs text-destructive">{vertical.error}</p>}
      {health?.healthError && <p className="text-xs text-warn">{health.healthError}</p>}
      {health && !health.healthError && health.streamStatus && (
        <p className="text-xs text-muted-foreground">
          {t("wave3.vertical.health", { status: health.streamStatus })}
        </p>
      )}
      {!live && <p className="text-xs text-muted-foreground">{t("wave3.vertical.notLive")}</p>}
    </div>
  );
}
