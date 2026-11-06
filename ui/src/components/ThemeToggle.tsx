import { useRef, type KeyboardEvent } from "react";
import { Monitor, Moon, Sun, type LucideIcon } from "lucide-react";

import { useTheme } from "@/components/ThemeProvider";
import { useT } from "@/lib/i18n";
import { THEME_CHOICES, type ThemeChoice } from "@/lib/theme";
import { cn } from "@/lib/utils";

const OPTIONS: Record<ThemeChoice, { icon: LucideIcon; label: "chrome.themeLight" | "chrome.themeDark" | "chrome.themeSystem" }> = {
  light: { icon: Sun, label: "chrome.themeLight" },
  dark: { icon: Moon, label: "chrome.themeDark" },
  system: { icon: Monitor, label: "chrome.themeSystem" },
};

/** Light / Dark / System, as a segmented control in the console header.
 *
 *  A radiogroup because exactly one of the three is true at a time: one tab
 *  stop on the checked option, arrow keys move AND pick (the platform's own
 *  radio behaviour, so a keyboard user sees the result as they go). The icons
 *  carry no meaning on their own: every option has an aria-label and a title
 *  in the operator's language. The focus ring is the one global ring in
 *  index.css, not restated here. */
export function ThemeToggle({ className }: Readonly<{ className?: string }>) {
  const t = useT();
  const { choice, setChoice } = useTheme();
  const refs = useRef<Partial<Record<ThemeChoice, HTMLButtonElement | null>>>({});

  const move = (to: ThemeChoice) => {
    setChoice(to);
    refs.current[to]?.focus();
  };

  const onKeyDown = (e: KeyboardEvent<HTMLButtonElement>, at: number) => {
    const last = THEME_CHOICES.length - 1;
    let to: number;
    switch (e.key) {
      case "ArrowRight":
      case "ArrowDown":
        to = at === last ? 0 : at + 1;
        break;
      case "ArrowLeft":
      case "ArrowUp":
        to = at === 0 ? last : at - 1;
        break;
      case "Home":
        to = 0;
        break;
      case "End":
        to = last;
        break;
      default:
        return;
    }
    e.preventDefault();
    move(THEME_CHOICES[to]);
  };

  return (
    <div
      role="radiogroup"
      aria-label={t("chrome.theme")}
      data-testid="theme-toggle"
      className={cn("inline-flex items-center gap-0.5 rounded-md border border-border bg-card p-0.5", className)}
    >
      {THEME_CHOICES.map((value, i) => {
        const { icon: Icon, label } = OPTIONS[value];
        const checked = choice === value;
        return (
          <button
            key={value}
            ref={(el) => {
              refs.current[value] = el;
            }}
            type="button"
            role="radio"
            aria-checked={checked}
            aria-label={t(label)}
            title={t(label)}
            data-theme-choice={value}
            tabIndex={checked ? 0 : -1}
            onClick={() => setChoice(value)}
            onKeyDown={(e) => onKeyDown(e, i)}
            className={cn(
              "flex h-6 w-7 items-center justify-center rounded-sm transition-colors",
              checked
                ? "bg-primary-dim text-primary-bright"
                : "text-muted-foreground hover:bg-accent hover:text-foreground",
            )}
          >
            <Icon className="h-3.5 w-3.5" aria-hidden="true" />
          </button>
        );
      })}
    </div>
  );
}
