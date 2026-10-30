import { clsx, type ClassValue } from "clsx";
import { extendTailwindMerge } from "tailwind-merge";

/* The type scale's own step names (index.css @theme: --text-micro, --text-tiny,
 * --text-display) are not in tailwind-merge's default list, so it read
 * `text-tiny` as a COLOUR and dropped the real colour before it: every small
 * primary button lost text-primary-foreground and drew near-white on blue,
 * 3.25:1 (B2, measured in the browser). Naming the steps makes them sizes. */
const twMerge = extendTailwindMerge({
  extend: { theme: { text: ["micro", "tiny", "display"] } },
});

/** Merge conditional class names, letting later Tailwind classes win. */
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}
