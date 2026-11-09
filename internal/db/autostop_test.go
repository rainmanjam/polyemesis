package db

import (
	"encoding/json"
	"strings"
	"testing"
)

func autoStopIngest(a AutoStopSettings) IngestSettings {
	in := DefaultSettings().Ingest
	in.Mode = IngestSRT
	in.AutoStop = a
	return in
}

// A SAVE THAT TURNS THE FALLBACK ON WITHOUT THE CONSENT IS REFUSED, BY THE
// VALIDATOR EVERY SAVE SHARES (source create/update and settings).
//
// Mutation: drop the Enabled && !Acknowledged branch of problems(). Observed
// to fail with "enabling without the acknowledgement was accepted".
func TestAutoStopWithoutTheAcknowledgementIsRefused(t *testing.T) {
	probs := autoStopIngest(AutoStopSettings{Enabled: true, WaitSeconds: 120}).problems()
	if len(probs) != 1 || !strings.Contains(probs[0], "permanently") {
		t.Fatalf("enabling without the acknowledgement was accepted or mis-worded: %v", probs)
	}
	if p := autoStopIngest(AutoStopSettings{Enabled: true, WaitSeconds: 120, Acknowledged: true}).problems(); len(p) != 0 {
		t.Fatalf("an acknowledged, in-range setting was refused: %v", p)
	}
	// Off is allowed with no consent and with a 0 wait: it is the default.
	if p := autoStopIngest(AutoStopSettings{}).problems(); len(p) != 0 {
		t.Fatalf("the zero value (off) was refused: %v", p)
	}
}

// The wait is bounded on both sides, and the floor is above the engine's own
// loss timers.
func TestAutoStopWaitBounds(t *testing.T) {
	for _, tc := range []struct {
		wait int
		ok   bool
	}{
		{0, true}, {MinAutoStopWaitSeconds - 1, false}, {MinAutoStopWaitSeconds, true},
		{DefaultAutoStopWaitSeconds, true}, {MaxAutoStopWaitSeconds, true}, {MaxAutoStopWaitSeconds + 1, false}, {-5, false},
	} {
		p := autoStopIngest(AutoStopSettings{Enabled: true, Acknowledged: true, WaitSeconds: tc.wait}).problems()
		if (len(p) == 0) != tc.ok {
			t.Errorf("wait %d: problems=%v, want ok=%v", tc.wait, p, tc.ok)
		}
	}
	// The floor must clear: alerts' 20s dwell, the hooks 5s, supervisor stall 5s,
	// the default failover grace 5s, and the 30s ingest respawn backoff.
	if MinAutoStopWaitSeconds <= 30 {
		t.Errorf("floor %ds does not clear the 30s ingest respawn backoff", MinAutoStopWaitSeconds)
	}
	if MinAutoStopWaitSeconds > DefaultAutoStopWaitSeconds || DefaultAutoStopWaitSeconds > MaxAutoStopWaitSeconds {
		t.Errorf("floor/default/ceiling are not ordered: %d/%d/%d",
			MinAutoStopWaitSeconds, DefaultAutoStopWaitSeconds, MaxAutoStopWaitSeconds)
	}
}

// The engine never acts inside the selector's own decision window: a failover
// grace raised after the wait was saved pushes the effective wait up.
//
// Mutation: return w without the failover clause. Observed to fail with
// "effective wait 120, want 330".
func TestAutoStopEffectiveWaitClearsTheFailoverGrace(t *testing.T) {
	a := AutoStopSettings{Enabled: true, Acknowledged: true}
	if got := a.EffectiveWait(0); got != DefaultAutoStopWaitSeconds {
		t.Errorf("default wait is %d, want %d", got, DefaultAutoStopWaitSeconds)
	}
	if got := a.EffectiveWait(MaxFailoverGraceSeconds); got != MaxFailoverGraceSeconds+AutoStopFailoverMarginSeconds {
		t.Errorf("effective wait %d, want %d", got, MaxFailoverGraceSeconds+AutoStopFailoverMarginSeconds)
	}
	if got := (AutoStopSettings{WaitSeconds: 3600}).EffectiveWait(300); got != 3600 {
		t.Errorf("a long wait was shortened to %d", got)
	}
	// A hand-edited row below the floor is lifted, not obeyed.
	if got := (AutoStopSettings{WaitSeconds: 1}).EffectiveWait(0); got != MinAutoStopWaitSeconds {
		t.Errorf("a wait of 1s became %d, want the %ds floor", got, MinAutoStopWaitSeconds)
	}
}

// OFF BY DEFAULT, additive on the wire, and absent from an older row.
func TestAutoStopIsOffByDefaultAndRoundTrips(t *testing.T) {
	if DefaultSettings().Ingest.AutoStop.Armed() {
		t.Fatal("auto-stop is armed in the default settings")
	}
	var old IngestSettings
	if err := json.Unmarshal([]byte(`{"mode":"srt"}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.AutoStop.Armed() || old.AutoStop.Wait() != DefaultAutoStopWaitSeconds {
		t.Fatalf("an older row decoded to %+v", old.AutoStop)
	}

	d := testDB(t)
	src := &Source{Name: "Backpack", Enabled: true, Ingest: autoStopIngest(AutoStopSettings{Enabled: true, WaitSeconds: 300, Acknowledged: true})}
	if err := d.CreateSource(src); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	got, err := d.GetSource(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := (AutoStopSettings{Enabled: true, WaitSeconds: 300, Acknowledged: true}); got.Ingest.AutoStop != want {
		t.Fatalf("stored %+v, want %+v", got.Ingest.AutoStop, want)
	}
	// Enabling without the consent never reaches the row.
	bad := &Source{Name: "Bad", Enabled: true, Ingest: autoStopIngest(AutoStopSettings{Enabled: true})}
	if err := d.CreateSource(bad); err == nil {
		t.Fatal("a source enabling auto-stop without the acknowledgement was stored")
	}
}
