import { X_GO_LIVE_TITLE_MAX } from "@/lib/platforms";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { useT, type TranslationKey } from "@/lib/i18n";
import type { BroadcastControl, XGoLiveTweet } from "@/lib/types";

/** The go-live announcement choices, in the order the picker shows them. */
const TWEET_KEY: Record<XGoLiveTweet, TranslationKey> = {
  first: "wave3.x.tweet.first",
  every: "wave3.x.tweet.every",
  never: "wave3.x.tweet.never",
};

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
 *  the server knows whether the operator switched X Livestream on. The
 *  broadcast state is the destination's own lifecycle block. The go-live title
 *  is the only X metadata polyemesis can set: X takes it on the publish step and
 *  has no write that changes a running broadcast, so the field is read-only while
 *  the broadcast is live and the server refuses a change then too. */
export function XConsolePanel({
  gate,
  lifecycle,
  title,
  onTitleChange,
  tweet,
  onTweetChange,
}: Readonly<{
  gate?: "open" | "closed";
  lifecycle?: BroadcastControl;
  title: string;
  onTitleChange: (title: string) => void;
  /** When X posts its go-live announcement (Destination.xGoLiveTweet). */
  tweet: XGoLiveTweet;
  onTweetChange: (tweet: XGoLiveTweet) => void;
}>) {
  const t = useT();
  // "prepublished" locks the title too: the publish already carried it to X.
  const live = lifecycle?.phase === "live" || lifecycle?.phase === "prepublished";
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
      {/* X posts an announcement on every publish unless told not to
          (should_not_tweet). A show is the time this destination stays on;
          polyemesis reopens a broadcast X ends mid-show, and the default
          publishes that one without a post. */}
      <div className="flex flex-col gap-1">
        <Label htmlFor="x-go-live-tweet">{t("wave3.x.tweetLabel")}</Label>
        <Select value={tweet} onValueChange={(v) => onTweetChange(v as XGoLiveTweet)}>
          <SelectTrigger id="x-go-live-tweet" aria-label={t("wave3.x.tweetLabel")}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {(Object.keys(TWEET_KEY) as XGoLiveTweet[]).map((v) => (
              <SelectItem key={v} value={v}>
                {t(TWEET_KEY[v])}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <span className="text-xs text-muted-foreground">{t("wave3.x.tweetHint")}</span>
      </div>
      <span className="text-xs text-muted-foreground">{t("wave3.x.provisional")}</span>
    </div>
  );
}
