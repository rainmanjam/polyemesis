import { useState, type ReactNode } from "react";
import { AlertTriangle, KeyRound, ShieldCheck } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { SecretInput } from "@/components/SecretInput";
import { Switch } from "@/components/ui/switch";
import {
  CATEGORY_KEYS,
  confidenceOutOfRange,
  connectionOf,
  isCustomPreset,
  MAX_CONFIDENCE,
  MIN_CONFIDENCE,
  presetOf,
  presetsFor,
  PRIVACY_KEYS,
  TEST_CASE_KEYS,
  testVerdictKey,
} from "@/lib/automodConfig";
import { useT, type TranslationKey } from "@/lib/i18n";
import type {
  AutomodModel,
  AutomodModelStats,
  AutomodPreset,
  AutomodProtocol,
  AutomodSlot,
  AutomodTestResult,
} from "@/lib/types";

/* ===========================================================================
   One model checker's connection: protocol, provider, address, model, budget,
   key, and the Test button that has to pass before it can be switched on.

   Rendered twice by AutomodConfig -- the primary, and the optional fallback
   asked only when the primary cannot answer. Everything here is per checker;
   the POLICY both apply (the instruction, the actions) stays on the card that
   holds them both, because the fallback is a second connection to ask and not
   a second opinion about what to do.

   THE SWITCH IS LOCKED UNTIL A TEST HAS PASSED FOR WHAT IS ON SCREEN. A checker
   that cannot answer fails every message open, and from the outside that is
   indistinguishable from a model that approves of everything. The server
   enforces the same rule (newlyEnabledUntested); this lock is the courtesy that
   says so before the save does.

   THE KEY GOES ONLY WHERE IT WAS GIVEN. The server sends a stored key to the
   saved address and nowhere else, so testing a new address needs its key typed
   here -- and a key that passes is sealed with the pass.
   =========================================================================== */

export interface AutomodModelCheckerProps {
  slot: AutomodSlot;
  value: AutomodModel;
  onPatch: (patch: Partial<AutomodModel>) => void;
  presets: readonly AutomodPreset[];
  /** This checker's own counters, or null when unread. */
  stats: AutomodModelStats | null;
  statsFailed: boolean;
  /** The instruction on screen, so the test asks what the checker will. */
  instruction: string;
  /** Called after the key changes, so the stats beside it are re-read. */
  onKeyChanged: () => void;
  enableLabel: TranslationKey;
  enableNote: TranslationKey;
}

function Problem({ children }: Readonly<{ children: ReactNode }>) {
  return (
    <p className="flex items-start gap-1 text-sm text-warn" role="alert">
      <AlertTriangle className="mt-0.5 size-3 shrink-0" aria-hidden />
      <span>{children}</span>
    </p>
  );
}

const PROTOCOL_KEYS: Record<AutomodProtocol, TranslationKey> = {
  jev: "automod.protocolJev",
  openai: "automod.protocolOpenAI",
};

export function AutomodModelChecker({
  slot,
  value,
  onPatch,
  presets,
  stats,
  statsFailed,
  instruction,
  onKeyChanged,
  enableLabel,
  enableNote,
}: Readonly<AutomodModelCheckerProps>) {
  const t = useT();
  // The primary keeps the ids it has always had, so links and tests that name
  // them still land.
  const idp = slot === "primary" ? "automod-model" : "automod-secondary";
  const protocol: AutomodProtocol = value.protocol ?? "openai";
  const preset = presetOf(presets, value);

  const [apiKey, setApiKey] = useState("");
  const [replacing, setReplacing] = useState(false);
  const [keyBusy, setKeyBusy] = useState(false);
  const [keyNote, setKeyNote] = useState<string | null>(null);
  const [keyError, setKeyError] = useState<string | null>(null);
  const [testBusy, setTestBusy] = useState(false);
  const [testResult, setTestResult] = useState<AutomodTestResult | null>(null);
  const [testError, setTestError] = useState<string | null>(null);
  const [keyPending, setKeyPending] = useState(false);
  // The address the stored key was given for, as the page loaded it: the
  // server sends the key nowhere else (db.NormalizeEndpoint binding).
  const [savedEndpoint] = useState(value.endpoint);
  // The connection a Test last passed for: the server's word at load, then
  // this page's own runs.
  const [testedConn, setTestedConn] = useState<string | null>(
    value.tested ? connectionOf(value) : null,
  );
  const conn = connectionOf(value);
  const testedNow = testedConn === conn;
  const canEnable = value.enabled || testedNow;

  async function storeKey(next: string) {
    setKeyBusy(true);
    setKeyError(null);
    setKeyNote(null);
    try {
      const res =
        slot === "primary"
          ? await api.setAutomodKey(next)
          : await api.setAutomodKey(next, slot);
      onPatch({ hasApiKey: res.hasApiKey });
      setApiKey("");
      setReplacing(false);
      // A new key -- or none -- has not been tested.
      setTestedConn(null);
      setKeyNote(next === "" ? t("automod.keyCleared") : t("automod.keyStored"));
      onKeyChanged();
    } catch (e) {
      setKeyError(
        t("automod.keyFailed", { detail: e instanceof ApiError ? e.message : String(e) }),
      );
    } finally {
      setKeyBusy(false);
    }
  }

  async function runTest() {
    setTestBusy(true);
    setTestError(null);
    setTestResult(null);
    const typed = apiKey.trim();
    try {
      const res = await api.testAutomodChecker({
        ...(typed === "" ? {} : { key: typed }),
        checker: slot,
        protocol,
        preset: value.preset ?? "",
        endpoint: value.endpoint,
        model: value.model,
        timeoutSeconds: value.timeoutSeconds,
        minConfidence: value.minConfidence,
        instruction,
      });
      setTestResult(res);
      if (res.tested) {
        setTestedConn(conn);
        if (typed !== "") {
          // The server kept the key that passed -- as this checker's key when
          // the address is the saved one, otherwise held until it is saved.
          // The field is emptied either way so the key is not left on screen.
          if (res.keyPending) setKeyPending(true);
          else onPatch({ hasApiKey: true });
          setApiKey("");
          setReplacing(false);
        }
      }
    } catch (e) {
      setTestError(
        t("automod.testFailedRequest", {
          detail: e instanceof ApiError ? e.message : String(e),
        }),
      );
    } finally {
      setTestBusy(false);
    }
  }

  function choosePreset(p: AutomodPreset) {
    onPatch({
      protocol: p.protocol,
      preset: p.id,
      // A Custom preset keeps whatever address is typed; a provider's fills
      // its own in.
      ...(p.endpoint === "" ? {} : { endpoint: p.endpoint }),
      ...(p.model === "" ? {} : { model: p.model }),
    });
  }

  function chooseProtocol(next: AutomodProtocol) {
    const first = presetsFor(presets, next)[0];
    if (first) choosePreset(first);
    else onPatch({ protocol: next });
  }

  const presetLabel = (p: AutomodPreset) =>
    isCustomPreset(p) ? t("automod.presetCustom") : p.name;

  return (
    <div className="grid gap-3">
      <div className="flex items-center justify-between rounded-md border p-3">
        <div>
          <Label htmlFor={`${idp}-enabled`}>{t(enableLabel)}</Label>
          <p className="mt-0.5 text-xs text-muted-foreground">{t(enableNote)}</p>
          {!canEnable && (
            <p className="mt-0.5 text-xs text-muted-foreground">{t("automod.testNeeded")}</p>
          )}
          {value.enabled && testedConn !== null && !testedNow && (
            <Problem>{t("automod.testRetestNeeded")}</Problem>
          )}
        </div>
        <Switch
          id={`${idp}-enabled`}
          checked={value.enabled}
          disabled={!canEnable}
          onCheckedChange={(v) => onPatch({ enabled: v })}
        />
      </div>

      <div className="grid gap-3 sm:grid-cols-2">
        <div className="flex flex-col gap-1">
          <Label htmlFor={`${idp}-protocol`}>{t("automod.protocol")}</Label>
          <select
            id={`${idp}-protocol`}
            className="h-8 rounded-md border border-input bg-transparent px-2 text-[12px]"
            value={protocol}
            onChange={(e) => chooseProtocol(e.target.value as AutomodProtocol)}
          >
            {(Object.keys(PROTOCOL_KEYS) as AutomodProtocol[]).map((p) => (
              <option key={p} value={p}>
                {t(PROTOCOL_KEYS[p])}
              </option>
            ))}
          </select>
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor={`${idp}-preset`}>{t("automod.preset")}</Label>
          <select
            id={`${idp}-preset`}
            className="h-8 rounded-md border border-input bg-transparent px-2 text-[12px]"
            value={value.preset ?? ""}
            onChange={(e) => {
              const p = presets.find((x) => x.id === e.target.value);
              if (p) choosePreset(p);
            }}
          >
            {preset === undefined && <option value={value.preset ?? ""}>{value.preset ?? ""}</option>}
            {presetsFor(presets, protocol).map((p) => (
              <option key={p.id} value={p.id}>
                {presetLabel(p)}
              </option>
            ))}
          </select>
        </div>
      </div>

      {/* WHERE MESSAGES GO, on the card and not in the docs: a hosted preset is
          a transfer of viewers' words to a third party, and for an operator in
          the EU an international one. */}
      {preset && (
        <p className="flex items-start gap-1 text-sm text-muted-foreground">
          <ShieldCheck className="mt-0.5 size-3.5 shrink-0" aria-hidden />
          <span>{t(PRIVACY_KEYS[preset.privacy], { provider: preset.name })}</span>
        </p>
      )}
      {preset?.local && (
        <>
          <p className="text-sm text-muted-foreground">{t("automod.localFlagOnly")}</p>
          <Problem>{t("automod.localCpuWarning")}</Problem>
        </>
      )}

      <div className="grid gap-3 sm:grid-cols-2">
        <div className="flex flex-col gap-1">
          <Label htmlFor={`${idp}-endpoint`}>{t("automod.endpoint")}</Label>
          <Input
            id={`${idp}-endpoint`}
            value={value.endpoint}
            spellCheck={false}
            autoComplete="off"
            onChange={(e) => onPatch({ endpoint: e.target.value })}
          />
          <span className="text-sm text-muted-foreground">{t("automod.endpointNote")}</span>
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor={`${idp}-name`}>{t("automod.modelName")}</Label>
          <Input
            id={`${idp}-name`}
            value={value.model}
            spellCheck={false}
            autoComplete="off"
            onChange={(e) => onPatch({ model: e.target.value })}
          />
        </div>
      </div>

      <div className="grid gap-3 sm:grid-cols-3">
        <div className="flex flex-col gap-1">
          <Label htmlFor={`${idp}-confidence`}>{t("automod.minConfidence")}</Label>
          <Input
            id={`${idp}-confidence`}
            type="number"
            step={0.05}
            min={MIN_CONFIDENCE}
            max={MAX_CONFIDENCE}
            className="w-28"
            value={value.minConfidence}
            aria-invalid={confidenceOutOfRange(value.minConfidence)}
            onChange={(e) => onPatch({ minConfidence: Number(e.target.value) })}
          />
          <span className="text-sm text-muted-foreground">{t("automod.minConfidenceNote")}</span>
          {confidenceOutOfRange(value.minConfidence) && (
            <Problem>{t("automod.confidenceOutOfRange")}</Problem>
          )}
        </div>

        <div className="flex flex-col gap-1">
          <Label htmlFor={`${idp}-calls`}>{t("automod.maxCalls")}</Label>
          <Input
            id={`${idp}-calls`}
            type="number"
            min={0}
            className="w-28"
            value={value.maxCallsPerHour}
            onChange={(e) => onPatch({ maxCallsPerHour: Number(e.target.value) })}
          />
          <span className="text-sm text-muted-foreground">{t("automod.maxCallsNote")}</span>
          {/* THE READING, BESIDE THE CEILING IT IS BOUNDED BY -- this
              checker's own, because each has its own budget. */}
          {statsFailed && (
            <span className="text-sm text-muted-foreground">{t("automod.spendUnread")}</span>
          )}
          {stats && (
            <span className="text-sm text-muted-foreground">
              {stats.ceiling > 0
                ? t("automod.spend", { used: stats.callsThisHour, ceiling: stats.ceiling })
                : t("automod.spendNoCeiling", { used: stats.callsThisHour })}
            </span>
          )}
          {stats && (stats.decisions ?? 0) > 0 && (
            <span className="text-sm text-muted-foreground">
              {t("automod.decisions", { count: stats.decisions ?? 0 })}
            </span>
          )}
          {stats && stats.failures > 0 && (
            <span className="text-sm text-muted-foreground">
              {t("automod.spendFailures", { count: stats.failures })}
            </span>
          )}
          {stats?.lastError && <Problem>{t("automod.lastError", { detail: stats.lastError })}</Problem>}
        </div>

        <div className="flex flex-col gap-1">
          <Label htmlFor={`${idp}-timeout`}>{t("automod.answerTimeout")}</Label>
          <Input
            id={`${idp}-timeout`}
            type="number"
            min={1}
            className="w-28"
            value={value.timeoutSeconds}
            onChange={(e) => onPatch({ timeoutSeconds: Number(e.target.value) })}
          />
          <span className="text-sm text-muted-foreground">{t("automod.answerTimeoutNote")}</span>
        </div>
      </div>

      {/* THE KEY. Never rendered, only replaced. */}
      <div className="flex flex-col gap-2 rounded-md border p-3">
        <div className="flex items-center gap-2">
          <KeyRound className="size-4 shrink-0" aria-hidden />
          <span className="text-xs font-medium">{t("automod.apiKey")}</span>
          <span className="text-xs text-muted-foreground">
            {value.hasApiKey ? t("automod.keySet") : t("automod.keyUnset")}
          </span>
        </div>
        {value.hasApiKey && !replacing ? (
          <div className="flex flex-wrap items-center gap-2">
            <Button type="button" size="sm" variant="secondary" onClick={() => setReplacing(true)}>
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
              <Label htmlFor={`${idp}-key`}>{t("automod.newKey")}</Label>
              <SecretInput
                id={`${idp}-key`}
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
            {value.hasApiKey && (
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
        <span className="text-sm text-muted-foreground">{t("automod.keyNeverReturned")}</span>
        {value.hasApiKey && value.endpoint.trim() !== savedEndpoint.trim() && (
          <Problem>{t("automod.keyBoundNote")}</Problem>
        )}
        {keyPending && (
          <span className="text-sm text-muted-foreground" role="status">
            {t("automod.keyPending")}
          </span>
        )}
        {keyNote && (
          <span className="text-sm text-muted-foreground" role="status">
            {keyNote}
          </span>
        )}
        {keyError && <Problem>{keyError}</Problem>}
      </div>

      <div className="flex flex-col gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <Button
            type="button"
            size="sm"
            variant="secondary"
            disabled={testBusy || value.endpoint.trim() === ""}
            onClick={() => void runTest()}
          >
            {testBusy ? t("automod.testRunning") : t("automod.testRun")}
          </Button>
          <span className="text-sm text-muted-foreground">{t("automod.testSynthetic")}</span>
        </div>
        {testError && <Problem>{testError}</Problem>}
        {testResult && (
          <div className="flex flex-col gap-0.5" role="status">
            {testResult.passed ? (
              <span className="text-sm">{t(testVerdictKey(testResult))}</span>
            ) : (
              <Problem>{t(testVerdictKey(testResult))}</Problem>
            )}
            {testResult.cases.map((c) => {
              const name = t(TEST_CASE_KEYS[c.case] ?? "automod.testCaseSpam");
              let line: string;
              if (!c.ok) line = t("automod.testCaseError", { case: name, detail: c.error ?? "" });
              else if (c.flagged)
                line = t("automod.testCaseFlagged", {
                  case: name,
                  category: c.category ? t(CATEGORY_KEYS[c.category] ?? "automod.catOther") : "",
                  ms: c.latencyMs,
                });
              else line = t("automod.testCaseClear", { case: name, ms: c.latencyMs });
              return (
                <span key={c.case} className="text-sm text-muted-foreground">
                  {line}
                </span>
              );
            })}
          </div>
        )}
      </div>
    </div>
  );
}
