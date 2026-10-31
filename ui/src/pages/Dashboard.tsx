import { lazy, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { LazyBoundary } from "@/components/ErrorBoundary";
import { urlCarriesCredential } from "@/lib/credential-url";
import { SecretCode } from "@/components/SecretCode";
import type { MutableRefObject, ReactNode } from "react";
import { Link } from "react-router";
import { toast } from "sonner";
import { ConfirmDestructive } from "@/components/ConfirmDestructive";
import { useDestinationUsage } from "@/lib/destinationUsage";
import { usePreviewTiles } from "@/hooks/usePreviewTiles";
import { previewLayout } from "@/lib/previewLayout";
import {
  attributableBitrate,
  audioTrackCount,
  ingestAttribution,
  ingestBitrateKbps,
  ingestUptimeSec,
  processAbsence,
} from "@/lib/dashboardFacts";
import { laneLayout } from "@/lib/sourceLanes";
import { cn } from "@/lib/utils";
import { topRowLayout } from "@/lib/dashboardLayout";
import { failoverNotice, type FailoverNotice } from "@/lib/failoverNotice";
import {
  failoverBannerTone,
  failoverElapsedSeconds,
  failoverSinceClock,
} from "@/lib/failoverBanner";
import { backupOnAir, ingestProblem } from "@/lib/ingestChip";
import { publishRows } from "@/lib/publish-url";
import { copyToClipboard } from "@/lib/clipboard";
import { StatusReason } from "@/components/signature/StatusReason";
import { trackCount, trackLabels } from "@/lib/trackLabels";
import { useConfirm } from "@/hooks/useConfirm";
import { Circle, Copy, Megaphone, Play, Plus, Radio, RotateCcw, ShieldAlert, Square } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { PageHeader } from "@/components/AppLayout";
import { NoProgrammeYet } from "@/components/NoProgrammeYet";
import { GoLiveSheet } from "@/components/GoLiveSheet";
import { goLiveRows, youtubeBroadcastsAtRisk, type GoLiveRow } from "@/lib/goLive";
import { SetupChecklist } from "@/components/SetupChecklist";
import { setupChecklist } from "@/lib/setupChecklist";
import { DestinationCard } from "@/components/DestinationCard";
import { DestinationHoldNote } from "@/components/DestinationHoldNote";
import { DestinationDialog } from "@/components/DestinationDialog";
import { OnAirBar } from "@/components/OnAirBar";
import { ChatPanel } from "@/components/ChatPanel";
import { StatusDot } from "@/components/signature/StatusDot";
import { Stat } from "@/components/signature/Stat";
import { useLiveData } from "@/hooks/useLiveData";
import { useProgrammeIngest } from "@/hooks/useProgrammeIngest";
import { api, isNoSource } from "@/lib/api";
import {
  pollVerdict,
  type BroadcastWindowRow,
  type MetaJob,
  type MetaState,
  type MetaTarget,
} from "@/lib/metadataPush";
import { duration, kbps } from "@/lib/format";
import { toneBadge, toneForState } from "@/lib/signal";
import type { SignalTone } from "@/lib/signal";
import type {
  BulkDestOutcome,
  BulkDestReport,
  Destination,
  DestinationId,
  DestStatus,
  FailoverPin,
  FailoverSettings,
  FailoverSource,
  FailoverStatus,
  MetaField,
  SourceInfo,
  SourceView,
  SystemInfo,
} from "@/lib/types";
import { useT, useStateLabel, type TranslationKey } from "@/lib/i18n";

// hls.js is a few hundred kilobytes that only the preview needs, and the
// preview is off entirely for some installs. Load it alongside the dashboard
// rather than ahead of it.
const PreviewPlayer = lazy(() =>
  import("@/components/PreviewPlayer").then((m) => ({ default: m.PreviewPlayer })),
);

// ---------------------------------------------------------- go-live composer
//
// Set the title, description and category once and push them to every
// connected account. The wire shapes, which mirror internal/api/metadata.go,
// and the rule for when to stop polling a push live in lib/metadataPush.ts, so
// lib/api.ts can type the routes and the rule can be tested without a page.
//
// MetaField lives in lib/types.ts: a push RESULT names those fields, so the
// union is an API contract rather than a detail of this page -- and
// internal/oauth has a drift guard that reads it there.

// A push is reported per platform, never as one boolean, so each state needs
// its own place in the signal language: still working, done, done with
// something left undone, and refused.
const metaTone: Record<MetaState, SignalTone> = {
  pending: "armed",
  ok: "live",
  partial: "warn",
  error: "down",
};

const metaLabel: Record<MetaState, string> = {
  pending: "Pushing",
  ok: "Updated",
  partial: "Partial",
  error: "Failed",
};

export function GoLiveComposer() {
  const t = useT();
  const [targets, setTargets] = useState<MetaTarget[] | null>(null);
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [category, setCategory] = useState("");
  const [job, setJob] = useState<MetaJob | null>(null);
  const [pushing, setPushing] = useState(false);
  const [tags, setTags] = useState("");
  const [scheduledStart, setScheduledStart] = useState("");
  // Tri-state: "" means the operator has not touched it, so the field is
  // omitted from the push and the platform keeps whatever it has. That
  // distinction is the whole reason the API takes pointers -- "off" and
  // "unmentioned" must not collapse into the same request.
  const [dvr, setDvr] = useState("");
  const [autoStart, setAutoStart] = useState("");
  const [autoStop, setAutoStop] = useState("");
  const [windows, setWindows] = useState<BroadcastWindowRow[] | null>(null);

  useEffect(() => {
    let live = true;
    api
      .metadata()
      .then((data) => {
        if (!live) return;
        setTargets(data.targets);
        // Restoring the last push means a reloaded tab still shows which
        // platforms took the title and which did not.
        if (data.last) {
          setJob(data.last);
          setTitle(data.last.metadata.title);
          setDescription(data.last.metadata.description);
          setCategory(data.last.metadata.category);
        }
      })
      // NULL, NOT []. #719. `targets` is `MetaTarget[] | null` and null is the
      // unknown state -- `if (targets === null) return null` hides the composer
      // entirely. Storing [] here made a failed read render the composer with
      // ZERO platforms, which is a positive claim: it says the server answered
      // and this broadcast has nowhere to push metadata. Unknown keeps it
      // hidden, which is what it was before the request.
      .catch(() => setTargets(null));
    // What is still editable, read once when the composer opens. Deliberately
    // not polled: each row is a live platform call, and a broadcast that goes
    // live mid-edit is caught by the write's own 403 rather than by a timer.
    api
      .metadataBroadcastWindow()
      .then((data) => {
        if (live) setWindows(data.accounts ?? []);
      })
      .catch(() => {
        // A failed read must not disable the controls. The operator can still
        // push; the platform decides.
        if (live) setWindows([]);
      });
    return () => {
      live = false;
    };
  }, []);

  // The push is a job precisely so a slow platform API cannot hold the page,
  // so the page has to poll it back.
  //
  // Until it cannot: jobs live in the server's memory, so a restart mid-push
  // loses this one and every poll after it fails. pollVerdict decides when
  // that is final, and `lostJob` is the job the composer has given up on --
  // keyed by id, so starting a new push clears it without a reset to forget.
  const jobId = job?.id;
  const [lostJob, setLostJob] = useState<string | null>(null);
  const jobLost = jobId !== undefined && lostJob === jobId;
  const jobDone = (job?.done ?? true) || jobLost;
  useEffect(() => {
    if (!jobId || jobDone) return;
    let live = true;
    let failures = 0;
    const timer = window.setInterval(() => {
      api
        .metadataPushJob(jobId)
        .then((next) => {
          if (!live) return;
          failures = 0;
          setJob(next);
        })
        .catch((err: unknown) => {
          if (!live) return;
          failures++;
          if (pollVerdict(err, failures) === "lost") setLostJob(jobId);
        });
    }, 1200);
    return () => {
      live = false;
      window.clearInterval(timer);
    };
  }, [jobId, jobDone]);

  const accepts = useCallback(
    (field: MetaField) => (targets ?? []).filter((t) => t.caps.fields.includes(field)),
    [targets],
  );

  // The counter shows the tightest limit among the platforms being pushed to,
  // because that is the one that will refuse first.
  const titleMax = useMemo(() => {
    const limits = (targets ?? []).map((t) => t.caps.titleMax ?? 0).filter((n) => n > 0);
    return limits.length > 0 ? Math.min(...limits) : 0;
  }, [targets]);

  const overLimit = useMemo(
    () =>
      (targets ?? []).filter((t) => {
        const max = t.caps.titleMax ?? 0;
        return max > 0 && title.length > max;
      }),
    [targets, title],
  );

  /* THE SAME COUNTER FOR THE DESCRIPTION, and it is here because the limit was
   * already on the wire and already enforced.
   *
   * oauth.MetadataCaps.DescriptionMax has been sent since the composer shipped
   * -- YouTube publishes 5000 -- and MetaCaps declared it, so the field existed
   * in this file and nothing read it. api/metadata.go:505 refuses the push over
   * it with "YouTube descriptions are limited to 5000 characters; yours is N",
   * which meant the one platform that has a description limit told the operator
   * about it only AFTER they pressed Push, next to a title field that had been
   * counting down beside them the whole time. #786.
   *
   * The tightest limit among the targets, exactly as titleMax above: it is the
   * one that will refuse first. Both zeros mean "no published limit" rather
   * than "zero characters", which is why they are filtered out and not
   * min()-ed in. */
  const descriptionMax = useMemo(() => {
    const limits = (targets ?? []).map((t) => t.caps.descriptionMax ?? 0).filter((n) => n > 0);
    return limits.length > 0 ? Math.min(...limits) : 0;
  }, [targets]);

  const descriptionOverLimit = useMemo(
    () =>
      (targets ?? []).filter((t) => {
        const max = t.caps.descriptionMax ?? 0;
        return max > 0 && description.length > max;
      }),
    [targets, description],
  );

  const categoryHint = (targets ?? []).find((t) => t.caps.categoryHint)?.caps.categoryHint ?? "";
  const noDescription = (targets ?? []).filter((t) => !t.caps.fields.includes("description"));
  // What a push will send BEYOND what is typed here.
  //
  // Compliance is configured per destination and has no field in this composer,
  // so without this the operator presses Push and a COPPA declaration, a privacy
  // setting or a set of content labels goes out with nothing on screen having
  // mentioned it. A push that does more than it says is the same complaint as a
  // push that does less.
  const withCompliance = (targets ?? []).filter((t) => {
    const c = t.compliance;
    if (!c) return false;
    return Boolean(
      c.privacy || c.facebookPrivacy || c.madeForKids !== undefined ||
        (c.labels && Object.keys(c.labels).length > 0),
    );
  });
  // Same signal as noDescription/categoryHint above, not the broadcast-window
  // fetch below: Facebook resolves tags through top-level Metadata.Tags and
  // has no broadcast resource at all, so gating on broadcastAccounts would
  // hide this control for every Facebook-only install.
  const tagTargets = accepts("tags");

  // Locked when EVERY account that supports broadcast settings says so. With
  // two YouTube accounts and one still in "ready", the controls stay enabled --
  // disabling them would block an edit that would have worked on one of them.
  const broadcastAccounts = (windows ?? []).filter((w) => w.supported);
  const lockedRows = broadcastAccounts.filter((w) => w.window?.contentDetailsLocked);
  const contentDetailsLocked =
    broadcastAccounts.length > 0 && lockedRows.length === broadcastAccounts.length;
  const lockedReason = lockedRows[0]?.window?.lockedReason ?? "";

  // A tri-state select maps to a pointer: absent when untouched.
  const boolOrUndef = (v: string) => (v === "" ? undefined : v === "on");

  const push = async () => {
    setPushing(true);
    try {
      // Split once, shared by both destinations below. An empty entry would
      // make Facebook's resolver search for nothing, so a blank between two
      // commas is dropped rather than sent.
      const tagList =
        tags.trim() === "" ? undefined : tags.split(",").map((t) => t.trim()).filter(Boolean);
      const broadcast = {
        // YouTube and Kick take tags through PushBroadcastSettings, which is
        // keyed to a broadcast resource neither Facebook nor Twitch has.
        tags: tagList,
        scheduledStart: scheduledStart.trim() === "" ? undefined : new Date(scheduledStart).toISOString(),
        // Omitted entirely when locked. Sending them would earn a 403 that
        // reads as though the whole push failed, when the title half of it
        // very likely succeeded.
        enableDvr: contentDetailsLocked ? undefined : boolOrUndef(dvr),
        enableAutoStart: contentDetailsLocked ? undefined : boolOrUndef(autoStart),
        enableAutoStop: contentDetailsLocked ? undefined : boolOrUndef(autoStop),
      };
      const started = await api.pushMetadata({
        title,
        description,
        category,
        // Facebook resolves these through top-level Metadata.Tags rather than
        // the broadcast resource above -- without this, the whole tag
        // resolution path Facebook implements is unreachable.
        tags: tagList,
        broadcast,
      });
      setJob(started);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("dash.couldNotPushTheMetadata"));
    } finally {
      setPushing(false);
    }
  };

  if (targets === null) return null;

  // A broadcast-only push is a real thing an operator does: turning the DVR
  // off before going live without retyping a title that is already correct.
  const broadcastTouched =
    tags.trim() !== "" || scheduledStart.trim() !== "" ||
    dvr !== "" || autoStart !== "" || autoStop !== "";
  // Nothing typed AND nothing stored to send. The second half matters: a push
  // whose whole purpose is applying a stored COPPA declaration or privacy
  // setting types nothing here, and a button greyed out on the composer alone
  // would make the server's own allowance for that unreachable.
  const empty =
    !title.trim() && !description.trim() && !category.trim() && !broadcastTouched &&
    withCompliance.length === 0;
  const busy = pushing || (job !== null && !jobDone);

  return (
    // The testid scopes ui/e2e/go-live-composer.spec.ts to this card. "Title"
    // and "Tags" are generic enough to appear elsewhere on a dashboard, and a
    // spec that reached the wrong one would assert about a control it was not
    // written for -- silently, and in whichever direction happened to pass.
    <Card className="mt-4" data-testid="golive-composer">
      <CardHeader className="flex-row items-center justify-between">
        <CardTitle className="flex items-center gap-2">
          <Megaphone className="h-3.5 w-3.5 text-muted-foreground" />
          {/* This card is the stream's title, tags and category, not the start
              path: that is the toolbar's Go live, which opens the sheet (A7). */}
          {t("dash.streamInfo")}
        </CardTitle>
        {/* Shown only when there is an account to count. "0 accounts" beside
            the live count read as a fault; the card's one sentence says what
            to do instead (A86). */}
        {targets.length > 0 && (
          <span className="font-mono text-sm text-muted-foreground">
            {targets.length === 1 ? t("i18nSweep.accountsOne") : t("i18nSweep.accountsMany", { count: targets.length })}
          </span>
        )}
      </CardHeader>

      {targets.length === 0 ? (
        <CardContent>
          <p className="text-sm text-muted-foreground">
            {t("dash.connectPrompt")}
          </p>
        </CardContent>
      ) : (
        <CardContent className="grid gap-4 lg:grid-cols-2">
          {/* ---------- the one form ---------- */}
          <div className="flex flex-col gap-2.5">
            <div className="flex flex-col gap-1">
              <div className="flex items-baseline justify-between">
                <Label htmlFor="golive-title">{t("dash.metaTitle")}</Label>
                {titleMax > 0 && (
                  <span
                    className={`tnum font-mono text-micro ${
                      overLimit.length > 0 ? "text-down" : "text-muted-foreground"
                    }`}
                  >
                    {title.length}/{titleMax}
                  </span>
                )}
              </div>
              <Input
                id="golive-title"
                value={title}
                placeholder={t("dash.titlePlaceholder")}
                onChange={(e) => setTitle(e.target.value)}
              />
              {overLimit.length > 0 && (
                <p className="text-sm text-down">
                  {t("i18nSweep.titleTooLong", { platforms: overLimit.map((o) => o.platform).join(", ") })}
                </p>
              )}
            </div>

            <div className="flex flex-col gap-1">
              <div className="flex items-baseline justify-between">
                <Label htmlFor="golive-description">{t("dash.metaDescription")}</Label>
                {descriptionMax > 0 && (
                  <span
                    className={`tnum font-mono text-[10px] ${
                      descriptionOverLimit.length > 0 ? "text-down" : "text-muted-foreground"
                    }`}
                  >
                    {description.length}/{descriptionMax}
                  </span>
                )}
              </div>
              <Textarea
                id="golive-description"
                value={description}
                rows={3}
                placeholder={t("dash.descriptionPlaceholder")}
                onChange={(e) => setDescription(e.target.value)}
              />
              {descriptionOverLimit.length > 0 && (
                <p className="text-sm text-down">
                  {t("dash.descTooLong", {
                    platforms: descriptionOverLimit.map((t) => t.platform).join(", "),
                  })}
                </p>
              )}
              {noDescription.length > 0 && (
                <p className="text-sm text-muted-foreground">
                  {t("i18nSweep.noDescriptionField", {
                    platforms: noDescription.map((o) => o.platform).join(", "),
                  })}
                </p>
              )}
            </div>

            <div className="flex flex-col gap-1">
              <Label htmlFor="golive-category">{t("dash.metaCategory")}</Label>
              <Input
                id="golive-category"
                value={category}
                placeholder={t("dash.categoryPlaceholder")}
                onChange={(e) => setCategory(e.target.value)}
              />
              {categoryHint && (
                <p className="text-sm text-muted-foreground">
                  {categoryHint} {t("i18nSweep.categoryTypeName")}
                </p>
              )}
            </div>

            {tagTargets.length > 0 && (
              <div className="flex flex-col gap-1">
                <Label htmlFor="golive-tags">{t("dash.metaTags")}</Label>
                <Input
                  id="golive-tags"
                  value={tags}
                  placeholder={t("dash.tagsPlaceholder")}
                  onChange={(e) => setTags(e.target.value)}
                />
                <span className="text-sm text-muted-foreground">
                  {t("i18nSweep.tagsReplace", { platforms: tagTargets.map((o) => o.platform).join(", ") })}
                </span>
              </div>
            )}

            {broadcastAccounts.length > 0 && (
              <div className="flex flex-col gap-2 rounded-md border border-border p-3">
                <div className="flex items-center justify-between gap-2">
                  <Label>{t("dash.broadcastSettings")}</Label>
                  <span className="text-sm text-muted-foreground">
                    {t("i18nSweep.platformsOnly", { platforms: broadcastAccounts.map((w) => w.platform).join(", ") })}
                  </span>
                </div>

                <div className="flex flex-col gap-1">
                  <Label htmlFor="golive-start">{t("dash.scheduledStart")}</Label>
                  <Input
                    id="golive-start"
                    type="datetime-local"
                    value={scheduledStart}
                    onChange={(e) => setScheduledStart(e.target.value)}
                  />
                  <span className="text-sm text-muted-foreground">
                    {t("i18nSweep.leaveEmptyKeep")}
                  </span>
                </div>

                {contentDetailsLocked && (
                  <p className="text-sm text-warn">
                    {lockedReason ||
                      t("dash.youtubeStopsAcceptingTheseOnce")}
                  </p>
                )}

                <div className="grid grid-cols-3 gap-2">
                  {[
                    { id: "dvr", label: t("i18nSweep.dvr"), value: dvr, set: setDvr },
                    { id: "autostart", label: t("i18nSweep.autoStart"), value: autoStart, set: setAutoStart },
                    { id: "autostop", label: t("i18nSweep.autoStop"), value: autoStop, set: setAutoStop },
                  ].map((f) => (
                    <div key={f.id} className="flex flex-col gap-1">
                      <Label htmlFor={`golive-${f.id}`}>{f.label}</Label>
                      <select
                        id={`golive-${f.id}`}
                        className="h-9 rounded-md border border-input bg-transparent px-2 text-sm disabled:opacity-50"
                        value={f.value}
                        disabled={contentDetailsLocked}
                        onChange={(e) => f.set(e.target.value)}
                      >
                        {/* "Leave alone" is the DEFAULT and is not the same as
                            "off". An omitted field keeps whatever the platform
                            has; sending false turns the feature off. */}
                        <option value="">{t("dash.leaveUnchanged")}</option>
                        <option value="on">{t("i18nSweep.on")}</option>
                        <option value="off">{t("dash.off")}</option>
                      </select>
                    </div>
                  ))}
                </div>
                <span className="text-sm text-muted-foreground">
            {t("dash.leaveUnchangedNote")}
                </span>
              </div>
            )}

            <div className="flex items-center gap-2">
              <Button size="sm" onClick={push} disabled={empty || busy}>
                <Megaphone /> {busy ? t("dash.pushing") : t("dash.pushToPlatforms")}
              </Button>
              <span className="text-sm text-muted-foreground">
                {t("i18nSweep.appliesTo", {
                  who: accepts("title").length === 1 ? t("dash.theConnectedAccount") : t("dash.everyConnectedAccount"),
                })}
              </span>
            </div>

            {withCompliance.length > 0 && (
              <p className="text-sm text-muted-foreground">
                {t("i18nSweep.pushSendsCompliance", {
                  accounts: withCompliance.map((o) => o.accountName || o.platform).join(", "),
                })}
              </p>
            )}
          </div>

          {/* ---------- what each platform did ---------- */}
          <div aria-live="polite" className="flex flex-col gap-1.5">
            {job === null ? (
              <p className="text-tiny text-muted-foreground">
                {t("i18nSweep.resultsEmpty")}
              </p>
            ) : (
              <>
              {/* Said once, above the rows, because it is about all of them:
                  whatever had not finished when the server lost the job may or
                  may not have reached the platform, and only the platform can
                  say which now. */}
              {jobLost && (
                <p role="status" className="text-sm text-warn">
                  {t("dash.pushStatusLost")}
                </p>
              )}
              {job.results.map((res) => {
                // A row still "pending" on a lost job is not pushing any more:
                // nothing is watching it. Unknown, not a green or a red it has
                // not earned.
                const unknown = jobLost && res.state === "pending";
                const tone = unknown ? "idle" : metaTone[res.state];
                return (
                  <div
                    key={res.accountId}
                    className="flex flex-col gap-0.5 rounded border border-border px-2 py-1.5"
                  >
                    <div className="flex items-center justify-between gap-2">
                      <div className="flex min-w-0 items-center gap-2">
                        <StatusDot tone={tone} size="sm" />
                        <span className="truncate text-tiny font-medium">{res.platform}</span>
                        <span className="truncate font-mono text-micro text-muted-foreground">
                          {res.accountName}
                        </span>
                      </div>
                      <Badge variant={toneBadge[tone]}>
                        {unknown ? t("dash.pushStatusUnknown") : metaLabel[res.state]}
                      </Badge>
                    </div>

                    {res.applied.length > 0 && (
                      <p className="text-sm text-muted-foreground">
                        {t("i18nSweep.pushApplied", { fields: res.applied.join(", ") })}
                        {res.category && ` — ${t("i18nSweep.pushCategory", { category: res.category })}`}
                        {res.target && ` ${t("i18nSweep.pushOnTarget", { target: res.target })}`}
                      </p>
                    )}
                    {res.skipped && res.skipped.length > 0 && (
                      <p className="text-sm text-muted-foreground">
                        {t("i18nSweep.pushNotSupported", { fields: res.skipped.join(", ") })}
                      </p>
                    )}
                    {res.message && (
                      <StatusReason
                        className="text-sm text-down"
                        reason={res.message}
                        step={t("foundation.status.step.metaPush", { platform: res.platform })}
                        asOf={null}
                      />
                    )}
                    {res.warnings?.map((warn) => (
                      <p key={warn} className="text-sm text-warn">
                        {warn}
                      </p>
                    ))}
                  </div>
                );
              })}
              </>
            )}
          </div>
        </CardContent>
      )}
    </Card>
  );
}

// ------------------------------------------------------ bulk start and stop
//
// One control that acts on EVERY destination. An operator with eight of them
// was pressing eight buttons.
//
// There are no per-card checkboxes and no selection state, deliberately: a bulk
// control with a selection is the per-destination control with extra steps, and
// a selection made thirty seconds ago is one more thing that can be stale when
// the button is finally pressed.
//
// WHAT "STOP ALL" DOES TO A YOUTUBE BROADCAST, because the button cannot say it
// and the operator has to know before they press it: on the server, stop and
// disable are ONE thing -- POST /destinations/{id}/stop clears the row's enabled
// column, and internal/api/lifecycle.go ends the broadcast of any destination
// that is disabled. A completed YouTube broadcast cannot return to live.
// Starting everything again puts the video back on the wire; it does not bring
// the broadcasts back, and a new one has to be created or announced. The
// confirmation below therefore names that outcome rather than reassuring the
// operator that they can simply start again.
//
// The confirmation is a plain one -- no requireTyping. That is reserved for the
// irreversible, and this control's own action is not: what it does to the
// PROCESSES is undone by pressing start. The irreversible part is a consequence
// the per-destination stop button already has, one row at a time, so making the
// bulk form harder to complete than eight presses of the control it replaces
// would train the caution out where it matters. The prose carries the warning;
// the friction stays proportional. See ConfirmDestructive's header.
//
// STARTING NEEDS NO CONFIRMATION. Nothing is lost by starting a destination.

// A bulk result is reported per destination, never as one boolean, so each
// outcome needs its own place in the signal language -- the same table the
// metadata composer keeps for the same reason. Tokens only: there is no
// text-ok, and DestinationCard.tsx:302-305 records what a hand-written colour
// class cost.
//
// `skipped` is armed rather than idle because "we never got to this one" is a
// thing still waiting to happen, not a thing at rest.
const bulkTone: Record<BulkDestOutcome, SignalTone> = {
  started: "live",
  stopped: "idle",
  warned: "warn",
  failed: "down",
  skipped: "armed",
};

function BulkDestinationControl({
  destinations,
  sources,
  onFinished,
  openStopAllRef,
}: Readonly<{
  destinations: readonly DestStatus[];
  sources: readonly SourceView[];
  onFinished: () => void;
  /** Filled with this control's Stop all opener, so the on-air strip's End all
   *  (A8) reaches the same dialog rather than a second stop path. */
  openStopAllRef?: MutableRefObject<(() => void) | null>;
}>) {
  const t = useT();
  const [report, setReport] = useState<BulkDestReport | null>(null);
  const [reportAt, setReportAt] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // The sheet's rows, frozen when it opens. See GoLiveSheet.
  const [sheetRows, setSheetRows] = useState<GoLiveRow[] | null>(null);
  const confirmStopAll = useConfirm<true>();
  // The Stop dialog's count comes from the server, the same answer the stop is judged by. It is
  // fetched fresh each time the dialog opens, and until it arrives the dialog cannot be confirmed:
  // a stop whose count is unknown is a stop the operator did not see.
  type StopCountState =
    | { status: "idle" }
    | { status: "loading" }
    | { status: "ready"; youtube: number; destinations: number }
    | { status: "failed"; message: string };
  const [stopCount, setStopCount] = useState<StopCountState>({ status: "idle" });
  const stopCountSeq = useRef(0);
  const count = destinations.length;
  // The Go live sheet still counts YouTube broadcasts locally for its own warning.
  const youtube = youtubeBroadcastsAtRisk(destinations);

  const openStopAll = () => {
    const seq = ++stopCountSeq.current;
    setStopCount({ status: "loading" });
    confirmStopAll.ask(true);
    api.stopImpact().then(
      (impact) => {
        if (seq === stopCountSeq.current) {
          setStopCount({
            status: "ready",
            youtube: impact.youtubeBroadcasts,
            destinations: impact.destinations,
          });
        }
      },
      (err: unknown) => {
        if (seq === stopCountSeq.current) {
          setStopCount({ status: "failed", message: err instanceof Error ? err.message : String(err) });
        }
      },
    );
  };

  useEffect(() => {
    if (!openStopAllRef) return;
    openStopAllRef.current = openStopAll;
    return () => {
      openStopAllRef.current = null;
    };
  });

  const bulkLabel: Record<BulkDestOutcome, string> = {
    started: t("dash.bulkStarted"),
    stopped: t("dash.bulkStopped"),
    warned: t("dash.bulkWarned"),
    failed: t("dash.bulkFailed"),
    skipped: t("dash.bulkSkipped"),
  };

  // STOP ALL ONLY. Starting moved to the Go live sheet, which lists every
  // destination before anything fires and starts each one through the same
  // per-destination path the card uses (see lib/goLive.ts).
  const stopAll = async () => {
    setBusy(true);
    setReport(null);
    try {
      const res = await api.stopAllDestinations();
      setReport(res);
      // The moment this answer arrived: the as-of time for every reason in it.
      setReportAt(new Date().toISOString());
      const done = res.results.filter((r) => r.outcome === "stopped").length;
      const params = { done, total: res.results.length };
      if (done === res.results.length) {
        toast.success(t("dash.bulkStopDone", params));
      } else {
        toast.warning(t("dash.bulkStopPartial", params));
      }
      onFinished();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("dash.bulkStopFailed"));
    } finally {
      setBusy(false);
    }
  };

  if (count === 0) return null;

  // Once the server has answered, the dialog names the server's destination count: that is the set a
  // stop-all touches, and the one the stop is judged by. Until then, the count on screen stands in.
  const shownCount = stopCount.status === "ready" ? stopCount.destinations : count;
  const consequence = t("dash.stopAllConsequence", { count: String(shownCount) });
  const stopDescription =
    stopCount.status === "loading" || stopCount.status === "idle"
      ? t("dash.stopCountLoading")
      : stopCount.status === "failed"
        ? `${t("dash.stopCountFailed")} ${stopCount.message}`
        : stopCount.youtube > 0
          ? `${consequence} ${t("golive.stopYoutube", { count: stopCount.youtube })}`
          : consequence;

  return (
    <div className="mb-3 flex flex-col gap-2 rounded-md border border-border p-2.5">
      <div className="flex flex-wrap items-center gap-2">
        <Button
          size="sm"
          disabled={busy}
          onClick={() => {
            setSheetRows(goLiveRows(destinations, sources));
          }}
        >
          <Play /> {t("golive.trigger")}
        </Button>
        <Button
          size="sm"
          variant="outline"
          disabled={busy}
          onClick={openStopAll}
        >
          <Square /> {busy ? t("dash.bulkStopping") : t("dash.stopAll")}
        </Button>
      </div>

      {/* Results appear only once there are some. An empty "results appear
          here" line sat under the buttons on every visit, and the sheet already
          says what a press is about to do. */}
      {report !== null && (
        <div aria-live="polite" className="flex flex-col gap-1.5">
          {report.results.map((res) => {
            const tone = bulkTone[res.outcome];
            return (
              <div
                key={res.id}
                className="flex flex-col gap-0.5 rounded border border-border px-2 py-1.5"
              >
                <div className="flex items-center justify-between gap-2">
                  <div className="flex min-w-0 items-center gap-2">
                    <StatusDot tone={tone} size="sm" />
                    <span className="truncate text-tiny font-medium">{res.name}</span>
                    <span className="truncate font-mono text-micro text-muted-foreground">
                      {res.platform}
                    </span>
                  </div>
                  <Badge variant={toneBadge[tone]}>{bulkLabel[res.outcome]}</Badge>
                </div>
                {/* WHY, on every row that is not a clean stop. */}
                {res.message && (
                  <StatusReason
                    className={`text-sm ${
                      res.outcome === "failed" ? "text-down" : "text-warn"
                    }`}
                    reason={res.message}
                    step={t("foundation.status.step.stop")}
                    asOf={reportAt}
                  />
                )}
              </div>
            );
          })}
        </div>
      )}

      <GoLiveSheet
        open={sheetRows !== null}
        onOpenChange={(o) => {
          if (!o) setSheetRows(null);
        }}
        rows={sheetRows ?? []}
        live={destinations}
        youtubeAtRisk={youtube}
        onStart={(id) => api.startDestination(id).then(() => undefined)}
        onRetry={(id) => api.restartDestination(id).then(() => undefined)}
        onFinished={onFinished}
      />

      <ConfirmDestructive
        open={confirmStopAll.open}
        onOpenChange={confirmStopAll.onOpenChange}
        // No one target: this stops every running broadcast. The count is
        // in the title and the description; a subject box would have shown
        // the verb "Stop all".
        subject={{ unnamed: true }}
        title={t("dash.stopAllTitle", { count: shownCount })}
        description={stopDescription}
        confirmLabel={t("dash.stopAll")}
        confirmDisabled={stopCount.status !== "ready"}
        onConfirm={() => stopAll()}
      />
    </div>
  );
}

/** A lane's heading (app-council A89): the programme's name, then what the
 *  number counts in words ("9 destinations · 5 live"), and a cue that is not
 *  colour. On air is a filled radio glyph, off air a hollow circle, and the
 *  heading carries data-lane-state for anything reading it without eyes. The
 *  bare "STUDIO A 9" it replaces said neither what was counted nor whether
 *  the lane was live. */
export function LaneHeading({ name, destinations }: { name: string; destinations: readonly DestStatus[] }) {
  const t = useT();
  const live = destinations.filter((d) => d.process?.state === "running").length;
  const onAir = live > 0;
  const Cue = onAir ? Radio : Circle;
  return (
    <h3
      data-lane-state={onAir ? "live" : "idle"}
      className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-sm font-medium text-muted-foreground"
    >
      <Cue
        aria-hidden="true"
        data-lane-cue={onAir ? "on-air" : "off-air"}
        className={cn("h-3.5 w-3.5 shrink-0", onAir ? "text-live" : "text-muted-foreground")}
      />
      <span data-lane-name className="truncate font-semibold uppercase tracking-wide text-foreground">
        {name}
      </span>
      <span className="tnum">
        {destinations.length === 1
          ? t("dash.laneCountsOne", { live })
          : t("dash.laneCounts", { count: destinations.length, live })}
      </span>
    </h3>
  );
}

/** The failover exposure line. Renders nothing when there is nothing to say,
 *  which is the common case on a correctly configured install. */
export function FailoverExposure({ notice }: { notice: FailoverNotice }) {
  const t = useT();
  if (notice.kind === "none") return null;

  // app-council A85: one line, the lead and the link to the setting. The
  // argument for failover (restart, a completed YouTube broadcast cannot return
  // to live, the remux hop) sits behind a closed disclosure. As a two-line
  // paragraph on every visit it pushed the destinations down and read as a
  // fault on a page where nothing was wrong.
  return (
    <div
      className="mb-2 flex items-start gap-2 rounded-md border border-border bg-card px-2.5 py-2 text-sm"
      // Not role="alert". Nothing has gone wrong yet, and announcing a standing
      // configuration fact as an alert on every page load is how a screen-reader
      // user learns to tune the region out.
    >
      <ShieldAlert className="mt-0.5 h-3.5 w-3.5 shrink-0 text-warn" aria-hidden="true" />
      <div className="min-w-0 text-muted-foreground">
        <p>
          <span className="text-foreground">{t("i18nSweep.noFailoverLead")}</span>{" "}
          <Link to="/settings?tab=pipeline#failover" className="underline underline-offset-2">
            {t("i18nSweep.noFailoverLink", { path: `${t("nav.settings")} → ${t("set.failover")}` })}
          </Link>
        </p>
        <details className="mt-1">
          <summary className="cursor-pointer hover:text-foreground">{t("dash.failoverWhy")}</summary>
          <p className="mt-1">{t("i18nSweep.noFailoverBody")}</p>
        </details>
      </div>
    </div>
  );
}

/** What each selector source is called on screen. Keyed by the wire value, so
 *  a kind the server adds later renders its own name rather than crashing the
 *  lookup -- see the fallback at the call site. */
const FAILOVER_LABEL: Record<FailoverSource, TranslationKey> = {
  primary: "dash.failoverPrimary",
  backup: "dash.failoverBackup",
  slate: "dash.failoverSlate",
  playlist: "dash.failoverPlaylist",
  "": "dash.failoverNothing",
};

/* ===========================================================================
   THE WAY BACK FROM A FAILOVER. Issue #768.

   POST /failover/source has been registered since the selector tier shipped and
   had no caller anywhere in this console. On a default install `failover.return`
   is `manual` -- deliberately, because an automatic return flaps and each flap
   is a visible cut -- so the product moved the broadcast to the backup, the
   slate or the playlist when the primary dropped and then offered NOTHING that
   could move it back. The operator found that out mid-broadcast, with the
   standby feed going to the platform and every control on every screen unable
   to end it. That is a one-way door, and it is the whole reason this exists.

   HERE, NOT IN SETTINGS. Settings is where failover is configured, months
   earlier, by someone who is not mid-broadcast. This is the screen an operator
   is already looking at when the switch has happened, so the control belongs
   beside the state that says it happened.

   NOT A PERMANENT ROW. It draws only when the broadcast is off the primary or
   an operator's pin is standing -- i.e. when there is something to undo. The
   failover exposure notice below the destinations makes the same argument at
   length: a banner that is on every install every day is furniture, and
   furniture is what an operator scrolls past on the one day it matters.

   BOTH BUTTONS CONFIRM, and neither is a formality. Returning to the primary
   cuts the feed the platform is receiving right now; handing the choice back
   can move it too, because with no pin a selector sitting on a slate or a
   playlist returns to a live ingest the moment one is available (see
   engine.chooseFrom). An operator who learns "the disruptive things ask" and
   then meets one that does not has had their caution trained out of them
   exactly where it mattered -- which is the argument ConfirmDestructive itself
   is built on.
   =========================================================================== */
export function FailoverControl({
  state,
  manualReturn,
  busy,
  onSwitch,
}: {
  /** The tier's live state, or null when failover is not running -- which is
   *  the default on most installs, and draws nothing. */
  state: FailoverStatus | null;
  /** Whether `failover.return` is `manual`. It comes from the settings read
   *  rather than from the status, because the status does not carry it, and
   *  the sentence this decides is the difference between "nothing will bring
   *  this back" and "something already will". Saying the first on an install
   *  configured for the second would be a false alarm. */
  manualReturn: boolean;
  busy: boolean;
  onSwitch: (to: FailoverPin) => void | Promise<void>;
}) {
  const t = useT();
  const confirm = useConfirm<FailoverPin>();

  if (!state) return null;
  const onPrimary = state.active === "primary";
  // "" is engine's sourceNone: the ABSENCE of a pin, not a pin on nothing.
  const pinned = state.pinned ? state.pinned : null;
  if (onPrimary && !pinned) return null;

  const name = (kind: FailoverSource) =>
    FAILOVER_LABEL[kind] ? t(FAILOVER_LABEL[kind]) : kind;
  const target = confirm.target ?? "primary";
  // Amber for a degraded picture the audience still gets, red for a slate.
  // A pin on the primary has no severity of its own, so it reads as amber.
  const severity = failoverBannerTone(state.active) ?? "warn";
  const since = failoverSinceClock(state.switchedAt);

  return (
    <section
      className={cn(
        "mb-3 flex w-full flex-col gap-2 rounded-md border px-3 py-2.5",
        severity === "down" ? "border-down/40 bg-down-dim" : "border-warn/40 bg-warn-dim",
      )}
      aria-label={t("dash.failoverTitle")}
    >
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
        <ShieldAlert
          className={cn("h-4 w-4 shrink-0", severity === "down" ? "text-down" : "text-warn")}
        />
        {/* THE LIVE REGION IS THE STATE, NOT THE CLOCK. The source and the
            switch time change only when the broadcast changes source, so this
            is the one thing a screen reader should announce. The elapsed
            figure sits outside it and is hidden from assistive tech: a figure
            that ticks every second would be read out every second. */}
        <div aria-live="polite" className="flex flex-wrap items-center gap-x-3 gap-y-1">
          <span className="text-sm font-semibold">
            {t("dash.failoverOnAir", { source: name(state.active) })}
          </span>
          {since && (
            <span className="text-sm text-muted-foreground">
              {t("failover.bannerSince", { time: since })}
            </span>
          )}
        </div>
        <FailoverElapsed switchedAt={state.switchedAt} />
        <Badge variant="outline">
          {pinned ? t("dash.failoverByHand") : t("dash.failoverAutomatic")}
        </Badge>
      </div>

      {/* The server's own sentence for the last move. Rendered verbatim rather
          than re-derived here: it names the cause ("the primary ingest stopped
          delivering") and a paraphrase computed from `active` alone could only
          restate the effect, which is the half already on screen above. */}
      {state.reason && <p className="text-sm text-muted-foreground">{state.reason}</p>}

      <p className="text-sm text-muted-foreground">
        {onPrimary
          ? t("dash.failoverPinnedHere")
          : manualReturn
            ? t("dash.failoverManualHint")
            : t("dash.failoverAutoHint")}
      </p>

      {/* A PIN ON A DEAD PRIMARY IS ACCEPTED AND DOES NOT SWITCH. engine's
          chooseFrom honours a pin only while that source is available, so the
          request answers 200 and the picture does not change until the encoder
          is back. Without this line the operator presses the button, gets a
          success toast, and watches the backup keep going out. */}
      {!onPrimary && !state.primaryLive && (
        <p className="text-sm font-medium text-warn">{t("dash.failoverPrimaryDown")}</p>
      )}

      <div className="flex flex-wrap items-center gap-2">
        {!onPrimary && (
          <Button size="sm" disabled={busy} onClick={() => confirm.ask("primary")}>
            <RotateCcw /> {t("dash.failoverReturn")}
          </Button>
        )}
        {pinned && (
          <Button
            size="sm"
            variant="outline"
            disabled={busy}
            onClick={() => confirm.ask("auto")}
          >
            {t("dash.failoverHandBack")}
          </Button>
        )}
      </div>

      <ConfirmDestructive
        open={confirm.open}
        onOpenChange={confirm.onOpenChange}
        subject={target === "auto" ? t("dash.failoverAutomatic") : name("primary")}
        title={target === "auto" ? t("dash.failoverHandBackTitle") : t("dash.failoverReturnTitle")}
        description={
          target === "auto" ? t("dash.failoverHandBackBody") : t("dash.failoverReturnBody")
        }
        confirmLabel={
          target === "auto" ? t("dash.failoverHandBack") : t("dash.failoverReturn")
        }
        onConfirm={() => onSwitch(target)}
      />
    </section>
  );
}

/** The ticking "for 2m 14s" figure. Its own component so the one-second tick
 *  re-renders this span and nothing else: the Dashboard is 1800 lines and a
 *  tick in the parent would redraw all of them every second. */
function FailoverElapsed({ switchedAt }: { switchedAt: string }) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, []);
  const secs = failoverElapsedSeconds(switchedAt, now);
  if (secs === null) return null;
  return (
    <span aria-hidden="true" className="tnum font-mono text-sm">
      {duration(secs)}
    </span>
  );
}

/** The dashboard's right-hand cards: a stack in one grid cell, or two cells.
 *
 *  A COMPONENT RATHER THAN A TERNARY AROUND THE CHILDREN, because the children
 *  are a couple of hundred lines of JSX and the alternative was writing them
 *  twice. Duplicated markup is how the two arrangements would drift -- a row
 *  added to the pipeline card in one branch and not the other, on a page where
 *  which branch you see depends on how many programmes the install has, and so
 *  is not something the person editing it is likely to be toggling.
 */
function SideColumn({ stacked, children }: { stacked: boolean; children: ReactNode }) {
  if (stacked) return <div className="flex flex-col gap-3">{children}</div>;
  return <>{children}</>;
}

export function Dashboard() {
  const stateLabel = useStateLabel();
  const t = useT();
  const { status, bitrate, snapshotKnown, programme } = useLiveData();
  const [system, setSystem] = useState<SystemInfo | null>(null);
  const [settingsPreview, setSettingsPreview] = useState(true);
  /* The failover settings, for the exposure line above the destinations. Null
   * until the read lands, and failoverNotice() treats that as "say nothing"
   * rather than "unprotected" -- see lib/failoverNotice.ts. */
  const [failover, setFailover] = useState<FailoverSettings | null>(null);
  /* The failover TIER's live state, which is a different thing from the
   * settings above: those are what the operator configured, this is what the
   * selector is doing right now.
   *
   * KEPT RATHER THAN READ STRAIGHT OFF `status`, and the copy is the point. The
   * status socket is install-wide -- every engine publishes onto it and the app
   * keeps one snapshot -- so on a two-programme install `status.failover`
   * alternates between two tiers every couple of seconds. Rendering that
   * directly would flash a control that switches the OTHER programme's source,
   * which is the #497 family of bug in its most expensive form: a switch
   * reports success for the programme the operator named and puts a different
   * show on its slate. `source.id` names whose snapshot this is, so snapshots
   * belonging to another programme are ignored and the last matching one is
   * held -- at most one publish interval stale, which is what every other
   * reading on this page already is. */
  const [failoverState, setFailoverState] = useState<FailoverStatus | null>(null);
  /* Every programme's last-reported source layout, by id, from the same
   * frames. A destination card sizes and names its track chips from ITS
   * programme's layout, which on a lanes install is usually not the selected
   * one. */
  const [frameSources, setFrameSources] = useState<Record<number, SourceInfo>>({});
  const [failoverBusy, setFailoverBusy] = useState(false);
  // The recorder's and the meters' own settings, kept rather than discarded.
  //
  // This page already fetched the whole settings object and threw all but one
  // boolean away, so the pipeline rows had no way to tell a feature the
  // operator switched off from one that is on and not running -- and printed
  // "disabled" for both. `null` until the read lands or when it fails, which
  // processAbsence() renders as no claim at all.
  const [featureEnabled, setFeatureEnabled] = useState<{
    recording: boolean;
    meters: boolean;
  } | null>(null);
  // Per-source preview state. Polled rather than taken from the status feed,
  // which is not source-scoped: every engine publishes onto one broker and the
  // app keeps one status, so a grid fed from it would draw every tile from
  // whichever engine spoke last.
  // Hoisted so the preview grid and the lanes read the SAME poll rather than
  // subscribing twice; usePreviewTiles is what costs an encoder.
  const tiles = usePreviewTiles(settingsPreview);
  const preview = previewLayout(tiles);


  const [dialogOpen, setDialogOpen] = useState(false);
  const [editing, setEditing] = useState<Destination | null>(null);
  const [busyId, setBusyId] = useState<DestinationId | null>(null);
  const [refreshKey, setRefreshKey] = useState(0);
  // The Stop all opener, lent to the on-air strip's End all (A8).
  const endAllRef = useRef<(() => void) | null>(null);

  // The programmes, so a destination card can say which one it carries. Named
  // only when there is more than one: on a single-source install every card
  // would carry the same badge, which is noise that tells nobody anything.
  const [sources, setSources] = useState<SourceView[]>([]);
  useEffect(() => {
    api.listSources().then(setSources).catch(() => setSources([]));
  }, [refreshKey]);

  const [pending, setPending] = useState<DestinationId[] | null>(null);
  const [moveNote, setMoveNote] = useState("");
  const [sourceCount, setSourceCount] = useState<number | null>(null);
  /* The selected programme's status frame, its relay throughput and whether it
   * is live, held the same way and for the same reason as failoverState: the
   * socket alternates between every engine's frames and the `stats` bitrate
   * series names no programme, so the Ingest card printed whichever one spoke
   * last. The header chip reads the same hook, so the two cannot disagree.
   * See useProgrammeIngest. */
  const {
    snapshot: programmeStatus,
    kbps: programmeKbps,
    live: ingestLive,
  } = useProgrammeIngest(sourceCount);

  // How many programmes exist, which is what decides whether this page shows a
  // pipeline or an empty state.
  //
  // `null` means NOT KNOWN, and the branch below tests `=== 0` rather than
  // falsiness precisely so that unknown renders the ordinary dashboard. The
  // cost is that a fresh install shows the pipeline for the length of one
  // request before the empty state replaces it; the alternative is a spinner in
  // front of the main screen on every load of every install, for a question
  // only one install in a thousand answers differently.
  //
  // A count that cannot be read leaves the page rendering as it always did. The
  // empty state is an improvement on a working dashboard, never a replacement
  // for one, and drawing it because a request failed would tell an operator
  // mid-broadcast that their programme is gone.
  const readSourceCount = useCallback(async (): Promise<number | null> => {
    try {
      const { sources } = await api.setupStatus();
      const n = sources ?? null;
      setSourceCount(n);
      return n;
    } catch {
      setSourceCount(null);
      return null;
    }
  }, []);

  useEffect(() => {
    api
      .getSettings()
      .then((s) => {
        setSettingsPreview(s.preview.enabled);
        setFeatureEnabled({ recording: s.recording.enabled, meters: s.meters.enabled });
        setFailover(s.failover ?? null);
      })
      .catch(() => {
        setFeatureEnabled(null);
        // Left null on a failed read, so the notice stays silent rather than
        // claiming an exposure it could not verify.
        setFailover(null);
      });
    readSourceCount();
    // AND KEEP READING IT. The count used to be read once per refreshKey, and
    // every control that bumps refreshKey is inside the pipeline this page
    // stops drawing the moment the count reaches zero -- so a dashboard left
    // open while the last source went away kept rendering a publish URL, a
    // track count and a relay subscriber for a programme that no longer
    // existed, with nothing on the page able to notice. The status socket
    // cannot rescue it either: status events are published BY an engine, so an
    // install with none simply stops sending them and the last frame stays on
    // screen for ever.
    //
    // The setup status rather than the source list: it is the one endpoint
    // whose entire job is this number, it is a single row count, and it is what
    // the empty state was given a count for in the first place.
    const poll = setInterval(readSourceCount, 10_000);
    return () => clearInterval(poll);
  }, [refreshKey, readSourceCount]);

  // See the failoverState declaration for why this filters by programme rather
  // than reading the snapshot straight through. `?? null` matters as much as
  // the filter: the field is omitted entirely once the tier stops, so a
  // snapshot without it has to CLEAR the state rather than leave the last one
  // standing -- otherwise switching failover off in Settings would leave a live
  // control on the dashboard aimed at a tier that no longer exists.
  // The publish address on the Ingest card is the SELECTED programme's. An
  // unnamed /system answers for the default source, so on a multi-programme
  // install an operator looking at Studio B copied Studio A's address.
  useEffect(() => {
    let live = true;
    api
      .system(programme)
      .then((s) => {
        if (live) setSystem(s);
      })
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [programme, refreshKey]);

  useEffect(() => {
    const src = status?.source;
    if (src?.id == null) return;
    setFrameSources((prev) => (prev[src.id] === src ? prev : { ...prev, [src.id]: src }));
  }, [status]);
  const sourceOf = (id: number | null | undefined): SourceInfo | null =>
    id == null ? null : (frameSources[id] ?? null);

  useEffect(() => {
    if (!status || programme == null) return;
    if (status.source?.id !== programme) return;
    setFailoverState(status.failover ?? null);
  }, [status, programme]);

  const switchSource = useCallback(
    async (to: FailoverPin) => {
      setFailoverBusy(true);
      try {
        // The ANSWER is stored, not an assumption: a pin on a source that is
        // not delivering is accepted and changes nothing until it is, so
        // guessing the new state here would show a switch that has not
        // happened. See api.switchSource.
        setFailoverState(await api.switchSource(to, programme));
        toast.success(to === "auto" ? t("dash.failoverHandedBack") : t("dash.failoverSwitched"));
      } catch (err) {
        // Every refusal here is this configuration saying it cannot do what was
        // asked -- failover not running, a source this install has not enabled.
        // The server's sentence says which, so it is shown rather than replaced.
        toast.error(err instanceof Error ? err.message : t("dash.failoverFailed"));
      } finally {
        setFailoverBusy(false);
      }
    },
    [programme, t],
  );

  const act = useCallback(
    async (id: DestinationId, fn: () => Promise<unknown>, label: string) => {
      setBusyId(id);
      try {
        await fn();
      } catch (err) {
        // THE RED TOAST THIS EXISTS TO REPLACE. Every destination control on
        // this page is behind requireSource, so on an install whose last source
        // has just gone the answer is a 503 -- and "Could not start the
        // destination" over a scarlet background is a fault report for a state
        // in which nothing is wrong and nothing is broken.
        //
        // RE-READ THE COUNT, DO NOT ASSUME IT, and the difference is a whole
        // screen. requireSource refuses on "no engine is running", which is
        // also the state of an install that HAS sources and whose engines all
        // failed to build or start -- manager.go logs and continues, per
        // source. Setting the count to 0 from this branch replaced that
        // operator's entire dashboard with "create a source in the web UI",
        // with a button to a Sources page listing the source they already have,
        // and no way back: nothing left on screen bumps refreshKey. The real
        // fault, an engine that did not come up, was then concealed by a screen
        // asserting the opposite.
        //
        // So the refusal is a prompt to ask, not an answer. Zero draws the
        // empty state; anything else says the true thing.
        if (isNoSource(err)) {
          const n = await readSourceCount();
          if (n !== 0) toast.error(t("dash.noEngineRunning"));
          return;
        }
        toast.error(err instanceof Error ? err.message : `Could not ${label}.`);
      } finally {
        setBusyId(null);
      }
    },
    [readSourceCount, t],
  );

  const openEdit = async (id: DestinationId) => {
    try {
      const { destination } = await api.getDestination(id);
      setEditing(destination);
      setDialogOpen(true);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("dash.couldNotLoadTheDestination"));
    }
  };

  const confirmDelete = useConfirm<{ id: DestinationId; name: string; live: boolean }>();
  // What still points at the destination being deleted (A18).
  const deleteUsage = useDestinationUsage(confirmDelete.open ? (confirmDelete.target?.id ?? null) : null);

  const remove = async (id: DestinationId) => {
    await act(id, () => api.deleteDestination(id), "delete the destination");
    toast.success(t("dash.destDeleted"));
    setRefreshKey((k) => k + 1);
  };

  // The SELECTED programme's ingest, not the last engine to publish. See the
  // useProgrammeIngest call.
  const ingest = programmeStatus?.ingest;
  const source = programmeStatus?.source;
  // BYTES ARRIVING, not a process running -- the same question AppLayout's
  // header got wrong until it was fixed, asked a second time here. For SRT the
  // answer is inverted rather than imprecise: engine.reconcileIngest returns
  // early for SRT on purpose, so `status.ingest` is null on a HEALTHY install,
  // stateLabel(undefined) is "Offline", and this card reported the ingest down
  // over three live programmes in a demo capture. useIngestLive is the app's
  // one definition of "a broadcast is going out"; ingest?.state is only the
  // process, consulted below for the bitrate where it exists. See #514.
  // Narrowed to the selected programme where the install has more than one:
  // the byte series behind useIngestLive names no programme, so there it is
  // this programme's own probe and relay bytes. See programmeIngestLive();
  // `ingestLive` is useProgrammeIngest's, the same verdict the header chip uses.
  // While the standby is on air the primary's "Offline" is true and misleading:
  // the show is going out. Say so instead of leaving a bare OFFLINE beside the
  // on-air banner. `failoverState` is the programme-scoped snapshot.
  const onBackup = backupOnAir(failoverState);
  const ingestTone = onBackup ? "warn" : ingestLive ? "live" : toneForState(ingest?.state);
  // The primary's failure stays visible here while the backup is on air: this
  // card is about the primary, and the reason it fell over is what the
  // operator needs to fix it. The header chip, which speaks for the show,
  // hides it in that case.
  const ingestReason = ingestProblem(ingest, ingestLive, false);
  // The word a pipeline row uses when it has no process. "—" for unknown: a
  // settings read that has not landed is not a fact about the feature.
  const absentLabel = (enabled: boolean | null | undefined) => {
    const verdict = processAbsence(enabled);
    if (verdict === "disabled") return t("dash.disabled");
    if (verdict === "idle") return t("dash.idle");
    return "—";
  };
  // Whose figures the Ingest card is showing, on an install where that is
  // genuinely ambiguous. See ingestAttribution().
  const attribution = ingestAttribution(sourceCount, source);
  const live = status?.destinations;
  // The Pipeline card's figures are the SELECTED programme's, like the Ingest
  // card's: recorder, preview, meters, relay subscribers and the rendition
  // ladder are all per engine, and the socket's last frame may be another
  // programme's. See programmeSnapshot().
  const renditions = programmeStatus?.renditions ?? [];

  // The socket republishes status on a two-second cadence, so a move applied
  // only on the server would leave the card sitting still after the click.
  // Hold the requested order locally until the server's own order agrees.
  const destinations = useMemo(() => {
    const rows = live ?? [];
    if (!pending) return rows;
    const rank = new Map(pending.map((id, i) => [id, i]));
    return [...rows].sort(
      (a, b) =>
        (rank.get(a.id) ?? Number.MAX_SAFE_INTEGER) - (rank.get(b.id) ?? Number.MAX_SAFE_INTEGER),
    );
  }, [live, pending]);

  // How the destination area divides. One programme means no headings at all,
  // which is the shape this page had before per-source anything.
  // The whole dashboard, divided by programme. Composed from the two layout
  // functions rather than restating their rules -- see sourceLanes.ts.
  const lanes = useMemo(
    () => laneLayout(tiles, destinations, sources),
    [tiles, destinations, sources],
  );

  /* The top row's shape, from one call so its two halves cannot disagree.
   * See lib/dashboardLayout.ts and #614. */
  const topRow = topRowLayout(lanes.laned);

  /* The first-run checklist, shown in place of the "no destinations" sentence
   * when a source exists and nothing is configured to send it anywhere. Every
   * tick is derived from what the server reports now (lib/setupChecklist), so
   * an operator who loses their source or encoder sees the list reopen rather
   * than a stored "done" over an empty page. Null while the source count has
   * not arrived: that is drawn as the plain card, never as a guessed checklist. */
  const checklist = setupChecklist({
    sourceCount,
    signal: ingestLive,
    destinations: destinations.length,
  });

  /* ONE CARD, rendered from the GLOBAL index rather than a per-group one.
   *
   * The move arrows reorder the whole list and the order is persisted whole, so
   * a card's neighbours are its neighbours in `destinations`, not in the group
   * it happens to be drawn under. Computing the index within a group would grey
   * out "move up" at the top of every heading while the move itself is still
   * possible -- a disabled control that is lying about why. */
  const renderDestination = (d: DestStatus, i: number) => (
              <DestinationCard
                key={d.id}
                dest={d}
                /* The anchor the attention list at the top of the page sends the
                 * operator to. Same id whichever branch below draws the card —
                 * lane, grid or orphan section — because the summary has no way
                 * to know which one a given destination landed in. */
                domId={`dest-${d.id}`}
                /* Names for the audio chips' tooltips, from THIS card's
                 * programme's last frame (frameSources), and null until that
                 * programme has reported. See lib/trackLabels.ts for why a
                 * wrong name is worse here than no name. */
                trackLabels={trackLabels(sourceOf(d.sourceId), d.sourceId)}
                /* One chip per track of THIS card's programme's ingest; null
                 * (no invented chips) until that programme has reported. */
                trackCount={trackCount(sourceOf(d.sourceId), d.sourceId)}
                busy={busyId === d.id}
                canMoveEarlier={i > 0}
                canMoveLater={i < destinations.length - 1}
                onMoveEarlier={() => move(d.id, -1)}
                onMoveLater={() => move(d.id, 1)}
                onStart={() => act(d.id, () => api.startDestination(d.id), "start the destination")}
                onStop={() => act(d.id, () => api.stopDestination(d.id), "stop the destination")}
                onStopNow={
                  (d.delaySeconds ?? 0) > 0 || d.delay
                    ? () => act(d.id, () => api.stopDestination(d.id, true), "cut the delayed destination")
                    : undefined
                }
                onRestart={() =>
                  act(d.id, () => api.restartDestination(d.id), "restart the destination")
                }
                onEdit={() => openEdit(d.id)}
                onDelete={() =>
                  confirmDelete.ask({
                    id: d.id,
                    name: d.name,
                    live: d.process?.state === "running",
                  })
                }
                onRefreshKey={async () => {
                  await act(
                    d.id,
                    async () => {
                      await api.refreshStreamKey(d.id);
                      toast.success(t("dash.keyRefreshed"));
                    },
                    "refresh the stream key",
                  );
                }}
                /* Handed over only for Facebook, so the card's kebab is given
                 * nothing to show on the platforms that have no broadcast to
                 * end — the gate is the platform, not a disabled item. */
                onEndBroadcast={
                  d.platform === "facebook"
                    ? async () => {
                        await act(
                          d.id,
                          async () => {
                            const r = await api.endFacebookBroadcast(d.id);
                            /* THREE OUTCOMES, NOT TWO, and the middle one is
                             * why this is not `r.ended ? success : error`.
                             * EndBroadcast reports Ended only when Facebook
                             * read the status back as VOD; a false with no
                             * error means the POST SUCCEEDED and the node has
                             * not settled yet. Facebook documents that it
                             * saves the VOD, not how fast — so reporting that
                             * as a failure would send an operator to end a
                             * broadcast that is already ending, and reporting
                             * it as a clean success would claim a confirmation
                             * nobody gave. It gets its own sentence. */
                            if (r.ended) toast.success(t("dash.broadcastEnded"));
                            else toast.info(t("dash.broadcastEndAccepted"));
                            /* Warnings name what was actually seen — a
                             * read-back that failed, a status that is still
                             * LIVE. Advisory beside the sentence above, not
                             * errors: the end was accepted either way. */
                            for (const w of r.warnings ?? []) toast.warning(w);
                          },
                          "end the Facebook broadcast",
                        );
                        setRefreshKey((k) => k + 1);
                      }
                    : undefined
                }
              />
  );

  // The override is only worth keeping while the server still lists the same
  // destinations in a different order. Once it agrees — or once one is added or
  // deleted underneath it — it can only mis-sort, so drop it.
  useEffect(() => {
    if (!pending || !live) return;
    const ids = live.map((d) => d.id);
    const awaitingServer =
      ids.length === pending.length &&
      ids.every((id) => pending.includes(id)) &&
      ids.some((id, i) => id !== pending[i]);
    if (!awaitingServer) setPending(null);
  }, [live, pending]);

  const move = async (id: DestinationId, delta: -1 | 1) => {
    const ids = destinations.map((d) => d.id);
    const from = ids.indexOf(id);
    const to = from + delta;
    if (from < 0 || to < 0 || to >= ids.length) return;
    [ids[from], ids[to]] = [ids[to], ids[from]];

    setPending(ids);
    setMoveNote(`${destinations[from].name} moved to position ${to + 1} of ${ids.length}.`);
    try {
      await api.reorderDestinations(ids);
    } catch (err) {
      setPending(null);
      setMoveNote("");
      toast.error(err instanceof Error ? err.message : t("dash.couldNotReorderTheDestinations"));
    }
  };

  // A1: the selected programme's publish address for the checklist, host
  // filled in. In RTMP mode the server sends the stream key as its own row
  // (OBS wants server and key in two boxes, and the address alone reaches
  // nothing), so it is offered as its own copy -- copied, never displayed.
  const checklistSource = sources.find((x) => x.id === programme) ?? sources[0];
  const checklistAddresses = checklistSource
    ? publishRows(checklistSource.publishUrls, window.location.hostname)
    : [];

  const copyIngest = async () => {
    if (!system?.ingestUrl) return;
    try {
      await navigator.clipboard.writeText(system.ingestUrl);
      toast.success(t("dash.ingestUrlCopied"));
    } catch {
      toast.error(t("dash.clipboardUnavailable"));
    }
  };

  // NO PROGRAMME YET. The whole page, not a strip above it.
  //
  // Everything below this line is about one: a preview of a stream nobody is
  // sending, an ingest URL no encoder can publish to, a pipeline of processes
  // that do not exist, and destination controls that answer 503. Rendering it
  // over an empty install is not merely unhelpful -- it invites the operator to
  // click the one button that cannot work, which is how they met the red toast
  // in the first place.
  //
  // `=== 0` rather than `!sourceCount`, deliberately: null means the count has
  // not arrived, and an operator mid-broadcast must never see their dashboard
  // replaced because a request was slow.
  if (sourceCount === 0) {
    return (
      <div className="p-3">
        <PageHeader title={t("dash.title")} subtitle={t("dash.subtitle")} />
        <NoProgrammeYet title={t("empty.dashTitle")} body={t("empty.dashBody")} />
        {/* The same checklist as a configured install, starting at step one. The
         * explanation above is kept, because it is the one place that says what
         * a programme is. The checklist is the state, and it is what reopens
         * when the last source is removed. */}
        {checklist && (
          <div className="mt-3">
            <SetupChecklist
              checklist={checklist}
              addresses={checklistAddresses}
              onCopyAddress={(a) => copyToClipboard(t, a.url, a.proto === "streamKey" ? t("sources.streamKey") : a.proto.toUpperCase())}
              ingestUrl={system?.ingestUrl ?? ""}
              onCopyIngest={copyIngest}
              onAddDestination={() => {
                setEditing(null);
                setDialogOpen(true);
              }}
            />
          </div>
        )}
      </div>
    );
  }

  return (
    <div className="p-3">
      <PageHeader
        title={t("dash.title")}
        subtitle={t("dash.subtitle")}
        actions={
          <Button
            size="sm"
            // The onboarding tour's step 4. This button, and not the one in the
            // empty state below, because the header renders whatever the
            // destination count is — the empty-state twin disappears the moment
            // the operator adds their first destination, which is precisely
            // when somebody replaying the tour from Settings would be looking
            // for it. See ui/src/lib/tourSteps.ts.
            data-tour="add-destination"
            // Outline, not filled: Go live is the one filled start action on this
            // page, and Add destination is a secondary one (A9).
            variant="outline"
            onClick={() => {
              setEditing(null);
              setDialogOpen(true);
            }}
          >
            <Plus /> {t("firstRun.addDestination")}
          </Button>
        }
      />

      {/* TIER ONE, above everything this page used to open with. What is on
          air, then what is wrong; the preview, the ingest card and the
          destination grid below are the detail behind those two answers rather
          than a flat inventory of the install. See components/OnAirBar.tsx. */}
      <OnAirBar
        status={status}
        failover={failoverState}
        onEndAll={() => endAllRef.current?.()}
        ingestReceiving={ingestLive}
      />

      {/* THE WAY BACK, directly under what is on air. See FailoverControl: it
          renders nothing at all unless this broadcast is off its primary ingest
          or a manual pin is standing. */}
      <FailoverControl
        state={failoverState}
        manualReturn={failover?.return !== "auto"}
        busy={failoverBusy}
        onSwitch={switchSource}
      />

      {/* TWO COLUMNS WITH A PREVIEW, THREE WITHOUT.
       *
       * The left column's tallest element is the preview, and lanes suppress it
       * (see the Suspense block below) because each lane carries its own. That
       * left the column holding nothing but the Ingest card while the right one
       * still stacked chat above the pipeline -- a grid row as tall as the
       * taller side, and roughly four hundred pixels of empty page directly
       * under the card an operator looks at first. It read as a component that
       * had failed to render. See #614.
       *
       * Unstacking the side column is the fix rather than filling the hole:
       * chat and the pipeline are both short, so side by side they make a row
       * about half the height the stack did. The dead space goes away AND the
       * destination area -- the thing lanes exist to organise -- moves up the
       * page instead of further down it.
       */}
      <div className={cn("grid gap-3", topRow.gridClass)}>
        {/* ---------- preview + ingest ---------- */}
        <div className="flex flex-col gap-3">
          <LazyBoundary
            fallback={
              // Same max-h as PreviewPlayer, and it has to be: this stands in for
              // it before a stream arrives, so a taller box here would make the
              // layout jump the moment one does.
              <div className="aspect-video max-h-[60vh] w-full rounded-md border border-border bg-black" />
            }
          >
            {/* ONE TILE PER SOURCE. The preview used to be a single player on
                whichever source happened to be first in display order, with no
                way to reach any other. A tile costs an encoder only while it is
                watched AND something is on air, so offline programmes here cost
                nothing.

                Which tiles those are, and what each one plays, is decided by
                previewLayout() in src/lib -- a plain function of the polled
                telemetry, so the decision is testable without a page test. */}
            {lanes.laned ? null : preview.grid ? (
              <div className="grid gap-3 sm:grid-cols-2">
                {preview.panes.map((pane) => (
                  <figure key={pane.id} className="flex flex-col gap-1.5">
                    <PreviewPlayer active={settingsPreview} {...pane.player} />
                    <figcaption className="flex items-center gap-2 text-tiny text-muted-foreground">
                      <span className="truncate">{pane.label}</span>
                    </figcaption>
                  </figure>
                ))}
              </div>
            ) : (
              <PreviewPlayer
                active={settingsPreview}
                {...preview.panes[0].player}
              />
            )}
          </LazyBoundary>

          <Card>
            <CardHeader className="flex-row items-center justify-between">
              <CardTitle className="flex items-center gap-2">
                <StatusDot tone={ingestTone} />
                {t("chrome.ingest")}
                {/* WHOSE INGEST. Only on an install with more than one
                    programme, where the answer is not obvious. The figures
                    are the selected programme's (programmeSnapshot), so this
                    names the programme in the header. */}
                {attribution && (
                  <Badge variant="outline" className="max-w-40 truncate">
                    {attribution.name}
                  </Badge>
                )}
              </CardTitle>
              <Badge variant={toneBadge[ingestTone]}>
                {onBackup
                  ? t("dash.ingestBackupOnAir")
                  : ingestLive
                    ? stateLabel("running")
                    : stateLabel(ingest?.state)}
              </Badge>
            </CardHeader>
            <CardContent className="flex flex-col gap-3">
              {onBackup && failoverState?.primaryLive !== true && (
                <p className="rounded border border-warn/40 bg-warn/10 px-2 py-1 text-sm leading-relaxed text-warn">
                  {t("dash.ingestPrimaryOffline")}
                </p>
              )}
              <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
                <Stat
                  labelKey="dash.bitrate"
                  value={(() => {
                    // See ingestBitrateKbps: for SRT there is no ingest
                    // process, so this asked the wrong source and printed "—"
                    // on a healthy install. null still renders "—", but now it
                    // means "nothing is arriving" rather than "nobody asked
                    // the relay".
                    const v = ingestBitrateKbps(
                      ingest?.state,
                      ingest?.progress?.bitrateKbps,
                      attributableBitrate(sourceCount, bitrate, programmeKbps),
                      ingestLive,
                    );
                    return v === null ? "—" : kbps(v);
                  })()}
                />
                <Stat
                  labelKey="dash.uptime"
                  value={(() => {
                    // A push on the shared SRT listener has no ingest
                    // process to time; its uptime is how long its stream has
                    // been arriving at this programme's hub. See
                    // ingestUptimeSec.
                    const v = ingestUptimeSec(ingest, programmeStatus?.relay, ingestLive);
                    return v === null ? "—" : duration(v);
                  })()}
                />
                {/* "—" until the probe answers, not "0". See audioTrackCount(). */}
                <Stat labelKey="dash.audioTracks" value={audioTrackCount(source)} />
                <Stat
                  labelKey="dash.reconnects"
                  value={ingest?.restarts ?? 0}
                  tone={(ingest?.restarts ?? 0) > 0 ? "warn" : "muted"}
                />
              </div>

              {source?.video && (
                <div className="font-mono text-sm text-muted-foreground">
                  {source.video.codec} {source.video.width}×{source.video.height}
                  {source.video.frameRate > 0 && ` @ ${source.video.frameRate.toFixed(2)}fps`}
                </div>
              )}

              <div className="flex items-center gap-2 rounded border border-border bg-background px-2 py-1.5">
                <Radio className="h-3 w-3 shrink-0 text-muted-foreground" />
                {urlCarriesCredential(system?.ingestUrl ?? "") ? (
                  <SecretCode value={system?.ingestUrl ?? ""} />
                ) : (
                  <code className="min-w-0 flex-1 truncate font-mono text-sm text-muted-foreground">
                    {system?.ingestUrl ?? "…"}
                  </code>
                )}
                <Button variant="ghost" size="icon-sm" onClick={copyIngest} aria-label={t("dash.copyIngestUrl")}>
                  <Copy />
                </Button>
              </div>

              {/* B1: the reason, the failing step and the as-of line. Gated by
                  ingestProblem, the same rule the header chip uses, so a stale
                  lastError never sits beside a stream that has recovered. */}
              {ingestReason && (
                <div className="rounded border border-down/30 bg-down-dim px-2 py-1 text-sm text-down">
                  <StatusReason
                    reason={ingestReason}
                    step={t("foundation.status.step.ingest")}
                    asOf={null}
                  />
                </div>
              )}

              {system && !system.ffmpeg.hasLibsrt && (
                <div className="rounded border border-warn/30 bg-warn-dim px-2 py-1 text-sm text-warn">
                  {t("dash.noLibsrt", { flag: "--enable-libsrt" })}
                </div>
              )}
            </CardContent>
          </Card>
        </div>

        {/* ---------- side stats ---------- */}
        {/* A FRAGMENT WHEN LANED, so the two cards become grid children in
            their own columns rather than a stack in one. Chat still comes
            first: reading order is unchanged, and it is the only thing on this
            side an operator reads mid-broadcast. It renders honestly when
            nothing is connected — "no platforms connected", not an error — so
            it costs an install with no accounts nothing but a heading. */}
        <SideColumn stacked={topRow.sideStacked}>
          <ChatPanel className="h-80" />

          <Card>
            <CardHeader>
              <CardTitle>{t("dash.pipeline")}</CardTitle>
            </CardHeader>
            <CardContent className="flex flex-col gap-1.5">
              {/* WHAT "no process" MEANS, per row, from the settings this page
                  already fetches.
                  A WARNING, not a control -- nothing here can stop the engine
                  dropping the recorder on a full disk, and the point is that
                  the row stops blaming the operator for it. absentLabel() is
                  processAbsence() in lib/dashboardFacts.ts: on when the feature
                  is enabled and merely not running, off when it is not, and
                  silence when the settings read has not landed. */}
              {(
                [
                  ["Recorder", programmeStatus?.recorder, absentLabel(featureEnabled?.recording)],
                  // The preview encoder is started on demand and stopped again
                  // when nobody is watching, so having no process is the normal
                  // idle state rather than a fault or a disabled feature.
                  ["Preview", programmeStatus?.preview, settingsPreview ? t("dash.idle") : t("dash.disabled")],
                  ["Meters", programmeStatus?.meters, absentLabel(featureEnabled?.meters)],
                ] as const
              ).map(([label, proc, absent]) => {
                const tone = proc ? toneForState(proc.state) : "idle";
                return (
                  <div key={label} className="flex items-center justify-between">
                    <div className="flex items-center gap-2">
                      <StatusDot tone={tone} size="sm" />
                      <span className="text-tiny">{label}</span>
                    </div>
                    <span className="font-mono text-sm text-muted-foreground">
                      {proc ? stateLabel(proc.state) : absent}
                    </span>
                  </div>
                );
              })}
              <div className="mt-1 flex items-center justify-between border-t border-border pt-1.5">
                <span className="text-tiny text-muted-foreground">{t("dash.relaySubscribers")}</span>
                <span className="tnum font-mono text-micro">
                  {/* Not `?? 0`: before the first snapshot there is no
                      subscriber count, and zero subscribers is a meaningful,
                      alarming number to show someone who is broadcasting. #663. */}
                  {snapshotKnown && programmeStatus ? (programmeStatus.relay?.subscribers?.length ?? 0) : "—"}
                </span>
              </div>

              {/* The shared encode tier, with the ref count that decides
                  whether each one runs. A rendition nobody enabled has no
                  process on purpose, so it reads as idle rather than as a
                  fault, and the destination count is the whole economic
                  argument: three platforms on one tier is still one encode. */}
              <div className="mt-1 flex flex-col gap-1 border-t border-border pt-1.5">
                <span className="text-tiny text-muted-foreground">{t("dash.renditions")}</span>
                {renditions.length === 0 ? (
                  <span className="font-mono text-sm text-muted-foreground">
                    {t("i18nSweep.noRenditions")}
                  </span>
                ) : (
                  renditions.map((r) => (
                    <div key={r.id} className="flex items-center justify-between gap-2">
                      <div className="flex min-w-0 items-center gap-2">
                        <StatusDot
                          tone={r.consumers === 0 ? "idle" : toneForState(r.process?.state)}
                          size="sm"
                        />
                        <span className="truncate text-tiny">{r.name}</span>
                      </div>
                      <span className="tnum shrink-0 font-mono text-sm text-muted-foreground">
                        {r.consumers === 1 ? t("i18nSweep.destsOne") : t("i18nSweep.destsMany", { count: r.consumers })}
                      </span>
                    </div>
                  ))
                )}
              </div>
            </CardContent>
          </Card>
        </SideColumn>
      </div>

      <GoLiveComposer />

      {/* ---------- destinations ---------- */}
      <div className="mt-4">
        <div className="mb-2 flex items-center justify-between">
          <h2 className="text-[13px] font-semibold tracking-tight">
            {t("route.destinations")}
            <span className="ml-1.5 font-mono text-tiny font-normal text-muted-foreground">
              {destinations.length}
            </span>
          </h2>
        </div>

        {/* THE ONE UNRECOVERABLE FAILURE, said where the destinations it would
            cost are listed. See lib/failoverNotice.ts and #512.

            Here rather than in the attention panel: that panel lists what is
            wrong NOW, and its own comment explains that padding it with states
            somebody chose is how it becomes something an operator scrolls past.
            A permanent row on every install that has not enabled failover is
            exactly that.

            No dismiss button. A banner with an X depends on the operator
            remembering, later, something they closed while busy -- rung zero.
            This clears itself when the setting changes and returns if it
            changes back, so what is on screen is the current exposure rather
            than a record of who clicked what. */}
        <FailoverExposure
          notice={failoverNotice(
            destinations.filter((d) => d.enabled).length,
            failover,
          )}
        />

        {/* Beside the list it acts on, not in the header row: the outcome list
            below the buttons needs the full width to put a destination name and
            a refusal on one line. */}
        <BulkDestinationControl
          destinations={destinations}
          sources={sources}
          onFinished={() => setRefreshKey((k) => k + 1)}
          openStopAllRef={endAllRef}
        />

        {/* A card that jumps position is invisible to a screen reader unless
            the move is announced. */}
        <output aria-live="polite" className="sr-only">
          {moveNote}
        </output>

        {/* Once, above the grid, because the hold is all-or-nothing by
            construction: a held pass plans no destination at all. */}
        <DestinationHoldNote hold={status?.destinationHold} />

        {/* snapshotKnown, not `destinations.length === 0` alone. Before the
            first snapshot arrives the list is empty because nothing has been
            said yet, and this card states the opposite: that the install HAS
            no destinations, with a button to add one. An operator refreshing
            during a slow connect saw that page, then saw the hold note replace
            it -- two different answers to "what is configured here" seconds
            apart. Waiting is the honest render; it is a fact we do not have
            yet, not a fact that is empty. #663. */}
        {!snapshotKnown ? (
          <Card>
            <CardContent className="py-8 text-center">
              <p className="text-sm text-muted-foreground">{t("dash.loadingDestinations")}</p>
            </CardContent>
          </Card>
        ) : destinations.length === 0 && checklist ? (
          <SetupChecklist
            checklist={checklist}
            addresses={checklistAddresses}
            onCopyAddress={(a) => copyToClipboard(t, a.url, a.proto === "streamKey" ? t("sources.streamKey") : a.proto.toUpperCase())}
            ingestUrl={system?.ingestUrl ?? ""}
            onCopyIngest={copyIngest}
            onAddDestination={() => {
              setEditing(null);
              setDialogOpen(true);
            }}
          />
        ) : destinations.length === 0 ? (
          <Card>
            <CardContent className="flex flex-col items-center gap-2 py-8 text-center">
              <p className="text-sm text-muted-foreground">
            {t("dash.noDestinations")}
              </p>
              <Button
                size="sm"
                onClick={() => {
                  setEditing(null);
                  setDialogOpen(true);
                }}
              >
                <Plus /> {t("firstRun.addDestination")}
              </Button>
            </CardContent>
          </Card>
        ) : (
          <div className="flex flex-col gap-5">
            {lanes.laned ? (
              /* ONE LANE PER PROGRAMME: its picture and the destinations that
                 carry it, in the same box.
              
                 Headings answered "which programme is this card on?" by name.
                 A lane answers "is THIS one on air, and where is it going?" by
                 position, which is the stronger form -- there is no pair of
                 labels for a reader to match up, and no way for them to
                 disagree. laneLayout pins that a lane can never hold another
                 programme's pane.
              
                 THE PLAYER MOVES HERE RATHER THAN BEING COPIED. A tile costs an
                 encoder while it is watched and something is on air, so drawing
                 the same source twice would cost twice. The preview area above
                 renders nothing while lanes are on. */
              lanes.lanes.map((lane) => (
                <section key={lane.sourceId} className="flex flex-col gap-2">
                  <LaneHeading name={lane.name} destinations={lane.destinations} />
                  <div className="grid gap-3 lg:grid-cols-3">
                    <LazyBoundary
                      fallback={
                        <div className="aspect-video w-full rounded-md border border-border bg-black" />
                      }
                    >
                      {lane.pane ? (
                        <PreviewPlayer active={settingsPreview} {...lane.pane.player} />
                      ) : (
                        /* A lane with no telemetry keeps its place rather than
                           collapsing: the programme exists, and a lane that
                           vanished until a tile arrived would read as a
                           programme that does not. */
                        <div className="flex aspect-video w-full items-center justify-center rounded-md border border-border bg-black text-tiny text-muted-foreground">
                          {t("dash.laneNoPreview")}
                        </div>
                      )}
                    </LazyBoundary>
                    <div className="grid gap-3 lg:col-span-2 xl:grid-cols-2">
                      {lane.destinations.length === 0 ? (
                        <p className="text-tiny text-muted-foreground">{t("dash.destNoneHere")}</p>
                      ) : (
                        lane.destinations.map((d) => renderDestination(d, destinations.indexOf(d)))
                      )}
                    </div>
                  </div>
                </section>
              ))
            ) : (
              <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
                {destinations.map((d, i) => renderDestination(d, i))}
              </div>
            )}

            {lanes.orphans.length > 0 && (
              /* NOT A LANE, because these name no programme this server listed.
                 Drawn rather than filtered out: each is still configured and may
                 still be running, and this is the only screen that lists it. */
              <section className="flex flex-col gap-2">
                <p className="text-tiny text-warn">{t("dash.destOrphaned")}</p>
                <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
                  {lanes.orphans.map((d) => renderDestination(d, destinations.indexOf(d)))}
                </div>
              </section>
            )}
          </div>
        )}
      </div>

      <DestinationDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        destination={editing}
        onSaved={() => setRefreshKey((k) => k + 1)}
      />
      <ConfirmDestructive
        open={confirmDelete.open}
        onOpenChange={confirmDelete.onOpenChange}
        subject={confirmDelete.target?.name ?? ""}
        // A18: typed only while it is live. Deleting a running destination
        // ends a broadcast people are watching, which does not come back;
        // deleting an idle one is a profile that can be added again.
        requireTyping={confirmDelete.target?.live ?? false}
        title={t("confirm.deleteTitle", { name: confirmDelete.target?.name ?? "" })}
        description={
          <>
            {confirmDelete.target?.live && (
              <span className="block font-medium text-down">{t("dash.deleteDestLive")}</span>
            )}
            {t("dash.deleteDestDescription")}
            {deleteUsage.status === "ready" && deleteUsage.schedules > 0 && (
              <span className="mt-1 block">{t("dash.deleteDestSchedulesNote")}</span>
            )}
            {deleteUsage.status === "failed" && (
              <span className="mt-1 block text-muted-foreground">{t("dash.deleteDestUsageUnknown")}</span>
            )}
          </>
        }
        // Held shut only while the count is on its way; a failed read says so
        // in the description and does not block a delete the operator asked for.
        confirmDisabled={deleteUsage.status === "loading"}
        consequencesLabel={t("dash.deleteDestUsedBy")}
        consequences={
          deleteUsage.status === "ready" && deleteUsage.schedules > 0
            ? [{ label: t("dash.deleteDestSchedules"), count: deleteUsage.schedules }]
            : undefined
        }
        confirmLabel={t("dash.deleteDest")}
        onConfirm={async () => {
          if (confirmDelete.target) await remove(confirmDelete.target.id);
        }}
      />
    </div>
  );
}
