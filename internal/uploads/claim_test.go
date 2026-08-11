package uploads

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// #203 item 1. Commit claimed its final name with O_CREATE|O_EXCL, which is
// what makes a collision loud instead of one operator's bytes silently
// replacing another's -- but the claim was a ZERO-BYTE FILE UNDER THE FINAL
// NAME, and that name is Listable. So between the claim and the rename the
// Library showed an empty row with a working pullUrl, and PUT /api/v1/settings
// would have accepted it as a playlist item.
//
// HOW THE WINDOW IS HELD OPEN, and why this is not a wall-clock test. Nothing
// here asserts on elapsed time and nothing sleeps waiting for a deadline. The
// staged bytes are removed before Commit, which makes the rename fail; and
// renameStaged retries a FIXED number of times, so the interval during which
// Commit has claimed the name and published nothing is bounded below by a loop
// count in the code under test rather than by how fast this machine is. On the
// broken code the empty row is present for the whole of it, so one sample is
// enough and the poll takes hundreds.
func TestNothingIsListableUnderTheFinalNameUntilTheBytesAreThere(t *testing.T) {
	s := newStore(t)
	p, err := s.Stage(strings.NewReader("real bytes"), "show.ts", 0, 0)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if err := os.Remove(p.Path()); err != nil {
		t.Fatalf("remove the staged bytes to hold the window open: %v", err)
	}

	var mu sync.Mutex
	var seen []File
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if list, err := s.List(); err == nil && len(list) > 0 {
				mu.Lock()
				seen = append(seen, list...)
				mu.Unlock()
			}
			time.Sleep(200 * time.Microsecond)
		}
	}()

	_, commitErr := p.Commit(VerifiedVerdict(MediaInfo{AudioTracks: 2}))
	close(stop)
	wg.Wait()

	if commitErr == nil {
		t.Fatal("Commit reported success with its staged bytes removed, so the " +
			"window this test watches was never opened")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) > 0 {
		t.Errorf("GET /api/v1/media would have listed %q at %d bytes while Commit "+
			"had only reserved the name -- a name reservation must be made under "+
			"something Listable refuses, not under the final name (%d sightings)",
			seen[0].Name, seen[0].Bytes, len(seen))
	}
	// And the reservation is not left behind. A claim that outlived its Commit
	// would make the name permanently unusable, which is worse than the window.
	if entries, err := os.ReadDir(s.dir); err != nil {
		t.Fatal(err)
	} else if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("a failed Commit left %v in the uploads directory", names)
	}
}

// The property the test above depends on, stated on its own so that a change to
// the claim's spelling fails HERE, with the reason, rather than as a timing
// mystery in the poll above.
func TestANameReservationIsNeverAListableName(t *testing.T) {
	const stored = "show-c37688205ca09aa2.ts"
	if claim := claimName(stored); Listable(claim) {
		t.Fatalf("Listable(%q) is true, so List offers a name reservation as media "+
			"and playlistUploadProblems accepts it as a playlist item", claim)
	}
	// Derived from the name and not from randomness, which is the whole reason
	// two Commits drawing the same stored name collide on it.
	if claimName(stored) != claimName(stored) {
		t.Fatal("claimName is not a function of its argument, so two Commits " +
			"racing for one name would not collide")
	}
}

// The other half of the reservation: it is exclusive. A second Commit for a
// name a first has already claimed must fail loudly rather than proceed and
// have one of the two renames win silently.
func TestACommitCannotTakeANameAnotherCommitHasReserved(t *testing.T) {
	s := newStore(t)
	p, err := s.Stage(strings.NewReader("ATTACKER"), "show.ts", 0, 0)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	// The reservation another Commit would be holding at this moment.
	if err := os.WriteFile(filepath.Join(s.dir, claimName(p.Name())), nil, 0o600); err != nil {
		t.Fatalf("seed the reservation: %v", err)
	}
	if _, err := p.Commit(VerifiedVerdict(MediaInfo{AudioTracks: 1})); err == nil {
		t.Fatal("Commit published over a name another Commit had already reserved")
	}
	if list, err := s.List(); err != nil {
		t.Fatal(err)
	} else if len(list) != 0 {
		t.Errorf("the refused Commit still published something: %+v", list)
	}
	// The loser still owns its bytes, so its caller's deferred Discard clears
	// them, and it did NOT remove the reservation it lost to.
	if err := p.Discard(); err != nil {
		t.Fatalf("Discard after a failed Commit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.dir, claimName(p.Name()))); err != nil {
		t.Errorf("the losing Commit removed the winner's reservation: %v", err)
	}
}
