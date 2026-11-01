import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useT, type TranslationKey } from "@/lib/i18n";
import type { BroadcastControl } from "@/lib/types";

/** X's limit on the go-live title. X documents the publish title as "subject to
 *  X post length limits" with no number, so the post limit is used; the server
 *  enforces the same bound (db.XGoLiveTitleMax). */
export const X_GO_LIVE_TITLE_MAX = 280;

/** The status key for the phase the lifecycle coordinator recorded. The server
 *  stores its own words for X's six states (created, prepublished, live,
 *  complete, revoked), so these are matched, never X's upper-case ones. */
function statusKey(phase: string | undefined): TranslationKey {
  switch (phase) {
    case "created":
      return "wave3.x.status.created";
    case "prepublished":
      return "wave3.x.status.prepublished";
    case "live":
      return "wave3.x.status.live";
    case "complete":
    case "revoked":
      return "wave3.x.status.ended";
    default:
      return "wave3.x.status.none";
  }
}

/** The X console in the destination dialog (#3).
 *
 *  The gate state comes from the setup guide (`livestreamGate`), because only
 *  the server knows whether the operator switched X Livestream access on. The
 *  broadcast state is the destination's own lifecycle block. The go-live title
 *  is the only X metadata polyemesis can set: X takes it on the publish step and
 *  has no write that changes a running broadcast, so the field is read-only while
 *  the broadcast is live and the server refuses a change then too. */
export function XConsolePanel({
  gate,
  lifecycle,
  title,
  onTitleChange,
}: Readonly<{
  gate?: "open" | "closed";
  lifecycle?: BroadcastControl;
  title: string;
  onTitleChange: (title: string) => void;
}>) {
  const t = useT();
  const live = lifecycle?.phase === "live";
  return (
    <div className="flex flex-col gap-2 rounded-md border p-2">
      <p className="text-xs font-medium">{t("wave3.x.consoleTitle")}</p>
      {gate === "closed" && <span className="text-sm text-warn">{t("wave3.x.gateClosed")}</span>}
      {gate === "open" && (
        <>
          <span className="text-sm text-muted-foreground">{t("wave3.x.gateOpen")}</span>
          <span className="text-sm">{t(statusKey(lifecycle?.phase))}</span>
          {lifecycle?.fault && (
            <span className="text-sm text-warn">{t("wave3.x.status.fault", { fault: lifecycle.fault })}</span>
          )}
        </>
      )}
      <div className="flex flex-col gap-1">
        <Label htmlFor="x-go-live-title">{t("wave3.x.titleLabel")}</Label>
        <Input
          id="x-go-live-title"
          value={title}
          maxLength={X_GO_LIVE_TITLE_MAX}
          disabled={live}
          onChange={(e) => onTitleChange(e.target.value)}
        />
        <span className="text-xs text-muted-foreground">
          {live ? t("wave3.x.titleLocked") : t("wave3.x.titleHint")}
        </span>
        <span className="text-xs text-muted-foreground">
          {t("wave3.x.titleCount", { n: [...title].length, max: X_GO_LIVE_TITLE_MAX })}
        </span>
      </div>
      <span className="text-xs text-muted-foreground">{t("wave3.x.provisional")}</span>
    </div>
  );
}
