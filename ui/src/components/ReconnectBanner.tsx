import { useEffect, useState } from "react";
import { RefreshCw, X } from "lucide-react";

import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import { useT, type TranslationKey } from "@/lib/i18n";
import { platformName } from "@/lib/platforms";
import { accountsBehind, dismissalKey, readDismissed, writeDismissed } from "@/lib/reconnectBanner";
import type { PlatformAccount } from "@/lib/types";

/** Tells an operator which connected accounts were granted under an older scope
 *  version than the platform now asks for, what a reconnect turns on, and
 *  offers the one-click reconnect (the same OAuth redirect as Settings →
 *  Platform credentials → Connect).
 *
 *  Admin-only by construction: the accounts list is, so a 403 for anyone else
 *  is read as "nothing to show". Dismissal is per account and per required
 *  version, in localStorage, so the next scope bump brings it back. */
export function ReconnectBanner() {
  const t = useT();
  const [accounts, setAccounts] = useState<PlatformAccount[]>([]);
  const [dismissed, setDismissed] = useState<Set<string>>(() => readDismissed());

  useEffect(() => {
    let live = true;
    try {
      api
        .listAccounts()
        .then((a) => {
          if (live) setAccounts(a);
        })
        .catch(() => {
          // Silent: a non-admin session is refused, and this is an offer.
        });
    } catch {
      // Same: never let a banner break the shell.
    }
    return () => {
      live = false;
    };
  }, []);

  const rows = accountsBehind(accounts).filter((a) => !dismissed.has(dismissalKey(a)));
  if (rows.length === 0) return null;

  const dismiss = (a: PlatformAccount) => {
    const next = new Set(dismissed).add(dismissalKey(a));
    setDismissed(next);
    writeDismissed(next);
  };

  return (
    <div className="flex flex-col border-b border-border bg-warn/10" role="status" data-testid="reconnect-banner">
      {rows.map((a) => {
        const platform = platformName(a.platform);
        const unlocks = (a.scopeUnlocks ?? []).map((k) => t(k as TranslationKey)).join("; ");
        return (
          <div key={a.id} className="flex flex-wrap items-center gap-2 px-3 py-1.5">
            <RefreshCw className="h-3.5 w-3.5 shrink-0 text-warn" />
            <span className="min-w-0 flex-1 text-[11px]">{t("reconnect.banner.line", { platform, unlocks })}</span>
            <Button size="sm" asChild>
              <a href={api.connectUrl(a.platform)} aria-label={t("reconnect.banner.actionAria", { platform })}>
                {t("reconnect.banner.action")}
              </a>
            </Button>
            <Button
              size="icon-sm"
              variant="ghost"
              onClick={() => dismiss(a)}
              aria-label={t("reconnect.banner.dismissAria", { platform })}
              title={t("reconnect.banner.dismissAria", { platform })}
            >
              <X />
            </Button>
          </div>
        );
      })}
    </div>
  );
}
