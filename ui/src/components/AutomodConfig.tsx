import { useCallback, useEffect, useState, type ReactNode } from "react";
import { AlertTriangle, KeyRound, Plus, Trash2 } from "lucide-react";
import { api, ApiError } from "@/lib/api";
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
import { SecretInput } from "@/components/SecretInput";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import {
  ACTION_KEYS,
  confidenceOutOfRange,
  MAX_CONFIDENCE,
  MIN_CONFIDENCE,
  nextRuleId,
  ruleProblems,
  serverRejection,
} from "@/lib/automodConfig";
import { useT } from "@/lib/i18n";
import type {
  AutomodAction,
  AutomodModelStats,
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
    <p className="flex items-start gap-1 text-[10px] text-warn" role="alert">
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
  const [apiKey, setApiKey] = useState("");
  const [replacing, setReplacing] = useState(false);
  const [keyBusy, setKeyBusy] = useState(false);
  const [keyNote, setKeyNote] = useState<string | null>(null);
  const [keyError, setKeyError] = useState<string | null>(null);
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

  function patchModel(patch: Partial<typeof model>) {
    onChange({
      ...settings,
      automod: { ...automod!, model: { ...model, ...patch } },
    });
  }

  /** Stores or clears the key, immediately.
   *
   *  Not part of the page's draft, because it is not part of the settings
   *  document: PUT /settings/automod-key seals it separately so GET /settings
   *  can never carry it. That makes this the one control on the card that acts
   *  before the operator presses Save, which is why it reports its own outcome
   *  rather than leaving them to assume. */
  async function storeKey(value: string) {
    setKeyBusy(true);
    setKeyError(null);
    setKeyNote(null);
    try {
      const res = await api.setAutomodKey(value);
      // The draft is corrected so the card stops offering to set a key that is
      // now set. The server derives hasApiKey on every read, so this cannot
      // outlive a refresh as a lie.
      patchModel({ hasApiKey: res.hasApiKey });
      setApiKey("");
      setReplacing(false);
      setKeyNote(value === "" ? t("automod.keyCleared") : t("automod.keyStored"));
      // The stored key is applied by rebuilding the checker, which resets the
      // per-connector failure counters — so the panel beside it is stale the
      // moment this returns.
      void loadStats();
    } catch (e) {
      setKeyError(
        t("automod.keyFailed", {
          detail: e instanceof ApiError ? e.message : String(e),
        }),
      );
    } finally {
      setKeyBusy(false);
    }
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
                  <span className="text-[10px] text-muted-foreground">
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
            <span className="text-[10px] text-muted-foreground">
              {t("automod.allOrNothing")}
            </span>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t("automod.modelTitle")}</CardTitle>
          <CardDescription>{t("automod.modelDesc")}</CardDescription>
        </CardHeader>

        <CardContent className="grid gap-3">
          <div className="flex items-center justify-between rounded-md border p-3">
            <div>
              <Label htmlFor="automod-model-enabled">
                {t("automod.modelEnabled")}
              </Label>
              <p className="mt-0.5 text-xs text-muted-foreground">
                {t("automod.modelEnabledNote")}
              </p>
            </div>
            <Switch
              id="automod-model-enabled"
              checked={model.enabled}
              onCheckedChange={(v) => patchModel({ enabled: v })}
            />
          </div>

          <div className="grid gap-3 sm:grid-cols-2">
            <div className="flex flex-col gap-1">
              <Label htmlFor="automod-model-endpoint">
                {t("automod.endpoint")}
              </Label>
              <Input
                id="automod-model-endpoint"
                value={model.endpoint}
                spellCheck={false}
                autoComplete="off"
                onChange={(e) => patchModel({ endpoint: e.target.value })}
              />
              <span className="text-[10px] text-muted-foreground">
                {t("automod.endpointNote")}
              </span>
            </div>

            <div className="flex flex-col gap-1">
              <Label htmlFor="automod-model-name">{t("automod.modelName")}</Label>
              <Input
                id="automod-model-name"
                value={model.model}
                spellCheck={false}
                autoComplete="off"
                onChange={(e) => patchModel({ model: e.target.value })}
              />
            </div>
          </div>

          <div className="flex flex-col gap-1">
            <Label htmlFor="automod-model-instruction">
              {t("automod.instruction")}
            </Label>
            {/* A textarea, because this is a prompt. It is the operator's own
                sentence about what counts as abuse in their community, it is
                sent with every message, and a single-line input hides all but
                the first few words of the thing being decided by. */}
            <Textarea
              id="automod-model-instruction"
              rows={3}
              value={model.instruction}
              onChange={(e) => patchModel({ instruction: e.target.value })}
            />
            <span className="text-[10px] text-muted-foreground">
              {t("automod.instructionNote")}
            </span>
          </div>

          <div className="grid gap-3 sm:grid-cols-2">
            <div className="flex flex-col gap-1">
              <Label htmlFor="automod-model-confidence">
                {t("automod.minConfidence")}
              </Label>
              <Input
                id="automod-model-confidence"
                type="number"
                step={0.05}
                min={MIN_CONFIDENCE}
                max={MAX_CONFIDENCE}
                className="w-28"
                value={model.minConfidence}
                aria-invalid={confidenceOutOfRange(model.minConfidence)}
                onChange={(e) =>
                  patchModel({ minConfidence: Number(e.target.value) })
                }
              />
              <span className="text-[10px] text-muted-foreground">
                {t("automod.minConfidenceNote")}
              </span>
              {/* THE ONLY PLACE THIS CAN BE SAID. The server does not refuse a
                  floor outside the scale: it logs, keeps 0.8, and carries on —
                  so a 0 typed here (act on every opinion the model has) or an
                  80 typed as a percentage (act on none of them, ever) is
                  silent everywhere else. */}
              {confidenceOutOfRange(model.minConfidence) && (
                <Problem>{t("automod.confidenceOutOfRange")}</Problem>
              )}
            </div>

            <div className="flex flex-col gap-1">
              <Label htmlFor="automod-model-calls">
                {t("automod.maxCalls")}
              </Label>
              <Input
                id="automod-model-calls"
                type="number"
                min={0}
                className="w-28"
                value={model.maxCallsPerHour}
                onChange={(e) =>
                  patchModel({ maxCallsPerHour: Number(e.target.value) })
                }
              />
              <span className="text-[10px] text-muted-foreground">
                {t("automod.maxCallsNote")}
              </span>
              {/* THE READING, BESIDE THE CEILING IT IS BOUNDED BY. A ceiling
                  with no spend beside it is a number an operator guesses at
                  and never revisits. */}
              {statsFailed && (
                <span className="text-[10px] text-muted-foreground">
                  {t("automod.spendUnread")}
                </span>
              )}
              {stats && (
                <span className="text-[10px] text-muted-foreground">
                  {stats.ceiling > 0
                    ? t("automod.spend", {
                        used: stats.callsThisHour,
                        ceiling: stats.ceiling,
                      })
                    : t("automod.spendNoCeiling", { used: stats.callsThisHour })}
                </span>
              )}
              {stats && stats.failures > 0 && (
                <span className="text-[10px] text-muted-foreground">
                  {t("automod.spendFailures", { count: stats.failures })}
                </span>
              )}
              {stats?.lastError && (
                <Problem>
                  {t("automod.lastError", { detail: stats.lastError })}
                </Problem>
              )}
            </div>
          </div>

          <div className="grid gap-3 sm:grid-cols-2">
            <div className="flex flex-col gap-1">
              <Label htmlFor="automod-model-timeout">
                {t("automod.answerTimeout")}
              </Label>
              <Input
                id="automod-model-timeout"
                type="number"
                min={1}
                className="w-28"
                value={model.timeoutSeconds}
                onChange={(e) =>
                  patchModel({ timeoutSeconds: Number(e.target.value) })
                }
              />
              <span className="text-[10px] text-muted-foreground">
                {t("automod.answerTimeoutNote")}
              </span>
            </div>

            <div className="flex flex-col gap-2">
              <ActionSelect
                id="automod-model-action"
                label={t("automod.modelAction")}
                value={model.action}
                onChange={(a) => patchModel({ action: a })}
              />
              {/* The two timeouts are not the same timeout, and conflating them
                  is a bug this feature has already had: the one above is how
                  long to wait for an answer, this one is how long the viewer
                  the model flags stays silenced. */}
              {model.action === "timeout" && (
                <div className="flex flex-col gap-1">
                  <Label htmlFor="automod-model-ban-timeout">
                    {t("automod.banTimeout")}
                  </Label>
                  <Input
                    id="automod-model-ban-timeout"
                    type="number"
                    min={1}
                    className="w-28"
                    value={model.timeoutForBan ?? ""}
                    onChange={(e) =>
                      patchModel({ timeoutForBan: Number(e.target.value) })
                    }
                  />
                </div>
              )}
            </div>
          </div>

          {/* THE KEY. Never rendered, only replaced. */}
          <div className="flex flex-col gap-2 rounded-md border p-3">
            <div className="flex items-center gap-2">
              <KeyRound className="size-4 shrink-0" aria-hidden />
              <span className="text-xs font-medium">{t("automod.apiKey")}</span>
              <span className="text-xs text-muted-foreground">
                {model.hasApiKey ? t("automod.keySet") : t("automod.keyUnset")}
              </span>
            </div>

            {model.hasApiKey && !replacing ? (
              <div className="flex flex-wrap items-center gap-2">
                <Button
                  type="button"
                  size="sm"
                  variant="secondary"
                  onClick={() => setReplacing(true)}
                >
                  {t("automod.replaceKey")}
                </Button>
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  disabled={keyBusy}
                  onClick={() => void storeKey("")}
                >
                  {t("automod.clearKey")}
                </Button>
              </div>
            ) : (
              <div className="flex flex-wrap items-end gap-2">
                <div className="flex min-w-48 flex-1 flex-col gap-1">
                  <Label htmlFor="automod-model-key">
                    {t("automod.newKey")}
                  </Label>
                  <SecretInput
                    id="automod-model-key"
                    value={apiKey}
                    onChange={(e) => setApiKey(e.target.value)}
                  />
                </div>
                <Button
                  type="button"
                  size="sm"
                  disabled={keyBusy || apiKey.trim() === ""}
                  onClick={() => void storeKey(apiKey.trim())}
                >
                  {t("automod.saveKey")}
                </Button>
                {model.hasApiKey && (
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    onClick={() => {
                      setReplacing(false);
                      setApiKey("");
                    }}
                  >
                    {t("common.cancel")}
                  </Button>
                )}
              </div>
            )}

            <span className="text-[10px] text-muted-foreground">
              {t("automod.keyNeverReturned")}
            </span>
            {keyNote && (
              <span className="text-[10px] text-muted-foreground" role="status">
                {keyNote}
              </span>
            )}
            {keyError && <Problem>{keyError}</Problem>}
          </div>
        </CardContent>
      </Card>

      <ConfirmDestructive
        open={deleting !== null}
        onOpenChange={(o) => !o && setDeleting(null)}
        subject={deleting?.name ?? ""}
        title={t("automod.deleteRuleTitle")}
        description={t("automod.deleteRuleDesc")}
        onConfirm={() => {
          if (deleting) setRules(rules.filter((r) => r.id !== deleting.id));
          setDeleting(null);
        }}
      />
    </>
  );
}
