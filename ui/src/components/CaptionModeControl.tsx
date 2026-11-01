import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { useT, type TranslationKey } from "@/lib/i18n";
import { CAPTION_ROUTES, captionModeNeedsDelay, captionModesFor, captionsOffered } from "@/lib/captions";
import type { CaptionMode } from "@/lib/types";

/** The label for each route. A mode maps to its catalogue key, not to its wire
 *  name, so the words can be translated without the server's spelling leaking
 *  into the page. */
const MODE_KEY: Record<CaptionMode, TranslationKey> = {
  off: "wave3.captions.off",
  sei: "wave3.captions.sei",
  youtube_post: "wave3.captions.youtubePost",
  facebook_sei: "wave3.captions.facebookSei",
};

/** Where captions go for one destination (#8). The picker offers only the routes
 *  the platform takes, from the same table the server enforces.
 *
 *  Honest about what it does: the choice is SAVED, but the engine does not send
 *  captions yet, so the note says it changes nothing on air. Kick gets its
 *  sentence instead of a picker, because no route exists to choose. */
export function CaptionModeControl({
  platform,
  value,
  onChange,
  delaySeconds,
}: Readonly<{
  platform: string;
  value: CaptionMode;
  onChange: (mode: CaptionMode) => void;
  /** The destination's broadcast delay, so the POST route can say whether it is met. */
  delaySeconds: number;
}>) {
  const t = useT();
  const route = CAPTION_ROUTES[platform];

  return (
    <div className="flex flex-col gap-2" data-testid="caption-mode">
      <Label htmlFor="dest-caption-mode">{t("wave3.captions.title")}</Label>
      <p className="text-xs text-muted-foreground">{t("wave3.captions.intro")}</p>
      {!captionsOffered(platform) ? (
        <p className="text-xs text-muted-foreground" data-testid="caption-kick-none">
          {t("wave3.captions.kickNone")}
        </p>
      ) : (
        <>
          <Select value={value} onValueChange={(v) => onChange(v as CaptionMode)}>
            <SelectTrigger id="dest-caption-mode" aria-label={t("wave3.captions.modeLabel")}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {captionModesFor(platform).map((m) => (
                <SelectItem key={m} value={m}>
                  {t(MODE_KEY[m])}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {route.status === "provisional" && (
            <p className="text-xs text-warn">{t("wave3.captions.provisional")}</p>
          )}
          {captionModeNeedsDelay(value) && delaySeconds !== 30 && delaySeconds !== 60 && (
            <p className="text-xs text-warn">{t("wave3.captions.delayNote")}</p>
          )}
          {value === "facebook_sei" && <p className="text-xs text-warn">{t("wave3.captions.facebookSwitch")}</p>}
        </>
      )}
      <p className="text-xs text-muted-foreground">{t("wave3.captions.storedOnly")}</p>
    </div>
  );
}
