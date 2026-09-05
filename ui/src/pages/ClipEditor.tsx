import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type MouseEvent as ReactMouseEvent,
} from "react";
import { Link, useParams } from "react-router";
import { toast } from "sonner";
import {
  ArrowLeft,
  ChevronLeft,
  Download,
  Loader2,
  Pause,
  Play,
  Scissors,
  Search,
  ZoomIn,
  ZoomOut,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { PageHeader } from "@/components/AppLayout";
import { Stat } from "@/components/signature/Stat";
import { Timeline, type TimelineSegmentMark } from "@/components/signature/Timeline";
import {
  driftText,
  parseSpriteVTT,
  parseTimecode,
  timecode,
  type SpriteCue,
} from "@/lib/timeline";
import { autoApi } from "@/lib/autoApi";
import { bytes } from "@/lib/format";
import { cn } from "@/lib/utils";
import { useT } from "@/lib/i18n";

/* ===========================================================================
   The clip editor.

   It sets an in-point and an out-point and queues an export. It is NOT a
   browser NLE: there is no second track, nothing to arrange, no transitions.
   Everything on this page serves one sentence — "cut me that bit out".

   Three things make it worth building rather than shipping two number fields:

   - It tells the truth about the cut. A fast cut can only start on a keyframe,
     so the delivered in-point moves; the timeline draws where it will land and
     the panel says how far, before anything runs.
   - It cuts by TRACK. Every microphone was recorded on its own track, so
     "clip just the mic" is a checkbox rather than an export to an editor.
   - It cuts by TRANSCRIPT. Each track was transcribed in isolation, which
     gives speaker attribution with no diarization model at all, so finding the
     moment is a text search and selecting the words sets the range.
   =========================================================================== */

// ------------------------------------------------------------------- the API

interface ClipSegment {
  recordingId: number;
  name: string;
  startMs: number;
  durationMs: number;
  proxy?: string;
  poster?: string;
  spriteVtt?: string;
  mediaBase: string;
  missing?: boolean;
}

interface ClipTrack {
  index: number;
  label?: string;
  role?: string;
  language?: string;
  speaker?: string;
}

interface ClipSource {
  recordingId: number;
  recording: string;
  sessionId?: number;
  sessionName?: string;
  startedAt: string;
  durationMs: number;
  segments: ClipSegment[];
  tracks: ClipTrack[];
  hasTranscript: boolean;
  maxClipSeconds: number;
  modes: string[];
}

interface ClipKeyframes {
  fromMs: number;
  toMs: number;
  known: boolean;
  timesMs: number[] | null;
  warnings?: string[];
}

interface ClipPlan {
  mode: string;
  requestedMode: string;
  requestedInMs: number;
  requestedOutMs: number;
  inMs: number;
  outMs: number;
  durationMs: number;
  inDriftMs: number;
  driftKnown: boolean;
  keyframeMs: number;
  reEncodedMs: number;
  losslessFraction: number;
  segments: number;
  concat: boolean;
  videoEncoder?: string;
  outName: string;
  describe: string;
  warnings?: string[];
}

interface TranscriptLine {
  recordingId: number;
  track: number;
  speaker?: string;
  startMs: number;
  endMs: number;
  text: string;
}

interface ClipTranscript {
  segments: TranscriptLine[] | null;
  speakers: string[] | null;
  tracks: number[] | null;
}

/** The shape internal/api/jobs.go returns. Only the fields this page reads. */
interface ClipJob {
  id: number;
  state: string;
  progress: number;
  error?: string;
  blocked?: boolean;
  reason?: string;
  result?: {
    path?: string;
    bytes?: number;
    mode?: string;
    inDriftMs?: number;
    driftKnown?: boolean;
    reEncodedMs?: number;
    warnings?: string[] | null;
  } | null;
  createdAt: string;
}

const errText = (err: unknown, fallback: string) =>
  err instanceof Error && err.message ? err.message : fallback;

// ------------------------------------------------------------------ constants

/** The fine nudge. The recording's real frame rate is not indexed anywhere, so
 *  this is named in milliseconds everywhere it is shown rather than being
 *  called "a frame" and quietly being wrong on a 25 or 60 fps source. It is one
 *  frame at 30 fps, which is the right order of magnitude for every source. */
const FINE_STEP_MS = 33;
const COARSE_STEP_MS = 1000;

/** Mirrors maxKeyframeWindow in internal/api/clips.go. Past this the server
 *  trims the window, so asking would draw ticks over the first five minutes of
 *  the span and nothing after — which reads as "the keyframes stop here". */
const KEYFRAME_MAX_SPAN_MS = 300000;

/** Zoom stops for the detail track, in visible seconds. */
const ZOOM_STOPS_MS = [2000, 5000, 15000, 30000, 60000, 180000, 600000, 1800000];
const DEFAULT_ZOOM_INDEX = 4;

/** Shuttle speeds L and J walk through, the way a deck does. */
const SHUTTLE_SPEEDS = [1, 2, 4, 8];

export function ClipEditor() {
  const t = useT();
  const params = useParams();
  const recordingId = Number(params.id);

  const [source, setSource] = useState<ClipSource | null>(null);
  const [loadError, setLoadError] = useState("");
  const [loading, setLoading] = useState(true);

  const [inMs, setInMs] = useState(0);
  const [outMs, setOutMs] = useState(10000);
  const [playheadMs, setPlayheadMs] = useState(0);
  const [zoom, setZoom] = useState(DEFAULT_ZOOM_INDEX);
  const [viewStartMs, setViewStartMs] = useState(0);

  const [mode, setMode] = useState("fast");
  const [audioAll, setAudioAll] = useState(true);
  const [tracks, setTracks] = useState<number[]>([]);
  const [container, setContainer] = useState("mkv");
  const [title, setTitle] = useState("");

  const [keyframes, setKeyframes] = useState<ClipKeyframes | null>(null);
  const [sprites, setSprites] = useState<SpriteCue[]>([]);
  const [plan, setPlan] = useState<ClipPlan | null>(null);
  const [planError, setPlanError] = useState("");
  const [planning, setPlanning] = useState(false);

  const [transcript, setTranscript] = useState<TranscriptLine[]>([]);
  const [filter, setFilter] = useState("");

  const [jobs, setJobs] = useState<ClipJob[]>([]);
  const [exporting, setExporting] = useState(false);

  const videoRef = useRef<HTMLVideoElement>(null);
  const [shuttle, setShuttle] = useState(0);
  const [activeSegment, setActiveSegment] = useState(0);

  const durationMs = source?.durationMs ?? 0;
  const spanMs = Math.min(ZOOM_STOPS_MS[zoom], Math.max(1000, durationMs));
  const viewEndMs = viewStartMs + spanMs;

  // ------------------------------------------------------------------ loading

  useEffect(() => {
    if (!Number.isFinite(recordingId)) {
      setLoadError(t("clipedit.thatIsNotARecording"));
      setLoading(false);
      return;
    }
    let cancelled = false;
    autoApi
      .get<ClipSource>(`/clipper/recordings/${recordingId}`)
      .then((s) => {
        if (cancelled) return;
        setSource(s);
        // A ten-second default cut at the top of the recording: something is
        // already selected, so the first thing an operator does is drag it
        // rather than work out how to create one.
        setOutMs(Math.min(10000, Math.max(1000, s.durationMs)));
        setTitle(s.recording.replace(/\.[^.]+$/, ""));
      })
      .catch((err) => {
        if (!cancelled) setLoadError(errText(err, t("clipedit.couldNotOpenThatRecording")));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [recordingId, t]);

  useEffect(() => {
    if (!source?.hasTranscript) return;
    let cancelled = false;
    autoApi
      .get<ClipTranscript>(`/clipper/recordings/${recordingId}/transcript`)
      .then((t) => {
        if (!cancelled) setTranscript(t.segments ?? []);
      })
      // A transcript that will not load costs the transcript panel, not the
      // editor. The cut works without it.
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, [recordingId, source?.hasTranscript]);

  // Sprite thumbnails, one VTT per segment, each shifted onto the stitched
  // timeline. Fetched with a plain fetch because a .vtt is not JSON.
  useEffect(() => {
    if (!source) return;
    let cancelled = false;
    const wanted = source.segments.filter((s) => s.spriteVtt);
    Promise.all(
      wanted.map(async (seg) => {
        try {
          const resp = await fetch(seg.spriteVtt as string, { credentials: "same-origin" });
          if (!resp.ok) return [];
          return parseSpriteVTT(await resp.text(), seg.mediaBase, seg.startMs);
        } catch {
          return [];
        }
      }),
    ).then((all) => {
      if (!cancelled) setSprites(all.flat());
    });
    return () => {
      cancelled = true;
    };
  }, [source]);

  // Keyframes for the visible span only. The server caps the window, and a
  // scrubber that asked for four hours would read every byte of every file to
  // draw ticks nobody can see.
  const keyframesFetchable = spanMs <= KEYFRAME_MAX_SPAN_MS;
  useEffect(() => {
    if (!source) return;
    if (!keyframesFetchable) {
      setKeyframes(null);
      return;
    }
    const handle = window.setTimeout(() => {
      autoApi
        .get<ClipKeyframes>(
          `/clipper/recordings/${recordingId}/keyframes?fromMs=${Math.floor(
            viewStartMs,
          )}&toMs=${Math.ceil(viewEndMs)}`,
        )
        .then(setKeyframes)
        // Unknown keyframes are drawn as unknown, not as none. Failing the
        // page over a probe would be the restrictive answer.
        .catch(() => setKeyframes(null));
    }, 250);
    return () => window.clearTimeout(handle);
  }, [recordingId, source, viewStartMs, viewEndMs, keyframesFetchable]);

  // The plan is re-fetched whenever anything that changes the cut changes. It
  // is what the drift readout and the export button are both built on, so it
  // is deliberately the same call the export will make.
  const audioMode = audioAll ? "all" : "tracks";
  useEffect(() => {
    if (!source) return;
    if (outMs <= inMs) return;
    if (!audioAll && tracks.length === 0) {
      setPlan(null);
      setPlanError(t("clipedit.pickAtLeastOneAudio"));
      return;
    }
    const handle = window.setTimeout(() => {
      setPlanning(true);
      autoApi
        .post<ClipPlan>(`/clipper/recordings/${recordingId}/plan`, {
          inMs: Math.round(inMs),
          outMs: Math.round(outMs),
          mode,
          audioMode,
          tracks: audioAll ? undefined : tracks,
          title,
          container,
        })
        .then((p) => {
          setPlan(p);
          setPlanError("");
        })
        .catch((err) => {
          setPlan(null);
          setPlanError(errText(err, t("clipedit.thatCutCouldNotBe")));
        })
        .finally(() => setPlanning(false));
    }, 300);
    return () => window.clearTimeout(handle);
  }, [recordingId, source, inMs, outMs, mode, audioMode, audioAll, tracks, title, container, t]);

  const loadJobs = useCallback(() => {
    autoApi
      .get<{ jobs: ClipJob[] | null }>(
        `/jobs?kind=clip.export&recordingId=${recordingId}&limit=20`,
      )
      .then((r) => setJobs(r.jobs ?? []))
      // No queue on this server answers 503; the export button says so on its
      // own, and an empty history is the honest thing to show.
      .catch(() => undefined);
  }, [recordingId]);

  useEffect(() => {
    loadJobs();
  }, [loadJobs]);

  const anyActive = jobs.some((j) => j.state === "queued" || j.state === "running" || j.state === "deferred");
  useEffect(() => {
    if (!anyActive) return;
    const t = window.setInterval(loadJobs, 1500);
    return () => window.clearInterval(t);
  }, [anyActive, loadJobs]);

  // ---------------------------------------------------------------- transport

  // Memoised because every callback below depends on it: a fresh array each
  // render would re-arm the playback loop sixty times a second.
  const segments = useMemo(() => source?.segments ?? [], [source]);

  const segmentAt = useCallback(
    (ms: number) => {
      for (let i = 0; i < segments.length; i++) {
        const s = segments[i];
        if (ms >= s.startMs && ms < s.startMs + s.durationMs) return i;
      }
      return segments.length > 0 ? segments.length - 1 : -1;
    },
    [segments],
  );

  const seek = useCallback(
    (ms: number) => {
      const clamped = Math.max(0, Math.min(durationMs, ms));
      setPlayheadMs(clamped);
      const idx = segmentAt(clamped);
      if (idx < 0) return;
      const seg = segments[idx];
      const video = videoRef.current;
      if (!video) return;
      if (idx !== activeSegment) {
        // Switching files: the source change resets currentTime, so the seek
        // has to wait for the new metadata.
        setActiveSegment(idx);
        const offset = (clamped - seg.startMs) / 1000;
        const once = () => {
          video.currentTime = offset;
          video.removeEventListener("loadedmetadata", once);
          // Playback that rolled off the end of one file continues into the
          // next rather than stopping at a boundary the operator never chose.
          if (shuttleRef.current > 0) void video.play().catch(() => undefined);
        };
        video.addEventListener("loadedmetadata", once);
        return;
      }
      video.currentTime = (clamped - seg.startMs) / 1000;
    },
    [durationMs, segmentAt, segments, activeSegment],
  );

  // The playhead follows the video while it plays. rAF rather than timeupdate,
  // which fires about four times a second and makes a scrubbing playhead look
  // like it is stuttering.
  const shuttleRef = useRef(0);
  useEffect(() => {
    shuttleRef.current = shuttle;
  }, [shuttle]);

  useEffect(() => {
    // Only while something is moving. A permanent 60 Hz loop on a page that
    // spends most of its life parked is a laptop fan for no reason.
    if (shuttle === 0) return;
    let raf = 0;
    let last = performance.now();
    const tick = (now: number) => {
      const dt = now - last;
      last = now;
      const video = videoRef.current;
      const seg = segments[activeSegment];
      if (video && seg) {
        if (shuttle < 0) {
          // No browser plays backwards, so reverse shuttle steps the current
          // time. Close enough to a deck to be usable for finding a moment.
          const next = Math.max(0, video.currentTime + (shuttle * dt) / 1000);
          video.currentTime = next;
          setPlayheadMs(seg.startMs + next * 1000);
        } else if (!video.paused) {
          setPlayheadMs(seg.startMs + video.currentTime * 1000);
        }
      }
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [segments, activeSegment, shuttle]);

  useEffect(() => {
    const video = videoRef.current;
    if (!video) return;
    if (shuttle > 0) {
      video.playbackRate = shuttle;
      void video.play().catch(() => undefined);
    } else {
      video.pause();
      video.playbackRate = 1;
    }
  }, [shuttle]);

  const nudge = useCallback(
    (delta: number) => {
      setShuttle(0);
      seek(playheadMs + delta);
    },
    [playheadMs, seek],
  );

  const shuttleForward = () =>
    setShuttle((s) => (s <= 0 ? 1 : SHUTTLE_SPEEDS[Math.min(SHUTTLE_SPEEDS.indexOf(s) + 1, SHUTTLE_SPEEDS.length - 1)]));
  const shuttleBack = () =>
    setShuttle((s) => (s >= 0 ? -1 : -SHUTTLE_SPEEDS[Math.min(SHUTTLE_SPEEDS.indexOf(-s) + 1, SHUTTLE_SPEEDS.length - 1)]));

  const markIn = useCallback(() => {
    setInMs(Math.min(playheadMs, outMs - 1));
  }, [playheadMs, outMs]);
  const markOut = useCallback(() => {
    setOutMs(Math.max(playheadMs, inMs + 1));
  }, [playheadMs, inMs]);

  // Broadcast keys. J/K/L and I/O are muscle memory for anybody who has ever
  // cut anything, and a tool that ignores them is a tool that feels wrong in
  // the first ten seconds.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement | null;
      if (el && (el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.isContentEditable)) {
        return;
      }
      switch (e.key.toLowerCase()) {
        case "j":
          e.preventDefault();
          shuttleBack();
          break;
        case "k":
          e.preventDefault();
          setShuttle(0);
          break;
        case "l":
          e.preventDefault();
          shuttleForward();
          break;
        case " ":
          e.preventDefault();
          setShuttle((s) => (s === 0 ? 1 : 0));
          break;
        case "i":
          e.preventDefault();
          markIn();
          break;
        case "o":
          e.preventDefault();
          markOut();
          break;
        case "arrowleft":
          e.preventDefault();
          nudge(-(e.shiftKey ? COARSE_STEP_MS : FINE_STEP_MS));
          break;
        case "arrowright":
          e.preventDefault();
          nudge(e.shiftKey ? COARSE_STEP_MS : FINE_STEP_MS);
          break;
        default:
          break;
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [markIn, markOut, nudge]);

  // The detail track follows the playhead rather than the other way round, so
  // scrubbing never walks off the edge of the visible window.
  useEffect(() => {
    setViewStartMs((start) => {
      const end = start + spanMs;
      if (playheadMs >= start + spanMs * 0.1 && playheadMs <= end - spanMs * 0.1) return start;
      const next = playheadMs - spanMs / 2;
      return Math.max(0, Math.min(Math.max(0, durationMs - spanMs), next));
    });
  }, [playheadMs, spanMs, durationMs]);

  // ---------------------------------------------------------------- transcript

  const visibleLines = useMemo(() => {
    const needle = filter.trim().toLowerCase();
    if (!needle) return transcript;
    return transcript.filter(
      (l) =>
        l.text.toLowerCase().includes(needle) || (l.speaker ?? "").toLowerCase().includes(needle),
    );
  }, [transcript, filter]);

  /** Selecting words sets the range. The DOM selection is mapped back to lines
   *  through the data attributes on each row, which is what makes "highlight
   *  the sentence, get a clip of the sentence" work without a custom text
   *  layer. */
  const onTranscriptMouseUp = () => {
    const sel = window.getSelection();
    if (!sel || sel.isCollapsed || sel.rangeCount === 0) return;
    const rows = Array.from(
      document.querySelectorAll<HTMLElement>("[data-line-start]"),
    ).filter((el) => sel.containsNode(el, true));
    if (rows.length === 0) return;
    const starts = rows.map((el) => Number(el.dataset.lineStart));
    const ends = rows.map((el) => Number(el.dataset.lineEnd));
    const from = Math.min(...starts);
    const to = Math.max(...ends);
    if (!Number.isFinite(from) || !Number.isFinite(to) || to <= from) return;
    setInMs(from);
    setOutMs(to);
    seek(from);
  };

  const clipLine = (line: TranscriptLine) => {
    setInMs(line.startMs);
    setOutMs(Math.max(line.endMs, line.startMs + 500));
    seek(line.startMs);
  };

  // ------------------------------------------------------------------ export

  const runExport = async () => {
    setExporting(true);
    try {
      await autoApi.post<ClipJob>(`/clipper/recordings/${recordingId}/export`, {
        inMs: Math.round(inMs),
        outMs: Math.round(outMs),
        mode,
        audioMode,
        tracks: audioAll ? undefined : tracks,
        title,
        container,
      });
      toast.success(t("clipedit.exportQueued"));
      loadJobs();
    } catch (err) {
      toast.error(errText(err, t("clipedit.couldNotQueueTheExport")));
    } finally {
      setExporting(false);
    }
  };

  // ------------------------------------------------------------------- render

  if (loading) {
    return (
      <div className="flex h-64 items-center justify-center">
        <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
      </div>
    );
  }
  if (!source) {
    return (
      <div className="p-3">
        <PageHeader title={t("clipedit.title")} subtitle={loadError || t("clipedit.thatRecordingIsNotAvailable")} />
        <Button variant="secondary" size="sm" asChild>
          <Link to="/library">
            <ArrowLeft /> Back to the library
          </Link>
        </Button>
      </div>
    );
  }

  const seg = segments[activeSegment];
  const marks: TimelineSegmentMark[] = segments.map((s) => ({
    startMs: s.startMs,
    endMs: s.startMs + s.durationMs,
    name: s.name,
    missing: s.missing,
  }));
  const snapInMs = plan && plan.driftKnown && plan.inMs !== plan.requestedInMs ? plan.inMs : null;
  const tooLong = (outMs - inMs) / 1000 > source.maxClipSeconds;

  return (
    <div className="p-3">
      <PageHeader
        title={t("clipedit.title")}
        subtitle={
          source.sessionName
            ? `${source.recording} — ${source.sessionName}`
            : source.recording
        }
        actions={
          <>
            <Badge variant="outline">{timecode(outMs - inMs)}</Badge>
            <Button variant="secondary" size="sm" asChild>
              <Link to="/library">
                <ArrowLeft /> Library
              </Link>
            </Button>
          </>
        }
      />

      <div className="grid gap-3 xl:grid-cols-[minmax(0,1fr)_22rem]">
        <div className="flex flex-col gap-3">
          {/* ---------- viewer ---------- */}
          <Card>
            <CardContent className="p-0">
              <div className="relative aspect-video w-full overflow-hidden rounded-t-md bg-black">
                {seg?.proxy ? (
                  <video
                    ref={videoRef}
                    src={seg.proxy}
                    poster={seg.poster}
                    preload="metadata"
                    playsInline
                    className="h-full w-full"
                    onClick={() => setShuttle((s) => (s === 0 ? 1 : 0))}
                    onEnded={() => {
                      const next = activeSegment + 1;
                      if (next < segments.length) seek(segments[next].startMs);
                      else setShuttle(0);
                    }}
                  />
                ) : (
                  <div className="flex h-full flex-col items-center justify-center gap-1 text-center">
                    <p className="text-[12px] text-muted-foreground">
                      No proxy has been generated for this segment.
                    </p>
                    <p className="text-[11px] text-subtle-foreground">
                      The cut still works — a proxy only gives you a picture to aim with. Generate
                      one from the library.
                    </p>
                  </div>
                )}
              </div>

              {/* ---------- transport ---------- */}
              <div className="flex flex-wrap items-center gap-1.5 border-t border-border p-2">
                <Button
                  size="sm"
                  variant={shuttle < 0 ? "default" : "secondary"}
                  onClick={shuttleBack}
                  title={t("clipedit.shuttleBack")}
                >
                  <ChevronLeft /> J
                </Button>
                <Button
                  size="sm"
                  variant={shuttle === 0 ? "default" : "secondary"}
                  onClick={() => setShuttle(0)}
                  title={t("clipedit.stop")}
                >
                  <Pause /> K
                </Button>
                <Button
                  size="sm"
                  variant={shuttle > 0 ? "default" : "secondary"}
                  onClick={shuttleForward}
                  title={t("clipedit.playFaster")}
                >
                  <Play /> L
                </Button>
                {shuttle !== 0 && (
                  <Badge variant="outline" className="tnum font-mono">
                    {/* A SIGN ON THE FORWARD SPEED. Both branches of this used
                        to be `${shuttle}x`, so the conditional decided nothing:
                        reverse already reads "-2x" from the negative number and
                        forward read a bare "2x", which is the same badge the
                        pause state would show if it were rendered. Shuttle runs
                        both ways and the direction is the thing the badge is
                        for. */}
                    {shuttle > 0 ? `+${shuttle}x` : `${shuttle}x`}
                  </Badge>
                )}

                <span className="mx-1 h-5 w-px bg-border" />

                <Button size="sm" variant="secondary" onClick={() => nudge(-FINE_STEP_MS)} title={t("clipedit.back33")}>
                  −33 ms
                </Button>
                <Button size="sm" variant="secondary" onClick={() => nudge(FINE_STEP_MS)} title={t("clipedit.forward33")}>
                  +33 ms
                </Button>
                <Button size="sm" variant="secondary" onClick={() => nudge(-COARSE_STEP_MS)}>
                  −1 s
                </Button>
                <Button size="sm" variant="secondary" onClick={() => nudge(COARSE_STEP_MS)}>
                  +1 s
                </Button>

                <span className="mx-1 h-5 w-px bg-border" />

                <Button size="sm" onClick={markIn} title={t("clipedit.markIn")}>
                  Mark in (I)
                </Button>
                <Button size="sm" onClick={markOut} title={t("clipedit.markOut")}>
                  Mark out (O)
                </Button>

                <div className="ml-auto flex items-center gap-1.5">
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label={t("clipedit.zoomOut")}
                    disabled={zoom >= ZOOM_STOPS_MS.length - 1}
                    onClick={() => setZoom((z) => Math.min(ZOOM_STOPS_MS.length - 1, z + 1))}
                  >
                    <ZoomOut />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label={t("clipedit.zoomIn")}
                    disabled={zoom <= 0}
                    onClick={() => setZoom((z) => Math.max(0, z - 1))}
                  >
                    <ZoomIn />
                  </Button>
                  <span className="tnum font-mono text-[11px] text-muted-foreground">
                    {timecode(playheadMs)}
                  </span>
                </div>
              </div>
            </CardContent>
          </Card>

          {/* ---------- timeline ---------- */}
          <Card>
            <CardContent className="flex flex-col gap-2 p-2">
              <Timeline
                variant="overview"
                viewStartMs={0}
                viewEndMs={Math.max(1000, durationMs)}
                inMs={inMs}
                outMs={outMs}
                playheadMs={playheadMs}
                segments={marks}
                windowMs={[viewStartMs, viewEndMs]}
                onSeek={seek}
                onMoveWindow={(start) =>
                  setViewStartMs(Math.max(0, Math.min(Math.max(0, durationMs - spanMs), start)))
                }
              />
              <Timeline
                variant="detail"
                viewStartMs={viewStartMs}
                viewEndMs={viewEndMs}
                inMs={inMs}
                outMs={outMs}
                playheadMs={playheadMs}
                segments={marks}
                sprites={sprites}
                keyframes={keyframes?.timesMs ?? []}
                // Zoomed out past what the server will probe, "unknown" would
                // be the wrong word: nobody asked. Only a probe that came back
                // empty earns the dashed "we cannot say" baseline.
                keyframesKnown={!keyframesFetchable || (keyframes?.known ?? false)}
                snapInMs={mode === "fast" ? snapInMs : null}
                onSeek={seek}
                onChangeIn={(ms) => setInMs(Math.max(0, ms))}
                onChangeOut={(ms) => setOutMs(Math.min(durationMs, ms))}
              />

              <div className="flex flex-wrap items-end gap-2">
                <TimecodeField label={t("clipedit.in")} valueMs={inMs} onCommit={(ms) => setInMs(Math.max(0, Math.min(ms, outMs - 1)))} />
                <TimecodeField label={t("clipedit.out")} valueMs={outMs} onCommit={(ms) => setOutMs(Math.max(inMs + 1, Math.min(ms, durationMs)))} />
                <div className="flex flex-col gap-1">
                  <Label className="text-[10px] uppercase tracking-wider text-muted-foreground">
                    Length
                  </Label>
                  <div className="tnum flex h-8 items-center rounded-md border border-border px-2 font-mono text-[12px]">
                    {timecode(Math.max(0, outMs - inMs))}
                  </div>
                </div>
                <p className="ml-auto max-w-md text-[10px] text-subtle-foreground">
            {t("clipedit.shortcuts")}
                </p>
              </div>
            </CardContent>
          </Card>

          {/* ---------- what the cut will actually do ---------- */}
          <PlanPanel
            plan={plan}
            planning={planning}
            error={planError}
            tooLong={tooLong}
            maxSeconds={source.maxClipSeconds}
          />
        </div>

        {/* ---------- right rail ---------- */}
        <div className="flex flex-col gap-3">
          <Card>
            <CardHeader>
              <CardTitle>{t("clipedit.cut")}</CardTitle>
            </CardHeader>
            <CardContent className="flex flex-col gap-3">
              <div className="flex flex-col gap-1">
                <Label className="text-[10px] uppercase tracking-wider text-muted-foreground">
                  Mode
                </Label>
                <div className="grid grid-cols-2 gap-1.5">
                  <ModeButton
                    active={mode === "fast"}
                    onClick={() => setMode("fast")}
                    title={t("clipedit.fast")}
                    body={t("clipedit.copiesEveryPacketSecondsBit")}
                  />
                  <ModeButton
                    active={mode === "precise"}
                    onClick={() => setMode("precise")}
                    title={t("clipedit.precise")}
                    body={t("clipedit.reEncodesOnlyTheLeading")}
                  />
                </div>
              </div>

              <div className="flex flex-col gap-1">
                <Label className="text-[10px] uppercase tracking-wider text-muted-foreground">
                  Audio
                </Label>
                <label className="flex items-center gap-2 text-[12px]">
                  <Checkbox
                    checked={audioAll}
                    onCheckedChange={(v) => setAudioAll(v === true)}
                    aria-label={t("clipedit.keepAllTracks")}
                  />
                  Every track, bit-exact
                </label>
                {!audioAll && (
                  <div className="mt-1 flex flex-col gap-1 rounded-md border border-border p-2">
                    {source.tracks.length === 0 ? (
                      <p className="text-[11px] text-muted-foreground">
                        This recording's track count was never measured, so there is nothing to pick
                        from. Take every track instead.
                      </p>
                    ) : (
                      source.tracks.map((t) => (
                        <label key={t.index} className="flex items-center gap-2 text-[12px]">
                          <Checkbox
                            checked={tracks.includes(t.index)}
                            onCheckedChange={(v) =>
                              setTracks((prev) =>
                                v === true
                                  ? [...prev, t.index].sort((a, b) => a - b)
                                  : prev.filter((n) => n !== t.index),
                              )
                            }
                            aria-label={`Audio track ${t.index + 1}`}
                          />
                          <span className="font-mono text-[11px]">{t.index + 1}</span>
                          <span className="truncate">
                            {t.speaker || t.label || `Track ${t.index + 1}`}
                          </span>
                          {t.role && (
                            <Badge variant="outline" className="ml-auto">
                              {t.role}
                            </Badge>
                          )}
                        </label>
                      ))
                    )}
                    <p className="text-[10px] text-subtle-foreground">
                      Selected tracks are copied, not mixed — the clip keeps them as separate
                      tracks, exactly as recorded.
                    </p>
                  </div>
                )}
              </div>

              <div className="flex flex-col gap-1">
                <Label htmlFor="clip-title">{t("clipedit.name")}</Label>
                <Input
                  id="clip-title"
                  value={title}
                  maxLength={80}
                  onChange={(e) => setTitle(e.target.value)}
                />
              </div>

              <div className="flex flex-col gap-1">
                <Label className="text-[10px] uppercase tracking-wider text-muted-foreground">
                  Container
                </Label>
                <div className="grid grid-cols-2 gap-1.5">
                  <ModeButton
                    active={container === "mkv"}
                    onClick={() => setContainer("mkv")}
                    title="MKV"
                    body={t("clipedit.keepsEveryAudioTrackIn")}
                  />
                  <ModeButton
                    active={container === "mp4"}
                    onClick={() => setContainer("mp4")}
                    title="MP4"
                    body={t("clipedit.whatASocialPlatformWill")}
                  />
                </div>
              </div>

              <Button
                onClick={runExport}
                disabled={exporting || !plan || tooLong || (!audioAll && tracks.length === 0)}
              >
                {exporting ? <Loader2 className="animate-spin" /> : <Scissors />}
                Export clip
              </Button>
              <p className="text-[10px] text-subtle-foreground">
            {t("clipedit.queueNote")}
              </p>
            </CardContent>
          </Card>

          {/* ---------- transcript ---------- */}
          <Card className="min-h-0">
            <CardHeader>
              <CardTitle className="flex items-center justify-between gap-2">
                Transcript
                {transcript.length > 0 && (
                  <Badge variant="outline">{transcript.length} lines</Badge>
                )}
              </CardTitle>
            </CardHeader>
            <CardContent className="flex flex-col gap-2">
              {!source.hasTranscript ? (
                <p className="text-[11px] text-muted-foreground">
            {t("clipedit.noTranscript")}
                </p>
              ) : (
                <>
                  <div className="relative">
                    <Search className="pointer-events-none absolute left-2 top-1/2 h-3 w-3 -translate-y-1/2 text-subtle-foreground" />
                    <Input
                      value={filter}
                      onChange={(e) => setFilter(e.target.value)}
                      placeholder={t("clipedit.findSaid")}
                      className="pl-7"
                      aria-label={t("clipedit.filterTranscript")}
                    />
                  </div>
                  <div
                    className="flex max-h-[28rem] flex-col gap-0.5 overflow-y-auto"
                    onMouseUp={onTranscriptMouseUp}
                  >
                    {visibleLines.length === 0 ? (
                      <p className="py-4 text-center text-[11px] text-muted-foreground">
                        Nothing matches.
                      </p>
                    ) : (
                      visibleLines.map((line, i) => (
                        <TranscriptRow
                          key={`${line.recordingId}-${line.track}-${line.startMs}-${i}`}
                          line={line}
                          selected={line.startMs >= inMs && line.endMs <= outMs}
                          onSeek={() => seek(line.startMs)}
                          onClip={() => clipLine(line)}
                        />
                      ))
                    )}
                  </div>
                  <p className="text-[10px] text-subtle-foreground">
            {t("clipedit.clickLine")}
                  </p>
                </>
              )}
            </CardContent>
          </Card>

          {/* ---------- exports ---------- */}
          <Card>
            <CardHeader>
              <CardTitle>{t("clipedit.exports")}</CardTitle>
            </CardHeader>
            <CardContent className="flex flex-col gap-2">
              {jobs.length === 0 ? (
                <p className="text-[11px] text-muted-foreground">{t("clipedit.nothingExported")}</p>
              ) : (
                jobs.map((j) => <JobRow key={j.id} job={j} />)
              )}
            </CardContent>
          </Card>
        </div>
      </div>
    </div>
  );
}

// ------------------------------------------------------------------ fragments

function ModeButton({
  active,
  onClick,
  title,
  body,
}: {
  active: boolean;
  onClick: () => void;
  title: string;
  body: string;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={cn(
        "rounded-md border p-2 text-left transition-colors",
        "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        active
          ? "border-primary bg-primary-dim text-foreground"
          : "border-border bg-card hover:bg-card-raised",
      )}
    >
      <div className="text-[12px] font-semibold">{title}</div>
      <div className="mt-0.5 text-[10px] leading-snug text-muted-foreground">{body}</div>
    </button>
  );
}

function TimecodeField({
  label,
  valueMs,
  onCommit,
}: {
  label: string;
  valueMs: number;
  onCommit: (ms: number) => void;
}) {
  const [draft, setDraft] = useState<string | null>(null);
  const shown = draft ?? timecode(valueMs);
  const commit = () => {
    if (draft === null) return;
    const parsed = parseTimecode(draft);
    // An unparseable entry reverts rather than jumping the cut to zero.
    if (parsed !== null) onCommit(parsed);
    setDraft(null);
  };
  return (
    <div className="flex flex-col gap-1">
      <Label className="text-[10px] uppercase tracking-wider text-muted-foreground">{label}</Label>
      <Input
        value={shown}
        aria-label={`${label} point`}
        className="tnum w-36 font-mono"
        onChange={(e) => setDraft(e.target.value)}
        onBlur={commit}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.currentTarget.blur();
          } else if (e.key === "Escape") {
            setDraft(null);
          }
        }}
      />
    </div>
  );
}

/** The honesty panel. Everything the cut will do that the user did not ask for
 *  is said here, in the units they chose the range in. */
function PlanPanel({
  plan,
  planning,
  error,
  tooLong,
  maxSeconds,
}: {
  plan: ClipPlan | null;
  planning: boolean;
  error: string;
  tooLong: boolean;
  maxSeconds: number;
}) {
  const t = useT();
  const drift = plan?.inDriftMs ?? 0;
  const moved = Boolean(plan?.driftKnown) && drift !== 0;

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-1.5">
          What this will do
          {planning && <Loader2 className="h-3 w-3 animate-spin text-muted-foreground" />}
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        {tooLong && (
          <p className="rounded-md border border-down bg-down-dim px-2 py-1.5 text-[11px]">
            That range is longer than the {Math.round(maxSeconds / 3600)} hour ceiling for one clip.
          </p>
        )}
        {error && !tooLong && (
          <p className="rounded-md border border-down bg-down-dim px-2 py-1.5 text-[11px]">
            {error}
          </p>
        )}

        {plan && (
          <>
            <p className="text-[12px]">{plan.describe}</p>
            <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
              <Stat
                labelKey="clipedit.starts"
                value={timecode(plan.inMs)}
                tone={moved ? "warn" : "default"}
              />
              <Stat
                labelKey="clipedit.startMoved"
                value={
                  !plan.driftKnown ? "unknown" : drift === 0 ? "not at all" : driftText(drift)
                }
                tone={!plan.driftKnown ? "warn" : moved ? "warn" : "default"}
              />
              <Stat
                labelKey="clipedit.copied"
                value={`${Math.round(plan.losslessFraction * 100)}%`}
                tone={plan.losslessFraction >= 0.999 ? "live" : "default"}
              />
              <Stat
                labelKey="clipedit.reencoded"
                value={plan.reEncodedMs > 0 ? `${plan.reEncodedMs} ms` : "nothing"}
                tone={plan.reEncodedMs > 0 ? "warn" : "live"}
              />
            </div>
            {plan.mode !== plan.requestedMode && (
              <p className="text-[11px] text-warn">
                Asked for {plan.requestedMode}, will run as {plan.mode}.
              </p>
            )}
            {plan.concat && (
              <p className="text-[11px] text-muted-foreground">
                The cut spans {plan.segments} recording files; they are joined before it is
                trimmed.
              </p>
            )}
            {(plan.warnings ?? []).map((wmsg) => (
              <p key={wmsg} className="text-[11px] text-warn">
                {wmsg}
              </p>
            ))}
          </>
        )}

        {plan && !plan.driftKnown && (
          <p className="text-[11px] text-warn">
            {t("clipedit.noKeyframes")}
          </p>
        )}
      </CardContent>
    </Card>
  );
}

function TranscriptRow({
  line,
  selected,
  onSeek,
  onClip,
}: {
  line: TranscriptLine;
  selected: boolean;
  onSeek: () => void;
  onClip: (e: ReactMouseEvent) => void;
}) {
  const t = useT();
  return (
    <div
      data-line-start={line.startMs}
      data-line-end={line.endMs}
      className={cn(
        "group flex cursor-pointer gap-2 rounded-[3px] px-1.5 py-1 text-[12px] transition-colors",
        selected ? "bg-primary-dim" : "hover:bg-card-raised",
      )}
      onClick={onSeek}
      // A KEYBOARD ROUTE TO THE SAME ACTION. This row seeks the player on
      // click and had no keyboard equivalent at all, so the transcript --
      // which is the fastest way to navigate a long recording -- was reachable
      // by pointer only. The same pattern MediaUploads.tsx already uses for its
      // drop zone: role, tab stop, and Enter/Space.
      //
      // The Scissors button inside stops propagation on its own click, so it
      // keeps its own behaviour and its own tab stop rather than being
      // swallowed by this one.
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          onSeek();
        }
      }}
      role="button"
      tabIndex={0}
      // NO aria-label ON PURPOSE. The row already contains its timecode, its
      // speaker and its text, so the accessible name computed from its content
      // is the line itself -- which is what somebody navigating the transcript
      // wants read out. A synthetic label would REPLACE that with something
      // shorter and less useful, and would need translating into twelve
      // locales to say less.
    >
      <span className="tnum shrink-0 font-mono text-[10px] text-subtle-foreground">
        {timecode(line.startMs).slice(0, 8)}
      </span>
      <span className="min-w-0 flex-1">
        {line.speaker && (
          <span className="mr-1 font-semibold text-armed">{line.speaker}:</span>
        )}
        {line.text}
      </span>
      <Button
        variant="ghost"
        size="icon-sm"
        aria-label={t("clipedit.clipThisLine")}
        title={t("clipedit.clipThisLineTitle")}
        className="opacity-0 transition-opacity group-hover:opacity-100"
        onClick={(e) => {
          e.stopPropagation();
          onClip(e);
        }}
      >
        <Scissors />
      </Button>
    </div>
  );
}

function JobRow({ job }: { job: ClipJob }) {
  const t = useT();
  const done = job.state === "done";
  const failed = job.state === "failed" || job.state === "cancelled";
  const res = job.result;
  return (
    <div className="flex flex-col gap-1 rounded-md border border-border p-2">
      <div className="flex items-center gap-2">
        <Badge variant={done ? "live" : failed ? "warn" : "outline"}>{job.state}</Badge>
        <span className="tnum font-mono text-[11px] text-muted-foreground">
          {done && res?.bytes ? bytes(res.bytes) : `${Math.round((job.progress ?? 0) * 100)}%`}
        </span>
        {done && (
          <Button variant="ghost" size="icon-sm" asChild aria-label={t("clipedit.downloadClip")}>
            <a href={`/api/v1/clipper/jobs/${job.id}/download`} download>
              <Download />
            </a>
          </Button>
        )}
      </div>
      {!done && !failed && (
        <div className="h-1 overflow-hidden rounded-full bg-muted">
          <div
            className="h-full bg-primary transition-[width]"
            style={{ width: `${Math.round((job.progress ?? 0) * 100)}%` }}
          />
        </div>
      )}
      {/* The reason is why a queued job is not running — "an ingest is live",
          "host cpu is above the ceiling". Without it a governed queue looks
          broken rather than polite. */}
      {job.blocked && job.reason && (
        <p className="text-[10px] text-muted-foreground">{job.reason}</p>
      )}
      {job.error && <p className="text-[10px] text-down">{job.error}</p>}
      {done && res?.driftKnown && (res.inDriftMs ?? 0) !== 0 && (
        <p className="text-[10px] text-warn">
          Started {driftText(res.inDriftMs ?? 0)} from where it was asked for.
        </p>
      )}
    </div>
  );
}
