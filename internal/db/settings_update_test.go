package db

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

// TestConcurrentSettingsUpdatesDoNotDiscardEachOther is the guard on the whole
// point of UpdateSettings.
//
// The settings are ONE JSON document and PutSettings writes all of it, so two
// callers that read it, change a DIFFERENT field each and write it back do not
// merge: whichever lands second stores a document assembled from a read that
// predates the first one's write, and the first one's field is simply gone. Not
// the field it was competing for -- there is no competition, they touched
// different fields -- but every field the other one changed.
//
// Four places in the running server do exactly this shape of write (the
// engine's scheduled playlist flip, PUT /settings, the annotations mirror and
// PUT /jobs/policy), and until UpdateSettings existed none of them serialised
// against any of the others.
//
// The sleep inside each mutation is what makes the failure deterministic rather
// than a race that shows up once a month on a loaded box: it holds both
// closures inside their read-modify-write span long enough that an
// unsynchronised pair is certain to overlap. With the lock the second closure
// cannot start until the first has stored, so it sees the first's field.
//
// The mutation: delete d.settingsMu.Lock() in UpdateSettings and this fails.
func TestConcurrentSettingsUpdatesDoNotDiscardEachOther(t *testing.T) {
	d := testDB(t)

	// Two fields nothing else here touches, both well inside what Validate
	// accepts, and both different from the defaults so "held its new value" is
	// distinguishable from "was never written".
	const wantInterval, wantHeight = 123, 480

	// The overlap window. Long enough to swamp the scheduling jitter between
	// two goroutines that were released together, short enough that the test
	// still runs in a tenth of a second.
	const inSpan = 50 * time.Millisecond

	mutations := []func(*Settings){
		func(s *Settings) { s.Meters.IntervalMS = wantInterval },
		func(s *Settings) { s.Preview.VideoHeight = wantHeight },
	}

	// Released together, so neither can finish before the other has begun.
	start := make(chan struct{})
	errs := make([]error, len(mutations))
	var wg sync.WaitGroup
	for i, mutate := range mutations {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = d.UpdateSettings(func(s *Settings) error {
				time.Sleep(inSpan)
				mutate(s)
				return nil
			})
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("UpdateSettings %d: %v", i, err)
		}
	}

	got, err := d.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if got.Meters.IntervalMS != wantInterval {
		t.Errorf("meter interval is %d, want %d: a concurrent update to an "+
			"unrelated field discarded it", got.Meters.IntervalMS, wantInterval)
	}
	if got.Preview.VideoHeight != wantHeight {
		t.Errorf("preview height is %d, want %d: a concurrent update to an "+
			"unrelated field discarded it", got.Preview.VideoHeight, wantHeight)
	}
}

// errMutateRefused stands in for whatever a real mutation refuses on -- the
// scheduled playlist flip's "this occurrence cannot be applied", say. What
// matters is that it is a sentinel, so the assertion below is about errors.Is
// rather than about a string.
var errMutateRefused = errors.New("the mutation refused")

// TestAFailedMutationWritesNothing checks the STORE, not just the error.
//
// A mutate that returns an error after it has already edited the document is
// the ordinary case, not a strange one: a closure discovers halfway through
// that it cannot proceed. The document it was handed is a working copy, so an
// abort has to mean the stored blob is untouched -- an UpdateSettings that
// wrote first and reported the error afterwards would be strictly worse than no
// transaction at all, because the caller would be told it failed.
//
// The mutation: make UpdateSettings write even when mutate returns an error and
// this fails.
func TestAFailedMutationWritesNothing(t *testing.T) {
	d := testDB(t)
	before, err := d.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}

	_, err = d.UpdateSettings(func(s *Settings) error {
		// Edited BEFORE the refusal, which is what makes this a test of the
		// write and not of the return value.
		s.Meters.IntervalMS = 321
		return errMutateRefused
	})
	// Unwrapped, so a caller can recognise its own error.
	if !errors.Is(err, errMutateRefused) {
		t.Fatalf("UpdateSettings returned %v, want %v", err, errMutateRefused)
	}

	after, err := d.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		// Only the field the closure touched is named: the whole document is
		// several hundred fields wide and dumping it twice buries the answer.
		t.Errorf("the stored settings changed after a refused mutation "+
			"(meter interval %d, was %d)", after.Meters.IntervalMS, before.Meters.IntervalMS)
	}
}

// TestAnInvalidDocumentIsRefusedAndNotStored is the store-level half of the
// lockout this branch already had to fix once.
//
// PutSettings marshals and inserts; it does not validate. So a caller that
// skipped Settings.Validate could store a document the settings API refuses,
// and every later PUT /settings would then answer 400 for a state the operator
// never wrote. The shipped default is exactly such a document one field away:
// the failover playlist is disabled with no items, and "enabled but no items"
// is what Validate rejects by name.
//
// The typed error matters as much as the refusal: the API answers 400 for an
// invalid document and 500 for a store that failed, and it must not tell them
// apart by matching strings.
func TestAnInvalidDocumentIsRefusedAndNotStored(t *testing.T) {
	d := testDB(t)
	before, err := d.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}

	_, err = d.UpdateSettings(func(s *Settings) error {
		s.Failover.Playlist.Enabled = true
		return nil
	})
	var invalid InvalidSettingsError
	if !errors.As(err, &invalid) {
		t.Fatalf("UpdateSettings returned %v (%T), want an InvalidSettingsError", err, err)
	}
	// Unwrap reaches the validator's own error, which is what the settings
	// endpoint puts in its 400 body.
	if invalid.Unwrap() == nil {
		t.Error("InvalidSettingsError carries no cause, so the operator would be told nothing")
	}

	after, err := d.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Errorf("an invalid document was stored (failover playlist enabled: %v)",
			after.Failover.Playlist.Enabled)
	}
}

// TestErrSettingsUnchangedWritesNothingAndIsNotAnError covers the no-op path.
//
// It is not a nicety. The scheduled playlist flip fires on every occurrence, so
// an overlapping schedule or a restart inside a window arrives at a playlist
// that is already in the wanted state; reporting that as an error would leave
// the occurrence unhandled and retried, and writing anyway would announce a
// change to everything watching the settings for a document that did not
// change.
func TestErrSettingsUnchangedWritesNothingAndIsNotAnError(t *testing.T) {
	d := testDB(t)
	// Something recognisable to read back, so "nothing was written" is a claim
	// about this document rather than about the defaults.
	seed := DefaultSettings()
	seed.Meters.IntervalMS = 200
	if err := d.PutSettings(seed); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}

	// The mutate EDITS the document and then refuses, and that is what makes
	// the no-write half of this test able to fail at all.
	//
	// The first version returned ErrSettingsUnchanged without touching s. A
	// PutSettings on that path would have stored a byte-identical document, so
	// the DeepEqual below could not tell a spurious write from no write -- the
	// exact mutation the test's name promises to catch left it green. Editing
	// first gives the write something to carry, so if the no-op path ever falls
	// through to PutSettings the stored interval comes back as 999.
	got, err := d.UpdateSettings(func(s *Settings) error {
		s.Meters.IntervalMS = 999
		return ErrSettingsUnchanged
	})
	if err != nil {
		t.Fatalf("UpdateSettings reported %v; a no-op is not a failure", err)
	}
	// The caller is handed the current document, because "nothing to do" still
	// has to answer "what is stored" -- the handlers that read fields out of the
	// return value cannot have it come back zeroed.
	if got.Meters.IntervalMS != seed.Meters.IntervalMS {
		t.Errorf("UpdateSettings returned meter interval %d, want the stored %d",
			got.Meters.IntervalMS, seed.Meters.IntervalMS)
	}

	stored, err := d.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if !reflect.DeepEqual(seed, stored) {
		t.Errorf("the stored settings changed on a no-op update (meter interval %d, was %d)",
			stored.Meters.IntervalMS, seed.Meters.IntervalMS)
	}
}
