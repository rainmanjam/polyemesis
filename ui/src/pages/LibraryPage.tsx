import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router";
import { toast } from "sonner";
import { ConfirmDestructive } from "@/components/ConfirmDestructive";
import { useConfirm } from "@/hooks/useConfirm";
import {
  Captions,
  ChevronDown,
  ChevronRight,
  Download,
  Film,
  Image as ImageIcon,
  Loader2,
  Pencil,
  Scissors,
  Search,
  Sparkles,
  Trash2,
  X,
} from "lucide-react";
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
  TranscriptView,
} from "@/lib/types";

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

export function LibraryPage() {
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

  const load = useCallback(async () => {
    try {
      setView(await api.library());
    } catch (err) {
      toast.error(errText(err, "Could not load the library."));
    } finally {
      setLoading(false);
    }
  }, []);

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
    const t = window.setTimeout(() => {
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
          toast.error(errText(err, "Search failed."));
        })
        .finally(() => setSearching(false));
    }, SEARCH_DEBOUNCE_MS);
    return () => window.clearTimeout(t);
  }, [query, order, speaker, since, until]);

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
      toast.success("Session saved.");
      setEditing(null);
      await load();
    } catch (err) {
      toast.error(errText(err, "Could not save the session."));
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

  return (
    <div className="p-3">
      <PageHeader
        title="Library"
        subtitle="Every broadcast, searchable. Each microphone was recorded on its own track, so the transcript already knows who said it."
        actions={
          <Button
            variant="outline"
            size="sm"
            disabled={busy}
            onClick={async () => {
              setBusy(true);
              try {
                const r = await api.regroupSessions();
                toast.success(
                  `Regrouped: ${r.created} new, ${r.assigned} segments assigned, ${r.extended} extended.`,
                );
                await load();
              } catch (err) {
                toast.error(errText(err, "Could not regroup."));
              } finally {
                setBusy(false);
              }
            }}
            title="Re-runs the grouping. Additive and idempotent: it never merges a grouping you split by hand."
          >
            <Sparkles />
            Regroup
          </Button>
        }
      />

      {/* Uploads sit above search because they are the one thing on this page
          an operator ADDS rather than searches. Everything below is a view of
          what the server already recorded. */}
      <div className="mb-3">
        <MediaUploads />
      </div>

      {/* ---------------- search: the top of the page, not a filter drawer ---- */}
      <Card className="mb-3">
        <CardContent className="flex flex-col gap-2 py-3">
          <div className="flex items-center gap-2">
            <Search className="h-4 w-4 shrink-0 text-muted-foreground" />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search everything anyone said, across every recording…"
              className="h-9 border-0 bg-transparent text-[13px] focus-visible:ring-0"
              aria-label="Search transcripts"
            />
            {searching && <Loader2 className="h-3.5 w-3.5 animate-spin text-muted-foreground" />}
            {query && (
              <Button
                variant="ghost"
                size="icon-sm"
                onClick={() => setQuery("")}
                aria-label="Clear search"
              >
                <X />
              </Button>
            )}
          </div>

          {query.trim().length >= 2 && (
            <div className="flex flex-wrap items-center gap-2 border-t border-border pt-2">
              <Select value={order} onValueChange={(v) => setOrder(v as TranscriptOrder)}>
                <SelectTrigger className="h-7 w-36 text-[11px]">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="relevance">Best match</SelectItem>
                  <SelectItem value="recent">Most recent</SelectItem>
                  <SelectItem value="time">Oldest first</SelectItem>
                </SelectContent>
              </Select>
              {(view?.speakers.length ?? 0) > 0 && (
                <Select value={speaker || "__all"} onValueChange={(v) => setSpeaker(v === "__all" ? "" : v)}>
                  <SelectTrigger className="h-7 w-40 text-[11px]">
                    <SelectValue placeholder="Any speaker" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="__all">Any speaker</SelectItem>
                    {view?.speakers.map((s) => (
                      <SelectItem key={s} value={s}>
                        {s}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
              {results && (
                <span className="tnum ml-auto font-mono text-[11px] text-muted-foreground">
                  {results.total < 0
                    ? `${results.hits.length} shown`
                    : `${results.hits.length} of ${results.total}`}
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
              <div className="px-3 py-8 text-center text-[12px] text-muted-foreground">
                Nothing said matches that.
                {!view?.transcribeAvailable && (
                  <>
                    {" "}
                    Transcription is unavailable on this machine
                    {view?.transcribeNote ? `: ${view.transcribeNote}` : ""}.
                  </>
                )}
              </div>
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
            <Label htmlFor="lib-tag">Tag</Label>
            <Select value={tag || "__all"} onValueChange={(v) => setTag(v === "__all" ? "" : v)}>
              <SelectTrigger id="lib-tag" className="h-7 w-40 text-[11px]">
                <SelectValue placeholder="Any tag" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="__all">Any tag</SelectItem>
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
          <Label htmlFor="lib-since">From</Label>
          <Input
            id="lib-since"
            type="date"
            className="tnum h-7 w-36 font-mono text-[11px]"
            value={since}
            onChange={(e) => setSince(e.target.value)}
          />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="lib-until">To</Label>
          <Input
            id="lib-until"
            type="date"
            className="tnum h-7 w-36 font-mono text-[11px]"
            value={until}
            onChange={(e) => setUntil(e.target.value)}
          />
        </div>
        <label className="flex items-center gap-2 pb-1 text-[11px]">
          <Switch checked={onlyTranscribed} onCheckedChange={setOnlyTranscribed} />
          Has a transcript
        </label>
        {filtersActive && (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              setTag("");
              setSince("");
              setUntil("");
              setOnlyTranscribed(false);
            }}
          >
            Clear filters
          </Button>
        )}
        <span className="tnum ml-auto pb-1 font-mono text-[11px] text-muted-foreground">
          {sessions.length} session{sessions.length === 1 ? "" : "s"}
        </span>
      </div>

      {/* ---------------- sessions ---------------- */}
      <div className="flex flex-col gap-2">
        {sessions.length === 0 && (
          <Card>
            <CardContent className="py-8 text-center text-[12px] text-muted-foreground">
              {view?.sessions.length
                ? "No session matches those filters."
                : "Nothing recorded yet. Sessions appear as the recorder writes segments."}
            </CardContent>
          </Card>
        )}
        {sessions.map((s) => (
          <SessionCard
            key={s.id}
            session={s}
            expanded={expanded === s.id}
            jobsAvailable={view?.jobsAvailable ?? false}
            transcribeAvailable={view?.transcribeAvailable ?? false}
            transcribeNote={view?.transcribeNote}
            onToggle={() => setExpanded(expanded === s.id ? null : s.id)}
            onEdit={() => setEditing(s)}
            onPlay={(recordingId) => setPlayer({ recordingId })}
            onChanged={load}
          />
        ))}

        {(view?.ungrouped.length ?? 0) > 0 && (
          <Card>
            <CardHeader>
              <CardTitle>Ungrouped segments</CardTitle>
            </CardHeader>
            <CardContent className="px-0 pb-0">
              <RecordingList
                recordings={view?.ungrouped ?? []}
                jobsAvailable={view?.jobsAvailable ?? false}
                transcribeAvailable={view?.transcribeAvailable ?? false}
                transcribeNote={view?.transcribeNote}
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
            <span className="text-[11px] font-semibold text-primary">{hit.speaker}</span>
          )}
          <span className="tnum font-mono text-[10px] text-muted-foreground">
            {offsetLabel(hit.startMs)}
          </span>
          <span className="truncate font-mono text-[10px] text-muted-foreground">
            {hit.recording}
          </span>
          <span className="ml-auto shrink-0 text-[10px] text-muted-foreground">
            {timestamp(hit.at)}
          </span>
        </div>
        <p className="text-[12px] leading-relaxed">{highlight(hit.snippet, markers)}</p>
        {hit.context && (
          <p className="line-clamp-2 text-[11px] leading-relaxed text-muted-foreground">
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
  transcribeNote,
  onToggle,
  onEdit,
  onPlay,
  onChanged,
}: {
  session: LibrarySession;
  expanded: boolean;
  jobsAvailable: boolean;
  transcribeAvailable: boolean;
  transcribeNote?: string;
  onToggle: () => void;
  onEdit: () => void;
  onPlay: (recordingId: number) => void;
  onChanged: () => void;
}) {
  const [recordings, setRecordings] = useState<LibraryRecording[] | null>(null);
  const [loading, setLoading] = useState(false);

  const loadMembers = useCallback(async () => {
    setLoading(true);
    try {
      const res = await api.librarySession(session.id);
      setRecordings(res.recordings);
    } catch (err) {
      toast.error(errText(err, "Could not load the session's segments."));
    } finally {
      setLoading(false);
    }
  }, [session.id]);

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
          aria-label={expanded ? "Collapse session" : "Expand session"}
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
          <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
            <span className="tnum font-mono">{timestamp(session.startedAt)}</span>
            <span className="tnum font-mono">{shortDuration(session.durationMs)}</span>
            <span className="tnum font-mono">{bytes(session.bytes)}</span>
            <span>
              {session.recordings} segment{session.recordings === 1 ? "" : "s"}
            </span>
            {session.transcribed > 0 && (
              <Badge variant="armed" title="Segments with a transcript, so they are searchable">
                <Captions className="h-2.5 w-2.5" />
                {session.transcribed}/{session.recordings}
              </Badge>
            )}
            {session.auto && (
              <Badge variant="outline" title="Grouped automatically. Editing it makes it yours.">
                auto
              </Badge>
            )}
            {(session.tags ?? []).map((t) => (
              <Badge key={t} variant="default">
                {t}
              </Badge>
            ))}
          </div>
          {session.description && (
            <p className="mt-1 line-clamp-2 text-[11px] text-muted-foreground">
              {session.description}
            </p>
          )}
        </div>

        <Button variant="ghost" size="icon-sm" onClick={onEdit} aria-label="Edit session">
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
              transcribeNote={transcribeNote}
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
  const [title, setTitle] = useState(session.title);
  const [description, setDescription] = useState(session.description);
  const [tags, setTags] = useState((session.tags ?? []).join(", "));

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>Session</DialogTitle>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          <div className="flex flex-col gap-1">
            <Label htmlFor="sess-title">Title</Label>
            <Input
              id="sess-title"
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              placeholder={session.displayTitle}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="sess-desc">Description</Label>
            <Textarea
              id="sess-desc"
              rows={3}
              value={description}
              onChange={(e) => setDescription(e.target.value)}
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="sess-tags">Tags</Label>
            <Input
              id="sess-tags"
              value={tags}
              onChange={(e) => setTags(e.target.value)}
              placeholder="comma, separated"
            />
          </div>
          {session.auto && (
            <p className="text-[11px] text-muted-foreground">
              This session was grouped automatically. Saving makes it yours, and
              the grouping pass will stop rewriting it.
            </p>
          )}
          <div className="flex justify-end gap-2">
            <Button variant="ghost" size="sm" onClick={onClose}>
              Cancel
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
              Save
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
  transcribeNote,
  onPlay,
  onChanged,
}: {
  recordings: LibraryRecording[];
  jobsAvailable: boolean;
  transcribeAvailable: boolean;
  transcribeNote?: string;
  onPlay: (id: number) => void;
  onChanged: () => void;
}) {
  const [busy, setBusy] = useState(0);
  // Above the early return: a hook after a conditional return runs in a
  // different order on the render where the list is empty, which is a crash
  // waiting for the first operator whose library happens to be empty.
  const confirmDelete = useConfirm<LibraryRecording>();

  if (recordings.length === 0) {
    return (
      <div className="px-3 py-6 text-center text-[12px] text-muted-foreground">
        No segments here.
      </div>
    );
  }

  const submit = async (rec: LibraryRecording, kind: string, label: string) => {
    setBusy(rec.id);
    try {
      const res = await api.submitRecordingJob(rec.id, kind);
      // created=false means the queue folded this into work already running,
      // which is what stops a double click doing the job twice.
      toast.success(res.created ? `${label} queued.` : `${label} was already queued.`);
      onChanged();
    } catch (err) {
      toast.error(errText(err, `Could not queue ${label.toLowerCase()}.`));
    } finally {
      setBusy(0);
    }
  };

  const remove = async (rec: LibraryRecording) => {
    setBusy(rec.id);
    try {
      await api.deleteRecording(rec.id);
      toast.success("Recording deleted.");
      onChanged();
    } catch (err) {
      toast.error(errText(err, "Could not delete the recording."));
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
              title={r.assets.proxy ? "Play the proxy" : "No proxy yet — open to generate one"}
            >
              <Film
                className={cn(
                  "h-3.5 w-3.5 shrink-0",
                  r.assets.proxy ? "text-primary" : "text-muted-foreground",
                )}
              />
              <span className="min-w-0">
                <span className="block truncate font-mono text-[11px]">
                  {r.title || r.filename}
                </span>
                <span className="tnum block font-mono text-[10px] text-muted-foreground">
                  {timestamp(r.startedAt)} · {r.durationMs > 0 ? shortDuration(r.durationMs) : "—"} ·{" "}
                  {bytes(r.bytes)}
                  {r.tracks > 0 ? ` · ${r.tracks} tracks` : ""}
                </span>
              </span>
            </button>

            <div className="flex shrink-0 items-center gap-1">
              {r.hasTranscript && (
                <Badge variant="armed" title="Searchable">
                  <Captions className="h-2.5 w-2.5" />
                </Badge>
              )}
              {active.map((j) => (
                <Badge key={j.id} variant={j.blocked ? "warn" : "live"} title={j.reason ?? ""}>
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
                    onClick={() => submit(r, "transcribe", "Transcription")}
                    aria-label="Transcribe"
                    title={
                      transcribeAvailable
                        ? "Transcribe each track on its own — that is what gives speaker attribution"
                        : `Transcription unavailable: ${transcribeNote ?? "whisper.cpp was not found"}`
                    }
                  >
                    <Captions />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    disabled={working}
                    onClick={() => submit(r, "media.proxy", "Proxy")}
                    aria-label="Generate proxy"
                    title="Generate the browser-playable proxy"
                  >
                    <Film />
                  </Button>
                  <Button variant="ghost" size="icon-sm" asChild aria-label="Clip">
                    {/* The clip editor owns in and out points, keyframes and
                        drift. Linking there beats a second, worse cut UI here. */}
                    <Link to={`/clips/${r.id}`} title="Cut a clip from this recording">
                      <Scissors />
                    </Link>
                  </Button>
                </>
              )}
              <Button variant="ghost" size="icon-sm" asChild aria-label="Download master">
                <a href={api.downloadUrl(r.id)} download title="Download the multitrack master">
                  <Download />
                </a>
              </Button>
              <Button
                variant="ghost"
                size="icon-sm"
                disabled={working}
                onClick={() => confirmDelete.ask(r)}
                aria-label="Delete"
                className="hover:text-down"
              >
                <Trash2 />
              </Button>
            </div>
          </li>
        );
      })}
      <ConfirmDestructive
        open={confirmDelete.open}
        onOpenChange={confirmDelete.onOpenChange}
        subject={confirmDelete.target?.filename ?? ""}
        title="Delete this recording?"
        description="The file is removed from disk, along with its proxies, thumbnails and transcript. This cannot be undone."
        requireTyping
        onConfirm={async () => {
          if (confirmDelete.target) await remove(confirmDelete.target);
        }}
      />
    </ul>
  );
}

// ----------------------------------------------------------------- player

/** The inline player, and the transcript beside it.
 *
 *  It plays the PROXY. Browsers cannot play the multitrack MKV masters at all,
 *  so when the proxy is missing the honest thing is to offer to generate one —
 *  not to render a <video> that will sit there black forever. */
function PlayerDialog({
  target,
  onClose,
  jobsAvailable,
  onChanged,
}: {
  target: PlayerTarget;
  onClose: () => void;
  jobsAvailable: boolean;
  onChanged: () => void;
}) {
  const [rec, setRec] = useState<LibraryRecording | null>(null);
  const [transcript, setTranscript] = useState<TranscriptView | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const [atMs, setAtMs] = useState(target.atMs ?? 0);

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
      toast.error(errText(err, "Could not open that recording."));
    } finally {
      setLoading(false);
    }
  }, [target.recordingId]);

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
      toast.success(res.created ? "Proxy queued." : "A proxy is already queued.");
      onChanged();
    } catch (err) {
      toast.error(errText(err, "Could not queue the proxy."));
    } finally {
      setBusy(false);
    }
  };

  const merged = transcript?.merged ?? [];

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-4xl">
        <DialogHeader>
          <DialogTitle className="truncate font-mono text-[12px]">
            {rec?.title || rec?.filename || "Recording"}
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
                  <p className="text-[12px] text-muted-foreground">
                    No proxy for this recording yet. Browsers cannot play the
                    multitrack MKV master, so there is nothing to show until one
                    is generated.
                  </p>
                  {jobsAvailable ? (
                    <Button size="sm" disabled={busy} onClick={queueProxy}>
                      <Film />
                      Generate a proxy
                    </Button>
                  ) : (
                    <p className="text-[11px] text-muted-foreground">
                      No job queue is running on this server, so one cannot be
                      generated here.
                    </p>
                  )}
                </div>
              )}

              <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
                {rec && (
                  <>
                    <span className="tnum font-mono">{timestamp(rec.startedAt)}</span>
                    <span className="tnum font-mono">{shortDuration(rec.durationMs)}</span>
                    <span className="tnum font-mono">{bytes(rec.bytes)}</span>
                    {rec.tracks > 0 && <span>{rec.tracks} audio tracks in the master</span>}
                    <a
                      className="ml-auto text-primary hover:underline"
                      href={api.downloadUrl(rec.id)}
                      download
                    >
                      Download master
                    </a>
                  </>
                )}
              </div>
            </div>

            {/* --- the transcript, which is the reason to open this at all --- */}
            <div className="flex max-h-[28rem] flex-col gap-1 overflow-y-auto rounded border border-border p-2">
              {merged.length === 0 ? (
                <p className="py-6 text-center text-[11px] text-muted-foreground">
                  No transcript for this recording yet.
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
                        "rounded px-1.5 py-1 text-left text-[11px] leading-relaxed transition-colors hover:bg-accent",
                        current && "bg-primary-dim",
                      )}
                    >
                      <span className="flex items-baseline gap-2">
                        {seg.speaker && (
                          <span className="shrink-0 font-semibold text-primary">
                            {seg.speaker}
                          </span>
                        )}
                        <span className="tnum shrink-0 font-mono text-[10px] text-muted-foreground">
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
        )}
      </DialogContent>
    </Dialog>
  );
}
