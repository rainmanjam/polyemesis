import { useId, useState } from "react";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { useT } from "@/lib/i18n";
import type { DegradedSettings } from "@/lib/types";

/** Degraded-link failover (#6): the per-programme thresholds that send a
 *  primary off air while it is still delivering, but badly.
 *
 *  Its own component so the long failover card on the settings page gains one
 *  line rather than five fields of state plumbing. The defaults shown match the
 *  server's (internal/db DefaultSettings), so a fresh install that ticks the
 *  switch sees the numbers it will actually get. */
/** The server's defaults (internal/db DefaultDegradedSettings). */
const DEFAULTS: Required<Omit<DegradedSettings, "enabled">> = {
  minBitrateKbps: 800,
  maxRttMs: 1500,
  maxLossPercent: 15,
  sustainSeconds: 5,
};

export function DegradedLinkSettings({
  value,
  onChange,
  idPrefix = "degraded",
  collapsible = false,
}: {
  value: DegradedSettings | undefined;
  onChange: (next: DegradedSettings) => void;
  /** Distinguishes the controls when several programmes' cards share a page. */
  idPrefix?: string;
  /** A32. Sources sets this: the four fields sit behind a disclosure whose
   *  summary states the thresholds, and open on their own while the switch is
   *  on. Settings keeps the default, where the fields are always shown. */
  collapsible?: boolean;
}) {
  const t = useT();
  const panelId = useId();
  // null follows the switch: open when on, closed when off. A click overrides it.
  const [userOpen, setUserOpen] = useState<boolean | null>(null);
  const v: DegradedSettings = value ?? { enabled: false };
  const expanded = !collapsible || (userOpen ?? v.enabled);
  const summaryValues = {
    bitrate: v.minBitrateKbps ?? DEFAULTS.minBitrateKbps,
    rtt: v.maxRttMs ?? DEFAULTS.maxRttMs,
    loss: v.maxLossPercent ?? DEFAULTS.maxLossPercent,
  };
  // Every change sends the thresholds the form is SHOWING, defaults included,
  // so a programme saved before #6 -- which has no thresholds stored -- does
  // not switch on with all four at zero and get refused for it.
  const set = (patch: Partial<DegradedSettings>) => onChange({ ...DEFAULTS, ...v, ...patch });
  const num = (field: keyof Omit<DegradedSettings, "enabled">, id: string, label: string, fallback: number, step?: number) => (
    <div className="flex flex-col gap-1">
      <Label htmlFor={id}>{label}</Label>
      <Input
        id={id}
        type="number"
        min={0}
        step={step}
        value={v[field] ?? fallback}
        onChange={(e) => set({ [field]: Number(e.target.value) })}
      />
    </div>
  );
  return (
    <div className="flex flex-col gap-3 rounded-md border border-border p-3">
      <div className="flex items-center justify-between">
        <Label htmlFor={idPrefix}>{t("set.degraded")}</Label>
        <Switch id={idPrefix} checked={v.enabled} onCheckedChange={(enabled) => set({ enabled })} />
      </div>
      <span className="text-sm text-muted-foreground">{t("set.degradedDesc")}</span>
      {collapsible && (
        <button
          type="button"
          aria-expanded={expanded}
          aria-controls={panelId}
          onClick={() => setUserOpen(!expanded)}
          className="flex flex-wrap items-baseline gap-x-2 text-left text-sm"
        >
          <span className="font-medium text-foreground">{t("set.degradedThresholds")}</span>
          <span className="text-muted-foreground">
            {t(v.enabled ? "set.degradedSummaryOn" : "set.degradedSummaryOff", summaryValues)}
          </span>
        </button>
      )}
      {/* Not revealed by the switch on Settings: the thresholds are worth
          reading before deciding to turn this on. Sources collapses them, and
          the summary above carries the same numbers. */}
      {expanded && (
        <div id={panelId} className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          {num("minBitrateKbps", `${idPrefix}-bitrate`, t("set.degradedMinBitrate"), DEFAULTS.minBitrateKbps)}
          {num("sustainSeconds", `${idPrefix}-sustain`, t("set.degradedSustain"), DEFAULTS.sustainSeconds)}
          {num("maxRttMs", `${idPrefix}-rtt`, t("set.degradedMaxRtt"), DEFAULTS.maxRttMs)}
          {num("maxLossPercent", `${idPrefix}-loss`, t("set.degradedMaxLoss"), DEFAULTS.maxLossPercent, 0.1)}
        </div>
      )}
      <span className="text-sm text-muted-foreground">{t("set.degradedSrtOnly")}</span>
      <span className="text-sm text-muted-foreground">{t("set.degradedClearNote")}</span>
    </div>
  );
}
