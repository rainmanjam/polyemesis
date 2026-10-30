import { useEffect, useState } from "react";
import { ExternalLink } from "lucide-react";

import { useT } from "@/lib/i18n";

/** The published troubleshooting section for an ingest that never goes live.
 *  The site builds docs/TROUBLESHOOTING.md at /docs/troubleshooting, and the
 *  anchor is the section heading. */
export const TROUBLESHOOT_INGEST_URL = "https://polyemesis.com/docs/troubleshooting#the-ingest-never-goes-live";

/** How long the waiting state pulses before it offers a way out. */
const STUCK_AFTER_MS = 60_000;

/** A source's "waiting for the first frame" state (A2).
 *
 *  Mounted only while the source is waiting, so the timer starts when the wait
 *  does and resets whenever a frame arrives and the panel unmounts. After a
 *  minute of silence it links the troubleshooting section, because an operator
 *  watching a dot pulse for longer than that has stopped expecting it to
 *  change on its own. */
export function WaitingForEncoder() {
  const t = useT();
  const [stuck, setStuck] = useState(false);
  useEffect(() => {
    const id = window.setTimeout(() => setStuck(true), STUCK_AFTER_MS);
    return () => window.clearTimeout(id);
  }, []);

  return (
    <div
      role="status"
      className="flex items-start gap-3 rounded-md border border-dashed border-armed/40 bg-armed-dim/20 px-3 py-2"
    >
      <span aria-hidden className="mt-1 h-2 w-2 shrink-0 rounded-full bg-armed motion-safe:animate-pulse" />
      <div className="min-w-0">
        <p className="text-[12px] font-semibold text-armed">{t("sources.waitingForEncoder")}</p>
        <p className="text-[12px] text-muted-foreground">{t("sources.waitingForEncoderBody")}</p>
        {stuck && (
          <p className="mt-1 text-[12px] text-muted-foreground">
            {t("sources.waitingStuck")}{" "}
            <a
              href={TROUBLESHOOT_INGEST_URL}
              target="_blank"
              rel="noreferrer noopener"
              className="inline-flex items-center gap-1 text-foreground underline underline-offset-2"
            >
              {t("sources.waitingTroubleshoot")}
              <ExternalLink aria-hidden className="h-3 w-3" />
            </a>
          </p>
        )}
      </div>
    </div>
  );
}
