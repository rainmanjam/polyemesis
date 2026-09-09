import { useState } from "react";
import { Eye, EyeOff } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/** A text input for a value that should not be readable over a shoulder.
 *
 *  Masked by default with an explicit reveal, because these fields are read and
 *  edited during exactly the activity that puts a screen in front of an
 *  audience: setting up a broadcast, often while screen-sharing with someone
 *  helping. A stream key in plain text is a credential handed to everyone
 *  watching, and the operator has no reason to think of it as one — it looks
 *  like a settings field.
 *
 *  The reveal is deliberate rather than hover-based: revealing must be
 *  something you chose, not something that happens because the pointer passed
 *  over it. */
export function SecretInput({
  value,
  onChange,
  className,
  ...rest
}: Readonly<
  Omit<React.ComponentProps<typeof Input>, "type"> & {
    value: string;
    onChange: (e: React.ChangeEvent<HTMLInputElement>) => void;
  }
>) {
  const t = useT();
  const [shown, setShown] = useState(false);
  // Translated, because the icon alone says nothing to a screen reader and this
  // control guards a credential -- the one place a reader most needs to know
  // what the button will do before pressing it.
  const label = shown ? t("secret.hide") : t("secret.reveal");
  return (
    <div className="relative flex items-center">
      <Input
        {...rest}
        type={shown ? "text" : "password"}
        value={value}
        onChange={onChange}
        autoComplete="off"
        spellCheck={false}
        /* MONOSPACED HERE, NOT AT THE CALL SITES, because this component is
           the definition of "a value nobody can afford to misread".
           docs/DESIGN-SYSTEM.md makes --font-mono a correctness rule and names
           this exact field as the example: a proportional font makes `l` and
           `1` in a stream key indistinguishable, and the failure is a stream
           that will not start for a reason nobody can see. Revealing a key to
           check it against the platform's console — the whole purpose of the
           reveal button beside this — is the moment the glyphs have to be
           unambiguous.

           Five call sites render one of these; exactly one had remembered to
           pass `font-mono`, which is what a rule enforced by remembering
           produces. Stated first in the list so a caller can still override
           it; nothing should, but the option belongs to them.

           It survives the type flip on purpose: `type` toggles between
           password and text as the value is revealed, so anything keyed on
           the type would style the masked dots and give up exactly when the
           characters appear. */
        className={cn("pe-8 font-mono", className)}
      />
      <Button
        type="button"
        size="icon"
        variant="ghost"
        className="absolute end-0 h-6 w-6"
        onClick={() => setShown((v) => !v)}
        aria-label={label}
        title={label}
      >
        {shown ? <EyeOff className="h-3 w-3" /> : <Eye className="h-3 w-3" />}
      </Button>
    </div>
  );
}
