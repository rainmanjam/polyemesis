package db

import (
	"path/filepath"
	"testing"
)

// #11: the broadcast delay is 0 to 15 minutes, in whole seconds. 0 is off and
// must stay valid, or every row written before the column existed becomes
// unsavable.
func TestABroadcastDelayIsAcceptedFromZeroToFifteenMinutesAndRefusedOutsideIt(t *testing.T) {
	for _, ok := range []int{0, 1, 30, MaxDestDelaySeconds} {
		d := validDest()
		d.DelaySeconds = ok
		if err := d.Validate(); err != nil {
			t.Errorf("a %d s delay was refused: %v", ok, err)
		}
	}
	for _, bad := range []int{-1, MaxDestDelaySeconds + 1, 3600} {
		d := validDest()
		d.DelaySeconds = bad
		if err := d.Validate(); err == nil {
			t.Errorf("a %d s delay was accepted", bad)
		}
	}
}

// The delay is stored, read back, and edited like every other column. Create
// and Update are separate statements with separate argument lists, so each is
// checked on its own.
func TestABroadcastDelaySurvivesCreateAndUpdate(t *testing.T) {
	d := keyDB(t, filepath.Join(t.TempDir(), "polyemesis.db"), WithSecretBox(testBox(t)))
	src := &Source{Name: "Main", Enabled: true, Ingest: DefaultSettings().Ingest}
	if err := d.CreateSource(src); err != nil {
		t.Fatal(err)
	}
	row := validDest()
	row.DelaySeconds = 30
	saved, err := d.CreateDestination(row)
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.GetDestination(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DelaySeconds != 30 {
		t.Fatalf("created with a 30 s delay, read back %d", got.DelaySeconds)
	}
	got.DelaySeconds = MaxDestDelaySeconds
	if _, err := d.UpdateDestination(got); err != nil {
		t.Fatal(err)
	}
	again, err := d.GetDestination(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.DelaySeconds != MaxDestDelaySeconds {
		t.Fatalf("updated to %d s, read back %d", MaxDestDelaySeconds, again.DelaySeconds)
	}
}
