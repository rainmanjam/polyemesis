package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
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
	"github.com/rainmanjam/polyemesis/internal/startupenv"
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

	registerProcessors(processorArgs{log: log, cfg: config.Config{DataDir: dir}, store: store, gov: gov, q: q, tools: tools, pp: db.PostProdSettings{}})

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
	}, nil)
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
			}, nil)(tc.job)

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
// startStoppablePostProd runs startPostProd against store, and on cleanup
// cancels it and WAITS for its loops before the store closes -- registered
// after wiringStore's cleanup, so it runs first. A test that only cancelled
// closed the store under a loop still mid-query, whose connection then held
// the database file open past t.TempDir's RemoveAll: on Windows, "The process
// cannot access the file because it is being used by another process".
func startStoppablePostProd(t *testing.T, store *db.DB, dir string) postProd {
	t.Helper()
	log := wiringLogger()
	cfg := config.Config{DataDir: dir}
	// NewManager and not Start: the manager is inert until Start, which binds a
	// real listener. startPostProd only captures its method values.
	eng := engine.NewManager(log, cfg, store, &ffmpeg.Tools{}, events.NewBroker())
	ctx, cancel := context.WithCancel(context.Background())
	pp := startPostProd(ctx, postProdArgs{log: log, cfg: cfg, settings: settingsOf(t, store), env: startupenv.Env{}, store: store, eng: eng, tools: &ffmpeg.Tools{
		FFmpeg:  filepath.Join(dir, "no-ffmpeg"),
		FFprobe: filepath.Join(dir, "no-ffprobe"),
	}})
	t.Cleanup(func() {
		cancel()
		stopPostProd(t, pp)
	})
	return pp
}

// stopPostProd waits for pp's loops within engine.ShutdownBudget -- the budget
// production gives them -- so this fails exactly when a real shutdown would
// leave the store open rather than close it under a running loop.
func stopPostProd(t *testing.T, pp postProd) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), engine.ShutdownBudget)
	defer cancel()
	if !pp.waitWithin(ctx) {
		t.Fatalf("startPostProd's loops did not return within the %v shutdown budget after "+
			"their context was cancelled; production would leave the store open", engine.ShutdownBudget)
	}
}

// THE STORE IS RELEASED WHEN POST-PRODUCTION STOPS. Cancel, wait, close: after
// that nothing may still hold a connection to the database -- which is what
// kept test.db open on Windows past TempDir's cleanup. Measured on the pool,
// which counts a connection a goroutine is still using even after Close, and
// on Linux and macOS additionally on the process's own open files.
//
// Mutation: make waitWithin return at once. Observed: an open connection
// after Close in every run, because the startup sweeps are still mid-query.
func TestStartPostProdReleasesTheDatabaseWhenItStops(t *testing.T) {
	store, dir := wiringStore(t)
	log := wiringLogger()
	cfg := config.Config{DataDir: dir}
	eng := engine.NewManager(log, cfg, store, &ffmpeg.Tools{}, events.NewBroker())
	ctx, cancel := context.WithCancel(context.Background())
	pp := startPostProd(ctx, postProdArgs{log: log, cfg: cfg, settings: settingsOf(t, store), env: startupenv.Env{}, store: store, eng: eng, tools: &ffmpeg.Tools{
		FFmpeg:  filepath.Join(dir, "no-ffmpeg"),
		FFprobe: filepath.Join(dir, "no-ffprobe"),
	}})

	cancel()
	stopPostProd(t, pp)
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if n := store.SQL().Stats().OpenConnections; n != 0 {
		t.Errorf("%d connection(s) to the database still open after post-production "+
			"stopped and the store closed; on Windows that open file fails the "+
			"data directory's removal", n)
	}
	if held := openFilesUnder(t, dir); len(held) > 0 {
		t.Errorf("the process still holds files in the data directory open: %v", held)
	}
}

// A RECONCILE STILL RUNNING KEEPS THE TIER RUNNING. reconcileOnNormalise
// starts the reconcile on its own goroutine, which reads the store, so the
// group the caller waits on before closing it has to count that goroutine from
// before it starts until it returns.
//
// The reconcile here blocks until the test releases it, so the group cannot
// legitimately drain first: the 100ms window can only produce a false pass,
// never a false failure. Mutation: drop track.Add (and its Done) -- observed to
// fail here.
func TestAReconcileStartedByAFinishedNormalisationIsWaitedFor(t *testing.T) {
	var track sync.WaitGroup
	entered, release := make(chan struct{}), make(chan struct{})
	reconcileOnNormalise(wiringLogger(), func() error {
		close(entered)
		<-release
		return nil
	}, &track)(jobs.Job{Kind: playlistmedia.KindNormalise, State: jobs.StateDone})
	<-entered

	drained := make(chan struct{})
	go func() { track.Wait(); close(drained) }()
	select {
	case <-drained:
		t.Fatal("the tier reported its goroutines finished while a reconcile was still running")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-drained
}

func TestStartPostProdRegistersItsProcessors(t *testing.T) {
	store, dir := wiringStore(t)

	pp := startStoppablePostProd(t, store, dir)
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
