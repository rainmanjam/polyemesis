import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { Plus, Save, Trash2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { SaveStatus } from "@/components/SaveStatus";
import { api, isSettingsConflict } from "@/lib/api";
import { useT, type TranslationKey } from "@/lib/i18n";
import { platformName } from "@/lib/platforms";
import { useSaveModel } from "@/lib/saveModel";
import type {
  ActivityKindState,
  ActivitySettings as Block,
  ActivitySourcesResponse,
  MinAmount,
  Settings,
} from "@/lib/types";
import {
  ACTIVITY_KINDS,
  activityForSave,
  DEFAULT_ACTIVITY_SETTINGS,
  enabledKinds,
  FALLBACK_PLATFORMS,
  firstMinProblem,
  isCustomised,
  isOverlayKind,
  resetPlatform,
  sameActivity,
  setKind,
  type MinProblem,
} from "./activityLib";
import { TestAlert } from "./TestAlert";

const PROBLEM_COPY: Record<MinProblem, TranslationKey> = {
  currency: "activity.settings.minBadCurrency",
  unit: "activity.settings.minBadUnit",
  incomplete: "activity.settings.minIncomplete",
  both: "activity.settings.minBoth",
};

/** The state a source reports for one kind of one platform. When several
 *  accounts report, a kind that is on for any of them counts as on. */
function reportedState(
  data: ActivitySourcesResponse | null | undefined,
  platform: string,
  kind: string,
): { state: ActivityKindState; detail?: string } | null {
  const found = (data?.sources ?? [])
    .filter((s) => s.platform === platform)
    .flatMap((s) => (s.kinds ?? []).filter((k) => k.kind === kind));
  if (found.length === 0) return null;
  return found.find((k) => k.state === "on") ?? found[0];
}

/** minor units as the currency's major figure, for the hint beside the field. */
function majorHint(m: MinAmount): string {
  if (!m.currency || !/^[A-Za-z]{3}$/.test(m.currency.trim()) || m.minor === undefined) return "";
  try {
    const f = new Intl.NumberFormat(undefined, { style: "currency", currency: m.currency.trim().toUpperCase() });
    // The currency's own minor-unit exponent (yen has none), not always 100.
    return f.format(m.minor / 10 ** (f.resolvedOptions().maximumFractionDigits ?? 2));
  } catch {
    return "";
  }
}

/** Chat -> Activity -> Settings. Loads the settings document itself, edits
 *  only its `activity` block, and saves through the same putSettings path (and
 *  the same version check) as every other section: the whole document goes back,
 *  so a save from a stale tab is refused rather than reverting somebody else. */
export function ActivitySettings({
  sources,
  onSaved,
}: Readonly<{
  /** The source health read, for the kinds list and the reasons. Without it
   *  every platform and kind is still listed, from the built-in set. */
  sources?: ActivitySourcesResponse | null;
  /** Called after a successful save, so the feed can re-read its on/off. */
  onSaved?: () => void;
}>) {
  const t = useT();
  const [settings, setSettings] = useState<Settings | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);

  useEffect(() => {
    let live = true;
    api
      .getSettings()
      .then((s) => live && setSettings(s))
      .catch(() => live && setLoadFailed(true));
    return () => {
      live = false;
    };
  }, []);

  const stored = settings?.activity ?? DEFAULT_ACTIVITY_SETTINGS;

  const model = useSaveModel<Block>({
    stored,
    equal: sameActivity,
    onSave: async (draft) => {
      if (!settings) return;
      try {
        const saved = await api.putSettings({ ...settings, activity: activityForSave(draft, settings.activity) });
        setSettings(saved);
        onSaved?.();
      } catch (err) {
        const msg = err instanceof Error ? err.message : t("activity.settings.saveFailed");
        // A conflict means the document changed under this tab; saving again
        // would conflict again, so it offers a reload instead of a retry.
        if (isSettingsConflict(err)) {
          toast.error(msg, {
            duration: Infinity,
            closeButton: true,
            action: { label: t("chatpage.reload"), onClick: () => window.location.reload() },
          });
        } else {
          toast.error(msg);
        }
        throw err;
      }
    },
  });
  const { draft, setDraft } = model;

  const platforms = useMemo(
    () => (sources?.platforms?.length ? sources.platforms : [...FALLBACK_PLATFORMS]),
    [sources],
  );
  const kinds = useMemo(
    () => (sources?.kinds?.length ? sources.kinds.map((k) => k.kind) : [...ACTIVITY_KINDS]),
    [sources],
  );
  const consoleOnly = (kind: string) => {
    const k = sources?.kinds?.find((x) => x.kind === kind);
    return k ? !k.overlay : !isOverlayKind(kind);
  };

  const problem = firstMinProblem(draft.minTip);
  const setMin = (i: number, patch: Partial<MinAmount>) =>
    setDraft({ ...draft, minTip: (draft.minTip ?? []).map((m, j) => (j === i ? { ...m, ...patch } : m)) });

  if (!settings) {
    return (
      <p className="p-3 text-sm text-muted-foreground" role={loadFailed ? "alert" : "status"}>
        {loadFailed ? t("activity.settings.loadFailed") : t("common.loading")}
      </p>
    );
  }

  const num = (raw: string) => (raw === "" ? undefined : Number(raw));

  return (
    <div className="flex flex-col gap-4 p-3" data-testid="activity-settings">
      <h2 className="text-base font-semibold">{t("activity.settings.title")}</h2>

      <div className="flex items-start justify-between gap-3">
        <div className="flex flex-col gap-0.5">
          <Label htmlFor="activity-enabled">{t("activity.settings.enabled")}</Label>
          <span className="text-sm text-muted-foreground">{t("activity.settings.enabledDesc")}</span>
        </div>
        <Switch
          id="activity-enabled"
          checked={draft.enabled}
          onCheckedChange={(enabled) => setDraft({ ...draft, enabled })}
        />
      </div>

      <section className="flex flex-col gap-2" aria-labelledby="activity-kinds-title">
        <h3 id="activity-kinds-title" className="text-sm font-semibold">
          {t("activity.settings.kindsTitle")}
        </h3>
        <p className="text-sm text-muted-foreground">{t("activity.settings.kindsHint")}</p>
        {platforms.map((p) => {
          const on = new Set(enabledKinds(draft.kinds, p, kinds));
          const custom = isCustomised(draft.kinds, p);
          return (
            <fieldset key={p} className="flex flex-col gap-1 rounded-md border border-border p-2">
              <legend className="flex items-center gap-2 px-1 text-sm font-semibold">
                {platformName(p)}
                <Badge variant={custom ? "default" : "outline"}>
                  {t(custom ? "activity.settings.customised" : "activity.settings.defaults")}
                </Badge>
              </legend>
              {custom && (
                <div>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => setDraft({ ...draft, kinds: resetPlatform(draft.kinds, p) })}
                  >
                    {t("activity.settings.resetDefaults")}
                  </Button>
                </div>
              )}
              <ul className="grid gap-x-4 gap-y-1 sm:grid-cols-2">
                {kinds.map((k) => {
                  const rep = reportedState(sources, p, k);
                  const blocked = rep && (rep.state === "unavailable" || rep.state === "reconnect");
                  return (
                    <li key={k} className="flex items-start gap-2 text-sm">
                      <Switch
                        id={`activity-kind-${p}-${k}`}
                        checked={on.has(k)}
                        onCheckedChange={(v) => setDraft({ ...draft, kinds: setKind(draft.kinds, p, k, v, kinds) })}
                        aria-label={t("activity.settings.kindAria", {
                          kind: t(`activity.kind.${k as (typeof ACTIVITY_KINDS)[number]}`),
                          platform: platformName(p),
                        })}
                      />
                      <span className={blocked ? "text-muted-foreground" : undefined}>
                        {t(`activity.kind.${k as (typeof ACTIVITY_KINDS)[number]}`)}
                        {consoleOnly(k) && (
                          <span className="ml-1 text-muted-foreground">({t("activity.settings.consoleOnly")})</span>
                        )}
                        {rep && rep.state !== "on" && (
                          <span className="block text-muted-foreground">
                            {t(`activity.kindState.${rep.state}`)}
                            {rep.detail ? `: ${rep.detail}` : ""}
                          </span>
                        )}
                      </span>
                    </li>
                  );
                })}
              </ul>
            </fieldset>
          );
        })}
      </section>

      <section className="flex flex-col gap-2" aria-labelledby="activity-min-title">
        <h3 id="activity-min-title" className="text-sm font-semibold">
          {t("activity.settings.minTitle")}
        </h3>
        <p className="text-sm text-muted-foreground">{t("activity.settings.minHint")}</p>
        {(draft.minTip ?? []).map((m, i) => {
          const money = m.currency !== undefined || m.minor !== undefined;
          const unit = m.unit !== undefined || m.units !== undefined;
          return (
            <div key={i} className="flex flex-wrap items-end gap-2">
              {money && (
                <>
                  <div className="flex flex-col gap-1">
                    <Label htmlFor={`activity-min-${i}-currency`}>{t("activity.settings.minCurrency")}</Label>
                    <Input
                      id={`activity-min-${i}-currency`}
                      className="w-28"
                      value={m.currency ?? ""}
                      onChange={(e) => setMin(i, { currency: e.target.value })}
                    />
                  </div>
                  <div className="flex flex-col gap-1">
                    <Label htmlFor={`activity-min-${i}-minor`}>{t("activity.settings.minMinor")}</Label>
                    <Input
                      id={`activity-min-${i}-minor`}
                      type="number"
                      min={0}
                      className="w-40"
                      value={m.minor ?? ""}
                      onChange={(e) => setMin(i, { minor: num(e.target.value) })}
                    />
                  </div>
                  <span aria-hidden className="tnum pb-1.5 font-mono text-sm text-muted-foreground">
                    {majorHint(m)}
                  </span>
                </>
              )}
              {unit && (
                <>
                  <div className="flex flex-col gap-1">
                    <Label htmlFor={`activity-min-${i}-unit`}>{t("activity.settings.minUnit")}</Label>
                    <Input
                      id={`activity-min-${i}-unit`}
                      className="w-28"
                      value={m.unit ?? ""}
                      onChange={(e) => setMin(i, { unit: e.target.value })}
                    />
                  </div>
                  <div className="flex flex-col gap-1">
                    <Label htmlFor={`activity-min-${i}-units`}>{t("activity.settings.minUnits")}</Label>
                    <Input
                      id={`activity-min-${i}-units`}
                      type="number"
                      min={0}
                      className="w-40"
                      value={m.units ?? ""}
                      onChange={(e) => setMin(i, { units: num(e.target.value) })}
                    />
                  </div>
                </>
              )}
              <Button
                size="icon-sm"
                variant="ghost"
                aria-label={t("activity.settings.minRemove")}
                title={t("activity.settings.minRemove")}
                onClick={() => setDraft({ ...draft, minTip: (draft.minTip ?? []).filter((_, j) => j !== i) })}
              >
                <Trash2 />
              </Button>
            </div>
          );
        })}
        <div className="flex flex-wrap gap-2">
          <Button
            size="sm"
            variant="outline"
            onClick={() => setDraft({ ...draft, minTip: [...(draft.minTip ?? []), { currency: "" }] })}
          >
            <Plus /> {t("activity.settings.addMoney")}
          </Button>
          <Button
            size="sm"
            variant="outline"
            onClick={() => setDraft({ ...draft, minTip: [...(draft.minTip ?? []), { unit: "" }] })}
          >
            <Plus /> {t("activity.settings.addUnit")}
          </Button>
        </div>
      </section>

      <div className="flex items-start justify-between gap-3">
        <div className="flex flex-col gap-0.5">
          <Label htmlFor="activity-overlay-messages">{t("activity.settings.overlayMessages")}</Label>
          <span className="text-sm text-muted-foreground">{t("activity.settings.overlayMessagesDesc")}</span>
        </div>
        <Switch
          id="activity-overlay-messages"
          checked={draft.overlayMessages}
          onCheckedChange={(overlayMessages) => setDraft({ ...draft, overlayMessages })}
        />
      </div>

      <div className="flex items-start justify-between gap-3">
        <div className="flex flex-col gap-0.5">
          <Label htmlFor="activity-reactions">{t("activity.settings.reactions")}</Label>
          <span className="text-sm text-muted-foreground">{t("activity.settings.reactionsDesc")}</span>
        </div>
        <Switch
          id="activity-reactions"
          checked={draft.reactions}
          onCheckedChange={(reactions) => setDraft({ ...draft, reactions })}
        />
      </div>

      <div className="flex flex-wrap items-center gap-3">
        <Button size="sm" onClick={() => void model.save()} disabled={!model.dirty || model.state === "saving" || !!problem}>
          <Save /> {t("activity.settings.save")}
        </Button>
        <SaveStatus state={model.state} />
        {problem && <span className="text-sm text-warn">{t(PROBLEM_COPY[problem.problem])}</span>}
      </div>

      <section className="flex flex-col gap-2 rounded-md border border-border p-3" aria-labelledby="activity-test-title">
        <h3 id="activity-test-title" className="text-sm font-semibold">
          {t("activity.test.title")}
        </h3>
        <p className="text-sm text-muted-foreground">{t("activity.test.desc")}</p>
        <TestAlert platforms={platforms} idPrefix="activity-settings-test" />
      </section>
    </div>
  );
}
