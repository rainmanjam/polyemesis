import { Info } from "lucide-react";

import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { useT } from "@/lib/i18n";
import type { TranslationKey } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/* The "what does this actually do" affordance, next to a setting's label.
 *
 * Click rather than hover, for three reasons that all point the same way:
 * a hover tooltip is unreachable on a touch device, it is awkward to read with
 * a screen reader, and it vanishes the moment the pointer moves — which is
 * exactly when someone is trying to read a two-sentence explanation of what
 * "keyframe interval" changes.
 *
 * The body is a catalogue key, never a literal. That is what makes this
 * translatable at all: an operator running the console in Japanese gets the
 * explanation in Japanese, and a locale that has not translated a given hint
 * yet falls back to English per-key rather than showing nothing.
 *
 * Deliberately NOT a replacement for a clear label. A setting that needs a
 * paragraph to be comprehensible usually needs a better name; this is for the
 * genuine domain knowledge underneath — what a bitrate ceiling does to a
 * viewer on a poor connection, why a keyframe interval has to match what the
 * platform expects. */
export function InfoHint({
  /** Catalogue key for the explanation. */
  body,
  /** Optional heading, when the hint needs to name what it is describing. */
  title,
  /** Accessible name for the button. Defaults to a generic "More information". */
  label,
  className,
}: {
  body: TranslationKey;
  title?: TranslationKey;
  label?: string;
  className?: string;
}) {
  const t = useT();

  // Names the setting when it can. A page carries a dozen of these, and a
  // screen reader announcing "More information" twelve times gives its user no
  // way to tell which control they are on — the label has to say what it
  // explains, not merely that it explains something.
  const accessibleName =
    label ?? (title ? `${t(title)} — ${t("common.moreInfo")}` : t("common.moreInfo"));

  return (
    <Popover>
      <PopoverTrigger asChild>
        <button
          type="button"
          // type="button" matters: these sit inside <form> elements all over
          // the settings page, and a bare <button> there submits the form.
          aria-label={accessibleName}
          className={cn(
            "inline-flex h-3.5 w-3.5 shrink-0 items-center justify-center rounded-full align-text-bottom",
            "text-subtle-foreground transition-colors hover:text-foreground",
            "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
            className,
          )}
        >
          <Info className="h-3.5 w-3.5" aria-hidden />
        </button>
      </PopoverTrigger>
      <PopoverContent>
        {title && (
          <p className="mb-1 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
            {t(title)}
          </p>
        )}
        <p className="text-[12px] leading-relaxed text-foreground">{t(body)}</p>
      </PopoverContent>
    </Popover>
  );
}

/** A label with its hint attached, for the common case.
 *
 *  Exists so the pairing is one element rather than a flex wrapper repeated at
 *  every field — several hundred times across the settings pages, which is
 *  several hundred chances to get the spacing subtly different. */
export function LabelWithHint({
  label,
  body,
  title,
  htmlFor,
  className,
}: {
  label: TranslationKey;
  body: TranslationKey;
  title?: TranslationKey;
  htmlFor?: string;
  className?: string;
}) {
  const t = useT();
  return (
    <span className={cn("inline-flex items-center gap-1", className)}>
      <label htmlFor={htmlFor} className="text-[12px] font-medium">
        {t(label)}
      </label>
      <InfoHint body={body} title={title} label={`${t(label)} — ${t("common.moreInfo")}`} />
    </span>
  );
}
