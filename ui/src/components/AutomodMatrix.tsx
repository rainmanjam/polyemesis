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

const ACTION_LABELS: Record<AutomodAction, string> = {
  flag: "Flag for review",
  hide_local: "Hide (local only)",
  hide: "Hide (upstream)",
  delete: "Delete",
  timeout: "Time out",
  ban: "Ban",
};

const CHECKER_LABELS: Record<AutomodChecker, string> = {
  rules: "Rules",
  history: "History",
  model: "Model",
};

/** Actions with no undo. They get a warning when armed, because the poka-yoke
 *  rule this project holds is that friction should be proportional to
 *  consequence — and these are where consequence stops being recoverable. */
const IRREVERSIBLE: AutomodAction[] = ["delete", "ban"];

export interface AutomodMatrixProps {
  settings: Settings;
  onChange: (next: Settings) => void;
}

export function AutomodMatrix({ settings, onChange }: Readonly<AutomodMatrixProps>) {
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

  if (!automod || !view) {
    return (
      <Card>
        <CardHeader>
          <CardTitle>Automatic moderation</CardTitle>
          <CardDescription>{error || "Loading…"}</CardDescription>
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
      if (auto && byKey.get(key)?.available) on[key] = true;
      else delete on[key];
    }
    onChange({ ...settings, automod: { ...automod!, on } });
  }

  /** Bulk: every action for one checker on one platform. */
  function setColumn(platform: string, checker: AutomodChecker, auto: boolean) {
    const on = { ...(automod!.on ?? {}) };
    for (const action of view!.actions) {
      const key = `${platform}/${action}/${checker}`;
      if (auto && byKey.get(key)?.available) on[key] = true;
      else delete on[key];
    }
    onChange({ ...settings, automod: { ...automod!, on } });
  }

  const anyIrreversibleArmed = view.actions.some(
    (a) =>
      IRREVERSIBLE.includes(a) &&
      view.platforms.some((p) =>
        view.checkers.some((c) => automod.on?.[`${p}/${a}/${c}`]),
      ),
  );

  return (
    <Card>
      <CardHeader>
        <CardTitle>Automatic moderation</CardTitle>
        <CardDescription>
          What each checker may do, on each platform, without asking. Everything
          starts off except flagging — nothing here acts until you switch it on.
        </CardDescription>
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
              Automatic moderation
            </Label>
            <p className="mt-0.5 text-xs text-muted-foreground">
              Off stops every automatic action everywhere, whatever the cells
              below say. Messages are still flagged for review.
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

        {anyIrreversibleArmed && (
          <div className="flex items-start gap-2 rounded-md border border-warn/40 bg-warn/10 p-2 text-xs">
            <AlertTriangle className="mt-0.5 size-3.5 shrink-0 text-warn" aria-hidden />
            <span>
              An irreversible action is armed. Deleting a message and banning a
              viewer cannot be undone by polyemesis — a ban needs a human to
              lift it, and a deleted message is gone from the platform.
            </span>
          </div>
        )}

        {view.platforms.map((platform) => {
          const platformOn = automod.platformEnabled?.[platform] !== false;
          const armed = view.summary[platform] ?? 0;
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
                      ? "nothing automatic"
                      : `${armed} automatic action${armed === 1 ? "" : "s"}`}
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
                    {platformOn ? "Active" : "Paused"}
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
                        <th className="pb-2 text-left font-medium">Action</th>
                        {view.checkers.map((c) => (
                          <th key={c} className="pb-2 px-2 font-medium">
                            <div>{CHECKER_LABELS[c]}</div>
                            <button
                              type="button"
                              className="mt-0.5 text-[10px] font-normal text-muted-foreground underline"
                              onClick={() => setColumn(platform, c, false)}
                            >
                              clear
                            </button>
                          </th>
                        ))}
                      </tr>
                    </thead>
                    <tbody>
                      {view.actions.map((action) => (
                        <tr key={action} className="border-t">
                          <td className="py-2 pr-2">
                            <div className="flex items-center gap-1.5">
                              {ACTION_LABELS[action]}
                              {IRREVERSIBLE.includes(action) && (
                                <span
                                  className="rounded bg-warn/15 px-1 text-[10px] text-warn"
                                  title="This cannot be undone by polyemesis"
                                >
                                  no undo
                                </span>
                              )}
                            </div>
                            <button
                              type="button"
                              className="text-[10px] text-muted-foreground underline"
                              onClick={() => setRow(platform, action, false)}
                            >
                              clear row
                            </button>
                          </td>
                          {view.checkers.map((checker) => {
                            const key = `${platform}/${action}/${checker}`;
                            const cell = byKey.get(key);
                            const available = cell?.available ?? false;
                            return (
                              <td key={checker} className="px-2 py-2 text-center">
                                {available ? (
                                  <Switch
                                    checked={Boolean(automod.on?.[key])}
                                    onCheckedChange={(v) => setCell(key, v)}
                                    aria-label={`${CHECKER_LABELS[checker]} may ${ACTION_LABELS[action]} on ${platform}`}
                                  />
                                ) : (
                                  /* Inert WITH a reason, never an unticked box.
                                     A switch that silently does nothing leaves
                                     the operator believing this channel is
                                     protected. */
                                  <span
                                    className="cursor-help text-[10px] text-muted-foreground"
                                    title={cell?.reason ?? "not supported here"}
                                  >
                                    n/a
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
