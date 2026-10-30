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
}: {
  value: DegradedSettings | undefined;
  onChange: (next: DegradedSettings) => void;
  /** Distinguishes the controls when several programmes' cards share a page. */
  idPrefix?: string;
}) {
  const t = useT();
  const v: DegradedSettings = value ?? { enabled: false };
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
      {/* Always shown, not revealed by the switch: the thresholds are worth
          reading before deciding to turn this on. */}
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        {num("minBitrateKbps", `${idPrefix}-bitrate`, t("set.degradedMinBitrate"), DEFAULTS.minBitrateKbps)}
        {num("sustainSeconds", `${idPrefix}-sustain`, t("set.degradedSustain"), DEFAULTS.sustainSeconds)}
        {num("maxRttMs", `${idPrefix}-rtt`, t("set.degradedMaxRtt"), DEFAULTS.maxRttMs)}
        {num("maxLossPercent", `${idPrefix}-loss`, t("set.degradedMaxLoss"), DEFAULTS.maxLossPercent, 0.1)}
      </div>
      <span className="text-sm text-muted-foreground">{t("set.degradedSrtOnly")}</span>
      <span className="text-sm text-muted-foreground">{t("set.degradedClearNote")}</span>
    </div>
  );
}
