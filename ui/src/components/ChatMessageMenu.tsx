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
import { authorReason, chatActionSupport, personActionSupport } from "@/lib/chatModeration";
import { TIMEOUTS } from "@/lib/chat";
import { openPlatformLink, platformLinkFor, platformNoun } from "@/lib/platformLinks";
import type { Asks } from "@/hooks/useConfirm";
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

   PER ACTION, and it was not. Every item here was gated on one value —
   `supportOf(caps, "moderation") === "yes"` — which is a column in the setup
   page's capability matrix and a summary of the whole platform. Facebook
   answers YES to that column, correctly, because it can delete and hide a
   comment; its adapter implements no chat.Banner at all, so Hub.Ban refuses
   before it reaches the network (internal/chat/hub.go:803). Four "Time out"
   items and "Ban permanently" therefore rendered live and enabled on Facebook
   and every press produced an error toast. Delete had the opposite defect and
   no gate whatsoever — `{onDelete && …}` — so Rumble, which implements
   nothing, offered a working-looking Delete three lines under this menu's own
   sentence saying Rumble publishes no moderation API.

   An enabled control that always fails is worse than a missing one: mid-raid
   the moderator presses it, reads a toast, presses it again, and the line is
   still on the overlay. The gate is now lib/chatModeration.ts, which answers
   about ONE ACTION and hands back the sentence to put on screen.
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
  onDelete?: Asks<ChatMessage>;
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
  const link = platformLinkFor(message);

  // The two reasons an action is unavailable are different and the operator
  // needs to tell them apart: the platform has no such API, versus this
  // particular message arrived without a user id to address.
  const noAuthor = authorReason(message);

  // Timeouts and a permanent ban are ONE question. Both route through Hub.Ban,
  // which type-asserts chat.Banner, so gating them separately would only invite
  // the two gates to drift apart.
  const person = personActionSupport(message, "ban");

  // Deleting a message is a DIFFERENT question, and deliberately not folded in
  // with the two above: it addresses a message rather than a person, so it does
  // not need an author id, and Facebook can do it while it cannot ban. Folding
  // them was the whole defect.
  const del = chatActionSupport(message.platform, "delete");

  // Which sentences to print, where, and each of them exactly once. Repeating
  // one reason five times is how a short menu turns back into a form, which is
  // the thing this file's header exists to prevent.
  //
  //   blanket      one sentence that governs every action, so it goes at the
  //                top: Rumble and every unlisted platform can do nothing at
  //                all, and the ban reason and the delete reason are then the
  //                same string.
  //   personReason governs the timeouts and the ban only. Facebook reaches this
  //                one with Delete still live, which is exactly why a menu-wide
  //                banner cannot be the answer any more.
  //   deleteReason governs Delete only. Nothing produces it today — every
  //                platform that can ban can also delete — and it is here so
  //                that a future adapter with the reverse shape is explained
  //                rather than silently greyed.
  const blanket = person.reason !== "" && person.reason === del.reason ? person.reason : "";
  const personReason = blanket === "" && noAuthor === "" ? person.reason : "";
  const deleteReason = blanket === "" ? del.reason : "";

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
            It was computed correctly and rendered only into `title` on items
            that carry `data-[disabled]:pointer-events-none`
            (ui/dropdown-menu.tsx:36) — an element that receives no pointer
            events never fires the hover a native tooltip needs, so the
            sentence could not be reached by any means. Which contradicted this
            file's own header: "shown DISABLED with the reason, never hidden —
            a missing button reads as a broken tool, not as an unsupported
            platform." Greyed items with no reason read as a broken tool too.

            This slot is for a sentence that governs the WHOLE menu: no author
            id, so there is nobody to open a card on, time out or ban; or a
            platform with no moderation API at all, where every item below is
            inert for one reason. Anything narrower sits beside the group it
            actually governs instead — since Facebook can delete and cannot
            ban, one banner over the whole menu would now be a lie. */}
        {(noAuthor || blanket) !== "" && (
          <>
            <DropdownMenuLabel className="whitespace-normal text-[10px] font-normal leading-snug text-warn">
              {noAuthor || blanket}
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
          title={authorId ? "Everything they have said, and every action" : noAuthor}
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

        {/* The reason for the five person-actions below, beside them rather
            than at the top of the menu. Facebook reaches this line with Delete
            still live underneath, so a single banner over everything would say
            something untrue about the one control that works. */}
        {personReason !== "" && (
          <DropdownMenuLabel className="whitespace-normal text-[10px] font-normal leading-snug text-warn">
            {personReason}
          </DropdownMenuLabel>
        )}

        {TIMEOUTS.map((t) => (
          <DropdownMenuItem
            key={t.seconds}
            disabled={!person.ok}
            title={person.reason}
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
          disabled={!person.ok}
          title={
            person.reason ||
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

        {/* STILL RENDERED when the platform cannot delete, and inert with the
            reason — the rule every other item here follows, and the one this
            one was skipping. `{onDelete && …}` alone meant Rumble, whose
            adapter implements nothing, offered a Delete that hub.go refuses;
            the operator pressed it, got a toast, and the message stayed up.

            Note what it is NOT gated on: an author id. Delete addresses a
            MESSAGE, so a comment whose author id never arrived is still
            deletable — which is why this asks chatModeration a separate
            question from the five above rather than reusing their answer. */}
        {onDelete && (
          <DropdownMenuItem
            disabled={!del.ok}
            title={del.reason || "Delete this one message on the platform it came from"}
            onSelect={() => {
              close();
              void onDelete(message);
            }}
          >
            <Trash2 className="mr-2 h-3.5 w-3.5 text-down" />
            Delete message
          </DropdownMenuItem>
        )}

        {/* Only when Delete is the ONLY thing this sentence governs. A platform
            that can do nothing at all has already said so at the top of the
            menu, and saying it twice in a five-item menu is noise. */}
        {onDelete && deleteReason !== "" && (
          <DropdownMenuLabel className="whitespace-normal text-[10px] font-normal leading-snug text-warn">
            {deleteReason}
          </DropdownMenuLabel>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
