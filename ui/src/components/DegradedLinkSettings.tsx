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
export function DegradedLinkSettings({
  value,
  onChange,
}: {
  value: DegradedSettings | undefined;
  onChange: (next: DegradedSettings) => void;
}) {
  const t = useT();
  const v: DegradedSettings = value ?? { enabled: false };
  const set = (patch: Partial<DegradedSettings>) => onChange({ ...v, ...patch });
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
        <Label htmlFor="fo-degraded">{t("set.degraded")}</Label>
        <Switch id="fo-degraded" checked={v.enabled} onCheckedChange={(enabled) => set({ enabled })} />
      </div>
      <span className="text-sm text-muted-foreground">{t("set.degradedDesc")}</span>
      {v.enabled && (
        <>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            {num("minBitrateKbps", "fo-degraded-bitrate", t("set.degradedMinBitrate"), 800)}
            {num("sustainSeconds", "fo-degraded-sustain", t("set.degradedSustain"), 5)}
            {num("maxRttMs", "fo-degraded-rtt", t("set.degradedMaxRtt"), 1500)}
            {num("maxLossPercent", "fo-degraded-loss", t("set.degradedMaxLoss"), 15, 0.1)}
          </div>
          <span className="text-sm text-muted-foreground">{t("set.degradedSrtOnly")}</span>
          <span className="text-sm text-muted-foreground">{t("set.degradedClearNote")}</span>
        </>
      )}
    </div>
  );
}
