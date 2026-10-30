import type { SourceView } from "@/lib/types";

/* What a source card can honestly say about its ingest.
 *
 *  - "unset"     no protocol chosen yet, or pull (which has its own fields).
 *                Nothing to wait for, so nothing is claimed.
 *  - "off"       the source is disabled; publishes are refused regardless.
 *  - "unavailable" SRT or RTMP is configured but the source's listener is not
 *                up (not running, or not enforcing the token). Nothing can
 *                connect, so nothing is waited for.
 *  - "waiting"   SRT or RTMP is configured and no encoder is attached.
 *  - "receiving" an encoder is attached. `publishing` only covers the shared
 *                listener, so a per-port SRT or RTMP source that is live is
 *                seen through its uplink (`link` or `rtmpLink`) instead.
 */
export type IngestPhase = "unset" | "off" | "unavailable" | "waiting" | "receiving";

type IngestFacts = Pick<
  SourceView,
  "enabled" | "ingest" | "publishing" | "link" | "rtmpLink" | "publishUrls" | "running" | "tokenEnforced"
>;

export function ingestPhase(s: IngestFacts): IngestPhase {
  const mode = s.ingest?.mode;
  if (mode !== "srt" && mode !== "rtmp") return "unset";
  if (!s.enabled) return "off";
  // Checked before the uplink: a live publisher on a listener that is not up
  // cannot exist, so this is the answer that stays true.
  if (!s.running || !s.tokenEnforced) return "unavailable";
  if (s.publishing || s.link || s.rtmpLink) return "receiving";
  return "waiting";
}

/** The port of the server's SRT publish address, or null when there is none.
 *  The URL is built by the server with `<server>` where the host goes, so the
 *  port is the only part that is ever read from it here. */
export function srtPortOf(urls: Record<string, string> | undefined): number | null {
  for (const raw of Object.values(urls ?? {})) {
    const m = /^srt:\/\/[^/?]*?:(\d+)/.exec(raw);
    if (m) return Number(m[1]);
  }
  return null;
}

/** The value the ingest select shows once a protocol is chosen, e.g.
 *  `SRT :40557`. Null while unset, so the select's placeholder shows instead
 *  of a blank control. */
export function ingestSummary(
  s: IngestFacts,
): { proto: string; detail: string | null } | null {
  const mode = s.ingest?.mode;
  if (mode === "srt") {
    const port = srtPortOf(s.publishUrls);
    return { proto: "SRT", detail: port === null ? null : `:${port}` };
  }
  if (mode === "rtmp") return { proto: "RTMP", detail: null };
  return null;
}
