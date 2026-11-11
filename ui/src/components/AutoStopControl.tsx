import { useEffect, useId, useState } from "react";
import { Loader2, Save } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import { WIRE } from "@/lib/contract/wire";
import { useT, type Translator } from "@/lib/i18n";
import { LIMITS } from "@/lib/limits";
import type { AutoStopInfo, AutoStopSettings, Source } from "@/lib/types";

/** Stop on ingest loss: one programme's safety net that stops its live
 *  destinations when neither the primary nor the backup ingest has delivered for
 *  the wait. Off by default.
 *
 *  It saves on its own button, to the source, and sends ONLY WHAT THIS EDIT
 *  CHANGED, with the version it read: the source PUT merges over the stored row,
 *  so a control left open on an old state must not re-send that state (it would
 *  turn back on what another panel turned off), and `acknowledged` goes only
 *  when the box was ticked in THIS edit session, never because the value it
 *  loaded had it. The server refuses a changing write whose version is not the
 *  stored one (409) and a turn-on that carries none. The acknowledgement is its
 *  own consent (the warning is the one Stop all gives) and is not the OBS
 *  bridge's stopAcknowledged. The server is the authority; the form mirrors its
 *  rules so the operator is not told a round trip later. */

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

/** An acknowledgement belongs to an enabled setting; a leftover on an off one
 *  is not consent for anything. */
const ackOf = (v: AutoStopSettings | undefined) => (v?.enabled ?? false) && (v?.acknowledged ?? false);

const waitText = (v: AutoStopSettings | undefined) =>
  v?.waitSeconds ? String(v.waitSeconds) : "";

export function AutoStopControl({
  sourceId,
  value,
  info,
  onSaved,
  idPrefix = "autostop",
}: Readonly<{
  sourceId: number;
  /** The source's stored ingest.autoStop; absent means off. */
  value: AutoStopSettings | undefined;
  /** The source's autoStopInfo: the version a changing write must send, and
   *  the wait the engine really applies. */
  info?: AutoStopInfo;
  /** Called with what was saved and the server's fresh info, so the caller's
   *  copy of the source follows. */
  onSaved?: (next: AutoStopSettings, info?: AutoStopInfo) => void;
  /** Distinguishes the controls when several programmes share a page. */
  idPrefix?: string;
}>) {
  const t = useT();
  const hintId = useId();
  const errId = useId();
  const [enabled, setEnabled] = useState(value?.enabled ?? false);
  const [wait, setWait] = useState(waitText(value));
  const [ack, setAck] = useState(ackOf(value));
  // Ticked in THIS edit session. The stored acknowledgement never travels.
  const [ackTicked, setAckTicked] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const stored = JSON.stringify(value ?? null);
  useEffect(() => {
    setEnabled(value?.enabled ?? false);
    setWait(waitText(value));
    setAck(ackOf(value));
    setAckTicked(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- follows the stored value's content, not its identity
  }, [stored]);

  const parsed = parseWait(wait);
  const { min, max } = LIMITS.autoStopWaitSeconds;
  const enabling = enabled && !(value?.enabled ?? false);
  // Turning it on needs the box ticked now; an already-on setting keeps the
  // acknowledgement it was given.
  const needsAck = enabling ? !ackTicked : enabled && !ack;
  const dirty =
    enabled !== (value?.enabled ?? false) ||
    wait !== waitText(value) ||
    (enabled && ack !== ackOf(value));
  // The wait the engine really applies, for the STORED setting: shown while the
  // field is untouched, since an edited one has no server answer yet.
  const effective =
    info && value?.enabled && wait === waitText(value) ? (
      <span className="text-sm text-muted-foreground" data-testid={`${idPrefix}-effective`}>
        {effectiveText(info, t)}
      </span>
    ) : null;
  const canSave = dirty && parsed.ok && !needsAck && !saving;

  const save = async () => {
    if (!parsed.ok) return;
    const next: AutoStopSettings = {
      enabled,
      waitSeconds: parsed.seconds,
      acknowledged: enabled && ack,
    };
    const patch = changedAutoStopFields(value, enabled, parsed.seconds, enabling, ackTicked);
    const body: Record<string, unknown> = { ingest: { autoStop: patch } };
    if (info?.version) body.autoStopVersion = info.version;
    setSaving(true);
    setError(null);
    try {
      const saved = await api.updateSource(sourceId as Source["id"], body as unknown as Partial<Source>);
      onSaved?.(next, saved?.autoStopInfo);
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
            if (!on) {
              setAck(false);
              setAckTicked(false);
            }
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
        {effective}
      </div>

      {enabled && (
        <div className="flex flex-col gap-2 rounded border border-warn/50 bg-warn/5 p-2">
          <span className="text-sm">{t("autostop.warning")}</span>
          <label className="flex items-center gap-2 text-sm">
            <Checkbox
              id={`${idPrefix}-ack`}
              checked={ack}
              aria-describedby={needsAck ? `${idPrefix}-ack-req` : undefined}
              onCheckedChange={(v) => {
                setAck(v === true);
                setAckTicked(v === true);
              }}
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

      {/* Only while there is something to save, like the card's own Apply: a
          second always-present "Apply" would break the page's "nothing pending
          means no Apply button" invariant. */}
      {(dirty || saving) && (
        <div>
          <Button type="button" size="sm" disabled={!canSave} onClick={() => void save()}>
            {saving ? <Loader2 className="animate-spin" /> : <Save />} {t("common.apply")}
          </Button>
        </div>
      )}
    </div>
  );
}

// The "effective wait" line: which rule raised the wait, and how long it is.
function effectiveText(
  info: { raisedBy?: string; effectiveWaitSeconds: number },
  t: Translator,
): string {
  if (info.raisedBy === "failover") return t("autostop.effectiveFailover", { seconds: info.effectiveWaitSeconds });
  if (info.raisedBy === "pull") return t("autostop.effectivePull", { seconds: info.effectiveWaitSeconds });
  return t("autostop.effective", { seconds: info.effectiveWaitSeconds });
}

// Only what changed in this edit: the fields the save sends.
function changedAutoStopFields(
  value: AutoStopSettings | undefined,
  enabled: boolean,
  seconds: number,
  enabling: boolean,
  ackTicked: boolean,
): Partial<AutoStopSettings> {
  const patch: Partial<AutoStopSettings> = {};
  if (enabled !== (value?.enabled ?? false)) patch.enabled = enabled;
  if (seconds !== (value?.waitSeconds ?? 0)) patch.waitSeconds = seconds;
  if (enabling && ackTicked) patch.acknowledged = true;
  return patch;
}
