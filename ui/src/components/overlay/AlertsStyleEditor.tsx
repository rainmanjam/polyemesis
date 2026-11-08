import { useState } from "react";
import { Volume2 } from "lucide-react";
import { AlertView } from "@/components/overlay/AlertView";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { playAlertSound } from "@/lib/alertSound";
import { useT, type TranslationKey } from "@/lib/i18n";
import { ALERT_SECONDS, OVERLAY_ALERT_KINDS, type OverlayStyle } from "@/lib/overlay";
import type { ActivityDisplay, AlertLayout, AlertSound, AlertStyle } from "@/lib/types";

const LAYOUTS: Record<AlertLayout, TranslationKey> = {
  banner: "overlay.alertsLayoutBanner",
  card: "overlay.alertsLayoutCard",
  minimal: "overlay.alertsLayoutMinimal",
};

const SOUNDS: Record<AlertSound, TranslationKey> = {
  none: "overlay.alertsSoundNone",
  chime: "overlay.alertsSoundChime",
  pop: "overlay.alertsSoundPop",
  bell: "overlay.alertsSoundBell",
};

/** The alerts overlay's style: layout, which kinds, seconds, message and amount,
 *  sound and accent. `value` is the resolved style (defaults filled in); the
 *  parent writes `alerts` into the overlay style only when `onChange` fires, so
 *  an overlay nobody touches here is saved exactly as it was.
 *
 *  The preview is the real AlertView on a sample tip, so what the editor shows
 *  is what the overlay draws. */
export function AlertsStyleEditor({
  value,
  style,
  onChange,
}: Readonly<{
  value: AlertStyle;
  /** The overlay's chat style, whose font and colours the alert shares. */
  style: OverlayStyle;
  onChange: (next: AlertStyle) => void;
}>) {
  const t = useT();
  // Mounted at a fixed time so the sample's id (and so its React key) is stable.
  const [sample] = useState<ActivityDisplay>(() => ({
    id: "preview",
    platform: "twitch",
    kind: "tip",
    name: "pixelpanda",
    amount: { currency: "USD", minor: 500, exponent: 2, display: "$5.00" },
    message: undefined,
    at: new Date().toISOString(),
  }));
  const set = <K extends keyof AlertStyle>(k: K, v: AlertStyle[K]) => onChange({ ...value, [k]: v });

  const seconds = (raw: string) => {
    const n = Math.round(Number(raw));
    if (Number.isFinite(n)) set("seconds", Math.max(ALERT_SECONDS.min, Math.min(ALERT_SECONDS.max, n)));
  };
  const toggleKind = (k: string, on: boolean) =>
    set(
      "kinds",
      OVERLAY_ALERT_KINDS.filter((x) => (x === k ? on : value.kinds.includes(x))),
    );

  return (
    <fieldset className="flex flex-col gap-3 rounded border border-border p-2" data-testid="alerts-style">
      <legend className="px-1 text-tiny font-medium">{t("overlay.alertsStyle")}</legend>

      <div className="grid grid-cols-2 gap-2">
        <div className="flex flex-col gap-1">
          <Label htmlFor="overlay-alerts-layout">{t("overlay.alertsLayout")}</Label>
          <Select value={value.layout} onValueChange={(v) => set("layout", v as AlertLayout)}>
            <SelectTrigger id="overlay-alerts-layout">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {(Object.keys(LAYOUTS) as AlertLayout[]).map((l) => (
                <SelectItem key={l} value={l}>
                  {t(LAYOUTS[l])}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="overlay-alerts-seconds">{t("overlay.alertsSeconds")}</Label>
          <Input
            id="overlay-alerts-seconds"
            type="number"
            min={ALERT_SECONDS.min}
            max={ALERT_SECONDS.max}
            value={value.seconds}
            onChange={(e) => seconds(e.target.value)}
          />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="overlay-alerts-sound">{t("overlay.alertsSound")}</Label>
          <div className="flex gap-1">
            <Select value={value.sound} onValueChange={(v) => set("sound", v as AlertSound)}>
              <SelectTrigger id="overlay-alerts-sound">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {(Object.keys(SOUNDS) as AlertSound[]).map((s) => (
                  <SelectItem key={s} value={s}>
                    {t(SOUNDS[s])}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Button
              size="icon-sm"
              variant="outline"
              aria-label={t("overlay.alertsPlaySound")}
              title={t("overlay.alertsPlaySound")}
              disabled={value.sound === "none"}
              onClick={() => void playAlertSound(value.sound)}
            >
              <Volume2 />
            </Button>
          </div>
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="overlay-alerts-accent">{t("overlay.alertsAccent")}</Label>
          <Input
            id="overlay-alerts-accent"
            type="color"
            className="h-8 p-1"
            value={value.accent}
            onChange={(e) => set("accent", e.target.value.toLowerCase())}
          />
        </div>
      </div>

      <div className="flex flex-col gap-1.5">
        <label className="flex items-center gap-2 text-sm">
          <Switch
            checked={value.showMessage}
            onCheckedChange={(on) => set("showMessage", on)}
            aria-label={t("overlay.alertsShowMessage")}
          />
          {t("overlay.alertsShowMessage")}
        </label>
        <label className="flex items-center gap-2 text-sm">
          <Switch
            checked={value.showAmount}
            onCheckedChange={(on) => set("showAmount", on)}
            aria-label={t("overlay.alertsShowAmount")}
          />
          {t("overlay.alertsShowAmount")}
        </label>
      </div>

      <div role="group" aria-label={t("overlay.alertsKinds")} className="flex flex-col gap-1">
        <span className="text-tiny font-medium">{t("overlay.alertsKinds")}</span>
        <div className="grid grid-cols-2 gap-1">
          {OVERLAY_ALERT_KINDS.map((k) => (
            <label key={k} className="flex items-center gap-1.5 text-sm">
              <Checkbox checked={value.kinds.includes(k)} onCheckedChange={(on) => toggleKind(k, Boolean(on))} />
              {t(`activity.kind.${k}`)}
            </label>
          ))}
        </div>
      </div>

      <div className="flex flex-col gap-1">
        <span className="text-tiny font-medium">{t("overlay.alertsPreview")}</span>
        <div className="overflow-hidden rounded border border-border bg-black/70">
          <AlertView
            display={{ ...sample, message: value.showMessage ? t("overlay.alertsPreviewMessage") : undefined }}
            alert={value}
            style={style}
            preview
          />
        </div>
      </div>
      <span className="text-sm text-muted-foreground">{t("overlay.alertsDowngradeHint")}</span>
    </fieldset>
  );
}
