package uploadverify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/uploads"
)

// ---------------------------------------------------------------- the fixture

// recordingStore is a Store that remembers what it was told, so a test can
// assert on the ABSENCE of a write as easily as on a write. A real
// *uploads.Store would answer "no verdict" for both "nothing was written" and
// "a write failed", and the distinction between those is the whole subject.
type recordingStore struct {
	dir string
	// verdicts is what is recorded. A missing key is "no record", which is a
	// distinct state from every recorded one.
	verdicts map[string]uploads.Verdict
	// puts counts EVERY PutVerdict call, including ones that would overwrite
	// with the same value, so "it wrote nothing" is checkable rather than
	// inferred from the end state looking unchanged.
	puts    []uploads.Verdict
	putErr  error
	resolve error
}

func newStore(t *testing.T) *recordingStore {
	t.Helper()
	return &recordingStore{dir: t.TempDir(), verdicts: map[string]uploads.Verdict{}}
}

func (s *recordingStore) Resolve(name string) (string, error) {
	if s.resolve != nil {
		return "", s.resolve
	}
	if strings.ContainsAny(name, `/\`) {
		return "", errors.New("no such upload")
	}
	return filepath.Join(s.dir, name), nil
}

func (s *recordingStore) Verdict(name string) (uploads.Verdict, bool) {
	v, ok := s.verdicts[name]
	return v, ok
}

func (s *recordingStore) PutVerdict(name string, v uploads.Verdict) error {
	s.puts = append(s.puts, v)
	if s.putErr != nil {
		return s.putErr
	}
	s.verdicts[name] = v
	return nil
}

// file puts bytes on disk under the store's directory so os.Stat finds them.
func (s *recordingStore) file(t *testing.T, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.dir, name), []byte("bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// noopReporter is jobs.Reporter with nothing behind it but a result slot.
type noopReporter struct{ result any }

func (r *noopReporter) Progress(float64)    {}
func (r *noopReporter) Logf(string, ...any) {}
func (r *noopReporter) SetResult(v any)     { r.result = v }

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func run(t *testing.T, st Store, probe Prober, name string) (*noopReporter, error) {
	t.Helper()
	p := New(quietLog(), Config{FFprobe: "/usr/bin/ffprobe", Uploads: st}, WithProber(probe))
	job, err := NewJob(Params{Upload: name})
	if err != nil {
		t.Fatalf("NewJob: %v", err)
	}
	rep := &noopReporter{}
	return rep, p.RunVerify(context.Background(), job, rep)
}

func good(context.Context, string) (*ffmpeg.ProbeResult, error) {
	return &ffmpeg.ProbeResult{
		Video:           &ffmpeg.VideoStream{Codec: "h264", Width: 1280, Height: 720},
		Audio:           []ffmpeg.AudioStream{{Codec: "aac", Channels: 2}},
		DurationSeconds: 30,
	}, nil
}

func refuses(context.Context, string) (*ffmpeg.ProbeResult, error) {
	return nil, fmt.Errorf("probe: %w", ffmpeg.ErrUnsupportedContainer)
}

// cannotRun is the shape a missing or unusable ffprobe binary arrives in:
// *exec.Error, never *exec.ExitError, and no context error anywhere.
func cannotRun(context.Context, string) (*ffmpeg.ProbeResult, error) {
	return nil, &exec.Error{Name: "ffprobe", Err: errors.New("no such file or directory")}
}

// =========================================================================
// THE RULE: only what was established is written.
// =========================================================================

// The first thing in the product that writes uploads.OutcomeRefused. #264 built
// the state and nothing produced it; if this stops working the state is
// unreachable again and the Library's "Refused" row describes something that
// cannot occur.
func TestAnInspectionThatRefusesRecordsTheRefusal(t *testing.T) {
	st := newStore(t)
	st.file(t, "show.ts")
	st.verdicts["show.ts"] = uploads.UnverifiedVerdict(uploads.ReasonInterrupted)

	if _, err := run(t, st, refuses, "show.ts"); err != nil {
		t.Fatalf("RunVerify: %v", err)
	}
	got, ok := st.Verdict("show.ts")
	if !ok {
		t.Fatal("nothing was recorded")
	}
	if got.Outcome != uploads.OutcomeRefused {
		t.Fatalf("recorded %q, want %q. An inspection that READ the file and "+
			"rejected it must say so: recorded as unverified instead, the Library "+
			"says 'Not checked' about a file this server has read, and offers "+
			"'upload it again' -- the one remedy that cannot work.",
			got.Outcome, uploads.OutcomeRefused)
	}
	if got.Reason == "" {
		t.Error("the refusal carries no reason, so uploads.Store.PutVerdict " +
			"would refuse to store it and the operator is told nothing")
	}
}

// The other direction, and the one #202 left undecided: a file recorded as
// refused that PASSES on a second look. The refusal must be replaced, or a file
// made usable by an FFmpeg upgrade is condemned forever by a stale record.
func TestAnInspectionThatPassesReplacesAnEarlierRefusal(t *testing.T) {
	st := newStore(t)
	st.file(t, "show.ts")
	st.verdicts["show.ts"] = uploads.RefusedVerdict("polyemesis does not accept this container format")

	rep, err := run(t, st, good, "show.ts")
	if err != nil {
		t.Fatalf("RunVerify: %v", err)
	}
	got, _ := st.Verdict("show.ts")
	if got.Outcome != uploads.OutcomeVerified {
		t.Fatalf("recorded %q, want %q. A refusal that cannot be cleared makes "+
			"every later inspection pointless: the file stays unusable as a "+
			"playlist item however many times the server reads it and agrees it "+
			"is fine.", got.Outcome, uploads.OutcomeVerified)
	}
	if got.Reason != "" {
		t.Errorf("the replaced verdict still carries the old refusal's reason %q", got.Reason)
	}
	if got.Info == nil {
		t.Error("a verified verdict with no MediaInfo leaves the Library row " +
			"blank, which is how a file stored before probing looks")
	}
	res, ok := rep.result.(Result)
	if !ok {
		t.Fatalf("result is %T, want Result", rep.result)
	}
	if res.Previous != uploads.OutcomeRefused || !res.Changed {
		t.Errorf("result reports previous=%q changed=%v; the operator reading the "+
			"jobs page cannot tell that this run is what unblocked the file",
			res.Previous, res.Changed)
	}
}

// ===================== THE TRAP =====================
//
// The failure mode this whole package is shaped around. An inspection that
// could not run establishes NOTHING, and nothing is exactly what may be
// written. Both starting states are asserted because they fail differently: an
// established refusal would be DOWNGRADED to "nobody read this", and an upload
// with no record at all -- every file predating verdicts -- would be moved from
// `unrecorded` to `unverified`, which uploadNotice, isSelectableUpload and
// playlistUploadProblems all treat differently.
func TestAnInspectionThatCouldNotRunRecordsNothing(t *testing.T) {
	cases := []struct {
		name    string
		seed    func(*recordingStore)
		wantHad bool
		because string
	}{
		{
			name: "over an established refusal",
			seed: func(s *recordingStore) {
				s.verdicts["show.ts"] = uploads.RefusedVerdict("this file carries no video or audio stream")
			},
			wantHad: true,
			because: "replacing 'this server read these bytes and they are not media' " +
				"with 'nobody read this' destroys a finding the server had and hands " +
				"back the remedy that cannot work",
		},
		{
			name:    "over an upload with no record at all",
			seed:    func(*recordingStore) {},
			wantHad: false,
			because: "every install has uploads stored before verdicts existed; " +
				"moving one from unrecorded to unverified strands media an operator " +
				"has had for a year, over a probe that never ran",
		},
		{
			name: "over a verified file",
			seed: func(s *recordingStore) {
				s.verdicts["show.ts"] = uploads.VerifiedVerdict(uploads.MediaInfo{AudioTracks: 2})
			},
			wantHad: true,
			because: "a file that passed does not stop having passed because this " +
				"server could not find its ffprobe today",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := newStore(t)
			st.file(t, "show.ts")
			c.seed(st)
			before, hadBefore := st.Verdict("show.ts")

			_, err := run(t, st, cannotRun, "show.ts")
			if err == nil {
				t.Fatal("RunVerify reported success having established nothing, so " +
					"the jobs page shows a green re-check that read no bytes")
			}
			if len(st.puts) != 0 {
				t.Fatalf("PutVerdict was called %d time(s) with %+v. NOTHING may be "+
					"written when the inspection did not happen: %s.",
					len(st.puts), st.puts, c.because)
			}
			after, hadAfter := st.Verdict("show.ts")
			if hadAfter != c.wantHad || after != before {
				t.Fatalf("the record moved from (%+v, recorded=%v) to (%+v, recorded=%v) -- %s",
					before, hadBefore, after, hadAfter, c.because)
			}
			// The failure has to SAY it recorded nothing. An error that only
			// says "could not inspect" leaves the operator assuming the row was
			// updated to reflect the failure.
			if !strings.Contains(err.Error(), "nothing was recorded") {
				t.Errorf("the failure reads %q; it must state that nothing was "+
					"recorded, or an operator reads a failed re-check as one that "+
					"downgraded the file", err)
			}
			// RETRYABLE. A fork that failed with EAGAIN under a live encode is
			// exactly the shape a retry fixes, and the attempt ceiling bounds it.
			if jobs.IsPermanent(err) {
				t.Errorf("the failure is Permanent, so a transient fork failure " +
					"under a live encode gives up on the first attempt")
			}
		})
	}
}

// A file that has gone away since the job was queued. No bytes means no verdict
// about them, and a sidecar describing a file that does not exist outlives the
// job that wrote it.
func TestAFileThatIsGoneRecordsNothing(t *testing.T) {
	st := newStore(t)
	st.verdicts["show.ts"] = uploads.RefusedVerdict("this file carries no video or audio stream")

	probed := false
	_, err := run(t, st, func(ctx context.Context, p string) (*ffmpeg.ProbeResult, error) {
		probed = true
		return good(ctx, p)
	}, "show.ts")
	if err == nil {
		t.Fatal("RunVerify succeeded against a file that is not on disk")
	}
	if probed {
		t.Error("the prober was run against a path with no file at it")
	}
	if len(st.puts) != 0 {
		t.Fatalf("PutVerdict was called with %+v for a file that does not exist; "+
			"a verdict about absent bytes is a record that can never be checked "+
			"against anything", st.puts)
	}
	if !jobs.IsPermanent(err) {
		t.Error("a deleted file is not going to come back on the next attempt, " +
			"so retrying spends the ceiling to reach the same answer")
	}
}

// No ffprobe on the box. Fails, says what to do, and -- the part that matters --
// writes nothing, because "this server has no inspector" is not a finding about
// anybody's file.
func TestNoProberRecordsNothingAndSaysWhatToDo(t *testing.T) {
	st := newStore(t)
	st.file(t, "show.ts")
	p := New(quietLog(), Config{FFprobe: "", Uploads: st})
	job, _ := NewJob(Params{Upload: "show.ts"})
	err := p.RunVerify(context.Background(), job, &noopReporter{})
	if err == nil {
		t.Fatal("RunVerify succeeded with no ffprobe configured")
	}
	if len(st.puts) != 0 {
		t.Fatalf("PutVerdict was called with %+v; an install with no ffprobe "+
			"would re-check every upload into 'Not checked' and erase whatever "+
			"the last install with a working ffprobe had established", st.puts)
	}
	if !strings.Contains(err.Error(), "install FFmpeg") {
		t.Errorf("the failure reads %q and does not name the remedy", err)
	}
}

// A store that refuses the write must fail the job. A best-effort PutVerdict
// whose error is swallowed reports a green re-check whose finding is not on
// disk, which is the same lie by a different route.
func TestAWriteThatFailsFailsTheJob(t *testing.T) {
	st := newStore(t)
	st.file(t, "show.ts")
	st.putErr = errors.New("read-only file system")

	if _, err := run(t, st, refuses, "show.ts"); err == nil {
		t.Fatal("RunVerify reported success while its finding was not stored, so " +
			"the jobs page says the file was re-checked and the Library still " +
			"shows the old state")
	}
}

// ------------------------------------------------------------ the job's shape

// Listable is the rule about which names the product admits to having, and it is
// asked HERE as well as at the handler, because a job row survives a restart and
// nothing re-validates it on the way out of the database. A sidecar name reaching
// the worker means an ffprobe against `.probe-<name>.json` and then a verdict
// written beside it -- a file this product has no other way to create.
func TestOnlyANameTheLibraryListsCanBeVerified(t *testing.T) {
	for _, name := range []string{
		"",
		"   ",
		".probe-show.ts.json",
		".partial-show.ts",
		".partial-claim-show.ts",
		"../../etc/passwd",
		"sub/dir.ts",
		" show.ts",
	} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			if _, err := NewJob(Params{Upload: name}); err == nil {
				t.Fatalf("NewJob accepted %q. uploads.Listable owns which names the "+
					"Library offers, and a name it refuses reaching the worker means "+
					"a probe against a sidecar and a verdict written beside it.", name)
			}
			// And the worker refuses it too, for a row that predates this check
			// or was written by hand.
			st := newStore(t)
			p := New(quietLog(), Config{FFprobe: "/usr/bin/ffprobe", Uploads: st})
			raw, _ := json.Marshal(Params{Upload: name})
			err := p.RunVerify(context.Background(),
				jobs.Job{Kind: Kind, Params: raw}.Normalized(), &noopReporter{})
			if err == nil {
				t.Fatalf("RunVerify accepted %q from a stored job row", name)
			}
			if !jobs.IsPermanent(err) {
				t.Errorf("retrying %q forever cannot make it a legal name", name)
			}
			if len(st.puts) != 0 {
				t.Errorf("something was recorded about %q: %+v", name, st.puts)
			}
		})
	}
}

// Unique folds on Kind AND Target, so pressing "Check again" twice asks once.
// The target spelling is shared with playlistmedia.NormaliseTarget on purpose;
// this pins that sharing it is safe by pinning that the kind differs.
func TestTwoSubmissionsForOneUploadAreOneJob(t *testing.T) {
	a, err := NewJob(Params{Upload: "show.ts"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewJob(Params{Upload: "show.ts"})
	if !a.Unique {
		t.Error("the job is not Unique, so a double click queues two probes of " +
			"the same multi-gigabyte file")
	}
	if a.Target != b.Target || a.Kind != b.Kind {
		t.Fatalf("two submissions for one upload do not fold: %s/%s vs %s/%s",
			a.Kind, a.Target, b.Kind, b.Target)
	}
	if a.Target != "upload:show.ts" {
		t.Errorf("Target is %q, want upload:show.ts", a.Target)
	}
	if err := a.Validate(); err != nil {
		t.Errorf("the queue would refuse this job: %v", err)
	}
}

// The processor registers under its own kind, at its own limit. A kind nothing
// registers is a button that queues work no worker will ever claim.
func TestRegisterUsesTheKindTheCatalogueNames(t *testing.T) {
	var gotKind jobs.Kind
	var gotLimit int
	reg := registryFunc(func(k jobs.Kind, limit int, _ jobs.Worker) error {
		gotKind, gotLimit = k, limit
		return nil
	})
	if err := New(quietLog(), Config{}).Register(reg); err != nil {
		t.Fatal(err)
	}
	if gotKind != Kind {
		t.Errorf("registered %q, want %q", gotKind, Kind)
	}
	if gotLimit != Limit || gotLimit < 1 {
		t.Errorf("registered at limit %d, want %d", gotLimit, Limit)
	}
}

type registryFunc func(jobs.Kind, int, jobs.Worker) error

func (f registryFunc) Register(k jobs.Kind, limit int, w jobs.Worker) error { return f(k, limit, w) }

// The nil-store guard has to fire on the INTERFACE being nil. A typed nil
// *uploads.Store assigned to it is not nil as an interface and would panic on
// the first Resolve instead; cmd/polyemesis has verifyStore for exactly that,
// and this pins the guard it depends on.
func TestNoStoreFailsRatherThanPanics(t *testing.T) {
	p := New(quietLog(), Config{FFprobe: "/usr/bin/ffprobe"})
	job, _ := NewJob(Params{Upload: "show.ts"})
	err := p.RunVerify(context.Background(), job, &noopReporter{})
	if err == nil {
		t.Fatal("RunVerify succeeded with no store")
	}
	if !strings.Contains(err.Error(), "no upload store") {
		t.Errorf("the failure reads %q and does not name the problem", err)
	}
}
