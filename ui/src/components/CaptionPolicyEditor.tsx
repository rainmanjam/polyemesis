import { useState } from "react";

import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { captionWords } from "@/lib/captions";
import { useT } from "@/lib/i18n";
import type { CaptionPolicy } from "@/lib/types";

/** One programme's caption words (#8): terms spelled the operator's way, and
 *  words masked on screen. Saved with its own button, because a list is typed
 *  over several keystrokes and a save on every change would send each half-word.
 *  A save reaches captions that are already running, from the next caption. */
export function CaptionPolicyEditor({
  sourceId,
  policy,
  busy,
  onSave,
}: Readonly<{
  sourceId: number;
  policy?: CaptionPolicy;
  busy: boolean;
  onSave: (policy: CaptionPolicy) => void;
}>) {
  const t = useT();
  const [vocab, setVocab] = useState((policy?.vocabulary ?? []).join("\n"));
  const [block, setBlock] = useState((policy?.blocklist ?? []).join("\n"));
  const vocabId = `src-caption-vocab-${sourceId}`;
  const blockId = `src-caption-block-${sourceId}`;
  return (
    <div className="flex flex-col gap-2" data-testid="caption-policy">
      <span className="text-sm font-medium">{t("wave3.captions.policyTitle")}</span>
      <div className="flex flex-col gap-1">
        <Label htmlFor={vocabId}>{t("wave3.captions.vocabLabel")}</Label>
        <Textarea id={vocabId} rows={3} value={vocab} onChange={(e) => setVocab(e.target.value)} disabled={busy} />
        <p className="text-xs text-muted-foreground">{t("wave3.captions.vocabHint")}</p>
      </div>
      <div className="flex flex-col gap-1">
        <Label htmlFor={blockId}>{t("wave3.captions.blockLabel")}</Label>
        <Textarea id={blockId} rows={3} value={block} onChange={(e) => setBlock(e.target.value)} disabled={busy} />
        <p className="text-xs text-muted-foreground">{t("wave3.captions.blockHint")}</p>
      </div>
      <div>
        <Button
          size="sm"
          variant="outline"
          disabled={busy}
          onClick={() => onSave({ vocabulary: captionWords(vocab), blocklist: captionWords(block) })}
        >
          {t("wave3.captions.policySave")}
        </Button>
      </div>
    </div>
  );
}
