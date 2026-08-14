package db

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/routing"
)

// vodProfile is a second mix that is meaningfully DIFFERENT from the live one,
// so a round trip that quietly substituted the live profile would be visible.
func vodProfile() routing.Profile {
	p := routing.DefaultProfile()
	p.Tracks = []routing.TrackSel{{Track: 1, Enabled: true, Gain: 0.5}}
	p.ExcludeRoles = []routing.TrackRole{routing.RoleMusic}
	p.DelayMS = 120
	return p
}

// TestADestinationRoundTripsItsSecondVODMix is the storage half of the VOD
// track: the operator's second mix has to survive a write and a read exactly,
// including the fields most likely to be dropped by a partial implementation.
//
// The gain of 0.5, the excluded role and the delay are all deliberate. A
// round trip that stored only the track list would pass a test that checked
// only the track list, and ExcludeRoles in particular is the DMCA switch --
// silently losing it on the VOD mix is precisely the archive-carries-the-music
// failure the role exists to prevent.
//
// MUTATION: `vod_profile` dropped from destUpdateCols (the UPDATE column list)
// and from its argument. Observed: FAIL, "second mix after update: track 0 =
// {Track:1 Enabled:true Gain:0.5}, want {Track:1 Enabled:true Gain:0.25}" and
// the same again on re-read -- the update silently kept the old value.
// Restored from /tmp backup; `git diff --stat` clean.
// MUTATION: marshalVODProfile returns `"{}"` instead of `""` for nil. Observed:
// FAIL in TestADestinationWithNoVODMixStoresNoVODMix (below), not here.
// Restored from /tmp backup; `git diff --stat` clean.
func TestADestinationRoundTripsItsSecondVODMix(t *testing.T) {
	d := testDB(t)

	in := validDest()
	in.Multitrack = true
	want := vodProfile()
	in.VODProfile = &want

	created, err := d.CreateDestination(in)
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	if !created.Multitrack {
		t.Error("multitrack was not stored")
	}
	if created.VODProfile == nil {
		t.Fatal("the second (VOD) mix was not stored at all")
	}
	assertProfileEqual(t, "second mix after create", *created.VODProfile, want)

	// It must survive an UPDATE too, which is a different column list and the
	// one most likely to be missed.
	updated := *created
	changed := vodProfile()
	changed.Tracks[0].Gain = 0.25
	updated.VODProfile = &changed
	got, err := d.UpdateDestination(&updated)
	if err != nil {
		t.Fatalf("UpdateDestination: %v", err)
	}
	if got.VODProfile == nil {
		t.Fatal("the second (VOD) mix was lost by the update")
	}
	assertProfileEqual(t, "second mix after update", *got.VODProfile, changed)

	// And a re-read from the database, not just the value the writer returned.
	reread, err := d.GetDestination(got.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if reread.VODProfile == nil {
		t.Fatal("the second (VOD) mix did not survive a re-read")
	}
	assertProfileEqual(t, "second mix on re-read", *reread.VODProfile, changed)
	if !reread.Multitrack {
		t.Error("multitrack did not survive a re-read")
	}
}

// TestADestinationWithNoVODMixStoresNoVODMix is the compatibility half, and it
// is the one that matters to every existing install: nearly every destination
// has no second mix, and "no second mix" has to read back as NIL rather than as
// a profile that happens to be empty.
//
// The distinction is not cosmetic. The zero routing.Profile fails Validate --
// no track enabled, no normalize mode, no sample rate -- so if absence decoded
// to a zero profile instead of nil, every row written before this column
// existed would come back carrying a second audio track that cannot compile.
// routing.CompilePair would then warn about a VOD mix the operator never asked
// for, on every destination in the install.
//
// MUTATION: marshalVODProfile returns `"{}"` for nil instead of `""`. Observed:
// FAIL, "a destination with no second mix came back with one". Restored from
// /tmp backup; `git diff --stat` clean.
// MUTATION: scanDestination's `if vodProfileRaw != ""` changed to an
// unconditional decode. Observed: FAIL, "decode second (VOD) audio profile:
// unexpected end of JSON input". Restored from /tmp backup; clean.
func TestADestinationWithNoVODMixStoresNoVODMix(t *testing.T) {
	d := testDB(t)

	created, err := d.CreateDestination(validDest())
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}
	if created.VODProfile != nil {
		t.Errorf("a destination with no second mix came back with one: %+v", *created.VODProfile)
	}
	if created.Multitrack {
		t.Error("multitrack defaulted to on; it must be opt-in")
	}

	reread, err := d.GetDestination(created.ID)
	if err != nil {
		t.Fatalf("GetDestination: %v", err)
	}
	if reread.VODProfile != nil {
		t.Errorf("a re-read invented a second mix: %+v", *reread.VODProfile)
	}

	// Turning it on and then off again must leave nothing behind. A clear that
	// wrote "{}" would read back as a broken second track rather than as none.
	on := *reread
	p := vodProfile()
	on.VODProfile = &p
	on.Multitrack = true
	if _, err := d.UpdateDestination(&on); err != nil {
		t.Fatalf("UpdateDestination (on): %v", err)
	}
	off := *reread
	off.VODProfile = nil
	off.Multitrack = false
	cleared, err := d.UpdateDestination(&off)
	if err != nil {
		t.Fatalf("UpdateDestination (off): %v", err)
	}
	if cleared.VODProfile != nil {
		t.Errorf("clearing the second mix left one behind: %+v", *cleared.VODProfile)
	}
	if cleared.Multitrack {
		t.Error("clearing multitrack left it on")
	}
}

// assertProfileEqual compares the fields a second mix can actually differ in.
// Written out rather than reflect.DeepEqual so that a failure names WHICH
// setting was lost, which is the thing a person reading the failure needs.
func assertProfileEqual(t *testing.T, what string, got, want routing.Profile) {
	t.Helper()
	if len(got.Tracks) != len(want.Tracks) {
		t.Fatalf("%s: got %d tracks, want %d", what, len(got.Tracks), len(want.Tracks))
	}
	for i := range want.Tracks {
		if got.Tracks[i] != want.Tracks[i] {
			t.Errorf("%s: track %d = %+v, want %+v", what, i, got.Tracks[i], want.Tracks[i])
		}
	}
	if got.DelayMS != want.DelayMS {
		t.Errorf("%s: delayMs = %d, want %d", what, got.DelayMS, want.DelayMS)
	}
	if got.Normalize != want.Normalize {
		t.Errorf("%s: normalize = %q, want %q", what, got.Normalize, want.Normalize)
	}
	if got.SampleRate != want.SampleRate {
		t.Errorf("%s: sampleRate = %d, want %d", what, got.SampleRate, want.SampleRate)
	}
	if len(got.ExcludeRoles) != len(want.ExcludeRoles) {
		t.Fatalf("%s: got %d excluded roles, want %d -- losing this is the "+
			"archive-carries-the-music failure", what, len(got.ExcludeRoles), len(want.ExcludeRoles))
	}
	for i := range want.ExcludeRoles {
		if got.ExcludeRoles[i] != want.ExcludeRoles[i] {
			t.Errorf("%s: excluded role %d = %q, want %q", what, i, got.ExcludeRoles[i], want.ExcludeRoles[i])
		}
	}
}
