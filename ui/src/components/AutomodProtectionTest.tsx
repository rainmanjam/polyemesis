import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api, ApiError } from "@/lib/api";
import { ACTION_KEYS, CATEGORY_KEYS } from "@/lib/automodConfig";
import { detectorLabelKey, TIER_KEYS } from "@/lib/automodProtection";
import { useT } from "@/lib/i18n";
import type { AutomodExplanation } from "@/lib/types";

/* THE TEST BOX.
 *
 * Type a pretend message, optionally say who "sent" it and on which platform,
 * and see which detectors would fire, whether the line would be held off the
 * overlays, and what each would ask a platform to do. It calls
 * POST /automod/explain, which is offline, stateless and unlogged: it never
 * counts a strike and never reaches a network. The text is kept in this
 * component's state only, and the answer names detectors, never the matched
 * value.
 *
 * It explains the SAVED settings, because that is what the server holds; the
 * note under the button says so, so an unsaved switch is not mistaken for one
 * that has been tested. */

const MAX_RUNES = 2000;

export function AutomodProtectionTest({ platforms }: Readonly<{ platforms: readonly string[] }>) {
  const t = useT();
  const [text, setText] = useState("");
  const [name, setName] = useState("");
  const [platform, setPlatform] = useState(platforms[0] ?? "twitch");
  const [result, setResult] = useState<AutomodExplanation | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function run() {
    setBusy(true);
    setError("");
    try {
      setResult(
        await api.automodExplain({
          text,
          platform,
          ...(name.trim() ? { authorName: name.trim() } : {}),
        }),
      );
    } catch (e) {
      setResult(null);
      setError(e instanceof ApiError ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  const over = [...text].length > MAX_RUNES;

  return (
    <section aria-labelledby="automod-test-title" className="grid gap-2 rounded-md border p-3">
      <div>
        <h3 id="automod-test-title" className="text-sm font-medium">
          {t("automod.protTestTitle")}
        </h3>
        <p className="mt-0.5 text-xs text-muted-foreground">{t("automod.protTestDesc")}</p>
      </div>
      <div className="flex flex-col gap-1">
        <Label htmlFor="automod-test-text">{t("automod.protTestText")}</Label>
        <Input id="automod-test-text" value={text} aria-invalid={over} onChange={(e) => setText(e.target.value)} />
      </div>
      <div className="flex flex-wrap items-end gap-2">
        <div className="flex flex-col gap-1">
          <Label htmlFor="automod-test-name">{t("automod.protTestName")}</Label>
          <Input id="automod-test-name" value={name} onChange={(e) => setName(e.target.value)} />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="automod-test-platform">{t("automod.protTestPlatform")}</Label>
          <select
            id="automod-test-platform"
            className="h-9 rounded-md border border-input bg-transparent px-2 text-[12px]"
            value={platform}
            onChange={(e) => setPlatform(e.target.value)}
          >
            {platforms.map((p) => (
              <option key={p} value={p}>
                {p}
              </option>
            ))}
          </select>
        </div>
        <Button type="button" size="sm" disabled={busy || text.trim() === "" || over} onClick={() => void run()}>
          {t("automod.protTestRun")}
        </Button>
      </div>
      <p className="text-sm text-muted-foreground">{t("automod.protTestSaved")}</p>

      {error && (
        <div role="alert" className="rounded-md border border-destructive/40 bg-destructive/10 p-2 text-xs text-destructive">
          {t("automod.protTestFailed", { message: error })}
        </div>
      )}

      {result && (
        <div className="grid gap-1.5" aria-live="polite">
          {!result.protection && (
            <p className="text-xs text-warn">{t("automod.protTestPackOff")}</p>
          )}
          <p className="text-sm font-medium">
            {result.held
              ? t("automod.protTestHeld")
              : result.wouldHoldIfOn
                ? t("automod.protTestHeldIfOn")
                : t("automod.protTestNotHeld")}
          </p>
          {result.detections.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t("automod.protTestNothing")}</p>
          ) : (
            <ul className="grid gap-1">
              {result.detections.map((d) => {
                const catKey = CATEGORY_KEYS[d.category];
                const detKey = detectorLabelKey(d.detector);
                const actKey = ACTION_KEYS[d.action];
                return (
                  <li key={`${d.checker}:${d.detector}:${d.reason}`} className="rounded-md border p-2 text-sm">
                    <div className="font-medium">
                      {catKey ? t(catKey) : d.category}
                      {" · "}
                      {detKey ? t(detKey) : d.detector}
                    </div>
                    <div className="text-muted-foreground">
                      {d.hold ? t("automod.protTestDetHolds") : t("automod.protTestDetFlags")}
                      {d.tier ? ` · ${t("automod.protTestTier", { tier: TIER_KEYS[d.tier] ? t(TIER_KEYS[d.tier]) : d.tier })}` : ""}
                      {d.form ? ` · ${t("automod.protTestForm", { form: d.form })}` : ""}
                      {d.act && actKey ? ` · ${t("automod.protTestAct", { action: t(actKey) })}` : ""}
                      {d.live ? "" : ` · ${t("automod.protTestNotLive")}`}
                    </div>
                  </li>
                );
              })}
            </ul>
          )}
          <p className="text-sm text-muted-foreground">{t("automod.protTestWindowed")}</p>
        </div>
      )}
    </section>
  );
}
