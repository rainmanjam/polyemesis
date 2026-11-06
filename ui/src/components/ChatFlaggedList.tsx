import { Flag } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/EmptyState";
import { useT } from "@/lib/i18n";
import { accentFor } from "@/lib/chat";
import { ACTION_KEYS, CATEGORY_KEYS, CHECKER_KEYS } from "@/lib/automodConfig";
import { clockTime } from "@/lib/format";
import type { ChatFlaggedMessage, ChatFlagFinding, ChatMessage } from "@/lib/types";

/* AUTOMOD'S REVIEW LIST, ON CHAT (UX Council A48).
 *
 * Automod's findings used to be written to the server log and nowhere else, so
 * a moderator could not see what it had flagged without a shell. This is that
 * list, one row per message, newest first, narrowed by the same platform
 * filter as the timeline.
 *
 * Presentation only: every word here comes from the catalogue through the
 * automod label maps, and nothing is sent back spelled with these words. */

/** One finding in the operator's words: category (when the model gave one),
 *  which checker, and what it asked for. */
function findingLabel(f: ChatFlagFinding, t: ReturnType<typeof useT>): string {
  const parts: string[] = [];
  const cat = f.category ? CATEGORY_KEYS[f.category] : undefined;
  if (cat) parts.push(t(cat));
  const checker = CHECKER_KEYS[f.checker];
  if (checker) parts.push(t(checker));
  const action = ACTION_KEYS[f.action];
  if (action) parts.push(t(action));
  return parts.join(" · ");
}

export function ChatFlaggedList({
  flags,
  failed,
  filtered,
  onOpenUser,
}: Readonly<{
  /** Already narrowed to the platforms the timeline is showing. */
  flags: ChatFlaggedMessage[];
  /** The read did not answer; say so rather than "nothing flagged". */
  failed: boolean;
  /** Some flags exist but the platform filter hides all of them. */
  filtered: boolean;
  onOpenUser: (m: ChatMessage) => void;
}>) {
  const t = useT();

  if (failed) {
    return (
      <div className="flex flex-1 items-center justify-center p-3">
        <EmptyState icon={Flag} title={t("chatFlags.loadFailedTitle")} body={t("chatFlags.loadFailed")} />
      </div>
    );
  }
  if (flags.length === 0) {
    return (
      <div className="flex flex-1 flex-col items-center justify-center gap-2 p-3">
        <EmptyState
          icon={Flag}
          title={t("chatFlags.emptyTitle")}
          body={filtered ? t("chatFlags.emptyFiltered") : t("chatFlags.emptyBody")}
        />
        <p className="max-w-md text-center text-sm text-muted-foreground">{t("chatFlags.note")}</p>
      </div>
    );
  }

  return (
    <div className="min-h-0 flex-1 overflow-y-auto">
      <ul aria-label={t("chatFlags.title")} className="divide-y divide-border">
        {flags.map((f) => {
          const m = f.message;
          const accent = accentFor(m.platform);
          const name = m.author.name || m.author.id || t("chatFlags.unknownAuthor");
          return (
            <li key={`${m.platform}-${m.account ?? ""}-${m.id}`} className="flex flex-col gap-1 px-2 py-1.5">
              <div className="flex flex-wrap items-center gap-1.5 text-sm">
                <span className="tnum font-mono text-muted-foreground">{clockTime(f.at)}</span>
                <Badge variant="outline" className={accent.text}>
                  {accent.label}
                </Badge>
                {m.author.id ? (
                  <button
                    type="button"
                    className="font-semibold text-primary-bright underline-offset-2 hover:underline"
                    aria-label={t("chatFlags.openUser", { name })}
                    onClick={() => onOpenUser(m)}
                  >
                    {name}
                  </button>
                ) : (
                  <span className="font-semibold">{name}</span>
                )}
                <Badge variant={f.acted ? "warn" : "outline"}>
                  {f.acted ? t("chatFlags.acted") : t("chatFlags.loggedOnly")}
                </Badge>
              </div>
              <p className="break-words text-[13px] leading-snug">
                {m.text || <span className="text-muted-foreground">{t("chatFlags.messageGone")}</span>}
              </p>
              <ul className="flex flex-col gap-0.5">
                {f.findings.map((fi, i) => (
                  <li key={i} className="text-sm text-muted-foreground">
                    <span className="text-foreground">{findingLabel(fi, t)}</span>
                    {/* The category already says why, in the catalogue's words;
                        the server's reason is the same thing in English. Only a
                        rule or history finding, which has no category, adds
                        its reason -- the operator's own rule name. */}
                    {fi.reason && !fi.category ? ` — ${fi.reason}` : ""}
                  </li>
                ))}
              </ul>
            </li>
          );
        })}
      </ul>
      <p className="px-2 py-1.5 text-sm text-muted-foreground">{t("chatFlags.note")}</p>
    </div>
  );
}
