import { useCallback, useEffect, useState, type ReactNode } from "react";
import { AlertTriangle, Plus, Trash2 } from "lucide-react";
import { api } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { ConfirmDestructive } from "@/components/ConfirmDestructive";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { AutomodModelChecker } from "@/components/AutomodModelChecker";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import {
  ACTION_KEYS,
  anyTimeout,
  CATEGORY_KEYS,
  categoryActionOf,
  MODEL_CATEGORIES,
  modelStatus,
  SUGGESTED_CATEGORY_ACTIONS,
  nextRuleId,
  ruleProblems,
  ruleReach,
  serverRejection,
} from "@/lib/automodConfig";
import { useT } from "@/lib/i18n";
import type {
  AutomodAction,
  AutomodModel,
  AutomodModelStats,
  AutomodPreset,
  AutomodRule,
  Settings,
} from "@/lib/types";

/* ===========================================================================
   The automod configuration nobody could reach.

   The matrix beside this card decides what each checker MAY do. It has always
   been the only automod control on the page — so an operator could grant the
   rules checker permission to delete a message without ever being able to write
   the rule that finds one, and could arm "Ban / Model" without a field anywhere
   for the endpoint, the prompt, the confidence floor or the hourly ceiling that
   bounds what the model costs. Twenty-seven of the thirty stored automod values
   had no input on any screen: the server read them, acted on them, and the only
   way to set one was to edit the database.

   Two things follow from that, and both shape this file:

   - THE API KEY IS SET HERE AND NOWHERE ELSE. `api.setAutomodKey` has existed
     since the endpoint did and had no caller, so the model checker could be
     switched on, pointed at a paid endpoint, and could never authenticate. The
     key is sealed server-side and never returned by any read — HasAPIKey only
     says one exists — so this renders "a key is stored" and a way to replace
     it, never a value. Anything else would be inventing a secret to display.

   - SPEND IS SHOWN BESIDE THE CEILING THAT BOUNDS IT. `api.automodStats` had no
     caller either, so MaxCallsPerHour was a number an operator set against no
     feedback at all. A ceiling with no reading beside it is a guess.

   RULE PATTERNS ARE REFUSED AGAINST THE FIELD, NOT IN A TOAST. NewRuleSet is
   all-or-nothing — one pattern that does not compile disarms every rule — so
   PUT /settings answers 400 for the whole document. The page's save reports
   that as a toast, which is four seconds long, names no field, and leaves six
   patterns to re-read. lib/automodConfig.ts turns the server's `rule "name":`
   prefix back into a field, and checks what it can before the save so most
   typos never reach it.
   =========================================================================== */

export interface AutomodConfigProps {
  settings: Settings;
  onChange: (next: Settings) => void;
  /** The last save's rejection, verbatim, when there was one.
   *
   *  Passed in rather than saved from here because the page owns the save
   *  button: automod's configuration rides inside the settings document, so
   *  there is one PUT for the whole page and one refusal for the whole
   *  document. `serverRejection` finds the rule it names; a message that names
   *  none is left to the page's own toast, because pinning an unrelated failure
   *  to a pattern sends an operator to rewrite a regex that was never wrong. */
  saveError?: string | null;
}

/** The default a NEW rule gets: on, and flagging.
 *
 *  Flagging for the reason DefaultMatrix starts there — it changes nothing an
 *  audience sees, so a rule saved before its pattern is finished cannot delete
 *  anything. Enabled, because a rule someone has just typed and has to switch
 *  on separately is a protection they believe they have. */
function blankRule(id: number): AutomodRule {
  return { id, name: "", enabled: true, pattern: "", action: "flag" };
}

/** The action picker, native.
 *
 *  A native <select> rather than the styled one, because the row it sits in is
 *  repeated per rule and this is a form control an operator tabs through — and
 *  because Dashboard and JobsPage already use plain selects for the same
 *  reason. It carries a real accessible name from its Label either way. */
function ActionSelect({
  id,
  label,
  value,
  onChange,
}: Readonly<{
  id: string;
  label: string;
  value: AutomodAction;
  onChange: (a: AutomodAction) => void;
}>) {
  const t = useT();
  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor={id}>{label}</Label>
      <select
        id={id}
        className="h-8 rounded-md border border-input bg-transparent px-2 text-[12px]"
        value={value}
        onChange={(e) => onChange(e.target.value as AutomodAction)}
      >
        {/* In ascending order of consequence, which is the order
            automod.Actions declares and the matrix renders: the most
            destructive answer is last and never the one reached by muscle
            memory. */}
        {(Object.keys(ACTION_KEYS) as AutomodAction[]).map((a) => (
          <option key={a} value={a}>
            {t(ACTION_KEYS[a])}
          </option>
        ))}
      </select>
    </div>
  );
}

/** One problem, said where the field is. Never a toast: a message about a
 *  pattern that is not beside the pattern is a message about nothing. */
function Problem({ children }: Readonly<{ children: ReactNode }>) {
  return (
    <p className="flex items-start gap-1 text-sm text-warn" role="alert">
      <AlertTriangle className="mt-0.5 size-3 shrink-0" aria-hidden />
      <span>{children}</span>
    </p>
  );
}

export function AutomodConfig({
  settings,
  onChange,
  saveError,
}: Readonly<AutomodConfigProps>) {
  const t = useT();
  const automod = settings.automod;

  const [stats, setStats] = useState<AutomodModelStats | null>(null);
  const [statsFailed, setStatsFailed] = useState(false);
  const [presets, setPresets] = useState<AutomodPreset[]>([]);
  const [categories, setCategories] = useState<readonly string[]>(MODEL_CATEGORIES);
  const [showSecondary, setShowSecondary] = useState(
    settings.automod?.secondary?.enabled === true,
  );
  const [deleting, setDeleting] = useState<AutomodRule | null>(null);

  const loadStats = useCallback(async () => {
    try {
      setStats(await api.automodStats());
      setStatsFailed(false);
    } catch {
      // NOT A ZERO. "0 of 500 calls this hour" is the reading an operator
      // checks when they suspect the model is running away with their money,
      // and a fabricated one is the reading that stops them looking.
      setStats(null);
      setStatsFailed(true);
    }
  }, []);

  useEffect(() => {
    void loadStats();
  }, [loadStats]);

  // The provider presets and the category list, from the server that enforces
  // them. A failure leaves the page usable on what it already shows: the
  // fields still edit, only the preset dropdown is empty.
  useEffect(() => {
    let live = true;
    api
      .automodMatrix()
      .then((m) => {
        if (!live) return;
        setPresets(m.presets ?? []);
        if (m.modelCategories?.length) setCategories(m.modelCategories);
      })
      .catch(() => undefined);
    return () => {
      live = false;
    };
  }, []);

  if (!automod?.model) {
    return (
      <Card>
        <CardHeader>
          <CardTitle>{t("automod.rulesTitle")}</CardTitle>
          <CardDescription>{t("automod.notLoaded")}</CardDescription>
        </CardHeader>
      </Card>
    );
  }

  const rules = automod.rules ?? [];
  const model = automod.model;

  function setRules(next: AutomodRule[]) {
    onChange({ ...settings, automod: { ...automod!, rules: next } });
  }

  function patchRule(id: number, patch: Partial<AutomodRule>) {
    setRules(rules.map((r) => (r.id === id ? { ...r, ...patch } : r)));
  }

  const secondary = automod.secondary;

  function patchModel(patch: Partial<typeof model>) {
    onChange({
      ...settings,
      automod: { ...automod!, model: { ...model, ...patch } },
    });
  }

  function patchSecondary(patch: Partial<AutomodModel>) {
    if (!secondary) return;
    onChange({
      ...settings,
      automod: { ...automod!, secondary: { ...secondary, ...patch } },
    });
  }

  /** "" puts a category back on the default action. The whole table is sent,
   *  because the server REPLACES it on save (a dropped key means default). */
  function setCategoryAction(cat: string, a: AutomodAction | "") {
    const next = { ...model.categoryActions };
    if (a === "") delete next[cat];
    else next[cat] = a;
    patchModel({ categoryActions: next });
  }

  const rejectedRule = serverRejection(saveError);

  return (
    <>
      <Card>
        <CardHeader>
          <CardTitle>{t("automod.rulesTitle")}</CardTitle>
          <CardDescription>{t("automod.rulesDesc")}</CardDescription>
        </CardHeader>

        <CardContent className="grid gap-3">
          {rules.length === 0 && (
            <p className="text-xs text-muted-foreground">
              {t("automod.rulesEmpty")}
            </p>
          )}

          {rules.map((rule) => {
            const problems = ruleProblems(rule);
            const problem = (field: string) =>
              problems.find((p) => p.field === field);
            const name = rule.name.trim();
            const refusal: string | null =
              rejectedRule !== null && rejectedRule === rule.name
                ? (saveError ?? null)
                : null;
            const idp = `automod-rule-${rule.id}`;
            const pattern = problem("pattern");
            const timeout = problem("timeout");
            const named = problem("name");

            return (
              <div
                key={rule.id}
                /* A group with a name, so a screen reader announces WHICH rule
                   these five fields belong to. Six rules on one card is six
                   fields called "Pattern" otherwise. */
                role="group"
                aria-label={name || t("automod.unnamedRule")}
                className="grid gap-2 rounded-md border p-3"
              >
                <div className="flex flex-wrap items-end gap-2">
                  <div className="flex min-w-40 flex-1 flex-col gap-1">
                    <Label htmlFor={`${idp}-name`}>
                      {t("automod.ruleName")}
                    </Label>
                    <Input
                      id={`${idp}-name`}
                      value={rule.name}
                      onChange={(e) =>
                        patchRule(rule.id, { name: e.target.value })
                      }
                    />
                  </div>
                  <div className="flex items-center gap-2 pb-1">
                    <Label htmlFor={`${idp}-enabled`} className="text-xs">
                      {t("automod.ruleEnabled")}
                    </Label>
                    <Switch
                      id={`${idp}-enabled`}
                      checked={rule.enabled}
                      onCheckedChange={(v) =>
                        patchRule(rule.id, { enabled: v })
                      }
                    />
                    <Button
                      type="button"
                      size="icon"
                      variant="ghost"
                      aria-label={t("automod.deleteRule", {
                        name: name || t("automod.unnamedRule"),
                      })}
                      onClick={() => setDeleting(rule)}
                    >
                      <Trash2 className="size-3.5" aria-hidden />
                    </Button>
                  </div>
                </div>
                {named && <Problem>{t(named.key)}</Problem>}

                <div className="flex flex-col gap-1">
                  <Label htmlFor={`${idp}-pattern`}>
                    {t("automod.rulePattern")}
                  </Label>
                  <Input
                    id={`${idp}-pattern`}
                    className="font-mono"
                    spellCheck={false}
                    autoComplete="off"
                    value={rule.pattern}
                    aria-invalid={pattern !== undefined || refusal !== null}
                    onChange={(e) =>
                      patchRule(rule.id, { pattern: e.target.value })
                    }
                  />
                  <span className="text-sm text-muted-foreground">
                    {t("automod.patternNote")}
                  </span>
                  {pattern && (
                    <Problem>
                      {t(pattern.key, { detail: pattern.detail ?? "" })}
                    </Problem>
                  )}
                  {/* The server's own words, against the field they are about.
                      RE2 refuses things JavaScript compiles happily — a
                      backreference, a lookahead — so the check above cannot be
                      the only one. */}
                  {refusal && (
                    <Problem>
                      {t("automod.serverRefused", { detail: refusal })}
                    </Problem>
                  )}
                </div>

                <div className="flex flex-wrap items-end gap-2">
                  <ActionSelect
                    id={`${idp}-action`}
                    label={t("automod.ruleAction")}
                    value={rule.action}
                    onChange={(a) => patchRule(rule.id, { action: a })}
                  />
                  {/* Only for the action that needs it. A duration beside
                      "Delete" is a field with no effect, and a field with no
                      effect is read as one that has one. */}
                  {rule.action === "timeout" && (
                    <div className="flex flex-col gap-1">
                      <Label htmlFor={`${idp}-timeout`}>
                        {t("automod.ruleTimeoutSeconds")}
                      </Label>
                      <Input
                        id={`${idp}-timeout`}
                        type="number"
                        min={1}
                        className="w-28"
                        value={rule.timeoutSeconds ?? ""}
                        onChange={(e) =>
                          patchRule(rule.id, {
                            timeoutSeconds: Number(e.target.value),
                          })
                        }
                      />
                    </div>
                  )}
                </div>
                {timeout && <Problem>{t(timeout.key)}</Problem>}
              </div>
            );
          })}

          <div className="flex flex-col gap-1">
            <div>
              <Button
                type="button"
                size="sm"
                variant="secondary"
                onClick={() => setRules([...rules, blankRule(nextRuleId(rules))])}
              >
                <Plus className="size-3.5" aria-hidden />
                {t("automod.addRule")}
              </Button>
            </div>
            <span className="text-sm text-muted-foreground">
              {t("automod.allOrNothing")}
            </span>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            {t("automod.modelTitle")}
            <Badge variant={modelStatus(model).tone}>{t(modelStatus(model).key)}</Badge>
          </CardTitle>
          <CardDescription>{t("automod.modelDesc")}</CardDescription>
        </CardHeader>

        <CardContent className="grid gap-4">
          <div className="flex flex-col gap-1">
            <Label htmlFor="automod-model-instruction">
              {t("automod.instruction")}
            </Label>
            {/* A textarea, because this is a prompt. It is the operator's own
                sentence about what counts as abuse in their community, it is
                sent with every message, and a single-line input hides all but
                the first few words of the thing being decided by. Both
                checkers are asked with it. */}
            <Textarea
              id="automod-model-instruction"
              rows={3}
              value={model.instruction}
              onChange={(e) => patchModel({ instruction: e.target.value })}
            />
            <span className="text-sm text-muted-foreground">
              {t("automod.instructionNote")}
            </span>
          </div>

          <section
            role="group"
            aria-label={t("automod.primaryTitle")}
            className="grid gap-2 rounded-md border p-3"
          >
            <h3 className="text-sm font-medium">{t("automod.primaryTitle")}</h3>
            <p className="text-xs text-muted-foreground">{t("automod.primaryDesc")}</p>
            <AutomodModelChecker
              slot="primary"
              value={model}
              onPatch={patchModel}
              presets={presets}
              // The top-level counters are the primary's, on every server
              // version -- an older one sends nothing else.
              stats={stats}
              statsFailed={statsFailed}
              instruction={model.instruction}
              onKeyChanged={() => void loadStats()}
              enableLabel="automod.modelEnabled"
              enableNote="automod.modelEnabledNote"
            />
          </section>

          {secondary && (
            <section
              role="group"
              aria-label={t("automod.secondaryTitle")}
              className="grid gap-2 rounded-md border p-3"
            >
              <h3 className="text-sm font-medium">{t("automod.secondaryTitle")}</h3>
              <p className="text-xs text-muted-foreground">{t("automod.secondaryDesc")}</p>
              {/* Rendered only once asked for, or when it is already on: an
                  optional fallback should not double the length of the card
                  for an operator who will never want one. */}
              {showSecondary ? (
                <AutomodModelChecker
                  slot="secondary"
                  value={secondary}
                  onPatch={patchSecondary}
                  presets={presets}
                  stats={stats?.secondary ?? null}
                  statsFailed={statsFailed}
                  instruction={model.instruction}
                  onKeyChanged={() => void loadStats()}
                  enableLabel="automod.secondaryEnabled"
                  enableNote="automod.modelEnabledNote"
                />
              ) : (
                <div>
                  <Button
                    type="button"
                    size="sm"
                    variant="secondary"
                    onClick={() => setShowSecondary(true)}
                  >
                    {t("automod.secondaryShow")}
                  </Button>
                </div>
              )}
            </section>
          )}

          {/* WHICH CHECKER DECIDED, and how often the fallback had to. */}
          {stats && (stats.fallbacks ?? 0) > 0 && (
            <span className="text-sm text-muted-foreground">
              {t("automod.fallbacks", { count: stats.fallbacks ?? 0 })}
            </span>
          )}
          {stats && (stats.failedOpen ?? 0) > 0 && (
            <Problem>{t("automod.failedOpen", { count: stats.failedOpen ?? 0 })}</Problem>
          )}
          {stats?.lastDecidedBy && (
            <span className="text-sm text-muted-foreground">
              {stats.lastDecidedBy === "secondary"
                ? t("automod.lastDecidedSecondary")
                : t("automod.lastDecidedPrimary")}
            </span>
          )}

          <section
            role="group"
            aria-label={t("automod.categoryTitle")}
            className="grid gap-2 rounded-md border p-3"
          >
            <h3 className="text-sm font-medium">{t("automod.categoryTitle")}</h3>
            <p className="text-xs text-muted-foreground">{t("automod.categoryDesc")}</p>

            <div className="flex flex-col gap-1">
              <ActionSelect
                id="automod-model-action"
                label={t("automod.modelAction")}
                value={model.action}
                onChange={(a) => patchModel({ action: a })}
              />
              <span className="text-sm text-muted-foreground">
                {t("automod.modelActionNote")}
              </span>
            </div>

            {categories.map((cat) => {
              const set = categoryActionOf(model, cat);
              const suggested = SUGGESTED_CATEGORY_ACTIONS[cat];
              const id = `automod-category-${cat}`;
              return (
                <div key={cat} className="flex flex-wrap items-end gap-2">
                  <div className="flex flex-col gap-1">
                    <Label htmlFor={id}>{t(CATEGORY_KEYS[cat] ?? "automod.catOther")}</Label>
                    <select
                      id={id}
                      className="h-8 rounded-md border border-input bg-transparent px-2 text-[12px]"
                      value={set ?? ""}
                      onChange={(e) => setCategoryAction(cat, e.target.value as AutomodAction | "")}
                    >
                      <option value="">
                        {t("automod.categoryDefault", { action: t(ACTION_KEYS[model.action]) })}
                      </option>
                      {(Object.keys(ACTION_KEYS) as AutomodAction[]).map((a) => (
                        <option key={a} value={a}>
                          {t(ACTION_KEYS[a])}
                        </option>
                      ))}
                    </select>
                  </div>
                  {suggested && (
                    <span className="pb-1 text-sm text-muted-foreground">
                      {t("automod.categorySuggested", { action: t(ACTION_KEYS[suggested]) })}
                    </span>
                  )}
                </div>
              );
            })}
            <div>
              <Button
                type="button"
                size="sm"
                variant="secondary"
                onClick={() =>
                  patchModel({
                    categoryActions: { ...model.categoryActions, ...SUGGESTED_CATEGORY_ACTIONS },
                  })
                }
              >
                {t("automod.categoryApplySuggested")}
              </Button>
            </div>

            {/* The two timeouts are not the same timeout, and conflating them
                is a bug this feature has already had: the answer timeout is
                how long to wait for a model, this one is how long a viewer a
                model flags stays silenced. Shown while any action is one. */}
            {anyTimeout(model) && (
              <div className="flex flex-col gap-1">
                <Label htmlFor="automod-model-ban-timeout">{t("automod.banTimeout")}</Label>
                <Input
                  id="automod-model-ban-timeout"
                  type="number"
                  min={1}
                  className="w-28"
                  value={model.timeoutForBan ?? ""}
                  onChange={(e) => patchModel({ timeoutForBan: Number(e.target.value) })}
                />
              </div>
            )}
          </section>
        </CardContent>
      </Card>

      <ConfirmDestructive
        open={deleting !== null}
        onOpenChange={(o) => !o && setDeleting(null)}
        subject={deleting?.name ?? ""}
        title={t("confirm.deleteTitle", { name: deleting?.name ?? "" })}
        // The shared pattern (A18): what stops, what is kept, how far it reaches,
        // and a verb on the button.
        description={`${t("automod.deleteRuleDesc")} ${t("automod.deleteRuleKept")}`}
        consequencesLabel={t("automod.deleteRuleReachLabel")}
        consequences={
          deleting ? [{ label: t("automod.deleteRuleReach"), count: ruleReach(automod, deleting) }] : undefined
        }
        confirmLabel={t("automod.deleteRuleConfirm")}
        onConfirm={() => {
          if (deleting) setRules(rules.filter((r) => r.id !== deleting.id));
          setDeleting(null);
        }}
      />
    </>
  );
}
