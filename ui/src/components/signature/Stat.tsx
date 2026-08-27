import { useT, type TranslationKey, type TranslateParams } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/** A label key that the catalogue also has a `.hint` for.
 *
 *  THE DEVICE, and it is a compile error rather than a test. `TranslationKey`
 *  is `keyof typeof en`, so this mapped type can ask, key by key, whether
 *  `<key>.hint` is also in the catalogue -- and keep only the ones where it
 *  is. A figure added with a label but no explanation does not render an empty
 *  tooltip or fail a suite someone can skip; it does not typecheck.
 *
 *  That matters because the failure this replaces is silent by construction.
 *  Sixty-eight figures across twelve screens had no explanation anywhere, and
 *  nothing about a bare `<Stat label={t("rend.speed")} value="2.17x" />` looks
 *  incomplete. An optional `hint` prop would have read exactly as well while
 *  covering none of them, and the sixty-ninth would have been added the same
 *  way. Shingo's rung one: make the mistake impossible rather than remembered.
 */
export type StatLabelKey = {
  [K in TranslationKey]: `${K}.hint` extends TranslationKey ? K : never;
}[TranslationKey];

/** A labelled figure. Dense by construction: label above, value below, both
 *  tight, value tabular so a row of these does not jitter as numbers update.
 *
 *  IT TAKES THE KEY, NOT THE TRANSLATED STRING, and that is the whole reason
 *  the hint can be mandatory. Given `t("rend.speed")` this component receives
 *  the word "Speed" and has no way back to the catalogue; given the key it can
 *  look up both halves, so the explanation cannot be omitted or drift away
 *  from the label it explains. The two live on adjacent lines of en.json.
 */
export function Stat({
  labelKey,
  labelParams,
  value,
  unit,
  tone,
  className,
}: {
  labelKey: StatLabelKey;
  /** Interpolation for labels that carry a count, e.g. "Stems ({count} files)". */
  labelParams?: TranslateParams;
  value: string | number;
  unit?: string;
  tone?: "default" | "live" | "warn" | "down" | "muted";
  className?: string;
}) {
  const t = useT();
  // Safe by the type above: StatLabelKey only admits keys whose `.hint` is in
  // the catalogue. The assertion is what tells TypeScript that the template
  // literal it just built is one of those keys, which it cannot infer through
  // a generic-free template expression.
  const hint = t(`${labelKey}.hint` as TranslationKey);
  return (
    <div
      className={cn("flex flex-col gap-0.5", className)}
      // ON THE WHOLE FIGURE, not on the label alone. The label is nine pixels
      // tall and the value beneath it is the part an operator's eye goes to,
      // so a tooltip that only answered over the caption would be one most
      // people never found.
      title={hint}
    >
      <div className="text-[9px] uppercase tracking-wider text-muted-foreground">
        {t(labelKey, labelParams)}
      </div>
      <div
        className={cn(
          "tnum font-mono text-[13px] leading-none",
          tone === "live" && "text-live",
          tone === "warn" && "text-warn",
          tone === "down" && "text-down",
          tone === "muted" && "text-muted-foreground",
        )}
      >
        {value}
        {unit && <span className="ml-0.5 text-[10px] text-muted-foreground">{unit}</span>}
      </div>
    </div>
  );
}
