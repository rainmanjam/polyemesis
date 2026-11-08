import { useState } from "react";
import { Loader2, Send } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { useT, type TranslationKey } from "@/lib/i18n";
import { platformName } from "@/lib/platforms";
import { cn } from "@/lib/utils";
import type { ActivityKind } from "@/lib/types";
import { ACTIVITY_KINDS, FALLBACK_PLATFORMS } from "./activityLib";
import { sendTestAlert, type TestOutcome } from "./sendTest";

/** The platform picker's "no preference" value; Radix Select cannot hold "". */
const ANY = "__any__";

/** Kind and (optionally) platform pickers and a Send button: POST /activity/test
 *  and the answer in words. It works with no platform connected -- the server
 *  makes the event up and sends it down the real path.
 *
 *  `kinds` narrows the kind picker (the overlay editor offers only what the
 *  overlay can draw). Without `platforms` there is no platform picker. */
export function TestAlert({
  kinds = ACTIVITY_KINDS,
  platforms,
  defaultKind = "tip",
  kindLabel = "activity.test.kind",
  idPrefix = "activity-test",
}: Readonly<{
  kinds?: readonly ActivityKind[];
  platforms?: readonly string[];
  defaultKind?: ActivityKind;
  kindLabel?: TranslationKey;
  idPrefix?: string;
}>) {
  const t = useT();
  const [kind, setKind] = useState<ActivityKind>(defaultKind);
  const [platform, setPlatform] = useState(ANY);
  const [busy, setBusy] = useState(false);
  const [outcome, setOutcome] = useState<TestOutcome | null>(null);

  const send = async () => {
    setBusy(true);
    setOutcome(null);
    try {
      setOutcome(await sendTestAlert(t, kind, platform === ANY ? undefined : platform));
    } finally {
      setBusy(false);
    }
  };

  const list = platforms && platforms.length > 0 ? platforms : FALLBACK_PLATFORMS;
  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-end gap-2">
        <div className="flex min-w-36 flex-col gap-1">
          <Label htmlFor={`${idPrefix}-kind`}>{t(kindLabel)}</Label>
          <Select value={kind} onValueChange={(v) => setKind(v as ActivityKind)}>
            <SelectTrigger id={`${idPrefix}-kind`}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {kinds.map((k) => (
                <SelectItem key={k} value={k}>
                  {t(`activity.kind.${k}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        {platforms !== undefined && (
          <div className="flex min-w-36 flex-col gap-1">
            <Label htmlFor={`${idPrefix}-platform`}>{t("activity.test.platform")}</Label>
            <Select value={platform} onValueChange={setPlatform}>
              <SelectTrigger id={`${idPrefix}-platform`}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ANY}>{t("activity.test.anyPlatform")}</SelectItem>
                {list.map((p) => (
                  <SelectItem key={p} value={p}>
                    {platformName(p)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        )}
        <Button size="sm" onClick={send} disabled={busy}>
          {busy ? <Loader2 className="animate-spin" /> : <Send />} {t("activity.sendTest")}
        </Button>
      </div>
      {/* Always mounted, empty until there is an answer: a live region that
          appears already holding text is often not announced. */}
      <p
        role="status"
        aria-live="polite"
        data-test-ok={outcome ? String(outcome.ok) : undefined}
        className={cn("text-sm", outcome?.ok ? "text-ok" : "text-warn")}
      >
        {outcome?.text ?? ""}
      </p>
    </div>
  );
}
