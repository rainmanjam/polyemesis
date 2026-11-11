import { useCallback, useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import { useT } from "@/lib/i18n";
import type { TwitchPackStatus, TwitchPackSync } from "@/lib/types";

/* TWITCH'S OWN FILTERS.
 *
 * Four actions, each off until clicked, because each changes the channel's own
 * settings on Twitch and not just polyemesis's:
 *
 *   - copy the pack's severe hate terms to Twitch blocked terms. Resumable: one
 *     pass adds a batch and says how many are left, so this re-runs it until
 *     none are. Idempotent, so a click after a failure is safe.
 *   - remove them again. Only the terms polyemesis itself added; one the
 *     operator or Twitch's AutoMod added is never recorded, so never deleted.
 *   - raise Twitch AutoMod to level 1 or 2. Never lowers a category, leaves
 *     swearing as the channel has it.
 *   - refresh the impersonation detector's protected names from the channel's
 *     moderators and VIPs.
 *
 * The answers are counts. The pack is a slur list and this card never shows,
 * or receives, a term. Nothing renders unless a Twitch account is connected. */

/** A runaway guard: 20 passes of a few hundred terms is far past the pack. */
const MAX_PASSES = 20;

type Note = { kind: "ok" | "error"; text: string };

/** unsaved: the protection card holds edits the server has not seen, and the
 *  copy uses the SAVED settings. savedKey changes when a save lands, so the
 *  eligible count is read again. */
export function AutomodProtectionTwitch({ unsaved = false, savedKey = "" }: Readonly<{ unsaved?: boolean; savedKey?: string }>) {
  const t = useT();
  const [account, setAccount] = useState<string | null | undefined>(undefined);
  const [status, setStatus] = useState<TwitchPackStatus | null>(null);
  const [busy, setBusy] = useState<string>("");
  const [note, setNote] = useState<Note | null>(null);

  // Whether Twitch is connected, and under which account: the tools address a
  // connection by the account the chat status reports.
  useEffect(() => {
    let live = true;
    api
      .chatOverview(1)
      .then((o) => {
        if (!live) return;
        const tw = o.statuses.find((s) => s.platform === "twitch");
        setAccount(tw ? (tw.account ?? "") : null);
      })
      .catch(() => live && setAccount(null));
    return () => {
      live = false;
    };
  }, []);

  const refresh = useCallback(async () => {
    if (account == null) return;
    try {
      setStatus(await api.twitchTool.read<TwitchPackStatus>("pack-terms", account || undefined));
    } catch {
      setStatus(null);
    }
  }, [account]);

  // savedKey is read only to re-run the status read after a settings save.
  useEffect(() => {
    void refresh();
  }, [refresh, savedKey]);

  async function run(name: string, job: () => Promise<string | Note>) {
    setBusy(name);
    setNote(null);
    try {
      const out = await job();
      setNote(typeof out === "string" ? { kind: "ok", text: out } : out);
    } catch (e) {
      setNote({ kind: "error", text: t("automod.protTwFailed", { reason: e instanceof Error ? e.message : String(e) }) });
    } finally {
      setBusy("");
      void refresh();
    }
  }

  if (!account && account !== "") return null;
  const acct = account || undefined;

  const copy = () =>
    run("copy", async () => {
      const r = await loop(() => api.twitchTool.post<TwitchPackSync>("pack-terms-sync", acct, {}));
      if (!r.done) return { kind: "error", text: t("automod.protTwStalled", { remaining: r.remaining }) };
      return t("automod.protTwCopied", { added: r.added, present: r.present, skipped: r.skipped, remaining: r.remaining });
    });
  const remove = () =>
    run("remove", async () => {
      const r = await loop(() => api.twitchTool.remove<TwitchPackSync>("pack-terms", acct, {}));
      if (!r.done) return { kind: "error", text: t("automod.protTwStalled", { remaining: r.remaining }) };
      return t("automod.protTwRemoved", { count: r.removed, remaining: r.remaining });
    });
  const level = (n: 1 | 2) =>
    run(`level${n}`, async () => {
      await api.twitchTool.put("pack-automod-level", acct, { level: n });
      return t("automod.protTwLevelDone", { level: n });
    });
  const names = () =>
    run("names", async () => {
      const r = await api.twitchTool.post<{ moderators: number; vips: number; vipNote?: string }>(
        "pack-protected-names",
        acct,
        {},
      );
      const base = t("automod.protTwNamesDone", { moderators: r.moderators, vips: r.vips });
      return r.vipNote ? `${base} ${t("automod.protTwNamesVip", { reason: r.vipNote })}` : base;
    });

  const on = status?.on ?? false;
  return (
    <section aria-labelledby="automod-twitch-title" className="grid gap-3 rounded-md border p-3">
      <div>
        <h3 id="automod-twitch-title" className="text-sm font-medium">
          {t("automod.protTwTitle")}
        </h3>
        <p className="mt-0.5 text-xs text-muted-foreground">{t("automod.protTwDesc")}</p>
      </div>

      <div className="grid gap-1">
        <div className="flex flex-wrap items-center gap-2">
          <Button type="button" size="sm" variant="secondary" disabled={busy !== "" || !on || unsaved} onClick={() => void copy()}>
            {busy === "copy" ? t("automod.protTwCopying") : t("automod.protTwCopy")}
          </Button>
          {(status?.recorded ?? 0) > 0 && (
            <Button type="button" size="sm" variant="outline" disabled={busy !== ""} onClick={() => void remove()}>
              {t("automod.protTwRemove")}
            </Button>
          )}
        </div>
        <p className="text-sm text-muted-foreground">
          {t("automod.protTwCopyHelp", { count: status?.eligible ?? 0 })}
          {status && status.recorded > 0 ? ` ${t("automod.protTwRecorded", { count: status.recorded })}` : ""}
        </p>
        {unsaved && <p className="text-sm text-warn">{t("automod.protTwUnsaved")}</p>}
      </div>

      <div className="grid gap-1">
        <fieldset aria-label={t("automod.protTwLevelTitle")} className="flex min-w-0 flex-wrap items-center gap-2">
          <Button type="button" size="sm" variant="secondary" disabled={busy !== ""} onClick={() => void level(1)}>
            {t("automod.protTwLevel1")}
          </Button>
          <Button type="button" size="sm" variant="secondary" disabled={busy !== ""} onClick={() => void level(2)}>
            {t("automod.protTwLevel2")}
          </Button>
        </fieldset>
        <p className="text-sm text-muted-foreground">{t("automod.protTwLevelHelp")}</p>
      </div>

      <div className="grid gap-1">
        <div>
          <Button type="button" size="sm" variant="secondary" disabled={busy !== ""} onClick={() => void names()}>
            {t("automod.protTwNames")}
          </Button>
        </div>
        <p className="text-sm text-muted-foreground">{t("automod.protTwNamesHelp")}</p>
      </div>

      {note && note.kind === "error" && (
        <p role="alert" className="text-sm text-destructive">
          {note.text}
        </p>
      )}
      {note && note.kind !== "error" && <output className="block text-sm">{note.text}</output>}
    </section>
  );
}

// Posts (or deletes) until the server says it is done, or the remaining
// count stops falling (a pass whose adds all came back "present" still makes
// progress, so adds and removals are not the measure), adding the passes'
// counts together. A stop short of done is reported by the caller.
async function loop(call: () => Promise<TwitchPackSync>): Promise<TwitchPackSync> {
  const sum: TwitchPackSync = { added: 0, present: 0, removed: 0, skipped: 0, remaining: 0, done: false };
  let prev = Number.POSITIVE_INFINITY;
  for (let i = 0; i < MAX_PASSES; i++) {
    const r = await call();
    sum.added += r.added;
    sum.removed += r.removed;
    sum.present = r.present;
    sum.skipped = r.skipped;
    sum.remaining = r.remaining;
    sum.done = r.done;
    if (r.done || r.remaining >= prev) break;
    prev = r.remaining;
  }
  return sum;
}
