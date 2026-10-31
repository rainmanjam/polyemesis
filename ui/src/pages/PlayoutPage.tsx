import { useCallback, useEffect, useId, useMemo, useState } from "react";
import { toast } from "sonner";
import { ConfirmDestructive } from "@/components/ConfirmDestructive";
import { useConfirm } from "@/hooks/useConfirm";
import {
  Check,
  ChevronDown,
  Copy,
  Eye,
  Globe,
  KeyRound,
  Loader2,
  Lock,
  Plus,
  RotateCw,
  ShieldAlert,
  Trash2,
  Radio,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { PageHeader } from "@/components/AppLayout";
import { StatusDot } from "@/components/signature/StatusDot";
import { Stat } from "@/components/signature/Stat";
import { api } from "@/lib/api";
import { bytes as fmtBytes } from "@/lib/format";
import { cn } from "@/lib/utils";
import { sourceChoices } from "@/lib/destinationSource";
import { asSourceId } from "@/lib/types";
import type {
  PlayoutAdminView,
  PlayoutProtection,
  PlayoutSettings,
  PlayoutVariant,
  RenditionView,
  Settings,
  SourceView,
} from "@/lib/types";
import { useT, type TranslationKey } from "@/lib/i18n";
import { DisabledReason } from "@/components/DisabledReason";
import { SaveStatus } from "@/components/SaveStatus";
import { disabledDescribedBy } from "@/lib/disabledReason";
import { useSaveModel } from "@/lib/saveModel";

const MAX_VARIANTS = 8;

/** Playout is the viewer-facing origin.
 *
 *  Two halves, and they are saved by different endpoints because they are
 *  different kinds of decision:
 *
 *   - The LADDER lives in Settings.playout: which rungs exist, which rendition
 *     each copies, how long a segment is. Saved with the rest of settings, and
 *     it restarts muxers.
 *   - PUBLISHING lives on its own: who may watch, and what the player page
 *     says. Saved on its own so changing a title does not cycle a live stream.
 *
 *  The page leads with exposure because that is the fact an operator most needs
 *  to be sure of, and the one that is most expensive to get wrong. */
export function PlayoutPage() {
  const t = useT();
  const [view, setView] = useState<PlayoutAdminView | null>(null);
  const [settings, setSettings] = useState<Settings | null>(null);
  const [renditions, setRenditions] = useState<RenditionView[]>([]);
  const [sources, setSources] = useState<SourceView[]>([]);
  const [busy, setBusy] = useState(false);

  const refresh = useCallback(async () => {
    try {
      const [v, s, r, src] = await Promise.all([
        api.playout(),
        api.getSettings(),
        api.listRenditions().catch(() => [] as RenditionView[]),
        api.listSources().catch(() => [] as SourceView[]),
      ]);
      setView(v);
      setSettings(s);
      setRenditions(r);
      setSources(src);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("play.couldNotLoadPlayout"));
    }
  }, [t]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  // The viewer count and the ladder's running state move on their own, so the
  // page polls. Deliberately not on the telemetry socket: playout is a page
  // somebody opens occasionally, and adding it to the always-on feed would cost
  // every other page a payload they never read.
  useEffect(() => {
    const id = window.setInterval(() => {
      void api
        .playout()
        .then(setView)
        .catch(() => {
          // A blip on a poll is not worth a toast; the next tick recovers.
        });
    }, 5000);
    return () => window.clearInterval(id);
  }, []);

  /** Save the ladder. The server merges over stored settings, so sending the
   *  whole object is safe and keeps this page from having to know what else
   *  lives in there. */
  /** The write itself, which THROWS on failure so a draft held by
   *  useSaveModel can stay dirty and say "Not saved" rather than look applied.
   *  savePlayoutSettings below is the same write for a switch or a choice,
   *  which applies at once and only needs the toast. */
  const putPlayout = useCallback(
    async (next: PlayoutSettings) => {
      if (!settings) return;
      setBusy(true);
      try {
        const saved = await api.putSettings({ ...settings, playout: next });
        setSettings(saved);
        setView(await api.playout());
      } catch (err) {
        toast.error(err instanceof Error ? err.message : t("play.saveFailed"));
        // Re-read rather than leave the form showing a value the server
        // rejected, which would look saved.
        void refresh();
        throw err;
      } finally {
        setBusy(false);
      }
    },
    [settings, refresh, t],
  );

  const savePlayoutSettings = useCallback(
    (next: PlayoutSettings) => putPlayout(next).catch(() => undefined),
    [putPlayout],
  );

  /** Same split as putPlayout: this one throws, for the title and
   *  description draft (app-council A20); savePublish swallows, for the
   *  protection choice, which applies at once. */
  const putPublish = useCallback(
    async (body: {
      protection?: PlayoutProtection;
      title?: string;
      description?: string;
    }) => {
      setBusy(true);
      try {
        await api.updatePlayoutPublish(body);
        setView(await api.playout());
      } catch (err) {
        toast.error(err instanceof Error ? err.message : t("play.saveFailed"));
        throw err;
      } finally {
        setBusy(false);
      }
    },
    [t],
  );

  const savePublish = useCallback(
    (body: { protection?: PlayoutProtection }) => putPublish(body).catch(() => undefined),
    [putPublish],
  );

  const rotate = useCallback(async () => {
    setBusy(true);
    try {
      await api.rotatePlayoutToken();
      setView(await api.playout());
      toast.success(t("play.linkReset"));
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("sources.rotateFailed"));
    } finally {
      setBusy(false);
    }
  }, [t]);

  const play = settings?.playout;

  if (!view || !settings || !play) {
    return (
      <div className="flex h-64 items-center justify-center">
        <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
      </div>
    );
  }

  const analytics = view.status.analytics;
  const usage = view.status.usage;
  const variants = view.status.variants ?? [];

  return (
    <div className="p-4">
      <PageHeader
        title={t("play.title")}
        subtitle={t("play.subtitle")}
        actions={
          <div className="flex items-center gap-2">
            <span className="text-sm text-muted-foreground">
              {play.enabled ? t("common.enabled") : t("common.disabled")}
            </span>
            <Switch
              checked={play.enabled}
              disabled={busy}
              onCheckedChange={(enabled) => void savePlayoutSettings({ ...play, enabled })}
              aria-label={t("play.enable")}
            />
          </div>
        }
      />

      {/* app-council A17: one status line under the title says whether playout
          is on, who can watch and how many are watching, so the page reads as
          a control room before it reads as a form. A18: when it is off, the
          line carries the one action that turns it on. */}
      <ExposureBanner
        view={view}
        busy={busy}
        onTurnOn={() => void savePlayoutSettings({ ...play, enabled: true })}
      />

      <ProgrammeCard play={play} sources={sources} busy={busy} onSave={savePlayoutSettings} />

      {!view.running && play.enabled && (
        <Card className="mb-3 border-warn/40">
          <CardContent className="flex items-center gap-2 py-3 text-sm text-muted-foreground">
            <ShieldAlert className="h-4 w-4 shrink-0 text-warn" />
            {t("play.packagerNotRunning")}
          </CardContent>
        </Card>
      )}

      {/* Share and Who can watch side by side: the two decisions an operator
          makes on air. Everything that restarts the stream is under Advanced. */}
      <div className="grid gap-3 lg:grid-cols-2">
        <ShareCard view={view} play={play} busy={busy} onRotate={rotate} />
        <ProtectionCard
          view={view}
          play={play}
          busy={busy}
          onSave={savePublish}
          onSaveSettings={savePlayoutSettings}
          className="mt-0"
        />
      </div>

      <Card className="mt-3">
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Eye className="h-3.5 w-3.5" />
            {t("play.audienceTitle")}
          </CardTitle>
          <CardDescription>{t("play.audienceDesc")}</CardDescription>
        </CardHeader>
        <CardContent>
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
            <Stat labelKey="play.watchingNow" value={String(analytics.viewers)} />
            <Stat labelKey="play.peak" value={String(analytics.peak)} />
            <Stat labelKey="play.sessions" value={String(analytics.sessions)} />
            <Stat labelKey="play.onDisk" value={fmtBytes(usage.bytes)} />
          </div>
          {analytics.uncounted > 0 && (
            <p className="mt-3 text-sm text-muted-foreground">
              {t("play.uncountedViewers", { count: analytics.uncounted })}
            </p>
          )}
          {usage.overLimit && (
            <p className="mt-3 flex items-start gap-1.5 text-sm text-down">
              <ShieldAlert className="mt-0.5 h-3 w-3 shrink-0" />
              {t("play.diskCapBelowWindow")}
            </p>
          )}
          <div className="mt-3">
            <Button
              variant="ghost"
              size="sm"
              disabled={busy || !view.running}
              onClick={() => {
                void api
                  .resetPlayoutAnalytics()
                  .then(() => api.playout())
                  .then(setView)
                  .catch(() => toast.error(t("play.resetFailed")));
              }}
            >
              <RotateCw />
              {t("play.resetCounters")}
            </Button>
          </div>
        </CardContent>
      </Card>

      <PlayerPageCard
        title={view.title}
        description={view.description}
        onSave={(body) => putPublish(body)}
      />

      <AdvancedSection
        play={play}
        variants={variants}
        renditions={renditions}
        busy={busy}
        viewers={analytics.viewers}
        onSave={savePlayoutSettings}
        onApply={putPlayout}
      />
    </div>
  );
}

// ---------------------------------------------------------------- exposure

/** The one thing on this page that must never be subtle.
 *
 *  Silently exposing somebody's stream would be a serious bug, so the state is
 *  stated in words at the top of the page rather than inferred from a toggle
 *  three cards down.
 *
 *  app-council A17: it is also the page's status line. It leads with whether
 *  playout is on and ends with how many are watching, so an operator reads
 *  state, audience and exposure in one place. A18: when playout is off, it
 *  carries the one primary action that turns it on, instead of leaving the
 *  header switch as the only way. Exported for its test. */
export function ExposureBanner({
  view,
  busy,
  onTurnOn,
}: {
  view: PlayoutAdminView;
  busy: boolean;
  onTurnOn: () => void;
}) {
  const t = useT();
  const counts = (
    <span className="ml-auto flex shrink-0 items-center gap-3 text-sm text-muted-foreground">
      <span>
        {t("play.watchingNow")}{" "}
        <span className="tnum font-mono text-foreground">{view.status.analytics.viewers}</span>
      </span>
      <span>
        {t("play.sessions")}{" "}
        <span className="tnum font-mono text-foreground">{view.status.analytics.sessions}</span>
      </span>
    </span>
  );
  const state = (on: boolean) => (
    <span className="flex shrink-0 items-center gap-1.5 font-medium">
      <StatusDot tone={on ? "live" : "idle"} />
      {on ? t("play.statusOn") : t("play.statusOff")}
    </span>
  );
  if (!view.status.enabled) {
    return (
      <div
        data-playout-status="off"
        className="mb-3 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-md border border-border bg-background px-3 py-2 text-sm"
      >
        {state(false)}
        <span className="flex items-center gap-1.5 text-muted-foreground">
          <Lock className="h-3.5 w-3.5 shrink-0" />
          {t("play.offBody")}
        </span>
        <Button size="sm" className="ml-auto" disabled={busy} onClick={onTurnOn}>
          {t("play.turnOn")}
        </Button>
      </div>
    );
  }
  if (!view.status.public) {
    return (
      <div
        data-playout-status="private"
        className="mb-3 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-md border border-border bg-background px-3 py-2 text-sm"
      >
        {state(true)}
        <span className="flex items-center gap-1.5">
          <Lock className="h-3.5 w-3.5 shrink-0 text-live" />
          <span>
            <span className="font-medium">{t("play.private")}</span>{" "}
            <span className="text-muted-foreground">
              {t("play.privateBanner", { public: t("play.public") })}
            </span>
          </span>
        </span>
        {counts}
      </div>
    );
  }
  if (view.exposed) {
    return (
      <div
        data-playout-status="exposed"
        className="mb-3 flex flex-wrap items-start gap-x-3 gap-y-2 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm"
      >
        {state(true)}
        <span className="flex items-start gap-1.5">
          <Globe className="mt-0.5 h-3.5 w-3.5 shrink-0 text-destructive" />
          <span>
            <span className="font-medium text-destructive">
              {t("play.exposedTitle")}
            </span>{" "}
            <span className="text-muted-foreground">
              {t("play.exposedBody", { mode: t("play.playbackLink") })}
            </span>
          </span>
        </span>
        {counts}
      </div>
    );
  }
  return (
    <div
      data-playout-status="unlisted"
      className="mb-3 flex flex-wrap items-start gap-x-3 gap-y-2 rounded-md border border-warn/40 bg-warn/10 px-3 py-2 text-sm"
    >
      {state(true)}
      <span className="flex items-start gap-1.5">
        <KeyRound className="mt-0.5 h-3.5 w-3.5 shrink-0 text-warn" />
        <span>
          <span className="font-medium">{t("play.unlisted")}</span>{" "}
          <span className="text-muted-foreground">
            {t("play.unlistedBanner")}
          </span>
        </span>
      </span>
      {counts}
    </div>
  );
}

// --------------------------------------------------------------- programme

/** Which programme the PUBLIC page serves.
 *
 *  Settings are a singleton -- one playout intent for the whole box -- while
 *  a multi-source install runs a separate playout manager per engine
 *  (playout.go:273 reads `set.Playout.SourceID` to pick one of them). This
 *  interface had the field on the wire and nowhere to set it from the UI, so
 *  the public route served whichever engine happened to come up first: a
 *  two-programme install could be showing its audience the horizontal feed
 *  while the operator broadcasts, and believes they are sharing, the
 *  vertical one.
 *
 *  Hidden entirely on a single-source install, the same call ShareCard's
 *  sibling controls and DestinationDialog's own source picker make: with one
 *  programme there is nothing to choose, and a Select whose every use lands
 *  on the same answer is not a choice, it is a shape occupying a slot. */
export function ProgrammeCard({
  play,
  sources,
  busy,
  onSave,
}: {
  play: PlayoutSettings;
  sources: SourceView[];
  busy: boolean;
  onSave: (next: PlayoutSettings) => Promise<void>;
}) {
  const t = useT();
  const choices = useMemo(() => sourceChoices(sources), [sources]);
  if (choices.length < 2) return null;

  const value = play.sourceId != null ? String(play.sourceId) : "default";

  return (
    <Card className="mb-3">
      <CardHeader>
        <CardTitle className="text-[13px]">{t("play.whichProgramme")}</CardTitle>
        <CardDescription>{t("play.programmeNote")}</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-1.5">
        <Label htmlFor="playout-source">{t("play.programme")}</Label>
        <Select
          value={value}
          disabled={busy}
          onValueChange={(v) =>
            void onSave({
              ...play,
              sourceId: v === "default" ? null : asSourceId(Number(v)),
            })
          }
        >
          <SelectTrigger id="playout-source" className="max-w-sm">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="default">{t("play.defaultProgramme")}</SelectItem>
            {choices.map((c) => (
              <SelectItem key={c.id} value={String(c.id)}>
                {c.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </CardContent>
    </Card>
  );
}

// ------------------------------------------------------------------- share

function CopyField({
  label,
  value,
  hint,
  mono = true,
  disabled = false,
  describedBy,
}: {
  label: string;
  value: string;
  hint?: string;
  mono?: boolean;
  /** Greyed while playout is off (app-council A18): a link that serves
   *  nothing must not look ready to hand out. The reason is the card's. */
  disabled?: boolean;
  describedBy?: string;
}) {
  const t = useT();
  const [copied, setCopied] = useState(false);
  return (
    <div className="grid gap-1.5">
      <Label>{label}</Label>
      <div className="flex items-center gap-1.5">
        <Input
          readOnly
          value={value}
          disabled={disabled}
          aria-label={label}
          aria-describedby={describedBy}
          onFocus={(e) => e.currentTarget.select()}
          className={cn("text-sm", mono && "font-mono")}
        />
        <Button
          variant="secondary"
          size="icon-sm"
          disabled={disabled}
          aria-describedby={describedBy}
          aria-label={t("play.copyAria", { label })}
          onClick={() => {
            navigator.clipboard
              .writeText(value)
              .then(() => {
                setCopied(true);
                window.setTimeout(() => setCopied(false), 1500);
              })
              .catch(() => toast.error(t("play.clipboardUnavailable")));
          }}
        >
          {copied ? <Check /> : <Copy />}
        </Button>
      </div>
      {hint && <p className="text-sm text-muted-foreground">{hint}</p>}
    </div>
  );
}

/** Exported for its test, like VariantsCard below. */
export function ShareCard({
  view,
  play,
  busy,
  onRotate,
}: {
  view: PlayoutAdminView;
  play: PlayoutSettings;
  busy: boolean;
  /** Awaited, so the dialog stays up until the rotation has actually landed
   *  rather than closing over a request still in flight. */
  onRotate: () => Promise<void>;
}) {
  const t = useT();
  const protectedLink = play.public && view.protection === "token";
  /* ONE CLICK ROTATED THE TOKEN. Every link already shared stopped working and
   * every viewer currently pulling segments was dropped, on a single
   * unconfirmed press of a ghost button -- on a page whose ladder rungs, which
   * are three fields to rebuild, ask first and name the viewers. The sentence
   * under the button said what it did; it said it BEFORE, to somebody who had
   * already decided to click, which is the one moment prose does not reach.
   *
   * The old token cannot be brought back, so there is nothing to undo to. It
   * goes through the same dialog every other destructive action here uses, and
   * the dialog names the number that makes it a decision. */
  const [rotateOpen, setRotateOpen] = useState(false);
  const viewers = view.status.analytics.viewers;
  // app-council A18: while playout is off the links serve nothing, so they are
  // greyed with the reason in words above them rather than looking ready.
  const offReasonId = useId();
  const offReason = play.enabled ? null : t("play.shareOffReason");
  const describedBy = disabledDescribedBy(offReasonId, offReason);
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("play.share")}</CardTitle>
        <CardDescription>
          {play.public
            ? t("play.linksPublic") : t("play.linksProtected")}
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3">
        <DisabledReason id={offReasonId} reason={offReason} />
        <CopyField
          label={t("play.playerPage")}
          value={view.urls.watch}
          disabled={!play.enabled}
          describedBy={describedBy}
          hint={
            protectedLink
              ? t("play.carriesThePlaybackTokenAnyone")
              : undefined
          }
        />
        <CopyField
          label={t("play.hlsPlaylist")}
          value={view.urls.master}
          hint={t("play.forObsACdnOr")}
          disabled={!play.enabled}
          describedBy={describedBy}
        />
        <div className="grid gap-1.5">
          <Label htmlFor="playout-embed">{t("play.embedCode")}</Label>
          <Textarea
            id="playout-embed"
            readOnly
            rows={3}
            value={view.urls.embed}
            disabled={!play.enabled}
            aria-describedby={describedBy}
            onFocus={(e) => e.currentTarget.select()}
            className="font-mono text-sm"
          />
          {!play.allowCrossOrigin && (
            <p className="text-sm text-warn">
              {t("play.embedNeedsCrossOrigin", { setting: t("play.allowCrossOrigin") })}
            </p>
          )}
        </div>
        {protectedLink && (
          <div>
            <Button
              variant="ghost"
              size="sm"
              disabled={busy}
              onClick={() => setRotateOpen(true)}
            >
              <KeyRound />
              {t("play.rotate")}
            </Button>
            <p className="mt-1 text-sm text-muted-foreground">
              {t("play.rotateHint")}
            </p>
            <ConfirmDestructive
              open={rotateOpen}
              onOpenChange={setRotateOpen}
              // No one target: rotating kills every shared link at once.
              subject={{ unnamed: true }}
              title={t("play.rotateTitle")}
              description={t("play.rotateDescription")}
              consequencesLabel={t("play.rotateDrops")}
              // Only when somebody is actually watching. A row reading 0 says
              // "this does nothing", and that is false -- every shared link
              // dies whether or not a player is pulling segments right now.
              consequences={
                viewers > 0
                  ? [{ label: t("play.watching"), count: viewers }]
                  : undefined
              }
              confirmLabel={t("play.rotate")}
              onConfirm={onRotate}
            />
          </div>
        )}
      </CardContent>
    </Card>
  );
}

// -------------------------------------------------------------- protection

export function ProtectionCard({
  view,
  play,
  busy,
  onSave,
  onSaveSettings,
  className = "mt-3",
}: {
  view: PlayoutAdminView;
  play: PlayoutSettings;
  busy: boolean;
  onSave: (body: { protection?: PlayoutProtection }) => Promise<void>;
  onSaveSettings: (next: PlayoutSettings) => Promise<void>;
  className?: string;
}) {
  const t = useT();
  // A15: making the stream reachable by people who are not signed in changes who
  // can watch, so it asks first. Turning it back to token or off does not.
  const exposure = useConfirm<"public" | "open">();
  return (
    <Card className={className}>
      <CardHeader>
        <CardTitle>{t("play.whoCanWatch")}</CardTitle>
        <CardDescription>
            {t("play.protectedNote")}
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3">
        <div className="grid gap-1.5">
          <Label htmlFor="playout-protection">{t("play.protection")}</Label>
          <Select
            value={view.protection}
            disabled={busy}
            onValueChange={(v) => {
              const next = v as PlayoutProtection;
              if (next === "open" && view.protection !== "open") exposure.ask("open");
              else void onSave({ protection: next });
            }}
          >
            <SelectTrigger id="playout-protection" className="max-w-sm">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="token">
                {t("play.protectionToken")}
              </SelectItem>
              <SelectItem value="open">
                {t("play.protectionOpen")}
              </SelectItem>
            </SelectContent>
          </Select>
          <p className="text-sm text-muted-foreground">
            {view.protection === "token"
              ? t("play.basicPasswordNote") : t("play.noCredentialNote")}
          </p>
        </div>

        <div className="flex items-start justify-between gap-3 rounded-md border border-border p-2.5">
          <div>
            <p className="text-sm font-medium">{t("play.public")}</p>
            <p className="text-sm text-muted-foreground">{t("play.publicNote")}</p>
          </div>
          <Switch
            checked={play.public}
            disabled={busy}
            aria-label={t("play.serveWithoutSession")}
            onCheckedChange={(pub) => {
              if (pub) exposure.ask("public");
              else void onSaveSettings({ ...play, public: pub });
            }}
          />
        </div>
        <ConfirmDestructive
          open={exposure.open}
          onOpenChange={exposure.onOpenChange}
          subject={exposure.target === "open" ? t("play.protectionOpen") : t("play.public")}
          title={exposure.target === "open" ? t("play.confirmOpenTitle") : t("play.confirmPublicTitle")}
          description={exposure.target === "open" ? t("play.confirmOpenDescription") : t("play.confirmPublicDescription")}
          confirmLabel={exposure.target === "open" ? t("play.confirmOpenAction") : t("play.confirmPublicAction")}
          tone="caution"
          onConfirm={async () => {
            const kind = exposure.target;
            exposure.close();
            if (kind === "public") await onSaveSettings({ ...play, public: true });
            else if (kind === "open") await onSave({ protection: "open" });
          }}
        />

        <div className="flex items-start justify-between gap-3 rounded-md border border-border p-2.5">
          <div>
            <p className="text-sm font-medium">{t("play.allowCrossOrigin")}</p>
            <p className="text-sm text-muted-foreground">{t("play.crossOriginNote")}</p>
          </div>
          <Switch
            checked={play.allowCrossOrigin}
            disabled={busy}
            aria-label={t("play.allowCrossOriginPlayback")}
            onCheckedChange={(allow) =>
              void onSaveSettings({ ...play, allowCrossOrigin: allow })
            }
          />
        </div>
      </CardContent>
    </Card>
  );
}

// ---------------------------------------------------------------- variants

export function VariantsCard({
  play,
  variants,
  renditions,
  busy,
  onSave,
  onApply = onSave,
  onDirtyChange,
}: {
  play: PlayoutSettings;
  variants: PlayoutAdminView["status"]["variants"];
  renditions: RenditionView[];
  busy: boolean;
  onSave: (next: PlayoutSettings) => Promise<void>;
  /** The typed fields' write, which throws on failure so the row's draft can
   *  say "Not saved". Defaults to onSave for a caller with only one. */
  onApply?: (next: PlayoutSettings) => Promise<void>;
  /** Per row, keyed by the rung's name, so a section can stay open while any
   *  row holds an unapplied edit. */
  onDirtyChange?: (key: string, dirty: boolean) => void;
}) {
  const t = useT();
  /* Removing a rung stopped its muxer and dropped everyone watching it the
     instant the icon was clicked -- no dialog of any kind, on an install where
     deleting a recording that can be downloaded again demands typing its
     filename. The inconsistency was the defect: an operator who has learned
     "deletes ask first" has had their caution trained out of them exactly
     where it mattered.

     A CONTROL: the click no longer performs the action, it opens a dialog that
     names the rung and the viewer count the row is already showing. No
     requireTyping -- a rung is three fields and comes back with one click, and
     spending typed confirmation on a recoverable action is how the typed
     confirmation stops meaning anything on the actions that are not. */
  const confirmRemove = useConfirm<{ index: number; name: string; viewers: number }>();
  const statusByName = useMemo(() => {
    const m = new Map<string, NonNullable<typeof variants>[number]>();
    for (const v of variants ?? []) m.set(v.name, v);
    return m;
  }, [variants]);

  const update = (i: number, patch: Partial<PlayoutVariant>) => {
    const next = play.variants.map((v, j) => (j === i ? { ...v, ...patch } : v));
    void onSave({ ...play, variants: next });
  };

  const add = () => {
    // Named for what it will be, uniquely, so the form never opens on a name
    // the server is about to reject as a duplicate.
    const used = new Set(play.variants.map((v) => v.name));
    let n = play.variants.length + 1;
    while (used.has(`rung${n}`)) n++;
    void onSave({
      ...play,
      variants: [
        ...play.variants,
        { name: `rung${n}`, enabled: true, renditionId: null, audioTrack: 0 },
      ],
    });
  };

  return (
    <Card className="mt-3">
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Radio className="h-3.5 w-3.5" />
          {t("play.ladder")}
        </CardTitle>
        <CardDescription>
            {t("play.rungNote")}
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-2">
        {play.variants.length === 0 && (
          <p className="text-sm text-muted-foreground">
            {t("play.noRungs")}
          </p>
        )}

        {play.variants.map((v, i) => (
          <RungRow
            key={v.name}
            variant={v}
            status={statusByName.get(v.name)}
            renditions={renditions}
            busy={busy}
            onUpdate={(patch) => update(i, patch)}
            onApply={(patch) => onApply({
              ...play,
              variants: play.variants.map((x, j) => (j === i ? { ...x, ...patch } : x)),
            })}
            onRemove={(viewers) => confirmRemove.ask({ index: i, name: v.name, viewers })}
            onDirtyChange={onDirtyChange}
          />
        ))}

        <div>
          <Button
            variant="secondary"
            size="sm"
            disabled={busy || play.variants.length >= MAX_VARIANTS}
            onClick={add}
          >
            <Plus />
            {t("play.addRung")}
          </Button>
        </div>
      </CardContent>

      <ConfirmDestructive
        open={confirmRemove.open}
        onOpenChange={confirmRemove.onOpenChange}
        subject={confirmRemove.target?.name ?? ""}
        title={t("play.removeRungTitle", { name: confirmRemove.target?.name ?? "" })}
        description={t("play.removeRungDescription")}
        consequencesLabel={t("play.removeRungDrops")}
        consequences={[
          { label: t("play.watching"), count: confirmRemove.target?.viewers ?? 0 },
        ]}
        confirmLabel={t("play.removeRung")}
        onConfirm={async () => {
          const target = confirmRemove.target;
          if (!target) return;
          await onSave({
            ...play,
            variants: play.variants.filter((_, j) => j !== target.index),
          });
        }}
      />
    </Card>
  );
}

/** One quality level. The switch and the video source apply at once -- a
 *  toggle or a choice has nothing to keep back. The name and the audio track
 *  are typed, and each change restarts this level's muxer, so they are a
 *  draft with an explicit Apply and a save status (useSaveModel), never a blur
 *  save: a blur save lost an edit that was never blurred and said nothing when
 *  it wrote one (app-council A16, A20's rule). */
function RungRow({
  variant: v,
  status: st,
  renditions,
  busy,
  onUpdate,
  onApply,
  onRemove,
  onDirtyChange,
}: {
  variant: PlayoutVariant;
  status: NonNullable<PlayoutAdminView["status"]["variants"]>[number] | undefined;
  renditions: RenditionView[];
  busy: boolean;
  onUpdate: (patch: Partial<PlayoutVariant>) => void;
  /** Throws on failure, so the draft stays and says "Not saved". */
  onApply: (patch: Pick<PlayoutVariant, "name" | "audioTrack">) => Promise<void>;
  onRemove: (viewers: number) => void;
  /** Stable (a useCallback in the section), with the row's own key, so the
   *  effects below run when the row's dirtiness changes and not every render. */
  onDirtyChange?: (key: string, dirty: boolean) => void;
}) {
  const t = useT();
  const reasonId = useId();
  const model = useSaveModel<{ name: string; audioTrack: string }>({
    stored: { name: v.name, audioTrack: String(v.audioTrack) },
    equal: (a, b) => a.name === b.name && a.audioTrack === b.audioTrack,
    onSave: async (d) => {
      await onApply({ name: d.name.trim(), audioTrack: Number(d.audioTrack) });
    },
  });
  const dirtyKey = `rung:${v.name}`;
  useEffect(() => onDirtyChange?.(dirtyKey, model.dirty), [dirtyKey, model.dirty, onDirtyChange]);
  // A row that goes away (removed, or renamed and re-keyed) is not dirty.
  useEffect(() => () => onDirtyChange?.(dirtyKey, false), [dirtyKey, onDirtyChange]);
  const track = Number(model.draft.audioTrack);
  const invalid =
    model.draft.name.trim() === "" ||
    model.draft.audioTrack.trim() === "" ||
    !Number.isInteger(track) ||
    track < 0 ||
    track > 63;
  const reason = model.dirty && invalid ? t("play.fixValuesFirst") : null;
  return (
    <div className="grid items-end gap-2 rounded-md border border-border p-2.5 sm:grid-cols-[auto_1fr_1fr_auto_auto]">
      <div className="flex items-center gap-2 pb-2">
        <StatusDot tone={st?.running ? "live" : v.enabled ? "warn" : "idle"} />
        <Switch
          checked={v.enabled}
          disabled={busy}
          aria-label={t("hooks.enableAria", { name: v.name })}
          onCheckedChange={(enabled) => onUpdate({ enabled })}
        />
      </div>

      <div className="grid gap-1">
        <Label className="text-sm" htmlFor={`${reasonId}-name`}>{t("play.name")}</Label>
        <Input
          id={`${reasonId}-name`}
          value={model.draft.name}
          maxLength={32}
          className="text-sm"
          onChange={(e) => model.setDraft({ ...model.draft, name: e.target.value })}
        />
      </div>

      <div className="grid gap-1">
        <Label className="text-sm">{t("play.videoSource")}</Label>
        <Select
          value={v.renditionId == null ? "source" : String(v.renditionId)}
          disabled={busy}
          onValueChange={(val) =>
            onUpdate({ renditionId: val === "source" ? null : Number(val) })
          }
        >
          <SelectTrigger className="text-sm">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="source">{t("play.passthrough")}</SelectItem>
            {renditions.map((r) => (
              <SelectItem key={r.rendition.id} value={String(r.rendition.id)}>
                {r.rendition.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      <div className="grid gap-1">
        <Label className="text-sm" htmlFor={`${reasonId}-track`}>{t("play.audioTrack")}</Label>
        <Input
          id={`${reasonId}-track`}
          type="number"
          min={0}
          max={63}
          value={model.draft.audioTrack}
          className="w-24 text-sm"
          onChange={(e) => model.setDraft({ ...model.draft, audioTrack: e.target.value })}
        />
      </div>

      <div className="flex items-center gap-1 pb-1">
        {st?.running && st.bandwidth > 0 && (
          <Badge variant="outline" className="font-mono text-sm">
            {Math.round(st.bandwidth / 1000)}k
            {st.height ? ` · ${st.height}p` : ""}
          </Badge>
        )}
        {st?.viewers ? (
          <Badge variant="outline" className="text-sm">
            {t("stats.watching", { count: st.viewers })}
          </Badge>
        ) : null}
        <Button
          variant="ghost"
          size="icon-sm"
          disabled={busy}
          aria-label={t("playlist.remove", { name: v.name })}
          onClick={() => onRemove(st?.viewers ?? 0)}
        >
          <Trash2 />
        </Button>
      </div>

      {(model.dirty || model.state !== "clean") && (
        <div className="flex flex-wrap items-center gap-2 sm:col-span-5">
          {model.dirty && (
            <>
              <Button
                size="sm"
                disabled={busy || invalid}
                aria-describedby={disabledDescribedBy(`${reasonId}-reason`, reason)}
                onClick={() => void model.save()}
              >
                {t("common.apply")}
              </Button>
              <Button size="sm" variant="ghost" onClick={model.discard}>
                {t("common.discard")}
              </Button>
            </>
          )}
          <SaveStatus state={model.state} />
          <DisabledReason id={`${reasonId}-reason`} reason={reason} />
        </div>
      )}

      {st?.error && (
        <p className="text-sm text-down sm:col-span-5">{st.error}</p>
      )}
    </div>
  );
}

// --------------------------------------------------------------- packaging

/** The packaging numbers, held as typed text so a half-typed value is never
 *  coerced, and so "was 4" can be shown beside a field that now says 2. */
type PackagingNumberKey =
  | "segmentSeconds"
  | "playlistSegments"
  | "dvrWindowSeconds"
  | "maxDiskMb"
  | "audioKbps"
  | "sessionIdleSeconds"
  | "maxSessions";

type PackagingDraft = { format: PlayoutSettings["format"] } & Record<PackagingNumberKey, string>;

const PACKAGING_FIELDS: {
  key: PackagingNumberKey;
  id: string;
  label: TranslationKey;
  hint: TranslationKey;
  min: number;
  max: number;
}[] = [
  { key: "segmentSeconds", id: "playout-segment", label: "play.segmentLength", hint: "play.shorterIsLowerLatencyAnd", min: 1, max: 30 },
  { key: "playlistSegments", id: "playout-window", label: "play.liveWindow", hint: "play.threeIsTheMinimumA", min: 3, max: 100 },
  { key: "dvrWindowSeconds", id: "playout-dvr", label: "play.dvrWindow", hint: "play.dvrHint", min: 0, max: 43200 },
  { key: "maxDiskMb", id: "playout-disk", label: "play.diskCap", hint: "play.acrossEveryRungTheBackstop", min: 64, max: 1048576 },
  { key: "audioKbps", id: "playout-audio", label: "play.audioBitrate", hint: "play.oneStereoAacTrackPer", min: 32, max: 512 },
  { key: "sessionIdleSeconds", id: "playout-idle", label: "play.idleTimeout", hint: "play.mustExceedOneSegmentOr", min: 5, max: 3600 },
  { key: "maxSessions", id: "playout-sessions", label: "play.sessionCap", hint: "play.boundsTheViewerTableViewers", min: 1, max: 1000000 },
];

function packagingOf(play: PlayoutSettings): PackagingDraft {
  const d = { format: play.format } as PackagingDraft;
  for (const f of PACKAGING_FIELDS) d[f.key] = String(play[f.key]);
  return d;
}

const samePackaging = (a: PackagingDraft, b: PackagingDraft) =>
  a.format === b.format && PACKAGING_FIELDS.every((f) => a[f.key] === b[f.key]);

/** A typed value that is a whole number inside the field's bounds, or null. */
function packagingValue(raw: string, min: number, max: number): number | null {
  if (raw.trim() === "") return null;
  const n = Number(raw);
  return Number.isInteger(n) && n >= min && n <= max ? n : null;
}

/** Packaging: every field here restarts the muxers.
 *
 *  app-council A16. Each of eight fields used to commit its own PUT on blur,
 *  and each PUT restarted the stream: tabbing through two fields on air was
 *  two interruptions, with nothing on screen saying so. The fields are now one
 *  draft (useSaveModel). Nothing is written until "Apply and restart stream",
 *  the bar that offers it appears only while something has changed, it counts
 *  the changes and shows what each field was, and with anyone watching it asks
 *  first and names how many. Exported for its test. */
export function PackagingCard({
  play,
  busy,
  viewers,
  onApply,
  onDirtyChange,
}: {
  play: PlayoutSettings;
  busy: boolean;
  viewers: number;
  /** Throws on failure, so the draft stays and says "Not saved". */
  onApply: (next: PlayoutSettings) => Promise<void>;
  onDirtyChange?: (dirty: boolean) => void;
}) {
  const t = useT();
  const reasonId = useId();
  const [confirmOpen, setConfirmOpen] = useState(false);
  const stored = packagingOf(play);
  const model = useSaveModel<PackagingDraft>({
    stored,
    equal: samePackaging,
    onSave: async (d) => {
      // Merged over the CURRENT settings at write time, so an Enabled or
      // Public switch flipped while this draft was open is not reverted.
      const next: PlayoutSettings = { ...play, format: d.format };
      for (const f of PACKAGING_FIELDS) next[f.key] = Number(d[f.key]);
      await onApply(next);
    },
  });
  const { draft, dirty } = model;
  useEffect(() => onDirtyChange?.(dirty), [dirty, onDirtyChange]);

  const changed = [
    draft.format !== stored.format,
    ...PACKAGING_FIELDS.map((f) => draft[f.key] !== stored[f.key]),
  ].filter(Boolean).length;
  const invalid = PACKAGING_FIELDS.some((f) => packagingValue(draft[f.key], f.min, f.max) === null);
  const reason = invalid ? t("play.fixValuesFirst") : null;

  const apply = () => {
    if (viewers > 0) setConfirmOpen(true);
    else void model.save();
  };

  return (
    <Card className="mt-3">
      <CardHeader>
        <CardTitle className="flex flex-wrap items-center gap-2">
          {t("play.packaging")}
          {dirty && (
            <Badge variant="warn" className="ml-auto">
              {t("play.changesPending", { count: changed })}
            </Badge>
          )}
        </CardTitle>
        <CardDescription>
            {t("play.restartNote")}
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <div className="grid gap-1.5">
          <Label htmlFor="playout-format">{t("play.format")}</Label>
          <Select
            value={draft.format}
            disabled={busy}
            onValueChange={(v) =>
              model.setDraft({ ...draft, format: v === "hls+dash" ? "hls+dash" : "hls" })
            }
          >
            <SelectTrigger id="playout-format">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="hls">{t("play.hls")}</SelectItem>
              <SelectItem value="hls+dash">{t("play.hlsDash")}</SelectItem>
            </SelectContent>
          </Select>
          {draft.format !== stored.format && (
            <p className="font-mono text-sm text-muted-foreground">
              {t("play.was", { value: stored.format === "hls" ? t("play.hls") : t("play.hlsDash") })}
            </p>
          )}
          <p className="text-sm text-muted-foreground">
            {t("play.dashNote")}
          </p>
        </div>

        {PACKAGING_FIELDS.map((f) => {
          const raw = draft[f.key];
          const bad = packagingValue(raw, f.min, f.max) === null;
          const was = raw !== stored[f.key];
          return (
            <div key={f.key} className="grid content-start gap-1.5">
              <Label htmlFor={f.id}>{t(f.label)}</Label>
              <Input
                id={f.id}
                type="number"
                min={f.min}
                max={f.max}
                value={raw}
                disabled={busy}
                aria-invalid={bad || undefined}
                className={cn(was && "border-primary")}
                onChange={(e) => model.setDraft({ ...draft, [f.key]: e.target.value })}
              />
              {was && (
                <p className="font-mono text-sm text-muted-foreground">
                  {t("play.was", { value: stored[f.key] })}
                </p>
              )}
              {bad && (
                <p className="text-sm text-down">{t("play.outOfRange", { min: f.min, max: f.max })}</p>
              )}
              <p className="text-sm text-muted-foreground">{t(f.hint)}</p>
            </div>
          );
        })}

        {(dirty || model.state !== "clean") && (
          <div
            data-testid="packaging-apply-bar"
            className="flex flex-wrap items-center gap-3 rounded-md border border-primary/50 bg-background p-3 sm:col-span-2 lg:col-span-3"
          >
            {dirty ? (
              <div className="min-w-0 flex-1">
                <p className="text-sm">
                  <span className="font-semibold">{t("play.applyTitle")}</span>{" "}
                  {t("play.applyBody")}
                </p>
                <p className="text-sm text-muted-foreground">
                  {t("play.watchingNow")}{" "}
                  <span className="tnum font-mono text-foreground">{viewers}</span>
                </p>
                <DisabledReason id={reasonId} reason={reason} />
              </div>
            ) : (
              <div className="flex-1" />
            )}
            <SaveStatus state={model.state} />
            {dirty && (
              <>
                <Button variant="outline" size="sm" disabled={model.state === "saving"} onClick={model.discard}>
                  {t("common.discard")}
                </Button>
                <Button
                  size="sm"
                  disabled={busy || invalid || model.state === "saving"}
                  aria-describedby={disabledDescribedBy(reasonId, reason)}
                  onClick={apply}
                >
                  {t("play.applyRestart")}
                </Button>
              </>
            )}
          </div>
        )}
      </CardContent>

      <ConfirmDestructive
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        subject={{ unnamed: true }}
        title={t("play.applyConfirmTitle")}
        description={t("play.applyConfirmDescription")}
        consequencesLabel={t("play.removeRungDrops")}
        consequences={[{ label: t("play.watching"), count: viewers }]}
        confirmLabel={t("play.applyRestart")}
        tone="caution"
        onConfirm={async () => {
          setConfirmOpen(false);
          await model.save();
        }}
      />
    </Card>
  );
}

/** Ladder and Packaging, behind one closed "Advanced" section (app-council
 *  A17). Both restart the stream, and neither is what an operator reaches for
 *  on air, so they no longer push Share and Who can watch down the page.
 *
 *  It cannot be closed over unapplied packaging changes: collapsing it would
 *  hide the only bar that says the stream has not been restarted with them. */
export function AdvancedSection({
  play,
  variants,
  renditions,
  busy,
  viewers,
  onSave,
  onApply,
}: {
  play: PlayoutSettings;
  variants: PlayoutAdminView["status"]["variants"];
  renditions: RenditionView[];
  busy: boolean;
  viewers: number;
  onSave: (next: PlayoutSettings) => Promise<void>;
  onApply: (next: PlayoutSettings) => Promise<void>;
}) {
  const t = useT();
  const id = useId();
  const [open, setOpen] = useState(false);
  const [dirtyParts, setDirtyParts] = useState<Record<string, boolean>>({});
  const markDirty = useCallback((key: string, d: boolean) => {
    setDirtyParts((cur) => (cur[key] === d ? cur : { ...cur, [key]: d }));
  }, []);
  const markPackaging = useCallback((d: boolean) => markDirty("packaging", d), [markDirty]);
  const dirty = Object.values(dirtyParts).some(Boolean);
  const shown = open || dirty;
  const reason = dirty ? t("play.applyOrDiscardFirst") : null;
  return (
    <section className="mt-3 rounded-md border border-border">
      <button
        type="button"
        className="flex w-full flex-wrap items-center gap-x-2 gap-y-1 px-3 py-2.5 text-left"
        aria-expanded={shown}
        aria-controls={`${id}-body`}
        aria-disabled={dirty || undefined}
        aria-describedby={disabledDescribedBy(`${id}-reason`, reason)}
        onClick={() => {
          if (!dirty) setOpen(!open);
        }}
      >
        <span className="text-base font-semibold">{t("play.advanced")}</span>
        <span className="text-sm text-muted-foreground">{t("play.advancedSummary")}</span>
        <ChevronDown
          aria-hidden="true"
          className={cn("ml-auto h-4 w-4 text-muted-foreground transition-transform", shown && "rotate-180")}
        />
      </button>
      {reason && <DisabledReason id={`${id}-reason`} reason={reason} className="px-3" />}
      {/* Mounted while closed, so a draft survives the section closing. */}
      <div id={`${id}-body`} hidden={!shown} className="px-3 pb-3">
        <VariantsCard
          play={play}
          variants={variants}
          renditions={renditions}
          busy={busy}
          onSave={onSave}
          onApply={onApply}
          onDirtyChange={markDirty}
        />
        <PackagingCard
          play={play}
          busy={busy}
          viewers={viewers}
          onApply={onApply}
          onDirtyChange={markPackaging}
        />
      </div>
    </section>
  );
}

// ------------------------------------------------------------- player page

/** Title and description on the public player page.
 *
 *  app-council A20. These saved on blur with no sign that anything was
 *  written, and an edit that was never blurred -- a tab closed, a route
 *  changed -- was simply lost. They are a draft now (useSaveModel): Save is
 *  offered once something differs, and the status line says Unsaved changes,
 *  Saving, Saved or Not saved. Exported for its test. */
export function PlayerPageCard({
  title,
  description,
  onSave,
}: {
  title: string;
  description: string;
  /** Throws on failure, so the draft stays and says "Not saved". */
  onSave: (body: { title: string; description: string }) => Promise<void>;
}) {
  const t = useT();
  const model = useSaveModel<{ title: string; description: string }>({
    stored: { title, description },
    equal: (a, b) => a.title === b.title && a.description === b.description,
    onSave,
  });
  return (
    <Card className="mt-3">
      <CardHeader>
        <CardTitle>{t("play.playerPage")}</CardTitle>
        <CardDescription>
          {t("play.posterNote")}
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3 sm:grid-cols-2">
        <div className="grid gap-1.5">
          <Label htmlFor="playout-title">{t("play.metaTitle")}</Label>
          <Input
            id="playout-title"
            value={model.draft.title}
            maxLength={200}
            placeholder={t("play.untitled")}
            onChange={(e) => model.setDraft({ ...model.draft, title: e.target.value })}
          />
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor="playout-description">{t("play.metaDescription")}</Label>
          <Textarea
            id="playout-description"
            value={model.draft.description}
            maxLength={2000}
            rows={3}
            onChange={(e) => model.setDraft({ ...model.draft, description: e.target.value })}
          />
        </div>
        <div className="flex flex-wrap items-center gap-2 sm:col-span-2">
          <Button
            size="sm"
            disabled={!model.dirty || model.state === "saving"}
            onClick={() => void model.save()}
          >
            {t("common.save")}
          </Button>
          {model.dirty && (
            <Button size="sm" variant="ghost" onClick={model.discard}>
              {t("common.discard")}
            </Button>
          )}
          <SaveStatus state={model.state} />
        </div>
      </CardContent>
    </Card>
  );
}
