import { Fragment, useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import { Link } from "react-router";
import { toast } from "sonner";
import { ConfirmDestructive } from "@/components/ConfirmDestructive";
import { useConfirm } from "@/hooks/useConfirm";
import {
  Captions,
  CaptionsOff,
  ChevronDown,
  ChevronRight,
  CirclePlay,
  Download,
  ExternalLink,
  Film,
  FileVideoCamera,
  Image as ImageIcon,
  Layers,
  Loader2,
  Pencil,
  Scissors,
  Search,
  Trash2,
  Upload,
  X,
} from "lucide-react";
import { EmptyState } from "@/components/EmptyState";
import { SaveStatus } from "@/components/SaveStatus";
import { useSaveModel } from "@/lib/saveModel";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { PageHeader } from "@/components/AppLayout";
import { api, ApiError } from "@/lib/api";
import { MediaUploads } from "@/components/MediaUploads";
import { bytes, shortDuration, timestamp } from "@/lib/format";
import { cn } from "@/lib/utils";
import type {
  LibraryRecording,
  LibrarySession,
  LibraryView,
  SearchResults,
  TranscriptHit,
  TranscriptOrder,
  TranscriptTrack,
  TranscriptView,
} from "@/lib/types";
import { useT, type TranslationKey } from "@/lib/i18n";

// The library is the session, not the file. A broadcast is one thing; that the
// recorder wrote it as eleven MKV segments is an implementation detail, and the
// only page that should ever have shown it that way is the one this replaces.
//
// Search sits at the top because it is the headline feature of the whole
// workstream. Every microphone was recorded on its own track, so a transcript
// here already carries speaker attribution with no diarization model behind it,
// and "find where I said X" is answerable across every broadcast this box holds.

const SEARCH_DEBOUNCE_MS = 250;
const SEARCH_LIMIT = 40;

/** What each recording job's toasts say. Keys per kind rather than one label
 *  spliced into a sentence: "{label} queued" fixes English word order, and
 *  lower-casing a label breaks every language that capitalises nouns. */
const JOB_TOASTS: Record<
  "transcribe" | "media.proxy",
  { queued: TranslationKey; already: TranslationKey; failed: TranslationKey }
> = {
  transcribe: {
    queued: "lib.transcriptionQueued",
    already: "lib.transcriptionAlreadyQueued",
    failed: "lib.couldNotQueueTranscription",
  },
  "media.proxy": {
    queued: "lib.proxyQueued",
    already: "lib.proxyAlreadyQueued",
    failed: "lib.couldNotQueueTheProxy",
  },
};

function errText(err: unknown, fallback: string): string {
  if (err instanceof ApiError || err instanceof Error) return err.message;
  return fallback;
}

/** A hit's timestamp as h:mm:ss into its recording — the number the player
 *  seeks to, so it is shown rather than the wall clock alone. */
function offsetLabel(ms: number): string {
  return shortDuration(ms);
}

/** Split a snippet on the server's highlight sentinels. They are private-use
 *  codepoints rather than markup precisely so this can be done without ever
 *  putting server text through dangerouslySetInnerHTML. */
function highlight(snippet: string, markers: [string, string]) {
  const [open, close] = markers;
  if (!open || !close) return <>{snippet}</>;
  const parts: React.ReactNode[] = [];
  let rest = snippet;
  let key = 0;
  while (rest.length > 0) {
    const i = rest.indexOf(open);
    if (i < 0) break;
    const j = rest.indexOf(close, i + open.length);
    if (j < 0) break;
    if (i > 0) parts.push(<Fragment key={key++}>{rest.slice(0, i)}</Fragment>);
    parts.push(
      <mark key={key++} className="rounded-sm bg-primary-dim px-0.5 text-foreground">
        {rest.slice(i + open.length, j)}
      </mark>,
    );
    rest = rest.slice(j + close.length);
  }
  if (rest) parts.push(<Fragment key={key++}>{rest}</Fragment>);
  return <>{parts}</>;
}

/** What the player was asked to open: a recording, and optionally the instant
 *  a search hit named. */
interface PlayerTarget {
  recordingId: number;
  atMs?: number;
}

/** Where "Install whisper.cpp" points: whisper.cpp's own project page, which
 *  carries its install and build instructions. Polyemesis finds the binary on
 *  $PATH (whisper-cli, whisper or whisper-cpp; docs/MODULES.md), so installing
 *  it is the whole fix and no polyemesis page documents more than that. A link
 *  rather than the config.yaml key the note used to end in, which meant nothing
 *  to an operator reading the Library (app-council A38). */
const WHISPER_DOCS_URL = "https://github.com/ggml-org/whisper.cpp";

export function LibraryPage() {
  const t = useT();
  const [view, setView] = useState<LibraryView | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);

  // --- search ---
  const [query, setQuery] = useState("");
  const [order, setOrder] = useState<TranscriptOrder>("relevance");
  const [speaker, setSpeaker] = useState("");
  const [results, setResults] = useState<SearchResults | null>(null);
  const [searching, setSearching] = useState(false);

  // --- filters over the session list ---
  const [tag, setTag] = useState("");
  const [since, setSince] = useState("");
  const [until, setUntil] = useState("");
  const [onlyTranscribed, setOnlyTranscribed] = useState(false);

  const [expanded, setExpanded] = useState<number | null>(null);
  const [player, setPlayer] = useState<PlayerTarget | null>(null);
  const [editing, setEditing] = useState<LibrarySession | null>(null);
  // app-council A37: uploads are a source-ingest feature, so they open from a
  // header button instead of taking the top of the archive.
  const [uploadsOpen, setUploadsOpen] = useState(false);
  const transcriptsOffId = useId();

  const load = useCallback(async () => {
    try {
      setView(await api.library());
    } catch (err) {
      toast.error(errText(err, t("lib.couldNotLoadTheLibrary")));
    } finally {
      setLoading(false);
    }
  }, [t]);

  useEffect(() => {
    void load();
  }, [load]);

  // Debounced, and prefix-matched, which together are what make this feel like
  // search-as-you-type rather than a form you submit.
  useEffect(() => {
    const q = query.trim();
    if (q.length < 2) {
      setResults(null);
      setSearching(false);
      return;
    }
    setSearching(true);
    const timer = window.setTimeout(() => {
      api
        .searchTranscripts({
          q,
          prefix: true,
          order,
          speaker: speaker || undefined,
          since: since || undefined,
          until: until || undefined,
          limit: SEARCH_LIMIT,
        })
        .then(setResults)
        .catch((err) => {
          // An unparseable query is the user's, not the server's. Say so
          // quietly rather than throwing a red toast on every third keystroke.
          if (err instanceof ApiError && err.status === 400) {
            setResults(null);
            return;
          }
          toast.error(errText(err, t("lib.searchFailed")));
        })
        .finally(() => setSearching(false));
    }, SEARCH_DEBOUNCE_MS);
    return () => window.clearTimeout(timer);
  }, [query, order, speaker, since, until, t]);

  const sessions = useMemo(() => {
    const list = view?.sessions ?? [];
    return list.filter((s) => {
      if (tag && !(s.tags ?? []).includes(tag)) return false;
      if (onlyTranscribed && s.transcribed === 0) return false;
      if (since && s.startedAt && s.startedAt.slice(0, 10) < since) return false;
      if (until && s.startedAt && s.startedAt.slice(0, 10) > until) return false;
      return true;
    });
  }, [view, tag, since, until, onlyTranscribed]);

  const saveSession = async (id: number, patch: { title: string; description: string; tags: string[] }) => {
    setBusy(true);
    try {
      await api.updateLibrarySession(id, patch);
      toast.success(t("lib.sessionSaved"));
      setEditing(null);
      await load();
    } catch (err) {
      toast.error(errText(err, t("lib.couldNotSaveTheSession")));
    } finally {
      setBusy(false);
    }
  };

  if (loading) {
    return (
      <div className="flex h-64 items-center justify-center">
        <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
      </div>
    );
  }

  const filtersActive = Boolean(tag || since || until || onlyTranscribed);
  // app-council A38: one short status in the header instead of a paragraph
  // that ended in a config.yaml key. Searching still works over whatever is
  // already transcribed, so only the placeholder changes.
  const transcriptsOff = Boolean(view?.jobsAvailable && !view.transcribeAvailable);
  const clearFilters = () => {
    setTag("");
    setSince("");
    setUntil("");
    setOnlyTranscribed(false);
  };

  return (
    <div className="p-3">
      <PageHeader
        title={t("lib.title")}
        subtitle={t("lib.subtitle")}
        actions={
          <div className="flex flex-wrap items-center gap-2">
          {transcriptsOff && (
            <span className="inline-flex items-center gap-1.5 rounded-md border border-warn/40 bg-warn/10 px-2 py-1 text-sm">
              <CaptionsOff aria-hidden="true" className="h-3.5 w-3.5 text-warn" />
              <span id={transcriptsOffId}>{t("lib.transcriptsOff")}</span>
              <a
                href={WHISPER_DOCS_URL}
                target="_blank"
                rel="noreferrer"
                className="inline-flex items-center gap-1 text-primary underline-offset-2 hover:underline"
              >
                {t("lib.installWhisper")}
                <ExternalLink aria-hidden="true" className="h-3 w-3" />
              </a>
            </span>
          )}
          <Button
            variant="outline"
            size="sm"
            aria-expanded={uploadsOpen}
            aria-controls="library-uploads"
            onClick={() => setUploadsOpen(!uploadsOpen)}
          >
            <Upload />
            {t("lib.uploadMedia")}
          </Button>
          <Button
            variant="outline"
            size="sm"
            disabled={busy}
            onClick={async () => {
              setBusy(true);
              try {
                const r = await api.regroupSessions();
                toast.success(
                  t("lib.regrouped", {
                    created: r.created,
                    assigned: r.assigned,
                    extended: r.extended,
                  }),
                );
                await load();
              } catch (err) {
                toast.error(errText(err, t("lib.couldNotRegroup")));
              } finally {
                setBusy(false);
              }
            }}
            title={t("lib.regroupTitle")}
            aria-describedby="library-regroup-explain"
          >
            <Layers />
            {t("lib.regroup")}
          </Button>
          </div>
        }
      />
      {/* app-council A39: what Rebuild sessions does, in one visible line;
          the tooltip keeps the detail. */}
      <p id="library-regroup-explain" className="-mt-1 mb-3 text-sm text-muted-foreground">
        {t("lib.regroupExplain")}
      </p>

      {/* app-council A37: closed until asked for. Uploads are a way to feed a
          source, not part of the archive, and they used to push the sessions
          about 220px down with "Nothing uploaded yet." */}
      {uploadsOpen && (
        <div id="library-uploads" className="mb-3">
          <MediaUploads />
        </div>
      )}

      {/* ---------------- search: the top of the page, not a filter drawer ---- */}
      <Card className="mb-3">
        <CardContent className="flex flex-col gap-2 py-3">
          <div className="flex items-center gap-2">
            <Search className="h-4 w-4 shrink-0 text-muted-foreground" />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder={t(transcriptsOff ? "lib.searchPlaceholderOff" : "lib.searchPlaceholder")}
              className="h-9 border-0 bg-transparent text-[13px] focus-visible:ring-0"
              aria-label={t("lib.searchTranscripts")}
            />
            {searching && <Loader2 className="h-3.5 w-3.5 animate-spin text-muted-foreground" />}
            {query && (
              <Button
                variant="ghost"
                size="icon-sm"
                onClick={() => setQuery("")}
                aria-label={t("lib.clearSearch")}
              >
                <X />
              </Button>
            )}
          </div>

          {query.trim().length >= 2 && (
            <div className="flex flex-wrap items-center gap-2 border-t border-border pt-2">
              <Select value={order} onValueChange={(v) => setOrder(v as TranscriptOrder)}>
                <SelectTrigger className="h-7 w-36 text-tiny">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="relevance">{t("lib.bestMatch")}</SelectItem>
                  <SelectItem value="recent">{t("lib.mostRecent")}</SelectItem>
                  <SelectItem value="time">{t("lib.oldestFirst")}</SelectItem>
                </SelectContent>
              </Select>
              {(view?.speakers.length ?? 0) > 0 && (
                <Select value={speaker || "__all"} onValueChange={(v) => setSpeaker(v === "__all" ? "" : v)}>
                  <SelectTrigger className="h-7 w-40 text-tiny">
                    <SelectValue placeholder={t("lib.anySpeaker")} />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="__all">{t("lib.anySpeaker")}</SelectItem>
                    {view?.speakers.map((s) => (
                      <SelectItem key={s} value={s}>
                        {s}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
              {results && (
                <span className="tnum ml-auto font-mono text-tiny text-muted-foreground">
                  {results.total < 0
                    ? t("lib.hitsShown", { n: results.hits.length })
                    : t("lib.hitsOf", { n: results.hits.length, total: results.total })}
                </span>
              )}
            </div>
          )}
        </CardContent>
      </Card>

      {results && (
        <Card className="mb-3">
          <CardContent className="px-0 py-0">
            {results.hits.length === 0 ? (
              <EmptyState
                icon={Search}
                title={t("lib.noHits")}
                body={view?.transcribeAvailable ? undefined : t("lib.searchTranscribeUnavailable")}
                className="m-3 border-0"
              />
            ) : (
              <ul className="divide-y divide-border">
                {results.hits.map((h) => (
                  <HitRow
                    key={h.segmentId}
                    hit={h}
                    markers={results.markers}
                    onOpen={() => setPlayer({ recordingId: h.recordingId, atMs: h.startMs })}
                  />
                ))}
              </ul>
            )}
          </CardContent>
        </Card>
      )}

      {/* ---------------- filters ---------------- */}
      <div className="mb-3 flex flex-wrap items-end gap-3">
        {(view?.tags.length ?? 0) > 0 && (
          <div className="flex flex-col gap-1">
            <Label htmlFor="lib-tag">{t("lib.tag")}</Label>
            <Select value={tag || "__all"} onValueChange={(v) => setTag(v === "__all" ? "" : v)}>
              <SelectTrigger id="lib-tag" className="h-7 w-40 text-tiny">
                <SelectValue placeholder={t("lib.anyTag")} />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="__all">{t("lib.anyTag")}</SelectItem>
                {view?.tags.map((t) => (
                  <SelectItem key={t} value={t}>
                    {t}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        )}
        <div className="flex flex-col gap-1">
          <Label htmlFor="lib-since">{t("lib.from")}</Label>
          <Input
            id="lib-since"
            type="date"
            className="tnum h-7 w-36 font-mono text-tiny"
            value={since}
            onChange={(e) => setSince(e.target.value)}
          />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="lib-until">{t("lib.to")}</Label>
          <Input
            id="lib-until"
            type="date"
            className="tnum h-7 w-36 font-mono text-tiny"
            value={until}
            onChange={(e) => setUntil(e.target.value)}
          />
        </div>
        <label className="flex items-center gap-2 pb-1 text-tiny">
          <Switch checked={onlyTranscribed} onCheckedChange={setOnlyTranscribed} />
          {t("lib.hasTranscript")}
        </label>
        {filtersActive && (
          <Button
            variant="ghost"
            size="sm"
            onClick={clearFilters}
          >
            {t("lib.clearFilters")}
          </Button>
        )}
        <span className="tnum ml-auto pb-1 font-mono text-tiny text-muted-foreground">
          {t(sessions.length === 1 ? "lib.sessionCountOne" : "lib.sessionCountMany", {
            n: sessions.length,
          })}
        </span>
      </div>

      {/* The reason the captions buttons are greyed used to be a paragraph
          here, ending in a config.yaml key. It is now the header's "Transcripts:
          off" status (app-council A38), which each greyed button names through
          aria-describedby, so it is read out where the control is. */}

      {/* ---------------- sessions ---------------- */}
      <div className="flex flex-col gap-2">
        {sessions.length === 0 &&
          (view?.sessions.length ? (
            <EmptyState
              icon={Search}
              title={t("lib.noSessionMatch")}
              action={filtersActive ? { label: t("lib.clearFilters"), onClick: clearFilters } : undefined}
            />
          ) : (
            <EmptyState
              icon={Film}
              title={t("lib.noRecordingsTitle")}
              body={t("lib.nothingRecorded")}
            />
          ))}
        {sessions.map((s) => (
          <SessionCard
            key={s.id}
            session={s}
            expanded={expanded === s.id}
            jobsAvailable={view?.jobsAvailable ?? false}
            transcribeAvailable={view?.transcribeAvailable ?? false}
            transcriptsOffId={transcriptsOff ? transcriptsOffId : undefined}
            onToggle={() => setExpanded(expanded === s.id ? null : s.id)}
            onEdit={() => setEditing(s)}
            onPlay={(recordingId) => setPlayer({ recordingId })}
            onChanged={load}
          />
        ))}

        {(view?.ungrouped.length ?? 0) > 0 && (
          <Card>
            <CardHeader>
              <CardTitle>{t("lib.ungrouped")}</CardTitle>
            </CardHeader>
            <CardContent className="px-0 pb-0">
              <RecordingList
                recordings={view?.ungrouped ?? []}
                jobsAvailable={view?.jobsAvailable ?? false}
                transcribeAvailable={view?.transcribeAvailable ?? false}
                transcriptsOffId={transcriptsOff ? transcriptsOffId : undefined}
                onPlay={(id) => setPlayer({ recordingId: id })}
                onChanged={load}
              />
            </CardContent>
          </Card>
        )}
      </div>

      {player && (
        <PlayerDialog
          target={player}
          onClose={() => setPlayer(null)}
          jobsAvailable={view?.jobsAvailable ?? false}
          transcribeAvailable={view?.transcribeAvailable ?? false}
          transcribeNote={view?.transcribeNote}
          onChanged={load}
        />
      )}

      {editing && (
        <SessionEditor
          session={editing}
          busy={busy}
          onClose={() => setEditing(null)}
          onSave={(patch) => saveSession(editing.id, patch)}
        />
      )}
    </div>
  );
}

// ------------------------------------------------------------- search hits

function HitRow({
  hit,
  markers,
  onOpen,
}: {
  hit: TranscriptHit;
  markers: [string, string];
  onOpen: () => void;
}) {
  return (
    <li>
      <button
        type="button"
        onClick={onOpen}
        className="flex w-full flex-col gap-0.5 px-3 py-2 text-left transition-colors hover:bg-accent"
      >
        <div className="flex flex-wrap items-baseline gap-2">
          {hit.speaker && (
            <span className="text-tiny font-semibold text-primary">{hit.speaker}</span>
          )}
          <span className="tnum font-mono text-micro text-muted-foreground">
            {offsetLabel(hit.startMs)}
          </span>
          <span className="truncate font-mono text-micro text-muted-foreground">
            {hit.recording}
          </span>
          <span className="ml-auto shrink-0 text-micro text-muted-foreground">
            {timestamp(hit.at)}
          </span>
        </div>
        <p className="text-sm leading-relaxed">{highlight(hit.snippet, markers)}</p>
        {hit.context && (
          <p className="line-clamp-2 text-tiny leading-relaxed text-muted-foreground">
            {hit.context}
          </p>
        )}
      </button>
    </li>
  );
}

// ---------------------------------------------------------------- sessions

function SessionCard({
  session,
  expanded,
  jobsAvailable,
  transcribeAvailable,
  transcriptsOffId,
  onToggle,
  onEdit,
  onPlay,
  onChanged,
}: {
  session: LibrarySession;
  expanded: boolean;
  jobsAvailable: boolean;
  transcribeAvailable: boolean;
  /** The header's "Transcripts: off" status, which a greyed captions button
   *  names as its reason. */
  transcriptsOffId?: string;
  onToggle: () => void;
  onEdit: () => void;
  onPlay: (recordingId: number) => void;
  onChanged: () => void;
}) {
  const t = useT();
  const [recordings, setRecordings] = useState<LibraryRecording[] | null>(null);
  const [loading, setLoading] = useState(false);

  const loadMembers = useCallback(async () => {
    setLoading(true);
    try {
      const res = await api.librarySession(session.id);
      setRecordings(res.recordings);
    } catch (err) {
      toast.error(errText(err, t("lib.couldNotLoadTheSession")));
    } finally {
      setLoading(false);
    }
  }, [session.id, t]);

  useEffect(() => {
    if (expanded) void loadMembers();
  }, [expanded, loadMembers]);

  // The contact sheet is the session's face: it shows the shape of the whole
  // broadcast rather than one arbitrary frame. Absent until the thumbnails job
  // has run, and its absence is a placeholder rather than a broken image.
  const poster =
    session.posterRecordingId && session.posterFile
      ? api.libraryMediaUrl(session.posterRecordingId, session.posterFile)
      : "";

  return (
    <Card>
      <CardHeader className="flex-row items-start gap-3">
        <button
          type="button"
          onClick={onToggle}
          aria-expanded={expanded}
          aria-label={expanded ? t("lib.collapseSession") : t("lib.expandSession")}
          className="mt-0.5 text-muted-foreground hover:text-foreground"
        >
          {expanded ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
        </button>

        <div className="hidden h-12 w-20 shrink-0 overflow-hidden rounded border border-border bg-card-raised sm:block">
          {poster ? (
            <img src={poster} alt="" className="h-full w-full object-cover" loading="lazy" />
          ) : (
            <div className="flex h-full w-full items-center justify-center">
              <ImageIcon className="h-4 w-4 text-muted-foreground" />
            </div>
          )}
        </div>

        <div className="min-w-0 flex-1">
          <CardTitle className="truncate">{session.displayTitle}</CardTitle>
          <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-tiny text-muted-foreground">
            <span className="tnum font-mono">{timestamp(session.startedAt)}</span>
            <span className="tnum font-mono">{shortDuration(session.durationMs)}</span>
            <span className="tnum font-mono">{bytes(session.bytes)}</span>
            <span>
              {t(session.recordings === 1 ? "lib.segmentCountOne" : "lib.segmentCountMany", {
                n: session.recordings,
              })}
            </span>
            {session.transcribed > 0 && (
              <Badge variant="armed" title={t("lib.searchableOnly")}>
                <Captions className="h-2.5 w-2.5" />
                {session.transcribed}/{session.recordings}
              </Badge>
            )}
            {session.auto && (
              <Badge variant="outline" title={t("lib.autoGrouped")}>
                {t("lib.autoBadge")}
              </Badge>
            )}
            {(session.tags ?? []).map((t) => (
              <Badge key={t} variant="default">
                {t}
              </Badge>
            ))}
          </div>
          {session.description && (
            <p className="mt-1 line-clamp-2 text-tiny text-muted-foreground">
              {session.description}
            </p>
          )}
        </div>

        <Button variant="ghost" size="icon-sm" onClick={onEdit} aria-label={t("lib.editSession")}>
          <Pencil />
        </Button>
      </CardHeader>

      {expanded && (
        <CardContent className="px-0 pb-0">
          {loading && !recordings ? (
            <div className="flex justify-center py-6">
              <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
            </div>
          ) : (
            <RecordingList
              recordings={recordings ?? []}
              jobsAvailable={jobsAvailable}
              transcribeAvailable={transcribeAvailable}
              transcriptsOffId={transcriptsOffId}
              onPlay={onPlay}
              onChanged={() => {
                void loadMembers();
                onChanged();
              }}
            />
          )}
        </CardContent>
      )}
    </Card>
  );
}

function SessionEditor({
  session,
  busy,
  onClose,
  onSave,
}: {
  session: LibrarySession;
  busy: boolean;
  onClose: () => void;
  onSave: (patch: { title: string; description: string; tags: string[] }) => void;
}) {
  const t = useT();
  const [title, setTitle] = useState(session.title);
  const [description, setDescription] = useState(session.description);
  const [tags, setTags] = useState((session.tags ?? []).join(", "));

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("lib.session")}</DialogTitle>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          <div className="flex flex-col gap-1">
            <Label htmlFor="sess-title">{t("lib.sessionTitle")}</Label>
            <Input
              id="sess-title"
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              placeholder={session.displayTitle}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="sess-desc">{t("lib.sessionDescription")}</Label>
            <Textarea
              id="sess-desc"
              rows={3}
              value={description}
              onChange={(e) => setDescription(e.target.value)}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="sess-tags">{t("lib.sessionTags")}</Label>
            <Input
              id="sess-tags"
              value={tags}
              onChange={(e) => setTags(e.target.value)}
              placeholder={t("lib.tagsPlaceholder")}
            />
          </div>
          {session.auto && (
            <p className="text-tiny text-muted-foreground">
            {t("lib.autoGroupedNote")}
            </p>
          )}
          <div className="flex justify-end gap-2">
            <Button variant="ghost" size="sm" onClick={onClose}>
              {t("common.cancel")}
            </Button>
            <Button
              size="sm"
              disabled={busy}
              onClick={() =>
                onSave({
                  title: title.trim(),
                  description: description.trim(),
                  tags: tags
                    .split(",")
                    .map((t) => t.trim())
                    .filter(Boolean),
                })
              }
            >
              {t("common.save")}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}

// -------------------------------------------------------------- recordings

function RecordingList({
  recordings,
  jobsAvailable,
  transcribeAvailable,
  transcriptsOffId,
  onPlay,
  onChanged,
}: {
  recordings: LibraryRecording[];
  jobsAvailable: boolean;
  transcribeAvailable: boolean;
  transcriptsOffId?: string;
  onPlay: (id: number) => void;
  onChanged: () => void;
}) {
  const t = useT();
  const [busy, setBusy] = useState(0);
  // Above the early return: a hook after a conditional return runs in a
  // different order on the render where the list is empty, which is a crash
  // waiting for the first operator whose library happens to be empty.
  const confirmDelete = useConfirm<LibraryRecording>();

  if (recordings.length === 0) {
    return (
      <EmptyState title={t("lib.noSegments")} className="m-3 border-0 py-4" />
    );
  }

  const submit = async (rec: LibraryRecording, kind: keyof typeof JOB_TOASTS) => {
    const say = JOB_TOASTS[kind];
    setBusy(rec.id);
    try {
      const res = await api.submitRecordingJob(rec.id, kind);
      // created=false means the queue folded this into work already running,
      // which is what stops a double click doing the job twice.
      toast.success(t(res.created ? say.queued : say.already));
      onChanged();
    } catch (err) {
      toast.error(errText(err, t(say.failed)));
    } finally {
      setBusy(0);
    }
  };

  const remove = async (rec: LibraryRecording) => {
    setBusy(rec.id);
    try {
      await api.deleteRecording(rec.id);
      toast.success(t("lib.recordingDeleted"));
      onChanged();
    } catch (err) {
      toast.error(errText(err, t("rec.deleteFailed")));
    } finally {
      setBusy(0);
    }
  };

  return (
    <ul className="divide-y divide-border">
      {recordings.map((r) => {
        const working = busy === r.id;
        const active = r.activeJobs ?? [];
        return (
          <li key={r.id} className="flex flex-wrap items-center gap-2 px-3 py-2">
            <button
              type="button"
              onClick={() => onPlay(r.id)}
              className="flex min-w-0 flex-1 items-center gap-2 text-left"
              title={r.assets.proxy ? t("lib.playProxy") : t("lib.noProxyYet")}
            >
              {/* app-council A40: a play glyph, not the Film icon that also
                  meant "generate proxy" two buttons to the right. */}
              <CirclePlay
                aria-hidden="true"
                className={cn(
                  "h-4 w-4 shrink-0",
                  r.assets.proxy ? "text-primary" : "text-muted-foreground",
                )}
              />
              <span className="min-w-0">
                <span className="block truncate font-mono text-tiny">
                  {r.title || r.filename}
                </span>
                <span className="tnum block font-mono text-micro text-muted-foreground">
                  {timestamp(r.startedAt)} · {r.durationMs > 0 ? shortDuration(r.durationMs) : "—"} ·{" "}
                  {bytes(r.bytes)}
                  {r.tracks > 0 ? ` · ${t("lib.trackCount", { n: r.tracks })}` : ""}
                </span>
              </span>
            </button>

            <div className="flex shrink-0 items-center gap-1">
              {r.hasTranscript && (
                <Badge variant="armed" title={t("lib.searchable")}>
                  <Captions className="h-2.5 w-2.5" />
                </Badge>
              )}
              {active.map((j) => (
                <Badge key={j.id} variant={j.blocked ? "warn" : "live"}>
                  {j.label ?? j.kind}
                  {j.state === "running" ? ` ${Math.round(j.progress * 100)}%` : ""}
                </Badge>
              ))}
            </div>

            <div className="flex shrink-0 items-center gap-0.5">
              {jobsAvailable && (
                <>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    disabled={working || !transcribeAvailable}
                    onClick={() => submit(r, "transcribe")}
                    aria-label={t("lib.transcribe")}
                    aria-describedby={transcribeAvailable ? undefined : transcriptsOffId}
                    title={transcribeAvailable ? t("lib.transcribeEachTrackOnIts") : undefined}
                  >
                    <Captions />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    disabled={working}
                    onClick={() => submit(r, "media.proxy")}
                    aria-label={t("lib.generateProxy")}
                    title={t("lib.generateProxyTitle")}
                  >
                    <FileVideoCamera />
                  </Button>
                  <Button variant="ghost" size="icon-sm" asChild aria-label={t("lib.clip")}>
                    {/* The clip editor owns in and out points, keyframes and
                        drift. Linking there beats a second, worse cut UI here. */}
                    <Link to={`/clips/${r.id}`} title={t("lib.clipTitle")}>
                      <Scissors />
                    </Link>
                  </Button>
                </>
              )}
              <Button variant="ghost" size="icon-sm" asChild aria-label={t("lib.downloadMaster")}>
                <a href={api.downloadUrl(r.id)} download title={t("lib.downloadMasterTitle")}>
                  <Download />
                </a>
              </Button>
              <Button
                variant="ghost"
                size="icon-sm"
                disabled={working}
                onClick={() => confirmDelete.ask(r)}
                aria-label={t("lib.delete")}
                className="hover:text-down"
              >
                <Trash2 />
              </Button>
            </div>
            {/* app-council A40: why a job is waiting, as row text. It was a
                title on a badge, which touch and keyboard users never see. */}
            {active
              .filter((j) => j.blocked && j.reason)
              .map((j) => (
                <p key={`why-${j.id}`} className="w-full text-sm text-warn">
                  {t("lib.jobBlocked", { job: j.label ?? j.kind, reason: j.reason ?? "" })}
                </p>
              ))}
          </li>
        );
      })}
      <ConfirmDestructive
        open={confirmDelete.open}
        onOpenChange={confirmDelete.onOpenChange}
        subject={confirmDelete.target?.filename ?? ""}
        title={t("lib.deleteTitle")}
        description={t("lib.deleteDescription")}
        requireTyping
        onConfirm={async () => {
          if (confirmDelete.target) await remove(confirmDelete.target);
        }}
      />
    </ul>
  );
}

/** One track's speaker name. A typed field, so it is a draft with an explicit
 *  Apply and a save status (useSaveModel), not a blur save: a blur save lost a
 *  name typed into a dialog that was then closed, and said nothing when it
 *  did write one (A95, the rule app-council A20 applies to Playout). */
function SpeakerField({
  track,
  speaker,
  busy,
  onSave,
}: {
  track: number;
  speaker: string;
  busy: boolean;
  /** Throws on failure. */
  onSave: (speaker: string) => Promise<void>;
}) {
  const t = useT();
  const model = useSaveModel<string>({
    stored: speaker,
    onSave: (v) => onSave(v.trim()),
  });
  return (
    <>
      <Input
        value={model.draft}
        placeholder={t("lib.speakerPlaceholder")}
        aria-label={t("lib.speakerFor", { n: track })}
        disabled={busy}
        className="h-6 flex-1 text-tiny"
        onChange={(e) => model.setDraft(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && model.dirty && !e.nativeEvent.isComposing) void model.save();
        }}
      />
      {model.dirty && (
        <Button size="sm" variant="secondary" className="h-6" disabled={busy} onClick={() => void model.save()}>
          {t("common.apply")}
        </Button>
      )}
      <SaveStatus state={model.state} />
    </>
  );
}

// ----------------------------------------------------------------- player

/** The inline player, and the transcript beside it.
 *
 *  It plays the PROXY. Browsers cannot play the multitrack MKV masters at all,
 *  so when the proxy is missing the honest thing is to offer to generate one —
 *  not to render a <video> that will sit there black forever.
 *
 *  Exported for its test, like ShareCard and VariantsCard are for PlayoutPage's. */
export function PlayerDialog({
  target,
  onClose,
  jobsAvailable,
  transcribeAvailable,
  transcribeNote,
  onChanged,
}: {
  target: PlayerTarget;
  onClose: () => void;
  jobsAvailable: boolean;
  transcribeAvailable: boolean;
  transcribeNote?: string;
  onChanged: () => void;
}) {
  const t = useT();
  const [rec, setRec] = useState<LibraryRecording | null>(null);
  const [transcript, setTranscript] = useState<TranscriptView | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const [atMs, setAtMs] = useState(target.atMs ?? 0);
  // #785: deleteTranscript and setTranscriptSpeaker were both defined on the
  // client (api.ts:1025, api.ts:1032) and never called from anywhere in ui/.
  // A transcript that whisper.cpp got wrong -- the wrong language, a track
  // transcribed against the wrong speaker's mic -- was permanent from this
  // screen: there was no delete, and so no way to ask it to try again either.
  // Scoped per TRACK, not per recording, because setTranscriptSpeaker already
  // is (`track 2` becomes `Ana`) and a bad transcript is almost always one
  // mic's, not the whole multitrack take's.
  const confirmDeleteTrack = useConfirm<TranscriptTrack>();

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const res = await api.libraryRecording(target.recordingId);
      setRec(res.recording);
      if (res.recording.hasTranscript) {
        setTranscript(await api.transcript(target.recordingId));
      } else {
        setTranscript(null);
      }
    } catch (err) {
      toast.error(errText(err, t("clipedit.couldNotOpenThatRecording")));
    } finally {
      setLoading(false);
    }
  }, [target.recordingId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const seek = (ms: number) => {
    setAtMs(ms);
    const v = videoRef.current;
    if (v) {
      v.currentTime = ms / 1000;
      void v.play().catch(() => {
        // Autoplay refusal is not an error worth a toast: the user has the
        // controls and the position is already correct.
      });
    }
  };

  const queueProxy = async () => {
    setBusy(true);
    try {
      const res = await api.submitRecordingJob(target.recordingId, "media.proxy");
      toast.success(res.created ? t("lib.proxyQueued") : t("lib.proxyAlreadyQueued"));
      onChanged();
    } catch (err) {
      toast.error(errText(err, t("lib.couldNotQueueTheProxy")));
    } finally {
      setBusy(false);
    }
  };

  const saveSpeaker = async (track: number, speaker: string) => {
    setBusy(true);
    try {
      await api.setTranscriptSpeaker(target.recordingId, track, speaker);
      toast.success(t("lib.speakerSaved"));
      await load();
    } catch (err) {
      toast.error(errText(err, t("lib.couldNotSaveSpeaker")));
      // Thrown on, so SpeakerField's draft stays and says "Not saved".
      throw err;
    } finally {
      setBusy(false);
    }
  };

  const deleteTrackTranscript = async (tr: TranscriptTrack) => {
    setBusy(true);
    try {
      await api.deleteTranscript(target.recordingId, tr.track);
      toast.success(t("lib.transcriptDeleted"));
      await load();
    } catch (err) {
      toast.error(errText(err, t("lib.couldNotDeleteTranscript")));
    } finally {
      setBusy(false);
    }
  };

  // Re-runs the SAME job RecordingList's captions button submits -- there is
  // no separate "retry" endpoint, and there does not need to be one: the
  // queue already folds a resubmit into work already running (submit()
  // above), so hitting this after a delete is indistinguishable from
  // transcribing for the first time.
  const retranscribe = async () => {
    setBusy(true);
    try {
      const res = await api.submitRecordingJob(target.recordingId, "transcribe");
      toast.success(
        res.created ? t("lib.transcriptionQueued") : t("lib.transcriptionAlreadyQueued"),
      );
      onChanged();
    } catch (err) {
      toast.error(errText(err, t("lib.couldNotQueueTranscription")));
    } finally {
      setBusy(false);
    }
  };

  const merged = transcript?.merged ?? [];
  const tracks = transcript?.transcript.tracks ?? [];

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-4xl">
        <DialogHeader>
          <DialogTitle className="truncate font-mono text-sm">
            {rec?.title || rec?.filename || t("lib.recordingFallback")}
          </DialogTitle>
        </DialogHeader>

        {loading ? (
          <div className="flex h-40 items-center justify-center">
            <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
          </div>
        ) : (
          <div className="grid gap-3 md:grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)]">
            <div className="flex flex-col gap-2">
              {rec?.assets.proxy ? (
                <video
                  ref={videoRef}
                  className="w-full rounded border border-border bg-black"
                  controls
                  preload="metadata"
                  poster={
                    rec.assets.poster
                      ? api.libraryMediaUrl(rec.id, "poster.jpg")
                      : undefined
                  }
                  src={`${api.libraryMediaUrl(rec.id, "proxy.mp4")}#t=${(atMs / 1000).toFixed(2)}`}
                  onLoadedMetadata={(e) => {
                    if (atMs > 0) e.currentTarget.currentTime = atMs / 1000;
                  }}
                >
                  <track kind="captions" />
                </video>
              ) : (
                <div className="flex flex-col items-center gap-2 rounded border border-dashed border-border-strong px-3 py-8 text-center">
                  <Film className="h-5 w-5 text-muted-foreground" />
                  <p className="text-sm text-muted-foreground">
                    {t("lib.noProxyForRecording")}
                  </p>
                  {jobsAvailable ? (
                    <Button size="sm" disabled={busy} onClick={queueProxy}>
                      <Film />
                      {t("lib.generateProxyButton")}
                    </Button>
                  ) : (
                    <p className="text-tiny text-muted-foreground">
                      {t("lib.noJobQueue")}
                    </p>
                  )}
                </div>
              )}

              <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-tiny text-muted-foreground">
                {rec && (
                  <>
                    <span className="tnum font-mono">{timestamp(rec.startedAt)}</span>
                    <span className="tnum font-mono">{shortDuration(rec.durationMs)}</span>
                    <span className="tnum font-mono">{bytes(rec.bytes)}</span>
                    {rec.tracks > 0 && <span>{t("lib.audioTracksInMaster", { n: rec.tracks })}</span>}
                    <a
                      className="ml-auto text-primary hover:underline"
                      href={api.downloadUrl(rec.id)}
                      download
                    >
            {t("lib.downloadMaster")}
                    </a>
                  </>
                )}
              </div>
            </div>

            {/* --- the transcript, which is the reason to open this at all --- */}
            <div className="flex flex-col gap-2">
              {/* #785: per-track speaker naming and deletion, and a way to
                  re-run. Before this, a track transcribed against the wrong
                  mic had no fix from here at all -- deleteTranscript and
                  setTranscriptSpeaker existed on the client and were never
                  called, so a wrong transcript was permanent and an
                  unlabelled speaker stayed unlabelled forever. */}
              {tracks.length > 0 && (
                <div className="flex flex-col gap-1 rounded border border-border p-2">
                  {tracks.map((tr) => (
                    <div key={tr.track} className="flex items-center gap-1.5">
                      <span className="shrink-0 font-mono text-micro text-muted-foreground">
                        {t("lib.trackLabel", { n: tr.track })}
                      </span>
                      <SpeakerField
                        track={tr.track}
                        speaker={tr.speaker ?? ""}
                        busy={busy}
                        onSave={(v) => saveSpeaker(tr.track, v)}
                      />
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        disabled={busy}
                        aria-label={t("lib.deleteTranscriptTrack", { n: tr.track })}
                        onClick={() => confirmDeleteTrack.ask(tr)}
                      >
                        <Trash2 />
                      </Button>
                    </div>
                  ))}
                  <div>
                    <Button
                      variant="ghost"
                      size="sm"
                      disabled={busy || !transcribeAvailable}
                      onClick={retranscribe}
                      title={
                        transcribeAvailable
                          ? t("lib.transcribeEachTrackOnIts")
                          : t("lib.transcriptionUnavailableReason", {
                            note: transcribeNote ?? t("lib.whisperNotFound"),
                          })
                      }
                    >
                      <Captions />
                      {t("lib.transcribe")}
                    </Button>
                  </div>
                </div>
              )}

              <div className="flex max-h-[28rem] flex-col gap-1 overflow-y-auto rounded border border-border p-2">
                {merged.length === 0 ? (
                  <p className="py-6 text-center text-tiny text-muted-foreground">
                    {t("lib.noTranscriptYet")}
                  </p>
                ) : (
                  merged.map((seg) => {
                    const current = atMs >= seg.startMs && atMs < seg.endMs;
                    return (
                      <button
                        key={seg.id}
                        type="button"
                        onClick={() => seek(seg.startMs)}
                        className={cn(
                          "rounded px-1.5 py-1 text-left text-tiny leading-relaxed transition-colors hover:bg-accent",
                          current && "bg-primary-dim",
                        )}
                      >
                        <span className="flex items-baseline gap-2">
                          {seg.speaker && (
                            <span className="shrink-0 font-semibold text-primary">
                              {seg.speaker}
                            </span>
                          )}
                          <span className="tnum shrink-0 font-mono text-micro text-muted-foreground">
                            {offsetLabel(seg.startMs)}
                          </span>
                        </span>
                        <span className="block">{seg.text}</span>
                      </button>
                    );
                  })
                )}
              </div>
            </div>
          </div>
        )}
      </DialogContent>

      <ConfirmDestructive
        open={confirmDeleteTrack.open}
        onOpenChange={confirmDeleteTrack.onOpenChange}
        subject={
          confirmDeleteTrack.target
            ? t("lib.trackLabel", { n: confirmDeleteTrack.target.track })
            : ""
        }
        title={t("lib.deleteTranscriptTitle", { n: confirmDeleteTrack.target?.track ?? 0 })}
        description={t("lib.deleteTranscriptDescription")}
        confirmLabel={t("lib.deleteTranscript")}
        onConfirm={async () => {
          if (confirmDeleteTrack.target) await deleteTrackTranscript(confirmDeleteTrack.target);
        }}
      />
    </Dialog>
  );
}
