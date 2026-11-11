import { lazy, useCallback, useEffect, useId, useState, type ReactNode } from "react";
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
import { CaptionPolicyEditor } from "@/components/CaptionPolicyEditor";
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
import { LazyBoundary } from "@/components/ErrorBoundary";
import { NoProgrammeYet } from "@/components/NoProgrammeYet";
import { SecretInput } from "@/components/SecretInput";
import { AutoStopControl } from "@/components/AutoStopControl";
import { DegradedLinkSettings } from "@/components/DegradedLinkSettings";
import { DestinationDialog } from "@/components/DestinationDialog";
import { PullIngestFields } from "@/components/PullIngestFields";
import { ConfirmDestructive } from "@/components/ConfirmDestructive";
import { api, mutationWarnings } from "@/lib/api";
import { useT, type TranslationKey } from "@/lib/i18n";
import { InfoHint } from "@/components/InfoHint";
import { SecretCode } from "@/components/SecretCode";
import { useLiveData } from "@/hooks/useLiveData";
import { copyToClipboard as copy } from "@/lib/clipboard";
import { WaitingForEncoder } from "@/components/WaitingForEncoder";
import { srtAddressParts, urlCarriesCredential } from "@/lib/credential-url";
import { tokenNotEnforcedKey } from "@/features/sources/tokenNotice";
import { LIMITS } from "@/lib/limits";
import { cn } from "@/lib/utils";
import { publishRows } from "@/lib/publish-url";
import { ingestPhase, ingestSummary, ingestWithoutAutoStop } from "@/features/sources/sourceIngest";
import type { AutoStopInfo, AutoStopSettings, Source, SourceId, SourceView } from "@/lib/types";

// hls.js is only needed once a card shows a receiving feed (A2), so it is
// fetched then, the same way the Dashboard loads it.
const PreviewPlayer = lazy(() =>
  import("@/components/PreviewPlayer").then((m) => ({ default: m.PreviewPlayer })),
);

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


export function SourcesPage() {
  const t = useT();
  const { refreshSources } = useLiveData();
  const [sources, setSources] = useState<SourceView[]>([]);
  const [loading, setLoading] = useState(true);
  const [creating, setCreating] = useState(false);
  const [newName, setNewName] = useState("");
  const [deleting, setDeleting] = useState<SourceView | null>(null);
  const [rotating, setRotating] = useState<SourceView | null>(null);
  const [busyId, setBusyId] = useState<SourceId | null>(null);
  // A30: the programme whose "Add destination" was pressed; the dialog opens on it.
  const [addingTo, setAddingTo] = useState<SourceId | null>(null);

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

  // Whether the preview encoder is switched on (Settings → Pipeline). A card
  // only offers a thumbnail when there is one to fetch; a failed read is
  // treated as off, which shows the line saying how to turn it on.
  const [previewOn, setPreviewOn] = useState(false);
  useEffect(() => {
    api.getSettings().then(
      (s) => setPreviewOn(!!s.preview?.enabled),
      () => setPreviewOn(false),
    );
  }, []);

  const create = async () => {
    const name = newName.trim();
    if (!name) return;
    try {
      // Name only: the server fills in a default ingest, so adding a programme
      // does not require choosing ports before you have one to configure. That
      // default is SRT (handleCreateSource), not "unset": the shared SRT port
      // admits only SRT-mode sources, and an unset one is refused on every
      // port. The card's select changes it afterwards.
      await api.createSource({ name });
      setCreating(false);
      setNewName("");
      toast.success(t("sources.added", { name }));
      await load();
      // The console follows ONE programme, resolved from /sources. Adding a
      // source changes that answer -- on a first-run install it changes it
      // from "none" to this one -- and nothing else re-asks. #646.
      await refreshSources();
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
        ingest: ingestWithoutAutoStop(s.ingest),
        token: s.token,
        position: s.position,
        // Carried every save, null included: the server decodes over the stored
        // row, so an absent key would keep the old track and null is how a
        // chosen track is cleared back to automatic.
        captionTrack: s.captionTrack ?? null,
        ...changes,
      };
      if (body.ingest) body.ingest = ingestWithoutAutoStop(body.ingest);
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
    // #18: matches patch()/rotate() above. ConfirmDestructive disables its own
    // Confirm button while busy, but that guard dies with the dialog -- Escape
    // or an overlay click closes it without waiting for this to finish, and an
    // in-flight delete left the Trash2 button on this row clickable again,
    // open to a second delete of a row already gone. busyId disables it too.
    setBusyId(deleting.id);
    try {
      // The count the dialog showed, which the server checks is still the
      // count: a destination added since the dialog opened refuses the delete
      // (409) and the toast says so, rather than taking a row nobody saw.
      const res = await api.deleteSource(deleting.id, deleting.destinations);
      toast.success(t(wasOnly ? "sources.deletedLast" : "sources.deleted", { name: deleting.name }));
      // What the cascade could not take with it, in the server's words.
      for (const w of mutationWarnings(res)) toast.warning(w, { duration: 15000 });
      setDeleting(null);
      await load();
      // Deleting the programme the console is following leaves every other
      // page pointed at a source that no longer exists, rendering plausible
      // idle states against a healthy server. #646.
      await refreshSources();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t("sources.deleteFailed"));
      // Close the dialog and re-read the list. The commonest failure here is
      // the 409 above, and the dialog is holding the stale count that caused
      // it: left open, every retry sends that same number and is refused
      // again, while the toast tells the operator to confirm a count the
      // console never shows them. Reopening from the refreshed row shows the
      // current count, and a source someone else already deleted (404) drops
      // out of the list instead of staying clickable.
      setDeleting(null);
      await load();
    } finally {
      setBusyId(null);
    }
  };

  const renderSources = (): ReactNode => {
    if (loading) {
      return (
        <div className="flex items-center gap-2 py-6 text-[12px] text-muted-foreground">
          <Loader2 className="h-3.5 w-3.5 animate-spin" /> {t("sources.loading")}
        </div>
      );
    }
    return sources.length === 0 ? (
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
        action={{ label: t("sources.addSource"), onClick: () => setCreating(true) }}
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
            onAddDestination={() => setAddingTo(s.id)}
            onAutoStopSaved={(next, info) =>
              // Local state, not load(): a re-read would hand every card a new
              // `source` and throw away what is typed in it but not yet applied.
              setSources((all) =>
                all.map((x) =>
                  x.id === s.id
                    ? { ...x, ingest: { ...x.ingest, autoStop: next }, autoStopInfo: info ?? x.autoStopInfo }
                    : x,
                ),
              )
            }
            previewOn={previewOn}
          />
        ))}
      </div>
    );
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
            // See ui/src/features/tour/tourSteps.ts.
            data-tour="add-source"
            onClick={() => setCreating(true)}
          >
            <Plus className="h-3.5 w-3.5" /> {t("sources.addSource")}
          </Button>
        }
      />

      {renderSources()}

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
      <DestinationDialog
        open={addingTo !== null}
        onOpenChange={(o) => !o && setAddingTo(null)}
        destination={null}
        initialSourceId={addingTo === null ? undefined : String(addingTo)}
        onSaved={() => void load()}
      />
    </div>
  );
}

/* Exported for its test. The card is where a source's whole truth is stated --
 * whether it is running, whether its token gates anything, and now whether it
 * accepts publishes at all -- and those are claims worth pinning without
 * standing up the page around it. */
/** A credential-carrying publish address (A31). For SRT the host:port the
 *  encoder points at is printed in clear, and the query string that holds the
 *  passphrase and stream ID is masked behind Reveal. Everything else that
 *  carries a credential, including pull URLs with userinfo, is masked whole.
 *  The Copy button beside this copies the full URL either way. */
function PublishAddress({ url }: Readonly<{ url: string }>) {
  const parts = srtAddressParts(url);
  if (!parts) return <SecretCode value={url} />;
  return (
    <>
      <code className="min-w-0 truncate rounded bg-muted px-1.5 py-1 font-mono text-sm">{parts.address}</code>
      <SecretCode value={parts.query} />
    </>
  );
}

export function SourceCard({
  source,
  busy,
  onPatch,
  onRotate,
  onDelete,
  onAddDestination,
  onAutoStopSaved,
  previewOn = false,
}: Readonly<{
  source: SourceView;
  busy: boolean;
  onPatch: (changes: Partial<Source>) => void;
  onRotate: () => void;
  onDelete: () => void;
  /** A30. Opens the destination dialog on this programme. Absent in tests that
   *  do not exercise the receiving card, so the button is then not drawn. */
  onAddDestination?: () => void;
  /** The stop-on-ingest-loss control saved: the page updates its copy of this
   *  source in place. */
  onAutoStopSaved?: (next: AutoStopSettings, info?: AutoStopInfo) => void;
  /** The preview encoder is on, so a receiving card can show a thumbnail. */
  previewOn?: boolean;
}>) {
  const t = useT();
  // Binds each ingest caption to its field. The captions were visible but not
  // programmatically tied to the inputs, so a screen reader announced three
  // unnamed fields. The InfoHint sits BESIDE the <label>, not in it: a button
  // inside a label is a second labelable element, and would end up in the
  // field's name ("Passphrase Passphrase — More information").
  const fieldId = useId();
  // A LOCAL DRAFT, not a per-field commit.
  //
  // These fields used to write straight through on blur, and every write
  // restarts this source's ingest. That turned tabbing out of a port field into
  // a dropped broadcast -- an outage as the side effect of a keystroke nobody
  // meant as a decision. Edits now accumulate here and go nowhere until Apply.
  //
  // The draft never holds ingest.autoStop (see ingestWithoutAutoStop): it saves
  // on its own button, so saving it neither dirties this draft nor resets it.
  const storedIngest = ingestWithoutAutoStop(source.ingest);
  const storedKey = JSON.stringify(storedIngest);
  const [draft, setDraft] = useState(storedIngest);
  // eslint-disable-next-line react-hooks/exhaustive-deps -- follows the stored ingest's content, not its identity
  useEffect(() => setDraft(ingestWithoutAutoStop(source.ingest)), [storedKey]);

  const ing = draft;
  const setIngest = (changes: Partial<Source["ingest"]>) =>
    setDraft((d) => ({ ...d, ...changes }));
  const dirty = JSON.stringify(draft) !== storedKey;
  // The state of the feed as the server reports it, not as the draft reads.
  // Derived from the saved source so an unsaved protocol change does not
  // claim an encoder is waiting on a port that is not yet listening.
  const phase = ingestPhase(source);
  const summary = ingestSummary({ ...source, ingest: ing });

  return (
    <Card className={source.running ? undefined : "border-warn/40"}>
      {/* Stacks below sm. Side by side at 390px, the Accepting block took
          its 16rem and the programme's name was squeezed to nothing beside
          the RUNNING badge; the badges now wrap under the name, and the switch
          block sits under the title instead of beside it. */}
      <CardHeader className="flex-col items-stretch justify-between gap-2 sm:flex-row sm:items-start">
        <div className="min-w-0">
          <CardTitle className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <RadioTower className="h-3.5 w-3.5 shrink-0" />
            <span className="min-w-0 truncate" data-testid="source-name">{source.name}</span>
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
            {/* AND BESIDE IT AGAIN, for the state that reads worst of all.
                `running` is Engine(id) != nil, and manager.go builds an engine
                for every row whether or not it is enabled -- so a disabled
                source keeps its saturated green "running" badge, its publish
                URLs and its "the token is enforced" line while the listener
                answers REJ_CLOSE to every encoder that dials it. Nothing else
                on this card mentions `enabled` but the switch.
                A WARNING, not a control: refusing to draw the card would be
                worse, and the switch beside it is already the control. What was
                missing is the sentence saying which of the two facts on screen
                wins. */}
            {!source.enabled && (
              <Badge variant="outline" data-testid="source-disabled">
                {t("sources.disabledRefused")}
              </Badge>
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
          {/* A33: the state in words and the consequence beside the switch. The
              aria-label names the switch; the visible text says what it does. */}
          <div className="flex flex-col items-start gap-0.5 sm:max-w-64 sm:items-end sm:text-right">
            <div className="flex items-center gap-2">
              <span className="text-sm font-medium">
                {source.enabled ? t("sources.acceptingLabel") : t("state.off")}
              </span>
              <Switch
                checked={source.enabled}
                onCheckedChange={(v) => onPatch({ enabled: v })}
                aria-label={t("sources.enableAria", { name: source.name })}
              />
            </div>
            <span className="text-sm text-muted-foreground">
              {source.enabled ? t("sources.enabledConsequence") : t("sources.disabledConsequence")}
            </span>
          </div>
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
            disabled={busy}
            title={t("sources.deleteThis")}
            aria-label={t("sources.deleteAria", { name: source.name })}
          >
            <Trash2 className="h-3.5 w-3.5" />
          </Button>
        </div>
      </CardHeader>

      <CardContent className="flex flex-col gap-3">
        {/* Which audio track of this programme the live captioner listens to
            (#8). Saved on change; the captioner follows it on the next reconcile. */}
        <div className="flex flex-col gap-1">
          <Label htmlFor={`src-caption-track-${source.id}`}>{t("wave3.captions.trackLabel")}</Label>
          <Select
            value={source.captionTrack == null ? "auto" : String(source.captionTrack)}
            onValueChange={(v) => onPatch({ captionTrack: v === "auto" ? null : Number(v) })}
            disabled={busy}
          >
            <SelectTrigger id={`src-caption-track-${source.id}`} aria-label={t("wave3.captions.trackLabel")}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="auto">{t("wave3.captions.trackAuto")}</SelectItem>
              {Array.from({ length: 16 }, (_, i) => (
                <SelectItem key={i} value={String(i)}>
                  {t("wave3.captions.trackN", { n: i + 1 })}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <p className="text-xs text-muted-foreground">{t("wave3.captions.trackHint")}</p>
        </div>
        {/* The programme's caption words (#8). The key remounts the editor
            when the stored policy changes, so a reload shows what was saved. */}
        <CaptionPolicyEditor
          key={JSON.stringify(source.captionPolicy ?? {})}
          sourceId={source.id}
          policy={source.captionPolicy}
          busy={busy}
          onSave={(captionPolicy) => onPatch({ captionPolicy })}
        />
        {!source.running && (
          <p className="flex items-start gap-1.5 text-sm text-warn">
            <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
            {t("sources.noEngine")}
          </p>
        )}

        {/* The detail, not just the badge. "Degraded" on its own sends an
            operator to the logs; the server already knows which address failed
            and why, and that sentence is the whole difference between noticing
            and fixing. */}
        {source.listenerHealth?.state === "degraded" && source.listenerHealth.detail && (
          <p className="flex items-start gap-1.5 text-sm text-warn">
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
          <p className="flex items-start gap-1.5 text-sm text-warn" data-testid="pull-unchecked">
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
                className="h-7 text-sm"
                aria-label={t("sources.ingest")}
                data-testid="ingest-mode"
              >
                <SelectValue placeholder={t("sources.ingestPlaceholder")}>
                  {summary && (
                    <span className="flex items-center gap-2">
                      <span className="font-mono text-[12px] font-semibold text-primary-bright">
                        {summary.proto}
                      </span>
                      {summary.detail && (
                        <span className="font-mono text-[12px] text-muted-foreground">
                          {summary.detail}
                        </span>
                      )}
                    </span>
                  )}
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="srt">SRT</SelectItem>
                <SelectItem value="rtmp">RTMP</SelectItem>
                <SelectItem value="pull">{t("sources.modePull")}</SelectItem>
              </SelectContent>
            </Select>
          </div>

          {/* THE FIELDS THE MODE NEEDS, for every mode the select offers.
              `pull` was in the list with nothing behind it: db/settings.go
              refuses the save with "pull url is required", naming a field this
              card did not have, and a source that already had one stored
              started dialling a URL nobody was shown. Same component the
              settings tab renders, so the two can never describe the mode
              differently. */}
          {ing.mode === "pull" && (
            <div className="flex flex-col gap-2 sm:col-span-3">
              <PullIngestFields
                idPrefix={`source-${source.id}-pull`}
                value={ing.pull}
                onChange={(pull) => setIngest({ pull })}
              />
            </div>
          )}

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
                <div className="flex items-center gap-1">
                  <Label htmlFor={`${fieldId}-passphrase`}>{t("sources.passphrase")}</Label>
                  <InfoHint body="sources.help.passphrase" title="sources.passphrase" />
                </div>
                <SecretInput
                  id={`${fieldId}-passphrase`}
                  className="h-7 text-sm"
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
                <div className="flex items-center gap-1">
                  <Label htmlFor={`${fieldId}-app`}>{t("sources.app")}</Label>
                  <InfoHint body="sources.help.app" title="sources.app" />
                </div>
                <Input
                  id={`${fieldId}-app`}
                  className="h-7 text-sm"
                  value={ing.rtmp.app}
                  onChange={(e) => setIngest({ rtmp: { ...ing.rtmp, app: e.target.value } })}
                />
              </div>
              <div className="flex flex-col gap-1">
                <div className="flex items-center gap-1">
                  <Label htmlFor={`${fieldId}-streamkey`}>{t("sources.streamKey")}</Label>
                  <InfoHint body="sources.help.streamKey" title="sources.streamKey" />
                </div>
                <SecretInput
                  id={`${fieldId}-streamkey`}
                  className="h-7 text-sm"
                  value={ing.rtmp.streamKey}
                  onChange={(e) => setIngest({ rtmp: { ...ing.rtmp, streamKey: e.target.value } })}
                />
              </div>
            </>
          )}
        </div>

        {/* This programme's degraded-link thresholds (#6), with its ingest:
            link quality is a property of one encoder's uplink. */}
        <DegradedLinkSettings
          collapsible
          idPrefix={`source-${source.id}-degraded`}
          value={ing.degraded}
          onChange={(degraded) => setIngest({ degraded })}
        />

        {/* This programme's stop-on-ingest-loss safety net. Saves on its own
            button, like the OBS bridge panel that edits the same stored value. */}
        <AutoStopControl
          sourceId={Number(source.id)}
          idPrefix={`source-${source.id}-autostop`}
          value={source.ingest.autoStop}
          info={source.autoStopInfo}
          onSaved={(next, info) => onAutoStopSaved?.(next, info)}
        />

        {phase === "waiting" && <WaitingForEncoder />}

        {phase === "receiving" && (
          <div
            role="status"
            className="flex items-start gap-3 rounded-md border border-ok/30 bg-ok-dim/20 px-3 py-2"
          >
            {/* WHAT IS ARRIVING, not only that something is (A2). A small live
                picture of this source's own feed, so the operator can see it is
                the right camera before adding a destination. Muted, and only
                while the card is in the receiving state. */}
            {previewOn ? (
              <div className="w-36 shrink-0 overflow-hidden rounded border border-border" data-testid="source-thumbnail">
                <LazyBoundary fallback={<div className="aspect-video bg-muted" />}>
                  <PreviewPlayer
                    active
                    sourceId={source.id}
                    outputLive
                    ingestLive
                  />
                </LazyBoundary>
              </div>
            ) : (
              <span aria-hidden className="mt-1 h-2 w-2 shrink-0 rounded-full bg-ok" />
            )}
            <div className="flex min-w-0 flex-col gap-1">
              <p className="text-[12px] font-semibold text-ok">{t("sources.signalDetected")}</p>
              <p className="text-[12px] text-muted-foreground">{t("sources.signalDetectedBody")}</p>
              {!previewOn && (
                <p className="text-[12px] text-muted-foreground">{t("sources.thumbnailOff")}</p>
              )}
              {/* A30: opens the destination dialog here, on this programme. The
                  old link went to Dashboard with nothing carried over. */}
              {onAddDestination && (
                <Button size="sm" variant="outline" className="w-fit" onClick={onAddDestination}>
                  {t("firstRun.addDestination")}
                </Button>
              )}
            </div>
          </div>
        )}

        {dirty && (
          /* The consequence, at the moment of decision. Applying reconciles
             this source's ingest, which restarts its FFmpeg child; if an
             encoder is connected that is a dropped broadcast. Stated on the
             button rather than in a dialog, because a label is read and a
             dialog is dismissed. */
          <div className="flex flex-wrap items-center gap-2 rounded-md border border-warn/40 bg-warn-dim/20 px-2 py-1.5">
            <AlertTriangle className="h-3.5 w-3.5 shrink-0 text-warn" />
            <span className="min-w-0 flex-1 text-sm">
              {source.publishing ? t("sources.dirtyPublishing") : t("sources.dirtyIdle")}
            </span>
            <Button size="sm" variant="ghost" onClick={() => setDraft(storedIngest)} disabled={busy}>
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

            See ui/src/features/tour/tourSteps.ts. */}
        <div data-tour="source-publish-urls" className="flex flex-col gap-3">
        {publishRows(source.publishUrls, window.location.hostname).map(({ proto, url }) =>
          url ? (
            // Wraps below sm: the label takes its own line, so the address, the
            // masked credential and the two buttons get the full width. At
            // 360px the SRT credential was squeezed to a single dot.
            <div key={proto} className="flex flex-wrap items-center gap-x-2 gap-y-1">
              <span className="w-full shrink-0 text-sm uppercase tracking-wider text-muted-foreground sm:w-20">
                {proto}
              </span>
              {/* Gated on what the string CARRIES, not on the row's label.
                  In RTMP mode the token is split out as the streamKey row and
                  the address beside it is a plain host, which stays readable:
                  an operator checks it at a glance, and masking it would train
                  them to hit reveal on everything. In SRT mode there is no
                  split -- publishURLs in internal/api/sources.go welds
                  passphrase and streamid INTO the srt row -- and a version of
                  this that keyed on the label alone printed both in plain text
                  on every SRT install. */}
              {proto === "streamKey" || urlCarriesCredential(url) ? (
                <PublishAddress url={url} />
              ) : (
                <code className="min-w-0 flex-1 truncate rounded bg-muted px-1.5 py-1 font-mono text-sm">
                  {url}
                </code>
              )}
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
          <div className="flex flex-wrap items-center gap-3 rounded-md border border-ok/30 bg-ok-dim/20 px-2 py-1.5">
            <span className="flex items-center gap-1.5 text-sm font-semibold uppercase tracking-wider text-ok">
              <Activity className="h-3 w-3" /> {t("sources.publishing")}
            </span>
            <span className="font-mono text-sm text-muted-foreground">{source.link.peer}</span>
            <span className="tnum font-mono text-sm">
              {t("sources.bitrate")} {Math.round(source.link.bitrateKbps ?? 0)} kbps
            </span>
            <span className="tnum font-mono text-sm">{t("sources.rtt")} {source.link.rttMs.toFixed(1)} ms</span>
            {/* Packets lost since connect, and the share lost over the last
                interval: the first says the link has history, the second says
                whether it is breaking up NOW, which is what failover reads. */}
            <span className="tnum font-mono text-sm">
              {t("sources.loss")} {source.link.lossPackets} ({(source.link.lossPercent ?? 0).toFixed(1)}%)
            </span>
            <span className="tnum font-mono text-sm">{t("sources.retrans")} {source.link.retransPackets}</span>
          </div>
        )}

        {source.bonded && (source.bonded.links?.length ?? 0) > 0 && (
          /* The SRTLA group behind the link above: one row per network link,
             so an operator can see which modem is carrying the stream and
             which has dropped to nothing. */
          <div className="flex flex-col gap-1 rounded-md border border-border px-2 py-1.5">
            <span className="text-sm font-semibold uppercase tracking-wider text-muted-foreground">
              {t((source.bonded.links?.length ?? 0) === 1 ? "sources.bondedOne" : "sources.bonded", {
                count: source.bonded.links?.length ?? 0,
              })}
            </span>
            {source.bonded.links?.map((l) => (
              <div key={l.peer} className="flex flex-wrap items-center gap-3">
                <span className="font-mono text-sm text-muted-foreground">{l.peer}</span>
                <span className="tnum font-mono text-sm">
                  {t("sources.bitrate")} {Math.round(l.bitrateKbps)} kbps
                </span>
                <span className="tnum font-mono text-sm">
                  {t("sources.share")} {Math.round(l.share * 100)}%
                </span>
              </div>
            ))}
          </div>
        )}

        {source.degraded && (
          /* Degraded-link failover (#6) judged this programme's primary link
             degraded. Shown even when nothing switched -- with no backup or
             slate the primary stays on air, and this is how the operator
             learns the uplink is breaking up. */
          <output className="flex flex-wrap items-center gap-2 rounded-md border border-warn/40 bg-warn/10 px-2 py-1.5 text-sm">
            <span className="font-semibold uppercase tracking-wider text-warn">{t("sources.degraded")}</span>
            <span className="font-mono text-muted-foreground">{source.degraded}</span>
          </output>
        )}

        {/* The token, and the truth about it. */}
        <div className="flex flex-col gap-1.5 border-t border-border pt-2">
          <div className="flex items-center gap-2">
            <span className="w-20 shrink-0 text-sm uppercase tracking-wider text-muted-foreground">
              {t("sources.token")}
            </span>
            <SecretCode value={source.token} />
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
            <p className="flex items-start gap-1.5 text-sm text-muted-foreground">
              <ShieldCheck className="mt-0.5 h-3 w-3 shrink-0 text-ok" />
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
            <p className="flex items-start gap-1.5 text-sm text-muted-foreground">
              <Info className="mt-0.5 h-3 w-3 shrink-0" />
              <span>
                <strong className="font-semibold">{t("sources.tokenNotEnforced")}</strong>{" "}
                {/* By mode, because "not enforced" covers a listener that is
                    down, a pull source, and an unchosen ingest whose token the
                    shared port refuses outright. See tokenNotEnforcedKey. */}
                {t(tokenNotEnforcedKey(ing.mode))}
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
}: Readonly<{
  label: string;
  hint?: TranslationKey;
  value: number;
  min: number;
  max: number;
  onChange: (n: number) => void;
}>) {
  const bad = value < min || value > max;
  // Bound to its caption and range; the InfoHint is beside the label, not in it.
  const id = useId();
  return (
    <div className="flex flex-col gap-1">
      <div className="flex items-center gap-1">
        <Label htmlFor={id} className="flex items-center gap-1">
          {label}
          <span className="font-normal text-muted-foreground">
            {min}–{max}
          </span>
        </Label>
        {hint && <InfoHint body={hint} />}
      </div>
      <Input
        id={id}
        className={cn("tnum h-7 font-mono text-sm", bad && "border-down")}
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
