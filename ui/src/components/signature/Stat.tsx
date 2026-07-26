import { cn } from "@/lib/utils";

/** A labelled figure. Dense by construction: label above, value below, both
 *  tight, value tabular so a row of these does not jitter as numbers update. */
export function Stat({
  label,
  value,
  unit,
  tone,
  className,
}: {
  label: string;
  value: string | number;
  unit?: string;
  tone?: "default" | "live" | "warn" | "down" | "muted";
  className?: string;
}) {
  return (
    <div className={cn("flex flex-col gap-0.5", className)}>
      <div className="text-[9px] uppercase tracking-wider text-muted-foreground">{label}</div>
      <div
        className={cn(
          "tnum font-mono text-[13px] leading-none",
          tone === "live" && "text-live",
          tone === "warn" && "text-warn",
          tone === "down" && "text-down",
          tone === "muted" && "text-muted-foreground",
        )}
      >
        {value}
        {unit && <span className="ml-0.5 text-[10px] text-muted-foreground">{unit}</span>}
      </div>
    </div>
  );
}
