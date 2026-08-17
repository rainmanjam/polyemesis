import { useEffect, useState } from "react";
import { AlertTriangle } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

/* ===========================================================================
   One confirmation for every destructive action.

   It replaces window.confirm, which was a weak control in three ways: it is one
   keystroke from OK, browsers let a user suppress it outright ("prevent this
   page from creating additional dialogs"), and it looks identical whether you
   are removing a queued job or erasing a recording that cannot come back.

   The friction here is PROPORTIONAL to the consequence, which is the whole
   idea. A recoverable action gets a button. An irreversible one requires
   typing the name of the thing being destroyed — the input only accepts the
   intended target, so a mis-click on the wrong row cannot complete. That is the
   contact method: make the wrong action not fit.

   Inconsistency was itself the defect being fixed. An operator who learns
   "deletes ask first" and then meets one that does not has had their caution
   trained out of them exactly where it mattered.
   =========================================================================== */

export interface ConfirmDestructiveProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** What is being destroyed, e.g. "rec-20260727.mkv". */
  subject: string;
  title: string;
  /** What will happen, in the operator's terms. */
  description: React.ReactNode;
  /** Requires the subject to be typed before the action unlocks.
   *
   *  Reserve this for things that do not come back: a deleted file, a revoked
   *  credential, a cascade. Using it on recoverable actions is how a control
   *  becomes a reflex, and a reflex is not a control. */
  requireTyping?: boolean;
  /** The blast radius, when the action reaches beyond its subject. Shown as
   *  counts rather than prose: confirming a number is a decision, confirming a
   *  vibe is a click. */
  consequences?: { label: string; count: number }[];
  /** The heading over that panel.
   *
   *  It was the hardcoded string "This also removes", which is exactly right
   *  for the five deletions that were the only callers and becomes a lie the
   *  moment a destructive action is not a deletion. Ending a Facebook
   *  broadcast REMOVES NOTHING — Facebook saves the live video as a VOD — so
   *  "This also removes: ingest streams 2" would describe a data loss that
   *  does not happen, in the panel whose entire job is being precise about
   *  what does.
   *
   *  A default rather than a required prop, so the deletions that meant the
   *  original sentence keep saying it without being touched. */
  consequencesLabel?: string;
  confirmLabel?: string;
  onConfirm: () => void | Promise<void>;
}

export function ConfirmDestructive({
  open,
  onOpenChange,
  subject,
  title,
  description,
  requireTyping = false,
  consequences,
  consequencesLabel = "This also removes",
  confirmLabel = "Delete",
  onConfirm,
}: Readonly<ConfirmDestructiveProps>) {
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);

  // Cleared on every open, so a previous confirmation cannot leave the button
  // already unlocked for the next one.
  useEffect(() => {
    if (open) {
      setTyped("");
      setBusy(false);
    }
  }, [open]);

  const unlocked = !requireTyping || typed.trim() === subject;

  const run = async () => {
    if (!unlocked || busy) return;
    setBusy(true);
    try {
      await onConfirm();
      onOpenChange(false);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <AlertTriangle className="h-4 w-4 shrink-0 text-down" />
            {title}
          </DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>

        {consequences && consequences.length > 0 && (
          <div className="flex flex-col gap-1 rounded-md border border-down/40 bg-down-dim/20 px-2.5 py-2">
            <span className="text-[10px] uppercase tracking-wider text-subtle-foreground">
              {consequencesLabel}
            </span>
            {consequences.map((c) => (
              <div key={c.label} className="flex items-baseline justify-between text-[12px]">
                <span>{c.label}</span>
                <span className="tnum font-mono font-semibold">{c.count}</span>
              </div>
            ))}
          </div>
        )}

        {requireTyping && (
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="confirm-subject">
              Type <span className="font-mono font-semibold">{subject}</span> to confirm
            </Label>
            <Input
              id="confirm-subject"
              value={typed}
              autoComplete="off"
              spellCheck={false}
              placeholder={subject}
              onChange={(e) => setTyped(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && void run()}
            />
          </div>
        )}

        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)} disabled={busy}>
            Cancel
          </Button>
          <Button variant="destructive" onClick={() => void run()} disabled={!unlocked || busy}>
            {confirmLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
