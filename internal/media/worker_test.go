package media

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/jobs"
)

// ------------------------------------------------------------------- fakes

// fakeReporter records what a worker told the queue.
type fakeReporter struct {
	mu       sync.Mutex
	progress []float64
	lines    []string
	result   any
}

func (r *fakeReporter) Progress(f float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress = append(r.progress, jobs.ClampProgress(f))
}

func (r *fakeReporter) Logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, format)
	_ = args
}

func (r *fakeReporter) SetResult(v any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.result = v
}

func (r *fakeReporter) last() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.progress) == 0 {
		return -1
	}
	return r.progress[len(r.progress)-1]
}

// fakeExec records the commands a worker ran and creates whatever outputs it
// was told to, so the rename-into-place and cleanup paths are real.
type fakeExec struct {
	mu   sync.Mutex
	runs []Command
	// creates maps a substring of an output path to the bytes to write there.
	// The path itself is taken from the command's own arguments, so a worker
	// that writes to the wrong place cannot pass.
	create func(cmd Command) error
	err    error
	// progress is emitted to the sink before returning.
	progress []ffmpeg.Progress
	lines    []string
}

func (f *fakeExec) run(_ context.Context, cmd Command, sink Sink) error {
	f.mu.Lock()
	f.runs = append(f.runs, cmd)
	f.mu.Unlock()

	for _, pr := range f.progress {
		if sink.Progress != nil {
			sink.Progress(pr)
		}
	}
	for _, l := range f.lines {
		if sink.Line != nil {
			sink.Line(l)
		}
	}
	if f.err != nil {
		return f.err
	}
	if f.create != nil {
		return f.create(cmd)
	}
	return nil
}

func (f *fakeExec) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.runs)
}

// writeOutputs creates every argument that looks like a path under root with n
// bytes in it. Crude on purpose: it means the fake writes exactly where the
// real FFmpeg would have.
func writeOutputs(root string, n int) func(Command) error {
	return func(cmd Command) error {
		for i, a := range cmd.Args {
			if !strings.HasPrefix(a, root) || i == 0 {
				continue
			}
			if cmd.Args[i-1] == "-i" || cmd.Args[i-1] == "-vaapi_device" {
				continue
			}
			path := a
			if strings.Contains(path, "%") {
				// An image2 pattern; write the first sheet.
				path = SheetName(path, 1)
				path = filepath.Join(filepath.Dir(a), path)
			}
			if err := os.WriteFile(path, make([]byte, n), 0o644); err != nil {
				return err
			}
		}
		return nil
	}
}

type testRig struct {
	root string
	proc *Processor
	exec *fakeExec
	rep  *fakeReporter
	// master is the fake recording on disk.
	master string
}

func newRig(t *testing.T, cfg Config, opts ...Option) *testRig {
	t.Helper()
	root := t.TempDir()
	master := filepath.Join(root, "rec-20240115-143000.mkv")
	if err := os.WriteFile(master, make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}

	fe := &fakeExec{create: writeOutputs(root, 4096)}
	cfg.RecordingsDir = root
	if cfg.FFmpeg == "" {
		cfg.FFmpeg = "/usr/bin/ffmpeg"
	}
	if cfg.FFprobe == "" {
		cfg.FFprobe = "/usr/bin/ffprobe"
	}
	all := append([]Option{WithExecer(fe.run)}, opts...)
	return &testRig{root: root, proc: New(testLog(), cfg, all...), exec: fe, rep: &fakeReporter{}, master: master}
}

// mustJob unwraps a builder that is not being tested here. It panics rather
// than taking a *testing.T, because Go will not let a multi-value call sit
// beside another argument.
func mustJob(j jobs.Job, err error) jobs.Job {
	if err != nil {
		panic("building the job: " + err.Error())
	}
	return j
}

// ---------------------------------------------------------------- job builders

func TestJobBuildersTargetTheRecordingWithTheOneCanonicalSpelling(t *testing.T) {
	tests := []struct {
		name    string
		build   func() (jobs.Job, error)
		kind    jobs.Kind
		wantPri jobs.Priority
	}{
		{"proxy", func() (jobs.Job, error) {
			return NewProxyJob(7, ProxyParams{Recording: "rec-1.mkv"})
		}, KindProxy, jobs.PriorityNormal},
		{"thumbnails", func() (jobs.Job, error) {
			return NewThumbnailJob(7, ThumbnailParams{Recording: "rec-1.mkv"})
		}, KindThumbnails, jobs.PriorityNormal},
		// The most expensive job in the product, and nobody is waiting on it.
		{"archive", func() (jobs.Job, error) {
			return NewArchiveJob(7, ArchiveParams{Recording: "rec-1.mkv"})
		}, KindArchive, jobs.PriorityBulk},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			j := mustJob(tc.build())
			if j.Kind != tc.kind {
				t.Fatalf("Kind = %q, want %q", j.Kind, tc.kind)
			}
			if j.Target != jobs.RecordingTarget(7) {
				t.Fatalf("Target = %q, want %q", j.Target, jobs.RecordingTarget(7))
			}
			if j.Priority != tc.wantPri {
				t.Fatalf("Priority = %d, want %d", j.Priority, tc.wantPri)
			}
			// Without this, clicking a button twice does the work twice.
			if !j.Unique {
				t.Fatal("the job is not Unique")
			}
			if err := j.Validate(); err != nil {
				t.Fatalf("the queue would reject this job: %v", err)
			}
		})
	}
}

func TestJobBuildersRefuseParamsNoWorkerCouldActOn(t *testing.T) {
	tests := []struct {
		name  string
		build func() (jobs.Job, error)
	}{
		{"proxy with a traversal", func() (jobs.Job, error) {
			return NewProxyJob(1, ProxyParams{Recording: "../etc/passwd"})
		}},
		{"proxy with no recording", func() (jobs.Job, error) {
			return NewProxyJob(1, ProxyParams{})
		}},
		{"thumbnails asking for nothing", func() (jobs.Job, error) {
			return NewThumbnailJob(1, ThumbnailParams{Recording: "r.mkv",
				SkipPoster: true, SkipContactSheet: true, SkipSprites: true})
		}},
		{"archive with an unknown codec", func() (jobs.Job, error) {
			return NewArchiveJob(1, ArchiveParams{Recording: "r.mkv", Codec: "vp9"})
		}},
		{"a target that is not a recording", func() (jobs.Job, error) {
			return NewProxyJob(0, ProxyParams{Recording: "r.mkv"})
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.build(); err == nil {
				t.Fatal("the builder accepted params no worker could act on")
			}
		})
	}
}

// The quality knob is the one field on this payload that VerifyArchive cannot
// check after the fact. It compares containers, durations, audio tracks and
// decode errors, and a CRF of 45 passes every one of them while looking nothing
// like the master — so with ReplaceOriginal set, a green job renames a smeared
// copy over a bit-exact multitrack original. The refusal has to happen here,
// before the encode, where the number is still a submission someone can fix.
func TestArchiveJobRefusesAQualityThatWouldDegradeTheMasterItReplaces(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*ArchiveParams)
		wantErr bool
	}{
		{"the destroying band with hevc and replacement", func(p *ArchiveParams) {
			p.Quality, p.ReplaceOriginal = 40, true
		}, true},
		// One point past the profile default is already outside the region that
		// default's own comment describes, and this job deletes the original.
		{"one point worse than the default, replacing", func(p *ArchiveParams) {
			p.Quality, p.ReplaceOriginal = 29, true
		}, true},
		{"av1 one point worse than its default, replacing", func(p *ArchiveParams) {
			p.Codec, p.Quality, p.ReplaceOriginal = ArchiveAV1, 33, true
		}, true},
		// Nothing is destroyed without ReplaceOriginal, but there is still a
		// point past which the file is a second proxy rather than an archive.
		{"past the alongside ceiling", func(p *ArchiveParams) { p.Quality = 35 }, true},
		{"the top of ffmpeg's own scale", func(p *ArchiveParams) { p.Quality = 51 }, true},
		// A negative today silently becomes the profile default, which is how a
		// typo turns into an encode nobody asked for.
		{"a negative number", func(p *ArchiveParams) { p.Quality = -5 }, true},

		// Positive controls. A Validate that refused every quality it was given
		// would satisfy every row above; these are the rows that catch it.
		{"unset, meaning the profile default", func(p *ArchiveParams) { p.Quality = 0 }, false},
		{"exactly the default, replacing", func(p *ArchiveParams) {
			p.Quality, p.ReplaceOriginal = 28, true
		}, false},
		{"better than the default, replacing", func(p *ArchiveParams) {
			p.Quality, p.ReplaceOriginal = 18, true
		}, false},
		{"av1 at its own default, replacing", func(p *ArchiveParams) {
			p.Codec, p.Quality, p.ReplaceOriginal = ArchiveAV1, 32, true
		}, false},
		{"exactly on the alongside ceiling", func(p *ArchiveParams) { p.Quality = 34 }, false},
		{"av1 alongside, on its wider ceiling", func(p *ArchiveParams) {
			p.Codec, p.Quality = ArchiveAV1, 38
		}, false},
		// The encoder is the more specific fact: an SVT-AV1 encode asked for
		// under the HEVC label must still be bounded by SVT-AV1's scale.
		{"a named av1 encoder widens a hevc-labelled job", func(p *ArchiveParams) {
			p.Encoder, p.Quality, p.ReplaceOriginal = "libsvtav1", 32, true
		}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := ArchiveParams{Recording: "rec-20240115-143000.mkv", AcknowledgeLossy: true}
			tc.mutate(&p)
			_, err := NewArchiveJob(1, p)
			if tc.wantErr && err == nil {
				t.Fatalf("quality %d (replace=%v) was accepted", p.Quality, p.ReplaceOriginal)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("quality %d (replace=%v) was refused: %v", p.Quality, p.ReplaceOriginal, err)
			}
			if tc.wantErr && !strings.Contains(err.Error(), "archive quality") {
				t.Fatalf("the refusal does not say what is wrong: %v", err)
			}
		})
	}
}

// The refusal is read in two places by the same person: as a 400 from the API
// when they submit, and as the Error text on a job row when a payload queued
// before this bound existed comes back around. In neither place is there
// anything else to read, so the message has to carry the ceiling that was
// applied, which way the scale runs, what number to use instead, and the fact
// that no footage has been touched. "Invalid quality" would satisfy the table
// above and tell an operator staring at a failed overnight job nothing at all.
func TestTheQualityRefusalCarriesTheCeilingTheReasonAndTheWayOut(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ArchiveParams)
		want   []string
	}{
		{"replacing the original", func(p *ArchiveParams) {
			p.Quality, p.ReplaceOriginal = 40, true
		}, []string{
			"archive quality 40",   // the number that was refused
			"1-28",                 // the bound it was measured against
			"worse picture",        // which way the scale runs
			"replaces the origina", // which of the two acts was being bounded
			"Resubmit at 28 or lower",
			// The looser ceiling is the other way out, and an operator cannot
			// guess that giving up the replacement buys six more points.
			"leave the original in place and resubmit at 34 or lower",
			"nothing has been deleted",
			"queued before this limit existed",
		}},
		{"written alongside", func(p *ArchiveParams) { p.Quality = 44 }, []string{
			"archive quality 44",
			"1-34",
			"beside the original",
			"Resubmit at 34 or lower",
			"nothing has been deleted",
		}},
		{"av1 keeps its own numbers", func(p *ArchiveParams) {
			p.Codec, p.Quality, p.ReplaceOriginal = ArchiveAV1, 40, true
		}, []string{"1-32", "Resubmit at 32 or lower", "resubmit at 38 or lower"}},
		// A number below the floor is a different mistake, and the ceiling's
		// sentence is actively wrong for it: "a higher number is a worse
		// picture" sends someone who typed -5 further the wrong way.
		{"below the floor", func(p *ArchiveParams) { p.Quality = -5 }, []string{
			"archive quality -5 is below 1",
			"a lower number is a better picture",
			"0 already means take the codec's own default",
			"Resubmit at 0 for the default, or between 1 and 34",
			"nothing has been deleted",
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := ArchiveParams{Recording: "rec-20240115-143000.mkv", AcknowledgeLossy: true}
			tc.mutate(&p)
			err := p.Validate()
			if err == nil {
				t.Fatalf("quality %d was accepted", p.Quality)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("the refusal never says %q:\n%v", want, err)
				}
			}
		})
	}

	// Negative controls, because every row above is a substring match and a
	// message that said everything at once would satisfy all of them.
	//
	// The alongside refusal must not offer "leave the original in place" as a
	// remedy, because the original is already staying.
	p := ArchiveParams{Recording: "rec-20240115-143000.mkv", AcknowledgeLossy: true, Quality: 44}
	if err := p.Validate(); err == nil || strings.Contains(err.Error(), "leave the original in place") {
		t.Fatalf("a job that destroys nothing was told to stop destroying things: %v", err)
	}
	// And the floor refusal must not tell someone who went too low that a
	// higher number is worse.
	p.Quality = -5
	if err := p.Validate(); err == nil || strings.Contains(err.Error(), "higher number is a worse picture") {
		t.Fatalf("the floor was explained with the ceiling's sentence: %v", err)
	}
}

// THE UPGRADE CASE. A payload queued by a build that had no quality bound is
// never re-submitted through NewArchiveJob; RunArchive is the only thing that
// ever looks at it again. This is what the operator who upgrades on Friday and
// reads the job list on Monday actually meets, and the two facts that matter
// are that it stops before the encode and that it stops for good rather than
// retrying an unencodable payload until the attempt ceiling.
func TestAnArchiveQueuedBeforeTheBoundFailsPermanentlyAndExplainsItself(t *testing.T) {
	src, out := goodPair()
	rig := archiveRig(t, Config{ArchiveAllowReplace: true}, src, out, nil)

	// Marshalled directly, exactly as the queue holds a row written by the
	// older build. Going through NewArchiveJob would be testing the submission
	// path a second time and would never produce this payload at all.
	raw, err := json.Marshal(ArchiveParams{
		Recording:        "rec-20240115-143000.mkv",
		DurationMS:       3600000,
		RecordedAtUnix:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix(),
		AcknowledgeLossy: true,
		ReplaceOriginal:  true,
		Quality:          40,
	})
	if err != nil {
		t.Fatal(err)
	}
	job := jobs.Job{
		Kind:   KindArchive,
		Target: jobs.RecordingTarget(1),
		Params: raw,
	}.Normalized()

	runErr := rig.proc.RunArchive(context.Background(), job, rig.rep)
	if runErr == nil {
		t.Fatal("the pre-upgrade payload encoded and replaced the master")
	}
	if !jobs.IsPermanent(runErr) {
		t.Fatalf("a payload no attempt can fix was left retryable: %v", runErr)
	}
	for _, want := range []string{"archive quality 40", "1-28", "Resubmit at 28 or lower",
		"nothing has been deleted", "queued before this limit existed"} {
		if !strings.Contains(runErr.Error(), want) {
			t.Fatalf("the failed row never says %q:\n%v", want, runErr)
		}
	}
	if rig.exec.count() != 0 {
		t.Fatalf("FFmpeg ran %d times before the refusal", rig.exec.count())
	}
	if info, err := os.Stat(rig.master); err != nil || info.Size() != 1<<20 {
		t.Fatalf("the master was touched by a job that was refused: %v", err)
	}
}

func TestRegisterWiresAllThreeKindsExactlyOnce(t *testing.T) {
	seen := map[jobs.Kind]int{}
	reg := registryFunc(func(k jobs.Kind, limit int, w jobs.Worker) error {
		seen[k]++
		if limit != 1 {
			t.Fatalf("%s registered with limit %d; these all saturate the CPU", k, limit)
		}
		if w == nil {
			t.Fatalf("%s registered with no worker", k)
		}
		return nil
	})
	if err := New(testLog(), Config{}).Register(reg); err != nil {
		t.Fatalf("Register: %v", err)
	}
	for _, k := range []jobs.Kind{KindProxy, KindThumbnails, KindArchive} {
		if seen[k] != 1 {
			t.Fatalf("%s registered %d times", k, seen[k])
		}
	}
}

type registryFunc func(jobs.Kind, int, jobs.Worker) error

func (f registryFunc) Register(k jobs.Kind, limit int, w jobs.Worker) error { return f(k, limit, w) }

func TestRegisterSurfacesADuplicateKind(t *testing.T) {
	want := errors.New("already registered")
	reg := registryFunc(func(jobs.Kind, int, jobs.Worker) error { return want })
	if err := New(testLog(), Config{}).Register(reg); !errors.Is(err, want) {
		t.Fatalf("Register = %v, want %v", err, want)
	}
}

// -------------------------------------------------------------- proxy worker

func TestRunProxyPublishesThroughAPartialFileSoNothingHalfWrittenIsEverServed(t *testing.T) {
	rig := newRig(t, Config{})
	job := mustJob(NewProxyJob(1, ProxyParams{Recording: "rec-20240115-143000.mkv", DurationMS: 60000}))

	if err := rig.proc.RunProxy(context.Background(), job, rig.rep); err != nil {
		t.Fatalf("RunProxy: %v", err)
	}

	layout := LayoutFor(rig.root, "rec-20240115-143000.mkv")
	if _, err := os.Stat(layout.Proxy); err != nil {
		t.Fatalf("no proxy was published: %v", err)
	}
	if _, err := os.Stat(layout.Proxy + PartialSuffix); !os.IsNotExist(err) {
		t.Fatalf("the .partial file survived: %v", err)
	}
	// The command must have written to the partial path, never to the final one.
	if got := rig.exec.runs[0].Args[len(rig.exec.runs[0].Args)-1]; got != layout.Proxy+PartialSuffix {
		t.Fatalf("the encode wrote straight to %q", got)
	}
	res, ok := rig.rep.result.(ProxyResult)
	if !ok {
		t.Fatalf("result = %#v, want a ProxyResult", rig.rep.result)
	}
	if res.Path != layout.Proxy || res.Bytes != 4096 {
		t.Fatalf("result = %+v", res)
	}
}

func TestRunProxyMapsFFmpegsProgressOntoTheJobsBar(t *testing.T) {
	rig := newRig(t, Config{})
	rig.exec.progress = []ffmpeg.Progress{{OutTimeMS: 15000}, {OutTimeMS: 30000}, {OutTimeMS: 60000, Done: true}}
	job := mustJob(NewProxyJob(1, ProxyParams{Recording: "rec-20240115-143000.mkv", DurationMS: 60000}))

	if err := rig.proc.RunProxy(context.Background(), job, rig.rep); err != nil {
		t.Fatalf("RunProxy: %v", err)
	}
	want := []float64{0.25, 0.5, 1, 1}
	if len(rig.rep.progress) != len(want) {
		t.Fatalf("progress = %v, want %v", rig.rep.progress, want)
	}
	for i := range want {
		if rig.rep.progress[i] != want[i] {
			t.Fatalf("progress = %v, want %v", rig.rep.progress, want)
		}
	}
}

func TestRunProxyLeavesNothingBehindWhenTheEncodeFails(t *testing.T) {
	rig := newRig(t, Config{})
	rig.exec.err = errors.New("ffmpeg exploded")
	job := mustJob(NewProxyJob(1, ProxyParams{Recording: "rec-20240115-143000.mkv"}))

	err := rig.proc.RunProxy(context.Background(), job, rig.rep)
	if err == nil {
		t.Fatal("RunProxy hid a failed encode")
	}
	// An unclassified failure is RETRYABLE; a transient disk-busy must not
	// throw the job away.
	if jobs.IsPermanent(err) {
		t.Fatalf("a failed encode was marked permanent: %v", err)
	}
	layout := LayoutFor(rig.root, "rec-20240115-143000.mkv")
	for _, p := range []string{layout.Proxy, layout.Proxy + PartialSuffix} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s survived a failed encode", p)
		}
	}
}

func TestRunProxyRefusesPermanentlyWhatARetryCannotFix(t *testing.T) {
	tests := []struct {
		name string
		// noFFmpeg simulates a machine where detection never found a binary.
		noFFmpeg bool
		job      func() jobs.Job
	}{
		{
			name: "a recording that is not on disk",
			job: func() jobs.Job {
				return mustJob(NewProxyJob(1, ProxyParams{Recording: "not-here.mkv"}))
			},
		},
		{
			name:     "no FFmpeg on the machine",
			noFFmpeg: true,
			job: func() jobs.Job {
				return mustJob(NewProxyJob(1, ProxyParams{Recording: "rec-20240115-143000.mkv"}))
			},
		},
		{
			name: "params that were hand-edited into nonsense",
			job: func() jobs.Job {
				j := mustJob(NewProxyJob(1, ProxyParams{Recording: "rec-20240115-143000.mkv"}))
				j.Params = json.RawMessage(`{"recording": 12}`)
				return j
			},
		},
		{
			name: "params with no payload at all",
			job: func() jobs.Job {
				j := mustJob(NewProxyJob(1, ProxyParams{Recording: "rec-20240115-143000.mkv"}))
				j.Params = nil
				return j
			},
		},
		{
			name: "a name that tries to leave the recordings directory",
			job: func() jobs.Job {
				j := mustJob(NewProxyJob(1, ProxyParams{Recording: "rec-20240115-143000.mkv"}))
				j.Params = json.RawMessage(`{"recording": "../../etc/passwd"}`)
				return j
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rig := newRig(t, Config{})
			if tc.noFFmpeg {
				rig.proc.SetConfig(Config{RecordingsDir: rig.root})
			}

			err := rig.proc.RunProxy(context.Background(), tc.job(), rig.rep)
			if err == nil {
				t.Fatal("RunProxy accepted a job it cannot do")
			}
			if !jobs.IsPermanent(err) {
				t.Fatalf("error is retryable but nothing will change: %v", err)
			}
			if rig.exec.count() != 0 {
				t.Fatalf("FFmpeg was started anyway: %v", rig.exec.runs)
			}
		})
	}
}

func TestRunProxyHonoursTheRequestedAudioTrackIncludingZeroAndSilence(t *testing.T) {
	tests := []struct {
		name  string
		track *int
		want  string
	}{
		{"unset means the first track", nil, "0:a:0?"},
		{"track zero explicitly", intPtr(0), "0:a:0?"},
		{"a later track", intPtr(4), "0:a:4?"},
		{"negative means silent", intPtr(-1), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rig := newRig(t, Config{})
			job := mustJob(NewProxyJob(1, ProxyParams{
				Recording: "rec-20240115-143000.mkv", AudioTrack: tc.track}))

			if err := rig.proc.RunProxy(context.Background(), job, rig.rep); err != nil {
				t.Fatalf("RunProxy: %v", err)
			}
			args := rig.exec.runs[0].Args
			if tc.want == "" {
				if !hasArg(args, "-an") {
					t.Fatalf("want a silent proxy: %v", args)
				}
				return
			}
			if !hasArg(args, tc.want) {
				t.Fatalf("track %s not mapped: %v", tc.want, args)
			}
		})
	}
}

func intPtr(v int) *int { return &v }

// --------------------------------------------------------- thumbnail worker

func TestRunThumbnailsPublishesEveryArtefactAndItsIndex(t *testing.T) {
	rig := newRig(t, Config{})
	job := mustJob(NewThumbnailJob(1, ThumbnailParams{
		Recording: "rec-20240115-143000.mkv", DurationMS: 600000}))

	if err := rig.proc.RunThumbnails(context.Background(), job, rig.rep); err != nil {
		t.Fatalf("RunThumbnails: %v", err)
	}

	layout := LayoutFor(rig.root, "rec-20240115-143000.mkv")
	for _, p := range []string{layout.Poster, layout.ContactSheet, layout.SpriteVTT} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s was not published: %v", filepath.Base(p), err)
		}
	}
	// One decode, not three.
	if rig.exec.count() != 1 {
		t.Fatalf("%d FFmpeg runs, want 1", rig.exec.count())
	}
	vtt, err := os.ReadFile(layout.SpriteVTT)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(vtt), "WEBVTT") {
		t.Fatalf("the index is not a WebVTT file: %q", string(vtt)[:20])
	}
	res, ok := rig.rep.result.(ThumbnailResult)
	if !ok {
		t.Fatalf("result = %#v", rig.rep.result)
	}
	if res.Poster == "" || res.ContactSheet == "" || res.SpriteVTT == "" || len(res.Sprites) == 0 {
		t.Fatalf("result does not name everything it produced: %+v", res)
	}
	if rig.rep.last() != 1 {
		t.Fatalf("the bar finished at %v", rig.rep.last())
	}
}

func TestRunThumbnailsProducesOnlyWhatWasAskedFor(t *testing.T) {
	tests := []struct {
		name   string
		params ThumbnailParams
		want   []string
		absent []string
	}{
		{
			"poster only",
			ThumbnailParams{SkipContactSheet: true, SkipSprites: true},
			[]string{PosterName}, []string{ContactSheetName, SpriteVTTName},
		},
		{
			"sprites only",
			ThumbnailParams{SkipPoster: true, SkipContactSheet: true},
			[]string{SpriteVTTName}, []string{PosterName, ContactSheetName},
		},
		{
			"no sprites",
			ThumbnailParams{SkipSprites: true},
			[]string{PosterName, ContactSheetName}, []string{SpriteVTTName},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rig := newRig(t, Config{})
			tc.params.Recording = "rec-20240115-143000.mkv"
			tc.params.DurationMS = 600000
			job := mustJob(NewThumbnailJob(1, tc.params))

			if err := rig.proc.RunThumbnails(context.Background(), job, rig.rep); err != nil {
				t.Fatalf("RunThumbnails: %v", err)
			}
			dir := LayoutFor(rig.root, tc.params.Recording).Dir
			for _, name := range tc.want {
				if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
					t.Fatalf("%s is missing: %v", name, err)
				}
			}
			for _, name := range tc.absent {
				if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
					t.Fatalf("%s was produced without being asked for", name)
				}
			}
		})
	}
}

// The sheets are still useful to a human; only the scrub preview is lost.
func TestRunThumbnailsSkipsTheIndexRatherThanWritingAnEmptyOneWithNoDuration(t *testing.T) {
	rig := newRig(t, Config{})
	job := mustJob(NewThumbnailJob(1, ThumbnailParams{Recording: "rec-20240115-143000.mkv"}))

	if err := rig.proc.RunThumbnails(context.Background(), job, rig.rep); err != nil {
		t.Fatalf("RunThumbnails: %v", err)
	}
	layout := LayoutFor(rig.root, "rec-20240115-143000.mkv")
	if _, err := os.Stat(layout.SpriteVTT); !os.IsNotExist(err) {
		t.Fatal("a cueless WebVTT was written; a player would draw nothing while pretending to have a preview")
	}
	if _, err := os.Stat(layout.Poster); err != nil {
		t.Fatalf("the poster was lost along with the index: %v", err)
	}
}

func TestRunThumbnailsLeavesNoPartialsBehindWhenTheDecodeFails(t *testing.T) {
	rig := newRig(t, Config{})
	rig.exec.err = errors.New("decode failed")
	job := mustJob(NewThumbnailJob(1, ThumbnailParams{
		Recording: "rec-20240115-143000.mkv", DurationMS: 600000}))

	if err := rig.proc.RunThumbnails(context.Background(), job, rig.rep); err == nil {
		t.Fatal("RunThumbnails hid a failed decode")
	}
	dir := LayoutFor(rig.root, "rec-20240115-143000.mkv").Dir
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), PartialSuffix) {
			t.Fatalf("%s survived a failed run", e.Name())
		}
	}
}

// filepath.Dir("") is ".", so an unguarded cleanup on a job that skipped
// sprites would sweep sprite-*.jpg out of the server's working directory.
func TestRunThumbnailsCleanupNeverReachesOutsideItsOwnDirectory(t *testing.T) {
	rig := newRig(t, Config{})
	rig.exec.err = errors.New("decode failed")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	bystander := filepath.Join(cwd, SheetName(SpritePattern, 999))
	if err := os.WriteFile(bystander, []byte("not ours"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(bystander) })

	job := mustJob(NewThumbnailJob(1, ThumbnailParams{
		Recording: "rec-20240115-143000.mkv", DurationMS: 600000, SkipSprites: true}))
	if err := rig.proc.RunThumbnails(context.Background(), job, rig.rep); err == nil {
		t.Fatal("RunThumbnails hid a failed decode")
	}

	if _, err := os.Stat(bystander); err != nil {
		t.Fatalf("cleanup deleted a file outside the recording's own directory: %v", err)
	}
}

// --------------------------------------------------------- archive worker
//
// Every test below is about a refusal. Read archive.go's header first.

func archiveRig(t *testing.T, cfg Config, src, out FileSummary, decodeLines []string) *testRig {
	t.Helper()
	cfg.ArchiveEnabled = true
	rig := newRig(t, cfg, WithClock(func() time.Time {
		return time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	}))
	rig.exec.lines = decodeLines
	rig.proc.probe = func(_ context.Context, path string) (FileSummary, error) {
		if strings.Contains(path, ArchiveBase) {
			out.Path = path
			return out, nil
		}
		src.Path = path
		return src, nil
	}
	return rig
}

func archiveJob(t *testing.T, mutate func(*ArchiveParams)) jobs.Job {
	t.Helper()
	p := ArchiveParams{
		Recording:        "rec-20240115-143000.mkv",
		DurationMS:       3600000,
		RecordedAtUnix:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix(),
		AcknowledgeLossy: true,
	}
	if mutate != nil {
		mutate(&p)
	}
	return mustJob(NewArchiveJob(1, p))
}

func TestRunArchiveVerifiesBeforeItTouchesAnythingAndLeavesTheOriginalByDefault(t *testing.T) {
	src, out := goodPair()
	rig := archiveRig(t, Config{}, src, out, nil)

	if err := rig.proc.RunArchive(context.Background(), archiveJob(t, nil), rig.rep); err != nil {
		t.Fatalf("RunArchive: %v", err)
	}

	layout := LayoutFor(rig.root, "rec-20240115-143000.mkv")
	if _, err := os.Stat(layout.Archive); err != nil {
		t.Fatalf("the archive copy was not published: %v", err)
	}
	// The master is still exactly where it was: nothing asked for replacement.
	info, err := os.Stat(rig.master)
	if err != nil || info.Size() != 1<<20 {
		t.Fatalf("the original was touched without being asked: %v", err)
	}
	res, ok := rig.rep.result.(ArchiveResult)
	if !ok {
		t.Fatalf("result = %#v", rig.rep.result)
	}
	if res.ReplacedOriginal {
		t.Fatal("the result claims the original was replaced")
	}
	if !res.Verification.OK {
		t.Fatalf("verification: %v", res.Verification.Reasons)
	}
	// Encode plus decode check: two runs, and the decode check must have looked
	// at every stream.
	if rig.exec.count() != 2 {
		t.Fatalf("%d FFmpeg runs, want an encode and a decode check", rig.exec.count())
	}
	if !hasArg(rig.exec.runs[1].Args, "-xerror") {
		t.Fatalf("the second run was not a decode check: %v", rig.exec.runs[1].Args)
	}
}

func TestRunArchiveReplacesTheOriginalOnlyWhenBothSwitchesAndTheJobAgree(t *testing.T) {
	tests := []struct {
		name         string
		allowReplace bool
		askReplace   bool
		wantReplaced bool
	}{
		{"nobody asked", false, false, false},
		{"the job asked but the setting is off", false, true, false},
		{"the setting is on but the job did not ask", true, false, false},
		{"both agree", true, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src, out := goodPair()
			rig := archiveRig(t, Config{ArchiveAllowReplace: tc.allowReplace}, src, out, nil)
			job := archiveJob(t, func(p *ArchiveParams) { p.ReplaceOriginal = tc.askReplace })

			if err := rig.proc.RunArchive(context.Background(), job, rig.rep); err != nil {
				t.Fatalf("RunArchive: %v", err)
			}
			info, err := os.Stat(rig.master)
			if err != nil {
				t.Fatalf("the original is gone: %v", err)
			}
			replaced := info.Size() != 1<<20
			if replaced != tc.wantReplaced {
				t.Fatalf("original replaced = %v, want %v", replaced, tc.wantReplaced)
			}
			res := rig.rep.result.(ArchiveResult)
			if res.ReplacedOriginal != tc.wantReplaced {
				t.Fatalf("result says replaced = %v", res.ReplacedOriginal)
			}
		})
	}
}

// The single most important test in this package.
func TestRunArchiveKeepsTheOriginalAndDeletesItsOwnCopyWhenVerificationFails(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(src, out *FileSummary)
		decodeLines []string
	}{
		{"a lost audio track", func(_, out *FileSummary) { out.Audio = out.Audio[:1] }, nil},
		{"a truncated copy", func(_, out *FileSummary) { out.DurationSeconds = 60 }, nil},
		{"a downmixed track", func(_, out *FileSummary) { out.Audio[0].Channels = 1 }, nil},
		{"a copy that does not decode", nil, []string{"[hevc] Invalid data found"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src, out := goodPair()
			if tc.mutate != nil {
				tc.mutate(&src, &out)
			}
			rig := archiveRig(t, Config{ArchiveAllowReplace: true}, src, out, tc.decodeLines)
			job := archiveJob(t, func(p *ArchiveParams) { p.ReplaceOriginal = true })

			err := rig.proc.RunArchive(context.Background(), job, rig.rep)
			if err == nil {
				t.Fatal("RunArchive accepted a copy that did not verify")
			}
			// Nothing about this gets better on a retry, and a retry would burn
			// the machine the live stream needs.
			if !jobs.IsPermanent(err) {
				t.Fatalf("a failed verification is retryable: %v", err)
			}
			info, err := os.Stat(rig.master)
			if err != nil || info.Size() != 1<<20 {
				t.Fatalf("THE ORIGINAL WAS DESTROYED: %v", err)
			}
			layout := LayoutFor(rig.root, "rec-20240115-143000.mkv")
			for _, p := range []string{layout.Archive, layout.Archive + PartialSuffix} {
				if _, err := os.Stat(p); !os.IsNotExist(err) {
					t.Fatalf("the unverified copy survived at %s", p)
				}
			}
			// The reasons reach the operator, not just the error string.
			res := rig.rep.result.(ArchiveResult)
			if len(res.Verification.Reasons) == 0 {
				t.Fatal("the result carries no reasons")
			}
		})
	}
}

func TestRunArchiveRefusesBeforeStartingFFmpegAtAll(t *testing.T) {
	src, out := goodPair()
	tests := []struct {
		name    string
		cfg     Config
		mutate  func(*ArchiveParams)
		wantWhy string
	}{
		{
			"the feature is switched off",
			Config{}, nil, "switched off",
		},
		{
			"the human never acknowledged the loss",
			Config{}, func(p *ArchiveParams) { p.AcknowledgeLossy = false }, "acknowledging",
		},
		{
			"the recording is younger than the policy",
			Config{}, func(p *ArchiveParams) {
				p.RecordedAtUnix = time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC).Unix()
			}, "younger",
		},
		{
			"the recording's date is unknown",
			Config{}, func(p *ArchiveParams) { p.RecordedAtUnix = 0 }, "age cannot be established",
		},
		{
			"a recording that is not on disk",
			Config{}, func(p *ArchiveParams) { p.Recording = "gone.mkv" }, "gone.mkv",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rig := archiveRig(t, tc.cfg, src, out, nil)
			if tc.name == "the feature is switched off" {
				cfg := rig.proc.Config()
				cfg.ArchiveEnabled = false
				rig.proc.SetConfig(cfg)
			}

			err := rig.proc.RunArchive(context.Background(), archiveJob(t, tc.mutate), rig.rep)
			if err == nil {
				t.Fatal("RunArchive proceeded with a job it should have refused")
			}
			if !jobs.IsPermanent(err) {
				t.Fatalf("the refusal is retryable: %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantWhy) {
				t.Fatalf("error %q does not mention %q", err, tc.wantWhy)
			}
			if rig.exec.count() != 0 {
				t.Fatalf("FFmpeg was started before the refusal: %v", rig.exec.runs)
			}
			info, err := os.Stat(rig.master)
			if err != nil || info.Size() != 1<<20 {
				t.Fatalf("the original was touched: %v", err)
			}
		})
	}
}

// A copy that could not be measured is a copy that cannot authorise a delete.
func TestRunArchiveTreatsAnUnrunnableDecodeCheckAsAFailedOne(t *testing.T) {
	src, out := goodPair()
	rig := archiveRig(t, Config{ArchiveAllowReplace: true}, src, out, nil)
	calls := 0
	inner := rig.exec.run
	rig.proc.exec = func(ctx context.Context, cmd Command, sink Sink) error {
		calls++
		if calls == 2 {
			return errors.New("the decoder itself would not start")
		}
		return inner(ctx, cmd, sink)
	}

	err := rig.proc.RunArchive(context.Background(), archiveJob(t,
		func(p *ArchiveParams) { p.ReplaceOriginal = true }), rig.rep)
	if err == nil {
		t.Fatal("an unverifiable copy authorised a delete")
	}
	if info, statErr := os.Stat(rig.master); statErr != nil || info.Size() != 1<<20 {
		t.Fatalf("THE ORIGINAL WAS DESTROYED: %v", statErr)
	}
}

func TestRunArchiveWithoutFFprobeRefusesRatherThanSkippingVerification(t *testing.T) {
	src, out := goodPair()
	rig := archiveRig(t, Config{}, src, out, nil)
	cfg := rig.proc.Config()
	cfg.FFprobe = ""
	rig.proc.SetConfig(cfg)

	err := rig.proc.RunArchive(context.Background(), archiveJob(t, nil), rig.rep)
	if err == nil || !jobs.IsPermanent(err) {
		t.Fatalf("RunArchive proceeded without a way to verify: %v", err)
	}
	if !strings.Contains(err.Error(), "verified") {
		t.Fatalf("error %q does not explain itself", err)
	}
}

func TestRunArchiveUsesTheEncoderTheJobNamed(t *testing.T) {
	tests := []struct {
		name  string
		codec ArchiveCodec
		enc   string
		want  string
	}{
		{"the default", "", "", "libx265"},
		{"av1", ArchiveAV1, "", "libsvtav1"},
		{"an explicit encoder", ArchiveHEVC, "hevc_nvenc", "hevc_nvenc"},
		// The only thing the build's encoder list is allowed to change: a
		// minimal FFmpeg with no x265 at all.
		{"a build without libx265", ArchiveHEVC, "", "hevc_qsv"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src, out := goodPair()
			cfg := Config{}
			if tc.name == "a build without libx265" {
				cfg.HasEncoder = func(name string) bool { return name == "hevc_qsv" }
			}
			rig := archiveRig(t, cfg, src, out, nil)
			job := archiveJob(t, func(p *ArchiveParams) { p.Codec, p.Encoder = tc.codec, tc.enc })

			if err := rig.proc.RunArchive(context.Background(), job, rig.rep); err != nil {
				t.Fatalf("RunArchive: %v", err)
			}
			mustArg(t, rig.exec.runs[0].Args, "-c:v", tc.want)
			if res := rig.rep.result.(ArchiveResult); res.Encoder != tc.want {
				t.Fatalf("result names encoder %q, want %q", res.Encoder, tc.want)
			}
		})
	}
}

func TestReplaceOriginalRefusesToPutOneContainerUnderAnothersName(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "archive.mkv")
	original := filepath.Join(dir, "rec-1.ts")
	for _, p := range []string{archive, original} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := replaceOriginal(archive, original); err == nil {
		t.Fatal("a .ts recording was replaced with Matroska content")
	}
	if _, err := os.Stat(archive); err != nil {
		t.Fatalf("the archive was consumed by the failed replacement: %v", err)
	}
}

func TestReplaceOriginalIsASingleRenameSoNeitherFileIsEverMissing(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "archive.mkv")
	original := filepath.Join(dir, "rec-1.mkv")
	if err := os.WriteFile(archive, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(original, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := replaceOriginal(archive, original); err != nil {
		t.Fatalf("replaceOriginal: %v", err)
	}
	got, err := os.ReadFile(original)
	if err != nil || string(got) != "new" {
		t.Fatalf("original = %q, %v", got, err)
	}
	if _, err := os.Stat(archive); !os.IsNotExist(err) {
		t.Fatal("the archive copy is still there as well; it was copied, not renamed")
	}
}

// ------------------------------------------------------------------- config

func TestConfigNormalizedNeverLeavesTheArchiveAgeBelowItsFloor(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"unset", 0, DefaultArchiveMinAge},
		{"negative", -time.Hour, DefaultArchiveMinAge},
		{"below the floor", time.Minute, MinArchiveMinAge},
		{"a real policy", 90 * 24 * time.Hour, 90 * 24 * time.Hour},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := (Config{ArchiveMinAge: tc.in}).Normalized().ArchiveMinAge; got != tc.want {
				t.Fatalf("ArchiveMinAge = %v, want %v", got, tc.want)
			}
		})
	}
}
