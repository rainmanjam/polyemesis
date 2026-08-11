package uploads

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// age backdates a file so the sweep's cutoff sees it as a leftover rather than
// as something that might still be in flight.
func age(t *testing.T, s *Store, name string, d time.Duration) {
	t.Helper()
	when := time.Now().Add(-d)
	if err := os.Chtimes(filepath.Join(s.dir, name), when, when); err != nil {
		t.Fatalf("backdate %s: %v", name, err)
	}
}

// #185. Nothing in the product ever swept <dataDir>/uploads, so a ".partial-"
// file left by a process killed mid-upload -- or by a Discard whose os.Remove
// failed -- occupied the volume the database and the recorder share, forever,
// with the free-space floor unaware of it and nothing reporting the total.
//
// EVERY REFUSAL HAS A CONTROL IN THE SAME TEST, because a Sweep that removed
// everything and a Sweep that removed the right things look identical from any
// assertion that only checks the leftovers are gone.
func TestSweepClearsStagedLeftoversAndLeavesRealUploadsAlone(t *testing.T) {
	s := newStore(t)
	writeUpload(t, s, "show-abcd1234.ts", "twelve bytes")
	writeUpload(t, s, ".probe-show-abcd1234.ts.json", `{"verified":true}`)
	writeUpload(t, s, ".partial-1234567.ts", "stranded")
	writeUpload(t, s, claimName("gone-abcd1234.ts"), "")
	for _, n := range []string{"show-abcd1234.ts", ".probe-show-abcd1234.ts.json",
		".partial-1234567.ts", claimName("gone-abcd1234.ts")} {
		age(t, s, n, 2*time.Hour)
	}

	res, err := s.Sweep(time.Hour)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	if got := dirNames(t, s); len(got) != 2 {
		t.Fatalf("after the sweep the directory holds %v; want the upload and its record", got)
	}
	for _, want := range []string{"show-abcd1234.ts", ".probe-show-abcd1234.ts.json"} {
		if _, err := os.Stat(filepath.Join(s.dir, want)); err != nil {
			t.Errorf("the sweep removed %q, which belongs to a published upload: %v", want, err)
		}
	}
	if res.Staged != 2 {
		t.Errorf("Staged = %d, want 2 (one stranded upload and one name reservation); "+
			"nothing reports the accumulated size if the count is wrong", res.Staged)
	}
	if res.StagedBytes != int64(len("stranded")) {
		t.Errorf("StagedBytes = %d, want %d", res.StagedBytes, len("stranded"))
	}
	if res.Empty() {
		t.Error("SweepResult.Empty() is true after removing two files, so the " +
			"startup log would say nothing on the boot after the crash")
	}
}

// The age gate is the whole reason this is safe to run anywhere other than
// startup. A ".partial-" file that is younger than the cutoff is an upload
// whose bytes are still arriving, and sweeping it would delete an operator's
// transfer mid-flight -- the exact failure #118 spent a round removing.
func TestSweepDoesNotTouchAnUploadThatIsStillArriving(t *testing.T) {
	s := newStore(t)
	p, err := s.Stage(strings.NewReader("bytes still landing"), "show.ts", 0, 0)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	// Old enough to be swept by a cutoff that ignored age, and far too young
	// for this one.
	age(t, s, filepath.Base(p.Path()), time.Minute)

	res, err := s.Sweep(time.Hour)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Staged != 0 {
		t.Errorf("the sweep removed %d staged file(s) while an upload was in flight", res.Staged)
	}
	if _, err := os.Stat(p.Path()); err != nil {
		t.Fatalf("the sweep deleted the bytes of an upload that had not finished: %v", err)
	}
	// And the Pending is still committable, which is the property an operator
	// actually loses if this goes wrong.
	if _, err := p.Commit(VerifiedVerdict(MediaInfo{AudioTracks: 1})); err != nil {
		t.Fatalf("Commit after a sweep: %v", err)
	}
}

// The second leftover: a verdict record whose upload was removed by something
// outside this process. It is invisible, unreferenceable and a few hundred
// bytes -- and it is also the thing that makes a name reused by a later upload
// inherit the previous file's PASS, which is why Delete already takes the
// sidecar with it.
func TestSweepClearsAVerdictRecordWhoseUploadIsGone(t *testing.T) {
	s := newStore(t)
	writeUpload(t, s, "kept-abcd1234.ts", "bytes")
	writeUpload(t, s, ".probe-kept-abcd1234.ts.json", `{"verified":true}`)
	writeUpload(t, s, ".probe-gone-abcd1234.ts.json", `{"verified":true}`)
	// The third case: a record written by a Commit that has reserved its name
	// and not yet renamed the bytes in. Its upload is "absent" by every test
	// except the right one, and removing it would turn a verified upload into
	// an unrecorded one.
	writeUpload(t, s, claimName("claiming-abcd1234.ts"), "")
	writeUpload(t, s, ".probe-claiming-abcd1234.ts.json", `{"verified":true}`)
	for _, n := range dirNames(t, s) {
		age(t, s, n, 2*time.Hour)
	}

	res, err := s.Sweep(time.Hour)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Sidecars != 1 {
		t.Errorf("Sidecars = %d, want exactly 1 (only the orphan)", res.Sidecars)
	}
	if _, err := os.Stat(filepath.Join(s.dir, ".probe-gone-abcd1234.ts.json")); err == nil {
		t.Error("the record for an upload that no longer exists survived the sweep")
	}
	if _, err := os.Stat(filepath.Join(s.dir, ".probe-kept-abcd1234.ts.json")); err != nil {
		t.Errorf("the sweep removed the record of an upload that is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.dir, ".probe-claiming-abcd1234.ts.json")); err != nil {
		t.Errorf("the sweep removed the record of an upload being published, which "+
			"turns a verified upload into an unrecorded one: %v", err)
	}
}

// A young orphan is left alone for the same reason a young staged file is:
// Pending.Commit writes the record BEFORE the bytes are renamed into place, so
// for the length of one rename an orphan and a commit-in-progress are the same
// two files on disk.
func TestSweepLeavesAYoungVerdictRecordAlone(t *testing.T) {
	s := newStore(t)
	writeUpload(t, s, ".probe-gone-abcd1234.ts.json", `{"verified":true}`)

	res, err := s.Sweep(time.Hour)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Sidecars != 0 {
		t.Errorf("the sweep removed %d record(s) written moments ago", res.Sidecars)
	}
}
