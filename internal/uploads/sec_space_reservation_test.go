package uploads

import (
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The recurrence guard for #200: the free-space guard was read-check-write with
// nothing subtracting what other in-flight uploads had already promised to
// write, so every concurrent Stage read the same number and all of them were
// admitted against room for one.
//
// TWO STORES OVER ONE DIRECTORY, and that is the part of this test that matters
// most. api.Server.uploadStore builds a NEW *Store on every request by design
// (media.go:616), so a reservation living on the struct would be a fresh zero
// counter per request: correct-looking, green against a single Store, and
// bounding nothing in production. A fixture that shared one Store would certify
// exactly the fix that does not work. So the eight concurrent Stages here run
// over eight SEPARATE Stores rooted at the same directory, which is what the
// server does.

// blockingBody is a request body that reports when the server started reading
// it and then blocks, so every admitted upload is still inside Stage while the
// others are deciding.
//
// Without this the test would be a race: eight goroutines that each complete in
// microseconds may not overlap at all, and a run where they did not overlap
// would pass with the guard deleted. The block is what makes the overlap a
// property of the test rather than of the scheduler.
type blockingBody struct {
	started *atomic.Int64
	release <-chan struct{}
	once    sync.Once
	body    io.Reader
}

func (b *blockingBody) Read(p []byte) (int, error) {
	b.once.Do(func() {
		b.started.Add(1)
		<-b.release
	})
	return b.body.Read(p)
}

func TestConcurrentStagesCannotAllSpendTheSameFreeSpace(t *testing.T) {
	const (
		callers  = 8
		maxBytes = int64(1 << 20)  // each upload may write up to 1 MiB
		floor    = uint64(4 << 20) // the reserve the database and recorder need
	)
	dir := t.TempDir()

	// Room for the floor plus EXACTLY ONE upload. The second concurrent caller
	// is the first one that must be refused.
	//
	// Constant rather than measured from the real volume: a guard about
	// arithmetic must not depend on how full the machine running it happens to
	// be. freeBytes is injected for exactly this reason (see Store.freeBytes).
	free := floor + uint64(maxBytes)

	var started atomic.Int64
	release := make(chan struct{})

	type outcome struct {
		pending *Pending
		err     error
	}
	results := make(chan outcome, callers)

	for range callers {
		go func() {
			// A NEW Store per caller, rooted at the same directory. This is
			// api.Server.uploadStore's shape, and it is what a per-struct
			// counter would fail to bound.
			s, err := New(dir)
			if err != nil {
				results <- outcome{err: err}
				return
			}
			s.freeBytes = func(string) (uint64, error) { return free, nil }
			body := &blockingBody{
				started: &started,
				release: release,
				body:    strings.NewReader("some bytes"),
			}
			p, err := s.Stage(body, "clip.mp4", maxBytes, floor)
			results <- outcome{pending: p, err: err}
		}()
	}

	// Wait until every caller has either RETURNED (it was refused before any
	// read) or is BLOCKED inside the copy, then let the blocked ones finish.
	//
	// Both halves are needed. Counting returns alone would let the test proceed
	// while a caller was still deciding; counting reads alone would hang on the
	// refusals, which never read anything.
	var settled []outcome
	timeout := time.After(20 * time.Second)
	for int64(len(settled))+started.Load() < callers {
		select {
		case r := <-results:
			settled = append(settled, r)
		case <-timeout:
			t.Fatalf("only %d of %d callers reached a decision (%d returned, %d reading). "+
				"Either an admitted upload never started reading, or the reservation "+
				"is being held somewhere it blocks another caller from deciding",
				int64(len(settled))+started.Load(), callers, len(settled), started.Load())
		case <-time.After(5 * time.Millisecond):
		}
	}
	close(release)

	var (
		admitted  int
		noSpace   int
		otherErrs []error
	)
	collect := func(r outcome) {
		switch {
		case r.err == nil:
			admitted++
			if r.pending != nil {
				_ = r.pending.Discard()
			}
		case errors.Is(r.err, ErrNoSpace):
			noSpace++
		default:
			otherErrs = append(otherErrs, r.err)
		}
	}
	for _, r := range settled {
		collect(r)
	}
	for range callers - len(settled) {
		collect(<-results)
	}

	if len(otherErrs) > 0 {
		t.Fatalf("Stage failed for a reason that is not ErrNoSpace, so this run says "+
			"nothing about the space accounting: %v", otherErrs)
	}
	// POSITIVE CONTROL: at least one caller must get through. A guard that
	// refused everybody would satisfy "seven were refused" and would be a
	// broken upload path rather than a fixed one.
	if admitted == 0 {
		t.Fatalf("every one of the %d concurrent uploads was refused. The volume was "+
			"reported as having room for one, so refusing all of them is not the "+
			"guard working -- it is uploads being impossible", callers)
	}
	if admitted != 1 || noSpace != callers-1 {
		t.Errorf("%d of %d concurrent Stages were admitted against free space reported "+
			"as room for exactly ONE (%d refused with ErrNoSpace). Every caller read "+
			"the same number and nothing subtracted what the others had already "+
			"promised to write, so the reserve the database and the recorder depend "+
			"on is spent %d times over.",
			admitted, callers, noSpace, admitted)
	}
}

// TestAFinishedStageGivesItsReservationBack is the half that keeps the fix from
// being a slow leak.
//
// The reservation covers the WRITE, and nothing longer: once Stage returns, the
// bytes are on disk and freeBytes counts them itself. A reservation that was
// never released would be indistinguishable from a fix on the first request and
// would refuse the second, third and every upload after it for the life of the
// process -- which is a worse outage than the bug.
func TestAFinishedStageGivesItsReservationBack(t *testing.T) {
	const (
		maxBytes = int64(1 << 20)
		floor    = uint64(4 << 20)
	)
	dir := t.TempDir()
	free := floor + uint64(maxBytes)

	for i := range 5 {
		s, err := New(dir)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		s.freeBytes = func(string) (uint64, error) { return free, nil }
		p, err := s.Stage(strings.NewReader("some bytes"), "clip.mp4", maxBytes, floor)
		if err != nil {
			t.Fatalf("sequential upload %d was refused with %v. The volume has room for "+
				"one upload at a time and this one is alone, so the previous upload's "+
				"reservation was never given back", i, err)
		}
		if err := p.Discard(); err != nil {
			t.Fatalf("discard %d: %v", i, err)
		}
	}
	inFlightMu.Lock()
	held := inFlight[dir]
	rows := len(inFlight)
	inFlightMu.Unlock()
	if held != 0 {
		t.Errorf("%d bytes are still reserved for %s after every upload finished", held, dir)
	}
	if rows != 0 {
		t.Errorf("the in-flight table kept %d row(s) after every upload finished; a "+
			"process that has written to many directories would accumulate one each",
			rows)
	}
}
