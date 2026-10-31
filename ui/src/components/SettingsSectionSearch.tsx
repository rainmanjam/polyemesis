import { useEffect, useId, useMemo, useRef, useState } from "react";
import { Search } from "lucide-react";
import { Input } from "@/components/ui/input";
import {
  isTypingTarget,
  revealSettingsHit,
  searchSettings,
  searchSettingsFields,
  type SettingsHit,
  type SettingsTab,
} from "@/lib/settingsSearch";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";

/** One row of the result list: a whole section, or a field inside one. */
type Result = { kind: "section"; tab: SettingsTab } | { kind: "field"; hit: SettingsHit };

/** How many frames to wait for a section's panel to mount before giving up on
 *  scrolling to the field. Radix mounts the panel on the render after the tab
 *  changes; a handful of frames covers a slow device without spinning for ever. */
const REVEAL_FRAMES = 20;

/** The settings search (A17). Filters sections and the fields inside them as
 *  you type. `/` focuses it from anywhere on the page that is not a text field,
 *  Esc clears it, the arrow keys move through the results and Enter opens one.
 *
 *  Picking a result hands the section back to the page, which sets the same
 *  `?tab=` the rail sets, so a search result is a link like any other. A field
 *  result then scrolls to the field and focuses it. */
export function SettingsSectionSearch({
  labels,
  onPick,
}: {
  labels: Record<SettingsTab, string>;
  /** The section to open, and the field hit when the pick was a field, so the
   *  page can open a hidden advanced card before the hit is revealed
   *  (settings-council R7). */
  onPick: (tab: SettingsTab, hit?: SettingsHit) => void;
}) {
  const t = useT();
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const [pending, setPending] = useState<SettingsHit | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const listId = useId();

  const results = useMemo<Result[]>(() => {
    const sections = searchSettings(query, labels).map((tab): Result => ({ kind: "section", tab }));
    // A field whose label is the section's own name (the Ingest card on the
    // Ingest tab) would be listed twice, once as each.
    const fields = searchSettingsFields(query, t)
      .filter((hit) => !(hit.context === "" && hit.label === labels[hit.tab]))
      .map((hit): Result => ({ kind: "field", hit }));
    return [...sections, ...fields];
  }, [query, labels, t]);
  const typed = query.trim() !== "";
  const open = typed;

  // `/` from anywhere that is not a text field. Ignored with a modifier, so
  // the browser's own shortcuts on that key keep working.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "/" || e.metaKey || e.ctrlKey || e.altKey) return;
      if (isTypingTarget(e.target)) return;
      e.preventDefault();
      inputRef.current?.focus();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  // After the tab changes, wait for its panel and then bring the field into
  // view. Done in an effect, not the click handler, because the panel the
  // field lives in does not exist until the page has re-rendered.
  useEffect(() => {
    if (!pending) return;
    let frame = 0;
    let handle = 0;
    const tryReveal = () => {
      if (revealSettingsHit(pending) || ++frame >= REVEAL_FRAMES) {
        setPending(null);
        return;
      }
      handle = window.requestAnimationFrame(tryReveal);
    };
    handle = window.requestAnimationFrame(tryReveal);
    return () => window.cancelAnimationFrame(handle);
  }, [pending]);

  const pick = (r: Result) => {
    setQuery("");
    setActive(0);
    if (r.kind === "section") {
      onPick(r.tab);
      return;
    }
    onPick(r.hit.tab, r.hit);
    setPending(r.hit);
  };

  const onKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Escape") {
      // First Esc clears; a second one leaves the box.
      if (query !== "") {
        e.preventDefault();
        setQuery("");
        setActive(0);
      } else {
        inputRef.current?.blur();
      }
      return;
    }
    if (!open || results.length === 0) return;
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setActive((i) => (i + 1) % results.length);
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setActive((i) => (i - 1 + results.length) % results.length);
    } else if (e.key === "Enter") {
      e.preventDefault();
      pick(results[Math.min(active, results.length - 1)]);
    }
  };

  const optionId = (i: number) => `${listId}-opt-${i}`;

  return (
    <div className="relative mb-3 max-w-md">
      <Search
        aria-hidden
        className="pointer-events-none absolute top-1/2 left-2.5 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground"
      />
      <Input
        ref={inputRef}
        type="search"
        role="combobox"
        aria-label={t("settings.search")}
        aria-expanded={open}
        aria-controls={listId}
        aria-autocomplete="list"
        aria-activedescendant={open && results.length > 0 ? optionId(active) : undefined}
        placeholder={t("settings.searchPlaceholder")}
        className="pr-8 pl-8"
        value={query}
        onChange={(e) => {
          setQuery(e.target.value);
          setActive(0);
        }}
        onKeyDown={onKeyDown}
      />
      {/* The shortcut, shown where the eye already is. */}
      {!typed && (
        <kbd
          aria-hidden
          className="pointer-events-none absolute top-1/2 right-2 -translate-y-1/2 rounded border border-border px-1.5 font-mono text-tiny text-muted-foreground"
        >
          /
        </kbd>
      )}
      {open && (
        <div
          className="absolute top-full right-0 left-0 z-20 mt-1 max-h-80 overflow-y-auto rounded-md border border-border bg-popover p-1 shadow-md"
        >
          {results.length === 0 ? (
            <p role="status" className="px-2 py-3 text-center text-sm text-muted-foreground">
              {t("settings.noMatches")}
            </p>
          ) : (
            <ul id={listId} role="listbox" aria-label={t("settings.search")}>
              {results.map((r, i) => {
                const label = r.kind === "section" ? labels[r.tab] : r.hit.label;
                const where =
                  r.kind === "section"
                    ? t("settings.wholeSection")
                    : [labels[r.hit.tab], r.hit.context].filter(Boolean).join(" › ");
                return (
                  <li
                    key={r.kind === "section" ? `s-${r.tab}` : `f-${r.hit.tab}-${r.hit.key}-${r.hit.anchor ?? ""}`}
                    id={optionId(i)}
                    role="option"
                    aria-selected={i === active}
                    // mousedown, not click: a click would blur the input first
                    // and the list would close under the pointer.
                    onMouseDown={(e) => {
                      e.preventDefault();
                      pick(r);
                    }}
                    onMouseEnter={() => setActive(i)}
                    className={cn(
                      "flex cursor-pointer flex-col rounded px-2 py-1.5",
                      i === active && "bg-accent text-accent-foreground",
                    )}
                  >
                    <span className="text-sm">{label}</span>
                    <span className="text-tiny text-muted-foreground">{where}</span>
                  </li>
                );
              })}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}
