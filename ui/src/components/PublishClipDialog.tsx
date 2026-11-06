import { useCallback, useEffect, useMemo, useState } from "react";
import { settingsHref } from "@/lib/settingsSearch";
import { Link } from "react-router";
import { CheckCircle2, ExternalLink, Lock, RotateCw, Send } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { ApiError, api } from "@/lib/api";
import { useT } from "@/lib/i18n";
import {
  formComplete,
  formKey,
  inFlight,
  isPublishCause,
  parseTags,
  tagsText,
  type Publication,
  type PublishForm,
  type PublishPlatform,
  type PublishPlatformId,
  type PublishPrepared,
  type PublishTargets,
} from "@/lib/publish";
import { cn } from "@/lib/utils";

/* Publish a finished clip or short (Wave 4 #5c).

   THE PUBLISH BUTTON IS THE ONLY THING THAT POSTS. The form is checked as it is
   edited (POST /publish/prepare sends nothing to any platform); that check
   returns a one-time confirmation for exactly the text on screen, and the
   Publish button spends it. Edit a field and the confirmation is gone until the
   new form has been checked. There is no auto-publish setting because there is
   nothing here that would act on one. */

interface Props {
  /** The finished clip.export job to publish. */
  clipJobId: number;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

const POLL_MS = 2000;
const DEBOUNCE_MS = 350;

const PLATFORM_LABEL: Record<PublishPlatformId, "publish.platform.youtube" | "publish.platform.tiktok" | "publish.platform.x"> = {
  youtube: "publish.platform.youtube",
  tiktok: "publish.platform.tiktok",
  x: "publish.platform.x",
};

export function PublishClipDialog({ clipJobId, open, onOpenChange }: Readonly<Props>) {
  const t = useT();
  const [targets, setTargets] = useState<PublishTargets | null>(null);
  const [loadError, setLoadError] = useState("");
  const [platformId, setPlatformId] = useState<PublishPlatformId>("youtube");
  const [accountId, setAccountId] = useState(0);
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [tags, setTags] = useState("");
  const [privacy, setPrivacy] = useState("private");

  const [prepared, setPrepared] = useState<{ key: string; value: PublishPrepared } | null>(null);
  const [checking, setChecking] = useState(false);
  const [problem, setProblem] = useState<{ code: string; message: string } | null>(null);
  const [sending, setSending] = useState(false);
  const [publication, setPublication] = useState<Publication | null>(null);
  // Bumped when a confirmation is spent, refused or expires, so the form is checked
  // again without the person having to edit a field to wake it.
  const [epoch, setEpoch] = useState(0);
  // The person has looked at the platform after an outcome_unknown failure.
  const [acknowledged, setAcknowledged] = useState(false);

  const platform: PublishPlatform | undefined = targets?.platforms.find((p) => p.id === platformId);
  const account = platform?.accounts.find((a) => a.id === accountId);

  // Load the options each time the dialog opens, so a connection made since the
  // last visit is there.
  useEffect(() => {
    if (!open) return;
    let live = true;
    setTargets(null);
    setLoadError("");
    setPublication(null);
    setPrepared(null);
    setProblem(null);
    setAcknowledged(false);
    api
      .publishTargets(clipJobId)
      .then((tg) => {
        if (!live) return;
        setTargets(tg);
        setTitle(tg.prefill.title);
        setDescription(tg.prefill.description);
        setTags(tagsText(tg.prefill.tags));
        const first = tg.platforms.find((p) => p.enabled && p.accounts.length > 0) ?? tg.platforms[0];
        if (!first) return;
        setPlatformId(first.id);
        setAccountId(first.accounts[0]?.id ?? 0);
        setPrivacy(first.privacy.default);
      })
      .catch((e: unknown) => live && setLoadError(e instanceof Error ? e.message : String(e)));
    return () => {
      live = false;
    };
  }, [open, clipJobId]);

  const choosePlatform = (p: PublishPlatform) => {
    setPlatformId(p.id);
    setAccountId(p.accounts[0]?.id ?? 0);
    setPrivacy(p.privacy.default);
  };

  const form: PublishForm = useMemo(
    () => ({
      clipJobId, platform: platformId, accountId, title, description, tags: parseTags(tags), privacy,
      ...(acknowledged ? { acknowledgeOutcome: true } : {}),
    }),
    [clipJobId, platformId, accountId, title, description, tags, privacy, acknowledged],
  );
  const key = formKey(form);
  const usable =
    !!platform && platform.enabled && !!account && !account.needsReconnect && formComplete(form) && !publication;

  // Check the form while it is being edited. This sends nothing anywhere.
  useEffect(() => {
    if (!open || !usable) return;
    let live = true;
    setChecking(true);
    const timer = setTimeout(() => {
      api
        .preparePublish(form)
        .then((value) => {
          if (!live) return;
          setPrepared({ key, value });
          setProblem(null);
        })
        .catch((e: unknown) => {
          if (!live) return;
          setPrepared(null);
          setProblem(problemFrom(e));
        })
        .finally(() => live && setChecking(false));
    }, DEBOUNCE_MS);
    return () => {
      live = false;
      clearTimeout(timer);
      setChecking(false);
    };
    // `key` stands for `form`: it changes exactly when the form does.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, usable, key, epoch]);

  // A confirmation is for one form, once, for ten minutes: drop it the moment
  // the form changes or time runs out.
  const confirmation = prepared && prepared.key === key ? prepared.value : null;
  useEffect(() => {
    if (!confirmation) return;
    const ms = new Date(confirmation.expiresAt).getTime() - Date.now();
    const timer = setTimeout(() => {
      setPrepared(null);
      setEpoch((e) => e + 1);
    }, Math.max(ms, 0));
    return () => clearTimeout(timer);
  }, [confirmation]);

  // Follow a publication until it ends.
  const pubId = publication?.id;
  const pubOpen = inFlight(publication);
  useEffect(() => {
    if (!pubId || !pubOpen) return;
    let live = true;
    const timer = setInterval(() => {
      api
        .publication(pubId)
        .then((p) => live && setPublication(p))
        .catch(() => undefined);
    }, POLL_MS);
    return () => {
      live = false;
      clearInterval(timer);
    };
  }, [pubId, pubOpen]);

  const publish = useCallback(async () => {
    if (!confirmation) return;
    setSending(true);
    setProblem(null);
    try {
      const p = await api.publishClip(form, confirmation.confirmToken);
      setPublication(p);
      setPrepared(null);
    } catch (e) {
      setProblem(problemFrom(e));
      setPrepared(null);
      setEpoch((e) => e + 1);
    } finally {
      setSending(false);
    }
  }, [confirmation, form]);

  const tryAgain = (acknowledgedOutcome: boolean) => {
    setAcknowledged(acknowledgedOutcome);
    setPublication(null);
    setPrepared(null);
    setEpoch((e) => e + 1);
  };

  const forcedPrivacyNote =
    platform?.privacy.forced && platform.id === "tiktok" ? t("publish.privacy.tiktokForced") : "";

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] max-w-xl overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{t("publish.title")}</DialogTitle>
          <DialogDescription>{t("publish.description")}</DialogDescription>
        </DialogHeader>

        {loadError && <p className="text-sm text-down">{t("publish.loadError", { error: loadError })}</p>}
        {!targets && !loadError && <p className="text-sm text-muted-foreground">{t("publish.loading")}</p>}

        {targets && !publication && (
          <div className="flex flex-col gap-3">
            <fieldset className="flex flex-col gap-1.5">
              <Label asChild>
                <legend>{t("publish.platform")}</legend>
              </Label>
              <div role="radiogroup" aria-label={t("publish.platform")} className="flex flex-wrap gap-1.5">
                {targets.platforms.map((p) => (
                  <Button
                    key={p.id}
                    type="button"
                    role="radio"
                    aria-checked={p.id === platformId}
                    variant={p.id === platformId ? "default" : "outline"}
                    size="sm"
                    disabled={!p.enabled}
                    onClick={() => choosePlatform(p)}
                  >
                    {t(PLATFORM_LABEL[p.id])}
                  </Button>
                ))}
              </div>
              {/* A disabled control says why, beside itself (DESIGN-SYSTEM rule 3):
                  the switched-off platform can never be the selected one, so its
                  reason is printed for every disabled button, not only the chosen. */}
              {targets.platforms
                .filter((p) => !p.enabled)
                .map((p) => (
                  <p key={p.id} data-testid={`publish-gated-${p.id}`} className="text-sm text-muted-foreground">
                    {t("publish.gatedFor", { platform: t(PLATFORM_LABEL[p.id]) })}
                  </p>
                ))}
            </fieldset>

            {platform?.enabled && (
              <fieldset className="flex flex-col gap-1.5">
                <Label asChild>
                  <legend>{t("publish.account")}</legend>
                </Label>
                {platform.accounts.length === 0 ? (
                  <p className="text-sm text-muted-foreground">
                    {t("publish.noAccount", { platform: t(PLATFORM_LABEL[platform.id]) })}{" "}
                    <Link className="underline" to={settingsHref("platforms")}>
                      {t("publish.connectInSettings")}
                    </Link>
                  </p>
                ) : (
                  <div role="radiogroup" aria-label={t("publish.account")} className="flex flex-wrap gap-1.5">
                    {platform.accounts.map((a) => (
                      <Button
                        key={a.id}
                        type="button"
                        role="radio"
                        aria-checked={a.id === accountId}
                        variant={a.id === accountId ? "default" : "outline"}
                        size="sm"
                        onClick={() => setAccountId(a.id)}
                      >
                        {a.name}
                      </Button>
                    ))}
                  </div>
                )}
                {account?.needsReconnect && (
                  <div className="flex flex-wrap items-center gap-2 rounded-md border border-warn/30 bg-warn-dim p-2 text-sm text-warn">
                    <span>{t("publish.needsReconnect")}</span>
                    <Button asChild size="sm" variant="outline">
                      <a href={account.connectPath}>{t("publish.reconnect")}</a>
                    </Button>
                  </div>
                )}
              </fieldset>
            )}

            {platform?.enabled && account && !account.needsReconnect && (
              <>
                <div className="flex flex-col gap-1">
                  <Label htmlFor="publish-title">{t("publish.titleLabel")}</Label>
                  <Input id="publish-title" value={title} onChange={(e) => setTitle(e.target.value)} />
                </div>
                <div className="flex flex-col gap-1">
                  <Label htmlFor="publish-description">{t("publish.descriptionLabel")}</Label>
                  <Textarea
                    id="publish-description"
                    value={description}
                    onChange={(e) => setDescription(e.target.value)}
                  />
                </div>
                <div className="flex flex-col gap-1">
                  <Label htmlFor="publish-tags">{t("publish.hashtagsLabel")}</Label>
                  <Input id="publish-tags" value={tags} onChange={(e) => setTags(e.target.value)} />
                  <p className="text-sm text-muted-foreground">{t("publish.hashtagsHint")}</p>
                </div>

                <fieldset className="flex flex-col gap-1.5">
                  <Label asChild>
                    <legend>{t("publish.privacy")}</legend>
                  </Label>
                  {platform.privacy.forced ? (
                    <div className="flex items-start gap-2 rounded-md border border-border-strong p-2 text-sm">
                      <Lock className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                      <div className="flex flex-col gap-0.5">
                        <span data-testid="publish-privacy-forced" className="font-medium">
                          {platform.id === "tiktok" ? t("publish.privacy.onlyYou") : t("publish.privacy.public")}
                        </span>
                        {forcedPrivacyNote && (
                          <span className="text-muted-foreground">{forcedPrivacyNote}</span>
                        )}
                      </div>
                    </div>
                  ) : (
                    <div role="radiogroup" aria-label={t("publish.privacy")} className="flex flex-wrap gap-1.5">
                      {platform.privacy.options.map((o) => (
                        <Button
                          key={o}
                          type="button"
                          role="radio"
                          aria-checked={o === privacy}
                          variant={o === privacy ? "default" : "outline"}
                          size="sm"
                          onClick={() => setPrivacy(o)}
                        >
                          {privacyOptionLabel(t, o)}
                        </Button>
                      ))}
                    </div>
                  )}
                  {platform.id === "youtube" && (
                    <p className="text-sm text-muted-foreground">{t("publish.youtubeNote")}</p>
                  )}
                  {platform.quotaUnits ? (
                    <p className="text-sm text-muted-foreground">
                      {t("publish.quota", { units: platform.quotaUnits })}
                    </p>
                  ) : null}
                </fieldset>
              </>
            )}

            {problem && (
              <p role="alert" className="rounded-md border border-warn/30 bg-warn-dim p-2 text-sm text-warn">
                {problem.code && isPublishCause(problem.code)
                  ? t(`publish.cause.${problem.code}`)
                  : problem.message}
              </p>
            )}
            {confirmation?.existing?.state === "posted" && (
              <p className="text-sm text-muted-foreground">
                {t("publish.alreadyPosted", { account: confirmation.existing.accountName })}
              </p>
            )}
            {platform?.enabled && account && !account.needsReconnect && !title.trim() && (
              <p className="text-sm text-muted-foreground">{t("publish.needsTitle")}</p>
            )}
            {usable && (
              <p className="text-sm text-muted-foreground" aria-live="polite">
                {statusLine(t, confirmation, checking)}
              </p>
            )}
          </div>
        )}

        {publication && (
          <PublicationStatus pub={publication} account={account} t={t} onAgain={tryAgain} />
        )}

        {targets && targets.publications.length > 0 && !publication && (
          <div className="flex flex-col gap-1 border-t border-border pt-2">
            <p className="text-sm font-medium">{t("publish.previous")}</p>
            {targets.publications.map((p) => (
              <PreviousRow key={p.id} pub={p} t={t} />
            ))}
          </div>
        )}

        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            {t("publish.close")}
          </Button>
          {!publication && (
            <Button
              onClick={() => void publish()}
              disabled={!usable || !confirmation || sending || confirmation.existing?.state === "posted"}
            >
              <Send />
              {sending ? t("publish.publishing") : t("publish.publish")}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

type Tr = ReturnType<typeof useT>;

function problemFrom(e: unknown): { code: string; message: string } {
  if (e instanceof ApiError) return { code: e.code, message: e.message };
  return { code: "", message: e instanceof Error ? e.message : String(e) };
}

function privacyOptionLabel(t: Tr, option: string): string {
  if (option === "private") return t("publish.privacy.private");
  return option === "unlisted" ? t("publish.privacy.unlisted") : t("publish.privacy.public");
}

function statusLine(t: Tr, confirmation: PublishPrepared | null, checking: boolean): string {
  if (confirmation) {
    return t("publish.readyTo", {
      account: confirmation.accountName,
      privacy: privacyWord(t, confirmation.meta.privacy),
    });
  }
  return checking ? t("publish.checking") : "";
}

function previousBadgeVariant(state: Publication["state"]): "live" | "warn" | "outline" {
  if (state === "posted") return "live";
  return state === "failed" ? "warn" : "outline";
}

function privacyWord(t: Tr, level: string): string {
  switch (level) {
    case "SELF_ONLY":
      return t("publish.privacy.onlyYou");
    case "private":
      return t("publish.privacy.private");
    case "unlisted":
      return t("publish.privacy.unlisted");
    default:
      return t("publish.privacy.public");
  }
}

function PreviousRow({ pub, t }: Readonly<{ pub: Publication; t: Tr }>) {
  return (
    <div className="flex flex-wrap items-center gap-2 text-sm">
      <Badge variant={previousBadgeVariant(pub.state)}>
        {t(`publish.state.${pub.state}`)}
      </Badge>
      <span>
        {t(PLATFORM_LABEL[pub.platform])} - {pub.accountName}
      </span>
      {pub.postUrl && (
        <a className="inline-flex items-center gap-1 underline" href={pub.postUrl} target="_blank" rel="noreferrer">
          {t("publish.openPost")} <ExternalLink className="h-3 w-3" />
        </a>
      )}
    </div>
  );
}

function PublicationStatus({
  pub,
  account,
  t,
  onAgain,
}: Readonly<{
  pub: Publication;
  account?: { connectPath?: string };
  t: Tr;
  onAgain: (acknowledgedOutcome: boolean) => void;
}>) {
  const [checked, setChecked] = useState(false);
  const pct = Math.round((pub.job?.progress ?? 0) * 100);
  if (pub.state === "posted") {
    return (
      <div data-testid="publish-result" className="flex flex-col gap-1.5 rounded-md border border-ok/40 bg-ok-dim p-3 text-sm">
        <span className="inline-flex items-center gap-1.5 font-medium text-ok">
          <CheckCircle2 className="h-4 w-4" /> {t("publish.posted")}
        </span>
        {pub.actualPrivacy && (
          <span>{t("publish.privacyApplied", { privacy: privacyWord(t, pub.actualPrivacy) })}</span>
        )}
        {pub.postUrl ? (
          <a className="inline-flex items-center gap-1 underline" href={pub.postUrl} target="_blank" rel="noreferrer">
            {t("publish.openPost")} <ExternalLink className="h-3 w-3" />
          </a>
        ) : (
          pub.platformPostId && <span className="font-mono text-sm">{t("publish.postId", { id: pub.platformPostId })}</span>
        )}
      </div>
    );
  }
  if (pub.state === "failed") {
    const cause = isPublishCause(pub.reason?.cause) ? pub.reason.cause : "unknown";
    return (
      <div data-testid="publish-failure" className={cn("flex flex-col gap-2 rounded-md border border-warn/30 bg-warn-dim p-3 text-sm text-warn")}>
        <span className="font-medium">{t("publish.failed")}</span>
        <p>{t(`publish.cause.${cause}`)}</p>
        {pub.reason?.line && cause !== "needs_reconnect" && (
          <p className="break-words font-mono text-sm text-muted-foreground">{pub.reason.line}</p>
        )}
        <div className="flex flex-wrap gap-2">
          {cause === "needs_reconnect" && account?.connectPath && (
            <Button asChild size="sm" variant="outline">
              <a href={account.connectPath}>{t("publish.reconnect")}</a>
            </Button>
          )}
          {cause === "outcome_unknown" && (
            // The last request may have landed. Publishing again needs the person
            // to have looked: the server refuses the re-arm without it.
            <label className="flex w-full items-center gap-2">
              <Checkbox checked={checked} onCheckedChange={(v) => setChecked(v === true)} />
              <span>{t("publish.ackOutcome", { platform: t(PLATFORM_LABEL[pub.platform]) })}</span>
            </label>
          )}
          {cause !== "gated" && (
            <Button
              size="sm"
              variant="outline"
              disabled={cause === "outcome_unknown" && !checked}
              onClick={() => onAgain(cause === "outcome_unknown")}
            >
              <RotateCw /> {t("publish.again")}
            </Button>
          )}
        </div>
      </div>
    );
  }
  return (
    <div data-testid="publish-progress" className="flex flex-col gap-2 rounded-md border border-border-strong p-3 text-sm">
      <span>{pct > 0 ? t("publish.progress", { pct }) : t("publish.queued")}</span>
      <div className="h-1 overflow-hidden rounded-full bg-muted">
        <div className="h-full bg-primary transition-[width]" style={{ width: `${pct}%` }} />
      </div>
      {pub.job?.blocked && pub.job.reason && (
        <p className="text-muted-foreground">{t("publish.waiting", { reason: pub.job.reason })}</p>
      )}
      {pub.failDetail && <p className="text-sm text-muted-foreground">{pub.failDetail}</p>}
    </div>
  );
}
