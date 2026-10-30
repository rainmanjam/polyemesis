import { useState } from "react";
import { checkConnection, type ConnectionOutcome } from "@/lib/api";
import { useT } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

/**
 * Test a pairing token: paste it, and see what the install says it is and may
 * do, before a plugin relies on it. Nothing here changes anything on the
 * install.
 *
 * The answer is about the pasted token alone. An admin token is flagged, since
 * a Stream Deck plugin needs only read access.
 */
export function PairingCheck() {
  const t = useT();
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [outcome, setOutcome] = useState<ConnectionOutcome | null>(null);

  const run = async (e: React.FormEvent) => {
    e.preventDefault();
    const value = token.trim();
    if (!value) return;
    setBusy(true);
    try {
      setOutcome(await checkConnection(value));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={run} className="space-y-3" aria-label={t("pairing.title")}>
      <div>
        <h3 className="text-sm font-medium">{t("pairing.title")}</h3>
        <p className="text-sm text-muted-foreground">{t("pairing.hint")}</p>
      </div>
      <div className="flex flex-wrap items-end gap-2">
        <div className="min-w-[16rem] flex-1 space-y-1">
          <Label htmlFor="pairing-token">{t("pairing.tokenLabel")}</Label>
          <Input
            id="pairing-token"
            type="password"
            autoComplete="off"
            spellCheck={false}
            value={token}
            onChange={(e) => setToken(e.target.value)}
          />
        </div>
        <Button type="submit" variant="outline" disabled={busy || token.trim() === ""}>
          {busy ? t("pairing.testing") : t("pairing.test")}
        </Button>
      </div>
      {outcome && <PairingOutcome outcome={outcome} />}
    </form>
  );
}

/** The scope word, translated. An unknown scope is shown as "session" rather
 * than echoed raw, since only the three the server names can appear. */
function scopeLabel(t: ReturnType<typeof useT>, scope: string): string {
  if (scope === "admin") return t("pairing.scopeAdmin");
  if (scope === "read") return t("pairing.scopeRead");
  return t("pairing.scopeSession");
}

function PairingOutcome({ outcome }: { outcome: ConnectionOutcome }) {
  const t = useT();
  if (!outcome.ok) {
    if (outcome.reason === "refused") {
      return (
        <p role="alert" data-testid="pairing-refused" className="text-sm text-destructive">
          {t("pairing.refused")}
        </p>
      );
    }
    if (outcome.reason === "unreachable") {
      return (
        <p role="alert" data-testid="pairing-unreachable" className="text-sm text-destructive">
          {t("pairing.unreachable")}
        </p>
      );
    }
    return (
      <p role="alert" data-testid="pairing-failed" className="text-sm text-destructive">
        {t("pairing.failed")}
      </p>
    );
  }

  const { info } = outcome;
  return (
    <div className="space-y-2">
      <p data-testid="pairing-result" className="text-sm">
        {t("pairing.result", {
          name: info.name,
          prefix: info.prefix,
          scope: scopeLabel(t, info.scope),
        })}
      </p>
      {info.adminScope && (
        <p role="note" data-testid="pairing-admin-warning" className="text-sm text-warn">
          {t("pairing.adminWarning")}
        </p>
      )}
    </div>
  );
}
