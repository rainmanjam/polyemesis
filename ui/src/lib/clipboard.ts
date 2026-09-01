import { toast } from "sonner";
import type { Translator } from "@/lib/i18n";

/** Copy a value and SAY whether it worked.
 *
 *  Lifted out of SourcesPage because HooksCard grew a Copy button that did not
 *  say: `void navigator.clipboard?.writeText(x)` discards the promise, and on
 *  an http:// install `navigator.clipboard` is undefined, so the click did
 *  nothing and the operator -- told the secret is never shown again -- believed
 *  it had copied. That inverts the point of masking the value: the argument
 *  for a mask is that Copy works while masked, and a Copy that fails silently
 *  makes reveal-and-retype the only path that actually works.
 *
 *  `what` is display-only, interpolated into the toast. It is lowercased for
 *  the failure message, which reads mid-sentence. */
export function copyToClipboard(t: Translator, text: string, what: string): void {
  const clip = navigator.clipboard;
  if (!clip) {
    // Not an error branch of writeText: the API is absent, which is what an
    // insecure context looks like. Same message, because the operator's next
    // step is the same either way.
    toast.error(t("sources.copyFailed", { what: what.toLowerCase() }));
    return;
  }
  void clip
    .writeText(text)
    .then(() => toast.success(t("sources.copied", { what })))
    .catch(() => toast.error(t("sources.copyFailed", { what: what.toLowerCase() })));
}
