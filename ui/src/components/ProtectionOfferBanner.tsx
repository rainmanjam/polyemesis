import { useState } from "react";
import { ShieldCheck } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useT } from "@/lib/i18n";

/* THE EXISTING-INSTALL BANNER.
 *
 * An install that predates the protection pack has no answer stored
 * (automod.protection.state is empty), so nothing new runs and this offers it.
 * There is no auto-enable path and "Not now" is final: it records "off", an
 * answer the server keeps, rather than a dismissed notice that comes back on
 * the next visit. Which means the banner clears itself the moment the answer is
 * recorded -- by its caller, from the state it reads. */

export type ProtectionAnswer = "on" | "off";

export function ProtectionOfferBanner({
  onAnswer,
}: Readonly<{ onAnswer: (answer: ProtectionAnswer) => void | Promise<void> }>) {
  const t = useT();
  const [busy, setBusy] = useState(false);
  const answer = async (a: ProtectionAnswer) => {
    setBusy(true);
    try {
      await onAnswer(a);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div role="region" aria-label={t("automod.protOfferTitle")} className="flex flex-wrap items-center gap-3 rounded-md border border-primary/40 bg-primary/5 p-3">
      <ShieldCheck className="size-5 shrink-0 text-primary-bright" aria-hidden />
      <div className="min-w-0 flex-1">
        <p className="text-sm font-medium">{t("automod.protOfferTitle")}</p>
        <p className="text-xs text-muted-foreground">{t("automod.protOfferBody")}</p>
      </div>
      <div className="flex shrink-0 gap-2">
        <Button type="button" size="sm" disabled={busy} onClick={() => void answer("on")}>
          {t("automod.protOfferOn")}
        </Button>
        <Button type="button" size="sm" variant="outline" disabled={busy} onClick={() => void answer("off")}>
          {t("automod.protOfferNotNow")}
        </Button>
      </div>
    </div>
  );
}
