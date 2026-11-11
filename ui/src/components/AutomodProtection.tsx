import { useEffect, useState } from "react";
import { ChevronDown, ChevronRight } from "lucide-react";
import { api } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { AddTermEditor, StringListEditor } from "@/components/AutomodProtectionEditors";
import { AutomodProtectionLadder } from "@/components/AutomodProtectionLadder";
import { AutomodProtectionTest } from "@/components/AutomodProtectionTest";
import { AutomodProtectionTwitch } from "@/components/AutomodProtectionTwitch";
import { ProtectionOfferBanner, type ProtectionAnswer } from "@/components/ProtectionOfferBanner";
import {
  categoryEnabled,
  categoryHoldState,
  ceilingOf,
  CEILING_KEYS,
  CEILINGS,
  detectorEnabled,
  detectorHolds,
  LADDER_RUNGS,
  protectionOf,
  PROTECTION_CATEGORIES,
  RISK_KEYS,
  TIER_KEYS,
  withCategoryEnabled,
  withCeiling,
  withDetectorEnabled,
  withDetectorHold,
  type ProtectionCategoryDef,
} from "@/lib/automodProtection";
import { useT } from "@/lib/i18n";
import type {
  AutomodCeiling,
  AutomodCell,
  AutomodProtection,
  AutomodProtectionState,
  Settings,
} from "@/lib/types";

/* ===========================================================================
   RECOMMENDED PROTECTION.

   The pack is DISPLAY-ONLY out of the box: a hit is flagged in the console and
   (for the detectors that hold) kept off every overlay, and nothing is sent to
   any platform until the operator arms a rung of the ladder on that platform.
   So the worst a false positive can do is hide one line from the broadcast.

   Everything here edits the same draft the rest of the page does, and is saved
   with it. What it stores is a DIFF over the pack (lib/automodProtection.ts
   keeps a value out of the draft when it equals the pack's default), so an
   install that never customised anything stays "uncustomised" and a pack
   update reaches it whole.
   =========================================================================== */

export interface AutomodProtectionCardProps {
  settings: Settings;
  onChange: (next: Settings) => void;
  /** The last SAVED settings. The Twitch actions use them, not the draft, so the
   *  card says so while the two differ. */
  saved?: Settings;
}

const HOLD_KEYS = {
  held: "automod.protHeld",
  some: "automod.protHeldSome",
  console: "automod.protConsoleOnly",
} as const;

export function AutomodProtectionCard({ settings, onChange, saved }: Readonly<AutomodProtectionCardProps>) {
  const t = useT();
  const automod = settings.automod;
  const [cells, setCells] = useState<AutomodCell[]>([]);
  const [platforms, setPlatforms] = useState<string[]>([]);
  const [open, setOpen] = useState<Record<string, boolean>>({});

  // The platform list and each rung's availability come from the server that
  // enforces them; a failed read leaves the categories usable and the ladder
  // empty rather than guessing which platform can do what.
  useEffect(() => {
    let live = true;
    api
      .automodMatrix()
      .then((m) => {
        if (!live) return;
        setCells(m.cells);
        setPlatforms(m.platforms);
      })
      .catch(() => undefined);
    return () => {
      live = false;
    };
  }, []);

  if (!automod) return null;
  const prot = protectionOf(automod);
  // The server's own bookkeeping (native) is not an edit.
  const keyOf = (s: Settings | undefined) =>
    s?.automod ? JSON.stringify({ ...protectionOf(s.automod), native: undefined, offer: undefined }) : "";
  const draftKey = keyOf(settings);
  const savedKey = saved ? keyOf(saved) : draftKey;
  const state = prot.state;

  function setProt(next: AutomodProtection) {
    onChange({ ...settings, automod: { ...automod!, protection: next } });
  }

  function answer(a: ProtectionAnswer) {
    // Turning it off disarms its rungs in the same save: the server refuses a
    // protection cell while the pack is off.
    const on = { ...automod!.on };
    if (a !== "on") {
      // Only the ladder's rungs: the fresh-install flag cells are not the pack's
      // to remove, and a pack switched off and on again must leave the matrix
      // as it found it.
      const rung = new RegExp(`/(${LADDER_RUNGS.join("|")})/protection$`);
      for (const k of Object.keys(on)) if (rung.test(k)) delete on[k];
    }
    onChange({ ...settings, automod: { ...automod!, on, protection: { ...prot, state: a as AutomodProtectionState } } });
  }

  const toggleId = "automod-protection-enabled";
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("automod.protTitle")}</CardTitle>
        <CardDescription>{t("automod.protDesc")}</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4">
        {state === "" ? (
          <ProtectionOfferBanner onAnswer={answer} />
        ) : (
          <div className="flex items-center justify-between gap-3 rounded-md border p-3">
            <div>
              <Label htmlFor={toggleId}>{t("automod.protToggle")}</Label>
              <p className="mt-0.5 text-xs text-muted-foreground">
                {t("automod.protPack", { pack: prot.pack || "en", version: Math.max(prot.packVersion, 1) })}
              </p>
            </div>
            <Switch id={toggleId} checked={state === "on"} onCheckedChange={(v) => answer(v ? "on" : "off")} />
          </div>
        )}

        {state === "on" && (
          <>
            <ul className="grid gap-2" aria-label={t("automod.protCategories")}>
              {PROTECTION_CATEGORIES.map((cat) => (
                <CategoryRow
                  key={cat.id}
                  cat={cat}
                  prot={prot}
                  open={open[cat.id] ?? false}
                  onToggleOpen={() => setOpen((o) => ({ ...o, [cat.id]: !(o[cat.id] ?? false) }))}
                  onChange={setProt}
                />
              ))}
            </ul>

            <AutomodProtectionLadder settings={settings} onChange={onChange} platforms={platforms} cells={cells} />
            <AutomodProtectionTwitch unsaved={savedKey !== draftKey} savedKey={savedKey} />
            <AutomodProtectionTest platforms={platforms} />
            <Advanced prot={prot} onChange={setProt} />
          </>
        )}
      </CardContent>
    </Card>
  );
}

function CategoryRow({
  cat,
  prot,
  open,
  onToggleOpen,
  onChange,
}: Readonly<{
  cat: ProtectionCategoryDef;
  prot: AutomodProtection;
  open: boolean;
  onToggleOpen: () => void;
  onChange: (next: AutomodProtection) => void;
}>) {
  const t = useT();
  const enabled = categoryEnabled(prot, cat);
  const hold = categoryHoldState(prot, cat);
  const ceiling = ceilingOf(prot, cat);
  const label = t(cat.label);
  const switchId = `automod-prot-cat-${cat.id}`;
  const panelId = `${switchId}-panel`;
  return (
    <li className="rounded-md border">
      <div className="flex flex-wrap items-center gap-2 p-2">
        <Switch
          id={switchId}
          checked={enabled}
          aria-label={label}
          onCheckedChange={(v) => onChange(withCategoryEnabled(prot, cat, v))}
        />
        <Label htmlFor={switchId} className="font-medium">
          {label}
        </Label>
        {enabled && (
          <>
            <Badge variant={hold === "console" ? "outline" : "default"}>{t(HOLD_KEYS[hold])}</Badge>
            <span className="text-sm text-muted-foreground">{t(RISK_KEYS[cat.risk])}</span>
            <span className="text-sm text-muted-foreground">
              {t("automod.protHighest", { action: t(CEILING_KEYS[ceiling]) })}
            </span>
          </>
        )}
        <button
          type="button"
          className="ml-auto flex items-center gap-1 text-sm text-muted-foreground underline"
          aria-expanded={open}
          aria-controls={panelId}
          aria-label={t("automod.protDetails", { category: label })}
          onClick={onToggleOpen}
        >
          {open ? <ChevronDown className="size-3.5" aria-hidden /> : <ChevronRight className="size-3.5" aria-hidden />}
          {t("automod.protDetailsShort")}
        </button>
      </div>
      {open && (
        <div id={panelId} className="grid gap-3 border-t p-3">
          <CategoryDetails cat={cat} prot={prot} enabled={enabled} onChange={onChange} />
        </div>
      )}
    </li>
  );
}

function CategoryDetails({
  cat,
  prot,
  enabled,
  onChange,
}: Readonly<{
  cat: ProtectionCategoryDef;
  prot: AutomodProtection;
  enabled: boolean;
  onChange: (next: AutomodProtection) => void;
}>) {
  const t = useT();
  const set = <K extends keyof AutomodProtection>(k: K, v: AutomodProtection[K]) => onChange({ ...prot, [k]: v });
  // A single-detector category (impersonation, raid) is switched by the row
  // itself; only its hold policy is a choice here.
  const single = cat.detectors.length === 1 && cat.detectors[0].id === cat.id;
  return (
    <>
      {cat.detectors.length > 0 && (
        <ul className="grid gap-1.5">
          {cat.detectors.map((d) => {
            const dLabel = t(d.label);
            const on = enabled && detectorEnabled(prot, cat, d);
            return (
              <li key={d.group} className="flex flex-wrap items-center gap-3">
                {!single && d.switchable && (
                  <Switch
                    checked={detectorEnabled(prot, cat, d)}
                    disabled={!enabled}
                    aria-label={dLabel}
                    onCheckedChange={(v) => onChange(withDetectorEnabled(prot, cat, d, v))}
                  />
                )}
                {!single && <span className="min-w-40 text-sm">{dLabel}</span>}
                <label className="flex items-center gap-1.5 text-sm text-muted-foreground">
                  <Checkbox
                    checked={detectorHolds(prot, d)}
                    disabled={!on && d.switchable}
                    aria-label={t("automod.protHoldDetector", { detector: dLabel })}
                    onCheckedChange={(v) => onChange(withDetectorHold(prot, d, v === true))}
                  />
                  {t("automod.protHoldOffOverlay")}
                </label>
              </li>
            );
          })}
        </ul>
      )}
      {cat.id === "doxxing" && <p className="text-sm text-muted-foreground">{t("automod.protAddressNote")}</p>}

      {cat.terms && (
        <>
          <AddTermEditor
            id={`automod-prot-add-${cat.id}`}
            category={cat.id}
            tiers={cat.terms}
            tierLabel={(x) => (TIER_KEYS[x] ? t(TIER_KEYS[x]) : x)}
            terms={prot.addTerms ?? []}
            onChange={(next) => set("addTerms", next)}
          />
          <StringListEditor
            id={`automod-prot-allow-${cat.id}`}
            label={t("automod.protAllowWord")}
            min={2}
            values={prot.allowTerms ?? []}
            onChange={(next) => set("allowTerms", next)}
          />
        </>
      )}
      {(cat.id === "scam" || cat.id === "doxxing") && (
        <StringListEditor
          id={`automod-prot-domains-${cat.id}`}
          label={t("automod.protAllowDomain")}
          placeholder="example.com"
          values={prot.allowDomains ?? []}
          onChange={(next) => set("allowDomains", next)}
        />
      )}
      {cat.id === "impersonation" && (
        <>
          <StringListEditor
            id="automod-prot-protect"
            label={t("automod.protProtectNames")}
            values={prot.impersonation.protect ?? []}
            onChange={(next) => set("impersonation", { ...prot.impersonation, protect: next })}
          />
          <StringListEditor
            id="automod-prot-allow-ids"
            label={t("automod.protAllowIds")}
            placeholder="twitch:123"
            values={prot.impersonation.allowIds ?? []}
            onChange={(next) => set("impersonation", { ...prot.impersonation, allowIds: next })}
          />
        </>
      )}
    </>
  );
}

/** The longest timeout the server accepts (two weeks, Twitch's own limit). */
const MAX_SECONDS = 14 * 24 * 3600;

/** A seconds field that keeps what is typed. Clearing it to type a new number
 *  must not become a 1 on the way (600 cleared then 30 typed is 30, not 130),
 *  so the raw text is local and only a whole number from 1 up is committed to
 *  the draft; on blur the text snaps to the value that will be saved. Never
 *  zero (a zero timeout is a permanent ban) and never over the server's bound. */
function SecondsInput({
  id,
  value,
  onCommit,
}: Readonly<{ id: string; value: number; onCommit: (n: number) => void }>) {
  const [raw, setRaw] = useState(String(value));
  useEffect(() => {
    setRaw((r) => (Number(r) === value ? r : String(value)));
  }, [value]);
  const parse = (v: string) => {
    const n = Math.floor(Number(v));
    return v.trim() !== "" && Number.isFinite(n) && n >= 1 ? Math.min(n, MAX_SECONDS) : null;
  };
  return (
    <Input
      id={id}
      type="number"
      min={1}
      max={MAX_SECONDS}
      value={raw}
      onChange={(e) => {
        setRaw(e.target.value);
        const n = parse(e.target.value);
        if (n !== null) onCommit(n);
      }}
      onBlur={() => setRaw(String(parse(raw) ?? value))}
    />
  );
}

function Advanced({
  prot,
  onChange,
}: Readonly<{ prot: AutomodProtection; onChange: (next: AutomodProtection) => void }>) {
  const t = useT();
  const [open, setOpen] = useState(false);
  return (
    <div className="rounded-md border">
      <button
        type="button"
        className="flex w-full items-center gap-2 p-2 text-left text-sm font-medium"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        {open ? <ChevronDown className="size-4" aria-hidden /> : <ChevronRight className="size-4" aria-hidden />}
        {t("automod.protAdvanced")}
      </button>
      {open && (
        <div className="grid gap-3 border-t p-3">
          <div className="flex flex-wrap gap-3">
            <div className="flex flex-col gap-1">
              <Label htmlFor="automod-prot-timeout">{t("automod.protTimeoutSeconds")}</Label>
              <SecondsInput
                id="automod-prot-timeout"
                value={prot.ladderTimeoutSeconds}
                onCommit={(n) => onChange({ ...prot, ladderTimeoutSeconds: n })}
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="automod-prot-window">{t("automod.protStrikeWindow")}</Label>
              <SecondsInput
                id="automod-prot-window"
                value={prot.strikeWindowSeconds}
                onCommit={(n) => onChange({ ...prot, strikeWindowSeconds: n })}
              />
            </div>
          </div>
          <p className="text-sm text-muted-foreground">{t("automod.protSelfHarmNote")}</p>
          <div className="grid gap-2">
            {PROTECTION_CATEGORIES.map((cat) => {
              const id = `automod-prot-ceiling-${cat.id}`;
              return (
                <div key={cat.id} className="flex flex-wrap items-center gap-2">
                  <Label htmlFor={id} className="min-w-48">
                    {t("automod.protCeilingFor", { category: t(cat.label) })}
                  </Label>
                  <select
                    id={id}
                    className="h-8 rounded-md border border-input bg-transparent px-2 text-[12px]"
                    value={ceilingOf(prot, cat)}
                    onChange={(e) => onChange(withCeiling(prot, cat, e.target.value as AutomodCeiling))}
                  >
                    {CEILINGS.map((c) => (
                      <option key={c} value={c}>
                        {t(CEILING_KEYS[c])}
                      </option>
                    ))}
                  </select>
                </div>
              );
            })}
          </div>
        </div>
      )}
    </div>
  );
}
