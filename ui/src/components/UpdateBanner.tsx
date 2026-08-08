import { useEffect, useState } from "react";
import { ExternalLink, X } from "lucide-react";

import { api } from "../lib/api";
import { useT } from "../lib/i18n";
import type { VersionInfo } from "../lib/types";

/** Tells an operator a release exists.
 *
 *  The check itself has been in the server since before this component: two
 *  endpoints, a 6h TTL cache, honest handling of dev builds. Nothing rendered
 *  it, so an install could sit several releases behind with the answer sitting
 *  one HTTP call away and no way to see it.
 *
 *  NOTHING HERE UPGRADES ANYTHING. polyemesis runs live broadcasts and an
 *  upgrade restarts the process, dropping every destination mid-stream, so the
 *  action belongs behind an off-air check that does not exist yet -- see the
 *  rest of issue #127. A link to the release notes is the honest surface until
 *  then: it tells the operator what they need and lets them choose the moment.
 */
export function UpdateBanner() {
  const t = useT();
  const [info, setInfo] = useState<VersionInfo | null>(null);
  const [dismissed, setDismissed] = useState(false);

  useEffect(() => {
    let alive = true;
    // The CACHED answer on mount, never a check. Making every page load reach
    // GitHub would turn a pull-only design into a phone-home one, which is the
    // property this feature was built around.
    api
      .version()
      .then((v) => {
        if (!alive) return;
        setInfo(v);
        // One check per session, and only when the server has never run one --
        // otherwise an install nobody clicks would never learn anything.
        if (!v.checkedAt) {
          api
            .checkUpdate()
            .then((fresh) => alive && setInfo(fresh))
            .catch(() => {});
        }
      })
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, []);

  if (!info || dismissed) return null;
  // A failed check says nothing and must look like nothing: an operator whose
  // box has no outbound network should not see a permanent warning about it.
  if (info.checkFailed || !info.updateAvailable || !info.latest) return null;

  return (
    <div
      role="status"
      className="flex items-center gap-3 border-b border-border bg-muted/50 px-3 py-1.5 text-sm"
    >
      <span className="min-w-0 flex-1 truncate">
        {t("chrome.updateAvailable", { latest: info.latest, current: info.version })}
      </span>
      {info.onAirSummary && (
        // Shown WITH the offer, not instead of it. An operator who learns a
        // release exists is going to act on it eventually, and the useful moment
        // to tell them what is live is while they are deciding -- not after they
        // have clicked something that turned out to end a broadcast.
        <span className="hidden shrink-0 text-muted-foreground sm:inline">
          {t("chrome.updateOnAir", { what: info.onAirSummary })}
        </span>
      )}
      {info.releaseUrl && (
        <a
          href={info.releaseUrl}
          target="_blank"
          rel="noreferrer noopener"
          className="inline-flex shrink-0 items-center gap-1 underline underline-offset-2"
        >
          {t("chrome.releaseNotes")}
          <ExternalLink className="size-3.5" aria-hidden />
        </a>
      )}
      <button
        type="button"
        onClick={() => setDismissed(true)}
        aria-label={t("chrome.dismiss")}
        className="shrink-0 rounded p-0.5 hover:bg-muted"
      >
        <X className="size-3.5" aria-hidden />
      </button>
    </div>
  );
}
