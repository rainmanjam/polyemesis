package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/clipper"
	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/playlistmedia"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// internal/playlistmedia was a complete, well-tested package that the product
// never once constructed: no registration, no submission, no consumer of its
// worker. The engine, meanwhile, refuses to put a playlist on air until that
// worker's output exists. The two together took a feature that worked and made
// it permanently unstartable, and every test in both packages stayed green
// because each one was right about its own half.
//
// These are the tests about the JOIN. They are deliberately about wiring
// rather than behaviour, because wiring is what was missing.

func wiringLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func wiringStore(t *testing.T) (*db.DB, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store, dir
}

// TestTheNormaliseWorkerIsRegisteredWithTheQueue is the guard on the line whose
// absence was the branch's Critical finding.
//
// "Registered" is asserted by trying to register the kind AGAIN: the queue
// refuses a duplicate registration on purpose, so a successful second Register
// means the first one never happened. That is the only observable the queue
// offers, and it is a better one than a getter would be -- it is the same
// mechanism that would catch two features claiming one kind.
//
// The mutation: delete the `np.Register(q)` call in registerProcessors and this
// fails with "the playlist normalisation kind is not registered".
func TestTheNormaliseWorkerIsRegisteredWithTheQueue(t *testing.T) {
	store, dir := wiringStore(t)
	log := wiringLogger()

	q := jobs.New(log, store)
	gov := jobs.NewGovernor(log, q)
	// A binary path that cannot exec: registration must not depend on FFmpeg
	// being present, which is the fail-open rule registerProcessors states.
	tools := &ffmpeg.Tools{FFmpeg: filepath.Join(dir, "no-ffmpeg"), FFprobe: filepath.Join(dir, "no-ffprobe")}

	registerProcessors(log, config.Config{DataDir: dir}, store, gov, q, tools, nil, db.PostProdSettings{})

	err := q.Register(playlistmedia.KindNormalise, 1,
		jobs.WorkerFunc(func(context.Context, jobs.Job, jobs.Reporter) error { return nil }))
	if err == nil {
		t.Fatal("the playlist normalisation kind is not registered, so nothing will ever " +
			"write a derivative and no playlist can start")
	}
}

// TestAFinishedNormalisationReconcilesTheEngine drives a REAL queue through a
// real job and asserts the engine was reconciled, because the thing being
// tested is the edge between them.
//
// Nothing else creates that edge. reconcilePlaylist is reached only through
// Engine.Reconcile, and Reconcile is called by settings saves, the API, the
// manager and the scheduler -- a job finishing is none of those. Without this
// hook a playlist whose last item finished transcoding sits off air until an
// operator happens to save something unrelated.
//
// It goes through newJobQueue, which is the constructor startPostProd itself
// calls, rather than assembling a queue of its own. A test that passed its own
// jobs.WithOnChange would prove the callback works and prove nothing about
// whether the server ever attaches it -- the exact shape of defect this
// sub-project has produced four times.
//
// The mutation: drop the jobs.WithOnChange option from newJobQueue, or drop
// either arm of reconcileOnNormalise's guard, and the reconcile never arrives.
func TestAFinishedNormalisationReconcilesTheEngine(t *testing.T) {
	store, _ := wiringStore(t)
	log := wiringLogger()

	reconciled := make(chan struct{}, 1)
	q := newJobQueue(log, store, 1, func() error {
		select {
		case reconciled <- struct{}{}:
		default:
		}
		return nil
	})
	if err := q.Register(playlistmedia.KindNormalise, 1,
		jobs.WorkerFunc(func(context.Context, jobs.Job, jobs.Reporter) error { return nil })); err != nil {
		t.Fatalf("register: %v", err)
	}

	job, err := playlistmedia.NewNormaliseJob(playlistmedia.NormaliseParams{Upload: "loop.mp4"})
	if err != nil {
		t.Fatalf("build job: %v", err)
	}
	if _, _, err := q.Submit(job); err != nil {
		t.Fatalf("submit: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Tick(ctx)

	select {
	case <-reconciled:
	case <-time.After(5 * time.Second):
		t.Fatal("a normalisation job finished and nothing reconciled the engine; " +
			"the playlist it unblocked would stay off air until an unrelated save")
	}
}

// TestOnlyAFinishedNormalisationReconciles is the other direction, and it is
// the one that keeps the hook from becoming a reconcile-on-everything.
//
// Reconcile stops and starts supervised processes. Firing it on every job state
// transition -- a transcription starting, a proxy encode retrying -- would put
// that cost on the queue's busiest path for no reason, and firing it on a
// FAILED normalisation would re-read exactly the state that was just refused.
func TestOnlyAFinishedNormalisationReconciles(t *testing.T) {
	for _, tc := range []struct {
		name string
		job  jobs.Job
		want bool
	}{
		{"a finished normalisation", jobs.Job{Kind: playlistmedia.KindNormalise, State: jobs.StateDone}, true},
		{"a normalisation that is still running", jobs.Job{Kind: playlistmedia.KindNormalise, State: jobs.StateRunning}, false},
		{"a normalisation that failed", jobs.Job{Kind: playlistmedia.KindNormalise, State: jobs.StateFailed}, false},
		{"a finished job of another kind", jobs.Job{Kind: "transcribe", State: jobs.StateDone}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fired := make(chan struct{}, 1)
			reconcileOnNormalise(wiringLogger(), func() error {
				fired <- struct{}{}
				return nil
			})(tc.job)

			var got bool
			select {
			case <-fired:
				got = true
			case <-time.After(300 * time.Millisecond):
			}
			if got != tc.want {
				t.Errorf("reconciled = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestStartPostProdRegistersItsProcessors pins registerProcessors to its call
// site, which nothing else does.
//
// The line is one call in the middle of startPostProd, and dropping it compiles
// and starts a server that serves every page, runs every loop, and executes no
// job of any kind. The queue's own registration test cannot see that: it calls
// registerProcessors directly, so it proves the function registers and proves
// nothing about whether the server ever calls it -- the same gap that let the
// whole playlist tier ship unwired.
//
// The mutation: delete the registerProcessors call in startPostProd and this
// fails.
func TestStartPostProdRegistersItsProcessors(t *testing.T) {
	store, dir := wiringStore(t)
	log := wiringLogger()

	cfg := config.Config{DataDir: dir}
	bus := events.NewBroker()
	// NewManager and not Start: the manager is inert until Start, which binds a
	// real listener. startPostProd only captures its method values.
	eng := engine.NewManager(log, cfg, store, &ffmpeg.Tools{}, bus)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pp := startPostProd(ctx, log, cfg, store, eng, &ffmpeg.Tools{
		FFmpeg:  filepath.Join(dir, "no-ffmpeg"),
		FFprobe: filepath.Join(dir, "no-ffprobe"),
	})
	if pp.queue == nil {
		t.Fatal("startPostProd returned no queue")
	}

	// Every kind the product can submit has to be claimable, and a second
	// Register is the queue's own way of saying one already is.
	for _, kind := range []jobs.Kind{
		playlistmedia.KindNormalise,
		transcribe.KindTranscribe,
		clipper.JobKind,
	} {
		err := pp.queue.Register(kind, 1,
			jobs.WorkerFunc(func(context.Context, jobs.Job, jobs.Reporter) error { return nil }))
		if err == nil {
			t.Errorf("startPostProd left kind %q unregistered; a job of that kind would "+
				"sit queued forever on a server that looks entirely healthy", kind)
		}
	}
}
