import { Check, CircleAlert, Loader2 } from "lucide-react";
import { useT, type TranslationKey } from "@/lib/i18n";
import type { SaveState } from "@/lib/saveModel";
import { cn } from "@/lib/utils";

/* THE SAVE STATUS LINE beside an explicit Save button (A95, A20).
 *
 * Shows nothing while clean, so a page with nothing to save stays quiet. It
 * announces changes politely (role="status") so a screen reader hears "Saved"
 * without the operator losing their place. A failure is red and says to retry,
 * and it keeps the draft (see useSaveModel), so the words are a promise the
 * page keeps. */

const LABEL: Record<Exclude<SaveState, "clean">, TranslationKey> = {
  dirty: "save.dirty",
  saving: "save.saving",
  saved: "save.saved",
  failed: "save.failed",
};

export function SaveStatus({ state, className }: Readonly<{ state: SaveState; className?: string }>) {
  const t = useT();
  // The region is always mounted, with empty text while clean. A live region
  // that appears already holding its text is often not announced, which is the
  // exact case of the first "Unsaved changes".
  const text = state === "clean" ? "" : t(LABEL[state]);
  const failed = state === "failed";
  const Icon = state === "saving" ? Loader2 : state === "saved" ? Check : failed ? CircleAlert : null;
  return (
    <span
      role="status"
      aria-live="polite"
      data-save-state={state}
      className={cn(
        "inline-flex items-center gap-1.5 text-sm",
        failed ? "text-down" : state === "saved" ? "text-live" : "text-muted-foreground",
        className,
      )}
    >
      {Icon && <Icon aria-hidden="true" className={cn("h-3.5 w-3.5 shrink-0", state === "saving" && "animate-spin")} />}
      {text}
    </span>
  );
}
