import { useCallback, useEffect, useState, type ReactNode } from "react";
import { toast } from "sonner";
import { Loader2, Wrench } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import { accentFor } from "@/lib/chat";
import { WIRE } from "@/lib/contract/wire";
import { clockTime } from "@/lib/format";
import { useT, type Translator } from "@/lib/i18n";
import type {
  ChatPlatform,
  ChatStatus,
  TwitchAutoMod,
  TwitchBanned,
  TwitchBlockedTerm,
  TwitchChatters,
  TwitchPin,
  TwitchRoleHolder,
  TwitchShield,
  TwitchShoutouts,
  TwitchTermSync,
  TwitchUnbanRequest,
} from "@/lib/types";

/* ===========================================================================
   Twitch channel tools: the things a broadcaster or moderator does to the room
   or to a role, rather than to one message -- announcements, shoutouts, pins,
   Shield Mode, warnings, blocked terms, AutoMod, moderators and VIPs, bans and
   appeals, and who is connected.

   ONE PANEL, THREE TABS (say, safety, people), beside the chat rules card for
   the same reason that card exists: these act on the room. Every call goes to
   /chat/twitch/{resource}; a refusal comes back as Twitch's own reason with
   what to do about it, and is shown as it arrives.

   NOT YET TRIED ON A LIVE ACCOUNT, and the panel says so. A message AutoMod is
   holding is decided here by id: learning that Twitch is holding one needs
   EventSub, which is a later piece, so the list of held messages does not
   exist yet and the id is typed or pasted.
   =========================================================================== */

type Tab = "say" | "safety" | "people";
type Run = (job: () => Promise<unknown>, done?: string) => Promise<boolean>;

const COLORS = ["primary", "blue", "green", "orange", "purple"] as const;
const AUTOMOD_CATEGORIES = [
  "aggression",
  "bullying",
  "disability",
  "misogyny",
  "raceEthnicityOrReligion",
  "sexBasedTerms",
  "sexualitySexOrGender",
  "swearing",
] as const;
const LEVELS = [0, 1, 2, 3, 4];
/** Twitch's limits, from the server's wire contract (internal/chat). */
const LIMITS = WIRE.twitchTools;

const inputCls = "h-6 min-w-0 rounded border border-border bg-card-raised px-1 text-sm";

export function TwitchTools({ statuses }: Readonly<{ statuses: ChatStatus[] }>) {
  const twitch = statuses.find((s) => s.platform === ("twitch" as ChatPlatform));
  // Keyed on the account so a different Twitch connection starts clean.
  return twitch ? <ToolsForAccount key={twitch.account ?? ""} account={twitch.account} /> : null;
}

function ToolsForAccount({ account }: Readonly<{ account: string | undefined }>) {
  const t = useT();
  const [tab, setTab] = useState<Tab>("say");
  const [busy, setBusy] = useState(false);

  // Runs one call, shows Twitch's reason on a refusal, never throws.
  const run: Run = useCallback(
    async (job, done) => {
      setBusy(true);
      try {
        await job();
        if (done) toast.success(done);
        return true;
      } catch (err) {
        toast.error(err instanceof Error ? err.message : t("twitchTools.failed"));
        return false;
      } finally {
        setBusy(false);
      }
    },
    [t],
  );

  return (
    <div className="flex flex-col gap-2 rounded-md border border-border bg-card p-2">
      <div className="flex items-center gap-2">
        <Wrench className="h-3.5 w-3.5 text-muted-foreground" />
        <span className="text-[12px] font-semibold">{t("twitchTools.title")}</span>
        <span className={`text-sm ${accentFor("twitch" as ChatPlatform).text}`}>{t("chatRules.twitchOnly")}</span>
        {busy && <Loader2 className="h-3 w-3 animate-spin" />}
      </div>
      <p className="text-sm text-muted-foreground">{t("twitchTools.unverified")}</p>
      <div role="tablist" className="flex gap-1">
        {(["say", "safety", "people"] as const).map((k) => (
          <Button
            key={k}
            role="tab"
            aria-selected={tab === k}
            size="sm"
            variant={tab === k ? "default" : "secondary"}
            onClick={() => setTab(k)}
          >
            {t(`twitchTools.tab.${k}`)}
          </Button>
        ))}
      </div>
      {tab === "say" && <SayTab account={account} run={run} busy={busy} t={t} />}
      {tab === "safety" && <SafetyTab account={account} run={run} busy={busy} t={t} />}
      {tab === "people" && <PeopleTab account={account} run={run} busy={busy} t={t} />}
    </div>
  );
}

interface TabProps {
  account: string | undefined;
  run: Run;
  busy: boolean;
  t: Translator;
}

function Section({ title, children }: Readonly<{ title: string; children: ReactNode }>) {
  return (
    <fieldset className="flex flex-col gap-1 border-t border-border pt-1.5">
      <legend className="sr-only">{title}</legend>
      <span className="text-sm font-medium">{title}</span>
      {children}
    </fieldset>
  );
}

const Row = ({ children }: Readonly<{ children: ReactNode }>) => (
  <div className="flex flex-wrap items-center gap-1.5">{children}</div>
);

/* ----------------------------------------------------------------- say */

function SayTab({ account, run, busy, t }: Readonly<TabProps>) {
  const [message, setMessage] = useState("");
  const [color, setColor] = useState<(typeof COLORS)[number]>("primary");
  const [shout, setShout] = useState("");
  const [cool, setCool] = useState<TwitchShoutouts | null>(null);
  const [warnUser, setWarnUser] = useState("");
  const [reason, setReason] = useState("");
  const [pinId, setPinId] = useState("");
  const [pinSecs, setPinSecs] = useState("");
  const [pin, setPin] = useState<TwitchPin | null | undefined>(undefined);

  const loadPin = useCallback(
    () => run(async () => setPin((await api.twitchTool.read<{ pin: TwitchPin | null }>("pin", account)).pin)),
    [run, account],
  );
  useEffect(() => {
    void loadPin();
  }, [loadPin]);

  const secs = pinSecs.trim() === "" ? 0 : Number(pinSecs);

  return (
    <div className="flex flex-col gap-2">
      <Section title={t("twitchTools.announce")}>
        <Row>
          <input
            className={`${inputCls} w-56`}
            value={message}
            maxLength={LIMITS.maxAnnouncement}
            placeholder={t("twitchTools.announcePlaceholder")}
            aria-label={t("twitchTools.announce")}
            onChange={(e) => setMessage(e.target.value)}
          />
          <select
            className={inputCls}
            value={color}
            aria-label={t("twitchTools.colorAria")}
            onChange={(e) => setColor(e.target.value as (typeof COLORS)[number])}
          >
            {COLORS.map((c) => (
              <option key={c} value={c}>
                {t(`twitchTools.color.${c}`)}
              </option>
            ))}
          </select>
          <Button
            size="sm"
            disabled={busy || !message.trim()}
            onClick={() =>
              void run(async () => {
                await api.twitchTool.post("announcements", account, { message, color });
                setMessage("");
              }, t("twitchTools.sent"))
            }
          >
            {t("twitchTools.send")}
          </Button>
        </Row>
      </Section>

      <Section title={t("twitchTools.shoutout")}>
        <Row>
          <input
            className={`${inputCls} w-40`}
            value={shout}
            placeholder={t("twitchTools.channelPlaceholder")}
            aria-label={t("twitchTools.shoutout")}
            onChange={(e) => setShout(e.target.value)}
          />
          <Button
            size="sm"
            disabled={busy || !shout.trim()}
            onClick={() =>
              void run(async () => {
                setCool(await api.twitchTool.post<TwitchShoutouts>("shoutouts", account, { login: shout }));
                setShout("");
              }, t("twitchTools.sent"))
            }
          >
            {t("twitchTools.send")}
          </Button>
        </Row>
        <span className="text-sm text-muted-foreground">
          {cool?.nextAnyAt
            ? t("twitchTools.shoutoutNext", { time: clockTime(cool.nextAnyAt) })
            : t("twitchTools.shoutoutNote")}
        </span>
      </Section>

      <Section title={t("twitchTools.warn")}>
        <Row>
          <input
            className={`${inputCls} w-32`}
            value={warnUser}
            placeholder={t("twitchTools.viewerPlaceholder")}
            aria-label={t("twitchTools.viewerPlaceholder")}
            onChange={(e) => setWarnUser(e.target.value)}
          />
          <input
            className={`${inputCls} w-48`}
            value={reason}
            maxLength={LIMITS.maxWarning}
            placeholder={t("twitchTools.reasonPlaceholder")}
            aria-label={t("twitchTools.reasonPlaceholder")}
            onChange={(e) => setReason(e.target.value)}
          />
          <Button
            size="sm"
            disabled={busy || !warnUser.trim() || !reason.trim()}
            onClick={() =>
              void run(async () => {
                await api.twitchTool.post("warnings", account, { login: warnUser, reason });
                setReason("");
              }, t("twitchTools.warned"))
            }
          >
            {t("twitchTools.warn")}
          </Button>
        </Row>
      </Section>

      <Section title={t("twitchTools.pin")}>
        <Row>
          <input
            className={`${inputCls} w-44`}
            value={pinId}
            placeholder={t("twitchTools.messageIdPlaceholder")}
            aria-label={t("twitchTools.messageIdPlaceholder")}
            onChange={(e) => setPinId(e.target.value)}
          />
          <input
            className={`${inputCls} w-24`}
            type="number"
            min={LIMITS.minPinSeconds}
            max={LIMITS.maxPinSeconds}
            value={pinSecs}
            placeholder={t("twitchTools.pinSecondsPlaceholder")}
            aria-label={t("twitchTools.pinSecondsPlaceholder")}
            onChange={(e) => setPinSecs(e.target.value)}
          />
          <Button
            size="sm"
            disabled={busy || !pinId.trim()}
            onClick={() =>
              void run(async () => {
                await api.twitchTool.put("pin", account, { messageId: pinId, durationSeconds: secs });
                await loadPin();
              }, t("twitchTools.pinned"))
            }
          >
            {t("twitchTools.pin")}
          </Button>
          <Button
            size="sm"
            variant="secondary"
            disabled={busy || !pinId.trim() || secs === 0}
            onClick={() =>
              void run(async () => {
                await api.twitchTool.patch("pin", account, { messageId: pinId, durationSeconds: secs });
              }, t("twitchTools.pinChanged"))
            }
          >
            {t("twitchTools.pinChange")}
          </Button>
          <Button
            size="sm"
            variant="secondary"
            disabled={busy || !(pinId.trim() || pin)}
            onClick={() =>
              void run(async () => {
                await api.twitchTool.remove("pin", account, { messageId: pinId.trim() || pin?.messageId || "" });
                await loadPin();
              }, t("twitchTools.unpinned"))
            }
          >
            {t("twitchTools.unpin")}
          </Button>
        </Row>
        <span className="text-sm text-muted-foreground">
          {pin ? t("twitchTools.pinnedNow", { name: pin.senderName, text: pin.text }) : t("twitchTools.noPin")}
        </span>
      </Section>
    </div>
  );
}

/* -------------------------------------------------------------- safety */

function SafetyTab({ account, run, busy, t }: Readonly<TabProps>) {
  const [shield, setShield] = useState<TwitchShield | null>(null);
  const [auto, setAuto] = useState<TwitchAutoMod | null>(null);
  const [each, setEach] = useState(false);
  const [terms, setTerms] = useState<TwitchBlockedTerm[]>([]);
  const [term, setTerm] = useState("");
  const [sync, setSync] = useState<TwitchTermSync | null>(null);
  const [heldId, setHeldId] = useState("");
  const [flagUser, setFlagUser] = useState("");
  const [flagStatus, setFlagStatus] = useState("ACTIVE_MONITORING");

  const loadTerms = useCallback(
    () => run(async () => setTerms((await api.twitchTool.read<{ terms: TwitchBlockedTerm[] }>("blocked-terms", account)).terms)),
    [run, account],
  );
  useEffect(() => {
    void run(async () => setShield(await api.twitchTool.read<TwitchShield>("shield", account)));
    void run(async () => {
      const a = await api.twitchTool.read<TwitchAutoMod>("automod-settings", account);
      setAuto(a);
      setEach(a.overallLevel === null);
    });
    void loadTerms();
  }, [run, account, loadTerms]);

  const setLevel = (key: keyof TwitchAutoMod, v: number) => setAuto((a) => (a ? { ...a, [key]: v } : a));

  return (
    <div className="flex flex-col gap-2">
      <Section title={t("twitchTools.shield")}>
        <Row>
          <Switch
            id="tt-shield"
            checked={!!shield?.active}
            disabled={busy || shield === null}
            onCheckedChange={(on) =>
              void run(async () => setShield(await api.twitchTool.put<TwitchShield>("shield", account, { active: on })))
            }
          />
          <Label htmlFor="tt-shield" className="text-sm">
            {shield?.active ? t("twitchTools.shieldOn") : t("twitchTools.shieldOff")}
          </Label>
        </Row>
      </Section>

      <Section title={t("twitchTools.automod")}>
        {auto && (
          <>
            <Row>
              {!each && (
                <select
                  className={inputCls}
                  value={auto.overallLevel ?? 0}
                  aria-label={t("twitchTools.automodOverall")}
                  onChange={(e) => setAuto({ ...auto, overallLevel: Number(e.target.value) })}
                >
                  {LEVELS.map((l) => (
                    <option key={l} value={l}>
                      {t("twitchTools.automodOverall")} {l}
                    </option>
                  ))}
                </select>
              )}
              <Switch id="tt-each" checked={each} onCheckedChange={setEach} disabled={busy} />
              <Label htmlFor="tt-each" className="text-sm">
                {t("twitchTools.automodEach")}
              </Label>
            </Row>
            {each && (
              <div className="flex flex-wrap gap-x-3 gap-y-1">
                {AUTOMOD_CATEGORIES.map((c) => (
                  <label key={c} className="flex items-center gap-1 text-sm">
                    {t(`twitchTools.automodCat.${c}`)}
                    <select
                      className={inputCls}
                      value={auto[c]}
                      onChange={(e) => setLevel(c, Number(e.target.value))}
                    >
                      {LEVELS.map((l) => (
                        <option key={l} value={l}>
                          {l}
                        </option>
                      ))}
                    </select>
                  </label>
                ))}
              </div>
            )}
            <Row>
              <Button
                size="sm"
                disabled={busy}
                onClick={() =>
                  void run(async () => {
                    const settings = each ? { ...auto, overallLevel: null } : { overallLevel: auto.overallLevel ?? 0 };
                    setAuto(await api.twitchTool.put<TwitchAutoMod>("automod-settings", account, { settings }));
                  }, t("twitchTools.saved"))
                }
              >
                {t("common.apply")}
              </Button>
              <span className="text-sm text-muted-foreground">{t("twitchTools.automodNote")}</span>
            </Row>
          </>
        )}
      </Section>

      <Section title={t("twitchTools.held")}>
        <Row>
          <input
            className={`${inputCls} w-48`}
            value={heldId}
            placeholder={t("twitchTools.messageIdPlaceholder")}
            aria-label={t("twitchTools.held")}
            onChange={(e) => setHeldId(e.target.value)}
          />
          {([true, false] as const).map((allow) => (
            <Button
              key={String(allow)}
              size="sm"
              variant={allow ? "default" : "secondary"}
              disabled={busy || !heldId.trim()}
              onClick={() =>
                void run(async () => {
                  await api.twitchTool.post("held-messages", account, { messageId: heldId, allow });
                  setHeldId("");
                }, t("twitchTools.decided"))
              }
            >
              {allow ? t("twitchTools.approve") : t("twitchTools.deny")}
            </Button>
          ))}
        </Row>
        <span className="text-sm text-muted-foreground">{t("twitchTools.heldNote")}</span>
      </Section>

      <Section title={t("twitchTools.blockedTerms")}>
        <Row>
          <input
            className={`${inputCls} w-44`}
            value={term}
            maxLength={LIMITS.maxTerm}
            placeholder={t("twitchTools.termPlaceholder")}
            aria-label={t("twitchTools.blockedTerms")}
            onChange={(e) => setTerm(e.target.value)}
          />
          <Button
            size="sm"
            disabled={busy || term.trim().length < LIMITS.minTerm}
            onClick={() =>
              void run(async () => {
                await api.twitchTool.post("blocked-terms", account, { text: term });
                setTerm("");
                await loadTerms();
              }, t("twitchTools.saved"))
            }
          >
            {t("common.add")}
          </Button>
          <Button
            size="sm"
            variant="secondary"
            disabled={busy}
            onClick={() =>
              void run(async () => {
                setSync(await api.twitchTool.post<TwitchTermSync>("blocked-terms-sync", account, {}));
                await loadTerms();
              })
            }
          >
            {t("twitchTools.termSync")}
          </Button>
        </Row>
        {sync && (
          <span className="text-sm text-muted-foreground">
            {t("twitchTools.termSyncResult", {
              added: sync.added.length,
              present: sync.present.length,
              skipped: sync.skipped.length,
            })}
          </span>
        )}
        <ul className="flex flex-col gap-0.5">
          {terms.map((x) => (
            <li key={x.id} className="flex items-center gap-1.5 text-sm">
              <span className="break-all">{x.text}</span>
              <Button
                size="sm"
                variant="secondary"
                disabled={busy}
                onClick={() =>
                  void run(async () => {
                    await api.twitchTool.remove("blocked-terms", account, { id: x.id });
                    await loadTerms();
                  })
                }
              >
                {t("common.delete")}
              </Button>
            </li>
          ))}
        </ul>
      </Section>

      <Section title={t("twitchTools.suspicious")}>
        <Row>
          <input
            className={`${inputCls} w-32`}
            value={flagUser}
            placeholder={t("twitchTools.viewerPlaceholder")}
            aria-label={t("twitchTools.suspicious")}
            onChange={(e) => setFlagUser(e.target.value)}
          />
          <select
            className={inputCls}
            value={flagStatus}
            aria-label={t("twitchTools.suspiciousStatusAria")}
            onChange={(e) => setFlagStatus(e.target.value)}
          >
            <option value="ACTIVE_MONITORING">{t("twitchTools.suspiciousMonitor")}</option>
            <option value="RESTRICTED">{t("twitchTools.suspiciousRestrict")}</option>
          </select>
          <Button
            size="sm"
            disabled={busy || !flagUser.trim()}
            onClick={() =>
              void run(
                () => api.twitchTool.post("suspicious", account, { login: flagUser, status: flagStatus }),
                t("twitchTools.saved"),
              )
            }
          >
            {t("twitchTools.flag")}
          </Button>
          <Button
            size="sm"
            variant="secondary"
            disabled={busy || !flagUser.trim()}
            onClick={() =>
              void run(
                () => api.twitchTool.remove("suspicious", account, { login: flagUser.trim() }),
                t("twitchTools.saved"),
              )
            }
          >
            {t("twitchTools.unflag")}
          </Button>
        </Row>
        <span className="text-sm text-muted-foreground">{t("twitchTools.suspiciousNote")}</span>
      </Section>
    </div>
  );
}

/* -------------------------------------------------------------- people */

function PeopleTab({ account, run, busy, t }: Readonly<TabProps>) {
  const [mods, setMods] = useState<TwitchRoleHolder[]>([]);
  const [vips, setVips] = useState<TwitchRoleHolder[]>([]);
  const [banned, setBanned] = useState<TwitchBanned[]>([]);
  const [appeals, setAppeals] = useState<TwitchUnbanRequest[]>([]);
  const [chatters, setChatters] = useState<TwitchChatters | null>(null);

  const loadRoles = useCallback(
    () =>
      run(async () => {
        setMods((await api.twitchTool.read<{ holders: TwitchRoleHolder[] }>("roles", account, { role: "moderator" })).holders);
        setVips((await api.twitchTool.read<{ holders: TwitchRoleHolder[] }>("roles", account, { role: "vip" })).holders);
      }),
    [run, account],
  );
  const loadBanned = useCallback(
    () => run(async () => setBanned((await api.twitchTool.read<{ banned: TwitchBanned[] }>("banned", account)).banned)),
    [run, account],
  );
  const loadAppeals = useCallback(
    () =>
      run(async () =>
        setAppeals((await api.twitchTool.read<{ requests: TwitchUnbanRequest[] }>("unban-requests", account, { status: "pending" })).requests),
      ),
    [run, account],
  );
  const loadChatters = useCallback(
    (list: boolean) =>
      run(async () => setChatters(await api.twitchTool.read<TwitchChatters>("chatters", account, list ? { list: "1" } : {}))),
    [run, account],
  );
  useEffect(() => {
    void loadRoles();
    void loadBanned();
    void loadAppeals();
    void loadChatters(false);
  }, [loadRoles, loadBanned, loadAppeals, loadChatters]);

  return (
    <div className="flex flex-col gap-2">
      <RoleList title={t("twitchTools.moderators")} role="moderator" holders={mods} account={account} run={run} busy={busy} t={t} reload={loadRoles} />
      <RoleList title={t("twitchTools.vips")} role="vip" holders={vips} account={account} run={run} busy={busy} t={t} reload={loadRoles} />

      <Section title={t("twitchTools.banned")}>
        {banned.length === 0 && <span className="text-sm text-muted-foreground">{t("twitchTools.none")}</span>}
        <ul className="flex flex-col gap-0.5">
          {banned.map((b) => (
            <li key={b.userId} className="flex flex-wrap items-center gap-1.5 text-sm">
              <span>{b.name}</span>
              {b.expiresAt && <span className="text-muted-foreground">{t("twitchTools.timeout")}</span>}
              {b.reason && <span className="text-muted-foreground">{b.reason}</span>}
              <Button
                size="sm"
                variant="secondary"
                disabled={busy}
                onClick={() =>
                  void run(async () => {
                    await api.unbanChatUser({ platform: "twitch" as ChatPlatform, account, userId: b.userId });
                    await loadBanned();
                  })
                }
              >
                {t("twitchTools.unban")}
              </Button>
            </li>
          ))}
        </ul>
      </Section>

      <Section title={t("twitchTools.appeals")}>
        {appeals.length === 0 && <span className="text-sm text-muted-foreground">{t("twitchTools.none")}</span>}
        <ul className="flex flex-col gap-1">
          {appeals.map((r) => (
            <li key={r.id} className="flex flex-wrap items-center gap-1.5 text-sm">
              <span className="font-medium">{r.name}</span>
              <span className="text-muted-foreground">{r.text}</span>
              {(["approved", "denied"] as const).map((status) => (
                <Button
                  key={status}
                  size="sm"
                  variant={status === "approved" ? "default" : "secondary"}
                  disabled={busy}
                  onClick={() =>
                    void run(async () => {
                      await api.twitchTool.patch("unban-requests", account, { id: r.id, status });
                      await loadAppeals();
                    })
                  }
                >
                  {status === "approved" ? t("twitchTools.approve") : t("twitchTools.deny")}
                </Button>
              ))}
            </li>
          ))}
        </ul>
      </Section>

      <Section title={t("twitchTools.chatters")}>
        <Row>
          <span className="text-sm">{chatters ? t("twitchTools.chattersCount", { count: chatters.total }) : t("common.loading")}</span>
          <Button size="sm" variant="secondary" disabled={busy} onClick={() => void loadChatters(true)}>
            {t("twitchTools.showNames")}
          </Button>
        </Row>
        {chatters?.chatters && (
          <p className="max-h-24 overflow-auto break-words text-sm text-muted-foreground">
            {chatters.chatters.map((c) => c.name).join(", ")}
            {chatters.truncated ? " …" : ""}
          </p>
        )}
      </Section>
    </div>
  );
}

function RoleList({
  title,
  role,
  holders,
  account,
  run,
  busy,
  t,
  reload,
}: Readonly<
  TabProps & { title: string; role: "moderator" | "vip"; holders: TwitchRoleHolder[]; reload: () => Promise<boolean> }
>) {
  const [who, setWho] = useState("");
  return (
    <Section title={title}>
      <Row>
        <input
          className={`${inputCls} w-36`}
          value={who}
          placeholder={t("twitchTools.viewerPlaceholder")}
          aria-label={title}
          onChange={(e) => setWho(e.target.value)}
        />
        <Button
          size="sm"
          disabled={busy || !who.trim()}
          onClick={() =>
            void run(async () => {
              await api.twitchTool.post("roles", account, { role, login: who });
              setWho("");
              await reload();
            })
          }
        >
          {t("common.add")}
        </Button>
      </Row>
      <ul className="flex flex-wrap gap-x-3 gap-y-0.5">
        {holders.map((h) => (
          <li key={h.userId} className="flex items-center gap-1 text-sm">
            {h.name}
            <button
              type="button"
              className="text-muted-foreground hover:text-foreground"
              aria-label={`${t("twitchTools.remove")} ${h.name}`}
              disabled={busy}
              onClick={() =>
                void run(async () => {
                  await api.twitchTool.remove("roles", account, { role, userId: h.userId });
                  await reload();
                })
              }
            >
              ×
            </button>
          </li>
        ))}
      </ul>
    </Section>
  );
}
