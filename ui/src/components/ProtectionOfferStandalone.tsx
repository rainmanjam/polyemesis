import { useEffect, useState } from "react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { useT } from "@/lib/i18n";
import type { Settings } from "@/lib/types";
import { ProtectionOfferBanner, type ProtectionAnswer } from "@/components/ProtectionOfferBanner";

/* The offer on a page that holds no settings draft (the chat review list).
 * It reads the stored settings itself, shows the banner only while the answer
 * is undecided, and records the operator's answer with a save of the whole
 * document, as the settings page does. A failed read shows nothing: the offer
 * is not worth a false claim about the install's state. */

export function ProtectionOfferStandalone() {
  const t = useT();
  const [settings, setSettings] = useState<Settings | null>(null);

  useEffect(() => {
    let live = true;
    api
      .getSettings()
      .then((s) => live && setSettings(s))
      .catch(() => undefined);
    return () => {
      live = false;
    };
  }, []);

  const prot = settings?.automod?.protection;
  if (!settings?.automod || !prot || prot.state !== "") return null;
  const automod = settings.automod;

  async function answer(a: ProtectionAnswer) {
    try {
      const saved = await api.putSettings({
        ...settings!,
        automod: { ...automod, protection: { ...prot!, state: a } },
      });
      setSettings(saved);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("automod.protOfferFailed"));
    }
  }

  return <ProtectionOfferBanner onAnswer={answer} />;
}
