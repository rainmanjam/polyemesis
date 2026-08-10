package uploads

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// THE THIRD STATE, at the level that stores it.
//
// A stored upload is in one of three states -- inspected and accepted, refused
// (in which case it is not stored), and STORED WITHOUT BEING INSPECTED. The
// third one used to be recorded the same way as the second half of the first:
// the sidecar held a MediaInfo or was absent, so "nobody read this" and "this
// predates sidecars" and "the record has not landed yet" were the same bytes on
// disk. Everything below is about telling them apart.

func TestAnUninspectedUploadIsRecordedAsOneAndSaysWhy(t *testing.T) {
	s := newStore(t)
	p, err := s.Stage(strings.NewReader("bytes"), "show.mkv", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	f, err := p.Commit(UnverifiedVerdict(ReasonInterrupted))
	if err != nil {
		t.Fatal(err)
	}
	if f.Verified {
		t.Error("Commit's own return claims the file was verified")
	}
	if f.UnverifiedReason != ReasonInterrupted {
		t.Errorf("reason = %q, want %q", f.UnverifiedReason, ReasonInterrupted)
	}
	if f.Media != nil {
		t.Errorf("an uninspected file carries metadata: %+v", f.Media)
	}

	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("listing = %+v", list)
	}
	if list[0].Verified || list[0].UnverifiedReason != ReasonInterrupted {
		t.Errorf("the listing lost the verdict: %+v", list[0])
	}

	v, recorded := s.Verdict(f.Name)
	if !recorded {
		t.Fatal("no verdict was written beside the file")
	}
	if v.Verified || v.Reason != ReasonInterrupted {
		t.Errorf("verdict on disk = %+v", v)
	}
}

func TestAnInspectedUploadCarriesWhatWasFound(t *testing.T) {
	s := newStore(t)
	p, err := s.Stage(strings.NewReader("bytes"), "show.mkv", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	f, err := p.Commit(VerifiedVerdict(MediaInfo{AudioTracks: 3, VideoCodec: "h264"}))
	if err != nil {
		t.Fatal(err)
	}
	if !f.Verified || f.Media == nil || f.Media.AudioTracks != 3 {
		t.Fatalf("Commit's return lost the verdict: %+v", f)
	}
	list, _ := s.List()
	if !list[0].Verified || list[0].Media == nil || list[0].Media.VideoCodec != "h264" {
		t.Fatalf("the listing lost the verdict: %+v", list[0])
	}
	if list[0].UnverifiedReason != "" {
		t.Errorf("a verified file carries a reason: %q", list[0].UnverifiedReason)
	}
}

// NO RECORD IS NOT A PASS. Every upload an install stored before verdicts
// existed has no sidecar, and the fail-closed reading of that is the only safe
// one -- but it is DISTINGUISHABLE from a recorded refusal to inspect, and the
// settings validator relies on that difference to avoid stranding a year of
// legitimate media.
func TestAFileWithNoRecordReadsAsUnverifiedButNotAsEvidence(t *testing.T) {
	s := newStore(t)
	if err := os.WriteFile(filepath.Join(s.dir, "legacy.ts"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	list, _ := s.List()
	if len(list) != 1 || list[0].Verified {
		t.Fatalf("a file with no record reads as verified: %+v", list)
	}
	if list[0].UnverifiedReason != "" {
		t.Errorf("a file with no record invented a reason: %q", list[0].UnverifiedReason)
	}
	if _, recorded := s.Verdict("legacy.ts"); recorded {
		t.Error("a file with no sidecar reports a recorded verdict")
	}
}

// A record this process cannot parse is a record, and the safe reading of one
// is the unverified one. The old readMedia returned nil for it, which is the
// same answer it gave for a file that had never been probed at all.
func TestAnUnreadableRecordFailsClosed(t *testing.T) {
	s := newStore(t)
	if err := os.WriteFile(filepath.Join(s.dir, "x.ts"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, sidecarName("x.ts")), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	v, recorded := s.Verdict("x.ts")
	if !recorded {
		t.Fatal("an unreadable record reads as no record at all")
	}
	if v.Verified {
		t.Error("an unreadable record reads as a pass")
	}
}

// A record claiming a pass while carrying no reading is still not allowed to
// smuggle metadata in the other direction: an unverified record's Info is
// dropped on read, so a hand-edited sidecar cannot make the Library show
// numbers for a file nobody read.
func TestAnUnverifiedRecordCannotCarryMetadata(t *testing.T) {
	s := newStore(t)
	if err := os.WriteFile(filepath.Join(s.dir, "x.ts"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, sidecarName("x.ts")),
		[]byte(`{"verified":false,"info":{"audioTracks":9,"videoCodec":"h264"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	v, _ := s.Verdict("x.ts")
	if v.Info != nil {
		t.Errorf("an unverified record kept its metadata: %+v", v.Info)
	}
	list, _ := s.List()
	if list[0].Media != nil {
		t.Errorf("the listing shows metadata for a file nobody read: %+v", list[0].Media)
	}
}

// F9. THE RECORD IS ON DISK BEFORE THE FILE IS. Commit used to publish and the
// caller wrote the metadata afterwards, leaving a window in which the file was
// listed, had a working pullUrl and was a legal playlist item while nothing on
// disk said whether anyone had read it. The window is short and that is exactly
// why it mattered: it meant "no record" could never be relied on to mean
// anything.
func TestTheVerdictIsWrittenBeforeTheFileIsPublished(t *testing.T) {
	s := newStore(t)
	p, err := s.Stage(strings.NewReader("bytes"), "show.mkv", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	// The sidecar's name is derivable from the promised name, so it can be
	// watched for. Poll from a second goroutine and record whether the file
	// ever existed while its record did not.
	var mu sync.Mutex
	var sawFileWithoutRecord bool
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		final := filepath.Join(s.dir, p.Name())
		side := filepath.Join(s.dir, sidecarName(p.Name()))
		for {
			select {
			case <-stop:
				return
			default:
			}
			if st, err := os.Stat(final); err == nil && st.Size() > 0 {
				if _, err := os.Stat(side); err != nil {
					mu.Lock()
					sawFileWithoutRecord = true
					mu.Unlock()
				}
			}
		}
	}()
	if _, err := p.Commit(UnverifiedVerdict(ReasonNoProber)); err != nil {
		t.Fatal(err)
	}
	close(stop)
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if sawFileWithoutRecord {
		t.Error("the published file existed before its verdict did")
	}
}

// F8. Commit renamed with no O_EXCL, so a name collision replaced one
// operator's bytes with another's while the first file's record survived to
// describe them -- the "tell two similar files apart" property this feature
// exists to provide, inverted. 200,000 draws of SafeName("show.ts") produced
// four collisions at the old four-byte suffix.
func TestCommitRefusesToOverwriteAnExistingUpload(t *testing.T) {
	s := newStore(t)
	p, err := s.Stage(strings.NewReader("VICTIM"), "show.ts", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	f, err := p.Commit(VerifiedVerdict(MediaInfo{AudioTracks: 1}))
	if err != nil {
		t.Fatal(err)
	}

	// A second Pending forced onto the same name, which is what a collision is.
	p2, err := s.Stage(strings.NewReader("ATTACKER"), "show.ts", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	p2.name = f.Name
	if _, err := p2.Commit(VerifiedVerdict(MediaInfo{AudioTracks: 7})); err == nil {
		t.Fatal("a colliding Commit replaced an existing upload")
	}

	b, err := os.ReadFile(filepath.Join(s.dir, f.Name))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "VICTIM" {
		t.Fatalf("the stored file reads %q; it was overwritten", string(b))
	}
	// And the loser's record did not overwrite the winner's either.
	if v, _ := s.Verdict(f.Name); v.Info == nil || v.Info.AudioTracks != 1 {
		t.Errorf("the surviving file's record describes the other upload: %+v", v.Info)
	}
	// The loser still owns its bytes, so its caller's deferred Discard clears
	// them: a failed Commit must not consume the upload.
	if err := p2.Discard(); err != nil {
		t.Fatalf("Discard after a failed Commit: %v", err)
	}
}

// The random suffix is the first of the two defences and its width is a
// collision budget, not a secrecy one. Pinned so it cannot quietly shrink back.
func TestTheStoredNameCarriesEnoughEntropyToNotCollide(t *testing.T) {
	if nameSuffixBytes < 8 {
		t.Fatalf("nameSuffixBytes = %d; 32 bits with a caller-controlled stem was "+
			"measured colliding four times in 200,000 draws", nameSuffixBytes*8)
	}
	seen := make(map[string]bool, 200000)
	for i := 0; i < 200000; i++ {
		n := SafeName("show.ts")
		if seen[n] {
			t.Fatalf("SafeName collided after %d draws", i)
		}
		seen[n] = true
	}
}

// F7. Running out of room DURING the write is the same condition as running out
// before it, and it must not be reported as an internal error carrying the
// server's paths.
//
// DRIVEN FROM THE READER because a unit test cannot fill a volume: what is
// pinned is the CLASSIFICATION -- an ENOSPC reaching Stage's copy becomes
// ErrNoSpace -- which is the line that was missing. The end-to-end shape (a
// real full filesystem) was reproduced by review on a 20 MB ramdisk and is not
// re-created here; the status mapping it feeds is asserted in
// internal/api.TestAStoreFailureDoesNotLeakServerPaths.
type enospcReader struct{ n int }

func (r *enospcReader) Read(p []byte) (int, error) {
	if r.n > 0 {
		r.n--
		p[0] = 'x'
		return 1, nil
	}
	return 0, &os.PathError{Op: "write", Path: "/srv/data/uploads/.partial-1", Err: syscall.ENOSPC}
}

func TestRunningOutOfRoomMidWriteIsReportedAsNoSpace(t *testing.T) {
	s := newStore(t)
	_, err := s.Stage(&enospcReader{n: 16}, "show.ts", 1<<20, 0)
	if !errors.Is(err, ErrNoSpace) {
		t.Fatalf("error = %v, want ErrNoSpace", err)
	}
	// And the classified error says nothing about where the server keeps files.
	if strings.Contains(err.Error(), "/srv/data") {
		t.Errorf("the classified error still carries the server's path: %v", err)
	}
	assertDirEmpty(t, s)
}
