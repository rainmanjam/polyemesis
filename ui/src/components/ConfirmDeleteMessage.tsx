import { ConfirmDestructive } from "@/components/ConfirmDestructive";
import { accentFor } from "@/lib/chat";
import type { ChatMessage } from "@/lib/types";
import { useT } from "@/lib/i18n";

/* ===========================================================================
   ONE delete-a-chat-message confirmation, for both places that ask.

   There were two, and they had drifted. ChatPage's dialog quoted the message
   being deleted in a scrollable block and named the platform by its display
   label; the dashboard's ChatPanel dialog quoted NOTHING and passed the raw
   platform slug into the same sentence.

   The missing quote is the whole safety property. A chat moderator is looking
   at a name that has said forty things in the last minute, and "Delete message
   from bob?" identifies none of them. The dialog that cannot say WHICH message
   is a dialog that cannot catch a mis-click — which is the only reason it is
   in front of the operator at all.

   Copies drift; that is the finding, not the two-line difference. So this is a
   component rather than a snippet to keep in sync, and the drift is now
   unspellable rather than merely fixed.
   =========================================================================== */

export function ConfirmDeleteMessage({
  target,
  onOpenChange,
  onConfirm,
}: Readonly<{
  /** The message pending deletion, or null when nothing is pending. */
  target: ChatMessage | null;
  onOpenChange: (open: boolean) => void;
  onConfirm: (m: ChatMessage) => void | Promise<void>;
}>) {
  const t = useT();
  return (
    <ConfirmDestructive
      open={target !== null}
      onOpenChange={onOpenChange}
      subject={target?.author.name ?? ""}
      title={t("chatpage.deleteTitle")}
      description={
        <>
          {t("chatpage.deleteBody", {
            // The display label, never the raw slug: the operator reads
            // "Twitch", and the sentence is the same one the menu prints.
            platform: target ? accentFor(target.platform).label : "",
          })}
          {/* WHICH message. Capped and scrollable so a pasted wall of text
              cannot push the confirm button off the bottom of the dialog. */}
          <span className="mt-2 block max-h-24 overflow-y-auto rounded border border-border bg-card-raised px-2 py-1 text-[11px] leading-snug text-foreground">
            {target?.text}
          </span>
        </>
      }
      confirmLabel={t("chatpage.deleteConfirm")}
      onConfirm={async () => {
        if (target) await onConfirm(target);
      }}
    />
  );
}
