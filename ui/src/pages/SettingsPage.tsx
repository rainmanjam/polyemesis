import { useEffect, useState } from "react";
import { useSearchParams } from "react-router";
import { toast } from "sonner";
import { ConfirmDestructive } from "@/components/ConfirmDestructive";
import { useConfirm } from "@/hooks/useConfirm";
import {
  AlertTriangle,
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
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { useIngestLive } from "@/hooks/useLiveData";
import { AccountLiveStats } from "@/components/AccountLiveStats";
import { DeviceCodeDialog } from "@/components/DeviceCodeDialog";
import { AutomodMatrix } from "@/components/AutomodMatrix";
import { PlaylistEditor } from "@/components/PlaylistEditor";
import { Badge } from "@/components/ui/badge";
import { DebugSettings } from "@/components/DebugSettings";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
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
import { api } from "@/lib/api";
import {
  acmeStance,
  acmeYaml,
  offersPreflight,
  suggestedHostname,
  RESTART_COMMAND,
  type AcmeStance,
} from "@/lib/acme-guidance";
import { useT, type TranslationKey } from "@/lib/i18n";
import { timestamp } from "@/lib/format";
import { toneBadge, toneText, type SignalTone } from "@/lib/signal";
import {
  platformConnectControls,
  platformSupportsDeviceCode,
} from "@/lib/platformConnect";
import { LIMITS } from "@/lib/limits";
import { PULL_SCHEMES, RTSP_TRANSPORTS } from "@/lib/types";
import type {
  AcmeCheckId,
  AcmeCheckStatus,
  AcmePreflight,
  ApiToken,
  TokenScope,
  CertInfo,
  ChatRetentionSettings,
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
      .then((rows) => setSourceCount(rows.length))
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
  const tab = params.get("tab") ?? (sourceCount === 0 ? "pipeline" : "ingest");

  // The OAuth round trip is a full navigation, so its outcome comes back in
  // the query string rather than as an XHR response.
  useEffect(() => {
    const ok = params.get("oauth_ok");
    const err = params.get("oauth_error");
    if (ok) toast.success(ok);
    if (err) toast.error(err);
    if (ok || err) {
      const next = new URLSearchParams(params);
      next.delete("oauth_ok");
      next.delete("oauth_error");
      setParams(next, { replace: true });
    }
  }, [params, setParams]);

  const save = async (next: Settings) => {
    setSaving(true);
    try {
      setSettings(await api.putSettings(next));
      toast.success(t("set.settingsSavedAffectedProcessesHave"));
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("rec.saveFailed"));
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
      toast.success("MQTT settings saved.");
    } catch (err) {
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

  if (loadFailed) {
    return (
      <div className="flex h-full items-center justify-center p-3 text-center text-[12px] text-muted-foreground">
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

      <Tabs value={tab} onValueChange={(v) => setParams({ tab: v })}>
        <TabsList>
          <TabsTrigger value="ingest">{t("set.ingest")}</TabsTrigger>
          <TabsTrigger value="pipeline">{t("set.tabPipeline")}</TabsTrigger>
          <TabsTrigger value="platforms">{t("set.tabPlatforms")}</TabsTrigger>
          <TabsTrigger value="security">{t("set.tabSecurity")}</TabsTrigger>
          {/* Last, and beside Security rather than filed by subject: the export
              is the only control here that hands a copy of this server's logs to
              somebody else, and the tab an operator reaches for when thinking
              about disclosure is the one next to it. */}
          <TabsTrigger value="debug">{t("set.tabDebug")}</TabsTrigger>
        </TabsList>

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
              <Card>
                <CardHeader>
                  <CardTitle>{t("set.ffmpegTitle")}</CardTitle>
                </CardHeader>
                <CardContent>
                  <FfmpegBadges system={system} />
                </CardContent>
              </Card>
            </div>
          ) : (
            <IngestSettings settings={settings} system={system} onSave={save} saving={saving} />
          )}
        </TabsContent>
        <TabsContent value="pipeline">
          <PipelineSettings
            settings={settings}
            onSave={save}
            onSaveMqtt={saveMqtt}
            onClearMqttPassword={clearMqttPassword}
            saving={saving}
          />
        </TabsContent>
        <TabsContent value="platforms">
          <PlatformSettings />
        </TabsContent>
        <TabsContent value="security">
          <SecuritySettings system={system} />
        </TabsContent>
        <TabsContent value="debug">
          <DebugSettings />
        </TabsContent>
      </Tabs>
    </div>
  );
}

/* ------------------------------------------------------------------ ingest */

function IngestSettings({
  settings,
  system,
  onSave,
  saving,
}: {
  settings: Settings;
  system: SystemInfo | null;
  onSave: (s: Settings) => void;
  saving: boolean;
}) {
  const t = useT();
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
          <CardDescription>{t("set.ingestDesc")}          </CardDescription>
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
              <span className="text-[10px] text-warn">{t("set.noLibsrt")}</span>
            )}
          </div>

          {draft.ingest.mode === "pull" ? (
            <PullIngestFields draft={draft} setDraft={setDraft} />
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
                <Input
                  id="srt-pass"
                  type="password"
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
                <span className="text-[10px] text-muted-foreground">{t("set.passphraseNote")}</span>
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
                <Input
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
                <span className="text-[10px] text-muted-foreground">{t("set.streamKeyNote")}</span>
              </div>
            </>
          )}

          {/* Saving ingest settings reconciles the ingest, which restarts the
              FFmpeg child and drops every viewer. That is sometimes exactly
              what an operator means to do -- so this is a warning stating the
              consequence at the moment of decision, not a control refusing it.
              The button says what will happen rather than "Save", because a
              label an operator reads is worth more than a dialog they dismiss. */}
          {live && (
            <p className="flex items-start gap-1.5 text-[11px] text-warn">
              <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
              A stream is arriving now. Saving restarts the ingest, which
              disconnects the encoder and drops everyone watching. The encoder
              will reconnect on its own once it is pointed at the new settings.
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
          <CardDescription>{t("set.pointObs")}</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-2">
          <div className="flex items-center gap-2 rounded border border-border bg-background px-2 py-1.5">
            <code className="min-w-0 flex-1 break-all font-mono text-[10px] text-muted-foreground">
              {system?.ingestUrl ?? "…"}
            </code>
            <Button variant="ghost" size="icon-sm" onClick={copyUrl} aria-label={t("set.copy")}>
              <Copy />
            </Button>
          </div>
          <p className="text-[10px] text-muted-foreground">
            For multitrack, use OBS → Settings → Output → Recording, set type to Custom Output
            (FFmpeg), container <code className="font-mono">mpegts</code>, and enable the audio
            tracks you want to send. The README has the exact field values.
          </p>
          <FfmpegBadges system={system} />
        </CardContent>
      </Card>
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
  listeners: { srtPort: number; rtmpPort: number };
  setListeners: (patch: Partial<{ srtPort: number; rtmpPort: number }>) => void;
  footer?: React.ReactNode;
}) {
  const t = useT();
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("set.listeners")}</CardTitle>
        <CardDescription>{t("set.listenersDesc")}          </CardDescription>
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
    return <span className="text-[11px] text-muted-foreground">{t("set.ffmpegUnknown")}</span>;
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
          text overlays {system.ffmpeg.filters?.includes("drawtext") ? t("set.yes") : t("set.no")}
        </Badge>
      )}
    </div>
  );
}

/** The pull source's fields.
 *
 *  The scheme list and the reconnect bounds are the SAME ones the server
 *  enforces, quoted here so a mistake is a hint under the field rather than a
 *  rejected save. They are a hint and nothing more: the server validates
 *  independently, because a UI check is a convenience and never a control. */
function PullIngestFields({
  draft,
  setDraft,
}: {
  draft: Settings;
  setDraft: (s: Settings) => void;
}) {
  const t = useT();
  const pull = draft.ingest.pull ?? { url: "", reconnectDelayMaxSeconds: 30, rtspTransport: "tcp" };
  const set = (patch: Partial<typeof pull>) =>
    setDraft({ ...draft, ingest: { ...draft.ingest, pull: { ...pull, ...patch } } });

  const scheme = pull.url.split("://")[0]?.toLowerCase() ?? "";
  const known = (PULL_SCHEMES as readonly string[]).includes(scheme);

  return (
    <>
      <div className="flex flex-col gap-1">
        <Label htmlFor="pull-url">{t("set.sourceUrl")}</Label>
        <Input
          id="pull-url"
          value={pull.url}
          placeholder={t("set.sourceUrlPlaceholder")}
          onChange={(e) => set({ url: e.target.value })}
        />
        {pull.url !== "" && !known ? (
          <span className="text-[10px] text-warn">
            {t("set.dialsOnly", { schemes: PULL_SCHEMES.join(", ") })}</span>
        ) : (
          <span className="text-[10px] text-muted-foreground">
            One of {PULL_SCHEMES.join(", ")}. A file:// source is a path RELATIVE to the data
            directory and may not contain ".." — it is confined there the same way a file
            destination is.
          </span>
        )}
      </div>

      <div className="grid grid-cols-2 gap-2">
        <div className="flex flex-col gap-1">
          <Label htmlFor="pull-reconnect">{t("set.reconnectCap")}</Label>
          <Input
            id="pull-reconnect"
            type="number"
            value={pull.reconnectDelayMaxSeconds}
            onChange={(e) => set({ reconnectDelayMaxSeconds: Number(e.target.value) })}
          />
          <span className="text-[10px] text-muted-foreground">{t("set.reconnectCapNote")}</span>
        </div>
        <div className="flex flex-col gap-1">
          <Label>{t("set.rtspTransport")}</Label>
          <Select value={pull.rtspTransport || "tcp"} onValueChange={(v) => set({ rtspTransport: v })}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {RTSP_TRANSPORTS.map((t) => (
                <SelectItem key={t} value={t}>
                  {t}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <span className="text-[10px] text-muted-foreground">{t("set.rtspNote")}</span>
        </div>
      </div>
    </>
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
  };
}

function PipelineSettings({
  settings,
  onSave,
  onSaveMqtt,
  onClearMqttPassword,
  saving,
}: {
  settings: Settings;
  onSave: (s: Settings) => void;
  onSaveMqtt: (s: Settings, password: string) => void;
  onClearMqttPassword: () => Promise<void>;
  saving: boolean;
}) {
  const t = useT();
  const [draft, setDraft] = useState(settings);
  useEffect(() => setDraft(settings), [settings]);
  // Held here and never seeded from the server: the password is not in the
  // settings payload at all, so there is nothing to seed it FROM. An empty box
  // means "leave the stored one alone", which is why saving it is a separate
  // call rather than part of the settings PUT.
  const [mqttPassword, setMqttPassword] = useState("");
  useEffect(() => setMqttPassword(""), [settings]);

  return (
    <div className="grid gap-3 lg:grid-cols-2">
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
          <span className="text-[10px] text-muted-foreground">{t("set.synthesiseNote")}</span>
          <Button size="sm" onClick={() => onSave(draft)} disabled={saving}>
            {saving ? <Loader2 className="animate-spin" /> : <Save />} {t("common.save")}
          </Button>
        </CardContent>
      </Card>

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
            <span className="text-[10px] text-muted-foreground">{t("set.staggerNote")}</span>
            <span className="text-[10px] text-muted-foreground">
              {t("set.staggerReconnectNote")}</span>
          </div>
          <Button size="sm" onClick={() => onSave(draft)} disabled={saving}>
            {saving ? <Loader2 className="animate-spin" /> : <Save />} {t("common.save")}
          </Button>
        </CardContent>
      </Card>

      <MultitrackHardware draft={draft} setDraft={setDraft} onSave={onSave} saving={saving} />

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
              <span className="text-[10px] text-muted-foreground">0 keeps everything, forever.</span>
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
              <span className="text-[10px] text-muted-foreground">{t("set.chatKeepNote")}</span>
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
              <span className="text-[10px] text-muted-foreground">{t("set.chatSendNote")}</span>
            </div>
          </div>

          <span className="text-[10px] text-muted-foreground">
            Both apply and the more generous one wins: a message goes when it is older than the
            hours <em>and</em> outside the newest N. So a busy channel keeps less time than you
            asked and a quiet one keeps more &mdash; which is the right way round, because the
            floor is what stops a slow channel&rsquo;s user cards being empty.
          </span>
          <span className="text-[10px] text-muted-foreground">{t("set.chatNotOnlyDisk")}</span>
          <span className="text-[10px] text-muted-foreground">{t("set.chatSmall")}</span>
          <span className="text-[10px] text-muted-foreground">
            <strong>{t("set.chatSendOnConnectShort")}</strong> is a different kind of number from the other two. Those
            bound what is kept on disk; this one is a buffer held in memory, allocated in full
            whether or not anyone is talking. It is what a browser receives the instant it opens the
            page, before any of the stored history is queried &mdash; so raising it costs memory all
            the time to make the first screen fuller.
          </span>
          <Button size="sm" onClick={() => onSave(draft)} disabled={saving}>
            {saving ? <Loader2 className="animate-spin" /> : <Save />} {t("common.save")}
          </Button>
        </CardContent>
      </Card>

      {/* Automod sits directly after chat retention, because the two are the
          same subject: retention is the DEPTH the history checker can see, and
          a rate detector with a two-hour scrollback behind it is a different
          instrument from one with a week. */}
      <AutomodMatrix settings={draft} onChange={setDraft} />

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
            <span className="text-[10px] text-muted-foreground">{t("set.attemptsNote")}</span>
          </div>
          <span className="text-[10px] text-muted-foreground">{t("set.attemptsRetryable")}</span>
          <span className="text-[10px] text-muted-foreground">{t("set.attemptsBackoff")}</span>
          <Button size="sm" onClick={() => onSave(draft)} disabled={saving}>
            {saving ? <Loader2 className="animate-spin" /> : <Save />} {t("common.save")}
          </Button>
        </CardContent>
      </Card>

      <div className="flex justify-end">
        <Button size="sm" onClick={() => onSave(draft)} disabled={saving}>
          {saving ? <Loader2 className="animate-spin" /> : <Save />} {t("common.save")}
        </Button>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>{t("set.failover")}</CardTitle>
          <CardDescription>{t("set.failoverDesc")}          </CardDescription>
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
          <span className="text-[10px] text-muted-foreground">{t("set.failoverRemuxNote")}</span>

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
                <span className="text-[10px] text-muted-foreground">{t("set.graceNote")}</span>
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
                <span className="text-[10px] text-muted-foreground">{t("set.returnManualNote")}</span>
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
                    <span className="text-[10px] text-muted-foreground">{t("set.slateImageNote")}</span>
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
                    <span className="text-[10px] text-muted-foreground">{t("set.slateColourNote")}</span>
                  </div>
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
              <span className="text-[10px] text-muted-foreground">{t("set.playLoopNote")}</span>

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

          <Button size="sm" onClick={() => onSave(draft)} disabled={saving}>
            {saving ? <Loader2 className="animate-spin" /> : <Save />} {t("common.save")}
          </Button>
        </CardContent>
      </Card>

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
          <span className="text-[10px] text-muted-foreground">
            <strong>{t("set.mqtt5Only")}</strong> A broker pinned to 3.1.1 will not complete a connection
            at all &mdash; it is not a degraded mode. Mosquitto 2.x, EMQX and the Home Assistant
            add-on are all fine.
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
                <span className="text-[10px] text-muted-foreground">
                  {t("set.brokerUrlNote")}
                </span>
                {/* Said at the point of configuration rather than left to the
                    docs, because the credential is encrypted at rest and then
                    sent in the clear -- which is the sort of gap an operator
                    reasonably assumes has been closed. Not a refusal: mqtt:// on
                    a trusted LAN is the normal Home Assistant setup and what our
                    own documentation recommends. */}
                {(draft.mqtt?.brokerUrl ?? "").startsWith("mqtt://") && (
                  <span className="text-[10px] text-warn">
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
                  <Input
                    id="mq-pw"
                    type="password"
                    value={mqttPassword}
                    placeholder={draft.mqtt?.hasPassword ? "(unchanged)" : ""}
                    onChange={(e) => setMqttPassword(e.target.value)}
                  />
                </div>
              </div>
              <span className="text-[10px] text-muted-foreground">{t("set.passwordNote")}</span>
              {draft.mqtt?.hasPassword && (
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => {
                    setMqttPassword("");
                    void onClearMqttPassword();
                  }}
                >
                  Clear stored password
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
              <span className="text-[10px] text-muted-foreground">
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
              <span className="text-[10px] text-muted-foreground">{t("set.intervalNote")}</span>

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
                <span className="text-[10px] text-muted-foreground">{t("set.clientIdNote")}</span>
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

          <Button size="sm" onClick={() => onSaveMqtt(draft, mqttPassword)} disabled={saving}>
            {saving ? <Loader2 className="animate-spin" /> : <Save />} {t("common.save")}
          </Button>
        </CardContent>
      </Card>

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
            <span className="text-[10px] text-muted-foreground">{t("set.previewIdleNote")}</span>
          </div>
        </CardContent>
      </Card>

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

      <Card>
        <CardHeader>
          <CardTitle>{t("set.processLogs")}</CardTitle>
          <CardDescription>{t("set.logsDesc")}          </CardDescription>
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
          <span className="text-[10px] text-muted-foreground">{t("set.logsBoundNote")}</span>
        </CardContent>
      </Card>

      <div className="lg:col-span-2">
        <Button size="sm" onClick={() => onSave(draft)} disabled={saving}>
          {saving ? <Loader2 className="animate-spin" /> : <Save />} {t("common.save")} pipeline settings
        </Button>
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
function MultitrackHardware({
  draft,
  setDraft,
  onSave,
  saving,
}: {
  draft: Settings;
  setDraft: (s: Settings) => void;
  onSave: (s: Settings) => void;
  saving: boolean;
}) {
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
          Enhanced Broadcasting hardware (Twitch)
          <ExperimentalBadge />
        </CardTitle>
        <CardDescription>
          What this machine's GPU is, for the negotiation a destination with Enhanced Broadcasting
          switched on makes at go-live.
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {/* The form is unchanged and still fully usable. This says only what
            polyemesis has and has not seen, which is a different question from
            what Twitch documents. It is also the one place an operator can act
            on knowing that Twitch reads the DECLARATION, so the mechanism is
            stated here rather than left to be inferred from a refusal. */}
        <Experimental>
          An inventory declared here does reach Twitch's live endpoint, and polyemesis's own
          tests watch it be accepted: a supported GPU is granted Enhanced Broadcasting, a VOD
          audio track and a minted key. What Twitch checks is <em>this declaration</em> &mdash;
          the vendor ID, device ID and driver version as sent, against a list it does not
          publish. It has no way to inspect the machine, which is why these fields exist at all:
          polyemesis sends what it is told. What has never been observed is a broadcast published
          through a minted key. Filling this in is safe either way &mdash; nothing is sent until
          a destination with the toggle on goes live, and a refusal falls back to the ordinary
          Twitch ingest.
        </Experimental>
        <span className="text-[10px] text-muted-foreground">
          Twitch grants Enhanced Broadcasting only to a client with a GPU it supports, and it checks
          what it is told: a zero vendor ID, a vendor it does not know and an out-of-date driver are
          each refused by name. polyemesis does not fill this in for you — it can read the PCI vendor
          ID of a render node on Linux and nothing else, and sending that one field with zeros in the
          rest would be describing a machine that does not exist. Leaving it empty is fine and is the
          normal state: nothing is asked, and a destination with the toggle on simply publishes to
          the ordinary Twitch ingest and says so once.
        </span>
        {gpus.map((g, i) => (
          <div key={i} className="flex flex-col gap-2 rounded-md border border-border p-2">
            <div className="flex flex-wrap items-end gap-2">
              <div className="flex min-w-56 flex-1 flex-col gap-1">
                <Label htmlFor={`mt-model-${i}`}>Model</Label>
                <Input
                  id={`mt-model-${i}`}
                  value={g.model ?? ""}
                  placeholder="NVIDIA GeForce RTX 4070"
                  onChange={(e) => patch(i, { model: e.target.value })}
                />
              </div>
              <div className="flex flex-col gap-1">
                <Label htmlFor={`mt-vendor-${i}`}>Vendor</Label>
                <Select
                  value={g.vendorId ? String(g.vendorId) : ""}
                  onValueChange={(v) => patch(i, { vendorId: Number(v) })}
                >
                  <SelectTrigger id={`mt-vendor-${i}`} className="w-52">
                    <SelectValue placeholder="Choose the vendor" />
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
                <Label htmlFor={`mt-driver-${i}`}>Driver version</Label>
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
                aria-label="Remove this GPU"
                onClick={() => write(gpus.filter((_, n) => n !== i))}
              >
                <Trash2 />
              </Button>
            </div>
            <div className="flex flex-wrap gap-2">
              <div className="flex flex-col gap-1">
                <Label htmlFor={`mt-device-${i}`}>Device ID (decimal)</Label>
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
                <Label htmlFor={`mt-vram-${i}`}>Video memory (bytes)</Label>
                <Input
                  id={`mt-vram-${i}`}
                  type="number"
                  min={0}
                  className="w-44"
                  value={g.dedicatedVideoMemory ?? 0}
                  onChange={(e) => patch(i, { dedicatedVideoMemory: Number(e.target.value) })}
                />
              </div>
            </div>
            {/* Optional, and said so rather than left for the operator to
                discover by saving. A number invented to fill a box is worse
                than an empty one: Twitch checks these. */}
            <span className="text-[10px] text-muted-foreground">
              Device ID and video memory are optional — leave them at zero rather than guessing. The
              model and the vendor are not: a vendor ID of zero is refused by name.
            </span>
          </div>
        ))}
        <div className="flex gap-2">
          <Button
            size="sm"
            variant="outline"
            onClick={() => write([...gpus, { model: "", vendorId: 0 }])}
          >
            Add a GPU
          </Button>
          <Button size="sm" onClick={() => onSave(draft)} disabled={saving}>
            {saving ? <Loader2 className="animate-spin" /> : <Save />} Save
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

/* --------------------------------------------------------------- platforms */

function PlatformSettings() {
  const t = useT();
  const [guides, setGuides] = useState<SetupGuide[]>([]);
  const [creds, setCreds] = useState<PlatformCreds[]>([]);
  const [accounts, setAccounts] = useState<PlatformAccount[]>([]);

  const load = () => {
    api.platformGuides().then(setGuides).catch(() => {});
    api.listCreds().then(setCreds).catch(() => {});
    api.listAccounts().then(setAccounts).catch(() => {});
  };
  useEffect(load, []);

  return (
    <div className="flex flex-col gap-3">
      <PlatformCapabilityMatrix />

      <Card>
        <CardHeader>
          <CardTitle>{t("set.whyDevApp")}</CardTitle>
          <CardDescription>{t("set.whyDevAppDesc")}          </CardDescription>
        </CardHeader>
      </Card>

      {guides.map((g) => (
        <PlatformCredCard
          key={g.platform}
          guide={g}
          creds={creds.find((c) => c.platform === g.platform) ?? null}
          accounts={accounts.filter((a) => a.platform === g.platform)}
          onChanged={load}
        />
      ))}
    </div>
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
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("set.whatPlatformsDo")}</CardTitle>
        <CardDescription>{t("set.platformsDesc")}        </CardDescription>
      </CardHeader>

      <CardContent className="flex flex-col gap-3">
        <div className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("set.platform")}</TableHead>
                {CAPABILITY_COLUMNS.map((c) => (
                  <TableHead key={c.key} title={c.help}>
                    {c.label}
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
                        title={tierInfo(p.tier).help}
                      >
                        {tierInfo(p.tier).label}
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
                          title={p.reasons?.[c.key] ?? info.help}
                        >
                          {info.label}
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
                {l.label}
              </Badge>
              <span className="text-[10px] text-muted-foreground">{l.help}</span>
            </span>
          ))}
        </div>

        {/* "Unverified" is the load-bearing value here and the one most likely
            to be misread as "no". Saying it plainly costs a sentence and saves
            an operator concluding a feature is blocked when it was only never
            checked. */}
        <p className="text-[10px] text-muted-foreground">
          Unverified means exactly that: polyemesis does not do it today and nobody here has read
          the platform's API either way. It is never a refusal — if you can make it work, it
          works, and we would rather be told than guess. Only “not possible” is a checked
          absence.
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
              <span className="text-[11px] font-medium">{p.name}</span>
              <Badge variant={p.tier === "unsupported" ? "warn" : "outline"}>read first</Badge>
            </div>
            <p
              className={
                p.tier === "unsupported" ? "text-[10px] text-warn" : "text-[10px] text-muted-foreground"
              }
            >
              {p.readFirst}
            </p>
          </div>
        ))}
      </CardContent>
    </Card>
  );
}

function PlatformCredCard({
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
  const [check, setCheck] = useState<CredentialCheck | null>(null);
  const [rechecking, setRechecking] = useState(false);

  useEffect(() => setClientId(creds?.clientId ?? ""), [creds]);

  const save = async () => {
    setBusy(true);
    try {
      const saved = await api.putCreds(guide.platform, clientId.trim(), clientSecret.trim());
      setCheck(saved.check ?? null);
      // Saved is saved, whatever the checker thought. The verdict renders
      // below; it does not turn a successful save into a failure toast.
      toast.success(`${guide.name} credentials saved.`);
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
      setCheck(await api.checkCreds(guide.platform));
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

  const disconnect = async (a: PlatformAccount) => {
    await api.deleteAccount(a.id);
    toast.success(t("set.accountDisconnected"));
    onChanged();
  };

  const copyRedirect = async () => {
    await navigator.clipboard.writeText(guide.redirectPath);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  };

  return (
    <Card>
      <CardHeader className="flex-row items-start justify-between">
        <div>
          <CardTitle className="flex items-center gap-2">
            {guide.name}
            {!guide.supported && <Badge variant="outline">manual key</Badge>}
            {creds?.hasSecret && <Badge variant="live">configured</Badge>}
          </CardTitle>
          {guide.note && <CardDescription className="mt-1">{guide.note}</CardDescription>}
        </div>
        {guide.consoleUrl && (
          <Button variant="outline" size="sm" asChild>
            <a href={guide.consoleUrl} target="_blank" rel="noreferrer noopener">
              <ExternalLink /> Console
            </a>
          </Button>
        )}
      </CardHeader>

      <CardContent className="flex flex-col gap-3">
        <Accordion type="single" collapsible>
          <AccordionItem value="steps" className="border-b-0">
            <AccordionTrigger>{t("set.stepByStep")}</AccordionTrigger>
            <AccordionContent>
              <ol className="flex list-decimal flex-col gap-1.5 pl-4 text-[11px] text-muted-foreground">
                {guide.steps.map((s, i) => (
                  <li key={i}>{s}</li>
                ))}
              </ol>
              {guide.redirectPath && (
                <div className="mt-2 flex flex-col gap-1">
                  <Label>{t("set.redirectUri")}</Label>
                  <div className="flex items-center gap-2 rounded border border-border bg-background px-2 py-1.5">
                    <code className="min-w-0 flex-1 break-all font-mono text-[10px]">
                      {guide.redirectPath}
                    </code>
                    <Button variant="ghost" size="icon-sm" onClick={copyRedirect} aria-label={t("set.copy")}>
                      {copied ? <Check className="text-live" /> : <Copy />}
                    </Button>
                  </div>
                  <span className="text-[10px] text-muted-foreground">{t("set.redirectNote")}</span>
                  {/* Above the credential fields on purpose: registering the
                      right URI has to happen before the credentials matter. */}
                  {guide.redirectWarnings?.map((warning) => (
                    <div
                      key={warning}
                      className="flex items-start gap-1.5 rounded border border-warn/30 bg-warn-dim px-2 py-1 text-[10px] text-warn"
                    >
                      <AlertTriangle className="mt-px size-3 shrink-0" />
                      <span>{warning}</span>
                    </div>
                  ))}
                </div>
              )}
              {guide.scopes && guide.scopes.length > 0 && (
                <div className="mt-2 flex flex-wrap gap-1">
                  {guide.scopes.map((s) => (
                    <Badge key={s} variant="outline" className="font-mono normal-case">
                      {s}
                    </Badge>
                  ))}
                </div>
              )}
            </AccordionContent>
          </AccordionItem>
        </Accordion>

        {guide.supported && (
          <>
            <div className="grid gap-2 sm:grid-cols-2">
              <div className="flex flex-col gap-1">
                <Label htmlFor={`cid-${guide.platform}`}>{t("set.clientId")}</Label>
                <Input
                  id={`cid-${guide.platform}`}
                  value={clientId}
                  onChange={(e) => setClientId(e.target.value)}
                  className="font-mono"
                />
              </div>
              <div className="flex flex-col gap-1">
                <Label htmlFor={`cs-${guide.platform}`}>{t("set.clientSecret")}</Label>
                <Input
                  id={`cs-${guide.platform}`}
                  type="password"
                  value={clientSecret}
                  placeholder={creds?.hasSecret ? "•••••••• (stored, encrypted)" : ""}
                  onChange={(e) => setClientSecret(e.target.value)}
                  className="font-mono"
                />
              </div>
            </div>

            {/* The verdict.
                Four distinct words, and four distinct colours. "not verifiable"
                must never read as "verified", and "could not check" must never
                read as "rejected": a check that CANNOT run must not look like
                one that ran and failed, and a platform outage must not be
                reported as the operator's mistake. */}
            {check && (
              <div className="flex items-start gap-2 text-[10px]">
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
                  {check.state === "verified" && "verified"}
                  {check.state === "rejected" && "rejected"}
                  {check.state === "unverified" && "not verifiable"}
                  {check.state === "unreachable" && "could not check"}
                </Badge>
                <span className="text-muted-foreground">{check.detail}</span>
              </div>
            )}

            <div className="flex flex-wrap gap-1.5">
              <Button size="sm" onClick={save} disabled={busy || !clientId.trim() || !clientSecret.trim()}>
                {busy ? <Loader2 className="animate-spin" /> : <Save />} Save credentials
              </Button>
              {creds?.hasSecret && (
                <Button size="sm" variant="outline" onClick={recheck} disabled={rechecking}>
                  {rechecking ? <Loader2 className="animate-spin" /> : <RefreshCw />} Re-check
                </Button>
              )}
              {/* Which of these the card offers is decided in
                  lib/platformConnect.ts, with the reasons -- in particular why
                  the code route is offered BESIDE the redirect one rather than
                  instead of it. Written here it was a rule nothing could
                  reach. */}
              {connectControls.includes("connectRedirect") && (
                <Button size="sm" variant="outline" asChild>
                  <a href={api.connectUrl(guide.platform)}>
                    <ExternalLink /> Connect an account
                  </a>
                </Button>
              )}
              {connectControls.includes("connectWithCode") && (
                <Button size="sm" variant="outline" onClick={() => setDeviceOpen(true)}>
                  <KeyRound /> {t("device.connectWithACode")}
                </Button>
              )}
              {connectControls.includes("removeCredentials") && (
                <Button size="sm" variant="ghost" onClick={() => confirmCreds.ask({ name: guide.name })}>
                  <Trash2 /> Remove
                </Button>
              )}
            </div>

            {accounts.length > 0 && (
              <div className="flex flex-col gap-1">
                <Label>{t("set.connectedAccounts")}</Label>
                {accounts.map((a) => (
                  <div
                    key={a.id}
                    className="flex items-center justify-between rounded border border-border bg-background px-2 py-1.5"
                  >
                    <div className="min-w-0">
                      <div className="flex items-center gap-1.5">
                        <span className="truncate text-[12px]">{a.accountName}</span>
                        {a.reconnect?.needed && (
                          <Badge variant="outline" className="border-warn text-[9px] text-warn">
                            reconnect needed
                          </Badge>
                        )}
                      </div>
                      <div className="font-mono text-[10px] text-muted-foreground">{a.accountRef}</div>
                      {/* What the capability matrix two cards up promises, on
                          the account it is a promise about. "Viewers: Works"
                          read as a claim nobody could check until this was
                          here, because the route existed and nothing called
                          it. Polled while this tab is visible; see
                          lib/viewerCount.ts for why the interval is what it
                          is. */}
                      <AccountLiveStats accountId={a.id} />
                      {/* Said in full rather than as a bare badge. "Reconnect"
                          with no reason reads as a fault the operator caused;
                          the point is that a token cannot gain a permission it
                          was not issued with, which is nobody's mistake. */}
                      {a.reconnect?.needed && (
                        <div className="mt-0.5 text-[10px] text-warn">
                          {a.reconnect.reason}
                          {a.reconnect.missing && a.reconnect.missing.length > 0 && (
                            <> Missing: <code>{a.reconnect.missing.join(", ")}</code></>
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
                <span className="text-[10px] text-muted-foreground">{t("set.multiAccountNote")}</span>
              </div>
            )}
          </>
        )}
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
              {busy ? <Loader2 className="animate-spin" /> : <ShieldCheck />} Change password
            </Button>
          </form>
        </CardContent>
      </Card>

      <TransportSecurity system={system} />

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

function expiryLabel(cert: CertInfo): string {
  if (cert.expired) {
    const ago = Math.abs(cert.daysRemaining);
    return ago === 0 ? "expired today" : `expired ${ago} day${ago === 1 ? "" : "s"} ago`;
  }
  return `${cert.daysRemaining} day${cert.daysRemaining === 1 ? "" : "s"} left`;
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
      <span className="text-[11px] text-muted-foreground">{t("set.acmeTitle")}</span>
      <p className="text-[10px] text-muted-foreground">{t(acmeStanceCopy[stance])}</p>

      {offersPreflight(stance) && (
        <>
          <div className="flex flex-col gap-1">
            <Label htmlFor="acme-host" className="text-[10px]">
              {t("set.acmeHostname")}
            </Label>
            <Input
              id="acme-host"
              className="h-7 font-mono text-[11px]"
              placeholder="stream.example.com"
              value={hostname}
              onChange={(e) => setHostname(e.target.value)}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="acme-email" className="text-[10px]">
              {t("set.acmeEmailLabel")}
            </Label>
            {/* Never sent to this server: it only ever appears in the YAML
                below, which the operator copies into a file by hand. */}
            <Input
              id="acme-email"
              type="email"
              className="h-7 font-mono text-[11px]"
              placeholder="you@example.com"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
            />
            <p className="text-[10px] text-muted-foreground">{t("set.acmeEmailHint")}</p>
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

          {checkError && <p className="text-[10px] text-down">{checkError}</p>}

          {result && (
            <>
              <p className={`text-[10px] ${result.ready ? toneText.live : toneText.down}`}>
                {result.ready ? t("set.acmeReady") : t("set.acmeBlocked")}
              </p>
              <ul className="flex flex-col gap-1.5">
                {result.checks.map((c) => (
                  <li key={c.id} className="flex flex-col">
                    <span className={`text-[10px] font-medium ${toneText[acmeCheckTone[c.status]]}`}>
                      {t(acmeCheckLabel[c.id])}
                    </span>
                    <span className="text-[10px] text-muted-foreground">{c.detail}</span>
                  </li>
                ))}
              </ul>
              {/* Said before the snippet, not after it: a failed order costs an
                  hour, and the operator reads downward. */}
              <p className="text-[10px] text-warn">{t("set.acmeRateLimit")}</p>
              <div className="flex items-center justify-between">
                <span className="text-[11px] text-muted-foreground">{t("set.acmeThen")}</span>
                <Button size="sm" variant="ghost" onClick={copySnippet}>
                  {copied ? <Check /> : <Copy />} {t("set.copy")}
                </Button>
              </div>
              <pre className="overflow-x-auto rounded border border-border bg-background p-2 font-mono text-[10px] text-muted-foreground">
                {snippet}
              </pre>
            </>
          )}
        </>
      )}
    </div>
  );
}

function TransportSecurity({ system }: { system: SystemInfo | null }) {
  const t = useT();
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
          <div className="flex items-center gap-2 text-[11px] text-muted-foreground">
            <Loader2 className="h-3 w-3 animate-spin" /> Reading the certificate…
          </div>
        )}
        {loadError && <p className="text-[11px] text-down">{loadError}</p>}

        {tls && (
          <>
            <div className="flex items-center justify-between">
              <span className="text-[11px] text-muted-foreground">{t("set.mode")}</span>
              <div className="flex items-center gap-1.5">
                {tls.configured === "auto" && (
                  <span className="text-[10px] text-subtle-foreground">auto resolved to</span>
                )}
                <Badge variant={toneBadge[modeTone(tls)]}>{t(tlsModeLabel[tls.mode])}</Badge>
              </div>
            </div>

            {tls.mode === "off" && (
              <p className="text-[10px] text-muted-foreground">
                {tls.trustProxyHeaders
                  ? t("set.proxyTerminatesTls") : t("set.noEncryption")}
              </p>
            )}

            {cert ? (
              <>
                <div className="flex items-center justify-between gap-2 border-t border-border pt-2">
                  <span className="text-[11px] text-muted-foreground">{t("set.expires")}</span>
                  <span className="flex items-baseline gap-1.5">
                    <code className="tnum font-mono text-[10px]">{timestamp(cert.notAfter)}</code>
                    <span className={`tnum text-[10px] ${toneText[expiryTone(cert.daysRemaining)]}`}>
                      {expiryLabel(cert)}</span>
                  </span>
                </div>
                <div className="flex items-start justify-between gap-2">
                  <span className="shrink-0 text-[11px] text-muted-foreground">{t("set.issuer")}</span>
                  <span className="min-w-0 break-all text-right text-[10px]">{cert.issuer}</span>
                </div>
                <div className="flex items-start justify-between gap-2">
                  <span className="shrink-0 text-[11px] text-muted-foreground">{t("set.subject")}</span>
                  <span className="min-w-0 break-all text-right text-[10px]">{cert.subject}</span>
                </div>
                <div className="flex items-start justify-between gap-2">
                  <span className="shrink-0 text-[11px] text-muted-foreground">{t("set.validFor")}</span>
                  <code className="min-w-0 break-all text-right font-mono text-[10px]">
                    {sans.length ? sans.join(", ") : "—"}
                  </code>
                </div>
              </>
            ) : (
              // In off mode the paragraph above already said this in context;
              // repeating the API's wording underneath it reads as a fault.
              tls.mode !== "off" &&
              tls.certificateError && (
                <p className="border-t border-border pt-2 text-[10px] text-muted-foreground">
                  {tls.certificateError}
                </p>
              )
            )}

            {tls.hstsWarning && <p className="text-[10px] text-warn">{tls.hstsWarning}</p>}
            {tls.hsts && (
              <p className="text-[10px] text-muted-foreground">
                HSTS is on. Browsers will refuse plain HTTP to
                <code className="mx-1 font-mono">{tls.hostname || "this host"}</code>
                until the max-age lapses; there is no server-side undo.
              </p>
            )}

            {tls.caAvailable && (
              <div className="flex flex-col gap-1.5 border-t border-border pt-2">
                <span className="text-[11px] text-muted-foreground">
                  Local certificate authority
                </span>
                <code className="tnum break-all rounded border border-border bg-background p-1.5 font-mono text-[9px] leading-relaxed text-muted-foreground">
                  {tls.caFingerprint}
                </code>
                {/* A plain link, not XHR: this is a file the browser saves, and
                    it stays reachable without a session so a user locked out by
                    the untrusted certificate can still get it. */}
                <Button asChild size="sm" variant="outline" className="w-fit">
                  <a href={api.caDownloadUrl()} download>
                    <Download /> Download CA certificate
                  </a>
                </Button>
                <p className="text-[10px] text-muted-foreground">
                  Install it to stop the browser warning, after checking the fingerprint above
                  matches the one your browser shows.
                  <span className="mt-1 block">
                    <strong className="font-medium text-foreground">macOS</strong> — open it in
                    Keychain Access, add to System, then set it to “Always Trust”.
                  </span>
                  <span className="block">
                    <strong className="font-medium text-foreground">Linux</strong> — copy to
                    <code className="mx-1 font-mono">/usr/local/share/ca-certificates/</code>and run
                    <code className="mx-1 font-mono">update-ca-certificates</code>.
                  </span>
                  <span className="block">
                    <strong className="font-medium text-foreground">Windows</strong> — import into
                    “Trusted Root Certification Authorities” for the local machine.
                  </span>
                  <span className="mt-1 block">
                    Firefox and Chrome keep their own stores on some platforms and may need it
                    imported there too.
                  </span>
                </p>
              </div>
            )}

            <LetsEncryptWalkthrough tls={tls} />

            <div className="flex items-center justify-between border-t border-border pt-2">
              <span className="text-[11px] text-muted-foreground">config.yaml</span>
              <Button size="sm" variant="ghost" onClick={copyYaml}>
                {copied ? <Check /> : <Copy />} {t("set.copy")}
              </Button>
            </div>
            <pre className="overflow-x-auto rounded border border-border bg-background p-2 font-mono text-[10px] text-muted-foreground">
              {tlsYaml(tls)}
            </pre>
          </>
        )}

        {/* The onboarding tour's last step anchors here, and the wrapper exists
            so the highlight covers the path AND the sentence about secret.key
            underneath it — the path alone would highlight a directory name with
            no statement of why it matters. Reached at /settings?tab=security:
            the tab is URL-driven (see the Tabs in SettingsPage above), which is
            what lets the tour navigate straight to it rather than asking the
            operator to find it. See ui/src/lib/tourSteps.ts. */}
        <div data-tour="data-directory" className="flex flex-col gap-2">
          <div className="flex items-start justify-between gap-2 border-t border-border pt-2">
            <span className="shrink-0 text-[11px] text-muted-foreground">{t("set.dataDirectory")}</span>
            <code className="min-w-0 break-all text-right font-mono text-[10px]">
              {system?.dataDir ?? "—"}
            </code>
          </div>
          <p className="text-[10px] text-muted-foreground">
            OAuth tokens and client secrets are encrypted at rest with NaCl secretbox, keyed by
            <code className="mx-1 font-mono">secret.key</code> in that directory. Back it up with the
            database, or connected accounts must be re-authorised.
          </p>
        </div>
      </CardContent>
    </Card>
  );
}

function ApiTokens() {
  const t = useT();
  const [tokens, setTokens] = useState<ApiToken[]>([]);
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
    api.listTokens().then(setTokens).catch(() => {});
  };
  useEffect(load, []);

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
              </SelectContent>
            </Select>
          </div>
          <Button type="submit" size="sm" disabled={busy || !name.trim()}>
            {busy ? <Loader2 className="animate-spin" /> : <KeyRound />} Create token
          </Button>
        </form>
        <span className="text-[10px] text-muted-foreground">
          {scope === "read" ? t("set.tokenScopeReadHint") : t("set.tokenScopeAdminHint")}
        </span>

        {minted && (
          <div className="flex flex-col gap-1.5 rounded border border-warn/50 bg-warn/5 p-2">
            <span className="text-[11px] font-medium">
              Copy {minted.token.name} now — it is never shown again.
            </span>
            <div className="flex items-center gap-1.5">
              <code className="min-w-0 flex-1 overflow-x-auto rounded border border-border bg-background px-2 py-1 font-mono text-[11px]">
                {minted.plaintext}
              </code>
              <Button size="sm" variant="outline" onClick={copy}>
                {copied ? <Check /> : <Copy />} {copied ? t("clipedit.copied") : t("common.copy")}
              </Button>
            </div>
            <span className="text-[10px] text-muted-foreground">{t("set.tokenHashNote")}</span>
          </div>
        )}

        {tokens.length === 0 ? (
          <span className="text-[11px] text-muted-foreground">{t("set.noTokens")}</span>
        ) : (
          <div className="flex flex-col gap-1">
            {tokens.map((t) => (
              <div
                key={t.id}
                className="flex items-center justify-between gap-2 rounded border border-border bg-background px-2 py-1.5"
              >
                <div className="min-w-0">
                  <div className="flex items-center gap-1.5">
                    <span className="truncate text-[12px]">{t.name}</span>
                    {/* Admin is the one worth spotting in a list, so it is the
                        one that gets the loud variant. A read token is the
                        expected case and reads as ordinary. */}
                    <Badge variant={t.scope === "admin" ? "warn" : "outline"}>
                      {t.scope}
                    </Badge>
                  </div>
                  <div className="font-mono text-[10px] text-muted-foreground">
                    {t.prefix}… · created {new Date(t.createdAt).toLocaleDateString()} ·{" "}
                    {tokenLastUsed(t)}
                  </div>
                </div>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  onClick={() => revoke(t)}
                  aria-label={`Revoke ${t.name}`}
                >
                  <Trash2 />
                </Button>
              </div>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

/** The server sends the zero time for a token nothing has presented yet. */
function tokenLastUsed(t: ApiToken): string {
  const used = new Date(t.lastUsedAt);
  if (Number.isNaN(used.getTime()) || used.getUTCFullYear() <= 1) return "never used";
  return `last used ${used.toLocaleString()}`;
}
