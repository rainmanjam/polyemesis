import { useMemo, useState } from "react";
import { ConfirmDestructive } from "@/components/ConfirmDestructive";
import { cn } from "@/lib/utils";
import {
  armedRungs,
  durationParts,
  IRREVERSIBLE_RUNGS,
  LADDER_RUNGS,
  protectionOf,
  rungKey,
  RUNG_KEYS,
  type LadderRung,
} from "@/lib/automodProtection";
import { useT } from "@/lib/i18n";
import type { AutomodCell, AutomodSettings, Settings } from "@/lib/types";

/* THE ACTION LADDER, PER PLATFORM.
 *
 * Three pills a platform: delete, then time out, then ban. A pill is the
 * matrix cell `platform/rung/protection`, switched on or off in the same draft
 * the matrix below edits, so there is one answer to "may this happen" and this
 * card is only another way to reach it. Nothing is armed until a click.
 *
 * A pill the platform cannot perform is inert and says why, with the server's
 * own reason (the capability table is the one source), never an unlit pill
 * that looks like a choice. Arming delete or ban asks first, because
 * polyemesis cannot take either back. */

/** Twitch's AutoMod and blocked terms see only Twitch chat. */
const NATIVE_FILTER_PLATFORMS = new Set(["twitch"]);

export function AutomodProtectionLadder({
  settings,
  onChange,
  platforms,
  cells,
}: Readonly<{
  settings: Settings;
  onChange: (next: Settings) => void;
  platforms: readonly string[];
  cells: readonly AutomodCell[];
}>) {
  const t = useT();
  const automod = settings.automod as AutomodSettings;
  const prot = protectionOf(automod);
  const packOn = prot.state === "on";
  const [asking, setAsking] = useState<{ platform: string; rung: LadderRung } | null>(null);

  const byKey = useMemo(() => {
    const m = new Map<string, AutomodCell>();
    for (const c of cells) m.set(`${c.platform}/${c.action}/${c.checker}`, c);
    return m;
  }, [cells]);

  const timeout = durationParts(prot.ladderTimeoutSeconds);
  const rungLabel = (r: LadderRung) =>
    r === "timeout"
      ? t("automod.protRungTimeout", { duration: t(timeout.key, { n: timeout.n }) })
      : t(RUNG_KEYS[r]);

  function setRung(platform: string, rung: LadderRung, armed: boolean) {
    const on = { ...(automod.on ?? {}) };
    if (armed) on[rungKey(platform, rung)] = true;
    else delete on[rungKey(platform, rung)];
    onChange({ ...settings, automod: { ...automod, on } });
  }

  function press(platform: string, rung: LadderRung, armed: boolean) {
    if (armed) return setRung(platform, rung, false);
    if (IRREVERSIBLE_RUNGS.includes(rung)) return setAsking({ platform, rung });
    setRung(platform, rung, true);
  }

  if (platforms.length === 0) return null;

  return (
    <section aria-labelledby="automod-ladder-title" className="grid gap-2 rounded-md border p-3">
      <div>
        <h3 id="automod-ladder-title" className="text-sm font-medium">
          {t("automod.protLadderTitle")}
        </h3>
        <p className="mt-0.5 text-xs text-muted-foreground">{t("automod.protLadderDesc")}</p>
      </div>
      {!automod.enabled && (
        <p className="text-xs text-warn">{t("automod.protLadderMasterOff")}</p>
      )}
      <ul className="grid gap-2">
        {platforms.map((platform) => {
          const armed = armedRungs(automod.on, platform);
          const name = platform.charAt(0).toUpperCase() + platform.slice(1);
          return (
            <li key={platform} className="grid gap-1 rounded-md border p-2">
              <div className="flex flex-wrap items-center gap-2">
                <span className="w-20 shrink-0 font-medium">{name}</span>
                <div role="group" aria-label={t("automod.protLadderRow", { platform: name })} className="flex flex-wrap gap-1.5">
                  {LADDER_RUNGS.map((rung, i) => {
                    const cell = byKey.get(rungKey(platform, rung));
                    const available = cell?.available ?? false;
                    const isArmed = armed.includes(rung);
                    const label = rungLabel(rung);
                    const sep = i < LADDER_RUNGS.length - 1;
                    if (!available || !packOn) {
                      /* Inert with the reason. A pill that looks pressable and
                         does nothing leaves the operator believing the channel
                         is protected. The platform's reason comes first, as in
                         the matrix: configuring the pack would change nothing
                         on a platform that cannot do it. */
                      const reason = !available
                        ? (cell?.reason ?? t("automod.notSupportedHere"))
                        : t("automod.protectionOff");
                      return (
                        <span key={rung} className="flex items-center gap-1.5">
                          <span
                            className="cursor-help rounded-md border border-dashed px-2 py-1 text-sm text-muted-foreground"
                            title={reason}
                            aria-label={t("automod.protRungUnavailable", { rung: label, platform: name, reason })}
                          >
                            {label}
                          </span>
                          {sep && <span aria-hidden className="text-muted-foreground">→</span>}
                        </span>
                      );
                    }
                    return (
                      <span key={rung} className="flex items-center gap-1.5">
                        <button
                          type="button"
                          aria-pressed={isArmed}
                          aria-label={t("automod.protRungAria", { rung: label, platform: name })}
                          className={cn(
                            "rounded-md border px-2 py-1 text-sm transition-colors",
                            isArmed
                              ? "border-primary bg-primary text-primary-foreground"
                              : "hover:bg-muted",
                          )}
                          onClick={() => press(platform, rung, isArmed)}
                        >
                          {label}
                        </button>
                        {sep && <span aria-hidden className="text-muted-foreground">→</span>}
                      </span>
                    );
                  })}
                </div>
              </div>
              <p className="text-sm text-muted-foreground">
                {armed.length === 0
                  ? t("automod.protNothingArmed")
                  : t("automod.protArmed", { rungs: armed.map(rungLabel).join(", ") })}
              </p>
              {NATIVE_FILTER_PLATFORMS.has(platform) ? (
                <p className="text-sm text-muted-foreground">{t("automod.protTwitchSafe")}</p>
              ) : (
                <p className="text-sm text-muted-foreground">{t("automod.protNoNativeFilter")}</p>
              )}
            </li>
          );
        })}
      </ul>

      <ConfirmDestructive
        open={asking !== null}
        onOpenChange={(o) => !o && setAsking(null)}
        subject={{ unnamed: true }}
        title={t("automod.protConfirmTitle", {
          rung: asking ? rungLabel(asking.rung) : "",
          platform: asking?.platform ?? "",
        })}
        consequence={t("automod.protConfirmConsequence")}
        description={t("automod.protConfirmDesc")}
        confirmLabel={t("automod.protConfirmArm", { rung: asking ? rungLabel(asking.rung) : "" })}
        onConfirm={() => {
          if (asking) setRung(asking.platform, asking.rung, true);
          setAsking(null);
        }}
      />
    </section>
  );
}
