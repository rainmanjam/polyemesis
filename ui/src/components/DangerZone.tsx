import { useCallback, useEffect, useState } from "react";
import { AlertTriangle } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { ConfirmDestructive } from "@/components/ConfirmDestructive";
import { api } from "@/lib/api";
import { useT } from "@/lib/i18n";
import type { ApiToken, PlatformAccount, PlatformCreds } from "@/lib/types";

/* THE DANGER ZONE (A17). The last section of Settings, and the one place that
 * gathers what cannot be undone from this page: every API token at once, and a
 * platform's app credentials. Each row says what it does, what it reaches, and
 * carries a verb on its button; every one goes through ConfirmDestructive and
 * asks for the name to be typed, because none of them comes back.
 *
 * The per-item controls stay where they were (a token's own Revoke, a
 * platform card's Remove). This is the install-wide view of the same actions,
 * at the bottom of the rail where a settings page conventionally keeps them,
 * so they are never the first thing under the cursor. */

type Pending =
  | { kind: "tokens"; count: number }
  | { kind: "creds"; platform: string; accounts: number };

export function DangerZone() {
  const t = useT();
  const [tokens, setTokens] = useState<ApiToken[] | null>(null);
  const [creds, setCreds] = useState<PlatformCreds[] | null>(null);
  const [accounts, setAccounts] = useState<PlatformAccount[]>([]);
  const [failed, setFailed] = useState(false);
  const [pending, setPending] = useState<Pending | null>(null);

  const load = useCallback(() => {
    Promise.all([api.listTokens(), api.listCreds(), api.listAccounts()]).then(
      ([tk, cr, ac]) => {
        setTokens(tk ?? []);
        setCreds(cr ?? []);
        setAccounts(ac ?? []);
        setFailed(false);
      },
      () => setFailed(true),
    );
  }, []);
  useEffect(load, [load]);

  const revokeAll = async () => {
    const list = tokens ?? [];
    // One at a time and every answer counted, so a partial failure is reported
    // as the number that did go rather than as "done".
    const results = await Promise.allSettled(list.map((tk) => api.revokeToken(tk.id)));
    const ok = results.filter((r) => r.status === "fulfilled").length;
    if (ok === list.length) toast.success(t("danger.tokensRevoked", { count: ok }));
    else toast.warning(t("danger.tokensPartial", { done: ok, total: list.length }));
    load();
  };

  const removeCreds = async (platform: string) => {
    try {
      await api.deleteCreds(platform);
      toast.success(t("danger.credsRemoved", { platform }));
    } catch (err) {
      // The server's own reason: it refuses while accounts still use the app,
      // and says which, which is more than this page could.
      toast.error(err instanceof Error ? err.message : t("danger.credsFailed", { platform }));
    }
    load();
  };

  const tokenCount = tokens?.length ?? 0;

  return (
    <Card className="border-down/50" data-testid="danger-zone">
      <CardHeader>
        <div className="flex items-center gap-1.5 text-down">
          <AlertTriangle className="h-4 w-4" aria-hidden="true" />
          <CardTitle>{t("danger.title")}</CardTitle>
        </div>
        <CardDescription>{t("danger.body")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col">
        {failed && <p className="pb-3 text-sm text-warn">{t("danger.loadFailed")}</p>}

        <DangerRow
          title={t("danger.tokensTitle")}
          body={t("danger.tokensBody", { count: tokenCount })}
          action={t("danger.tokensAction")}
          disabled={tokens === null || tokenCount === 0}
          onClick={() => setPending({ kind: "tokens", count: tokenCount })}
        />

        {(creds ?? []).map((c) => {
          const using = accounts.filter((a) => a.platform === c.platform).length;
          return (
            <DangerRow
              key={c.platform}
              title={t("danger.credsTitle", { platform: c.platform })}
              body={t("danger.credsBody", { count: using })}
              action={t("set.removeCreds")}
              onClick={() => setPending({ kind: "creds", platform: c.platform, accounts: using })}
            />
          );
        })}
      </CardContent>

      <ConfirmDestructive
        open={pending !== null}
        onOpenChange={(o) => !o && setPending(null)}
        subject={pending?.kind === "creds" ? pending.platform : t("danger.tokensSubject")}
        requireTyping
        title={pending?.kind === "creds" ? t("set.removeCredsTitle") : t("danger.tokensTitle")}
        description={pending?.kind === "creds" ? t("set.removeCredsDesc") : t("danger.tokensConfirmBody")}
        consequences={
          pending?.kind === "creds"
            ? [{ label: t("danger.accountsUsing"), count: pending.accounts }]
            : pending
              ? [{ label: t("danger.tokensLabel"), count: pending.count }]
              : undefined
        }
        consequencesLabel={pending?.kind === "creds" ? t("danger.stopsWorking") : undefined}
        confirmLabel={pending?.kind === "creds" ? t("set.removeCreds") : t("danger.tokensAction")}
        onConfirm={async () => {
          if (!pending) return;
          if (pending.kind === "tokens") await revokeAll();
          else await removeCreds(pending.platform);
          setPending(null);
        }}
      />
    </Card>
  );
}

function DangerRow({
  title,
  body,
  action,
  disabled = false,
  onClick,
}: Readonly<{ title: string; body: string; action: string; disabled?: boolean; onClick: () => void }>) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-3 border-t border-border py-3 first:border-t-0 first:pt-0">
      <div className="flex min-w-0 flex-col gap-0.5">
        <span className="text-sm font-semibold">{title}</span>
        <span className="text-sm text-muted-foreground">{body}</span>
      </div>
      <Button variant="outline" size="sm" className="text-down" disabled={disabled} onClick={onClick}>
        {action}
      </Button>
    </div>
  );
}
