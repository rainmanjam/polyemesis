import { useEffect, useState } from "react";
import { toast } from "sonner";
import { Ban, ExternalLink, Timer, Trash2, UserSearch } from "lucide-react";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { api } from "@/lib/api";
import { capabilityFor, supportOf } from "@/lib/capabilities";
import { TIMEOUTS } from "@/lib/chat";
import { openPlatformLink, platformLinkFor, platformNoun } from "@/lib/platformLinks";
import type { ChatMessage } from "@/lib/types";

/* ===========================================================================
   Right-click (or double-click) on a message.

   This adds no capability. Everything here can already be done from the user
   card, and the card remains the place for anything that needs to be read
   before it is decided — history, ban state, the retention caveat. What this
   buys is the two-second path for the case that does not need reading: a line
   scrolls past, it is obviously bad, and the moderator already knows what to do
   about it.

   So the menu is deliberately the SHORT list. Timeouts, a ban, delete the
   message, open the card, open the platform. Anything longer would turn a
   shortcut back into a form.

   Actions that the platform cannot perform are shown DISABLED with the reason,
   never hidden — the same rule the card follows, and for the same reason: a
   missing button reads as a broken tool, not as an unsupported platform.
   =========================================================================== */

export interface MenuAnchor {
  x: number;
  y: number;
}

export function ChatMessageMenu({
  message,
  anchor,
  onClose,
  onOpenCard,
  onDelete,
}: {
  message: ChatMessage;
  /** Viewport coordinates of the click that opened this. */
  anchor: MenuAnchor;
  onClose: () => void;
  onOpenCard: (m: ChatMessage) => void;
  onDelete?: (m: ChatMessage) => void;
}) {
  // Radix positions against a trigger element, and the trigger here is a point
  // rather than a control. A zero-size fixed div at the cursor is the standard
  // shape for that: it anchors correctly, collision-flips near screen edges for
  // free, and there is nothing to see or tab to.
  const [open, setOpen] = useState(true);

  useEffect(() => {
    if (!open) onClose();
  }, [open, onClose]);

  const authorId = message.author.id?.trim() ?? "";
  const caps = capabilityFor(message.platform);
  const canModerate = supportOf(caps, "moderation") === "yes";
  const link = platformLinkFor(message);

  // The two reasons an action is unavailable are different and the operator
  // needs to tell them apart: the platform has no such API, versus this
  // particular message arrived without a user id to address.
  const modReason = !canModerate
    ? `${platformNoun(message.platform)} publishes no moderation API that polyemesis can call`
    : !authorId
      ? `${platformNoun(message.platform)} sent no user id with this message, so there is nobody to address`
      : "";

  const run = async (what: string, fn: () => Promise<{ detail?: string } | void>) => {
    try {
      const res = await fn();
      // The server's sentence wins. It knows the difference between "timed out
      // on Twitch" and "Kick rounded this to the nearest minute".
      const detail = res && "detail" in res ? res.detail : "";
      toast.success(detail || `${what} — ${message.author.name}`);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : `Could not ${what.toLowerCase()}.`);
    }
  };

  const close = () => setOpen(false);

  return (
    <DropdownMenu open={open} onOpenChange={setOpen}>
      {/* Radix measures the TRIGGER to place the content, so the cursor point
          has to be the trigger itself — a marker rendered as a sibling is not
          measured and the menu lands wherever the layout happens to put it.
          A zero-size fixed element gives Radix a real rect at the cursor and
          gets edge collision-flipping for free. */}
      <DropdownMenuTrigger asChild>
        <span
          aria-hidden
          style={{
            position: "fixed",
            left: anchor.x,
            top: anchor.y,
            width: 0,
            height: 0,
          }}
        />
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align="start"
        sideOffset={2}
        className="w-60"
        onCloseAutoFocus={(e) => e.preventDefault()}
      >
        <DropdownMenuLabel className="truncate">
          {message.author.name}
          <span className="ml-1 font-normal text-subtle-foreground">
            on {platformNoun(message.platform)}
          </span>
        </DropdownMenuLabel>
        <DropdownMenuSeparator />

        {/* THE REASON, WHERE IT CAN BE READ.
            `modReason` was computed correctly and rendered only into `title`
            on items that carry `data-[disabled]:pointer-events-none`
            (ui/dropdown-menu.tsx:36) — an element that receives no pointer
            events never fires the hover a native tooltip needs, so the
            sentence could not be reached by any means. Which contradicted this
            file's own header: "shown DISABLED with the reason, never hidden —
            a missing button reads as a broken tool, not as an unsupported
            platform." Greyed items with no reason read as a broken tool too.

            A label under the separator rather than a note per item: the reason
            is the same for every greyed item in the menu, and repeating it
            five times is how a short menu turns back into a form. */}
        {modReason !== "" && (
          <>
            <DropdownMenuLabel className="whitespace-normal text-[10px] font-normal leading-snug text-warn">
              {modReason}
            </DropdownMenuLabel>
            <DropdownMenuSeparator />
          </>
        )}

        <DropdownMenuItem
          onSelect={() => {
            close();
            onOpenCard(message);
          }}
          disabled={!authorId}
          title={authorId ? "Everything they have said, and every action" : modReason}
        >
          <UserSearch className="mr-2 h-3.5 w-3.5" />
          View history &amp; all actions
        </DropdownMenuItem>

        {link && (
          <DropdownMenuItem
            onSelect={() => {
              close();
              openPlatformLink(link);
            }}
            title={link.caveat}
          >
            <ExternalLink className="mr-2 h-3.5 w-3.5" />
            {link.label}
          </DropdownMenuItem>
        )}

        <DropdownMenuSeparator />

        {TIMEOUTS.map((t) => (
          <DropdownMenuItem
            key={t.seconds}
            disabled={!canModerate || !authorId}
            title={modReason}
            onSelect={() => {
              close();
              void run(`Timed out for ${t.label}`, () =>
                api.banChatUser({
                  platform: message.platform,
                  account: message.account,
                  userId: authorId,
                  seconds: t.seconds,
                }),
              );
            }}
          >
            <Timer className="mr-2 h-3.5 w-3.5" />
            Time out {t.label}
          </DropdownMenuItem>
        ))}

        <DropdownMenuSeparator />

        {/* A permanent ban is the one irreversible action reachable from a
            right-click, so it does not fire from the menu. It opens the card,
            which asks again — the confirmation lives in one place rather than
            being re-implemented here with different wording. */}
        <DropdownMenuItem
          disabled={!canModerate || !authorId}
          title={
            modReason ||
            "Opens the user card, which confirms before banning: a permanent ban is not a menu click"
          }
          onSelect={() => {
            close();
            onOpenCard(message);
          }}
        >
          <Ban className="mr-2 h-3.5 w-3.5 text-down" />
          Ban permanently…
        </DropdownMenuItem>

        {onDelete && (
          <DropdownMenuItem
            title="Delete this one message on the platform it came from"
            onSelect={() => {
              close();
              void onDelete(message);
            }}
          >
            <Trash2 className="mr-2 h-3.5 w-3.5 text-down" />
            Delete message
          </DropdownMenuItem>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
