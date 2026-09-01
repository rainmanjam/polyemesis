import { useState } from "react";
import { Eye, EyeOff } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/** SecretInput's read-only counterpart: a credential that is DISPLAYED.
 *
 *  SecretInput covers the field an operator types into. It does not cover the
 *  far more exposed case, which is a credential the console simply prints —
 *  and the Sources page printed the publish token twice, in plain text, on the
 *  page an operator opens precisely when someone is helping them get a
 *  broadcast up. secret-fields.test.ts could not see it: that guard matches
 *  `value={...}` bindings, and a `<code>{source.token}</code>` has no `value`.
 *
 *  Masked to a FIXED width rather than one dot per character. A mask that
 *  tracks length still answers "how long is it", which for a token of known
 *  alphabet is the one free question an onlooker gets.
 *
 *  Copy stays outside this component and keeps working while masked. Copying
 *  is how an operator is supposed to move a credential; making them reveal it
 *  first would put it on screen for the one workflow that never needed it
 *  there. */
export function SecretCode({
  value,
  className,
}: Readonly<{ value: string; className?: string }>) {
  const t = useT();
  const [shown, setShown] = useState(false);
  const label = shown ? t("secret.hide") : t("secret.reveal");
  return (
    <>
      <code
        className={cn(
          "min-w-0 flex-1 truncate rounded bg-muted px-1.5 py-1 font-mono text-[10px]",
          className,
        )}
      >
        {shown ? value : "•".repeat(16)}
      </code>
      <Button
        type="button"
        size="icon"
        variant="ghost"
        onClick={() => setShown((v) => !v)}
        aria-label={label}
        title={label}
        aria-pressed={shown}
      >
        {shown ? <EyeOff className="h-3 w-3" /> : <Eye className="h-3 w-3" />}
      </Button>
    </>
  );
}
