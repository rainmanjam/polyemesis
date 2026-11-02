/* ===========================================================================
   The planned-rollover panel on a destination card (#19).

   It shows the platform's broadcast cap with its source, a countdown to the
   cutoff when there is one to count, the settings that trigger a rollover, and a
   manual "Roll over now". It shows nothing for a platform with no cap.

   The countdown rules live in lib/rolloverCountdown.ts, so this file only draws
   them. A reported cap is labelled as reported. A cap that is unverified shows no
   countdown at all. "Roll over now" reports each answer as its own outcome: 200
   is done, 409 is a refusal, 502 is the platform refusing, and the rest are
   plain failures.
   =========================================================================== */

import { useCallback, useEffect, useState } from "react";
import { ConfirmDestructive } from "@/components/ConfirmDestructive";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { countdownFor, formatRemaining } from "@/lib/rolloverCountdown";
import { classifyRun } from "@/lib/recurringOutcome";
import { getRollover, runRollover, saveRollover } from "@/lib/recurringApi";
import { useT } from "@/lib/i18n";
import type { DestStatus, RolloverSettings, RolloverView } from "@/lib/types";

/** How often the card re-reads the server. The countdown itself ticks on the same beat. */
const REFRESH_MS = 60_000;

type Notice = { tone: "ok" | "warn" | "bad"; text: string };

function messageOf(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

interface SettingsForm {
  enabled: boolean;
  marginMinutes: number;
  quietStart: string;
  quietEnd: string;
  quietZone: string;
  obsScene: string;
  chatNotice: boolean;
}

/** The form's starting values. A read that carries no settings block starts from the server's defaults
 *  (off, a 30-minute margin), so a malformed answer never throws while the card renders. */
function formFrom(s: Partial<RolloverSettings> | undefined): SettingsForm {
  return {
    enabled: s?.enabled ?? false,
    marginMinutes: s?.marginMinutes ?? 30,
    quietStart: s?.quietStart ?? "",
    quietEnd: s?.quietEnd ?? "",
    quietZone: s?.quietZone ?? "",
    obsScene: s?.obsScene ?? "",
    chatNotice: s?.chatNotice ?? false,
  };
}

export function RolloverPanel({ dest }: { dest: DestStatus }) {
  const t = useT();
  const [view, setView] = useState<RolloverView | null>(null);
  const [now, setNow] = useState(() => new Date());
  const [form, setForm] = useState<SettingsForm | null>(null);
  const [showSettings, setShowSettings] = useState(false);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<Notice | null>(null);
  const [runOpen, setRunOpen] = useState(false);
  // ONE READ, AT MOUNT. The panel used to look up the destination's programme with its own GET first, which put
  // a round trip between the card mounting and the rollover read; a row deleted inside that gap answered the
  // read 404 (the browser suite's intermittent console error). The rollover routes need no programme from the
  // client -- the server takes it from the destination row -- so there is nothing to look up.
  const load = useCallback(async () => {
    try {
      const v = await getRollover(dest.id);
      setView(v);
      setNow(new Date());
      setForm((cur) => cur ?? formFrom(v.settings));
    } catch (err) {
      setNotice({ tone: "bad", text: messageOf(err) });
    }
  }, [dest.id]);

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), REFRESH_MS);
    return () => window.clearInterval(timer);
  }, [load]);

  // A failed read leaves no view, but the failure must still show: rendering nothing would make a
  // broken read look the same as a platform with no cap.
  if (!view) {
    return notice ? (
      <section className="grid gap-2 rounded border border-border p-2 text-sm" aria-label={t("wave3.rollover.countdownTitle")}>
        <p role="status" className="text-xs text-down">
          {notice.text}
        </p>
      </section>
    ) : null;
  }
  if (!view.limit) return null;

  const countdown = countdownFor(view, now);
  const actionKey = view.action === "automatic" ? "wave3.rollover.actionAutomatic" : "wave3.rollover.actionCountdown";

  const save = async () => {
    if (!form) return;
    setBusy(true);
    setNotice(null);
    try {
      const v = await saveRollover(dest.id, {
        enabled: form.enabled,
        marginMinutes: form.marginMinutes,
        quietStart: form.quietStart,
        quietEnd: form.quietEnd,
        quietZone: form.quietZone,
        obsScene: form.obsScene,
        chatNotice: form.chatNotice,
      });
      setView(v);
      setNotice({ tone: "ok", text: t("wave3.rollover.saved") });
    } catch (err) {
      setNotice({ tone: "bad", text: messageOf(err) });
    } finally {
      setBusy(false);
    }
  };

  const runNow = async () => {
    setBusy(true);
    setNotice(null);
    try {
      await runRollover(dest.id);
      setNotice({ tone: "ok", text: t("wave3.rollover.runDone") });
    } catch (err) {
      const c = classifyRun(err);
      if (c.kind === "refused") {
        setNotice({ tone: "warn", text: t("wave3.rollover.runRefused", { reason: messageOf(err) }) });
      } else if (c.kind === "platform") {
        setNotice({ tone: "bad", text: t("wave3.rollover.runPlatform") });
      } else if (c.kind === "missing") {
        setNotice({ tone: "bad", text: t("wave3.rollover.runMissing") });
      } else if (c.kind === "server") {
        setNotice({ tone: "bad", text: t("wave3.rollover.runServer") });
      } else {
        setNotice({ tone: "bad", text: t("wave3.rollover.runOther", { status: c.kind === "other" ? c.status : 0 }) });
      }
    } finally {
      setBusy(false);
      void load();
    }
  };

  return (
    <section className="grid gap-2 rounded border border-border p-2 text-sm" aria-label={t("wave3.rollover.countdownTitle")}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h4 className="font-medium">{t("wave3.rollover.countdownTitle")}</h4>
        <span className="text-xs text-muted-foreground">
          {t("wave3.rollover.limitHours", { hours: view.limit.hours })}
        </span>
      </div>

      {countdown.kind === "running" ? (
        <p className="tabular-nums">{t("wave3.rollover.remaining", { time: formatRemaining(countdown.remainingMs) })}</p>
      ) : null}
      {countdown.kind === "waiting" ? <p>{t("wave3.rollover.waiting")}</p> : null}
      {countdown.kind === "none" && countdown.reason === "unverified" ? (
        <p>{t("wave3.rollover.noCountdownUnverified")}</p>
      ) : null}
      {countdown.kind === "none" && countdown.reason === "not-delivering" ? (
        <p>{t("wave3.rollover.noCountdownNotDelivering")}</p>
      ) : null}

      {view.limit.status === "reported" ? (
        <p className="text-xs text-warn">{t("wave3.rollover.reported")}</p>
      ) : null}
      {view.limit.source ? (
        <p className="text-xs">
          <a className="underline" href={view.limit.source} target="_blank" rel="noreferrer">
            {t("wave3.rollover.sourceLink")}
          </a>
        </p>
      ) : null}
      <p className="text-xs text-muted-foreground">{t(actionKey)}</p>

      {notice ? (
        <p role="status" className={notice.tone === "ok" ? "text-xs text-ok" : notice.tone === "warn" ? "text-xs text-warn" : "text-xs text-down"}>
          {notice.text}
        </p>
      ) : null}

      <div className="flex flex-wrap gap-2">
        <Button size="sm" variant="ghost" disabled={busy} onClick={() => setShowSettings((v) => !v)}>
          {t("wave3.rollover.settings")}
        </Button>
        {/* Only an automatic platform can be rolled over by polyemesis. A countdown-only one is just
            counted down, so offering the button there would only earn a refusal. */}
        {view.action === "automatic" && view.delivering ? (
          <Button size="sm" variant="outline" disabled={busy} onClick={() => setRunOpen(true)}>
            {t("wave3.rollover.runNow")}
          </Button>
        ) : null}
      </div>

      {showSettings && form ? (
        <div className="grid gap-2 border-t border-border pt-2">
          <div className="flex items-center gap-2">
            <Switch
              checked={form.enabled}
              onCheckedChange={(v) => setForm({ ...form, enabled: v })}
              aria-label={t("wave3.rollover.enabled")}
            />
            <Label>{t("wave3.rollover.enabled")}</Label>
          </div>
          <div className="grid gap-1">
            <Label htmlFor={`roll-margin-${dest.id}`}>{t("wave3.rollover.margin")}</Label>
            <Input
              id={`roll-margin-${dest.id}`}
              type="number"
              min={0}
              value={form.marginMinutes}
              onChange={(e) => setForm({ ...form, marginMinutes: Number(e.target.value) })}
            />
          </div>
          <div className="grid gap-1">
            <Label htmlFor={`roll-qs-${dest.id}`}>{t("wave3.rollover.quietStart")}</Label>
            <Input id={`roll-qs-${dest.id}`} value={form.quietStart} placeholder="HH:MM" onChange={(e) => setForm({ ...form, quietStart: e.target.value })} />
          </div>
          <div className="grid gap-1">
            <Label htmlFor={`roll-qe-${dest.id}`}>{t("wave3.rollover.quietEnd")}</Label>
            <Input id={`roll-qe-${dest.id}`} value={form.quietEnd} placeholder="HH:MM" onChange={(e) => setForm({ ...form, quietEnd: e.target.value })} />
          </div>
          <div className="grid gap-1">
            <Label htmlFor={`roll-qz-${dest.id}`}>{t("wave3.rollover.quietZone")}</Label>
            <Input id={`roll-qz-${dest.id}`} value={form.quietZone} placeholder="Europe/London" onChange={(e) => setForm({ ...form, quietZone: e.target.value })} />
          </div>
          <div className="grid gap-1">
            <Label htmlFor={`roll-obs-${dest.id}`}>{t("wave3.rollover.obsScene")}</Label>
            <Input id={`roll-obs-${dest.id}`} value={form.obsScene} onChange={(e) => setForm({ ...form, obsScene: e.target.value })} />
            <p className="text-xs text-muted-foreground">{t("wave3.rollover.obsSceneHint")}</p>
          </div>
          <div className="flex items-start gap-2">
            <Switch
              className="mt-0.5"
              checked={form.chatNotice}
              onCheckedChange={(v) => setForm({ ...form, chatNotice: v })}
              aria-label={t("wave3.rollover.chatNotice")}
            />
            <div>
              <Label>{t("wave3.rollover.chatNotice")}</Label>
              <p className="text-xs text-muted-foreground">{t("wave3.rollover.chatNoticeHint")}</p>
            </div>
          </div>
          <Button size="sm" disabled={busy} onClick={() => void save()}>
            {t("wave3.rollover.save")}
          </Button>
        </div>
      ) : null}

      <ConfirmDestructive
        open={runOpen}
        onOpenChange={setRunOpen}
        subject={dest.name}
        title={t("wave3.rollover.runNow")}
        description={t("wave3.rollover.runConfirm")}
        confirmLabel={t("wave3.rollover.runNow")}
        onConfirm={async () => {
          setRunOpen(false);
          await runNow();
        }}
      />
    </section>
  );
}
