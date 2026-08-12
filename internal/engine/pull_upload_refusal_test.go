package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/uploads"
)

// #255: an INHERITED pull source -- one configured before the save-time gate
// existed -- is re-examined at reconcile, and the two bad states get different
// answers. Refused stops the ingest; unchecked keeps streaming and is warned
// about on the card.
//
// EVERY REFUSAL HERE IS PAIRED WITH A CONTROL THAT REACHES AIR, because "the
// ingest did not start" is satisfied by an ingest that failed to start for
// reasons that have nothing to do with this change -- this fixture's FFmpeg
// path cannot exec, so nothing in the package could tell a gate from a broken
// spawn without one. The controls are what make the assertion mean the gate.

const refusedWhy = "no video or audio stream this server can play"

// seedPullUpload puts a stored upload in an engine's data directory and returns
// the pull URL that names it. A nil verdict is the third state: bytes on disk
// with no record at all, which is every upload stored before verdicts existed.
func seedPullUpload(t *testing.T, dataDir, name string, v *uploads.Verdict) string {
	t.Helper()
	store, err := uploads.New(dataDir)
	if err != nil {
		t.Fatalf("uploads.New(%s): %v", dataDir, err)
	}
	if err := os.WriteFile(filepath.Join(store.Dir(), name), []byte("stand-in bytes"), 0o600); err != nil {
		t.Fatalf("write upload: %v", err)
	}
	if v != nil {
		// PutVerdict refuses to record one for a name it cannot find, so the
		// file above is load-bearing rather than decoration.
		if err := store.PutVerdict(name, *v); err != nil {
			t.Fatalf("PutVerdict(%s): %v", name, err)
		}
	}
	return uploads.PullURL(name)
}

func verdictOf(v uploads.Verdict) *uploads.Verdict { return &v }

// The split itself, asserted on the one function that decides it.
//
// The refused row and the unchecked row are the whole of #255's answer, and a
// change that collapsed them -- in either direction -- would take exactly one
// of these two rows down. That collapse is not hypothetical: a mechanical merge
// produced "stored without being checked (no video or audio stream this server
// can play); upload it again", a reason only an inspection could write beside
// the one remedy that cannot fix a permanent refusal.
func TestOnlyARefusedUploadStopsAnInheritedPullSource(t *testing.T) {
	dir := t.TempDir()
	e := &Engine{}
	e.cfg.DataDir = dir

	for _, tc := range []struct {
		name    string
		verdict *uploads.Verdict
		stop    bool
		why     string
	}{
		{
			name:    "refused.ts",
			verdict: verdictOf(uploads.RefusedVerdict(refusedWhy)),
			stop:    true,
			why: "an inspection RAN and rejected these bytes. That cannot be an " +
				"install-wide condition, so the missing-ffprobe blast radius that " +
				"argues for failing open does not apply to it",
		},
		{
			name:    "unchecked.ts",
			verdict: verdictOf(uploads.UnverifiedVerdict(uploads.ReasonNoProber)),
			stop:    false,
			why: "unverified is a fact about this SERVER, never about the file: on " +
				"an install with no ffprobe every upload looks like this, so stopping " +
				"here is a kill switch keyed to a missing subprocess",
		},
		{
			name:    "verified.ts",
			verdict: verdictOf(uploads.VerifiedVerdict(uploads.MediaInfo{AudioTracks: 2})),
			stop:    false,
			why:     "this file was inspected and passed",
		},
		{
			name:    "norecord.ts",
			verdict: nil,
			stop:    false,
			why: "no record at all is every upload stored before verdicts existed, " +
				"and refusing those would strand media an operator has had for a year",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			url := seedPullUpload(t, dir, tc.name, tc.verdict)
			got := e.pullUploadRefusal("this source", url)
			if tc.stop && got == "" {
				t.Fatalf("%q did not stop the ingest, and it must: %s", tc.name, tc.why)
			}
			if !tc.stop && got != "" {
				t.Fatalf("%q stopped the ingest with %q, and it must not: %s", tc.name, got, tc.why)
			}
			if !tc.stop {
				return
			}
			for _, want := range []string{tc.name, refusedWhy, "inspected and refused", "point it at a different file"} {
				if !strings.Contains(got, want) {
					t.Errorf("the refusal does not mention %q: %s", want, got)
				}
			}
			// The remedy for a permanent refusal is not "try again". This is
			// the assertion the merged sentence would have failed.
			if strings.Contains(got, "upload it again") {
				t.Errorf("a file that was inspected and refused is met with "+
					"\"upload it again\", which is the one thing that cannot fix it: %s", got)
			}
		})
	}
}

// A URL that names no upload is nobody's business here, and one that names an
// upload in a different SPELLING is the same upload.
//
// The spellings are #266's: four ways of writing one pull source that produce
// one identical -i argument. A gate that parsed the URL itself rather than
// asking ffmpeg.PullFilePath -- the normalisation that BUILDS that argument --
// would answer differently for each, which is the bypass that PR closed and
// which a new gate can silently reintroduce.
func TestTheRefusalIsKeyedOnTheInputFFmpegWillOpenNotTheSpelling(t *testing.T) {
	dir := t.TempDir()
	e := &Engine{}
	e.cfg.DataDir = dir
	seedPullUpload(t, dir, "refused.ts", verdictOf(uploads.RefusedVerdict(refusedWhy)))

	for _, spelling := range []string{
		"file://uploads/refused.ts",
		"file://uploads//refused.ts",
		"file://uploads/./refused.ts",
		"file://./uploads/refused.ts",
		"file://uploads/././refused.ts",
	} {
		if got := e.pullUploadRefusal("this source", spelling); got == "" {
			t.Errorf("%q was not refused, though it opens the same file as "+
				"file://uploads/refused.ts -- the gate is keyed on the spelling", spelling)
		}
	}
	for _, other := range []string{
		"rtsp://camera.example/live",
		"https://example.test/stream.m3u8",
		"file://recordings/refused.ts",
		"",
	} {
		if got := e.pullUploadRefusal("this source", other); got != "" {
			t.Errorf("%q is not a pull from a stored upload and was refused anyway: %s", other, got)
		}
	}
}

// Not being able to ask must not take a programme off air.
//
// The three save-time gates fail CLOSED here and this one deliberately does
// not: a save can refuse and hand the operator the error, while the only
// refusal available at reconcile is stopping a running stream -- and "this
// server's data directory could not be opened" is exactly the class of
// install-wide fact the fail-open argument is about.
func TestAnUnaskableStoreDoesNotStopTheIngest(t *testing.T) {
	e := &Engine{}
	e.cfg.DataDir = ""
	if got := e.pullUploadRefusal("this source", uploads.PullURL("refused.ts")); got != "" {
		t.Errorf("an engine with no data directory stopped its ingest: %s", got)
	}
}

// pullEngine brings up a manager whose second source pulls from a seeded
// upload, and returns that source's engine after a full reconcile.
func pullEngine(t *testing.T, name string, v *uploads.Verdict) *Engine {
	t.Helper()
	m, store := managerFixture(t)
	url := seedPullUpload(t, m.cfg.DataDir, name, v)

	ing := db.DefaultSettings().Ingest
	ing.Mode = db.IngestPull
	ing.Pull.URL = url
	src := &db.Source{Name: "Pull", Enabled: true, Ingest: ing}
	if err := store.CreateSource(src); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	e := m.Engine(src.ID)
	if e == nil {
		t.Fatal("no engine for the pull source")
	}
	return e
}

func ingestSpawned(e *Engine) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.ingest != nil
}

// The gate reaches the engine's own reconcile, which is the whole of #255: the
// save-time gate is scoped to what a save INTRODUCES, so a source configured
// before it existed had nothing re-examining it and -- unlike a playlist item,
// which the normalise worker refuses a second time -- no downstream gate either.
//
// The reason travels with the stop. A stopped ingest and a log line is what an
// operator finds at 3am; the reconcile report is where the refusal has
// somewhere to be reported, so the sentence is asserted there rather than just
// the absence of a child.
func TestReconcileStopsAPullIngestNamingARefusedUpload(t *testing.T) {
	e := pullEngine(t, "refused.ts", verdictOf(uploads.RefusedVerdict(refusedWhy)))

	if ingestSpawned(e) {
		t.Fatal("the ingest child was spawned for an upload this server inspected " +
			"and refused, so nothing re-examines an inherited pull source")
	}
	var stop *ReloadNote
	for _, n := range e.LastReload().Notes {
		if n.Tier == "ingest" && n.Action == reloadStop {
			note := n
			stop = &note
		}
	}
	if stop == nil {
		t.Fatalf("the reconcile stopped the ingest and said nothing about why: %+v",
			e.LastReload().Notes)
	}
	for _, want := range []string{"refused.ts", refusedWhy, "point it at a different file"} {
		if !strings.Contains(stop.Reason, want) {
			t.Errorf("the reconcile's reason does not mention %q: %s", want, stop.Reason)
		}
	}
}

// THE CONTROL FOR THE TEST ABOVE, and the fail-open half of the decision in its
// own right. Same fixture, same route, an upload recorded as merely unchecked:
// the child is spawned.
//
// Without this, "the ingest did not start" above is satisfied by an engine that
// starts no ingest at all -- and it also asserts the policy, since an
// implementation that stopped on any recorded objection would kill every pull
// ingest on an install whose ffprobe is missing.
func TestReconcileKeepsAPullIngestNamingAnUncheckedUpload(t *testing.T) {
	e := pullEngine(t, "unchecked.ts", verdictOf(uploads.UnverifiedVerdict(uploads.ReasonNoProber)))

	if !ingestSpawned(e) {
		t.Fatal("an upload stored without being checked took the ingest off air. " +
			"That state is a fact about this server -- an install with no ffprobe " +
			"has every upload in it -- and #255's answer for it is a warning on the card")
	}
	for _, n := range e.LastReload().Notes {
		if n.Tier == "ingest" && n.Action == reloadStop {
			t.Errorf("the reconcile reported stopping the ingest: %s", n.Reason)
		}
	}
}

// The second control: an upload that passed inspection, on the same path.
func TestReconcileKeepsAPullIngestNamingAVerifiedUpload(t *testing.T) {
	e := pullEngine(t, "verified.ts", verdictOf(uploads.VerifiedVerdict(uploads.MediaInfo{AudioTracks: 2})))
	if !ingestSpawned(e) {
		t.Fatal("a pull source naming an upload this server inspected and PASSED " +
			"did not start, so the gate is refusing something other than a refusal")
	}
}

// The standby is a second thing that reaches air, and the save-time gate checks
// it precisely because "storing a source that is off today and switched on
// during an outage is the case this exists for". A reconcile gate on the
// primary alone would leave that hole on the path nobody is watching.
func TestReconcileStopsABackupPullIngestNamingARefusedUpload(t *testing.T) {
	refused := func(t *testing.T, name string, v *uploads.Verdict) *Engine {
		t.Helper()
		e := failoverEngine(t)
		dir := t.TempDir()
		e.cfg.DataDir = dir
		s := failoverOnSettings()
		s.Failover.Backup.Enabled = true
		s.Failover.Backup.Mode = db.IngestPull
		s.Failover.Backup.Pull.URL = seedPullUpload(t, dir, name, v)
		setSettings(e, s)
		e.reconcileBackupIngest(s)
		return e
	}

	e := refused(t, "refused.ts", verdictOf(uploads.RefusedVerdict(refusedWhy)))
	if e.backupHub() != nil {
		t.Error("the standby listener came up on an upload this server inspected and refused")
	}

	// The control, without which the assertion above is satisfied by a backup
	// tier that never starts in this fixture at all.
	ok := refused(t, "unchecked.ts", verdictOf(uploads.UnverifiedVerdict(uploads.ReasonNoProber)))
	if ok.backupHub() == nil {
		t.Error("the standby did not come up for an upload that was merely stored " +
			"unchecked, so the backup gate is stopping more than a refusal")
	}
}
