import type { TranslationKey } from "@/lib/i18n";
import type { FailureCause } from "@/lib/types";

/** What an operator can DO about a failure cause (A9).
 *
 *  `edit` is the destination's own settings (the key, the address, the
 *  scheme); `renditions` is where the codec and the bitrate a destination
 *  receives are chosen; `log` is the destination's own FFmpeg log, for the
 *  causes that clear by themselves and the one nobody could classify. */
export type RemedyAction = "edit" | "renditions" | "log";

export interface Remedy {
  text: TranslationKey;
  action: RemedyAction;
}

const REMEDY: Record<FailureCause, Remedy> = {
  key_rejected: { text: "reason.remedy.key_rejected", action: "edit" },
  // The platform still holds the old session, or the broadcast is not set up
  // yet. Retrying continues by itself, so an edit would only throw that away.
  platform_not_ready: { text: "reason.remedy.platform_not_ready", action: "log" },
  dns: { text: "reason.remedy.dns", action: "edit" },
  connection_refused: { text: "reason.remedy.connection_refused", action: "edit" },
  timeout: { text: "reason.remedy.timeout", action: "log" },
  tls: { text: "reason.remedy.tls", action: "edit" },
  codec_rejected: { text: "reason.remedy.codec_rejected", action: "renditions" },
  upload_too_slow: { text: "reason.remedy.upload_too_slow", action: "renditions" },
  unknown: { text: "reason.remedy.unknown", action: "log" },
};

/** The remedy for a cause. A cause newer than this UI reads the log: the one
 *  remedy that is never wrong. */
export function remedyFor(cause: FailureCause): Remedy {
  return Object.hasOwn(REMEDY, cause) ? REMEDY[cause] : REMEDY.unknown;
}

/** The Monitoring page's FFmpeg log, filtered to one process. The fragment is
 *  the log card's id, so the link lands on the log rather than the page top. */
export function processLogHref(process: string): string {
  return `/monitoring?process=${encodeURIComponent(process)}#process-log`;
}
