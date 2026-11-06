import { WIRE } from "@/lib/contract/wire";

/* The console's counterpart of services.IVSAppPathFix (internal/services/services.go),
 * so the dialog can say so while the URL is being typed rather than after the
 * server refuses the save.
 *
 * An Amazon IVS ingest -- Kick's per-channel host, Twitch's contribute host --
 * takes exactly one RTMP application, "app". Kick's dashboard prints the host
 * with no path at all, and rtmps://<host>/<key> makes the stream key the
 * application name, which IVS answers by dropping the connection. The server
 * refuses a save that introduces such a URL; this offers the corrected one.
 *
 * Offered, never applied: rewriting what somebody typed is how you get a bug
 * report that says "it changed my URL" (db.Warning.Fix).
 *
 * The host rule and the application name come from the platform registry
 * (internal/platform IsIVSIngestHost and IVSApp) through the generated
 * contract, so there is one definition of what an IVS ingest is. The leading
 * dot of the suffix is load-bearing: without it a different registrable domain
 * ("evilglobal-contribute.live-video.net") would match. */
const IVS_HOST_SUFFIX: string = WIRE.platforms.rules.ivsHostSuffix;
const IVS_APP: string = WIRE.platforms.rules.ivsApp;

/** The server URL that works when `raw` is an RTMP(S) URL to an IVS ingest
 *  whose application path is not /app; null for anything else. Keeps the
 *  scheme and any port typed, and adds :443 to an rtmps:// URL without one. */
export function ivsAppPathFix(raw: string): string | null {
  const m = /^(rtmps?):\/\/([^/?#]+)([^?#]*)/i.exec(raw.trim());
  if (!m) return null;
  const scheme = m[1].toLowerCase();
  const authority = m[2];
  if (authority.includes("@")) return null;
  const hostPort = /^([^:]+)(?::(\d+))?$/.exec(authority);
  if (!hostPort) return null;
  const host = hostPort[1].replace(/\.$/, "").toLowerCase();
  if (host.length <= IVS_HOST_SUFFIX.length || !host.endsWith(IVS_HOST_SUFFIX)) return null;
  const first = m[3].replace(/^\/+|\/+$/g, "").split("/")[0];
  if (first === IVS_APP) return null;
  const port = hostPort[2] ?? (scheme === "rtmps" ? "443" : "");
  return `${scheme}://${hostPort[1]}${port ? `:${port}` : ""}/${IVS_APP}`;
}
