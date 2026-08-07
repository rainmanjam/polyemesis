import { Suspense, lazy, useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { ConfirmDestructive } from "@/components/ConfirmDestructive";
import { useConfirm } from "@/hooks/useConfirm";
import { Copy, Megaphone, Plus, Radio } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { PageHeader } from "@/components/AppLayout";
import { DestinationCard } from "@/components/DestinationCard";
import { DestinationDialog } from "@/components/DestinationDialog";
import { ChatPanel } from "@/components/ChatPanel";
import { StatusDot } from "@/components/signature/StatusDot";
import { Stat } from "@/components/signature/Stat";
import { useLiveData } from "@/hooks/useLiveData";
import { api } from "@/lib/api";
import { duration, kbps } from "@/lib/format";
import { toneBadge, toneForState } from "@/lib/signal";
import type { SignalTone } from "@/lib/signal";
import type { Destination, MetaField, SystemInfo } from "@/lib/types";
import { useT, useStateLabel } from "@/lib/i18n";

// hls.js is a few hundred kilobytes that only the preview needs, and the
// preview is off entirely for some installs. Load it alongside the dashboard
// rather than ahead of it.
const PreviewPlayer = lazy(() =>
  import("@/components/PreviewPlayer").then((m) => ({ default: m.PreviewPlayer })),
);

// ---------------------------------------------------------- go-live composer
//
// Set the title, description and category once and push them to every
// connected account. The shapes below mirror internal/api/metadata.go; they
// live here rather than in lib/types.ts because nothing else renders them.
//
// MetaField is the exception and now lives in lib/types.ts: a push RESULT names
// those fields, so the union is an API contract rather than a detail of this
// page -- and internal/oauth has a drift guard that reads it there.

/** What YouTube will still accept on the current broadcast.
 *
 *  Fetched when the composer opens, never polled: every row is a live platform
 *  call. A row that failed to read disables nothing -- the write still happens
 *  and the platform's 403 remains the authority. */
interface BroadcastWindowRow {
  accountId: number;
  platform: string;
  accountName: string;
  window?: {
    broadcastId: string;
    title: string;
    lifeCycleStatus: string;
    contentDetailsLocked: boolean;
    lockedReason?: string;
  };
  /** false for a platform with no broadcast concept, which is not an error. */
  supported: boolean;
  error?: string;
}
type MetaState = "pending" | "ok" | "partial" | "error";

interface MetaCaps {
  fields: MetaField[];
  categoryLabel?: string;
  categoryHint?: string;
  titleMax?: number;
  descriptionMax?: number;
}

interface MetaTarget {
  accountId: number;
  platform: string;
  accountName: string;
  caps: MetaCaps;
  /** Obligation metadata resolved from this account's destinations, sent on
   *  every push whether or not anything is typed here. Absent when no
   *  destination on the account set any. Mirrors internal/api's metadataTarget;
   *  the stream key it resolves alongside is deliberately never serialised. */
  compliance?: {
    privacy?: string;
    madeForKids?: boolean;
    labels?: Record<string, boolean>;
    facebookPrivacy?: string;
  };
}

interface MetaOutcome {
  accountId: number;
  platform: string;
  accountName: string;
  state: MetaState;
  message?: string;
  applied: MetaField[];
  skipped?: MetaField[];
  target?: string;
  category?: string;
  warnings?: string[];
}

interface MetaJob {
  id: string;
  done: boolean;
  results: MetaOutcome[];
  metadata: { title: string; description: string; category: string };
}

/** The double-submit CSRF token, read the way lib/api.ts reads it. */
function csrfToken(): string {
  const match = document.cookie.match(/(?:^|;\s*)polyemesis_csrf=([^;]+)/);
  return match ? decodeURIComponent(match[1]) : "";
}

async function metaFetch<T>(path: string, body?: unknown): Promise<T> {
  const headers = new Headers();
  if (body !== undefined) {
    headers.set("Content-Type", "application/json");
    headers.set("X-CSRF-Token", csrfToken());
  }
  const resp = await fetch("/api/v1" + path, {
    method: body === undefined ? "GET" : "POST",
    headers,
    credentials: "same-origin",
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await resp.text();
  const parsed: unknown = text ? JSON.parse(text) : null;
  if (!resp.ok) {
    const msg =
      parsed && typeof parsed === "object" && "error" in parsed
        ? String((parsed as { error: unknown }).error)
        : `request failed (${resp.status})`;
    throw new Error(msg);
  }
  return parsed as T;
}

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

function GoLiveComposer() {
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
    metaFetch<{ targets: MetaTarget[]; last?: MetaJob }>("/metadata")
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
      .catch(() => setTargets([]));
    // What is still editable, read once when the composer opens. Deliberately
    // not polled: each row is a live platform call, and a broadcast that goes
    // live mid-edit is caught by the write's own 403 rather than by a timer.
    metaFetch<{ accounts: BroadcastWindowRow[] }>("/metadata/broadcast-window")
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
  const jobId = job?.id;
  const jobDone = job?.done ?? true;
  useEffect(() => {
    if (!jobId || jobDone) return;
    let live = true;
    const timer = window.setInterval(() => {
      metaFetch<MetaJob>(`/metadata/push/${jobId}`)
        .then((next) => {
          if (live) setJob(next);
        })
        .catch(() => {});
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
      const started = await metaFetch<MetaJob>("/metadata/push", {
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
  const busy = pushing || (job !== null && !job.done);

  return (
    <Card className="mt-4">
      <CardHeader className="flex-row items-center justify-between">
        <CardTitle className="flex items-center gap-2">
          <Megaphone className="h-3.5 w-3.5 text-muted-foreground" />
          Go live
        </CardTitle>
        <span className="font-mono text-[10px] text-muted-foreground">
          {targets.length === 1 ? "1 account" : `${targets.length} accounts`}
        </span>
      </CardHeader>

      {targets.length === 0 ? (
        <CardContent>
          <p className="text-[12px] text-muted-foreground">
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
                    className={`tnum font-mono text-[10px] ${
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
                <p className="text-[10px] text-down">
                  Too long for {overLimit.map((t) => t.platform).join(", ")}; that platform will be
                  reported as failed and the others still pushed.
                </p>
              )}
            </div>

            <div className="flex flex-col gap-1">
              <Label htmlFor="golive-description">{t("dash.metaDescription")}</Label>
              <Textarea
                id="golive-description"
                value={description}
                rows={3}
                placeholder={t("dash.descriptionPlaceholder")}
                onChange={(e) => setDescription(e.target.value)}
              />
              {noDescription.length > 0 && (
                <p className="text-[10px] text-muted-foreground">
                  {noDescription.map((t) => t.platform).join(", ")} has no description field, so
                  this is skipped there rather than failed.
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
                <p className="text-[10px] text-muted-foreground">
                  {categoryHint} Type the name — polyemesis looks up the id.
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
                <span className="text-[10px] text-muted-foreground">
                  Comma separated. These REPLACE the existing tags rather than adding to them,
                  because that is what each platform's API does. Applies to{" "}
                  {tagTargets.map((t) => t.platform).join(", ")}.
                </span>
              </div>
            )}

            {broadcastAccounts.length > 0 && (
              <div className="flex flex-col gap-2 rounded-md border border-border p-3">
                <div className="flex items-center justify-between gap-2">
                  <Label>{t("dash.broadcastSettings")}</Label>
                  <span className="text-[10px] text-muted-foreground">
                    {broadcastAccounts.map((w) => w.platform).join(", ")} only
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
                  <span className="text-[10px] text-muted-foreground">
                    Leave empty to keep the current one.
                  </span>
                </div>

                {contentDetailsLocked && (
                  <p className="text-[10px] text-amber-600 dark:text-amber-500">
                    {lockedReason ||
                      t("dash.youtubeStopsAcceptingTheseOnce")}
                  </p>
                )}

                <div className="grid grid-cols-3 gap-2">
                  {[
                    { id: "dvr", label: "DVR", value: dvr, set: setDvr },
                    { id: "autostart", label: "Auto-start", value: autoStart, set: setAutoStart },
                    { id: "autostop", label: "Auto-stop", value: autoStop, set: setAutoStop },
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
                        <option value="on">On</option>
                        <option value="off">{t("dash.off")}</option>
                      </select>
                    </div>
                  ))}
                </div>
                <span className="text-[10px] text-muted-foreground">
            {t("dash.leaveUnchangedNote")}
                </span>
              </div>
            )}

            <div className="flex items-center gap-2">
              <Button size="sm" onClick={push} disabled={empty || busy}>
                <Megaphone /> {busy ? t("dash.pushing") : t("dash.pushToPlatforms")}
              </Button>
              <span className="text-[10px] text-muted-foreground">
                Applies to {accepts("title").length === 1 ? t("dash.theConnectedAccount") : t("dash.everyConnectedAccount")}.
              </span>
            </div>

            {withCompliance.length > 0 && (
              <p className="text-[10px] text-muted-foreground">
                This push also sends the compliance settings stored on{" "}
                {withCompliance.map((t) => t.accountName || t.platform).join(", ")} &mdash;
                visibility, made-for-kids and content labels are configured per destination, not
                here, and go out whether or not you change anything above.
              </p>
            )}
          </div>

          {/* ---------- what each platform did ---------- */}
          <div aria-live="polite" className="flex flex-col gap-1.5">
            {job === null ? (
              <p className="text-[11px] text-muted-foreground">
                Results appear here, one row per platform.
              </p>
            ) : (
              job.results.map((res) => {
                const tone = metaTone[res.state];
                return (
                  <div
                    key={res.accountId}
                    className="flex flex-col gap-0.5 rounded border border-border px-2 py-1.5"
                  >
                    <div className="flex items-center justify-between gap-2">
                      <div className="flex min-w-0 items-center gap-2">
                        <StatusDot tone={tone} size="sm" />
                        <span className="truncate text-[11px] font-medium">{res.platform}</span>
                        <span className="truncate font-mono text-[10px] text-muted-foreground">
                          {res.accountName}
                        </span>
                      </div>
                      <Badge variant={toneBadge[tone]}>{metaLabel[res.state]}</Badge>
                    </div>

                    {res.applied.length > 0 && (
                      <p className="text-[10px] text-muted-foreground">
                        Set {res.applied.join(", ")}
                        {res.category && ` — category “${res.category}”`}
                        {res.target && ` on ${res.target}`}
                      </p>
                    )}
                    {res.skipped && res.skipped.length > 0 && (
                      <p className="text-[10px] text-muted-foreground">
                        Not supported here: {res.skipped.join(", ")}
                      </p>
                    )}
                    {res.message && <p className="text-[10px] text-down">{res.message}</p>}
                    {res.warnings?.map((warn) => (
                      <p key={warn} className="text-[10px] text-warn">
                        {warn}
                      </p>
                    ))}
                  </div>
                );
              })
            )}
          </div>
        </CardContent>
      )}
    </Card>
  );
}

export function Dashboard() {
  const stateLabel = useStateLabel();
  const t = useT();
  const { status } = useLiveData();
  const [system, setSystem] = useState<SystemInfo | null>(null);
  const [settingsPreview, setSettingsPreview] = useState(true);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editing, setEditing] = useState<Destination | null>(null);
  const [busyId, setBusyId] = useState<number | null>(null);
  const [refreshKey, setRefreshKey] = useState(0);
  const [pending, setPending] = useState<number[] | null>(null);
  const [moveNote, setMoveNote] = useState("");

  useEffect(() => {
    api.system().then(setSystem).catch(() => {});
    api
      .getSettings()
      .then((s) => setSettingsPreview(s.preview.enabled))
      .catch(() => {});
  }, [refreshKey]);

  const act = useCallback(
    async (id: number, fn: () => Promise<unknown>, label: string) => {
      setBusyId(id);
      try {
        await fn();
      } catch (err) {
        toast.error(err instanceof Error ? err.message : `Could not ${label}.`);
      } finally {
        setBusyId(null);
      }
    },
    [],
  );

  const openEdit = async (id: number) => {
    try {
      const { destination } = await api.getDestination(id);
      setEditing(destination);
      setDialogOpen(true);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("dash.couldNotLoadTheDestination"));
    }
  };

  const confirmDelete = useConfirm<{ id: number; name: string }>();

  const remove = async (id: number) => {
    await act(id, () => api.deleteDestination(id), "delete the destination");
    toast.success(t("dash.destDeleted"));
    setRefreshKey((k) => k + 1);
  };

  const ingest = status?.ingest;
  const ingestTone = toneForState(ingest?.state);
  const source = status?.source;
  const live = status?.destinations;
  const renditions = status?.renditions ?? [];

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

  const move = async (id: number, delta: -1 | 1) => {
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

  const copyIngest = async () => {
    if (!system?.ingestUrl) return;
    try {
      await navigator.clipboard.writeText(system.ingestUrl);
      toast.success(t("dash.ingestUrlCopied"));
    } catch {
      toast.error(t("dash.clipboardUnavailable"));
    }
  };

  return (
    <div className="p-3">
      <PageHeader
        title={t("dash.title")}
        subtitle={t("dash.subtitle")}
        actions={
          <Button
            size="sm"
            onClick={() => {
              setEditing(null);
              setDialogOpen(true);
            }}
          >
            <Plus /> Add destination
          </Button>
        }
      />

      <div className="grid gap-3 lg:grid-cols-[minmax(0,1fr)_20rem]">
        {/* ---------- preview + ingest ---------- */}
        <div className="flex flex-col gap-3">
          <Suspense
            fallback={
              <div className="aspect-video w-full rounded-md border border-border bg-black" />
            }
          >
            <PreviewPlayer active={settingsPreview} />
          </Suspense>

          <Card>
            <CardHeader className="flex-row items-center justify-between">
              <CardTitle className="flex items-center gap-2">
                <StatusDot tone={ingestTone} />
                Ingest
              </CardTitle>
              <Badge variant={toneBadge[ingestTone]}>{stateLabel(ingest?.state)}</Badge>
            </CardHeader>
            <CardContent className="flex flex-col gap-3">
              <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
                <Stat
                  label={t("dash.bitrate")}
                  value={ingest?.state === "running" ? kbps(ingest.progress?.bitrateKbps ?? 0) : "—"}
                />
                <Stat
                  label={t("dash.uptime")}
                  value={ingest?.state === "running" ? duration(ingest.uptimeSec) : "—"}
                />
                <Stat label={t("dash.audioTracks")} value={source?.tracks?.length ?? 0} />
                <Stat
                  label={t("dash.reconnects")}
                  value={ingest?.restarts ?? 0}
                  tone={(ingest?.restarts ?? 0) > 0 ? "warn" : "muted"}
                />
              </div>

              {source?.video && (
                <div className="font-mono text-[10px] text-muted-foreground">
                  {source.video.codec} {source.video.width}×{source.video.height}
                  {source.video.frameRate > 0 && ` @ ${source.video.frameRate.toFixed(2)}fps`}
                </div>
              )}

              <div className="flex items-center gap-2 rounded border border-border bg-background px-2 py-1.5">
                <Radio className="h-3 w-3 shrink-0 text-muted-foreground" />
                <code className="min-w-0 flex-1 truncate font-mono text-[10px] text-muted-foreground">
                  {system?.ingestUrl ?? "…"}
                </code>
                <Button variant="ghost" size="icon-sm" onClick={copyIngest} aria-label={t("dash.copyIngestUrl")}>
                  <Copy />
                </Button>
              </div>

              {ingest?.lastError && ingest.state !== "running" && (
                <div className="rounded border border-down/30 bg-down-dim px-2 py-1 text-[10px] text-down">
                  {ingest.lastError}
                </div>
              )}

              {system && !system.ffmpeg.hasLibsrt && (
                <div className="rounded border border-warn/30 bg-warn-dim px-2 py-1 text-[10px] text-warn">
                  This FFmpeg build has no SRT support, so multi-track SRT ingest will not work.
                  Install a build with <code className="font-mono">--enable-libsrt</code>, or switch
                  the ingest to RTMP in Settings.
                </div>
              )}
            </CardContent>
          </Card>
        </div>

        {/* ---------- side stats ---------- */}
        <div className="flex flex-col gap-3">
          {/* Chat sits above the pipeline because it is the only thing on this
              column an operator reads mid-broadcast. It renders honestly when
              nothing is connected — "no platforms connected", not an error —
              so it costs an install with no accounts nothing but a heading. */}
          <ChatPanel className="h-80" />

          <Card>
            <CardHeader>
              <CardTitle>{t("dash.pipeline")}</CardTitle>
            </CardHeader>
            <CardContent className="flex flex-col gap-1.5">
              {(
                [
                  ["Recorder", status?.recorder, "disabled"],
                  // The preview encoder is started on demand and stopped again
                  // when nobody is watching, so having no process is the normal
                  // idle state rather than a fault or a disabled feature.
                  ["Preview", status?.preview, settingsPreview ? t("dash.idle") : t("dash.disabled")],
                  ["Meters", status?.meters, "disabled"],
                ] as const
              ).map(([label, proc, absent]) => {
                const tone = proc ? toneForState(proc.state) : "idle";
                return (
                  <div key={label} className="flex items-center justify-between">
                    <div className="flex items-center gap-2">
                      <StatusDot tone={tone} size="sm" />
                      <span className="text-[11px]">{label}</span>
                    </div>
                    <span className="font-mono text-[10px] text-muted-foreground">
                      {proc ? stateLabel(proc.state) : absent}
                    </span>
                  </div>
                );
              })}
              <div className="mt-1 flex items-center justify-between border-t border-border pt-1.5">
                <span className="text-[11px] text-muted-foreground">{t("dash.relaySubscribers")}</span>
                <span className="tnum font-mono text-[10px]">
                  {status?.relay.subscribers?.length ?? 0}
                </span>
              </div>

              {/* The shared encode tier, with the ref count that decides
                  whether each one runs. A rendition nobody enabled has no
                  process on purpose, so it reads as idle rather than as a
                  fault, and the destination count is the whole economic
                  argument: three platforms on one tier is still one encode. */}
              <div className="mt-1 flex flex-col gap-1 border-t border-border pt-1.5">
                <span className="text-[11px] text-muted-foreground">{t("dash.renditions")}</span>
                {renditions.length === 0 ? (
                  <span className="font-mono text-[10px] text-muted-foreground">
                    none — every destination is on passthrough
                  </span>
                ) : (
                  renditions.map((r) => (
                    <div key={r.id} className="flex items-center justify-between gap-2">
                      <div className="flex min-w-0 items-center gap-2">
                        <StatusDot
                          tone={r.consumers === 0 ? "idle" : toneForState(r.process?.state)}
                          size="sm"
                        />
                        <span className="truncate text-[11px]">{r.name}</span>
                      </div>
                      <span className="tnum shrink-0 font-mono text-[10px] text-muted-foreground">
                        {r.consumers === 1 ? "1 dest" : `${r.consumers} dests`}
                      </span>
                    </div>
                  ))
                )}
              </div>
            </CardContent>
          </Card>
        </div>
      </div>

      <GoLiveComposer />

      {/* ---------- destinations ---------- */}
      <div className="mt-4">
        <div className="mb-2 flex items-center justify-between">
          <h2 className="text-[13px] font-semibold tracking-tight">
            Destinations
            <span className="ml-1.5 font-mono text-[11px] font-normal text-muted-foreground">
              {destinations.length}
            </span>
          </h2>
        </div>

        {/* A card that jumps position is invisible to a screen reader unless
            the move is announced. */}
        <output aria-live="polite" className="sr-only">
          {moveNote}
        </output>

        {destinations.length === 0 ? (
          <Card>
            <CardContent className="flex flex-col items-center gap-2 py-8 text-center">
              <p className="text-[12px] text-muted-foreground">
            {t("dash.noDestinations")}
              </p>
              <Button
                size="sm"
                onClick={() => {
                  setEditing(null);
                  setDialogOpen(true);
                }}
              >
                <Plus /> Add destination
              </Button>
            </CardContent>
          </Card>
        ) : (
          <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
            {destinations.map((d, i) => (
              <DestinationCard
                key={d.id}
                dest={d}
                busy={busyId === d.id}
                canMoveEarlier={i > 0}
                canMoveLater={i < destinations.length - 1}
                onMoveEarlier={() => move(d.id, -1)}
                onMoveLater={() => move(d.id, 1)}
                onStart={() => act(d.id, () => api.startDestination(d.id), "start the destination")}
                onStop={() => act(d.id, () => api.stopDestination(d.id), "stop the destination")}
                onRestart={() =>
                  act(d.id, () => api.restartDestination(d.id), "restart the destination")
                }
                onEdit={() => openEdit(d.id)}
                onDelete={() => confirmDelete.ask({ id: d.id, name: d.name })}
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
              />
            ))}
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
        title={`Delete “${confirmDelete.target?.name}”?`}
        description={t("dash.deleteDestDescription")}
        confirmLabel={t("dash.deleteDest")}
        onConfirm={async () => {
          if (confirmDelete.target) await remove(confirmDelete.target.id);
        }}
      />
    </div>
  );
}
