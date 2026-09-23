import { useCallback, useEffect, useMemo, useState } from "react";
import { AlertTriangle, ChevronDown, ChevronRight, Power } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import {
  anyIrreversibleArmed,
  armedCount,
  IRREVERSIBLE,
  isOperable,
} from "@/lib/automodArmed";
import { ACTION_KEYS, CHECKER_KEYS, checkerReady } from "@/lib/automodConfig";
import { useT } from "@/lib/i18n";
import type {
  AutomodAction,
  AutomodCell,
  AutomodChecker,
  AutomodMatrixView,
  Settings,
} from "@/lib/types";

/* ===========================================================================
   The automod switch matrix: action x platform x checker.

   Three dimensions rather than one, each earning its place differently:

   - CHECKER, because the same action deserves different trust depending on the
     evidence. A regex hit is deterministic, reproducible and explainable after
     the fact; a model verdict is none of those. Auto-deleting on the first is
     defensible; on the second it is a judgement the operator should make
     knowingly.
   - PLATFORM, because they are not equivalent — only Facebook can hide
     upstream — and because exposure differs per channel. Somebody may automate
     their second-language stream happily and want nothing automatic on the one
     their income depends on.
   - ACTION, because consequence runs from "flagged for review" to "removed
     with no undo".

   Sixty cells is a table nobody reads, so it collapses to a summary line per
   platform and expands on demand, with row and column bulk toggles and two
   kill switches. Mid-incident, unticking fifteen boxes is not a thing anyone
   should have to do.
   =========================================================================== */

/* The action and checker names moved to lib/automodConfig.ts, unchanged.
   AutomodConfig — the card that finally lets an operator write the rule and
   configure the model these cells hand permission to — names the same six
   actions, and a second copy of the vocabulary is precisely how the collapsed
   line and the cells came to disagree. Every visible string on this card is a
   catalogue key; the English values are unchanged, because they are the
   accessible names the behaviour suite matches on. */


export interface AutomodMatrixProps {
  settings: Settings;
  onChange: (next: Settings) => void;
}

export function AutomodMatrix({ settings, onChange }: Readonly<AutomodMatrixProps>) {
  const t = useT();
  const [view, setView] = useState<AutomodMatrixView | null>(null);
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const [error, setError] = useState("");

  const refresh = useCallback(async () => {
    try {
      setView(await api.automodMatrix());
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e));
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const automod = settings.automod;

  /** Cells indexed for lookup, so rendering does not scan the list per cell. */
  const byKey = useMemo(() => {
    const m = new Map<string, AutomodCell>();
    for (const c of view?.cells ?? []) {
      m.set(`${c.platform}/${c.action}/${c.checker}`, c);
    }
    return m;
  }, [view]);

  /** Whether each checker EXISTS on the server, from the draft.
   *
   *  THE ONE THIS CARD WAS MISSING. `view.cells[].available` answers "can this
   *  platform perform this action", which is the only gate the matrix had — so
   *  every cell in the Rules and Model columns was a live switch on an install
   *  with no rules written and the model switched off, which is the default.
   *  An operator could arm "Ban / Model", see it counted as armed, and be
   *  granting a permission to a checker `automod.New` was handed as nil.
   *
   *  From the DRAFT rather than a second fetch, for the reason the armed count
   *  is: the fields that decide this are edited on the same page, in the card
   *  directly below, and a readiness read once at mount would say "no rules"
   *  over a rule the operator has just typed. */
  const readiness = useMemo(() => {
    const m = new Map<AutomodChecker, ReturnType<typeof checkerReady>>();
    for (const c of view?.checkers ?? []) {
      m.set(c, checkerReady(settings.automod, c));
    }
    return m;
  }, [view, settings.automod]);

  /** Whether a cell could ever act: the platform can do it AND the checker
   *  that would ask exists. Both gates, in one place, because the collapsed
   *  line and the cells have to agree about what "armed" means — they did not
   *  once already, and lib/automodArmed.ts exists because of it. */
  const armable = useCallback(
    (key: string) => {
      const checker = key.split("/")[2] as AutomodChecker;
      return (
        (byKey.get(key)?.available ?? false) &&
        (readiness.get(checker)?.ready ?? true)
      );
    },
    [byKey, readiness],
  );

  if (!automod || !view) {
    return (
      <Card>
        <CardHeader>
          <CardTitle>{t("automod.matrixTitle")}</CardTitle>
          <CardDescription>{error || t("common.loading")}</CardDescription>
        </CardHeader>
      </Card>
    );
  }

  /** Writes a cell through to settings. Off DELETES the key rather than storing
   *  false, so the persisted form stays sparse and "absent means off" remains
   *  the only rule a reader needs. */
  function setCell(key: string, auto: boolean) {
    const on = { ...(automod!.on ?? {}) };
    if (auto) on[key] = true;
    else delete on[key];
    onChange({ ...settings, automod: { ...automod!, on } });
  }

  function setPlatformEnabled(platform: string, enabled: boolean) {
    const pe = { ...(automod!.platformEnabled ?? {}) };
    pe[platform] = enabled;
    onChange({ ...settings, automod: { ...automod!, platformEnabled: pe } });
  }

  /** Bulk: every checker for one action on one platform. */
  function setRow(platform: string, action: AutomodAction, auto: boolean) {
    const on = { ...(automod!.on ?? {}) };
    for (const checker of view!.checkers) {
      const key = `${platform}/${action}/${checker}`;
      if (auto && armable(key)) on[key] = true;
      else delete on[key];
    }
    onChange({ ...settings, automod: { ...automod!, on } });
  }

  /** Bulk: every action for one checker on one platform. */
  function setColumn(platform: string, checker: AutomodChecker, auto: boolean) {
    const on = { ...(automod!.on ?? {}) };
    for (const action of view!.actions) {
      const key = `${platform}/${action}/${checker}`;
      if (auto && armable(key)) on[key] = true;
      else delete on[key];
    }
    onChange({ ...settings, automod: { ...automod!, on } });
  }

  const irreversibleArmed = anyIrreversibleArmed({
    platforms: view.platforms,
    actions: view.actions,
    checkers: view.checkers,
    on: automod.on,
    available: armable,
  });

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("automod.matrixTitle")}</CardTitle>
        <CardDescription>{t("automod.matrixDesc")}</CardDescription>
      </CardHeader>

      <CardContent className="grid gap-4">
        {error && (
          <div className="rounded-md border border-destructive/40 bg-destructive/10 p-2 text-xs text-destructive">
            {error}
          </div>
        )}

        {/* The global kill switch, first and obvious. */}
        <div className="flex items-center justify-between rounded-md border p-3">
          <div>
            <Label htmlFor="automod-enabled" className="flex items-center gap-2">
              <Power className="size-4" aria-hidden />
              {t("automod.matrixTitle")}
            </Label>
            <p className="mt-0.5 text-xs text-muted-foreground">
              {t("automod.killSwitchNote")}
            </p>
          </div>
          <Switch
            id="automod-enabled"
            checked={automod.enabled}
            onCheckedChange={(v) =>
              onChange({ ...settings, automod: { ...automod, enabled: v } })
            }
          />
        </div>

        {irreversibleArmed && (
          <div className="flex items-start gap-2 rounded-md border border-warn/40 bg-warn/10 p-2 text-xs">
            <AlertTriangle className="mt-0.5 size-3.5 shrink-0 text-warn" aria-hidden />
            <span>{t("automod.irreversibleArmed")}</span>
          </div>
        )}

        {view.platforms.map((platform) => {
          const platformOn = automod.platformEnabled?.[platform] !== false;
          /* FROM THE DRAFT, not from `view.summary`.
             `view.summary` is the server's count from the one fetch at mount,
             and every other thing on this card — the cells, the irreversible
             banner — reads the draft. So this line could say "nothing
             automatic" directly beneath "An irreversible action is armed", and
             mid-raid could keep reading "5 automatic actions" after everything
             had been disarmed. It is the only thing an operator sees without
             expanding sixty cells, which is exactly why it must not be the
             stale one. Counted by the same rule the server uses, in lib so the
             two cannot drift again. */
          const armed = armedCount({
            platform,
            actions: view.actions,
            checkers: view.checkers,
            on: automod.on,
            available: armable,
          });
          const isOpen = expanded[platform] ?? false;

          return (
            <div key={platform} className="rounded-md border">
              {/* Collapsed: one line per platform. Sixty cells is a table
                  nobody reads. */}
              <div className="flex items-center justify-between gap-2 p-3">
                <button
                  type="button"
                  className="flex min-w-0 items-center gap-2 text-left"
                  onClick={() =>
                    setExpanded((e) => ({ ...e, [platform]: !isOpen }))
                  }
                  aria-expanded={isOpen}
                >
                  {isOpen ? (
                    <ChevronDown className="size-4 shrink-0" aria-hidden />
                  ) : (
                    <ChevronRight className="size-4 shrink-0" aria-hidden />
                  )}
                  <span className="font-medium capitalize">{platform}</span>
                  <span className="text-xs text-muted-foreground">
                    {armed === 0
                      ? t("automod.nothingAutomatic")
                      : armed === 1
                        ? t("automod.armedOne")
                        : t("automod.armedMany", { count: armed })}
                  </span>
                </button>

                {/* Per-platform kill switch. Mid-incident this is what an
                    operator reaches for, so it is on the collapsed row rather
                    than inside the expanded panel. */}
                <div className="flex shrink-0 items-center gap-2">
                  <Label
                    htmlFor={`automod-${platform}`}
                    className="text-xs text-muted-foreground"
                  >
                    {platformOn ? t("automod.platformActive") : t("automod.platformPaused")}
                  </Label>
                  <Switch
                    id={`automod-${platform}`}
                    checked={platformOn}
                    onCheckedChange={(v) => setPlatformEnabled(platform, v)}
                  />
                </div>
              </div>

              {isOpen && (
                <div className="overflow-x-auto border-t p-3">
                  <table className="w-full text-xs">
                    <thead>
                      <tr>
                        <th className="pb-2 text-left font-medium">{t("automod.colAction")}</th>
                        {view.checkers.map((c) => {
                          const ready = readiness.get(c);
                          return (
                            <th key={c} className="pb-2 px-2 font-medium">
                              <div>{t(CHECKER_KEYS[c])}</div>
                              {/* Said once at the top of the column as well as
                                  in every cell, because the cells are what an
                                  operator scans and the column is where the
                                  answer is: nothing here can be switched on
                                  until this checker is configured, below. */}
                              {ready && !ready.ready && (
                                <div className="font-normal text-[10px] text-muted-foreground">
                                  {t(ready.reason)}
                                </div>
                              )}
                              <button
                                type="button"
                                className="mt-0.5 text-[10px] font-normal text-muted-foreground underline"
                                onClick={() => setColumn(platform, c, false)}
                              >
                                {t("automod.clearColumn")}
                              </button>
                            </th>
                          );
                        })}
                      </tr>
                    </thead>
                    <tbody>
                      {view.actions.map((action) => (
                        <tr key={action} className="border-t">
                          <td className="py-2 pr-2">
                            <div className="flex items-center gap-1.5">
                              {t(ACTION_KEYS[action])}
                              {IRREVERSIBLE.includes(action) && (
                                <span
                                  className="rounded bg-warn/15 px-1 text-[10px] text-warn"
                                  title={t("automod.noUndoTitle")}
                                >
                                  {t("automod.noUndo")}
                                </span>
                              )}
                            </div>
                            {/* No "clear row" on a row that cannot be cleared;
                                the sentence that says why, instead. */}
                            {isOperable(action) ? (
                              <button
                                type="button"
                                className="text-[10px] text-muted-foreground underline"
                                onClick={() => setRow(platform, action, false)}
                              >
                                {t("automod.clearRow")}
                              </button>
                            ) : (
                              <div className="text-[10px] text-muted-foreground">
                                {t("automod.flagRecordedOnly")}
                              </div>
                            )}
                          </td>
                          {view.checkers.map((checker) => {
                            const key = `${platform}/${action}/${checker}`;
                            const cell = byKey.get(key);
                            const available = cell?.available ?? false;
                            const ready = readiness.get(checker);
                            return (
                              <td key={checker} className="px-2 py-2 text-center">
                                {!isOperable(action) ? (
                                  /* FIXED TEXT, NOT A SWITCH — a CONTROL, and
                                     the same one this file already applies to
                                     an unavailable cell twelve lines down.

                                     Flagging is recorded before the matrix is
                                     consulted (chat/automod.go's recordAutomod
                                     logs every finding in the verdict) and the
                                     worker returns immediately for the flag
                                     action, so these twelve switches had one
                                     possible outcome each. Rendering them
                                     disabled would be no better: a greyed
                                     switch still says "this could be turned
                                     on", and an operator who turns flagging
                                     "off" to quieten a raid and sees the row
                                     go dark believes something changed.

                                     The server agrees, which is where the
                                     inconsistency showed: Summary excludes
                                     this action from the armed count
                                     (automod/matrix.go:220), so the collapsed
                                     line and these switches were already
                                     disagreeing about what "armed" means. */
                                  <span
                                    className="cursor-help text-[10px] text-muted-foreground"
                                    title={t("automod.alwaysOnTitle")}
                                  >
                                    {t("automod.alwaysOn")}
                                  </span>
                                ) : available && ready && !ready.ready ? (
                                  /* INERT WITH THE REASON, exactly as an
                                     unavailable platform's cell is, and for
                                     the same reason spelled out below it: a
                                     switch that silently does nothing leaves
                                     the operator believing this channel is
                                     protected.

                                     What it protects against here is worse
                                     than that. `automod.New` takes each
                                     checker as a pointer and a nil one
                                     "contributes nothing" -- and nil is the
                                     DEFAULT for two of the three columns, so
                                     "Ban / Model" was a live switch on a
                                     fresh install with no model configured
                                     and no field anywhere to configure one.
                                     Arming it granted permission to
                                     permanently remove a viewer to something
                                     that did not exist, and the collapsed
                                     line counted it as armed.

                                     The platform gate is checked FIRST: when
                                     a platform cannot perform the action at
                                     all, configuring the checker would change
                                     nothing, and "not configured" would send
                                     an operator to fix the wrong thing. */
                                  <span
                                    className="cursor-help text-[10px] text-muted-foreground"
                                    title={t(ready.reason)}
                                  >
                                    {t("automod.notConfigured")}
                                  </span>
                                ) : available ? (
                                  <Switch
                                    checked={Boolean(automod.on?.[key])}
                                    onCheckedChange={(v) => setCell(key, v)}
                                    aria-label={t("automod.cellLabel", {
                                      checker: t(CHECKER_KEYS[checker]),
                                      action: t(ACTION_KEYS[action]),
                                      platform,
                                    })}
                                  />
                                ) : (
                                  /* Inert WITH a reason, never an unticked box.
                                     A switch that silently does nothing leaves
                                     the operator believing this channel is
                                     protected. */
                                  <span
                                    className="cursor-help text-[10px] text-muted-foreground"
                                    title={cell?.reason ?? t("automod.notSupportedHere")}
                                  >
                                    {t("automod.notApplicable")}
                                  </span>
                                )}
                              </td>
                            );
                          })}
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </div>
          );
        })}
      </CardContent>
    </Card>
  );
}
