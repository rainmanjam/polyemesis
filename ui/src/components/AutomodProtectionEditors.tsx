import { useState } from "react";
import { X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useT } from "@/lib/i18n";
import type { AutomodProtectionTerm } from "@/lib/types";

/* Small editors for the pack's diff: a list of strings (allowed words, allowed
 * domains, protected names, author ids) and the "add a word" row. Controlled,
 * draft-only: nothing here saves. The server bounds a term to 2-100 characters
 * and refuses the rest, so the add button applies the same bound rather than
 * letting a 400 arrive after the Save button. */

const TERM_MIN = 2;
const TERM_MAX = 100;

export function StringListEditor({
  id,
  label,
  placeholder,
  values,
  onChange,
  min = 1,
}: Readonly<{
  id: string;
  label: string;
  placeholder?: string;
  /** The fewest characters an entry may have (the server's bound for it). */
  min?: number;
  values: readonly string[];
  onChange: (next: string[]) => void;
}>) {
  const t = useT();
  const [draft, setDraft] = useState("");
  const value = draft.trim();
  const dup = values.includes(value);
  const short = value !== "" && [...value].length < min;
  const add = () => {
    if (value === "" || dup || short) return;
    onChange([...values, value]);
    setDraft("");
  };
  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      <div className="flex gap-2">
        <Input
          id={id}
          value={draft}
          placeholder={placeholder}
          maxLength={TERM_MAX}
          aria-invalid={short}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              add();
            }
          }}
        />
        <Button type="button" size="sm" variant="secondary" disabled={value === "" || dup || short} onClick={add}>
          {t("automod.protAdd")}
        </Button>
      </div>
      {short && <p className="text-sm text-destructive">{t("automod.protTermLength", { min, max: TERM_MAX })}</p>}
      {values.length > 0 && (
        <ul className="flex flex-wrap gap-1.5" aria-label={label}>
          {values.map((v) => (
            <li key={v} className="flex items-center gap-1 rounded-md border px-1.5 py-0.5 text-sm">
              <span>{v}</span>
              <button
                type="button"
                className="text-muted-foreground hover:text-foreground"
                aria-label={t("automod.protRemove", { name: v })}
                onClick={() => onChange(values.filter((x) => x !== v))}
              >
                <X className="size-3" aria-hidden />
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/** Add a word to a term category at one of its tiers. */
export function AddTermEditor({
  id,
  category,
  tiers,
  tierLabel,
  terms,
  onChange,
}: Readonly<{
  id: string;
  category: string;
  tiers: readonly string[];
  tierLabel: (tier: string) => string;
  terms: readonly AutomodProtectionTerm[];
  onChange: (next: AutomodProtectionTerm[]) => void;
}>) {
  const t = useT();
  const [draft, setDraft] = useState("");
  const [tier, setTier] = useState(tiers[0]);
  const value = draft.trim();
  const len = [...value].length;
  const mine = terms.filter((x) => x.category === category);
  const bad = value !== "" && (len < TERM_MIN || len > TERM_MAX);
  const dup = mine.some((x) => x.term === value);
  const add = () => {
    if (value === "" || bad || dup) return;
    onChange([...terms, { term: value, category, tier }]);
    setDraft("");
  };
  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor={id}>{t("automod.protAddWord")}</Label>
      <div className="flex flex-wrap gap-2">
        <Input
          id={id}
          className="min-w-40 flex-1"
          value={draft}
          maxLength={TERM_MAX}
          aria-invalid={bad}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              add();
            }
          }}
        />
        <select
          aria-label={t("automod.protTier")}
          className="h-9 rounded-md border border-input bg-transparent px-2 text-[12px]"
          value={tier}
          onChange={(e) => setTier(e.target.value)}
        >
          {tiers.map((x) => (
            <option key={x} value={x}>
              {tierLabel(x)}
            </option>
          ))}
        </select>
        <Button type="button" size="sm" variant="secondary" disabled={value === "" || bad || dup} onClick={add}>
          {t("automod.protAdd")}
        </Button>
      </div>
      {bad && <p className="text-sm text-destructive">{t("automod.protTermLength", { min: TERM_MIN, max: TERM_MAX })}</p>}
      {mine.length > 0 && (
        <ul className="flex flex-wrap gap-1.5" aria-label={t("automod.protAddedWords")}>
          {mine.map((x) => (
            <li key={`${x.tier}:${x.term}`} className="flex items-center gap-1 rounded-md border px-1.5 py-0.5 text-sm">
              <span>{x.term}</span>
              <span className="text-muted-foreground">{tierLabel(x.tier)}</span>
              <button
                type="button"
                className="text-muted-foreground hover:text-foreground"
                aria-label={t("automod.protRemove", { name: x.term })}
                onClick={() => onChange(terms.filter((y) => y !== x))}
              >
                <X className="size-3" aria-hidden />
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
