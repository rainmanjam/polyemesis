import { createContext, useContext, useEffect, useId, useState } from "react";
import { urlCarriesCredential } from "@/lib/credential-url";
import { SecretCode } from "@/components/SecretCode";
import { Link, Navigate, useNavigate, useSearchParams } from "react-router";
import { SettingsSectionSearch } from "@/components/SettingsSectionSearch";
import { toast } from "sonner";
import { ConfirmDestructive } from "@/components/ConfirmDestructive";
import { useConfirm } from "@/hooks/useConfirm";
import {
  AlertTriangle,
  ChevronDown,
  RefreshCw,
  Check,
  Copy,
  Download,
  ExternalLink,
  KeyRound,
  Loader2,
  Save,
  ShieldCheck,
  Trash2,
  Unplug,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { SecretInput } from "@/components/SecretInput";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Checkbox } from "@/components/ui/checkbox";
import { useIngestLive } from "@/hooks/useLiveData";
import { AccountLiveStats } from "@/components/AccountLiveStats";
import { DeviceCodeDialog } from "@/components/DeviceCodeDialog";
import { AutomodConfig } from "@/components/AutomodConfig";
import { AutomodMatrix } from "@/components/AutomodMatrix";
import { PlaylistEditor } from "@/components/PlaylistEditor";
import { Badge } from "@/components/ui/badge";
import { OverlaySettings } from "@/components/OverlaySettings";
import { ObsBridgeSettings } from "@/components/ObsBridgeSettings";
import { DangerZone } from "@/components/DangerZone";
import { PairingCheck } from "@/components/PairingCheck";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { SettingsOverview } from "@/components/SettingsOverview";
import { readSecretKeyAck, SECRET_KEY_ACK } from "@/lib/settingsOverview";
import { StatusPill } from "@/components/StatusPill";
import { EmptyState } from "@/components/EmptyState";
import { DisabledReason } from "@/components/DisabledReason";
import { disabledDescribedBy } from "@/lib/disabledReason";
import { platformSetupStatus } from "@/lib/status";
import { SETTINGS_GROUPS, settingsTabFor, type SettingsHit } from "@/lib/settingsSearch";
import {
  ADVANCED_CARDS,
  advancedCardShown,
  isAdvancedCard,
  useShowAdvanced,
  type AdvancedCard as AdvancedCardKey,
} from "@/lib/settingsAdvanced";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Accordion,
  AccordionContent,
  AccordionItem,
  AccordionTrigger,
} from "@/components/ui/accordion";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { PageHeader } from "@/components/AppLayout";
import { NoProgrammeYet } from "@/components/NoProgrammeYet";
import { TourReplayButton } from "@/components/Tour";
import { Experimental, ExperimentalBadge } from "@/components/Experimental";
// The destination dialog renders this matrix inline; this page shows the same
// rows as a full table. Both read it from lib, which is where it belongs.
import {
  CAPABILITY_COLUMNS,
  PLATFORM_CAPABILITIES,
  SUPPORT_LEGEND,
  supportInfo,
  supportOf,
  tierInfo,
} from "@/lib/capabilities";
import {
  api,
  isAccountInUse,
  isSettingsConflict,
  type AccountDestination,
} from "@/lib/api";
import {
  acmeStance,
  acmeYaml,
  offersPreflight,
  suggestedHostname,
  RESTART_COMMAND,
  type AcmeStance,
} from "@/lib/acme-guidance";
import { useT, useTN, type TranslationKey, type Translator } from "@/lib/i18n";
import { timestamp } from "@/lib/format";
import { toneBadge, toneText, type SignalTone } from "@/lib/signal";
import {
  platformConnectControls,
  platformSupportsDeviceCode,
} from "@/lib/platformConnect";
import { LIMITS } from "@/lib/limits";
import { credentialLabelsFor } from "@/lib/platformCredentialLabels";
import { cn } from "@/lib/utils";
import { pipelineDirty, pipelineSaveCarriesPassword } from "@/lib/pipelineSave";
import {
  failedRead,
  mayClaim,
  okRead,
  pendingRead,
  readFailed,
  rowsOf,
  type ReadState,
} from "@/lib/readState";
import { PullIngestFields } from "@/components/PullIngestFields";
import { ChatCommandSettings } from "@/components/ChatCommandSettings";
import type {
  StoredCredentialCheck,
  AcmeCheckId,
  AcmeCheckStatus,
  AcmePreflight,
  ApiToken,
  TokenScope,
  CertInfo,
  ChatRetentionSettings,
  ListenerSettings,
  CredentialCheck,
  FailoverReturn,
  IngestMode,
  MultitrackGpu,
  PlatformAccount,
  PlatformCreds,
  Settings,
  SetupGuide,
  SystemInfo,
  TlsMode,
  TlsStatus,
} from "@/lib/types";
import { MoreAbout } from "@/components/MoreAbout";

export function SettingsPage() {
  const t = useT();
  const [params, setParams] = useSearchParams();

  const [settings, setSettings] = useState<Settings | null>(null);
  const [system, setSystem] = useState<SystemInfo | null>(null);
  const [saving, setSaving] = useState(false);
  // How many programmes exist. `null` means NOT KNOWN -- either the request is
  // still in flight or it failed -- and it is deliberately not the same value
  // as 0, because every branch below that reads 0 hides something. A count that
  // could not be read must leave this page exactly as it was before the count
  // existed, never draw an empty state over an install with four sources.
  const [sourceCount, setSourceCount] = useState<number | null>(null);
  // The programme the Ingest tab writes through to (settings-council R8). The
  // server writes PUT /settings' ingest to DefaultSourceID -- the first source
  // by position -- which the list marks isDefault, so the scope label names the
  // same row the save will change. Null when unknown: the label then says only
  // that it is one programme's setting, never a guessed name.
  const [defaultSourceName, setDefaultSourceName] = useState<string | null>(null);
  // Whether the question has been ANSWERED, however it was answered. The render
  // gate waits on this rather than on the count, so a failed read stops the
  // spinner instead of leaving it turning for ever.
  const [sourcesResolved, setSourcesResolved] = useState(false);
  // The same question for the system read, and it exists so that `system ===
  // null` means one thing rather than two. FfmpegBadges renders nothing at all
  // for a null, which inside a titled card is a box with a heading and no
  // content -- and the operator docs/INSTALL.md sends here to check whether
  // FFmpeg has SRT is told nothing, with no way to tell "still loading" from
  // "could not read". Gating the page on this makes the null unambiguous.
  const [systemResolved, setSystemResolved] = useState(false);
  const [loadFailed, setLoadFailed] = useState(false);

  useEffect(() => {
    // EXPLICIT BRANCHES, not `.catch(() => {})`.
    //
    // These reads used to swallow every failure, which meant the page sat on
    // its spinner for ever and said nothing. That was survivable while the only
    // realistic failure was a dead server; it stopped being survivable when a
    // fresh install became a normal state, because "the settings never arrived"
    // and "this install has nothing yet" look identical from behind an empty
    // catch -- and only the second one has an answer the operator can act on.
    api.getSettings().then(setSettings).catch(() => setLoadFailed(true));
    api
      .listSources()
      .then((rows) => {
        setSourceCount(rows.length);
        setDefaultSourceName((rows.find((r) => r.isDefault) ?? rows[0])?.name ?? null);
      })
      // NOT 0. Claiming zero sources because the request failed would replace
      // the ingest form with "create a source" on an install that has several,
      // which is a worse lie than the one this change is fixing. Unknown leaves
      // every branch on its pre-existing behaviour.
      .catch(() => setSourceCount(null))
      .finally(() => setSourcesResolved(true));
    // The system read is the one that may legitimately fail on an install that
    // is otherwise fine -- it is the widest read on this page -- so it stays
    // non-fatal, but it says so now instead of vanishing.
    api
      .system()
      .then(setSystem)
      .catch(() => setSystem(null))
      .finally(() => setSystemResolved(true));
  }, []);

  // WHICH TAB OPENS, when the URL does not say.
  //
  // "ingest" was the default and is the wrong one on an install with no source:
  // the ingest belongs to the source row, so the form on that tab has nothing
  // to write to and the server refuses a change to it. Landing a first-time
  // operator on a form that cannot be saved, as the first thing they see on the
  // first settings page they open, is the dead editor in its most visible
  // possible form.
  //
  // An explicit ?tab=ingest is still honoured. Somebody who navigated there
  // deliberately gets the tab and the explanation on it, which is a different
  // thing from being put there.
  //
  // On an install with no source that is now the Overview (settings-council
  // R3): its "Set up this stream" checklist is the shortest true answer to
  // "what do I do first", where the eleven-card Pipeline tab was the longest.
  // An install that already has sources keeps landing where it always did.
  const requested = params.get("tab") ?? (sourceCount === 0 ? "overview" : "ingest");
  // Every value a link has ever carried keeps its panel (settingsTabFor); an
  // unknown one opens the Overview instead of an empty panel.
  const tab = settingsTabFor(requested);
  const navigate = useNavigate();
  const openTab = (v: string) => setParams({ tab: v });

  // ONE "SHOW ADVANCED" SWITCH for the page (settings-council R7), remembered
  // in this browser. `revealed` holds advanced cards a search result opened
  // while the switch was off: the operator asked for that card by name, so it
  // is shown without turning every other expert card on.
  const [showAdvanced, setShowAdvanced] = useShowAdvanced();
  const [revealed, setRevealed] = useState<ReadonlySet<string>>(new Set());
  const onSearchPick = (v: string, hit?: SettingsHit) => {
    if (v === "debug") {
      // navigate may return a promise (data routers); nothing waits on it.
      void navigate("/system");
      return;
    }
    if (hit && isAdvancedCard(hit.cardKey)) setRevealed((prev) => new Set(prev).add(hit.cardKey));
    openTab(v);
  };

  // The OAuth round trip is a full navigation, so its outcome comes back in
  // the query string rather than as an XHR response.
  useEffect(() => {
    const ok = params.get("oauth_ok");
    const err = params.get("oauth_error");
    /*  The third outcome: connected, and there is something the operator has to
     *  know before they rely on it. Today that is a platform whose API is
     *  reachable only on a paid tier — polyemesis asks at connect time so the
     *  refusal does not arrive mid-broadcast instead.
     *
     *  Not folded into `ok`: a green tick with a paragraph after it is the
     *  shape of message people stop reading. Not an error either — the
     *  connection worked and there is nothing to retry, and red would send
     *  somebody back round a flow that already succeeded.
     *
     *  Held open far longer than the default, and closeButton so it is
     *  dismissed rather than missed. It is several sentences of platform
     *  policy, and a toast that vanishes while it is still being read is the
     *  same as no warning at all. */
    const warn = params.get("oauth_warn");
    if (ok) toast.success(ok);
    if (err) toast.error(err);
    if (warn) toast.warning(warn, { duration: 60_000, closeButton: true });
    if (ok || err || warn) {
      const next = new URLSearchParams(params);
      next.delete("oauth_ok");
      next.delete("oauth_error");
      next.delete("oauth_warn");
      setParams(next, { replace: true });
    }
  }, [params, setParams]);

  // KEPT AS STATE AS WELL AS A TOAST, because one refusal has a field to land
  // on. The server rejects an automod rule whose regex will not compile -- it
  // has to, since NewRuleSet is all-or-nothing and one bad pattern would
  // silently disarm every rule -- and a toast puts that message somewhere the
  // operator cannot see while looking at the pattern they mistyped.
  //
  // AutomodConfig reads this and renders the rejection against the offending
  // rule. Everything else still gets the toast, which is the right shape for
  // an error with no single field behind it.
  const [saveError, setSaveError] = useState<string | null>(null);

  /* SOMEBODY ELSE SAVED FIRST, so this save was refused and nothing of it was
   * stored. The server's sentence says so. Held open with a reload action,
   * because the draft on screen is now based on a document that no longer
   * exists, and saving it again would conflict again. Not reloaded
   * automatically: that would throw away the draft the operator is looking at
   * without asking. */
  const conflictToast = (msg: string) =>
    toast.error(msg, {
      duration: Infinity,
      closeButton: true,
      action: { label: t("chatpage.reload"), onClick: () => window.location.reload() },
    });

  const save = async (next: Settings) => {
    setSaving(true);
    setSaveError(null);
    try {
      setSettings(await api.putSettings(next));
      toast.success(t("set.settingsSavedAffectedProcessesHave"));
    } catch (err) {
      const msg = err instanceof Error ? err.message : t("rec.saveFailed");
      // A conflict is not given to setSaveError: it belongs to no field, and
      // AutomodConfig would otherwise hunt for a rule to pin it on.
      if (isSettingsConflict(err)) {
        conflictToast(msg);
        return;
      }
      setSaveError(msg);
      toast.error(msg);
    } finally {
      setSaving(false);
    }
  };

  // Two calls, in this order, because the password does not live in the
  // settings blob. Settings first: if the broker URL and the password changed
  // together, storing the password against the OLD broker for a moment is the
  // harmless ordering, and storing new settings that briefly carry the old
  // password is not.
  const saveMqtt = async (next: Settings, password: string) => {
    setSaving(true);
    try {
      const saved = await api.putSettings(next);
      if (password !== "") {
        await api.putMqttPassword(password);
        saved.mqtt = { ...saved.mqtt, enabled: saved.mqtt?.enabled ?? false, hasPassword: true };
      }
      setSettings(saved);
      toast.success(t("i18nSweep.mqttSaved"));
    } catch (err) {
      if (isSettingsConflict(err) && err instanceof Error) {
        conflictToast(err.message);
        return;
      }
      toast.error(err instanceof Error ? err.message : t("set.couldNotSaveTheMqtt"));
    } finally {
      setSaving(false);
    }
  };

  const clearMqttPassword = async () => {
    try {
      await api.putMqttPassword("");
      setSettings((prev) =>
        prev ? { ...prev, mqtt: { ...prev.mqtt, enabled: prev.mqtt?.enabled ?? false, hasPassword: false } } : prev,
      );
      toast.success(t("set.brokerPasswordCleared"));
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("set.couldNotClearThePassword"));
    }
  };

  // The old Debug tab's address, kept working (A17).
  if (requested === "debug") return <Navigate to="/system" replace />;

  if (loadFailed) {
    return (
      <div className="flex h-full items-center justify-center p-3 text-center text-sm text-muted-foreground">
        {t("set.couldNotLoad")}
      </div>
    );
  }

  // The source count is waited for as well as the settings, because the tab
  // above turns on it. Rendering before it lands would open the ingest tab and
  // then move the operator to another one under their cursor. The system read
  // is waited for so that a null means "could not read" and never "not yet".
  if (!settings || !sourcesResolved || !systemResolved) {
    return (
      <div className="flex h-full items-center justify-center">
        <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
      </div>
    );
  }

  return (
    <div className="p-3">
      <PageHeader
        title={t("set.title")}
        subtitle={system ? `polyemesis ${system.version}` : undefined}
        // The tour's replay control, in the header rather than in a tab: the
        // tour crosses four pages and two of the four tabs here, so filing it
        // under one of them would be filing it under the wrong one. It is also
        // the reason the first-run offer can be a dismissible strip instead of
        // a modal — dismissing costs nothing when the thing is still findable.
        actions={<TourReplayButton />}
      />

      <SettingsSectionSearch
        labels={{
          overview: t("settingsUx.tabOverview"),
          ingest: t("set.ingest"),
          pipeline: t("set.tabPipeline"),
          platforms: t("set.tabPlatforms"),
          overlays: t("set.tabOverlays"),
          obs: t("obs.tab"),
          security: t("set.tabSecurity"),
          danger: t("danger.title"),
          debug: t("set.tabDebug"),
        }}
        // Debug lives on the System page now (A17); the search still finds it.
        // A hit on an advanced card opens that card (settings-council R7).
        onPick={onSearchPick}
      />

      {/* The sections run down a left rail on wide screens and stack on narrow
          ones. The rail is the same tab lists, so a rail link and a search
          result both set the same ?tab= value.

          GROUPED BY TASK (settings-council R6): Stream in, Stream out,
          Processing, Access and security, then the Advanced switch and Danger
          zone last. Each group is its own tablist, labelled by its visible
          heading, so the headings and the switch are never children of a
          tablist -- a screen reader hears "Stream out, tab list, 3 items"
          rather than a heading announced as a tab. */}
      <AdvancedContext.Provider value={{ show: showAdvanced, revealed, setShow: setShowAdvanced }}>
      <Tabs
        value={tab}
        orientation="vertical"
        onValueChange={openTab}
        className="lg:grid lg:grid-cols-[13rem_minmax(0,1fr)] lg:items-start lg:gap-4 [&>[role=tabpanel]]:lg:col-start-2"
      >
        <nav
          aria-label={t("set.title")}
          className="flex flex-wrap items-start gap-2 lg:row-span-full lg:flex-col lg:flex-nowrap lg:items-stretch lg:gap-1 lg:rounded-md lg:border lg:border-border lg:bg-card lg:p-1"
        >
          <TabsList aria-label={t("settingsUx.tabOverview")} className="lg:flex-col lg:items-stretch lg:border-0 lg:bg-transparent lg:p-0">
            <TabsTrigger value="overview">{t("settingsUx.tabOverview")}</TabsTrigger>
          </TabsList>
          {SETTINGS_GROUPS.map((group) => (
            <div key={group.label} className="flex flex-col gap-0.5">
              <span id={`settings-group-${group.label}`} className="px-2.5 pt-1 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                {t(group.label)}
              </span>
              <TabsList
                aria-labelledby={`settings-group-${group.label}`}
                className="lg:flex-col lg:items-stretch lg:border-0 lg:bg-transparent lg:p-0"
              >
                {group.tabs.map((v) => (
                  <TabsTrigger key={v} value={v}>
                    {t(RAIL_LABEL[v])}
                  </TabsTrigger>
                ))}
              </TabsList>
            </div>
          ))}
          {/* The Advanced switch sits in the rail because it changes every
              tab, not one card (settings-council R7). */}
          <div className="flex flex-col gap-1 px-2.5 pt-1">
            <span className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
              {t("settingsUx.groupAdvanced")}
            </span>
            <div className="flex items-center justify-between gap-2">
              {/* A plain label, not the kit's Label: this is the rail's switch,
                  not a field of any tab, so it stays out of the search index. */}
              <label htmlFor="settings-show-advanced" className="text-sm">
                {t("settingsUx.showAdvanced")}
              </label>
              <Switch
                id="settings-show-advanced"
                checked={showAdvanced}
                onCheckedChange={setShowAdvanced}
                aria-describedby="settings-show-advanced-hint"
              />
            </div>
            <span id="settings-show-advanced-hint" className="text-sm text-muted-foreground">
              {t("settingsUx.showAdvancedHint")}
            </span>
          </div>
          {/* Last, set apart and in the down tone (A17): the section that only
              holds what cannot be undone. Debug, which used to sit here, is on
              the System page. */}
          <TabsList aria-label={t("danger.title")} className="lg:mt-2 lg:flex-col lg:items-stretch lg:border-0 lg:bg-transparent lg:p-0">
            <TabsTrigger value="danger" className="text-down">
              {t("danger.title")}
            </TabsTrigger>
          </TabsList>
        </nav>

        <TabsContent value="overview">
          <SettingsOverview
            settings={settings}
            system={system}
            sourceCount={sourceCount}
            defaultSourceName={defaultSourceName}
            showAdvanced={showAdvanced}
            onOpen={openTab}
          />
        </TabsContent>
        <TabsContent value="ingest">
          {/* The form is REPLACED rather than disabled, and the difference is
              what the operator is told. A greyed-out form says "not now"; this
              says what the ingest actually is -- a property of a source -- and
              where to go and make one. Reached only by an explicit ?tab=ingest
              or by clicking the tab, since the default above no longer lands
              anybody here. */}
          {sourceCount === 0 ? (
            <div className="flex flex-col gap-3">
              <NoProgrammeYet title={t("empty.ingestTitle")} body={t("empty.ingestBody")} />
              {/* THE LISTENER PORTS STAY, and they are not a detail. They are
                  install-wide -- one SRT listener for every source, one RTMP
                  listener for at most one -- so PUT /settings deliberately
                  carries no requireSource and the server binds a changed port
                  on an install with no source at all. This card lived inside
                  IngestSettings, which the branch above replaces wholesale, and
                  that left the only port control in the product unreachable on
                  exactly the boot that logs `bind: address already in use`. The
                  operator was then told to create a source that would arrive on
                  the port that cannot bind. */}
              <ListenerPortsOnly settings={settings} onSave={save} saving={saving} />
              {/* The FFmpeg badges STAY. docs/INSTALL.md's verification step
                  sends a first-time operator here to read them, and that
                  operator has not created a source yet by definition -- so
                  hiding them with the form would have taken the check away
                  from exactly the person it exists for. */}
              <FfmpegCard system={system} />
            </div>
          ) : (
            <IngestSettings
              settings={settings}
              system={system}
              onSave={save}
              saving={saving}
              programme={defaultSourceName}
            />
          )}
        </TabsContent>
        <TabsContent value="pipeline">
          <PipelineSettings
            settings={settings}
            onSave={save}
            onSaveMqtt={saveMqtt}
            onClearMqttPassword={clearMqttPassword}
            saving={saving}
            saveError={saveError}
          />
        </TabsContent>
        <TabsContent value="platforms">
          <PlatformSettings />
        </TabsContent>
        <TabsContent value="overlays">
          <OverlaySettings />
        </TabsContent>
        <TabsContent value="obs">
          <ObsBridgeSettings />
        </TabsContent>
        <TabsContent value="security">
          <SecuritySettings system={system} />
        </TabsContent>
        <TabsContent value="danger">
          <DangerZone />
        </TabsContent>
      </Tabs>
      </AdvancedContext.Provider>
    </div>
  );
}

/** The rail's word for each grouped tab. */
const RAIL_LABEL: Record<(typeof SETTINGS_GROUPS)[number]["tabs"][number], TranslationKey> = {
  overview: "settingsUx.tabOverview",
  ingest: "set.ingest",
  pipeline: "set.tabPipeline",
  platforms: "set.tabPlatforms",
  overlays: "set.tabOverlays",
  obs: "obs.tab",
  security: "set.tabSecurity",
  danger: "danger.title",
  debug: "set.tabDebug",
};

/** The page's Advanced switch, read by every AdvancedCard below. */
const AdvancedContext = createContext<{
  show: boolean;
  revealed: ReadonlySet<string>;
  setShow: (next: boolean) => void;
}>({ show: true, revealed: new Set(), setShow: () => {} });

/** One advanced card (settings-council R4, R7), HIDDEN rather than unmounted
 *  when it is not shown -- see lib/settingsAdvanced.ts for why. `settings` is
 *  the STORED document, not the draft: a card shown because its saved value is
 *  not the default must not vanish under the operator's cursor the moment they
 *  type the default back in. */
function AdvancedCard({
  card,
  settings,
  inError,
  children,
}: {
  card: AdvancedCardKey;
  settings: Settings;
  inError: boolean;
  children: React.ReactNode;
}) {
  const ctx = useContext(AdvancedContext);
  const shown = advancedCardShown(card, { show: ctx.show, settings, inError, revealed: ctx.revealed });
  return (
    <div hidden={!shown} data-advanced-card={card} className="contents">
      {children}
    </div>
  );
}

/** The line that says what the Advanced switch is hiding on this tab, so an
 *  operator never has to guess whether a setting exists. Names the hidden
 *  cards and turns the switch on in one click. Renders nothing when nothing is
 *  hidden. */
function AdvancedHiddenLine({
  settings,
  inError,
  cards,
}: {
  settings: Settings;
  inError: boolean;
  cards: readonly AdvancedCardKey[];
}) {
  const t = useT();
  const ctx = useContext(AdvancedContext);
  const hidden = cards.filter(
    (card) => !advancedCardShown(card, { show: ctx.show, settings, inError, revealed: ctx.revealed }),
  );
  if (hidden.length === 0) return null;
  return (
    <div
      data-testid="advanced-hidden"
      className="flex flex-wrap items-center gap-2 rounded-md border border-dashed border-border-strong px-3 py-2 lg:col-span-2"
    >
      <span className="mr-auto text-sm text-muted-foreground">
        {t("settingsUx.advancedHidden", { names: hidden.map((card) => t(card)).join(", ") })}
      </span>
      <Button size="sm" variant="outline" onClick={() => ctx.setShow(true)}>
        {t("settingsUx.showThem")}
      </Button>
    </div>
  );
}

/** A group heading inside a tab (settings-council R4). */
function SettingsGroupHeading({ title, desc }: { title: TranslationKey; desc: TranslationKey }) {
  const t = useT();
  return (
    <div className="flex flex-col gap-0.5 pt-1 lg:col-span-2">
      <h2 className="text-sm font-semibold">{t(title)}</h2>
      <p className="text-sm text-muted-foreground">{t(desc)}</p>
    </div>
  );
}

/** Says which programmes a card's settings apply to (settings-council R8). */
function ScopeNote({ programme }: { programme?: string | null }) {
  const t = useT();
  if (programme === undefined) {
    return (
      <Badge variant="outline" className="w-fit normal-case">
        {t("settingsUx.scopeInstall")}
      </Badge>
    );
  }
  return (
    <span className="flex flex-wrap items-center gap-1.5">
      <Badge variant="outline" className="w-fit normal-case">
        {programme ? t("settingsUx.scopeProgramme", { name: programme }) : t("settingsUx.scopeOneProgramme")}
      </Badge>
      <Link to="/sources" className="text-sm text-muted-foreground underline underline-offset-2 hover:text-foreground">
        {t("settingsUx.scopeOthersOnSources")}
      </Link>
    </span>
  );
}

/* ------------------------------------------------------------------ ingest */

function IngestSettings({
  settings,
  system,
  onSave,
  saving,
  programme,
}: {
  settings: Settings;
  system: SystemInfo | null;
  onSave: (s: Settings) => void;
  saving: boolean;
  /** The programme this form writes to, or null when it could not be named. */
  programme: string | null;
}) {
  const t = useT();
  const tn = useTN();
  const [draft, setDraft] = useState(settings);
  useEffect(() => setDraft(settings), [settings]);
  // This page had no idea whether a broadcast was running: it never read live
  // state at all, so changing an SRT port mid-stream dropped every viewer with
  // nothing on screen having said so.
  const live = useIngestLive();

  const copyUrl = async () => {
    if (!system?.ingestUrl) return;
    await navigator.clipboard.writeText(system.ingestUrl);
    toast.success(t("set.ingestUrlCopied"));
  };

  const listeners = draft.listeners ?? { srtPort: 6000, rtmpPort: 1935 };
  const setListeners = (patch: Partial<typeof listeners>) =>
    setDraft({ ...draft, listeners: { ...listeners, ...patch } });

  return (
    <div className="grid gap-3 lg:grid-cols-2">
      <ListenerPortsCard listeners={listeners} setListeners={setListeners} />

      <Card>
        <CardHeader>
          <CardTitle>{t("set.ingest")}</CardTitle>
          <ScopeNote programme={programme} />
          <CardDescription>{t("settingsUx.ingestShort")}</CardDescription>
          <MoreAbout>{t("set.ingestDesc")}</MoreAbout>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <div className="flex flex-col gap-1">
            <Label>{t("set.mode")}</Label>
            <Select
              value={draft.ingest.mode}
              onValueChange={(v) =>
                setDraft({ ...draft, ingest: { ...draft.ingest, mode: v as IngestMode } })
              }
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="srt">{t("set.modeSrt")}</SelectItem>
                <SelectItem value="rtmp">{t("set.modeRtmp")}</SelectItem>
                <SelectItem value="pull">{t("set.modePull")}</SelectItem>
              </SelectContent>
            </Select>
            {system && !system.ffmpeg.hasLibsrt && draft.ingest.mode === "srt" && (
              <span className="text-sm text-warn">{t("set.noLibsrt")}</span>
            )}
          </div>

          {draft.ingest.mode === "pull" ? (
            <SettingsPullIngestFields draft={draft} setDraft={setDraft} />
          ) : draft.ingest.mode === "srt" ? (
            <>
              <div className="grid grid-cols-2 gap-2">
                <div className="flex flex-col gap-1">
                  <Label htmlFor="srt-latency">{t("set.latencyMs")}</Label>
                  <Input
                    id="srt-latency"
                    type="number"
                    value={draft.ingest.srt.latencyMs}
                    onChange={(e) =>
                      setDraft({
                        ...draft,
                        ingest: {
                          ...draft.ingest,
                          srt: { ...draft.ingest.srt, latencyMs: Number(e.target.value) },
                        },
                      })
                    }
                  />
                </div>
              </div>
              <div className="flex flex-col gap-1">
                <Label htmlFor="srt-pass">{t("set.passphrase")}</Label>
                <SecretInput
                  id="srt-pass"
                  value={draft.ingest.srt.passphrase}
                  placeholder={t("set.passphrasePlaceholder")}
                  onChange={(e) =>
                    setDraft({
                      ...draft,
                      ingest: {
                        ...draft.ingest,
                        srt: { ...draft.ingest.srt, passphrase: e.target.value },
                      },
                    })
                  }
                />
                <span className="text-sm text-muted-foreground">{t("set.passphraseNote")}</span>
              </div>
            </>
          ) : (
            <>
              <div className="grid grid-cols-2 gap-2">
                <div className="flex flex-col gap-1">
                  <Label htmlFor="rtmp-app">{t("set.app")}</Label>
                  <Input
                    id="rtmp-app"
                    value={draft.ingest.rtmp.app}
                    onChange={(e) =>
                      setDraft({
                        ...draft,
                        ingest: {
                          ...draft.ingest,
                          rtmp: { ...draft.ingest.rtmp, app: e.target.value },
                        },
                      })
                    }
                  />
                </div>
              </div>
              <div className="flex flex-col gap-1">
                <Label htmlFor="rtmp-key">{t("set.streamKey")}</Label>
                <SecretInput
                  id="rtmp-key"
                  value={draft.ingest.rtmp.streamKey}
                  onChange={(e) =>
                    setDraft({
                      ...draft,
                      ingest: {
                        ...draft.ingest,
                        rtmp: { ...draft.ingest.rtmp, streamKey: e.target.value },
                      },
                    })
                  }
                />
                <span className="text-sm text-muted-foreground">{t("set.streamKeyNote")}</span>
              </div>
            </>
          )}

          {/* Degraded-link failover's thresholds (#6) are per programme, and
              their ONE HOME is that programme's row on Sources (settings-
              council R13): this tab edits only the first programme, so a
              second copy here could only ever reach one of them. Linked, not
              deleted -- the control is one click away, for every programme.
              The draft still carries the stored thresholds unchanged, so
              saving this form never resets them. */}
          <p className="flex flex-wrap items-center gap-1.5 text-sm text-muted-foreground">
            {t("settingsUx.degradedOnSources")}
            <Link to="/sources" className="underline underline-offset-2 hover:text-foreground">
              {t("empty.goToSources")}
            </Link>
          </p>

          {/* Saving ingest settings reconciles the ingest, which restarts the
              FFmpeg child and drops every viewer. That is sometimes exactly
              what an operator means to do -- so this is a warning stating the
              consequence at the moment of decision, not a control refusing it.
              The button says what will happen rather than "Save", because a
              label an operator reads is worth more than a dialog they dismiss. */}
          {live && (
            <p className="flex items-start gap-1.5 text-sm text-warn">
              <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
              {t("set.ingestLiveWarn")}
            </p>
          )}
          <Button
            size="sm"
            variant={live ? "destructive" : "default"}
            onClick={() => onSave(draft)}
            disabled={saving}
          >
            {saving ? <Loader2 className="animate-spin" /> : <Save />}
            {live ? t("set.saveAndDrop") : t("set.saveIngest")}
          </Button>
        </CardContent>
      </Card>

      <Card className="h-fit">
        <CardHeader>
          <CardTitle>{t("set.encoderUrl")}</CardTitle>
          <ScopeNote programme={programme} />
          <CardDescription>{t("set.pointObs")}</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-2">
          <div className="flex items-center gap-2 rounded border border-border bg-background px-2 py-1.5">
            {urlCarriesCredential(system?.ingestUrl ?? "") ? (
              <SecretCode value={system?.ingestUrl ?? ""} />
            ) : (
              <code className="min-w-0 flex-1 break-all font-mono text-sm text-muted-foreground">
                {system?.ingestUrl ?? "…"}
              </code>
            )}
            <Button variant="ghost" size="icon-sm" onClick={copyUrl} aria-label={t("set.copy")}>
              <Copy />
            </Button>
          </div>
          <p className="text-sm text-muted-foreground">
            {tn("set.multitrackObsHint", {
              container: <code className="font-mono">mpegts</code>,
            })}
          </p>
        </CardContent>
      </Card>

      {/* The same FFmpeg card the zero-source branch draws (settings-council
          R13): one component, both render paths, so the check docs/INSTALL.md
          sends operators to reads the same whichever path they are on. */}
      <FfmpegCard system={system} />
    </div>
  );
}

/** Where the server binds, for the whole install.
 *
 *  Its own component for the reason FfmpegBadges is one: it has to render on an
 *  install with NO source. These two ports are not a property of a programme --
 *  one SRT listener serves every source and tells them apart by their publish
 *  token -- which is exactly why PUT /settings carries no requireSource and why
 *  the server binds a changed port with nothing configured behind it. */
function ListenerPortsCard({
  listeners,
  setListeners,
  footer,
}: {
  listeners: ListenerSettings;
  setListeners: (patch: Partial<ListenerSettings>) => void;
  footer?: React.ReactNode;
}) {
  const t = useT();
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("set.listeners")}</CardTitle>
        <ScopeNote />
        <CardDescription>{t("settingsUx.listenersShort")}</CardDescription>
        <MoreAbout>{t("set.listenersDesc")}</MoreAbout>
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        <div className="grid grid-cols-2 gap-2">
          <div className="flex flex-col gap-1">
            <Label htmlFor="listener-srt">{t("set.srtPort")}</Label>
            <Input
              id="listener-srt"
              type="number"
              min={LIMITS.port.min}
              max={LIMITS.port.max}
              value={listeners.srtPort}
              onChange={(e) => setListeners({ srtPort: Number(e.target.value) })}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="listener-rtmp">{t("set.rtmpPort")}</Label>
            <Input
              id="listener-rtmp"
              type="number"
              min={LIMITS.port.min}
              max={LIMITS.port.max}
              value={listeners.rtmpPort}
              onChange={(e) => setListeners({ rtmpPort: Number(e.target.value) })}
            />
          </div>
          <div className="col-span-2 flex flex-col gap-1">
            <Label htmlFor="listener-srtla">{t("set.srtlaPort")}</Label>
            <Input
              id="listener-srtla"
              type="number"
              min={0}
              max={LIMITS.port.max}
              value={listeners.srtlaPort ?? 0}
              onChange={(e) => setListeners({ srtlaPort: Number(e.target.value) })}
            />
            <span className="text-sm text-muted-foreground">{t("set.srtlaPortNote")}</span>
          </div>
        </div>
        {footer}
      </CardContent>
    </Card>
  );
}

/** The listener ports on an install with no source, where they are the only
 *  thing on this tab that can be saved.
 *
 *  It carries its own Save button because the one in IngestSettings belongs to
 *  the ingest form, and that form is not on this screen. The saved document is
 *  the stored settings with only `listeners` replaced: the ingest block goes
 *  back exactly as it arrived, which is what keeps the server's refusal --
 *  "the ingest changed and there is no source to write it through to" -- off a
 *  request that is not about the ingest at all. */
function ListenerPortsOnly({
  settings,
  onSave,
  saving,
}: {
  settings: Settings;
  onSave: (s: Settings) => void;
  saving: boolean;
}) {
  const t = useT();
  const [listeners, setListeners] = useState(settings.listeners ?? { srtPort: 6000, rtmpPort: 1935 });
  useEffect(
    () => setListeners(settings.listeners ?? { srtPort: 6000, rtmpPort: 1935 }),
    [settings],
  );
  return (
    <ListenerPortsCard
      listeners={listeners}
      setListeners={(patch) => setListeners({ ...listeners, ...patch })}
      footer={
        <Button
          size="sm"
          className="self-start"
          onClick={() => onSave({ ...settings, listeners })}
          disabled={saving}
        >
          {saving ? <Loader2 className="animate-spin" /> : <Save />}
          {t("set.saveListeners")}
        </Button>
      }
    />
  );
}

/** The FFmpeg capability card, drawn by BOTH ingest render paths (settings-
 *  council R13). It used to be a card of its own on the zero-source branch and
 *  a row inside the Encoder URL card on the other, so the same check looked
 *  like two different things depending on whether a source existed. */
function FfmpegCard({ system }: { system: SystemInfo | null }) {
  const t = useT();
  return (
    <Card className="h-fit">
      <CardHeader>
        <CardTitle>{t("set.ffmpegTitle")}</CardTitle>
        <ScopeNote />
      </CardHeader>
      <CardContent>
        <FfmpegBadges system={system} />
      </CardContent>
    </Card>
  );
}

/** What this box can actually do, as three or four badges.
 *
 *  Its own component because it has to render on an install with NO source as
 *  well as on one with a programme configured. docs/INSTALL.md's verification
 *  step sends a first-time operator to *Settings → Ingest* to read exactly
 *  these -- "if `srt` reads `no`, no amount of OBS configuration will fix it"
 *  -- and that operator has, by definition, not created a source yet. Leaving
 *  it inside the ingest form would have taken the FFmpeg check away from the
 *  one person the check was written for. */
function FfmpegBadges({ system }: { system: SystemInfo | null }) {
  const t = useT();
  /* NOT `return null`. This renders inside a card whose only other content is
   * its title, so nothing meant an empty box with a heading on it and no way
   * to tell a failed read from a slow one. The page waits for systemResolved
   * before it renders at all, so a null here means the read FAILED and saying
   * so is the whole difference between a blank panel and an answer. */
  if (!system) {
    return <span className="text-sm text-muted-foreground">{t("set.ffmpegUnknown")}</span>;
  }
  return (
    <div className="mt-1 flex flex-wrap gap-1">
      <Badge variant="outline">ffmpeg {system.ffmpeg.version}</Badge>
      <Badge variant={system.ffmpeg.hasLibsrt ? "live" : "warn"}>
        srt {system.ffmpeg.hasLibsrt ? t("set.yes") : t("set.no")}
      </Badge>
      <Badge variant={system.ffmpeg.hasLibx264 ? "outline" : "warn"}>
        x264 {system.ffmpeg.hasLibx264 ? t("set.yes") : t("set.no")}
      </Badge>
      {/* Only shown once the filter probe has run. An absent list means the
          probe did not happen, and reporting "no" for that would be a claim
          nobody measured. */}
      {(system.ffmpeg.filters?.length ?? 0) > 0 && (
        <Badge
          variant={system.ffmpeg.filters?.includes("drawtext") ? "outline" : "warn"}
          title={
            system.ffmpeg.filters?.includes("drawtext")
              ? t("set.drawtextOk") : t("set.drawtextMissing")
          }
        >
          {t("set.textOverlaysBadge")} {system.ffmpeg.filters?.includes("drawtext") ? t("set.yes") : t("set.no")}
        </Badge>
      )}
    </div>
  );
}

/** The pull source's fields, on the settings tab.
 *
 *  The fields themselves are @/components/PullIngestFields: the Sources page
 *  offers the same three ingest modes and rendered NOTHING for `pull`, so the
 *  block had to stop being private to this file. This is the settings-shaped
 *  adapter over it -- the shared component speaks PullSettings, this tab speaks
 *  a whole Settings draft. */
function SettingsPullIngestFields({
  draft,
  setDraft,
}: {
  draft: Settings;
  setDraft: (s: Settings) => void;
}) {
  return (
    <PullIngestFields
      value={draft.ingest.pull}
      onChange={(pull) => setDraft({ ...draft, ingest: { ...draft.ingest, pull } })}
    />
  );
}

/* ---------------------------------------------------------------- pipeline */

/** Every chat field, filled from the draft or from the server's defaults.
 *
 *  The controls below used to rebuild the chat object field by field, naming
 *  each sibling explicitly. That works right up until a field is ADDED: the
 *  handlers that predate it keep constructing an object without it, so editing
 *  the retention hours silently drops the new value and the save comes back
 *  rejected for a field the operator never touched. `historyMessages` was the
 *  field that would have done it.
 *
 *  Spreading this instead means a handler names only what it changes, and a
 *  future fifth field is carried by every existing control for free.
 *
 *  The fallbacks mirror db.DefaultSettings. They are reached only before the
 *  first GET lands or against a server too old to send the key. */
function chatFrom(draft: Settings): Required<ChatRetentionSettings> {
  return {
    retentionHours: draft.chat?.retentionHours ?? 2,
    keepMessages: draft.chat?.keepMessages ?? 2000,
    purgeMinutes: draft.chat?.purgeMinutes ?? 5,
    historyMessages: draft.chat?.historyMessages ?? 500,
    youtubeQuotaUnits: draft.chat?.youtubeQuotaUnits ?? 10000,
    youtubeQuotaReserve: draft.chat?.youtubeQuotaReserve ?? 200,
  };
}

/** Whether THIS BROWSER can resolve an IANA zone name.
 *
 *  The server is the authority -- db.Settings.Validate refuses a zone
 *  time.LoadLocation cannot load -- but that refusal arrives after a save, as
 *  one line among however many other problems the whole document had. Asking
 *  Intl the same question at the keystroke names the typo while the operator is
 *  still looking at the field they typed it into.
 *
 *  NOT A GATE ON THE INPUT, and the difference matters: the zone database lives
 *  in the browser here and in the Go binary there, so a browser older than the
 *  server would refuse a zone that saves perfectly. This warns and the Save
 *  stays live, which is the right way round -- the server is what actually
 *  decides, and a warning is recoverable where a disabled field is not.
 *
 *  Empty is not "unknown", it is UTC. lib/format.ts makes the same call for the
 *  same reason, and disagreeing here would mark the default install as broken.
 */
function zoneResolves(tz: string): boolean {
  const want = tz.trim();
  if (!want) return true;
  try {
    new Intl.DateTimeFormat(undefined, { timeZone: want });
    return true;
  } catch {
    return false;
  }
}

function PipelineSettings({
  settings,
  onSave,
  onSaveMqtt,
  onClearMqttPassword,
  saving,
  // The last save's refusal, threaded down rather than re-fetched, so
  // AutomodConfig can render a rejected regex against the rule that carries it.
  // A toast cannot: the operator is looking at the pattern they mistyped.
  saveError,
}: {
  settings: Settings;
  onSave: (s: Settings) => void;
  onSaveMqtt: (s: Settings, password: string) => void;
  onClearMqttPassword: () => Promise<void>;
  saving: boolean;
  saveError: string | null;
}) {
  const t = useT();
  const tn = useTN();
  const [draft, setDraft] = useState(settings);
  useEffect(() => setDraft(settings), [settings]);
  // Held here and never seeded from the server: the password is not in the
  // settings payload at all, so there is nothing to seed it FROM. An empty box
  // means "leave the stored one alone", which is why saving it is a separate
  // call rather than part of the settings PUT.
  const [mqttPassword, setMqttPassword] = useState("");
  useEffect(() => setMqttPassword(""), [settings]);

  // ONE SAVE FOR THE TAB, and it is a CONTROL rather than a warning.
  //
  // There used to be nine buttons over this single draft. Eight of them called
  // the plain settings save, which does not carry the MQTT broker password --
  // it is a separate PUT. So: type a broker password, click any of the eight,
  // and you got "Settings saved.", the password never sent, the box wiped by
  // the effect above re-seeding from the server's reply, and `hasPassword`
  // still false. Nothing on screen contradicted the success toast.
  //
  // Warning beside eight buttons would have made the loss visible. Routing
  // every save through one function that carries the password makes it
  // impossible. And since all nine PUT the whole tab draft anyway, collapsing
  // them also ends the second half of the same defect: a Save under the
  // failover slate that silently committed an abandoned chat-retention edit.
  const dirty = pipelineDirty(settings, draft, mqttPassword);
  const saveTab = () =>
    pipelineSaveCarriesPassword(mqttPassword)
      ? onSaveMqtt(draft, mqttPassword)
      : onSave(draft);

  return (
    <div className="flex flex-col gap-3">
      {/* Sticky, because this tab is two long columns and a bar that lives
          only at the top is a bar the operator editing the last card cannot
          see. It is also the only place a Save now exists on this tab, so it
          has to be reachable from everywhere in it. */}
      <div className="sticky top-0 z-10 flex items-center gap-2 rounded-md border border-border bg-background/95 px-2 py-1.5 backdrop-blur">
        <span className={cn("mr-auto text-sm", dirty ? "text-warn" : "text-muted-foreground")}>
          {dirty ? t("set.pipelineUnsaved") : t("set.pipelineOneSave")}
        </span>
        <Button size="sm" onClick={saveTab} disabled={saving || !dirty}>
          {saving ? <Loader2 className="animate-spin" /> : <Save />} {t("set.savePipeline")}
        </Button>
      </div>

    <div className="grid gap-3 lg:grid-cols-2">
      <div className="lg:col-span-2">
        {/* Every card on this tab writes the one install-wide settings
            document (settings-council R8). */}
        <ScopeNote />
      </div>

      <SettingsGroupHeading title="settingsUx.groupRoutine" desc="settingsUx.groupRoutineDesc" />

      <Card>
        <CardHeader>
          <CardTitle>{t("set.goingLive")}</CardTitle>
          <CardDescription>{t("set.goingLiveDesc")}          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <div className="flex flex-col gap-1">
            <Label htmlFor="dest-stagger">{t("set.stagger")}</Label>
            <Input
              id="dest-stagger"
              type="number"
              min={0}
              max={5000}
              value={draft.destinations?.staggerMs ?? 0}
              onChange={(e) =>
                setDraft({ ...draft, destinations: { staggerMs: Number(e.target.value) } })
              }
              className="w-28"
            />
            <span className="text-sm text-muted-foreground">{t("set.staggerNote")}</span>
            <span className="text-sm text-muted-foreground">
              {t("set.staggerReconnectNote")}</span>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t("set.chatHistory")}</CardTitle>
          <CardDescription>{t("set.chatHistoryDesc")}          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <div className="flex flex-wrap gap-4">
            <div className="flex flex-col gap-1">
              <Label htmlFor="chat-hours">{t("set.chatHours")}</Label>
              <Input
                id="chat-hours"
                type="number"
                min={0}
                max={43800}
                value={draft.chat?.retentionHours ?? 2}
                onChange={(e) =>
                  setDraft({
                    ...draft,
                    chat: { ...chatFrom(draft), retentionHours: Number(e.target.value) },
                  })
                }
                className="w-28"
              />
              <span className="text-sm text-muted-foreground">{t("set.chatRetentionZero")}</span>
            </div>

            <div className="flex flex-col gap-1">
              <Label htmlFor="chat-keep">{t("set.chatKeep")}</Label>
              <Input
                id="chat-keep"
                type="number"
                min={0}
                max={5000000}
                value={draft.chat?.keepMessages ?? 2000}
                onChange={(e) =>
                  setDraft({
                    ...draft,
                    chat: { ...chatFrom(draft), keepMessages: Number(e.target.value) },
                  })
                }
                className="w-32"
              />
              <span className="text-sm text-muted-foreground">{t("set.chatKeepNote")}</span>
            </div>

            {/* THE SWEEP THAT MAKES THE TWO ABOVE TRUE ON DISK. Retention and
                the keep-floor decide which rows are past it; nothing deletes
                anything until this runs, so a message an operator believes was
                dropped an hour ago is still in the database until the next
                sweep -- which is a subject-access answer, not a tidiness one.

                Cheap enough that the number is about promptness rather than
                load: it is one indexed delete. db.ChatSettings.problems refuses
                0 and anything over a day, and 0 is refused rather than
                defaulted because a sweep on every tick is far likelier to be a
                typo than a wish. */}
            <div className="flex flex-col gap-1">
              <Label htmlFor="chat-purge">{t("set.chatPurge")}</Label>
              <Input
                id="chat-purge"
                type="number"
                min={1}
                max={1440}
                value={draft.chat?.purgeMinutes ?? 5}
                onChange={(e) =>
                  setDraft({
                    ...draft,
                    chat: { ...chatFrom(draft), purgeMinutes: Number(e.target.value) },
                  })
                }
                className="w-28"
              />
              <span className="text-sm text-muted-foreground">{t("set.chatPurgeNote")}</span>
            </div>

            <div className="flex flex-col gap-1">
              <Label htmlFor="chat-history">{t("set.chatSendOnConnect")}</Label>
              <Input
                id="chat-history"
                type="number"
                min={LIMITS.chatHistoryMessages.min}
                max={LIMITS.chatHistoryMessages.max}
                value={draft.chat?.historyMessages ?? 500}
                onChange={(e) =>
                  setDraft({
                    ...draft,
                    chat: { ...chatFrom(draft), historyMessages: Number(e.target.value) },
                  })
                }
                className="w-32"
              />
              <span className="text-sm text-muted-foreground">{t("set.chatSendNote")}</span>
            </div>

            {/* YOUTUBE IS THE ONLY PLATFORM HERE THAT BILLS FOR CHAT. Twitch is
                an IRC socket and Kick posts webhooks; both are free and
                real-time. YouTube is polled and every poll costs 5 of a daily
                10,000, so this number is the denominator of every pacing
                decision the server makes — and until #732 nothing could tell it
                the operator had been granted more. */}
            <div className="flex flex-col gap-1">
              <Label htmlFor="chat-yt-quota">{t("set.chatYouTubeQuota")}</Label>
              <Input
                id="chat-yt-quota"
                type="number"
                min={LIMITS.youtubeQuotaUnits.min}
                max={LIMITS.youtubeQuotaUnits.max}
                value={draft.chat?.youtubeQuotaUnits ?? 10000}
                onChange={(e) =>
                  setDraft({
                    ...draft,
                    chat: { ...chatFrom(draft), youtubeQuotaUnits: Number(e.target.value) },
                  })
                }
                className="w-32"
              />
              <span className="text-sm text-muted-foreground">
                {t("set.chatYouTubeQuotaNote")}
              </span>
            </div>

            {/* THE SLICE HELD BACK SO SENDING STILL WORKS. Reading YouTube chat
                and posting into it are billed out of the same daily allowance,
                so a poller that spends the day leaves a moderator unable to
                type -- and the message that could not be sent is the timeout,
                at the moment it was needed. The pacer has always held a reserve
                back for exactly that; it just held chat.DefaultQuotaReserve,
                because the field beside the allowance had no control either.

                Bounded against the allowance rather than absolutely, the way
                db.ChatSettings.problems bounds it: a reserve at or above half
                the budget leaves too little to read with, so chat would pause
                the moment it started with the quota panel showing a full tank
                -- a refusal that reads as a bug. */}
            <div className="flex flex-col gap-1">
              <Label htmlFor="chat-yt-reserve">{t("set.chatYouTubeReserve")}</Label>
              <Input
                id="chat-yt-reserve"
                type="number"
                min={0}
                max={Math.floor((draft.chat?.youtubeQuotaUnits ?? 10000) / 2)}
                value={draft.chat?.youtubeQuotaReserve ?? 200}
                onChange={(e) =>
                  setDraft({
                    ...draft,
                    chat: { ...chatFrom(draft), youtubeQuotaReserve: Number(e.target.value) },
                  })
                }
                className="w-32"
              />
              <span className="text-sm text-muted-foreground">
                {t("set.chatYouTubeReserveNote")}
              </span>
            </div>
          </div>

          <span className="text-sm text-muted-foreground">
            {t("set.chatBothApply")} {t("set.chatFloorWhy")}
          </span>
          <span className="text-sm text-muted-foreground">{t("set.chatNotOnlyDisk")}</span>
          <span className="text-sm text-muted-foreground">{t("set.chatSmall")}</span>
          <span className="text-sm text-muted-foreground">
            {tn("set.chatSendOnConnectWhy", {
              name: <strong>{t("set.chatSendOnConnectShort")}</strong>,
            })}
          </span>
        </CardContent>
      </Card>

      {/* Automod sits directly after chat retention, because the two are the
          same subject: retention is the DEPTH the history checker can see, and
          a rate detector with a two-hour scrollback behind it is a different
          instrument from one with a week. */}
      {/* THE CONFIG SITS ABOVE THE MATRIX BECAUSE THE MATRIX GRANTS PERMISSION
          TO WHAT THIS CREATES. A matrix row for a checker nobody has configured
          is a permission over an empty set -- and until this card existed the
          matrix would happily arm "Ban / Model" over a nil checker, which is
          how an operator ends up believing bans are being issued by a model
          that was never given an endpoint.

          Reading order is therefore the causal order: write the rules, key the
          model, then say what each may do. */}
      <AutomodConfig settings={draft} onChange={setDraft} saveError={saveError} />

      <AutomodMatrix settings={draft} onChange={setDraft} />

      <ChatCommandSettings
        value={draft.chatCommands}
        onChange={(chatCommands) => setDraft({ ...draft, chatCommands })}
      />

      {/* Alert DELIVERY, not alert matching. Which conditions fire is per-rule
          and lives on the Automation page; this is the one install-wide answer
          behind all of them, and it is stored in the same settings tree as
          everything else on this page — so it is saved by the same button. */}
      <Card>
        <CardHeader>
          <CardTitle>{t("set.alertDelivery")}</CardTitle>
          <CardDescription>{t("set.alertDeliveryDesc")}          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <div className="flex flex-col gap-1">
            <Label htmlFor="alert-attempts">{t("set.deliveryAttempts")}</Label>
            <Input
              id="alert-attempts"
              type="number"
              min={LIMITS.alertRetryAttempts.min}
              max={LIMITS.alertRetryAttempts.max}
              value={draft.alerts?.retryAttempts ?? 4}
              onChange={(e) =>
                setDraft({ ...draft, alerts: { retryAttempts: Number(e.target.value) } })
              }
              className="w-28"
            />
            <span className="text-sm text-muted-foreground">{t("set.attemptsNote")}</span>
          </div>
          <span className="text-sm text-muted-foreground">{t("set.attemptsRetryable")}</span>
          <span className="text-sm text-muted-foreground">{t("set.attemptsBackoff")}</span>
        </CardContent>
      </Card>

      <SettingsGroupHeading title="settingsUx.groupOnce" desc="settingsUx.groupOnceDesc" />

      {/* FIRST in the tab, which is where db.Settings puts Display and for the
          reason its comment gives: it is the one install-wide block that
          changes what every other screen LOOKS like without changing what any
          of them DO. Nothing on this page could reach it -- lib/format.ts has
          held the zone for every clock in the console since it was written, and
          api.ts pushes it in on every settings read and every settings save, so
          the whole mechanism was live with no producer at the front of it. An
          install could only ever read UTC.

          It is on this tab rather than a Display tab of its own because it is a
          field of the same Settings document as everything else here, and the
          tab has the one Save that commits it. */}
      <Card>
        <CardHeader>
          <CardTitle>{t("set.displayTitle")}</CardTitle>
          <CardDescription>{t("set.displayDesc")}</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <div className="flex flex-col gap-1">
            <Label htmlFor="disp-tz">{t("set.displayTimeZone")}</Label>
            <Input
              id="disp-tz"
              value={draft.display?.timeZone ?? ""}
              placeholder={t("set.displayTimeZonePlaceholder")}
              onChange={(e) => setDraft({ ...draft, display: { timeZone: e.target.value } })}
            />
            <span className="text-sm text-muted-foreground">{t("set.displayTimeZoneNote")}</span>
            {/* Warned rather than refused -- see zoneResolves. The line is the
                only thing between a typo and a console that reads UTC for ever
                with nothing saying why. */}
            {!zoneResolves(draft.display?.timeZone ?? "") && (
              <span className="text-sm text-warn">{t("set.displayTimeZoneUnknown")}</span>
            )}
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t("set.syntheticAudio")}</CardTitle>
          <CardDescription>{t("set.syntheticDesc")}          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <div className="flex items-center justify-between">
            <Label htmlFor="synth-silence">{t("set.synthesiseSilent")}</Label>
            <Switch
              id="synth-silence"
              checked={draft.synth?.silenceOnVideoOnly ?? true}
              onCheckedChange={(v) => setDraft({ ...draft, synth: { silenceOnVideoOnly: v } })}
            />
          </div>
          <MoreAbout lead={t("set.synthesiseLead")}>{t("set.synthesiseNote")}</MoreAbout>
        </CardContent>
      </Card>

      {/* Failover stays OUT of the Advanced group on purpose: the dashboard
          links to #failover (Dashboard.tsx), and an operator who followed
          that link must land on the switch, whatever the Advanced switch says.
          Off, it is already one row -- its fields render only once it is on. */}
      <Card>
        <CardHeader>
          {/* The anchor the dashboard's exposure line points at. The tab is
              selected by ?tab=pipeline; this scrolls to the card within it, so
              a link from the dashboard lands on the switch rather than at the
              top of a long page. */}
          <CardTitle id="failover">{t("set.failover")}</CardTitle>
          <CardDescription>{t("settingsUx.failoverShort")}</CardDescription>
          <MoreAbout>{t("set.failoverDesc")}</MoreAbout>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <div className="flex items-center justify-between">
            <Label htmlFor="fo-enabled">{t("set.enabled")}</Label>
            <Switch
              id="fo-enabled"
              checked={draft.failover?.enabled ?? false}
              onCheckedChange={(v) =>
                setDraft({ ...draft, failover: { ...draft.failover, enabled: v } })
              }
            />
          </div>
          <span className="text-sm text-muted-foreground">{t("set.failoverRemuxNote")}</span>

          {draft.failover?.enabled && (
            <>
              <div className="flex flex-col gap-1">
                <Label htmlFor="fo-grace">{t("set.gracePeriod")}</Label>
                <Input
                  id="fo-grace"
                  type="number"
                  min={1}
                  value={draft.failover?.graceSeconds ?? 10}
                  onChange={(e) =>
                    setDraft({
                      ...draft,
                      failover: { ...draft.failover, enabled: true, graceSeconds: Number(e.target.value) },
                    })
                  }
                />
                <span className="text-sm text-muted-foreground">{t("set.graceNote")}</span>
              </div>

              <div className="flex flex-col gap-1">
                <Label>{t("set.comingBack")}</Label>
                <Select
                  value={draft.failover?.return ?? "manual"}
                  onValueChange={(v) =>
                    setDraft({
                      ...draft,
                      failover: { ...draft.failover, enabled: true, return: v as FailoverReturn },
                    })
                  }
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="manual">{t("set.returnManual")}</SelectItem>
                    <SelectItem value="auto">{t("set.returnAutomatic")}</SelectItem>
                  </SelectContent>
                </Select>
                <span className="text-sm text-muted-foreground">{t("set.returnManualNote")}</span>
              </div>

              {draft.failover?.return === "auto" && (
                <div className="flex flex-col gap-1">
                  <Label htmlFor="fo-stable">{t("set.returnAfter")}</Label>
                  <Input
                    id="fo-stable"
                    type="number"
                    min={0}
                    value={draft.failover?.returnStableSeconds ?? 60}
                    onChange={(e) =>
                      setDraft({
                        ...draft,
                        failover: {
                          ...draft.failover,
                          enabled: true,
                          returnStableSeconds: Number(e.target.value),
                        },
                      })
                    }
                  />
                </div>
              )}

              <div className="flex items-center justify-between">
                <Label htmlFor="fo-slate">{t("set.showSlate")}</Label>
                <Switch
                  id="fo-slate"
                  checked={draft.failover?.slate?.enabled ?? false}
                  onCheckedChange={(v) =>
                    setDraft({
                      ...draft,
                      failover: {
                        ...draft.failover,
                        enabled: true,
                        slate: { ...draft.failover?.slate, enabled: v },
                      },
                    })
                  }
                />
              </div>

              {draft.failover?.slate?.enabled && (
                <>
                  <div className="flex flex-col gap-1">
                    <Label htmlFor="fo-slate-img">{t("set.slateImage")}</Label>
                    <Input
                      id="fo-slate-img"
                      value={draft.failover?.slate?.imagePath ?? ""}
                      placeholder={t("set.slateImagePlaceholder")}
                      onChange={(e) =>
                        setDraft({
                          ...draft,
                          failover: {
                            ...draft.failover,
                            enabled: true,
                            slate: { ...draft.failover?.slate, enabled: true, imagePath: e.target.value },
                          },
                        })
                      }
                    />
                    <span className="text-sm text-muted-foreground">{t("set.slateImageNote")}</span>
                  </div>

                  <div className="flex flex-col gap-1">
                    <Label htmlFor="fo-slate-col">{t("set.slateColour")}</Label>
                    <Input
                      id="fo-slate-col"
                      value={draft.failover?.slate?.color ?? ""}
                      placeholder={t("set.slateColourPlaceholder")}
                      onChange={(e) =>
                        setDraft({
                          ...draft,
                          failover: {
                            ...draft.failover,
                            enabled: true,
                            slate: { ...draft.failover?.slate, enabled: true, color: e.target.value },
                          },
                        })
                      }
                    />
                    <span className="text-sm text-muted-foreground">{t("set.slateColourNote")}</span>
                  </div>

                  {/* THE SLATE'S OWN ENCODE. All three land in
                      ffmpeg.SlateArgs, so all three are argv: changing one
                      respawns the slate tier (docs/HOT-RELOAD.md, "the slate's
                      own argv") and nothing else. Editing them while the slate
                      is on screen is therefore a visible thing to do, and
                      editing them while the primary is live costs nothing.

                      There is deliberately NO width, height or frame rate here,
                      and that absence is a design rather than a gap: the slate
                      has to match the ingest that went away closely enough that
                      a `-c:v copy` destination does not choke on the change, and
                      the only thing that knows what the ingest was is the probe.
                      See db.SlateSettings. */}
                  <div className="flex flex-col gap-1">
                    <Label htmlFor="fo-slate-kbps">{t("set.slateBitrate")}</Label>
                    <Input
                      id="fo-slate-kbps"
                      type="number"
                      min={0}
                      max={100000}
                      value={draft.failover?.slate?.videoKbps ?? 0}
                      onChange={(e) =>
                        setDraft({
                          ...draft,
                          failover: {
                            ...draft.failover,
                            enabled: true,
                            slate: {
                              ...draft.failover?.slate,
                              enabled: true,
                              videoKbps: Number(e.target.value),
                            },
                          },
                        })
                      }
                      className="w-28"
                    />
                    <span className="text-sm text-muted-foreground">{t("set.slateBitrateNote")}</span>
                  </div>

                  <div className="grid grid-cols-2 gap-2">
                    <div className="flex flex-col gap-1">
                      <Label htmlFor="fo-slate-enc">{t("set.slateEncoder")}</Label>
                      {/* Free text rather than a picker, for the reason
                          Rendition.encoder is: which encoders exist is a
                          property of the running FFmpeg build, and this page
                          does not read GET /encoders. Empty is libx264 and is
                          the right answer even on a box full of hardware -- a
                          static frame costs a software encoder nothing, and the
                          one job the slate has is to start when everything else
                          has already failed. */}
                      <Input
                        id="fo-slate-enc"
                        value={draft.failover?.slate?.encoder ?? ""}
                        placeholder={t("set.slateEncoderPlaceholder")}
                        onChange={(e) =>
                          setDraft({
                            ...draft,
                            failover: {
                              ...draft.failover,
                              enabled: true,
                              slate: {
                                ...draft.failover?.slate,
                                enabled: true,
                                encoder: e.target.value,
                              },
                            },
                          })
                        }
                      />
                    </div>
                    <div className="flex flex-col gap-1">
                      <Label htmlFor="fo-slate-preset">{t("set.slatePreset")}</Label>
                      <Input
                        id="fo-slate-preset"
                        value={draft.failover?.slate?.preset ?? ""}
                        placeholder={t("set.slatePresetPlaceholder")}
                        onChange={(e) =>
                          setDraft({
                            ...draft,
                            failover: {
                              ...draft.failover,
                              enabled: true,
                              slate: {
                                ...draft.failover?.slate,
                                enabled: true,
                                preset: e.target.value,
                              },
                            },
                          })
                        }
                      />
                    </div>
                  </div>
                  <MoreAbout lead={t("set.slateEncoderLead")}>{t("set.slateEncoderNote")}</MoreAbout>
                </>
              )}

              <div className="flex items-center justify-between">
                <Label htmlFor="fo-playlist">{t("set.playLoop")}</Label>
                <Switch
                  id="fo-playlist"
                  checked={draft.failover?.playlist?.enabled ?? false}
                  onCheckedChange={(v) =>
                    setDraft({
                      ...draft,
                      failover: {
                        ...draft.failover,
                        enabled: true,
                        playlist: {
                          items: draft.failover?.playlist?.items ?? [],
                          ...draft.failover?.playlist,
                          enabled: v,
                        },
                      },
                    })
                  }
                />
              </div>
              <span className="text-sm text-muted-foreground">{t("set.playLoopNote")}</span>

              {draft.failover?.playlist?.enabled && (
                <PlaylistEditor
                  items={draft.failover?.playlist?.items ?? []}
                  onChange={(items) =>
                    setDraft({
                      ...draft,
                      failover: {
                        ...draft.failover,
                        enabled: true,
                        playlist: { ...draft.failover?.playlist, enabled: true, items },
                      },
                    })
                  }
                />
              )}
            </>
          )}

        </CardContent>
      </Card>

      <SettingsGroupHeading title="settingsUx.groupAdvanced" desc="settingsUx.groupAdvancedDesc" />
      <AdvancedHiddenLine settings={settings} inError={saveError !== null} cards={ADVANCED_CARDS} />

      <AdvancedCard card="set.mqtt" settings={settings} inError={saveError !== null}>
        <Card>
          <CardHeader>
            <CardTitle>{t("set.mqtt")}</CardTitle>
            <CardDescription>{t("set.mqttDesc")}          </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="flex items-center justify-between">
              <Label htmlFor="mq-enabled">{t("set.enabled")}</Label>
              <Switch
                id="mq-enabled"
                checked={draft.mqtt?.enabled ?? false}
                onCheckedChange={(v) => setDraft({ ...draft, mqtt: { ...draft.mqtt, enabled: v } })}
              />
            </div>
            <span className="text-sm text-muted-foreground">
              {tn("set.mqtt5Why", { only: <strong>{t("set.mqtt5Only")}</strong> })}
            </span>

            {draft.mqtt?.enabled && (
              <>
                <div className="flex flex-col gap-1">
                  <Label htmlFor="mq-url">{t("set.brokerUrl")}</Label>
                  {/* The placeholder suggests mqtts, not mqtt. A placeholder is a
                      suggestion, and the encrypted scheme is the better thing to
                      suggest — the help below already says all four schemes work,
                      and the warning under it explains when plaintext is a
                      reasonable choice on a trusted LAN. It also stops
                      SonarCloud's S5332 reading illustrative text in an empty
                      field as an insecure connection. */}
                  <Input
                    id="mq-url"
                    value={draft.mqtt?.brokerUrl ?? ""}
                    placeholder={t("set.brokerUrlPlaceholder")}
                    onChange={(e) =>
                      setDraft({ ...draft, mqtt: { ...draft.mqtt, enabled: true, brokerUrl: e.target.value } })
                    }
                  />
                  <span className="text-sm text-muted-foreground">
                    {t("set.brokerUrlNote")}
                  </span>
                  {/* Said at the point of configuration rather than left to the
                      docs, because the credential is encrypted at rest and then
                      sent in the clear -- which is the sort of gap an operator
                      reasonably assumes has been closed. Not a refusal: mqtt:// on
                      a trusted LAN is the normal Home Assistant setup and what our
                      own documentation recommends. */}
                  {(draft.mqtt?.brokerUrl ?? "").startsWith("mqtt://") && (
                    <span className="text-sm text-warn">
                      {t("set.brokerPlainNote")}
                    </span>
                  )}
                </div>

                <div className="grid grid-cols-2 gap-2">
                  <div className="flex flex-col gap-1">
                    <Label htmlFor="mq-user">{t("set.username")}</Label>
                    <Input
                      id="mq-user"
                      value={draft.mqtt?.username ?? ""}
                      onChange={(e) =>
                        setDraft({ ...draft, mqtt: { ...draft.mqtt, enabled: true, username: e.target.value } })
                      }
                    />
                  </div>
                  <div className="flex flex-col gap-1">
                    <Label htmlFor="mq-pw">{t("set.password")}</Label>
                    <SecretInput
                      id="mq-pw"
                      value={mqttPassword}
                      placeholder={draft.mqtt?.hasPassword ? t("set.unchangedPlaceholder") : ""}
                      onChange={(e) => setMqttPassword(e.target.value)}
                    />
                  </div>
                </div>
                <span className="text-sm text-muted-foreground">{t("set.passwordNote")}</span>
                {draft.mqtt?.hasPassword && (
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => {
                      setMqttPassword("");
                      void onClearMqttPassword();
                    }}
                  >
                    {t("i18nSweep.clearStoredPassword")}
                  </Button>
                )}

                <div className="grid grid-cols-2 gap-2">
                  <div className="flex flex-col gap-1">
                    <Label htmlFor="mq-prefix">{t("set.topicPrefix")}</Label>
                    <Input
                      id="mq-prefix"
                      value={draft.mqtt?.prefix ?? "polyemesis"}
                      onChange={(e) =>
                        setDraft({ ...draft, mqtt: { ...draft.mqtt, enabled: true, prefix: e.target.value } })
                      }
                    />
                  </div>
                  <div className="flex flex-col gap-1">
                    <Label htmlFor="mq-inst">{t("set.instance")}</Label>
                    <Input
                      id="mq-inst"
                      value={draft.mqtt?.instance ?? "polyemesis"}
                      onChange={(e) =>
                        setDraft({ ...draft, mqtt: { ...draft.mqtt, enabled: true, instance: e.target.value } })
                      }
                    />
                  </div>
                </div>
                <span className="text-sm text-muted-foreground">
                  {t("set.topicPrefixNote")}
                </span>

                <div className="grid grid-cols-2 gap-2">
                  <div className="flex flex-col gap-1">
                    <Label htmlFor="mq-int">{t("set.publishInterval")}</Label>
                    <Input
                      id="mq-int"
                      type="number"
                      min={1}
                      max={3600}
                      value={draft.mqtt?.intervalSeconds ?? 10}
                      onChange={(e) =>
                        setDraft({
                          ...draft,
                          mqtt: { ...draft.mqtt, enabled: true, intervalSeconds: Number(e.target.value) },
                        })
                      }
                    />
                  </div>
                  <div className="flex flex-col gap-1">
                    <Label htmlFor="mq-ka">{t("set.keepAlive")}</Label>
                    <Input
                      id="mq-ka"
                      type="number"
                      min={1}
                      max={65535}
                      value={draft.mqtt?.keepAliveSeconds ?? 30}
                      onChange={(e) =>
                        setDraft({
                          ...draft,
                          mqtt: { ...draft.mqtt, enabled: true, keepAliveSeconds: Number(e.target.value) },
                        })
                      }
                    />
                  </div>
                </div>
                <span className="text-sm text-muted-foreground">{t("set.intervalNote")}</span>

                <div className="flex flex-col gap-1">
                  <Label htmlFor="mq-cid">{t("set.clientId")}</Label>
                  <Input
                    id="mq-cid"
                    value={draft.mqtt?.clientId ?? ""}
                    placeholder={t("set.clientIdPlaceholder")}
                    onChange={(e) =>
                      setDraft({ ...draft, mqtt: { ...draft.mqtt, enabled: true, clientId: e.target.value } })
                    }
                  />
                  <span className="text-sm text-muted-foreground">{t("set.clientIdNote")}</span>
                </div>

                <div className="flex items-center justify-between">
                  <Label htmlFor="mq-disc">{t("set.haDiscovery")}</Label>
                  <Switch
                    id="mq-disc"
                    checked={draft.mqtt?.discovery ?? true}
                    onCheckedChange={(v) =>
                      setDraft({ ...draft, mqtt: { ...draft.mqtt, enabled: true, discovery: v } })
                    }
                  />
                </div>
                <div className="flex items-center justify-between">
                  <Label htmlFor="mq-tls">{t("set.acceptSelfSigned")}</Label>
                  <Switch
                    id="mq-tls"
                    checked={draft.mqtt?.tlsSkipVerify ?? false}
                    onCheckedChange={(v) =>
                      setDraft({ ...draft, mqtt: { ...draft.mqtt, enabled: true, tlsSkipVerify: v } })
                    }
                  />
                </div>
              </>
            )}

          </CardContent>
        </Card>
      </AdvancedCard>

      <AdvancedCard card="set.preview" settings={settings} inError={saveError !== null}>
        <Card>
          <CardHeader>
            <CardTitle>{t("set.preview")}</CardTitle>
            <CardDescription>{t("set.previewDesc")}          </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="flex items-center justify-between">
              <Label htmlFor="prev-enabled">{t("set.enabled")}</Label>
              <Switch
                id="prev-enabled"
                checked={draft.preview.enabled}
                onCheckedChange={(v) => setDraft({ ...draft, preview: { ...draft.preview, enabled: v } })}
              />
            </div>
            <div className="grid grid-cols-3 gap-2">
              <div className="flex flex-col gap-1">
                <Label htmlFor="prev-seg">{t("set.segmentSec")}</Label>
                <Input
                  id="prev-seg"
                  type="number"
                  min={1}
                  max={10}
                  value={draft.preview.segmentSeconds}
                  onChange={(e) =>
                    setDraft({
                      ...draft,
                      preview: { ...draft.preview, segmentSeconds: Number(e.target.value) },
                    })
                  }
                />
              </div>
              <div className="flex flex-col gap-1">
                <Label htmlFor="prev-h">{t("set.height")}</Label>
                <Input
                  id="prev-h"
                  type="number"
                  value={draft.preview.videoHeight}
                  onChange={(e) =>
                    setDraft({
                      ...draft,
                      preview: { ...draft.preview, videoHeight: Number(e.target.value) },
                    })
                  }
                />
              </div>
              <div className="flex flex-col gap-1">
                <Label htmlFor="prev-kbps">kbps</Label>
                <Input
                  id="prev-kbps"
                  type="number"
                  value={draft.preview.videoKbps}
                  onChange={(e) =>
                    setDraft({
                      ...draft,
                      preview: { ...draft.preview, videoKbps: Number(e.target.value) },
                    })
                  }
                />
              </div>
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="prev-idle">{t("set.stopAfterIdle")}</Label>
              <Input
                id="prev-idle"
                type="number"
                min={5}
                max={3600}
                value={draft.preview.idleTimeoutSeconds}
                onChange={(e) =>
                  setDraft({
                    ...draft,
                    preview: { ...draft.preview, idleTimeoutSeconds: Number(e.target.value) },
                  })
                }
              />
              <span className="text-sm text-muted-foreground">{t("set.previewIdleNote")}</span>
            </div>
          </CardContent>
        </Card>
      </AdvancedCard>

      <AdvancedCard card="set.audioMeters" settings={settings} inError={saveError !== null}>
        <Card>
          <CardHeader>
            <CardTitle>{t("set.audioMeters")}</CardTitle>
            <CardDescription>{t("set.metersDesc")}          </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="flex items-center justify-between">
              <Label htmlFor="met-enabled">{t("set.enabled")}</Label>
              <Switch
                id="met-enabled"
                checked={draft.meters.enabled}
                onCheckedChange={(v) => setDraft({ ...draft, meters: { ...draft.meters, enabled: v } })}
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="met-int">{t("set.updateInterval")}</Label>
              <Input
                id="met-int"
                type="number"
                min={40}
                max={2000}
                value={draft.meters.intervalMs}
                onChange={(e) =>
                  setDraft({ ...draft, meters: { ...draft.meters, intervalMs: Number(e.target.value) } })
                }
              />
            </div>
          </CardContent>
        </Card>
      </AdvancedCard>

      <AdvancedCard card="set.processLogs" settings={settings} inError={saveError !== null}>
        <Card>
          <CardHeader>
            <CardTitle>{t("set.processLogs")}</CardTitle>
            <CardDescription>{t("settingsUx.logsShort")}</CardDescription>
          <MoreAbout>{t("set.logsDesc")}</MoreAbout>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="flex items-center justify-between">
              <Label htmlFor="log-persist">{t("set.writeLogs")}</Label>
              <Switch
                id="log-persist"
                checked={draft.logging.persistProcessLogs}
                onCheckedChange={(v) =>
                  setDraft({ ...draft, logging: { ...draft.logging, persistProcessLogs: v } })
                }
              />
            </div>
            <div className="grid grid-cols-2 gap-2">
              <div className="flex flex-col gap-1">
                <Label htmlFor="log-mb">{t("set.fileSizeMb")}</Label>
                <Input
                  id="log-mb"
                  type="number"
                  min={1}
                  max={1024}
                  value={draft.logging.maxFileMb}
                  onChange={(e) =>
                    setDraft({
                      ...draft,
                      logging: { ...draft.logging, maxFileMb: Number(e.target.value) },
                    })
                  }
                />
              </div>
              <div className="flex flex-col gap-1">
                <Label htmlFor="log-files">{t("set.filesKept")}</Label>
                <Input
                  id="log-files"
                  type="number"
                  min={1}
                  max={100}
                  value={draft.logging.maxFiles}
                  onChange={(e) =>
                    setDraft({
                      ...draft,
                      logging: { ...draft.logging, maxFiles: Number(e.target.value) },
                    })
                  }
                />
              </div>
            </div>
            <span className="text-sm text-muted-foreground">{t("set.logsBoundNote")}</span>
          </CardContent>
        </Card>
      </AdvancedCard>

      <AdvancedCard card="set.ebTitle" settings={settings} inError={saveError !== null}>
        <MultitrackHardware draft={draft} setDraft={setDraft} />
      </AdvancedCard>

      </div>
    </div>
  );
}

/* ------------------------------------------------------- multitrack hardware */

/** The three PCI vendor IDs Twitch's encoders come from, in the DECIMAL
 *  spelling the wire format uses. Offered as a picker rather than a free number
 *  because a hex/decimal mix-up is the single easiest way to send a vendor ID
 *  Twitch refuses — 0x10de typed as 10 is not NVIDIA, it is nothing. */
const PCI_VENDORS = [
  { id: 4318, label: "NVIDIA (4318 / 0x10de)" },
  { id: 4098, label: "AMD (4098 / 0x1002)" },
  { id: 32902, label: "Intel (32902 / 0x8086)" },
];

/** The declared GPU inventory for Twitch Enhanced Broadcasting.
 *
 *  DECLARED RATHER THAN DETECTED, and the copy says so, because an operator who
 *  sees an empty form on a machine that plainly has a GPU will otherwise read it
 *  as a bug. polyemesis can measure exactly one of these six fields on one
 *  platform; the rest are not enumerated anywhere, and a request that filled
 *  them with zeros would describe a machine that does not exist. See
 *  db.MultitrackSettings.
 *
 *  Copy is inline English rather than catalogue keys, following the Enhanced
 *  Broadcasting toggle in DestinationDialog that this block exists to make
 *  work — the two are read together and a half-translated pair is worse than an
 *  untranslated one. */
/** No Save of its own: this card edits the Pipeline tab's one draft and the
 *  tab's one Save commits it. Its own button called `onSave(draft)` like the
 *  other seven, which is how a GPU declaration came to be saved by a button
 *  that also dropped a typed MQTT password. */
function MultitrackHardware({
  draft,
  setDraft,
}: {
  draft: Settings;
  setDraft: (s: Settings) => void;
}) {
  const t = useT();
  const gpus = draft.multitrack?.gpus ?? [];
  // Always sent as an explicit array, never omitted, so clearing the last entry
  // is a value this form can actually send. An absent key would leave the
  // stored list alone and the operator would watch a delete undo itself.
  const write = (next: MultitrackGpu[]) =>
    setDraft({ ...draft, multitrack: { gpus: next } });

  const patch = (i: number, p: Partial<MultitrackGpu>) =>
    write(gpus.map((g, n) => (n === i ? { ...g, ...p } : g)));

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-1.5">
          {t("set.ebTitle")}
          <ExperimentalBadge />
        </CardTitle>
        <CardDescription>{t("set.ebDesc")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {/* The form is unchanged and still fully usable. This says only what
            polyemesis has and has not seen, which is a different question from
            what Twitch documents. It is also the one place an operator can act
            on knowing that Twitch reads the DECLARATION, so the mechanism is
            stated here rather than left to be inferred from a refusal. */}
        <Experimental>{t("set.ebExperimental")}</Experimental>
        <MoreAbout lead={t("set.ebGpuLead")}>{t("set.ebGpuWhy")}</MoreAbout>
        {gpus.map((g, i) => (
          <div key={i} className="flex flex-col gap-2 rounded-md border border-border p-2">
            <div className="flex flex-wrap items-end gap-2">
              <div className="flex min-w-56 flex-1 flex-col gap-1">
                <Label htmlFor={`mt-model-${i}`}>{t("automod.modelName")}</Label>
                <Input
                  id={`mt-model-${i}`}
                  value={g.model ?? ""}
                  placeholder="NVIDIA GeForce RTX 4070"
                  onChange={(e) => patch(i, { model: e.target.value })}
                />
              </div>
              <div className="flex flex-col gap-1">
                <Label htmlFor={`mt-vendor-${i}`}>{t("i18nSweep.gpuVendor")}</Label>
                <Select
                  value={g.vendorId ? String(g.vendorId) : ""}
                  onValueChange={(v) => patch(i, { vendorId: Number(v) })}
                >
                  <SelectTrigger id={`mt-vendor-${i}`} className="w-52">
                    <SelectValue placeholder={t("i18nSweep.gpuChooseVendor")} />
                  </SelectTrigger>
                  <SelectContent>
                    {PCI_VENDORS.map((v) => (
                      <SelectItem key={v.id} value={String(v.id)} data-value={String(v.id)}>
                        {v.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="flex flex-col gap-1">
                <Label htmlFor={`mt-driver-${i}`}>{t("i18nSweep.gpuDriver")}</Label>
                <Input
                  id={`mt-driver-${i}`}
                  value={g.driverVersion ?? ""}
                  placeholder="550.54.14"
                  className="w-36"
                  onChange={(e) => patch(i, { driverVersion: e.target.value })}
                />
              </div>
              <Button
                size="sm"
                variant="ghost"
                aria-label={t("i18nSweep.gpuRemove")}
                onClick={() => write(gpus.filter((_, n) => n !== i))}
              >
                <Trash2 />
              </Button>
            </div>
            <div className="flex flex-wrap gap-2">
              <div className="flex flex-col gap-1">
                <Label htmlFor={`mt-device-${i}`}>{t("i18nSweep.gpuDeviceId")}</Label>
                <Input
                  id={`mt-device-${i}`}
                  type="number"
                  min={0}
                  className="w-32"
                  value={g.deviceId ?? 0}
                  onChange={(e) => patch(i, { deviceId: Number(e.target.value) })}
                />
              </div>
              <div className="flex flex-col gap-1">
                <Label htmlFor={`mt-vram-${i}`}>{t("i18nSweep.gpuVram")}</Label>
                <Input
                  id={`mt-vram-${i}`}
                  type="number"
                  min={0}
                  className="w-44"
                  value={g.dedicatedVideoMemory ?? 0}
                  onChange={(e) => patch(i, { dedicatedVideoMemory: Number(e.target.value) })}
                />
              </div>
              {/* The sixth field, and the one the other five were shipped
                  without. It goes into the go-live body beside them
                  (docs/HOT-RELOAD.md lists all six), so an inventory typed here
                  described a GPU with no shared memory at all -- which is a
                  claim about the machine rather than a blank, and this is a
                  declaration Twitch validates rather than a form it ignores.

                  BYTES, like the field above it and for the same reason: bytes
                  is the unit the wire format uses, and converting in the form
                  would put a unit mismatch between what is typed and what is
                  sent. An integrated GPU shares system RAM and this is where
                  that shows; a discrete card usually reports a smaller figure
                  here rather than none at all. */}
              <div className="flex flex-col gap-1">
                <Label htmlFor={`mt-shared-${i}`}>{t("i18nSweep.gpuShared")}</Label>
                <Input
                  id={`mt-shared-${i}`}
                  type="number"
                  min={0}
                  className="w-44"
                  value={g.sharedSystemMemory ?? 0}
                  onChange={(e) => patch(i, { sharedSystemMemory: Number(e.target.value) })}
                />
              </div>
            </div>
            {/* Optional, and said so rather than left for the operator to
                discover by saving. A number invented to fill a box is worse
                than an empty one: Twitch checks these. */}
            <span className="text-sm text-muted-foreground">
              {t("set.ebGpuOptional")}
            </span>
          </div>
        ))}
        <div className="flex gap-2">
          <Button
            size="sm"
            variant="outline"
            onClick={() => write([...gpus, { model: "", vendorId: 0 }])}
          >
            {t("i18nSweep.gpuAdd")}
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

/* --------------------------------------------------------------- platforms */

function PlatformSettings() {
  const t = useT();
  // Three ReadStates, not three swallowed catches.
  //
  // `creds.find(...) ?? null` is what PlatformCredCard reads as "this platform
  // has no stored credentials", so a failed GET drew an UNCONFIGURED INSTALL
  // over a configured one -- and the operator's response to that screen is to
  // go and regenerate a client secret that was working. A failed
  // platformGuides() was worse still: no guides, no cards, a build that
  // apparently supports no platforms at all.
  //
  // A warning rather than a control: nothing here can make the read succeed.
  // What it can do is refuse to show setup forms it cannot honestly fill in.
  const [guides, setGuides] = useState<ReadState<SetupGuide[]>>(pendingRead);
  const [creds, setCreds] = useState<ReadState<PlatformCreds[]>>(pendingRead);
  const [accounts, setAccounts] = useState<ReadState<PlatformAccount[]>>(pendingRead);

  const load = () => {
    api.platformGuides().then((r) => setGuides(okRead(r))).catch(() => setGuides(failedRead()));
    api.listCreds().then((r) => setCreds(okRead(r))).catch(() => setCreds(failedRead()));
    api.listAccounts().then((r) => setAccounts(okRead(r))).catch(() => setAccounts(failedRead()));
  };
  useEffect(load, []);

  // The cards below claim, per platform, that nothing is stored. Only the
  // creds read can support that claim, so the cards are withheld until it has
  // -- showing an empty setup form over stored credentials is the failure.
  const credsUsable = mayClaim(creds);

  return (
    <div className="flex flex-col gap-3">
      {/* SCOPED, NOT "OPTIONAL" FULL STOP (settings-council R2, as revised):
          a pasted stream key on a destination needs none of this, but
          connecting an account does need the operator's own app. The old
          "Why you need your own developer app" card read as a mandatory first
          step; its explanation is kept, one click down. */}
      <div className="flex flex-col gap-1 rounded-md border border-primary/40 bg-primary-dim px-3 py-2 text-sm">
        <p>
          <strong>{t("settingsUx.optional")}</strong> {t("settingsUx.platformsBanner")}
        </p>
        <MoreAbout>
          <span className="font-medium text-foreground">{t("set.whyDevApp")}.</span> {t("set.whyDevAppDesc")}
        </MoreAbout>
      </div>

      {(readFailed(guides) || readFailed(creds) || readFailed(accounts)) && (
        <Card>
          <CardContent className="flex items-start gap-1.5 py-3 text-sm text-warn">
            <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
            <span>{t("set.platformReadFailed")}</span>
          </CardContent>
        </Card>
      )}

      {credsUsable && (
        <PlatformList
          guides={rowsOf(guides)}
          creds={creds.value}
          accounts={rowsOf(accounts)}
          onChanged={load}
        />
      )}

      {/* Below the rows, closed: the table pushed every platform below the
          fold, and it answers "which platform can do what", a question asked
          once, not on every visit (settings-council R1). */}
      <PlatformCapabilityMatrix />
    </div>
  );
}

/** One row per platform, connected ones first (settings-council R1).
 *
 *  Each row is a name, ONE status pill and ONE button. The credentials, the
 *  redirect URI, the accounts and the setup guide are the platform's card,
 *  opened inline under its row -- inline rather than in a modal drawer because
 *  the card itself opens confirm dialogs and the device-code dialog, and a
 *  dialog inside a dialog is a focus trap inside a focus trap. One row open at
 *  a time, so the page never grows back into the wall of forms it replaced. */
function PlatformList({
  guides,
  creds,
  accounts,
  onChanged,
}: {
  guides: SetupGuide[];
  creds: PlatformCreds[];
  accounts: PlatformAccount[];
  onChanged: () => void;
}) {
  const t = useT();
  const [open, setOpen] = useState<string | null>(null);
  const rows = guides.map((g) => {
    const c = creds.find((x) => x.platform === g.platform) ?? null;
    const a = accounts.filter((x) => x.platform === g.platform);
    return { guide: g, creds: c, accounts: a };
  });
  const connected = rows.filter((r) => r.accounts.length > 0);
  const rest = rows.filter((r) => r.accounts.length === 0);

  const renderRow = (r: (typeof rows)[number]) => (
    <PlatformRow
      key={r.guide.platform}
      guide={r.guide}
      creds={r.creds}
      accounts={r.accounts}
      open={open === r.guide.platform}
      onToggle={() => setOpen((cur) => (cur === r.guide.platform ? null : r.guide.platform))}
      onChanged={onChanged}
    />
  );

  return (
    <>
      <Card data-testid="platforms-connected">
        <CardHeader>
          <CardTitle>{t("settingsUx.connectedChannels")}</CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-0 p-0">
          {connected.length > 0 ? (
            <ul className="flex flex-col">{connected.map(renderRow)}</ul>
          ) : (
            <EmptyState
              className="m-3"
              title={t("settingsUx.noChannelsTitle")}
              body={t("settingsUx.noChannelsBody")}
            />
          )}
        </CardContent>
      </Card>
      {rest.length > 0 && (
        <Card data-testid="platforms-add">
          <CardHeader>
            <CardTitle>{t("settingsUx.addPlatform")}</CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-0 p-0">
            <ul className="flex flex-col">{rest.map(renderRow)}</ul>
          </CardContent>
        </Card>
      )}
    </>
  );
}

/** One platform's row and, when open, its card. Exported for its test. */
export function PlatformRow({
  guide,
  creds,
  accounts,
  open,
  onToggle,
  onChanged,
}: {
  guide: SetupGuide;
  creds: PlatformCreds | null;
  accounts: PlatformAccount[];
  open: boolean;
  onToggle: () => void;
  onChanged: () => void;
}) {
  const t = useT();
  const panelId = `platform-panel-${guide.platform}`;
  const reading = platformSetupStatus({
    supported: guide.supported,
    hasSecret: creds?.hasSecret ?? false,
    lastCheckState: creds?.lastCheck?.state,
    accounts: accounts.length,
    reconnectNeeded: accounts.some((a) => a.reconnect?.needed),
  });
  // ONE button, and its word says what opening the row is for.
  const action: TranslationKey = !guide.supported
    ? "settingsUx.details"
    : creds?.hasSecret || accounts.length > 0
      ? "settingsUx.manage"
      : "settingsUx.setUp";
  return (
    <li className="border-t border-border first:border-t-0" data-platform-row={guide.platform}>
      <div className="flex flex-wrap items-center gap-2 px-3 py-2">
        <div className="flex min-w-0 flex-1 flex-col">
          <span className="text-sm font-medium">{guide.name}</span>
          {accounts.length > 0 && (
            <span className="truncate text-sm text-muted-foreground">
              {accounts.map((a) => a.accountName).join(", ")}
            </span>
          )}
        </div>
        <StatusPill reading={reading} />
        <Button
          size="sm"
          variant={action === "settingsUx.setUp" ? "default" : "outline"}
          aria-expanded={open}
          aria-controls={panelId}
          onClick={onToggle}
          aria-label={`${t(action)}: ${guide.name}`}
        >
          {t(action)}
          <ChevronDown aria-hidden className={cn("transition-transform", open && "rotate-180")} />
        </Button>
      </div>
      {open && (
        <div id={panelId} className="border-t border-border bg-background/40 p-3">
          <PlatformCredCard guide={guide} creds={creds} accounts={accounts} onChanged={onChanged} />
        </div>
      )}
    </li>
  );
}

/** The honest answer to "what do I actually get?", above the setup forms
 *  rather than below them.
 *
 *  Every row of this table was a question somebody would otherwise only answer
 *  by spending an evening: Kick signs in but never hands over a key; Facebook
 *  works completely and needs Meta's App Review first; Instagram cannot be
 *  streamed to at all. Reading it takes thirty seconds and it is deliberately
 *  the first thing on this tab. */
function PlatformCapabilityMatrix() {
  const t = useT();
  // Closed by default (settings-council R1): see PlatformSettings.
  const [open, setOpen] = useState(false);
  return (
    <Card>
      <CardHeader className="flex-row items-start justify-between gap-3">
        <div className="flex flex-col gap-1">
          <CardTitle>{t("set.whatPlatformsDo")}</CardTitle>
          <CardDescription>{t("set.platformsDesc")}</CardDescription>
        </div>
        <Button
          size="sm"
          variant="outline"
          aria-expanded={open}
          aria-controls="platform-capability-table"
          onClick={() => setOpen((v) => !v)}
        >
          {open ? t("settingsUx.hideTable") : t("settingsUx.showTable")}
        </Button>
      </CardHeader>

      {open && (
      <CardContent id="platform-capability-table" className="flex flex-col gap-3">
        <div className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("set.platform")}</TableHead>
                {CAPABILITY_COLUMNS.map((c) => (
                  <TableHead key={c.key} title={t(c.help)}>
                    {t(c.label)}
                  </TableHead>
                ))}
              </TableRow>
            </TableHeader>
            <TableBody>
              {PLATFORM_CAPABILITIES.map((p) => (
                <TableRow key={p.presetId}>
                  <TableCell>
                    <div className="flex flex-col gap-0.5">
                      <span
                        className={
                          p.tier === "unsupported" ? "text-muted-foreground" : undefined
                        }
                      >
                        {p.name}</span>
                      <Badge
                        variant={p.tier === "unsupported" ? "warn" : "outline"}
                        className="w-fit"
                        title={t(tierInfo(p.tier).help)}
                      >
                        {t(tierInfo(p.tier).label)}
                      </Badge>
                    </div>
                  </TableCell>
                  {CAPABILITY_COLUMNS.map((c) => {
                    const info = supportInfo(supportOf(p, c.key));
                    return (
                      <TableCell key={c.key}>
                        {/* The reason, where there is one, is the cell's own
                            tooltip: a tick with no explanation is how a claim
                            becomes folklore. */}
                        <Badge
                          variant={info.variant}
                          className="normal-case"
                          title={t(p.reasons?.[c.key] ?? info.help)}
                        >
                          {t(info.label)}
                        </Badge>
                      </TableCell>
                    );
                  })}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>

        <div className="flex flex-wrap gap-x-3 gap-y-1">
          {SUPPORT_LEGEND.map((l) => (
            <span key={l.key} className="flex items-center gap-1.5">
              <Badge variant={l.variant} className="normal-case">
                {t(l.label)}
              </Badge>
              <span className="text-sm text-muted-foreground">{t(l.help)}</span>
            </span>
          ))}
        </div>

        {/* "Unverified" is the load-bearing value here and the one most likely
            to be misread as "no". Saying it plainly costs a sentence and saves
            an operator concluding a feature is blocked when it was only never
            checked. */}
        <p className="text-sm text-muted-foreground">
          {t("caps.unverifiedMeans", {
            unverified: t("caps.supportUnknown"),
            no: t("caps.supportNo"),
          })}
        </p>

        {PLATFORM_CAPABILITIES.filter((p) => p.readFirst).map((p) => (
          <div
            key={p.presetId}
            className={
              p.tier === "unsupported"
                ? "flex flex-col gap-1 rounded-md border border-warn/40 bg-warn-dim p-2"
                : "flex flex-col gap-1 rounded-md border border-dashed p-2"
            }
          >
            <div className="flex items-center gap-1.5">
              <span className="text-sm font-medium">{p.name}</span>
              <Badge variant={p.tier === "unsupported" ? "warn" : "outline"}>{t("caps.readFirstBadge")}</Badge>
            </div>
            <p
              className={
                p.tier === "unsupported" ? "text-sm text-warn" : "text-sm text-muted-foreground"
              }
            >
              {p.readFirst && t(p.readFirst)}
            </p>
          </div>
        ))}
      </CardContent>
      )}
    </Card>
  );
}

/** Exported for its test, like VariantsCard in PlayoutPage.
 *
 *  SettingsPage.disconnect.test.tsx drives the disconnect flow through this
 *  card rather than through the whole settings page, which would have to stand
 *  up TLS status, system info and the API-token list to ask a question about
 *  one button. */
export function PlatformCredCard({
  guide,
  creds,
  accounts,
  onChanged,
}: {
  guide: SetupGuide;
  creds: PlatformCreds | null;
  accounts: PlatformAccount[];
  onChanged: () => void;
}) {
  const t = useT();
  const [clientId, setClientId] = useState(creds?.clientId ?? "");
  const [clientSecret, setClientSecret] = useState("");
  const [busy, setBusy] = useState(false);
  const [copied, setCopied] = useState(false);
  // SEEDED FROM THE STORED VERDICT (settings-council R10). It used to start
  // null on every load, so a reload turned "the platform refused this pair"
  // back into a plain "configured". `checkedAt` is absent only for a verdict
  // this card has just been handed by a Save or Re-check, which is "now".
  const [check, setCheck] = useState<(CredentialCheck & { checkedAt?: string }) | StoredCredentialCheck | null>(
    creds?.lastCheck ?? null,
  );
  const [rechecking, setRechecking] = useState(false);
  const labels = credentialLabelsFor(guide.platform);

  useEffect(() => setClientId(creds?.clientId ?? ""), [creds]);
  useEffect(() => {
    if (creds?.lastCheck) setCheck(creds.lastCheck);
  }, [creds]);

  // THE DEVELOPER-APP PAIR IS THE ADVANCED LAYER (settings-council R2): open
  // when there is nothing stored yet (connecting is impossible without it) or
  // when the platform refused what is stored; closed once the pair works, so a
  // connected platform shows its accounts first.
  // Show advanced settings opens it too (settings-council R7: one switch for
  // every advanced layer on the page, not only the Pipeline cards).
  const { show: showAdvanced } = useContext(AdvancedContext);
  const [credsOpen, setCredsOpen] = useState(
    showAdvanced || !creds?.hasSecret || creds?.lastCheck?.state === "rejected",
  );

  // ONE WARNING PER CARD, the one that applies now (settings-council R16). A
  // redirect problem the server found for THIS origin blocks connecting today,
  // so it outranks the platform's standing caveat; whichever is not shown here
  // is in the setup guide below, not lost.
  const warning = guide.redirectWarnings?.[0] ?? guide.note ?? "";
  const saveReasonId = useId();
  const saveReason =
    !busy && (!clientId.trim() || !clientSecret.trim()) ? t("settingsUx.saveCredsNeedsBoth") : null;
  const otherWarnings = [
    ...(guide.redirectWarnings ?? []).filter((w) => w !== warning),
    ...(guide.note && guide.note !== warning ? [guide.note] : []),
  ];

  const save = async () => {
    setBusy(true);
    try {
      const saved = await api.putCreds(guide.platform, clientId.trim(), clientSecret.trim());
      setCheck(saved.check ? { ...saved.check, checkedAt: new Date().toISOString() } : null);
      // Saved is saved, whatever the checker thought. The verdict renders
      // below; it does not turn a successful save into a failure toast.
      toast.success(t("i18nSweep.credentialsSaved", { name: guide.name }));
      setClientSecret("");
      onChanged();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("set.couldNotSaveTheCredentials"));
    } finally {
      setBusy(false);
    }
  };

  const recheck = async () => {
    setRechecking(true);
    try {
      setCheck({ ...(await api.checkCreds(guide.platform)), checkedAt: new Date().toISOString() });
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("set.couldNotReCheckThe"));
    } finally {
      setRechecking(false);
    }
  };

  const confirmCreds = useConfirm<{ name: string }>();
  const confirmDisconnect = useConfirm<PlatformAccount>();
  // Which ways this card offers to connect an account, and whether the device
  // code flow is one of them. Both are the SERVER'S answer -- see
  // lib/platformConnect.ts, which holds the rules and the reasons.
  const connectControls = platformConnectControls(guide, creds);
  const [deviceOpen, setDeviceOpen] = useState(false);

  const remove = async () => {
    await api.deleteCreds(guide.platform);
    toast.success(t("set.credsRemoved"));
    onChanged();
  };

  /* THE 409 IS THE ORDINARY ANSWER HERE, not an edge case.
   *
   * The server refuses an unconfirmed disconnect while any destination on the
   * account is enabled or mid-broadcast, and a normal install leaves its
   * destinations enabled — so most presses of this button get the refusal back
   * the first time. Before this branch existed the rejection escaped into an
   * unhandled promise: no dialog, no toast, no row removed. The button simply
   * did nothing, on most accounts, and looked like a broken build rather than a
   * guard doing its job.
   *
   * The state below is the operator's second decision, held separately from
   * `confirmDisconnect` because it is a different question. The first dialog
   * asks "disconnect this account?"; this one answers "not while these are
   * still on it — here they are, still want to?" and carries the server's own
   * sentence rather than a paraphrase, so a terminal and a browser say the same
   * thing. */
  const [inUse, setInUse] = useState<{
    account: PlatformAccount;
    message: string;
    destinations: AccountDestination[];
  } | null>(null);

  const disconnected = () => {
    toast.success(t("set.accountDisconnected"));
    onChanged();
  };

  const disconnect = async (a: PlatformAccount) => {
    try {
      await api.deleteAccount(a.id);
    } catch (err) {
      if (isAccountInUse(err)) {
        // Nothing was deleted. The account is still connected and the
        // destinations still point at it, so there is no half-done state to
        // report — only a question to ask.
        setInUse({ account: a, message: err.message, destinations: err.destinations });
        return;
      }
      toast.error(err instanceof Error ? err.message : t("set.couldNotDisconnect"));
      return;
    }
    disconnected();
  };

  const disconnectAnyway = async () => {
    if (!inUse) return;
    try {
      await api.confirmDeleteAccount(inUse.account.id);
    } catch (err) {
      // SWALLOWED HERE, DELIBERATELY, and reported as a toast. Rethrowing would
      // leave the dialog open, which reads better -- but ConfirmDestructive
      // awaits this without a catch of its own, so the rejection would escape
      // into an unhandled promise and the operator would see nothing at all.
      // That is the exact failure this whole change exists to end.
      //
      // The dialog closes either way: ConfirmDestructive calls onOpenChange
      // (false) as soon as onConfirm RESOLVES, and returning resolves it. The
      // account is still connected, so pressing Disconnect again re-runs the
      // same refusal and brings the same list back.
      toast.error(err instanceof Error ? err.message : t("set.couldNotDisconnect"));
      return;
    }
    setInUse(null);
    disconnected();
  };

  const copyRedirect = async () => {
    await navigator.clipboard.writeText(guide.redirectPath);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  };

  const verdictBadge = check && (
    <Badge
      variant={
        check.state === "verified"
          ? "live"
          : check.state === "rejected"
            ? "down"
            : check.state === "unreachable"
              ? "warn"
              : "outline"
      }
    >
      {check.state === "verified" && t("i18nSweep.credVerified")}
      {check.state === "rejected" && t("i18nSweep.credRejected")}
      {check.state === "unverified" && t("i18nSweep.credUnverifiable")}
      {check.state === "unreachable" && t("i18nSweep.credUnreachable")}
    </Badge>
  );

  return (
    <Card>
      <CardHeader className="flex-row items-start justify-between">
        <div className="flex flex-col gap-1">
          <CardTitle className="flex items-center gap-2">
            {guide.name}
            {/* The row's word for it (settings-council R11: one term). */}
            {!guide.supported && <Badge variant="outline">{t("settingsUx.pillManualKey")}</Badge>}
            {creds?.hasSecret && <Badge variant="live">{t("i18nSweep.configured")}</Badge>}
          </CardTitle>
        </div>
        {guide.consoleUrl && (
          <Button variant="outline" size="sm" asChild>
            <a href={guide.consoleUrl} target="_blank" rel="noreferrer noopener">
              <ExternalLink /> {t("i18nSweep.console")}
            </a>
          </Button>
        )}
      </CardHeader>

      <CardContent className="flex flex-col gap-3">
        {warning && (
          <div
            data-testid="platform-warning"
            className="flex items-start gap-1.5 rounded border border-warn/30 bg-warn-dim px-2 py-1.5 text-sm text-warn"
          >
            <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
            <span>{warning}</span>
          </div>
        )}

        {guide.supported && (
          <>
            {/* THE CONNECTED LAYER FIRST (settings-council R2): what most
                operators come here for is the account, not the app. */}
            <div className="flex flex-col gap-1.5">
              <Label>{t("set.connectedAccounts")}</Label>
              {accounts.length === 0 && (
                <span className="text-sm text-muted-foreground">{t("settingsUx.noAccountsYet")}</span>
              )}
              {accounts.map((a) => (
                <div
                  key={a.id}
                  className="flex items-center justify-between rounded border border-border bg-background px-2 py-1.5"
                >
                  <div className="min-w-0">
                    <div className="flex items-center gap-1.5">
                      <span className="truncate text-sm">{a.accountName}</span>
                      {a.reconnect?.needed && (
                        <Badge variant="outline" className="border-warn text-sm text-warn">
                          {t("i18nSweep.reconnectNeeded")}
                        </Badge>
                      )}
                    </div>
                    <div className="font-mono text-sm text-muted-foreground">{a.accountRef}</div>
                    {/* What the capability matrix promises, on the account it
                        is a promise about. Polled while this row is open; see
                        lib/viewerCount.ts for why the interval is what it is. */}
                    <AccountLiveStats accountId={a.id} />
                    {/* Said in full rather than as a bare badge: a token cannot
                        gain a permission it was not issued with, which is
                        nobody's mistake. */}
                    {a.reconnect?.needed && (
                      <div className="mt-0.5 text-sm text-warn">
                        {a.reconnect.reason}
                        {a.reconnect.missing && a.reconnect.missing.length > 0 && (
                          <> {t("i18nSweep.missingScopes")} <code>{a.reconnect.missing.join(", ")}</code></>
                        )}
                      </div>
                    )}
                  </div>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    onClick={() => confirmDisconnect.ask(a)}
                    aria-label={t("set.disconnect")}
                  >
                    <Unplug />
                  </Button>
                </div>
              ))}
              {accounts.length > 0 && (
                <span className="text-sm text-muted-foreground">{t("set.multiAccountNote")}</span>
              )}
              {/* Which of these the card offers is decided in
                  lib/platformConnect.ts, with the reasons. Nothing is offered
                  before the app credentials are stored, and the line below says
                  so rather than leaving an empty row. */}
              <div className="flex flex-wrap gap-1.5">
                {connectControls.includes("connectRedirect") && (
                  <Button size="sm" asChild>
                    <a href={api.connectUrl(guide.platform)}>
                      <ExternalLink /> {t("i18nSweep.connectAccount")}
                    </a>
                  </Button>
                )}
                {connectControls.includes("connectWithCode") && (
                  <Button size="sm" variant="outline" onClick={() => setDeviceOpen(true)}>
                    <KeyRound /> {t("device.connectWithACode")}
                  </Button>
                )}
              </div>
              {!creds && <span className="text-sm text-muted-foreground">{t("settingsUx.connectNeedsApp")}</span>}
            </div>

            {/* THE ADVANCED LAYER: the developer app's pair (settings-council
                R2). A native disclosure, so the fields stay in the page for the
                search and for a screen reader's form list. */}
            <details
              open={credsOpen}
              onToggle={(e) => setCredsOpen((e.currentTarget as HTMLDetailsElement).open)}
              className="rounded-md border border-border"
            >
              <summary className="flex cursor-pointer list-none flex-wrap items-center gap-2 px-2 py-1.5 text-sm font-medium">
                <ChevronDown aria-hidden className={cn("h-3.5 w-3.5 transition-transform", credsOpen && "rotate-180")} />
                {t("settingsUx.appCredentials")}
                {verdictBadge}
                {check?.checkedAt && (
                  <span className="text-sm font-normal text-muted-foreground">
                    {t("settingsUx.lastChecked", { time: timestamp(check.checkedAt) })}
                  </span>
                )}
              </summary>
              <div className="flex flex-col gap-3 border-t border-border p-2">
                <span className="text-sm text-muted-foreground">{t("settingsUx.appCredentialsHint")}</span>
                {/* Labelled in the platform's own words (Meta's "App ID", X's
                    "OAuth 2.0 Client ID"), with where to find each one, because
                    the operator is copying from that platform's console. The
                    values are still clientId / clientSecret on the wire -- see
                    lib/platformCredentialLabels.ts. */}
                <div className="grid gap-2 sm:grid-cols-2">
                  <div className="flex flex-col gap-1">
                    <Label htmlFor={`cid-${guide.platform}`}>{t(labels.id)}</Label>
                    <Input
                      id={`cid-${guide.platform}`}
                      value={clientId}
                      onChange={(e) => setClientId(e.target.value)}
                      aria-describedby={labels.idWhere ? `cid-${guide.platform}-where` : undefined}
                      className="font-mono"
                    />
                    {labels.idWhere && (
                      <span id={`cid-${guide.platform}-where`} className="text-sm text-muted-foreground">
                        {t(labels.idWhere)}
                      </span>
                    )}
                  </div>
                  <div className="flex flex-col gap-1">
                    <Label htmlFor={`cs-${guide.platform}`}>{t(labels.secret)}</Label>
                    <SecretInput
                      id={`cs-${guide.platform}`}
                      value={clientSecret}
                      placeholder={creds?.hasSecret ? t("i18nSweep.storedEncrypted") : ""}
                      onChange={(e) => setClientSecret(e.target.value)}
                      aria-describedby={labels.secretWhere ? `cs-${guide.platform}-where` : undefined}
                      className="font-mono"
                    />
                    {labels.secretWhere && (
                      <span id={`cs-${guide.platform}-where`} className="text-sm text-muted-foreground">
                        {t(labels.secretWhere)}
                      </span>
                    )}
                  </div>
                </div>

                {/* The verdict, in full. Four distinct words, and four distinct
                    colours. "not verifiable" must never read as "verified", and
                    "could not check" must never read as "rejected". */}
                {check && (
                  <div className="flex items-start gap-2 text-sm">
                    {verdictBadge}
                    <span className="text-muted-foreground">{check.detail}</span>
                  </div>
                )}

                <div className="flex flex-wrap gap-1.5">
                  <Button
                    size="sm"
                    onClick={save}
                    disabled={busy || !clientId.trim() || !clientSecret.trim()}
                    aria-describedby={disabledDescribedBy(saveReasonId, saveReason)}
                  >
                    {busy ? <Loader2 className="animate-spin" /> : <Save />} {t("i18nSweep.saveCredentials")}
                  </Button>
                  {creds?.hasSecret && (
                    <Button size="sm" variant="outline" onClick={recheck} disabled={rechecking}>
                      {rechecking ? <Loader2 className="animate-spin" /> : <RefreshCw />} {t("i18nSweep.recheck")}
                    </Button>
                  )}
                  {connectControls.includes("removeCredentials") && (
                    <Button size="sm" variant="ghost" onClick={() => confirmCreds.ask({ name: guide.name })}>
                      <Trash2 /> {t("i18nSweep.remove")}
                    </Button>
                  )}
                </div>

                {/* Why Save is greyed, in words (the kit's DisabledReason): the
                    usual case is an operator editing only the ID of a pair whose
                    secret is stored and can never be shown back. */}
                <DisabledReason id={saveReasonId} reason={saveReason} />

                {guide.redirectPath && (
                  <div className="flex flex-col gap-1">
                    <Label>{t("set.redirectUri")}</Label>
                    <div className="flex items-center gap-2 rounded border border-border bg-background px-2 py-1.5">
                      <code className="min-w-0 flex-1 break-all font-mono text-sm">{guide.redirectPath}</code>
                      <Button variant="ghost" size="icon-sm" onClick={copyRedirect} aria-label={t("set.copy")}>
                        {copied ? <Check className="text-live" /> : <Copy />}
                      </Button>
                    </div>
                    <span className="text-sm text-muted-foreground">{t("set.redirectNote")}</span>
                  </div>
                )}
                {guide.scopes && guide.scopes.length > 0 && (
                  <div className="flex flex-wrap gap-1">
                    {guide.scopes.map((sc) => (
                      <Badge key={sc} variant="outline" className="font-mono normal-case">
                        {sc}
                      </Badge>
                    ))}
                  </div>
                )}
              </div>
            </details>
          </>
        )}

        {/* The setup guide, closed: plain words for what it is and how long
            (settings-council R16, R11). The warnings not shown above live
            here rather than stacking at the top of the card. */}
        <Accordion type="single" collapsible>
          <AccordionItem value="steps" className="border-b-0">
            <AccordionTrigger>{t("settingsUx.setupGuide", { n: guide.steps.length })}</AccordionTrigger>
            <AccordionContent>
              <ol className="flex list-decimal flex-col gap-1.5 pl-4 text-sm text-muted-foreground">
                {guide.steps.map((step, i) => (
                  <li key={i}>{step}</li>
                ))}
              </ol>
              {otherWarnings.map((w) => (
                <div
                  key={w}
                  className="mt-2 flex items-start gap-1.5 rounded border border-warn/30 bg-warn-dim px-2 py-1 text-sm text-warn"
                >
                  <AlertTriangle className="mt-px size-3 shrink-0" />
                  <span>{w}</span>
                </div>
              ))}
            </AccordionContent>
          </AccordionItem>
        </Accordion>
      </CardContent>
      <ConfirmDestructive
        open={confirmCreds.open}
        onOpenChange={confirmCreds.onOpenChange}
        subject={confirmCreds.target?.name ?? ""}
        title={t("set.removeCredsTitle")}
        description={t("set.removeCredsDesc")}
        requireTyping
        confirmLabel={t("set.removeCreds")}
        onConfirm={remove}
      />
      <ConfirmDestructive
        open={confirmDisconnect.open}
        onOpenChange={confirmDisconnect.onOpenChange}
        subject={confirmDisconnect.target?.accountName ?? ""}
        title={t("set.disconnectTitle")}
        description={t("set.disconnectDesc")}
        confirmLabel={t("set.disconnect")}
        onConfirm={async () => {
          if (confirmDisconnect.target) await disconnect(confirmDisconnect.target);
        }}
      />
      {/* THE SERVER'S REFUSAL, RENDERED. Second dialog rather than a branch
          inside the first, because the first one has already been dismissed by
          the time the 409 arrives and its subject is a different question.

          requireTyping, and it earns it: ON DELETE SET NULL rewrites every one
          of these rows in place, reconnecting mints a NEW account id, and the
          destinations' account_id is already NULL by then — so there is no
          undo, only re-linking each destination by hand. That is exactly the
          "does not come back" that ConfirmDestructive reserves typing for.

          The sentence is the server's, verbatim. The same refusal reaches a
          terminal, and two phrasings is how they come to disagree. */}
      <ConfirmDestructive
        open={inUse !== null}
        onOpenChange={(o) => {
          if (!o) setInUse(null);
        }}
        subject={inUse?.account.accountName ?? ""}
        title={t("set.disconnectInUseTitle")}
        description={
          <>
            {inUse?.message}
            {inUse && inUse.destinations.length > 0 && (
              <span className="mt-2 flex flex-col gap-0.5">
                <span className="text-sm uppercase tracking-wider text-muted-foreground">
                  {t("set.disconnectInUseList")}
                </span>
                {/* NAMED, not counted. "3 destinations" is not something an
                    operator can act on; "Main YouTube" is — and the one that
                    is mid-broadcast is the one they most need to see, because
                    disconnecting leaves it live with nothing able to end it. */}
                {inUse.destinations.map((d) => (
                  <span key={d.id} className="text-sm">
                    {d.name}
                    {d.broadcasting && (
                      <span className="text-warn"> — {t("set.disconnectMidBroadcast")}</span>
                    )}
                  </span>
                ))}
              </span>
            )}
          </>
        }
        requireTyping
        confirmLabel={t("set.disconnectAnyway")}
        onConfirm={disconnectAnyway}
      />
      {platformSupportsDeviceCode(guide) && (
        <DeviceCodeDialog
          platform={guide.platform}
          platformName={guide.name}
          open={deviceOpen}
          onOpenChange={setDeviceOpen}
          onConnected={onChanged}
        />
      )}
    </Card>
  );
}

/* ---------------------------------------------------------------- security */

function SecuritySettings({ system }: { system: SystemInfo | null }) {
  const t = useT();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);

  const change = async (e: React.FormEvent) => {
    e.preventDefault();
    if (next !== confirm) {
      toast.error(t("set.passwordMismatch"));
      return;
    }
    setBusy(true);
    try {
      await api.changePassword(current, next);
      toast.success(t("set.passwordChanged"));
      setCurrent("");
      setNext("");
      setConfirm("");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("set.couldNotChangeThePassword"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="grid gap-3 lg:grid-cols-2">
      {/* FIRST, and full width (settings-council R9): the one thing on this
          tab a first-time operator has to DO on day one. It sat at the bottom
          of the transport card, under the Let's Encrypt walkthrough. */}
      <SecretKeyCard system={system} />

      <Card>
        <CardHeader>
          <CardTitle>{t("set.adminPassword")}</CardTitle>
        </CardHeader>
        <CardContent>
          <form onSubmit={change} className="flex flex-col gap-3">
            <div className="flex flex-col gap-1">
              <Label htmlFor="cur-pw">{t("set.currentPassword")}</Label>
              <Input
                id="cur-pw"
                type="password"
                autoComplete="current-password"
                value={current}
                onChange={(e) => setCurrent(e.target.value)}
                required
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="new-pw">{t("set.newPassword")}</Label>
              <Input
                id="new-pw"
                type="password"
                autoComplete="new-password"
                value={next}
                onChange={(e) => setNext(e.target.value)}
                required
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="conf-pw">{t("set.confirmPassword")}</Label>
              <Input
                id="conf-pw"
                type="password"
                autoComplete="new-password"
                value={confirm}
                onChange={(e) => setConfirm(e.target.value)}
                required
              />
            </div>
            <Button type="submit" size="sm" disabled={busy}>
              {busy ? <Loader2 className="animate-spin" /> : <ShieldCheck />} {t("i18nSweep.changePassword")}
            </Button>
          </form>
        </CardContent>
      </Card>

      <TransportSecurity />

      <ApiTokens />
    </div>
  );
}

/* ------------------------------------------------------- transport security */

/** Human labels for the resolved mode. `auto` never reaches here — the server
 *  resolves it — but it is listed so the map stays exhaustive over TlsMode. */
const tlsModeLabel: Record<TlsMode, TranslationKey> = {
  auto: "set.tlsAuto",
  acme: "set.tlsAcme",
  selfsigned: "set.tlsSelfsigned",
  manual: "set.tlsManual",
  off: "set.tlsOff",
};

/** Signal tone for the resolved mode. `off` is the one that depends on
 *  context: behind a trusted proxy someone else is doing TLS correctly, which
 *  is `armed`; without one the login form and session cookie cross the network
 *  in plaintext, which is exactly what `down` is for. */
function modeTone(tls: TlsStatus): SignalTone {
  switch (tls.mode) {
    case "acme":
    case "manual":
      return "live";
    case "selfsigned":
      return "warn";
    default:
      return tls.trustProxyHeaders ? "armed" : "down";
  }
}

/** Expiry is the number every operator actually watches. Thirty days is the
 *  point renewal should already have happened; seven is the point it has
 *  started going wrong. */
function expiryTone(daysRemaining: number): SignalTone {
  if (daysRemaining < 7) return "down";
  if (daysRemaining < 30) return "warn";
  return "live";
}

function expiryLabel(cert: CertInfo, t: Translator): string {
  if (cert.expired) {
    const ago = Math.abs(cert.daysRemaining);
    if (ago === 0) return t("set.certExpiredToday");
    return t(ago === 1 ? "set.certExpiredAgoOne" : "set.certExpiredAgoMany", { n: ago });
  }
  const n = cert.daysRemaining;
  return t(n === 1 ? "set.certDaysLeftOne" : "set.certDaysLeftMany", { n });
}

/** The config.yaml the current mode corresponds to, filled in with the values
 *  this server actually resolved so it is copy-pasteable rather than a sample. */
function tlsYaml(tls: TlsStatus): string {
  const host = tls.hostname || "streams.example.com";
  switch (tls.mode) {
    case "acme":
      return [
        "# config.yaml",
        "tls:",
        "  mode: acme",
        `  hostname: ${host}`,
        "  acmeEmail: you@example.com",
        "  hsts: true",
      ].join("\n");
    case "selfsigned":
      return [
        "# config.yaml",
        "tls:",
        "  mode: selfsigned",
        `  hostname: ${host}`,
        "  # hsts stays off: a browser must never be told to pin a host",
        "  # whose certificate it cannot validate.",
      ].join("\n");
    case "manual":
      return [
        "# config.yaml",
        "tls:",
        "  mode: manual",
        "  certFile: /etc/polyemesis/cert.pem",
        "  keyFile:  /etc/polyemesis/key.pem",
        "  hsts: true",
      ].join("\n");
    default:
      return [
        "# config.yaml",
        "tls:",
        "  mode: off",
        "# Set this when a reverse proxy terminates TLS in front of you, so",
        "# session cookies are marked Secure and OAuth uses your public origin.",
        "trustProxyHeaders: true",
      ].join("\n");
  }
}

/* ------------------------------------------------- the Let's Encrypt walkthrough */

/** Tone for a preflight verdict. `unknown` is `warn`, never `down`: the server
 *  says unknown exactly where it cannot see far enough — whether the public
 *  internet reaches port 80, whether a record pointing off-box is NAT or a
 *  mistake — and colouring that red would report a correct deployment as
 *  broken while giving the operator nothing to fix. */
const acmeCheckTone: Record<AcmeCheckStatus, SignalTone> = {
  pass: "live",
  fail: "down",
  unknown: "warn",
};

/** The label is the UI's; the detail underneath it is the server's own English,
 *  like `certificateError` above — those sentences name config keys, systemd
 *  directives and paths, and are what an operator pastes into a search. */
const acmeCheckLabel: Record<AcmeCheckId, TranslationKey> = {
  name: "set.acmeCheckName",
  dns: "set.acmeCheckDns",
  port80: "set.acmeCheckPort80",
  email: "set.acmeCheckEmail",
  issuance: "set.acmeCheckIssuance",
};

/** Which of the five situations this operator is in, said in one sentence.
 *  Getting this wrong in either direction is the expensive mistake: telling a
 *  working reverse-proxy deployment to fix its TLS, or telling someone on a
 *  self-signed certificate with a real domain that there is nothing to do. */
const acmeStanceCopy: Record<AcmeStance, TranslationKey> = {
  trusted: "set.acmeTrusted",
  issuing: "set.acmeIssuing",
  "own-cert": "set.acmeOwnCert",
  proxy: "set.acmeProxy",
  switchable: "set.acmeSwitchable",
};

/** Walks an operator from the certificate they have to one browsers trust.
 *
 *  IT WRITES NOTHING, and that is a decision rather than a missing feature.
 *  config.yaml is root-owned and this service cannot write it; the power to
 *  rewrite its own transport security is a privilege question, not a small
 *  convenience. The operator who needs this panel most is also reaching it over
 *  plain HTTP — self-signed certificates and `tls.mode: off` are exactly the
 *  states it exists for — and a form that takes a contact address and
 *  reconfigures the server is the wrong thing to offer over that connection.
 *  Guidance is safe to show over HTTP. So: it says what to write, checks
 *  whether it would work first, and leaves the writing to a person with a
 *  shell. */
function LetsEncryptWalkthrough({ tls }: { tls: TlsStatus }) {
  const t = useT();
  const stance = acmeStance(tls);
  // The address bar is the fallback source of the name, and only the browser
  // knows it — which keeps the server from having to trust a Host header for
  // something it will make a DNS query about.
  const [hostname, setHostname] = useState(() =>
    suggestedHostname(tls, window.location.hostname),
  );
  const [email, setEmail] = useState("");
  const [checking, setChecking] = useState(false);
  const [result, setResult] = useState<AcmePreflight | null>(null);
  const [checkError, setCheckError] = useState("");
  const [copied, setCopied] = useState(false);

  const snippet = result ? `${acmeYaml(result.hostname, email)}\n\n# ${RESTART_COMMAND}` : "";

  const run = async () => {
    setChecking(true);
    setCheckError("");
    try {
      setResult(await api.acmePreflight(hostname));
    } catch (err) {
      setResult(null);
      setCheckError(err instanceof Error ? err.message : t("set.acmeFailed"));
    } finally {
      setChecking(false);
    }
  };

  const copySnippet = async () => {
    await navigator.clipboard.writeText(snippet);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  };

  return (
    <div className="flex flex-col gap-2 border-t border-border pt-2">
      <span className="text-sm text-muted-foreground">{t("set.acmeTitle")}</span>
      <p className="text-sm text-muted-foreground">{t(acmeStanceCopy[stance])}</p>

      {offersPreflight(stance) && (
        <>
          <div className="flex flex-col gap-1">
            <Label htmlFor="acme-host" className="text-sm">
              {t("set.acmeHostname")}
            </Label>
            <Input
              id="acme-host"
              className="h-7 font-mono text-sm"
              placeholder="stream.example.com"
              value={hostname}
              onChange={(e) => setHostname(e.target.value)}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="acme-email" className="text-sm">
              {t("set.acmeEmailLabel")}
            </Label>
            {/* Never sent to this server: it only ever appears in the YAML
                below, which the operator copies into a file by hand. */}
            <Input
              id="acme-email"
              type="email"
              className="h-7 font-mono text-sm"
              placeholder="you@example.com"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
            />
            <p className="text-sm text-muted-foreground">{t("set.acmeEmailHint")}</p>
          </div>
          <Button
            size="sm"
            variant="outline"
            className="w-fit"
            onClick={run}
            disabled={checking || !hostname.trim()}
          >
            {checking ? <Loader2 className="animate-spin" /> : <ShieldCheck />}
            {checking ? t("set.acmeRunning") : t("set.acmeRun")}
          </Button>

          {checkError && <p className="text-sm text-down">{checkError}</p>}

          {result && (
            <>
              <p className={`text-sm ${result.ready ? toneText.live : toneText.down}`}>
                {result.ready ? t("set.acmeReady") : t("set.acmeBlocked")}
              </p>
              <ul className="flex flex-col gap-1.5">
                {result.checks.map((c) => (
                  <li key={c.id} className="flex flex-col">
                    <span className={`text-sm font-medium ${toneText[acmeCheckTone[c.status]]}`}>
                      {t(acmeCheckLabel[c.id])}
                    </span>
                    <span className="text-sm text-muted-foreground">{c.detail}</span>
                  </li>
                ))}
              </ul>
              {/* Said before the snippet, not after it: a failed order costs an
                  hour, and the operator reads downward. */}
              <p className="text-sm text-warn">{t("set.acmeRateLimit")}</p>
              <div className="flex items-center justify-between">
                <span className="text-sm text-muted-foreground">{t("set.acmeThen")}</span>
                <Button size="sm" variant="ghost" onClick={copySnippet}>
                  {copied ? <Check /> : <Copy />} {t("set.copy")}
                </Button>
              </div>
              <pre className="overflow-x-auto rounded border border-border bg-background p-2 font-mono text-sm text-muted-foreground">
                {snippet}
              </pre>
            </>
          )}
        </>
      )}
    </div>
  );
}

function TransportSecurity() {
  const t = useT();
  // The details below open with the page's Show advanced switch (settings-
  // council R7); closed otherwise.
  const { show: showAdvanced } = useContext(AdvancedContext);
  const tn = useTN();
  const [tls, setTls] = useState<TlsStatus | null>(null);
  const [loadError, setLoadError] = useState("");
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    api
      .tlsStatus()
      .then(setTls)
      .catch((err) =>
        setLoadError(err instanceof Error ? err.message : t("set.couldNotReadTheTls")),
      );
  }, [t]);

  const copyYaml = async () => {
    if (!tls) return;
    await navigator.clipboard.writeText(tlsYaml(tls));
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  };

  const cert = tls?.certificate ?? null;
  const sans = cert ? [...cert.dnsNames, ...cert.ipAddresses] : [];

  return (
    <Card className="h-fit">
      <CardHeader>
        <CardTitle>{t("set.transportSecurity")}</CardTitle>
        <CardDescription>{t("set.tlsDesc")}        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        {!tls && !loadError && (
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="h-3 w-3 animate-spin" /> {t("i18nSweep.readingCertificate")}
          </div>
        )}
        {loadError && <p className="text-sm text-down">{loadError}</p>}

        {tls && (
          <>
            <div className="flex items-center justify-between">
              <span className="text-sm text-muted-foreground">{t("set.mode")}</span>
              <div className="flex items-center gap-1.5">
                {tls.configured === "auto" && (
                  <span className="text-sm text-muted-foreground">{t("set.tlsAutoResolvedTo")}</span>
                )}
                <Badge variant={toneBadge[modeTone(tls)]}>{t(tlsModeLabel[tls.mode])}</Badge>
              </div>
            </div>

            {tls.mode === "off" && (
              <p className="text-sm text-muted-foreground">
                {tls.trustProxyHeaders
                  ? t("set.proxyTerminatesTls") : t("set.noEncryption")}
              </p>
            )}

            {cert ? (
              <>
                <div className="flex items-center justify-between gap-2 border-t border-border pt-2">
                  <span className="text-sm text-muted-foreground">{t("set.expires")}</span>
                  <span className="flex items-baseline gap-1.5">
                    <code className="tnum font-mono text-sm">{timestamp(cert.notAfter)}</code>
                    <span className={`tnum text-sm ${toneText[expiryTone(cert.daysRemaining)]}`}>
                      {expiryLabel(cert, t)}</span>
                  </span>
                </div>
                <div className="flex items-start justify-between gap-2">
                  <span className="shrink-0 text-sm text-muted-foreground">{t("set.issuer")}</span>
                  <span className="min-w-0 break-all text-right text-sm">{cert.issuer}</span>
                </div>
                <div className="flex items-start justify-between gap-2">
                  <span className="shrink-0 text-sm text-muted-foreground">{t("set.subject")}</span>
                  <span className="min-w-0 break-all text-right text-sm">{cert.subject}</span>
                </div>
                <div className="flex items-start justify-between gap-2">
                  <span className="shrink-0 text-sm text-muted-foreground">{t("set.validFor")}</span>
                  <code className="min-w-0 break-all text-right font-mono text-sm">
                    {sans.length ? sans.join(", ") : "—"}
                  </code>
                </div>
              </>
            ) : (
              // In off mode the paragraph above already said this in context;
              // repeating the API's wording underneath it reads as a fault.
              tls.mode !== "off" &&
              tls.certificateError && (
                <p className="border-t border-border pt-2 text-sm text-muted-foreground">
                  {tls.certificateError}
                </p>
              )
            )}

            {tls.hstsWarning && <p className="text-sm text-warn">{tls.hstsWarning}</p>}
            {tls.hsts && (
              <p className="text-sm text-muted-foreground">
                {tn("set.hstsOn", {
                  host: <code className="mx-1 font-mono">{tls.hostname || t("set.thisHost")}</code>,
                })}
              </p>
            )}

            {/* THE DETAIL, CLOSED (settings-council R9): the mode and the
                expiry above are what an operator checks; the CA download, the
                Let's Encrypt walkthrough and the config.yaml are what they read
                once. A native disclosure keeps the fields mounted, so the
                search can still open it and land on the hostname field. */}
            <details className="group border-t border-border pt-2" open={showAdvanced || undefined}>
              <summary className="flex w-fit cursor-pointer list-none items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
                <ChevronDown aria-hidden className="h-3.5 w-3.5 transition-transform group-open:rotate-180" />
                {t("settingsUx.tlsDetails")}
              </summary>
              <div className="mt-2 flex flex-col gap-2">
            {tls.caAvailable && (
              <div className="flex flex-col gap-1.5 border-t border-border pt-2">
                <span className="text-sm text-muted-foreground">
                  {t("i18nSweep.localCa")}
                </span>
                <code className="tnum break-all rounded border border-border bg-background p-1.5 font-mono text-sm leading-relaxed text-muted-foreground">
                  {tls.caFingerprint}
                </code>
                {/* A plain link, not XHR: this is a file the browser saves, and
                    it stays reachable without a session so a user locked out by
                    the untrusted certificate can still get it. */}
                <Button asChild size="sm" variant="outline" className="w-fit">
                  <a href={api.caDownloadUrl()} download>
                    <Download /> {t("i18nSweep.downloadCa")}
                  </a>
                </Button>
                <p className="text-sm text-muted-foreground">
                  {t("set.caInstallIntro")}
                  <span className="mt-1 block">
                    {tn("set.caMacos", {
                      os: <strong className="font-medium text-foreground">macOS</strong>,
                    })}
                  </span>
                  <span className="block">
                    {tn("set.caLinux", {
                      os: <strong className="font-medium text-foreground">Linux</strong>,
                      dir: <code className="mx-1 font-mono">/usr/local/share/ca-certificates/</code>,
                      cmd: <code className="mx-1 font-mono">update-ca-certificates</code>,
                    })}
                  </span>
                  <span className="block">
                    {tn("set.caWindows", {
                      os: <strong className="font-medium text-foreground">Windows</strong>,
                    })}
                  </span>
                  <span className="mt-1 block">{t("set.caBrowsers")}</span>
                </p>
              </div>
            )}

            <LetsEncryptWalkthrough tls={tls} />

            <div className="flex items-center justify-between border-t border-border pt-2">
              <span className="text-sm text-muted-foreground">config.yaml</span>
              <Button size="sm" variant="ghost" onClick={copyYaml}>
                {copied ? <Check /> : <Copy />} {t("set.copy")}
              </Button>
            </div>
            <pre className="overflow-x-auto rounded border border-border bg-background p-2 font-mono text-sm text-muted-foreground">
              {tlsYaml(tls)}
            </pre>
              </div>
            </details>
          </>
        )}

      </CardContent>
    </Card>
  );
}

/** The key under the data directory that decrypts every stored credential,
 *  and whether this operator has said they backed it up (settings-council R9).
 *
 *  THE ACKNOWLEDGEMENT IS THIS BROWSER'S, and the card does not pretend
 *  otherwise. polyemesis cannot see a backup, so a server-side "backed up" flag
 *  would be a claim nothing checks; a tick in this browser is the operator's
 *  own note to themselves, and the Overview reads it to stop asking. */
/** A long absolute path, shortened from the middle so both the root and the
 *  directory name stay readable. The full path is the title and the copy. */
function shortenPath(path: string, max = 48): string {
  if (path.length <= max) return path;
  const keep = Math.floor((max - 1) / 2);
  return `${path.slice(0, keep)}…${path.slice(path.length - keep)}`;
}

function SecretKeyCard({ system }: { system: SystemInfo | null }) {
  const t = useT();
  const tn = useTN();
  const [ack, setAck] = useState(readSecretKeyAck);
  const dir = system?.dataDir ?? "";
  const toggle = (next: boolean) => {
    setAck(next);
    try {
      window.localStorage.setItem(SECRET_KEY_ACK, next ? "1" : "0");
    } catch {
      // Not remembered; the box still shows what was ticked for this visit.
    }
  };
  const copyDir = async () => {
    if (!dir) return;
    await navigator.clipboard.writeText(dir);
    toast.success(t("settingsUx.pathCopied"));
  };
  return (
    <Card className="lg:col-span-2">
      <CardHeader>
        <CardTitle>{t("settingsUx.secretKeyTitle")}</CardTitle>
        <CardDescription>{t("settingsUx.secretKeyDesc")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        {/* The onboarding tour's last step anchors here, and the wrapper exists
            so the highlight covers the path AND the sentence about secret.key
            underneath it — the path alone would highlight a directory name with
            no statement of why it matters. Reached at /settings?tab=security:
            the tab is URL-driven (see the Tabs in SettingsPage above), which is
            what lets the tour navigate straight to it rather than asking the
            operator to find it. See ui/src/lib/tourSteps.ts. */}
        <div data-tour="data-directory" className="flex flex-col gap-2">
          <div className="flex items-center justify-between gap-2">
            <span className="shrink-0 text-sm text-muted-foreground">{t("set.dataDirectory")}</span>
            <span className="flex min-w-0 items-center gap-1">
              <code className="min-w-0 truncate font-mono text-sm" title={dir || undefined}>
                {dir ? shortenPath(dir) : "—"}
              </code>
              {dir && (
                <Button variant="ghost" size="icon-sm" onClick={copyDir} aria-label={t("set.copy")}>
                  <Copy />
                </Button>
              )}
            </span>
          </div>
          <p className="text-sm text-muted-foreground">
            {tn("set.secretKeyNote", { file: <code className="mx-1 font-mono">secret.key</code> })}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Checkbox id="secret-key-ack" checked={ack} onCheckedChange={(v) => toggle(v === true)} />
          <Label htmlFor="secret-key-ack" className="text-sm font-normal">
            {t("settingsUx.backedUp")}
          </Label>
        </div>
      </CardContent>
    </Card>
  );
}

/** What each scope means, under the picker. */
const SCOPE_HINT: Record<TokenScope, TranslationKey> = {
  read: "set.tokenScopeReadHint",
  admin: "set.tokenScopeAdminHint",
  obs: "set.tokenScopeObsHint",
  "mcp-read": "set.tokenScopeMcpReadHint",
  "mcp-admin": "set.tokenScopeMcpAdminHint",
};

function ApiTokens() {
  const t = useT();
  // A ReadState, not an array. A failed GET /tokens used to be swallowed into
  // `[]`, which this card renders as "No tokens yet." -- and that sentence is
  // a claim about WHO CAN ADMINISTER THIS BOX, read by somebody auditing
  // exactly that. The top of this file has forbidden the pattern since the
  // sources read was fixed; this was the one list that still did it.
  const [tokens, setTokens] = useState<ReadState<ApiToken[]>>(pendingRead);
  const [name, setName] = useState("");
  // Read by default, matching the server: a token minted without a thought
  // should be the one that cannot change anything. Choosing admin is a
  // deliberate act here, which is the whole point of offering the choice.
  const [scope, setScope] = useState<TokenScope>("read");
  const [busy, setBusy] = useState(false);
  // Held in state and nowhere else: the server keeps only a hash, so this is
  // the one and only time the plaintext exists.
  const [minted, setMinted] = useState<{ token: ApiToken; plaintext: string } | null>(null);
  const [copied, setCopied] = useState(false);

  const load = () => {
    api
      .listTokens()
      .then((rows) => setTokens(okRead(rows)))
      // EXPLICIT, and not `[]`. "Could not be read" and "there are none" are
      // opposite answers to the only question this card exists to answer.
      .catch(() => setTokens(failedRead()));
  };
  useEffect(load, []);

  const confirmRevoke = useConfirm<ApiToken>();

  const create = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      setMinted(await api.createToken(name.trim(), scope));
      setCopied(false);
      setName("");
      setScope("read");
      load();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("set.couldNotCreateTheToken"));
    } finally {
      setBusy(false);
    }
  };

  const revoke = async (token: ApiToken) => {
    try {
      await api.revokeToken(token.id);
      toast.success(t("set.tokenRevoked", { name: token.name }));
      if (minted?.token.id === token.id) setMinted(null);
      load();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("set.couldNotRevokeTheToken"));
    }
  };

  const copy = async () => {
    if (!minted) return;
    await navigator.clipboard.writeText(minted.plaintext);
    setCopied(true);
  };

  return (
    <Card className="lg:col-span-2">
      <CardHeader>
        <CardTitle>{t("set.apiTokens")}</CardTitle>
        <CardDescription>
          {t("set.apiTokensDesc")}
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <form onSubmit={create} className="flex flex-wrap items-end gap-2">
          <div className="flex min-w-48 flex-1 flex-col gap-1">
            <Label htmlFor="tok-name">{t("set.name")}</Label>
            <Input
              id="tok-name"
              value={name}
              maxLength={64}
              placeholder={t("set.tokenNamePlaceholder")}
              onChange={(e) => setName(e.target.value)}
              required
            />
          </div>
          <div className="flex min-w-40 flex-col gap-1">
            <Label htmlFor="tok-scope">{t("set.tokenScope")}</Label>
            <Select value={scope} onValueChange={(v) => setScope(v as TokenScope)}>
              <SelectTrigger id="tok-scope">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="read">{t("set.tokenScopeRead")}</SelectItem>
                <SelectItem value="admin">{t("set.tokenScopeAdmin")}</SelectItem>
                {/* MCP tokens work at /mcp and nowhere else; see McpAccessCard. */}
                <SelectItem value="mcp-read">{t("set.tokenScopeMcpRead")}</SelectItem>
                <SelectItem value="mcp-admin">{t("set.tokenScopeMcpAdmin")}</SelectItem>
                <SelectItem value="obs">{t("set.tokenScopeObs")}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <Button type="submit" size="sm" disabled={busy || !name.trim()}>
            {busy ? <Loader2 className="animate-spin" /> : <KeyRound />} {t("i18nSweep.createToken")}
          </Button>
        </form>
        <span className="text-sm text-muted-foreground">
          {t(SCOPE_HINT[scope])}
        </span>

        {minted && (
          <div className="flex flex-col gap-1.5 rounded border border-warn/50 bg-warn/5 p-2">
            <span className="text-sm font-medium">
              {t("set.tokenCopyNow", { name: minted.token.name })}
            </span>
            <div className="flex items-center gap-1.5">
              <SecretCode value={minted.plaintext} />
              <Button size="sm" variant="outline" onClick={copy}>
                {copied ? <Check /> : <Copy />} {copied ? t("clipedit.copied") : t("common.copy")}
              </Button>
            </div>
            <span className="text-sm text-muted-foreground">{t("set.tokenHashNote")}</span>
          </div>
        )}

        {readFailed(tokens) ? (
          <span className="text-sm text-warn">{t("set.tokensUnread")}</span>
        ) : !mayClaim(tokens) ? (
          <span className="text-sm text-muted-foreground">{t("set.tokensLoading")}</span>
        ) : tokens.value.length === 0 ? (
          <span className="text-sm text-muted-foreground">{t("set.noTokens")}</span>
        ) : (
          <div className="flex flex-col gap-1">
            {tokens.value.map((tok) => (
              <div
                key={tok.id}
                className="flex items-center justify-between gap-2 rounded border border-border bg-background px-2 py-1.5"
              >
                <div className="min-w-0">
                  <div className="flex items-center gap-1.5">
                    <span className="truncate text-sm">{tok.name}</span>
                    {/* Admin is the one worth spotting in a list, so it is the
                        one that gets the loud variant. A read token is the
                        expected case and reads as ordinary. */}
                    <Badge variant={tok.scope === "admin" || tok.scope === "mcp-admin" ? "warn" : "outline"}>
                      {tok.scope}
                    </Badge>
                  </div>
                  <div className="font-mono text-sm text-muted-foreground">
                    {tok.prefix}… · {t("set.tokenCreated", { date: new Date(tok.createdAt).toLocaleDateString() })} ·{" "}
                    {tokenLastUsed(tok, t)}
                  </div>
                </div>
                {/* Asks, and asks with the name typed. Revoking is
                    irreversible in the strongest sense this app has: the
                    plaintext exists nowhere any more, so an accidental revoke
                    cannot be undone by re-entering it -- every scraper holding
                    it starts 401ing and a new token has to be distributed.
                    Removing a platform credential 700 lines up already
                    required typing; a misclick here was one click. */}
                <Button
                  variant="ghost"
                  size="icon-sm"
                  onClick={() => confirmRevoke.ask(tok)}
                  aria-label={t("set.tokenRevokeAria", { name: tok.name })}
                >
                  <Trash2 />
                </Button>
              </div>
            ))}
          </div>
        )}
        {/* Paste a token here to see what it is before a plugin uses it. The
            check is read-only and changes nothing on the install. */}
        <div className="border-t pt-4">
          <PairingCheck />
        </div>
      </CardContent>

      <ConfirmDestructive
        open={confirmRevoke.open}
        onOpenChange={confirmRevoke.onOpenChange}
        subject={confirmRevoke.target?.name ?? ""}
        title={t("set.revokeTokenTitle", { name: confirmRevoke.target?.name ?? "" })}
        description={t("set.revokeTokenDesc")}
        requireTyping
        confirmLabel={t("set.revokeToken")}
        onConfirm={async () => {
          if (confirmRevoke.target) await revoke(confirmRevoke.target);
        }}
      />
    </Card>
  );
}

/** The server sends the zero time for a token nothing has presented yet. */
function tokenLastUsed(t: ApiToken, tr: Translator): string {
  const used = new Date(t.lastUsedAt);
  if (Number.isNaN(used.getTime()) || used.getUTCFullYear() <= 1) return tr("set.tokenNeverUsed");
  return tr("set.tokenLastUsed", { at: used.toLocaleString() });
}
