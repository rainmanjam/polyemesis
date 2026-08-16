import { useCallback, useEffect, useState } from "react";

import { Badge } from "@/components/ui/badge";
import { api } from "@/lib/api";
import { useLanguage, useT } from "@/lib/i18n";
import { toneBadge, toneForState, toneText } from "@/lib/signal";
import { VIEWER_POLL_MS, viewerReadout, type ViewerReadout } from "@/lib/viewerCount";

/* ===========================================================================
   One connected account's live state, beside the account it belongs to.

   Three platforms answer GET /platforms/accounts/{id}/stats and nothing in the
   UI asked, so the capability matrix said "Viewers: Works" while no operator
   could see a number anywhere. This is the asking.

   NOTHING HERE ANIMATES ITS ARRIVAL. docs/DESIGN-SYSTEM.md:104 names the viewer
   count as its example: a count changes because reality changed, and easing it
   in adds latency to the one number the operator is watching. No count-up, no
   fade, no shimmer, and no skeleton — the "checking" state is a word, because a
   skeleton that never resolves is indistinguishable from a broken panel.

   COLOUR RESOLVES THROUGH toneForState/toneBadge, never a hand-written class.
   DestinationCard.tsx:302 records what happens otherwise: `text-ok` is not a
   token, so a healthy backup rendered invisible.

   AND THE TWO NEUTRAL CASES STAY NEUTRAL. "polyemesis cannot ask this platform"
   and "the poll failed" are properties of the READ, not of the broadcast. The
   five saturated tokens mean destination state — painting either of them amber
   would put them in a vocabulary that already means something else, which is
   the reasoning Experimental.tsx sets out at length. Outline, and the words
   carry the weight.
   =========================================================================== */

/** The rendered readout, split out from the polling so it can be rendered from
 *  a plain value in a test. Every branch is a distinct sentence; none of them
 *  is a bare number that could be misread. */
export function ViewerReadoutLine({ readout }: { readout: ViewerReadout }) {
  const t = useT();
  const lang = useLanguage();

  switch (readout.kind) {
    case "unsupported":
      // LABEL, NEVER GATE. The row is not hidden and nothing is disabled: the
      // account still works, it just will not produce this number, and the
      // server's own sentence says which platform and why. Shown verbatim
      // because it names the platform ("polyemesis does not read a viewer count
      // from facebook") and a paraphrase would lose that.
      return (
        <div className="mt-0.5 flex flex-wrap items-center gap-1.5">
          <Badge variant="outline" className="text-[9px]">
            {t("stats.noCount")}
          </Badge>
          <span className="text-[10px] text-muted-foreground">{readout.reason}</span>
        </div>
      );

    case "offline":
      return (
        <div className="mt-0.5 flex items-center gap-1.5">
          <Badge variant={toneBadge[toneForState("stopped")]} className="text-[9px]">
            {t("state.offline")}
          </Badge>
        </div>
      );

    case "count":
      // A REPORTED ZERO IS A FACT AND RENDERS AS ZERO. The stream is live and
      // the platform said nobody is watching; hiding that would be the mirror
      // of the bug this component exists to avoid.
      return (
        <div className="mt-0.5 flex items-center gap-1.5">
          <Badge variant={toneBadge[toneForState("running")]} className="text-[9px]">
            {t("state.live")}
          </Badge>
          <span className={`text-[10px] ${toneText[toneForState("running")]}`}>
            {t("stats.watching", {
              count: new Intl.NumberFormat(lang).format(readout.count),
            })}
          </span>
        </div>
      );

    case "notReported":
      // THE CASE THE WHOLE FEATURE EXISTS FOR. The stream is live and the
      // platform withheld the number — the streamer whose count is hidden in
      // YouTube's settings is the one who would otherwise be told, on a stream
      // with an audience, that nobody is watching.
      //
      // Words, not a symbol. An em dash, a "0", or a greyed-out digit all read
      // as zero at a glance, and this must not; the LIVE badge stays in its
      // signal colour because the stream really is live, while the sentence
      // beside it is muted because it describes the report rather than the
      // broadcast.
      return (
        <div className="mt-0.5 flex flex-wrap items-center gap-1.5">
          <Badge variant={toneBadge[toneForState("running")]} className="text-[9px]">
            {t("state.live")}
          </Badge>
          <span className="text-[10px] text-muted-foreground">{t("stats.notReported")}</span>
        </div>
      );

    case "unreadable":
      // Said out loud rather than left blank. MonitoringPage's freshness
      // tracker records the lesson: a single failed poll is not worth a toast,
      // but silence must not be indefinite or a stale panel reads as current.
      return (
        <div className="mt-0.5 text-[10px] text-muted-foreground">{t("stats.unreadable")}</div>
      );
  }
}

/** Asks, on an interval, while anyone is looking. */
export function AccountLiveStats({ accountId }: { accountId: number }) {
  const t = useT();
  const [readout, setReadout] = useState<ViewerReadout | null>(null);
  const [title, setTitle] = useState<string>("");

  // POLL ONLY WHILE THE PANEL IS VISIBLE. A backgrounded tab left open
  // overnight would otherwise spend a YouTube project's whole daily quota on a
  // number nobody is reading, and take title push down with it — see
  // VIEWER_POLL_MS for the arithmetic. Unmounting stops it; so does hiding the
  // tab, and coming back asks again immediately rather than waiting out the
  // remainder of an interval.
  const [visible, setVisible] = useState(
    () => typeof document === "undefined" || document.visibilityState !== "hidden",
  );
  useEffect(() => {
    if (typeof document === "undefined") return;
    const onChange = () => setVisible(document.visibilityState !== "hidden");
    document.addEventListener("visibilitychange", onChange);
    return () => document.removeEventListener("visibilitychange", onChange);
  }, []);

  const read = useCallback(async () => {
    try {
      const res = await api.accountStats(accountId);
      setReadout(viewerReadout(res));
      setTitle(res.supported ? (res.stats.title ?? "") : "");
    } catch {
      // A 502 from the platform, or the poll racing a disconnect. Not a toast:
      // the next tick recovers, and the line below says so meanwhile.
      setReadout({ kind: "unreadable" });
      setTitle("");
    }
  }, [accountId]);

  // A platform that cannot answer will not start answering, so once it has said
  // so the polling stops. Re-asking every minute for a constant would be spent
  // quota and a request an operator would find in their access log wondering
  // what it was for.
  const settled = readout?.kind === "unsupported";

  useEffect(() => {
    if (!visible || settled) return;
    void read();
    const id = window.setInterval(() => void read(), VIEWER_POLL_MS);
    return () => window.clearInterval(id);
  }, [read, visible, settled]);

  if (!readout) {
    // A word rather than a skeleton, per the header comment.
    return <div className="mt-0.5 text-[10px] text-muted-foreground">{t("stats.checking")}</div>;
  }

  return (
    <>
      <ViewerReadoutLine readout={readout} />
      {/* Platform data, so it is not a catalogue key. Only present when the
          platform sent one, which is only ever while live. */}
      {title && <div className="mt-0.5 truncate text-[10px] text-muted-foreground">{title}</div>}
    </>
  );
}
