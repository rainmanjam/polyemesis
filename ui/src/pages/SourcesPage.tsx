import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import {
  Activity,
  AlertTriangle,
  Copy,
  Info,
  KeyRound,
  Loader2,
  Plus,
  RadioTower,
  ShieldCheck,
  Trash2,
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
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { PageHeader } from "@/components/AppLayout";
import { NoProgrammeYet } from "@/components/NoProgrammeYet";
import { SecretInput } from "@/components/SecretInput";
import { ConfirmDestructive } from "@/components/ConfirmDestructive";
import { api } from "@/lib/api";
import { useT, type Translator, type TranslationKey } from "@/lib/i18n";
import { InfoHint } from "@/components/InfoHint";
import { LIMITS } from "@/lib/limits";
import { cn } from "@/lib/utils";
import type { Source, SourceView } from "@/lib/types";

/* ===========================================================================
   Sources: one ingested programme each.

   This page exists because a horizontal and a vertical feed out of OBS's
   vertical-canvas plugin are two different compositions, not one cropped from
   the other. Before it, the answer to "I stream both" was "run two containers".

   The honesty problem this page has to solve: every source has a publish
   token, and "token" reads like authentication. It is not, yet. Sources are
   separated by PORT, because the listener is FFmpeg's and an FFmpeg SRT
   listener takes one caller per port and never reads streamid. What actually
   gates an ingest today is the RTMP stream key or the SRT passphrase. The
   server sends tokenEnforced so this page can say that out loud rather than
   let an operator believe rotating a token protected something.
   =========================================================================== */

function copy(t: Translator, text: string, what: string) {
  void navigator.clipboard
    ?.writeText(text)
    .then(() => toast.success(t("sources.copied", { what })))
    .catch(() => toast.error(t("sources.copyFailed", { what: what.toLowerCase() })));
}

export function SourcesPage() {
  const t = useT();
  const [sources, setSources] = useState<SourceView[]>([]);
  const [loading, setLoading] = useState(true);
  const [creating, setCreating] = useState(false);
  const [newName, setNewName] = useState("");
  const [deleting, setDeleting] = useState<SourceView | null>(null);
  const [rotating, setRotating] = useState<SourceView | null>(null);
  const [busyId, setBusyId] = useState<number | null>(null);

  const load = useCallback(async () => {
    try {
      setSources(await api.listSources());
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t("sources.loadFailed"));
    } finally {
      setLoading(false);
    }
    // `t` changes identity on a language switch, so this reloads once when the
    // operator changes language. Harmless — it is the same request — and the
    // alternative is a stale closure that reports a failure in the previous
    // language.
  }, [t]);

  useEffect(() => {
    void load();
  }, [load]);

  const create = async () => {
    const name = newName.trim();
    if (!name) return;
    try {
      // Name only: the server fills in a default ingest, so adding a programme
      // does not require choosing ports before you have one to configure.
      await api.createSource({ name });
      setCreating(false);
      setNewName("");
      toast.success(t("sources.added", { name }));
      await load();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t("sources.addFailed"));
    }
  };

  const patch = async (s: SourceView, changes: Partial<Source>) => {
    setBusyId(s.id);
    try {
      // Only the stored fields. Spreading the whole SourceView would send
      // publishUrls, isDefault, tokenEnforced and running back, and the server
      // rejects unknown fields — so the save 400s and the operator watches
      // their edit silently fail to stick.
      const body: Partial<Source> = {
        name: s.name,
        enabled: s.enabled,
        ingest: s.ingest,
        token: s.token,
        position: s.position,
        ...changes,
      };
      await api.updateSource(s.id, body);
      await load();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t("sources.saveFailed"));
    } finally {
      setBusyId(null);
    }
  };

  const rotate = async (s: SourceView) => {
    setBusyId(s.id);
    try {
      await api.rotateSourceToken(s.id);
      toast.success(t("sources.tokenIssued"));
      await load();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t("sources.rotateFailed"));
    } finally {
      setBusyId(null);
    }
  };

  const remove = async () => {
    if (!deleting) return;
    // WAS it the only one, not IS it — asked of the list this render was drawn
    // from, which still holds the row about to go. Safe wherever it sits in
    // this function because load() installs new state rather than rewriting
    // the array this closure captured; anyone replacing it with a fresh count
    // from the server after the delete gets the opposite answer.
    //
    // The dialog spends a whole sentence saying that deleting the last source
    // is a different act. The toast is what is left on screen once it is true,
    // and saying the ordinary thing there takes that back.
    const wasOnly = sources.length === 1;
    try {
      await api.deleteSource(deleting.id);
      toast.success(t(wasOnly ? "sources.deletedLast" : "sources.deleted", { name: deleting.name }));
      setDeleting(null);
      await load();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t("sources.deleteFailed"));
    }
  };

  return (
    <div className="p-3">
      <PageHeader
        title={t("sources.title")}
        subtitle={t("sources.subtitle")}
        actions={
          <Button
            size="sm"
            // The onboarding tour's anchor for "install.sh printed a publish
            // address once and that terminal is gone". It is THIS button and
            // not a source card, because a first-run install has no sources at
            // all — nothing is seeded — so the card is the one thing on this
            // page that is guaranteed absent when the tour matters most.
            // See ui/src/lib/tourSteps.ts.
            data-tour="add-source"
            onClick={() => setCreating(true)}
          >
            <Plus className="h-3.5 w-3.5" /> {t("sources.addSource")}
          </Button>
        }
      />

      {loading ? (
        <div className="flex items-center gap-2 py-6 text-[12px] text-muted-foreground">
          <Loader2 className="h-3.5 w-3.5 animate-spin" /> {t("sources.loading")}
        </div>
      ) : sources.length === 0 ? (
        // THE END OF THE TRAIL. Every refusal on every other screen sends the
        // operator here, so a blank page under a heading is the worst possible
        // thing for this one to be -- it reads as "the thing you were sent to
        // do has already been done, or is broken".
        //
        // Its own button rather than the shared link, because this page IS the
        // Sources page: a link back to itself would be a loop, and the control
        // that ends the state is the create dialog three lines below.
        <NoProgrammeYet
          title={t("empty.sourcesTitle")}
          body={t("empty.sourcesBody")}
          action={
            <Button size="sm" onClick={() => setCreating(true)}>
              <Plus className="h-3.5 w-3.5" /> {t("sources.addSource")}
            </Button>
          }
        />
      ) : (
        <div className="flex flex-col gap-3">
          {sources.map((s) => (
            <SourceCard
              key={s.id}
              source={s}
              busy={busyId === s.id}
              onPatch={(c) => patch(s, c)}
              onRotate={() => setRotating(s)}
              onDelete={() => setDeleting(s)}
            />
          ))}
        </div>
      )}

      <Dialog open={creating} onOpenChange={setCreating}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("sources.addTitle")}</DialogTitle>
            <DialogDescription>{t("sources.addDescription")}</DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="src-name">{t("sources.name")}</Label>
            <Input
              id="src-name"
              value={newName}
              placeholder={t("sources.namePlaceholder")}
              onChange={(e) => setNewName(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && void create()}
            />
          </div>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setCreating(false)}>
              {t("common.cancel")}
            </Button>
            <Button onClick={() => void create()} disabled={!newName.trim()}>
              {t("common.add")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <ConfirmDestructive
        open={rotating !== null}
        onOpenChange={(o) => !o && setRotating(null)}
        subject={rotating?.name ?? ""}
        title={t("sources.rotateTitle", { name: rotating?.name ?? "" })}
        description={
          rotating?.publishing
            ? t("sources.rotateWhilePublishing")
            : t("sources.rotateIdle")
        }
        confirmLabel={t("sources.rotateConfirm")}
        onConfirm={async () => {
          if (rotating) await rotate(rotating);
        }}
      />

      <ConfirmDestructive
        open={deleting !== null}
        onOpenChange={(o) => !o && setDeleting(null)}
        subject={deleting?.name ?? ""}
        title={t("sources.deleteTitle", { name: deleting?.name ?? "" })}
        description={
          // The last source gets a second sentence, because deleting it is a
          // different act: the cascade is the same, but what the install is
          // left with is not. The store refused this delete until now, so
          // nothing on this screen had ever had to describe it.
          sources.length === 1
            ? `${t("sources.deleteDescription")} ${t("sources.deleteLastDescription")}`
            : t("sources.deleteDescription")
        }
        requireTyping
        consequences={[
          { label: t("sources.destinations"), count: deleting?.destinations ?? 0 },
          { label: t("sources.renditions"), count: deleting?.renditions ?? 0 },
        ]}
        confirmLabel={t("sources.deleteConfirm")}
        onConfirm={remove}
      />
    </div>
  );
}

function SourceCard({
  source,
  busy,
  onPatch,
  onRotate,
  onDelete,
}: {
  source: SourceView;
  busy: boolean;
  onPatch: (changes: Partial<Source>) => void;
  onRotate: () => void;
  onDelete: () => void;
}) {
  const t = useT();
  // A LOCAL DRAFT, not a per-field commit.
  //
  // These fields used to write straight through on blur, and every write
  // restarts this source's ingest. That turned tabbing out of a port field into
  // a dropped broadcast -- an outage as the side effect of a keystroke nobody
  // meant as a decision. Edits now accumulate here and go nowhere until Apply.
  const [draft, setDraft] = useState(source.ingest);
  useEffect(() => setDraft(source.ingest), [source.ingest]);

  const ing = draft;
  const setIngest = (changes: Partial<Source["ingest"]>) =>
    setDraft((d) => ({ ...d, ...changes }));
  const dirty = JSON.stringify(draft) !== JSON.stringify(source.ingest);

  return (
    <Card className={source.running ? undefined : "border-warn/40"}>
      <CardHeader className="flex-row items-start justify-between gap-2">
        <div className="min-w-0">
          <CardTitle className="flex items-center gap-2">
            <RadioTower className="h-3.5 w-3.5 shrink-0" />
            <span className="truncate">{source.name}</span>
            {source.isDefault && <Badge variant="outline">{t("sources.default")}</Badge>}
            <Badge variant={source.running ? "live" : "warn"}>
              {source.running ? t("sources.running") : t("sources.notRunning")}
            </Badge>
            {/* Beside the running badge rather than replacing it, because both
                are true at once: the source IS running, and it is reachable on
                only some of the addresses it was asked to listen on. Showing
                one without the other is how a half-bound listener looked
                perfectly healthy for as long as it did. */}
            {source.listenerHealth?.state === "degraded" && (
              <Badge variant="warn">{t("sources.listenerDegraded")}</Badge>
            )}
            {/* Beside the running badge for the same reason: this source IS
                running, and it is pulling from a file nothing ever read. */}
            {source.pullUploadUnchecked && (
              <Badge variant="warn">{t("sources.pullUnchecked")}</Badge>
            )}
          </CardTitle>
          <CardDescription>
            {source.isDefault
              ? t("sources.defaultDescription")
              : t("sources.ownDescription")}
          </CardDescription>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {busy && <Loader2 className="h-3.5 w-3.5 animate-spin text-muted-foreground" />}
          <Switch
            checked={source.enabled}
            onCheckedChange={(v) => onPatch({ enabled: v })}
            aria-label={t("sources.enableAria", { name: source.name })}
          />
          {/* Enabled on the only source too. The store used to refuse that
              delete, so a disabled button was the honest thing to show; it now
              accepts it, and an install with no source is a state the whole
              product answers in rather than one it falls over in. The weight
              moved to the confirmation, which says what the install is left
              with. */}
          <Button
            size="icon"
            variant="ghost"
            onClick={onDelete}
            title={t("sources.deleteThis")}
            aria-label={t("sources.deleteAria", { name: source.name })}
          >
            <Trash2 className="h-3.5 w-3.5" />
          </Button>
        </div>
      </CardHeader>

      <CardContent className="flex flex-col gap-3">
        {!source.running && (
          <p className="flex items-start gap-1.5 text-[11px] text-warn">
            <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
            {t("sources.noEngine")}
          </p>
        )}

        {/* The detail, not just the badge. "Degraded" on its own sends an
            operator to the logs; the server already knows which address failed
            and why, and that sentence is the whole difference between noticing
            and fixing. */}
        {source.listenerHealth?.state === "degraded" && source.listenerHealth.detail && (
          <p className="flex items-start gap-1.5 text-[11px] text-warn">
            <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
            <span>
              <strong className="font-semibold">{t("sources.listenerDegraded")}</strong>{" "}
              {source.listenerHealth.detail}
            </span>
          </p>
        )}

        {/* The server's sentence, not a badge on its own. It names the FILE and
            says why nothing read it, and those two facts are the whole
            difference between "something is wrong here" and knowing which
            upload to send again. Rendered verbatim for the same reason
            listenerHealth.detail is: the server is the only thing that knows,
            and a second copy of the sentence in the UI would drift from it. */}
        {source.pullUploadUnchecked && (
          <p className="flex items-start gap-1.5 text-[11px] text-warn" data-testid="pull-unchecked">
            <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
            <span>
              <strong className="font-semibold">{t("sources.pullUnchecked")}</strong>{" "}
              {source.pullUploadUnchecked}
            </span>
          </p>
        )}

        <div className="grid gap-2 sm:grid-cols-4">
          <div className="flex flex-col gap-1">
            <Label className="flex items-center gap-1">
              {t("sources.ingest")}
              <InfoHint body="sources.help.ingest" title="sources.ingest" />
            </Label>
            <Select value={ing.mode} onValueChange={(v) => setIngest({ mode: v as typeof ing.mode })}>
              <SelectTrigger
                className="h-7 text-[11px]"
                aria-label={t("sources.ingest")}
                data-testid="ingest-mode"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="srt">SRT</SelectItem>
                <SelectItem value="rtmp">RTMP</SelectItem>
                <SelectItem value="pull">{t("sources.modePull")}</SelectItem>
              </SelectContent>
            </Select>
          </div>

          {ing.mode === "srt" && (
            <>
              <NumberField
                label={t("sources.latencyMs")}
                hint="sources.help.latencyMs"
                value={ing.srt.latencyMs}
                min={LIMITS.srtLatencyMs.min}
                max={LIMITS.srtLatencyMs.max}
                onChange={(n) => setIngest({ srt: { ...ing.srt, latencyMs: n } })}
              />
              <div className="flex flex-col gap-1">
                <Label className="flex items-center gap-1">
                  {t("sources.passphrase")}
                  <InfoHint body="sources.help.passphrase" title="sources.passphrase" />
                </Label>
                <Input
                  className="h-7 text-[11px]"
                  type="password"
                  value={ing.srt.passphrase}
                  placeholder={t("sources.passphrasePlaceholder")}
                  onChange={(e) => setIngest({ srt: { ...ing.srt, passphrase: e.target.value } })}
                />
              </div>
            </>
          )}

          {ing.mode === "rtmp" && (
            <>
              <div className="flex flex-col gap-1">
                <Label className="flex items-center gap-1">
                  {t("sources.app")}
                  <InfoHint body="sources.help.app" title="sources.app" />
                </Label>
                <Input
                  className="h-7 text-[11px]"
                  value={ing.rtmp.app}
                  onChange={(e) => setIngest({ rtmp: { ...ing.rtmp, app: e.target.value } })}
                />
              </div>
              <div className="flex flex-col gap-1">
                <Label className="flex items-center gap-1">
                  {t("sources.streamKey")}
                  <InfoHint body="sources.help.streamKey" title="sources.streamKey" />
                </Label>
                <SecretInput
                  className="h-7 text-[11px]"
                  value={ing.rtmp.streamKey}
                  onChange={(e) => setIngest({ rtmp: { ...ing.rtmp, streamKey: e.target.value } })}
                />
              </div>
            </>
          )}
        </div>

        {dirty && (
          /* The consequence, at the moment of decision. Applying reconciles
             this source's ingest, which restarts its FFmpeg child; if an
             encoder is connected that is a dropped broadcast. Stated on the
             button rather than in a dialog, because a label is read and a
             dialog is dismissed. */
          <div className="flex flex-wrap items-center gap-2 rounded-md border border-warn/40 bg-warn-dim/20 px-2 py-1.5">
            <AlertTriangle className="h-3.5 w-3.5 shrink-0 text-warn" />
            <span className="min-w-0 flex-1 text-[11px]">
              {source.publishing ? t("sources.dirtyPublishing") : t("sources.dirtyIdle")}
            </span>
            <Button size="sm" variant="ghost" onClick={() => setDraft(source.ingest)} disabled={busy}>
              {t("common.discard")}
            </Button>
            <Button
              size="sm"
              variant={source.publishing ? "destructive" : "default"}
              onClick={() => onPatch({ ingest: draft })}
              disabled={busy}
            >
              {source.publishing ? t("sources.applyAndDrop") : t("common.apply")}
            </Button>
          </div>
        )}

        {/* What to paste into the encoder.

            The wrapper is the onboarding tour's CONDITIONAL step, and the only
            one it has. It exists only once the operator has a source, which on
            a first run they do not — nothing is seeded — so the tour skips it
            and the step before it, on the Add source button, carries the
            teaching instead. On a configured install, which is what a replay
            from Settings usually is, the step shows.

            It deliberately spans the URLs AND the token block below rather than
            the URLs alone: publishUrls is EMPTY while ingest.mode is unset —
            see publishURLs() in internal/api/sources.go — so a wrapper around
            the map alone would collapse to zero height on a source whose
            protocol has not been chosen, and the tour would highlight a
            hairline. The token row always renders, so this span always has
            something in it. It is also the honest boundary for the step, which
            is about the address and the credential together.

            See ui/src/lib/tourSteps.ts. */}
        <div data-tour="source-publish-urls" className="flex flex-col gap-3">
        {Object.entries(source.publishUrls).map(([proto, url]) =>
          url ? (
            <div key={proto} className="flex items-center gap-2">
              <span className="w-20 shrink-0 text-[10px] uppercase tracking-wider text-subtle-foreground">
                {proto}
              </span>
              <code className="min-w-0 flex-1 truncate rounded bg-muted px-1.5 py-1 font-mono text-[10px]">
                {url}
              </code>
              <Button
                size="icon"
                variant="ghost"
                onClick={() => copy(t, url, proto.toUpperCase())}
                aria-label={t("sources.copyUrlAria", { proto })}
              >
                <Copy className="h-3 w-3" />
              </Button>
            </div>
          ) : null,
        )}

        {source.link && (
          /* Per-source uplink health. With several programmes on one install,
             "why is it breaking up" is a question about one encoder's uplink,
             and answering it per programme is something Restreamer's UI does
             not do. */
          <div className="flex flex-wrap items-center gap-3 rounded-md border border-live/30 bg-live-dim/20 px-2 py-1.5">
            <span className="flex items-center gap-1.5 text-[10px] font-semibold uppercase tracking-wider text-live">
              <Activity className="h-3 w-3" /> {t("sources.publishing")}
            </span>
            <span className="font-mono text-[10px] text-muted-foreground">{source.link.peer}</span>
            <span className="tnum font-mono text-[10px]">{t("sources.rtt")} {source.link.rttMs.toFixed(1)} ms</span>
            <span className="tnum font-mono text-[10px]">{t("sources.loss")} {source.link.lossPackets}</span>
            <span className="tnum font-mono text-[10px]">{t("sources.retrans")} {source.link.retransPackets}</span>
          </div>
        )}

        {/* The token, and the truth about it. */}
        <div className="flex flex-col gap-1.5 border-t border-border pt-2">
          <div className="flex items-center gap-2">
            <span className="w-20 shrink-0 text-[10px] uppercase tracking-wider text-subtle-foreground">
              {t("sources.token")}
            </span>
            <code className="min-w-0 flex-1 truncate rounded bg-muted px-1.5 py-1 font-mono text-[10px]">
              {source.token}
            </code>
            <Button
              size="icon"
              variant="ghost"
              onClick={() => copy(t, source.token, t("sources.token"))}
              aria-label={t("sources.copyTokenAria")}
            >
              <Copy className="h-3 w-3" />
            </Button>
            {/* Rotate sat beside Copy with identical weight, though one is
                idempotent and the other invalidates a credential an encoder may
                be using right now. Confirmed rather than merely restyled: the
                consequence is not visual, it is that somebody's publisher stops
                being able to connect. */}
            <Button
              size="sm"
              variant="outline"
              className="border-warn/50 text-warn hover:bg-warn-dim/30"
              onClick={onRotate}
              disabled={busy}
            >
              <KeyRound className="h-3 w-3" /> {t("sources.rotate")}
              <InfoHint body="sources.help.token" title="sources.token" />
            </Button>
          </div>
          {/* Stated plainly either way. Telling someone a rotated token
              secures an ingest it does not is the worse error, but hiding that
              it now does is also wrong — so the copy follows the server's
              tokenEnforced, which follows the running listener rather than the
              setting. */}
          {source.tokenEnforced ? (
            <p className="flex items-start gap-1.5 text-[10px] text-muted-foreground">
              <ShieldCheck className="mt-0.5 h-3 w-3 shrink-0 text-live" />
              <span>
                <strong className="font-semibold">{t("sources.tokenIsCredential")}</strong>{" "}
                {/* Mode-specific, because the two protocols carry the token in
                    different places and the generic sentence named only SRT's.
                    Now that the token is enforced for RTMP too, an RTMP operator
                    was being told to put it in an "SRT streamid" — a field their
                    encoder does not have. */}
                {ing.mode === "rtmp"
                  ? t("sources.tokenEnforcedDetailRtmp")
                  : t("sources.tokenEnforcedDetailSrt")}
              </span>
            </p>
          ) : (
            <p className="flex items-start gap-1.5 text-[10px] text-muted-foreground">
              <Info className="mt-0.5 h-3 w-3 shrink-0" />
              <span>
                <strong className="font-semibold">{t("sources.tokenNotEnforced")}</strong>{" "}
                {ing.mode === "rtmp"
                  ? t("sources.tokenNotEnforcedRtmp")
                  : t("sources.tokenNotEnforcedSrt")}
              </span>
            </p>
          )}
        </div>
        </div>
      </CardContent>
    </Card>
  );
}

/** A bounded number input.
 *
 *  min/max/step are on the element itself, so the browser refuses an
 *  out-of-range value before it can be submitted. The server validates the same
 *  ranges -- that is the real guarantee -- but a form that accepts 70000 into a
 *  port field and reports the problem a round trip later has made the operator
 *  do the checking. Refusing the keystroke is the control; validating the
 *  request is the backstop.
 *
 *  Bounds come from lib/limits.ts, which mirrors the Go constants in one place
 *  rather than being retyped per field. */
function NumberField({
  label,
  hint,
  value,
  min,
  max,
  onChange,
}: {
  label: string;
  hint?: TranslationKey;
  value: number;
  min: number;
  max: number;
  onChange: (n: number) => void;
}) {
  const bad = value < min || value > max;
  return (
    <div className="flex flex-col gap-1">
      <Label className="flex items-center gap-1">
        {label}
        <span className="font-normal text-subtle-foreground">
          {min}–{max}
        </span>
        {hint && <InfoHint body={hint} />}
      </Label>
      <Input
        className={cn("tnum h-7 font-mono text-[11px]", bad && "border-down")}
        type="number"
        inputMode="numeric"
        min={min}
        max={max}
        step={1}
        value={value}
        onChange={(e) => {
          const n = Number(e.target.value);
          if (Number.isFinite(n)) onChange(n);
        }}
      />
    </div>
  );
}
