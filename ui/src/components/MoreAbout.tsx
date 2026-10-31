import type { ReactNode } from "react";
import { ChevronDown } from "lucide-react";

import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/* ONE LINE BY DEFAULT, THE REST ONE CLICK AWAY (settings-council R11,
 * app-council A84).
 *
 * The console had 154 English strings over 200 characters and 25 over 400,
 * and several sat inline on routine screens: the mux-queue note, the
 * multitrack and give-up notes in the destination dialog, the slate, silence
 * and GPU notes in Settings, the expert-arguments note on Monitoring. Each was
 * correct and none was short enough to read mid-task.
 *
 * The long explanation is unchanged; it is only no longer the first thing an
 * operator has to read. `lead` is one plain sentence that is always shown; the
 * children are the full text, behind a native <details>, so the disclosure
 * works with a keyboard and a screen reader and needs no state. Without a lead
 * this is the Settings pattern it was lifted from: a card whose title already
 * says what it is shows only the disclosure. */
export function MoreAbout({
  lead,
  children,
  className,
}: Readonly<{ lead?: ReactNode; children: ReactNode; className?: string }>) {
  const t = useT();
  const disclosure = (
    <details className={cn("group text-sm text-muted-foreground", !lead && className)}>
      <summary className="flex w-fit cursor-pointer list-none items-center gap-1 text-sm underline-offset-2 hover:text-foreground hover:underline">
        <ChevronDown aria-hidden className="h-3.5 w-3.5 transition-transform group-open:rotate-180" />
        {t("settingsUx.more")}
      </summary>
      <div className="mt-1">{children}</div>
    </details>
  );
  if (!lead) return disclosure;
  return (
    <div className={cn("flex flex-col gap-1 text-sm text-muted-foreground", className)}>
      <span>{lead}</span>
      {disclosure}
    </div>
  );
}
