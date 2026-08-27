import { useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { ConfirmDestructive } from "@/components/ConfirmDestructive";
import { useConfirm } from "@/hooks/useConfirm";
import {
  Check,
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
import type {
  PlayoutAdminView,
  PlayoutProtection,
  PlayoutSettings,
  PlayoutVariant,
  RenditionView,
  Settings,
} from "@/lib/types";
import { useT } from "@/lib/i18n";

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
  const [busy, setBusy] = useState(false);

  // Draft copies. Publishing fields are free text, so they are edited locally
  // and saved on blur or on a button rather than on every keystroke.
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");

  const refresh = useCallback(async () => {
    try {
      const [v, s, r] = await Promise.all([
        api.playout(),
        api.getSettings(),
        api.listRenditions().catch(() => [] as RenditionView[]),
      ]);
      setView(v);
      setSettings(s);
      setRenditions(r);
      setTitle(v.title);
      setDescription(v.description);
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
  const savePlayoutSettings = useCallback(
    async (next: PlayoutSettings) => {
      if (!settings) return;
      setBusy(true);
      try {
        const saved = await api.putSettings({ ...settings, playout: next });
        setSettings(saved);
        setView(await api.playout());
      } catch (err) {
        toast.error(err instanceof Error ? err.message : "Could not save");
        // Re-read rather than leave the form showing a value the server
        // rejected, which would look saved.
        void refresh();
      } finally {
        setBusy(false);
      }
    },
    [settings, refresh],
  );

  const savePublish = useCallback(
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
        toast.error(err instanceof Error ? err.message : "Could not save");
      } finally {
        setBusy(false);
      }
    },
    [],
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
            <span className="text-[11px] text-muted-foreground">
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

      <ExposureBanner view={view} />

      {!view.running && play.enabled && (
        <Card className="mb-3 border-warn/40">
          <CardContent className="flex items-center gap-2 py-3 text-[12px] text-muted-foreground">
            <ShieldAlert className="h-4 w-4 shrink-0 text-warn" />
            Playout is enabled but the packager is not running on this server.
            Nothing is being published.
          </CardContent>
        </Card>
      )}

      <div className="grid gap-3 lg:grid-cols-2">
        <ShareCard view={view} play={play} busy={busy} onRotate={() => void rotate()} />

        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-[13px]">
              <Eye className="h-3.5 w-3.5" />
              Audience
            </CardTitle>
            <CardDescription>
              A viewer is counted while they keep pulling segments.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
              <Stat labelKey="play.watchingNow" value={String(analytics.viewers)} />
              <Stat labelKey="play.peak" value={String(analytics.peak)} />
              <Stat labelKey="play.sessions" value={String(analytics.sessions)} />
              <Stat labelKey="play.onDisk" value={fmtBytes(usage.bytes)} />
            </div>
            {analytics.uncounted > 0 && (
              <p className="mt-3 text-[11px] text-muted-foreground">
                {analytics.uncounted} viewers arrived with the session table full.
                They are being served normally; only the count is short. Raise the
                session cap below to measure them.
              </p>
            )}
            {usage.overLimit && (
              <p className="mt-3 flex items-start gap-1.5 text-[11px] text-down">
                <ShieldAlert className="mt-0.5 h-3 w-3 shrink-0" />
                The disk cap is below one playlist window. Raise it or lower the
                bitrate — segments viewers are mid-playback on cannot be deleted.
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
                Reset counters
              </Button>
            </div>
          </CardContent>
        </Card>
      </div>

      <ProtectionCard
        view={view}
        play={play}
        busy={busy}
        onSave={savePublish}
        onSaveSettings={savePlayoutSettings}
      />

      <Card className="mt-3">
        <CardHeader>
          <CardTitle className="text-[13px]">{t("play.playerPage")}</CardTitle>
          <CardDescription>
            {t("play.posterNote")}
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-3 sm:grid-cols-2">
          <div className="grid gap-1.5">
            <Label htmlFor="playout-title">{t("play.metaTitle")}</Label>
            <Input
              id="playout-title"
              value={title}
              maxLength={200}
              placeholder={t("play.untitled")}
              onChange={(e) => setTitle(e.target.value)}
              onBlur={() => {
                if (title !== view.title) void savePublish({ title });
              }}
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="playout-description">{t("play.metaDescription")}</Label>
            <Textarea
              id="playout-description"
              value={description}
              maxLength={2000}
              rows={3}
              onChange={(e) => setDescription(e.target.value)}
              onBlur={() => {
                if (description !== view.description) void savePublish({ description });
              }}
            />
          </div>
        </CardContent>
      </Card>

      <VariantsCard
        play={play}
        variants={variants}
        renditions={renditions}
        busy={busy}
        onSave={savePlayoutSettings}
      />

      <PackagingCard play={play} busy={busy} onSave={savePlayoutSettings} />
    </div>
  );
}

// ---------------------------------------------------------------- exposure

/** The one thing on this page that must never be subtle.
 *
 *  Silently exposing somebody's stream would be a serious bug, so the state is
 *  stated in words at the top of the page rather than inferred from a toggle
 *  three cards down. */
function ExposureBanner({ view }: { view: PlayoutAdminView }) {
  const t = useT();
  if (!view.status.enabled) {
    return (
      <div className="mb-3 flex items-center gap-2 rounded-md border border-border bg-background px-3 py-2 text-[12px] text-muted-foreground">
        <Lock className="h-3.5 w-3.5 shrink-0" />
        Playout is off. Nothing is being served to viewers.
      </div>
    );
  }
  if (!view.status.public) {
    return (
      <div className="mb-3 flex items-center gap-2 rounded-md border border-border bg-background px-3 py-2 text-[12px]">
        <Lock className="h-3.5 w-3.5 shrink-0 text-live" />
        <span>
          <span className="font-medium">{t("play.private")}</span>{" "}
          <span className="text-muted-foreground">
            Only a signed-in administrator can watch. Turn on “Public” below to
            share a link.
          </span>
        </span>
      </div>
    );
  }
  if (view.exposed) {
    return (
      <div className="mb-3 flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-[12px]">
        <Globe className="mt-0.5 h-3.5 w-3.5 shrink-0 text-destructive" />
        <span>
          <span className="font-medium text-destructive">
            This stream is public to the internet.
          </span>{" "}
          <span className="text-muted-foreground">
            Anyone who reaches this server can watch without a password or a
            link they had to be given. Switch protection back to “Playback link”
            below to close it.
          </span>
        </span>
      </div>
    );
  }
  return (
    <div className="mb-3 flex items-start gap-2 rounded-md border border-warn/40 bg-warn/10 px-3 py-2 text-[12px]">
      <KeyRound className="mt-0.5 h-3.5 w-3.5 shrink-0 text-warn" />
      <span>
        <span className="font-medium">{t("play.unlisted")}</span>{" "}
        <span className="text-muted-foreground">
          Anyone holding the playback link can watch, without signing in. The
          link is the password — treat it like one.
        </span>
      </span>
    </div>
  );
}

// ------------------------------------------------------------------- share

function CopyField({
  label,
  value,
  hint,
  mono = true,
}: {
  label: string;
  value: string;
  hint?: string;
  mono?: boolean;
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
          onFocus={(e) => e.currentTarget.select()}
          className={cn("text-[11px]", mono && "font-mono")}
        />
        <Button
          variant="secondary"
          size="icon-sm"
          aria-label={`Copy ${label}`}
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
      {hint && <p className="text-[11px] text-muted-foreground">{hint}</p>}
    </div>
  );
}

function ShareCard({
  view,
  play,
  busy,
  onRotate,
}: {
  view: PlayoutAdminView;
  play: PlayoutSettings;
  busy: boolean;
  onRotate: () => void;
}) {
  const t = useT();
  const protectedLink = play.public && view.protection === "token";
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-[13px]">{t("play.share")}</CardTitle>
        <CardDescription>
          {play.public
            ? t("play.linksPublic") : t("play.linksProtected")}
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3">
        <CopyField
          label={t("play.playerPage")}
          value={view.urls.watch}
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
        />
        <div className="grid gap-1.5">
          <Label htmlFor="playout-embed">{t("play.embedCode")}</Label>
          <Textarea
            id="playout-embed"
            readOnly
            rows={3}
            value={view.urls.embed}
            onFocus={(e) => e.currentTarget.select()}
            className="font-mono text-[11px]"
          />
          {!play.allowCrossOrigin && (
            <p className="text-[11px] text-warn">
              Embedding on another site also needs “Allow cross-origin” on. Without
              it the player only works on pages served from this server.
            </p>
          )}
        </div>
        {protectedLink && (
          <div>
            <Button variant="ghost" size="sm" disabled={busy} onClick={onRotate}>
              <KeyRound />
              Generate a new link
            </Button>
            <p className="mt-1 text-[11px] text-muted-foreground">
              Immediately stops every link already shared. This is how you revoke
              access.
            </p>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

// -------------------------------------------------------------- protection

function ProtectionCard({
  view,
  play,
  busy,
  onSave,
  onSaveSettings,
}: {
  view: PlayoutAdminView;
  play: PlayoutSettings;
  busy: boolean;
  onSave: (body: { protection?: PlayoutProtection }) => Promise<void>;
  onSaveSettings: (next: PlayoutSettings) => Promise<void>;
}) {
  const t = useT();
  return (
    <Card className="mt-3">
      <CardHeader>
        <CardTitle className="text-[13px]">{t("play.whoCanWatch")}</CardTitle>
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
            onValueChange={(v) => void onSave({ protection: v as PlayoutProtection })}
          >
            <SelectTrigger id="playout-protection" className="max-w-sm">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="token">
                Playback link — a secret in the URL, or an HTTP basic password
              </SelectItem>
              <SelectItem value="open">
                Open — anyone who can reach this server
              </SelectItem>
            </SelectContent>
          </Select>
          <p className="text-[11px] text-muted-foreground">
            {view.protection === "token"
              ? t("play.basicPasswordNote") : t("play.noCredentialNote")}
          </p>
        </div>

        <div className="flex items-start justify-between gap-3 rounded-md border border-border p-2.5">
          <div>
            <p className="text-[12px] font-medium">{t("play.public")}</p>
            <p className="text-[11px] text-muted-foreground">
              Serve viewers who are not signed in. Off means administrators only,
              whatever the protection setting says.
            </p>
          </div>
          <Switch
            checked={play.public}
            disabled={busy}
            aria-label={t("play.serveWithoutSession")}
            onCheckedChange={(pub) => void onSaveSettings({ ...play, public: pub })}
          />
        </div>

        <div className="flex items-start justify-between gap-3 rounded-md border border-border p-2.5">
          <div>
            <p className="text-[12px] font-medium">{t("play.allowCrossOrigin")}</p>
            <p className="text-[11px] text-muted-foreground">
              Lets a player embedded on another website fetch the media, and lets
              this server's player page be framed. Not needed for same-site
              embedding.
            </p>
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
}: {
  play: PlayoutSettings;
  variants: PlayoutAdminView["status"]["variants"];
  renditions: RenditionView[];
  busy: boolean;
  onSave: (next: PlayoutSettings) => Promise<void>;
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
        <CardTitle className="flex items-center gap-2 text-[13px]">
          <Radio className="h-3.5 w-3.5" />
          Ladder
        </CardTitle>
        <CardDescription>
            {t("play.rungNote")}
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-2">
        {play.variants.length === 0 && (
          <p className="text-[12px] text-muted-foreground">
            {t("play.noRungs")}
          </p>
        )}

        {play.variants.map((v, i) => {
          const st = statusByName.get(v.name);
          return (
            <div
              key={v.name}
              className="grid items-end gap-2 rounded-md border border-border p-2.5 sm:grid-cols-[auto_1fr_1fr_auto_auto]"
            >
              <div className="flex items-center gap-2 pb-2">
                <StatusDot tone={st?.running ? "live" : v.enabled ? "warn" : "idle"} />
                <Switch
                  checked={v.enabled}
                  disabled={busy}
                  aria-label={`Enable ${v.name}`}
                  onCheckedChange={(enabled) => update(i, { enabled })}
                />
              </div>

              <div className="grid gap-1">
                <Label className="text-[10px]">{t("play.name")}</Label>
                {/* Uncontrolled, and committed on blur rather than on every
                    keystroke: a rename is a directory move and a muxer
                    restart, so saving mid-word would cycle the stream once per
                    character. Keyed by the name so a save re-seeds the field. */}
                <Input
                  key={v.name}
                  defaultValue={v.name}
                  maxLength={32}
                  className="text-[12px]"
                  onBlur={(e) => {
                    const name = e.currentTarget.value.trim();
                    if (name && name !== v.name) update(i, { name });
                    else e.currentTarget.value = v.name;
                  }}
                />
              </div>

              <div className="grid gap-1">
                <Label className="text-[10px]">{t("play.videoSource")}</Label>
                <Select
                  value={v.renditionId == null ? "source" : String(v.renditionId)}
                  disabled={busy}
                  onValueChange={(val) =>
                    update(i, { renditionId: val === "source" ? null : Number(val) })
                  }
                >
                  <SelectTrigger className="text-[12px]">
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
                <Label className="text-[10px]">{t("play.audioTrack")}</Label>
                {/* Committed on blur for the same reason the name is: each
                    save restarts this rung's muxer. */}
                <Input
                  key={`${v.name}-track-${v.audioTrack}`}
                  type="number"
                  min={0}
                  max={63}
                  defaultValue={v.audioTrack}
                  className="w-20 text-[12px]"
                  onBlur={(e) => {
                    const n = Number(e.currentTarget.value);
                    if (Number.isFinite(n) && n >= 0 && n !== v.audioTrack) {
                      update(i, { audioTrack: n });
                    } else {
                      e.currentTarget.value = String(v.audioTrack);
                    }
                  }}
                />
              </div>

              <div className="flex items-center gap-1 pb-1">
                {st?.running && st.bandwidth > 0 && (
                  <Badge variant="outline" className="font-mono text-[10px]">
                    {Math.round(st.bandwidth / 1000)}k
                    {st.height ? ` · ${st.height}p` : ""}
                  </Badge>
                )}
                {st?.viewers ? (
                  <Badge variant="outline" className="text-[10px]">
                    {st.viewers} watching
                  </Badge>
                ) : null}
                <Button
                  variant="ghost"
                  size="icon-sm"
                  disabled={busy}
                  aria-label={`Remove ${v.name}`}
                  onClick={() =>
                    confirmRemove.ask({ index: i, name: v.name, viewers: st?.viewers ?? 0 })
                  }
                >
                  <Trash2 />
                </Button>
              </div>

              {st?.error && (
                <p className="text-[11px] text-down sm:col-span-5">{st.error}</p>
              )}
            </div>
          );
        })}

        <div>
          <Button
            variant="secondary"
            size="sm"
            disabled={busy || play.variants.length >= MAX_VARIANTS}
            onClick={add}
          >
            <Plus />
            Add a rung
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

// --------------------------------------------------------------- packaging

function NumberField({
  id,
  label,
  hint,
  value,
  min,
  max,
  disabled,
  onCommit,
}: {
  id: string;
  label: string;
  hint?: string;
  value: number;
  min: number;
  max: number;
  disabled: boolean;
  onCommit: (n: number) => void;
}) {
  const [draft, setDraft] = useState(String(value));
  useEffect(() => setDraft(String(value)), [value]);
  return (
    <div className="grid gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      <Input
        id={id}
        type="number"
        min={min}
        max={max}
        value={draft}
        disabled={disabled}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={() => {
          const n = Number(draft);
          if (Number.isFinite(n) && n !== value) onCommit(n);
          else setDraft(String(value));
        }}
      />
      {hint && <p className="text-[11px] text-muted-foreground">{hint}</p>}
    </div>
  );
}

function PackagingCard({
  play,
  busy,
  onSave,
}: {
  play: PlayoutSettings;
  busy: boolean;
  onSave: (next: PlayoutSettings) => Promise<void>;
}) {
  const t = useT();
  const set = (patch: Partial<PlayoutSettings>) => void onSave({ ...play, ...patch });

  return (
    <Card className="mt-3">
      <CardHeader>
        <CardTitle className="text-[13px]">{t("play.packaging")}</CardTitle>
        <CardDescription>
            {t("play.restartNote")}
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <div className="grid gap-1.5">
          <Label htmlFor="playout-format">{t("play.format")}</Label>
          <Select
            value={play.format}
            disabled={busy}
            onValueChange={(v) => set({ format: v === "hls+dash" ? "hls+dash" : "hls" })}
          >
            <SelectTrigger id="playout-format">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="hls">{t("play.hls")}</SelectItem>
              <SelectItem value="hls+dash">{t("play.hlsDash")}</SelectItem>
            </SelectContent>
          </Select>
          <p className="text-[11px] text-muted-foreground">
            DASH is muxed by the same process from the same copied video — no
            second encode.
          </p>
        </div>

        <NumberField
          id="playout-segment"
          label={t("play.segmentLength")}
          hint={t("play.shorterIsLowerLatencyAnd")}
          value={play.segmentSeconds}
          min={1}
          max={30}
          disabled={busy}
          onCommit={(segmentSeconds) => set({ segmentSeconds })}
        />
        <NumberField
          id="playout-window"
          label={t("play.liveWindow")}
          hint={t("play.threeIsTheMinimumA")}
          value={play.playlistSegments}
          min={3}
          max={100}
          disabled={busy}
          onCommit={(playlistSegments) => set({ playlistSegments })}
        />
        <NumberField
          id="playout-dvr"
          label={t("play.dvrWindow")}
          hint="0 is live only. Anything longer lets viewers seek back."
          value={play.dvrWindowSeconds}
          min={0}
          max={43200}
          disabled={busy}
          onCommit={(dvrWindowSeconds) => set({ dvrWindowSeconds })}
        />
        <NumberField
          id="playout-disk"
          label={t("play.diskCap")}
          hint={t("play.acrossEveryRungTheBackstop")}
          value={play.maxDiskMb}
          min={64}
          max={1048576}
          disabled={busy}
          onCommit={(maxDiskMb) => set({ maxDiskMb })}
        />
        <NumberField
          id="playout-audio"
          label={t("play.audioBitrate")}
          hint={t("play.oneStereoAacTrackPer")}
          value={play.audioKbps}
          min={32}
          max={512}
          disabled={busy}
          onCommit={(audioKbps) => set({ audioKbps })}
        />
        <NumberField
          id="playout-idle"
          label={t("play.idleTimeout")}
          hint={t("play.mustExceedOneSegmentOr")}
          value={play.sessionIdleSeconds}
          min={5}
          max={3600}
          disabled={busy}
          onCommit={(sessionIdleSeconds) => set({ sessionIdleSeconds })}
        />
        <NumberField
          id="playout-sessions"
          label={t("play.sessionCap")}
          hint={t("play.boundsTheViewerTableViewers")}
          value={play.maxSessions}
          min={1}
          max={1000000}
          disabled={busy}
          onCommit={(maxSessions) => set({ maxSessions })}
        />
      </CardContent>
    </Card>
  );
}
