package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/clipper"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/media"
	"github.com/rainmanjam/polyemesis/internal/playlistmedia"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
	"github.com/rainmanjam/polyemesis/internal/uploads"
)

// The post-production tier is assembled here and nowhere else.
//
// Nine packages implement it — the queue, the governor, transcription, derived
// media, the clip cutter, storage and the API — and every one of them is inert
// until something constructs it. A processor that is never registered with the
// queue does nothing at all, and a queue with no governor yields to nothing, so
// this file is the difference between a feature and a package.
//
// The governing principle is enforced by construction: the governor is built
// BEFORE the processors, and every processor's child commands are routed
// through its NiceCommand wrapper, so there is no way to add a heavy task that
// silently runs at the same OS priority as the encoder feeding the broadcast.

// postProd is everything the API needs handed to it.
type postProd struct {
	queue   *jobs.Queue
	gov     *jobs.Governor
	whisper *transcribe.Tools
}

// startPostProd builds the queue, the governor and every processor, and starts
// both loops.
//
// It never returns an error. Every part of this tier is optional by design:
// whisper may be absent, FFmpeg may lack an encoder, the machine may have no
// nice(1). None of those is a reason to refuse to serve a live stream, so a
// failure here degrades one feature and logs why. The one thing that can fail
// hard — the database — was already opened by the caller.
func startPostProd(ctx context.Context, log *slog.Logger, cfg config.Config, store *db.DB, eng *engine.Manager, tools *ffmpeg.Tools) postProd {
	settings, err := store.GetSettings()
	if err != nil {
		// Falling back to defaults rather than giving up: the stored policy is
		// how strictly this tier yields, and the default is the strictest one.
		log.Warn("cannot read the post-production policy; using defaults", "error", err)
		settings = db.DefaultSettings()
	}

	// Whisper is detected once, at startup, and a machine without it is an
	// ordinary machine. Every method on *Tools is nil-receiver safe precisely so
	// this cannot become a startup failure.
	whisper, werr := transcribe.Detect(ctx, cfg.Transcription.Binary)
	if werr != nil {
		log.Info("speech transcription unavailable", "reason", werr)
	} else {
		log.Info("whisper detected", "path", whisper.Binary, "version", whisper.Version)
	}

	q := newJobQueue(log, store, settings.PostProd.Concurrency, eng.Reconcile)

	// The governor comes first because the processors need its NiceCommand.
	gov := jobs.NewGovernor(log, q,
		jobs.WithPolicy(settings.PostProd.Policy()),
		jobs.WithSensors(jobs.Sensors{
			// IngestLive and GPUBusy aggregate across every programme: one
			// live source is reason enough to keep heavy background work off
			// the CPU. CPUPercent is system-wide whichever engine reports it,
			// so it comes off whichever one is default.
			IngestLive: eng.IngestLive,
			CPUPercent: func() float64 {
				d := eng.Default()
				if d == nil {
					return 0
				}
				return d.Monitor().System().CPUPercent
			},
			GPUBusy: eng.GPUBusy,
			Power:   jobs.ReadPowerState,
		}),
	)

	// The veto, not just the deferral. Without this the queue would claim a job
	// the instant it was submitted — and again the instant one finished — both
	// of which happen between two governor ticks, so the gate would only ever
	// be applied to work that had already started.
	q.SetAdmit(gov.MayStart)

	registerProcessors(log, cfg, store, gov, q, tools, whisper, settings.PostProd)

	// Registration before Run, always: Run calls Recover() before it claims
	// anything, and a kind claimed with no worker registered costs a tick.
	go q.Run(ctx)
	go gov.Run(ctx)

	// The stored history bound, applied once at startup. Nothing else calls
	// Purge, and a job row is tiny — but "tiny forever" is still a leak.
	// Re-read every sweep rather than captured here: see purgeJobHistoryLoop.
	exportDir := clipper.ExportDirIn(cfg.RecordingsDir())
	go purgeJobHistoryLoop(ctx, log, q, func() db.PostProdSettings {
		s, err := store.GetSettings()
		if err != nil {
			// Keep-forever is the safe answer to an unreadable settings row.
			// Purging on a guess would delete history the operator may have
			// asked to keep, and unlike a missed sweep that is not recoverable.
			log.Warn("cannot read job retention settings; skipping this sweep", "err", err)
			return db.PostProdSettings{}
		}
		return s.PostProd
	}, exportDir, jobPurgeEvery)

	// Existing installs get their sessions built from the recordings already
	// indexed, so upgrading does not look like losing your history. Idempotent
	// and cheap; the scanner keeps it current from here.
	backfillSessions(log, store, settings)

	// Live captions are opt-in and off after a restart, but the engine has to
	// know whether they are offerable at all. Passing NiceCommand is the
	// important half — without it whisper would compete with the encoders for
	// the CPU, which is the one thing this whole tier exists to prevent.
	eng.SetTranscriber(whisper, cfg.ModelsDir(), niceWrapper(gov))

	return postProd{queue: q, gov: gov, whisper: whisper}
}

// registerProcessors wires every worker into the queue.
//
// Each kind is registered UNCONDITIONALLY, even when the tool it needs is
// missing. A processor that fails a job with "whisper.cpp is not installed"
// tells an operator what to do; a button that silently does not exist tells
// them the feature is broken. This is the fail-open rule applied to a queue.
func registerProcessors(log *slog.Logger, cfg config.Config, store *db.DB, gov *jobs.Governor, q *jobs.Queue, tools *ffmpeg.Tools, whisper *transcribe.Tools, pp db.PostProdSettings) {
	reg := func(kind jobs.Kind, err error) {
		if err != nil {
			log.Error("cannot register a job kind", "kind", kind, "error", err)
		}
	}

	// The operator's model choice, when they have made one. Empty leaves the
	// hardware-derived default, which is what every install ran before this was
	// reachable -- transcribe.WithDefaultModel existed from the start and
	// nothing ever called it, so the guess was the only available answer.
	tpOpts := []transcribe.Option{transcribe.WithNice(niceWrapper(gov))}
	if m := strings.TrimSpace(pp.WhisperModel); m != "" {
		tpOpts = append(tpOpts, transcribe.WithDefaultModel(m))
		log.Info("transcription default model set by settings", "model", m)
	}
	tp := transcribe.NewProcessor(log, tools, whisper, cfg.RecordingsDir(), cfg.ModelsDir(), tpOpts...)
	// Concurrency 1: whisper saturates every core it is given, so a second
	// transcription buys nothing and costs the first one half the machine.
	reg(transcribe.KindTranscribe, q.Register(transcribe.KindTranscribe, 1, tp))

	mp := media.New(log, media.Config{
		FFmpeg:        tools.FFmpeg,
		FFprobe:       tools.FFprobe,
		RecordingsDir: cfg.RecordingsDir(),
		HasEncoder:    tools.HasEncoder,
		// Archive re-encodes a master and can replace it. It stays off until
		// there is a settings field for it; the most expensive and only
		// destructive job in the product does not get to default to on.
	}, media.WithExecer(niceExecer(gov)))
	if err := mp.Register(q); err != nil {
		log.Error("cannot register the derived-media job kinds", "error", err)
	}

	cutter := clipper.New(log, tools.FFmpeg, tools.FFprobe)
	resolve := recordingTimeline(store, cfg.RecordingsDir())
	reg(clipper.JobKind, q.Register(clipper.JobKind, 1, clipper.NewWorker(cutter, resolve)))

	// Playlist normalisation. THIS IS THE PRODUCER OF THE FILE THE PLAYLIST
	// TIER REFUSES TO START WITHOUT: engine.playlistItemsReady requires a
	// normalised derivative for every item, and this worker is the only thing
	// in the product that writes one. Registered here and submitted from the
	// settings handler, an unregistered kind would leave every playlist
	// permanently unavailable -- the spec's stated worst outcome -- while the
	// package sat in the tree looking complete.
	//
	// A store that cannot be built is passed through as nil rather than made a
	// startup failure, the same fail-open rule as everything else in this
	// function: the job then fails with "no upload store is configured", which
	// names the problem, where a kind that quietly does not exist does not.
	upStore, uerr := uploads.New(cfg.DataDir)
	if uerr != nil {
		log.Error("cannot open the uploads store; playlist items cannot be normalised", "error", uerr)
	}
	np := playlistmedia.New(log, playlistmedia.Config{
		FFmpeg:  tools.FFmpeg,
		FFprobe: tools.FFprobe,
		// The SAME DataDir string the engine builds DerivativePath from, not an
		// absolute-ised copy of it: the writer and the readiness check must
		// agree on the path byte for byte or the derivative lands somewhere the
		// gate never looks.
		DataDir: cfg.DataDir,
		Uploads: uploadResolver(upStore),
	}, playlistmedia.WithExecer(niceExecer(gov)))
	if err := np.Register(q); err != nil {
		log.Error("cannot register the playlist normalisation job kind", "error", err)
	}
}

// uploadResolver hands the processor its resolver, or nothing at all.
//
// A typed nil *uploads.Store assigned to the interface field would be non-nil
// as an interface and the worker's "no upload store is configured" guard would
// never fire -- it would panic on the first Resolve instead.
func uploadResolver(s *uploads.Store) playlistmedia.Resolver {
	if s == nil {
		return nil
	}
	return s
}

// newJobQueue builds the queue this tier runs on.
//
// A named constructor rather than a jobs.New call inlined in startPostProd, so
// that a test can build THE SAME QUEUE the server builds. Only the change hook
// makes that worth doing: it is a single line whose absence is invisible --
// everything still compiles, every job still runs, and the only symptom is a
// playlist that never comes back on air. A test that assembled its own queue
// with its own options would prove the callback works and prove nothing about
// whether the server ever attaches it, which is the exact shape of defect this
// sub-project has now produced four times.
func newJobQueue(log *slog.Logger, store *db.DB, concurrency int, reconcile func() error) *jobs.Queue {
	return jobs.New(log, store,
		jobs.WithConcurrency(concurrency),
		// A finished normalisation has to reach the engine, or the playlist tier
		// it unblocks stays off air until something unrelated happens to
		// reconcile. See reconcileOnNormalise.
		jobs.WithOnChange(reconcileOnNormalise(log, reconcile)),
	)
}

// reconcileOnNormalise turns a finished normalisation job into a reconcile.
//
// THE MISSING EDGE. engine.reconcilePlaylist refuses to start a tier until every
// item has a derivative, and it is reached only through Engine.Reconcile --
// which settings saves, the API, the manager and the scheduler call, and a
// finishing job does not. Without this a playlist whose last item finished
// transcoding stayed off air until an operator happened to save something else.
// The selector's own 500 ms sweep does not help: it calls sweepSelector, which
// never touches the playlist.
//
// Only StateDone, and only this kind. A failed or retrying job has written no
// derivative, so reconciling would re-read exactly the state that has just been
// refused; every other kind produces nothing the engine's configuration depends
// on. "Done" includes the reuse path, where the derivative was already there --
// that is still the answer the gate was waiting for.
//
// On its own goroutine because WithOnChange's contract is that the callback does
// not block: it is called from the queue's per-job goroutine, and a reconcile
// stops and starts supervised processes.
func reconcileOnNormalise(log *slog.Logger, reconcile func() error) func(jobs.Job) {
	return func(j jobs.Job) {
		if j.Kind != playlistmedia.KindNormalise || j.State != jobs.StateDone {
			return
		}
		go func() {
			if err := reconcile(); err != nil {
				log.Warn("a normalised playlist item did not reach the engine",
					"job", j.ID, "upload", j.Target, "error", err)
			}
		}()
	}
}

// niceWrapper adapts the governor's variadic NiceCommand to the slice-taking
// callback the transcription and caption paths accept. Two shapes of the same
// wrapper, not two policies.
func niceWrapper(gov *jobs.Governor) func(string, []string) (string, []string) {
	return func(name string, args []string) (string, []string) {
		return gov.NiceCommand(name, args...)
	}
}

// niceExecer routes every derived-media child through the governor's priority
// wrapper. nice(1) and ionice(1) EXEC their target rather than forking, so the
// child PID is still FFmpeg's and the supervisor's process-group kill is
// unaffected; both binaries are optional and absent means the command is
// returned untouched.
func niceExecer(gov *jobs.Governor) media.Execer {
	return func(ctx context.Context, cmd media.Command, sink media.Sink) error {
		name, args := gov.NiceCommand(cmd.Name, cmd.Args...)
		return media.Exec(ctx, media.Command{Name: name, Args: args}, sink)
	}
}

// recordingTimeline resolves a job's Target back to the files to cut from.
//
// The clip editor sends its segments inline, because the timeline it drew is
// the one the human chose their in and out points against. This is the fallback
// for a job submitted without them, and it deliberately answers with the single
// recording the target names rather than guessing at the session around it: an
// export that cuts more than the caller described is worse than one that
// refuses.
func recordingTimeline(store *db.DB, recordingsDir string) clipper.Resolver {
	return func(_ context.Context, target string) (clipper.Timeline, error) {
		id, ok := jobs.ParseRecordingTarget(target)
		if !ok {
			return clipper.Timeline{}, clipper.ErrNoSegments
		}
		rec, err := store.GetRecording(id)
		if err != nil {
			return clipper.Timeline{}, err
		}
		return clipper.NewTimeline([]clipper.Segment{{
			Path:     filepath.Join(recordingsDir, rec.Filename),
			Duration: time.Duration(rec.DurationMS) * time.Millisecond,
		}})
	}
}

// jobPurgeEvery is how often the retention bound is re-applied.
//
// An hour, where the recorder's equivalent sweep runs every 30 seconds, and the
// difference is the unit: recording retention is bounded in GB against a disk
// that fills, while this is bounded in DAYS against rows that are a few hundred
// bytes each. An hour is prompt enough that "I lowered retention" is something
// an operator sees happen, and rare enough that the query is invisible.
const jobPurgeEvery = time.Hour

// purgeJobHistoryLoop applies the retention bound now and every jobPurgeEvery
// after that, re-reading the settings each sweep.
//
// It takes a FUNCTION rather than a value, which is the whole point and the
// same shape recording.Manager.Run uses. Retention was read exactly once, at
// boot, so lowering it did nothing an operator could observe until they
// restarted -- the settings page said saved and the history did not move.
// Capturing the value here instead would move that bug one level up rather than
// fix it: from "read once at boot" to "read once when the loop started", which
// is the same instant.
//
// every is a parameter so the test can drive it without waiting an hour.
// exportDir is where clip exports live, so the sweep can delete the file a
// purged clip.export owned. It is a parameter rather than read off the engine
// because this loop must keep working when there is no clip export in sight,
// and an empty string simply means "purge rows, touch nothing on disk".
func purgeJobHistoryLoop(ctx context.Context, log *slog.Logger, q *jobs.Queue,
	settings func() db.PostProdSettings, exportDir string, every time.Duration) {
	// Once immediately, so a restart reflects whatever is stored rather than
	// waiting out the first tick.
	purgeJobHistory(log, q, settings(), exportDir)

	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			purgeJobHistory(log, q, settings(), exportDir)
		}
	}
}

// purgeJobHistory applies the stored retention bound. RetainDays 0 means "keep
// forever", which is why it is checked rather than turned into a zero cutoff
// that would delete the lot.
func purgeJobHistory(log *slog.Logger, q *jobs.Queue, p db.PostProdSettings, exportDir string) {
	if p.RetainDays <= 0 {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -p.RetainDays)
	purged, err := q.Purge(cutoff, p.RetainJobs)
	if err != nil {
		log.Warn("cannot purge the job history", "error", err)
		return
	}
	if len(purged) == 0 {
		return
	}
	// The scheduled half of #222, and the half that made it urgent: this runs
	// on a timer nobody is watching, so a clip export purged here is stranded
	// without anyone present to notice the disk did not go down.
	files := 0
	if exportDir != "" {
		for _, j := range purged {
			removed, err := clipper.RemoveExport(exportDir, j)
			if err != nil {
				log.Warn("purged a clip export job but could not delete its file",
					"job", j.ID, "error", err)
				continue
			}
			if removed {
				files++
			}
		}
	}
	log.Info("purged finished jobs", "count", len(purged),
		"exportFilesDeleted", files, "olderThanDays", p.RetainDays)
}

// backfillSessions groups the recordings that were indexed before sessions
// existed. The segment length comes from the stored setting rather than the
// rules' own default, or a 10-minute-segment install would be told it had been
// streaming for an hour between every pair of files.
func backfillSessions(log *slog.Logger, store *db.DB, settings db.Settings) {
	rules := db.DefaultSessionRules()
	if s := settings.Recording.SegmentSeconds; s > 0 {
		rules.SegmentHint = time.Duration(s) * time.Second
	}
	res, err := store.BackfillSessions(rules)
	if err != nil {
		log.Warn("cannot group existing recordings into sessions", "error", err)
		return
	}
	if res.Created > 0 || res.Assigned > 0 {
		log.Info("grouped existing recordings into sessions",
			"sessions", res.Created, "recordings", res.Assigned, "extended", res.Extended)
	}
}
