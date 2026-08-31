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
        className={cn("pe-8", className)}
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
