import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Check, Copy, Terminal } from "lucide-react";
import { cn } from "@/lib/utils";

/** The generated FFmpeg filter graph, shown verbatim.
 *
 *  This exists for transparency: routing is the feature people will least
 *  trust, and being able to read (and paste) the exact filter is what turns
 *  "I hope it excluded the music track" into "I can see that it did". */
export function FilterString({
  value,
  className,
}: {
  value: string;
  className?: string;
}) {
  const [copied, setCopied] = useState(false);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      /* clipboard blocked (insecure origin): the text is selectable anyway */
    }
  };

  if (!value) return null;

  return (
    <div className={cn("rounded-md border border-border bg-background", className)}>
      <div className="flex items-center justify-between border-b border-border px-2 py-1">
        <div className="flex items-center gap-1.5 text-[10px] uppercase tracking-wider text-muted-foreground">
          <Terminal className="h-3 w-3" />
          generated filter_complex
        </div>
        <Button variant="ghost" size="icon-sm" onClick={copy} aria-label="Copy filter string">
          {copied ? <Check className="text-live" /> : <Copy />}
        </Button>
      </div>
      <pre className="max-h-32 overflow-auto whitespace-pre-wrap break-all px-2 py-1.5 font-mono text-[10px] leading-relaxed text-muted-foreground">
        {value}
      </pre>
    </div>
  );
}
