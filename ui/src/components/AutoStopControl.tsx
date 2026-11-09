import { useEffect, useId, useState } from "react";
import { Loader2, Save } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import { WIRE } from "@/lib/contract/wire";
import { useT } from "@/lib/i18n";
import { LIMITS } from "@/lib/limits";
import type { AutoStopSettings, Source } from "@/lib/types";

/** Stop on ingest loss: one programme's safety net that stops its live
 *  destinations when neither the primary nor the backup ingest has delivered for
 *  the wait. Off by default.
 *
 *  It saves on its own button, to the source, and always sends the WHOLE object:
 *  the source PUT merges over the stored row and the server wants the request
 *  itself to carry `acknowledged: true` whenever it newly turns this on. The
 *  acknowledgement is its own consent (the warning is the one Stop all gives) and
 *  is not the OBS bridge's stopAcknowledged. The server is the authority; the
 *  form mirrors its rules so the operator is not told a round trip later. */

const DEFAULT_WAIT = WIRE.defaults.autoStopWaitSeconds;

/** "" means the default. Anything else must be a whole number in bounds. */
export function parseWait(raw: string): { ok: true; seconds: number } | { ok: false } {
  const s = raw.trim();
  if (s === "") return { ok: true, seconds: 0 };
  if (!/^\d+$/.test(s)) return { ok: false };
  const n = Number(s);
  const { min, max } = LIMITS.autoStopWaitSeconds;
  return n >= min && n <= max ? { ok: true, seconds: n } : { ok: false };
}

const waitText = (v: AutoStopSettings | undefined) =>
  v?.waitSeconds ? String(v.waitSeconds) : "";

export function AutoStopControl({
  sourceId,
  value,
  onSaved,
  idPrefix = "autostop",
}: Readonly<{
  sourceId: number;
  /** The source's stored ingest.autoStop; absent means off. */
  value: AutoStopSettings | undefined;
  /** Called with what was saved, so the caller's copy of the source follows. */
  onSaved?: (next: AutoStopSettings) => void;
  /** Distinguishes the controls when several programmes share a page. */
  idPrefix?: string;
}>) {
  const t = useT();
  const hintId = useId();
  const errId = useId();
  const [enabled, setEnabled] = useState(value?.enabled ?? false);
  const [wait, setWait] = useState(waitText(value));
  const [ack, setAck] = useState(value?.acknowledged ?? false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const stored = JSON.stringify(value ?? null);
  useEffect(() => {
    setEnabled(value?.enabled ?? false);
    setWait(waitText(value));
    setAck(value?.acknowledged ?? false);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- follows the stored value's content, not its identity
  }, [stored]);

  const parsed = parseWait(wait);
  const { min, max } = LIMITS.autoStopWaitSeconds;
  const needsAck = enabled && !ack;
  const dirty =
    enabled !== (value?.enabled ?? false) ||
    wait !== waitText(value) ||
    (enabled && ack !== (value?.acknowledged ?? false));
  const canSave = dirty && parsed.ok && !needsAck && !saving;

  const save = async () => {
    if (!parsed.ok) return;
    const next: AutoStopSettings = {
      enabled,
      waitSeconds: parsed.seconds,
      acknowledged: enabled && ack,
    };
    setSaving(true);
    setError(null);
    try {
      // Only the autoStop block: the server merges it over the stored ingest.
      await api.updateSource(sourceId as Source["id"], { ingest: { autoStop: next } } as unknown as Partial<Source>);
      onSaved?.(next);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="flex flex-col gap-3 rounded-md border border-border p-3" data-testid={`${idPrefix}-control`}>
      <div className="flex items-center justify-between">
        <Label htmlFor={idPrefix}>{t("autostop.title")}</Label>
        <Switch
          id={idPrefix}
          checked={enabled}
          aria-describedby={hintId}
          onCheckedChange={(on) => {
            setEnabled(on);
            if (!on) setAck(false);
          }}
        />
      </div>
      <span id={hintId} className="text-sm text-muted-foreground">
        {t("autostop.desc")}
      </span>
      <span className="text-sm text-muted-foreground">{t("autostop.details")}</span>

      <div className="flex max-w-xs flex-col gap-1">
        <Label htmlFor={`${idPrefix}-wait`}>{t("autostop.wait")}</Label>
        <Input
          id={`${idPrefix}-wait`}
          type="number"
          inputMode="numeric"
          min={min}
          max={max}
          step={1}
          value={wait}
          placeholder={String(DEFAULT_WAIT)}
          aria-invalid={!parsed.ok}
          aria-describedby={parsed.ok ? undefined : `${idPrefix}-wait-err`}
          onChange={(e) => setWait(e.target.value)}
        />
        {parsed.ok ? (
          <span className="text-sm text-muted-foreground">
            {t("autostop.waitHint", { min, max, default: DEFAULT_WAIT })}
          </span>
        ) : (
          <span id={`${idPrefix}-wait-err`} role="alert" className="text-sm text-down">
            {t("autostop.waitInvalid", { min, max })}
          </span>
        )}
      </div>

      {enabled && (
        <div className="flex flex-col gap-2 rounded border border-warn/50 bg-warn/5 p-2">
          <span className="text-sm">{t("autostop.warning")}</span>
          <label className="flex items-center gap-2 text-sm">
            <Checkbox
              id={`${idPrefix}-ack`}
              checked={ack}
              aria-describedby={needsAck ? `${idPrefix}-ack-req` : undefined}
              onCheckedChange={(v) => setAck(v === true)}
            />
            {t("autostop.ack")}
          </label>
          {needsAck && (
            <span id={`${idPrefix}-ack-req`} className="text-sm text-muted-foreground">
              {t("autostop.ackRequired")}
            </span>
          )}
        </div>
      )}

      {error !== null && (
        <span id={errId} role="alert" className="text-sm text-down">
          {error}
        </span>
      )}

      <div>
        <Button type="button" size="sm" disabled={!canSave} onClick={() => void save()}>
          {saving ? <Loader2 className="animate-spin" /> : <Save />} {t("common.apply")}
        </Button>
      </div>
    </div>
  );
}
