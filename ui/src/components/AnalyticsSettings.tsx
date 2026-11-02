import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { useT } from "@/lib/i18n";
import { LIMITS } from "@/lib/limits";
import type { AnalyticsSettings as Value } from "@/lib/types";

const DEFAULTS: Value = { enabled: true, intervalSeconds: 60, retentionDays: 90 };

/** The audience sampler (Wave 4 #4): whether it runs, how often it asks the
 *  connected accounts while a destination is running, and how long a finished
 *  stream's report is kept. */
export function AnalyticsSettings({
  value,
  onChange,
}: {
  value: Value | undefined;
  onChange: (next: Value) => void;
}) {
  const t = useT();
  const v: Value = { ...DEFAULTS, ...value };
  return (
    <Card>
      <CardHeader>
        <CardTitle id="analytics-settings">{t("set.analytics")}</CardTitle>
        <CardDescription>{t("set.analyticsDesc")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="flex items-center justify-between">
          <Label htmlFor="analytics-enabled">{t("set.enabled")}</Label>
          <Switch
            id="analytics-enabled"
            checked={v.enabled}
            onCheckedChange={(enabled) => onChange({ ...v, enabled })}
          />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="analytics-interval">{t("set.analyticsInterval")}</Label>
          <Input
            id="analytics-interval"
            type="number"
            min={LIMITS.analyticsIntervalSeconds.min}
            max={LIMITS.analyticsIntervalSeconds.max}
            value={v.intervalSeconds}
            onChange={(e) => onChange({ ...v, intervalSeconds: Number(e.target.value) })}
            className="w-28"
          />
          <span className="text-sm text-muted-foreground">{t("set.analyticsIntervalNote")}</span>
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="analytics-retention">{t("set.analyticsRetention")}</Label>
          <Input
            id="analytics-retention"
            type="number"
            min={LIMITS.analyticsRetentionDays.min}
            max={LIMITS.analyticsRetentionDays.max}
            value={v.retentionDays}
            onChange={(e) => onChange({ ...v, retentionDays: Number(e.target.value) })}
            className="w-28"
          />
        </div>
      </CardContent>
    </Card>
  );
}
