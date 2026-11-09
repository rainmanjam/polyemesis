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
	if got := a.EffectiveWait(0, 0); got != DefaultAutoStopWaitSeconds {
		t.Errorf("default wait is %d, want %d", got, DefaultAutoStopWaitSeconds)
	}
	if got := a.EffectiveWait(MaxFailoverGraceSeconds, 0); got != MaxFailoverGraceSeconds+AutoStopFailoverMarginSeconds {
		t.Errorf("effective wait %d, want %d", got, MaxFailoverGraceSeconds+AutoStopFailoverMarginSeconds)
	}
	if got := (AutoStopSettings{WaitSeconds: 3600}).EffectiveWait(300, 0); got != 3600 {
		t.Errorf("a long wait was shortened to %d", got)
	}
	// A hand-edited row below the floor is lifted, not obeyed.
	if got := (AutoStopSettings{WaitSeconds: 1}).EffectiveWait(0, 0); got != MinAutoStopWaitSeconds {
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

// A pull source may legitimately wait up to its reconnect delay between
// attempts, so the 60s floor (twice the LISTENER respawn backoff) is not enough
// for it: the wait clears the reconnect delay plus the margin.
//
// Mutation: drop pullReconnectSeconds from the loop in EffectiveWait. Observed
// to fail with "effective wait 120, want 3630".
func TestAutoStopEffectiveWaitClearsAPullSourcesReconnectDelay(t *testing.T) {
	a := AutoStopSettings{Enabled: true, Acknowledged: true}
	if got := a.EffectiveWait(0, 3600); got != 3600+AutoStopFailoverMarginSeconds {
		t.Errorf("effective wait %d, want %d", got, 3600+AutoStopFailoverMarginSeconds)
	}
	if got := a.EffectiveWait(0, 30); got != DefaultAutoStopWaitSeconds {
		t.Errorf("the default pull delay lifted the default wait to %d", got)
	}
	if got := a.EffectiveWait(300, 600); got != 600+AutoStopFailoverMarginSeconds {
		t.Errorf("both clearances: %d, want the larger, %d", got, 600+AutoStopFailoverMarginSeconds)
	}
}

// The acknowledgement belongs to the enabled setting it was given for: any
// write that leaves the fallback off stores it withdrawn, on every store path,
// so no merge can later turn it on by saying nothing else.
//
// Read back through GetSource this test sees the source path through scanSource
// as well, which normalizes too, so it does not isolate validateSource: dropping
// normalized() from validateSource ALONE survives it. The layers are tested one
// by one below (TestValidateSourceNormalizesAutoStop, TestScanSourceNormalizesAutoStop,
// TestUpdateSettingsStoresTheAcknowledgementWithdrawn); this one is the end to end.
//
// Mutation: drop normalized() from validateSource AND scanSource at once, or
// from UpdateSettings (settings path). Observed to fail with "the
// acknowledgement outlived the setting".
func TestSwitchingAutoStopOffWithdrawsTheAcknowledgement(t *testing.T) {
	d := testDB(t)
	src := &Source{Name: "p", Enabled: true, Ingest: DefaultSettings().Ingest}
	src.Ingest.Mode = IngestSRT
	src.Ingest.AutoStop = AutoStopSettings{Enabled: true, Acknowledged: true}
	if err := d.CreateSource(src); err != nil {
		t.Fatal(err)
	}
	src.Ingest.AutoStop.Enabled = false // the ack is left true, as a stale merge would
	if err := d.UpdateSource(src); err != nil {
		t.Fatal(err)
	}
	got, err := d.GetSource(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ingest.AutoStop.Acknowledged {
		t.Error("the acknowledgement outlived the setting (source)")
	}
	st, err := d.UpdateSettings(func(s *Settings) error {
		s.Ingest.AutoStop = AutoStopSettings{Enabled: false, Acknowledged: true}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.Ingest.AutoStop.Acknowledged {
		t.Error("the acknowledgement outlived the setting (settings)")
	}
}

// LAYER 1, WHAT IS WRITTEN: validateSource withdraws the acknowledgement of a
// setting that is off, and the row the store writes says so. Read from the
// stored ingest JSON, not through GetSource (whose scanSource would hide a
// missing normalization here).
//
// Mutation: drop the normalized() call from validateSource. Observed to fail
// with "validateSource left the acknowledgement" and "the stored ingest JSON
// still carries the acknowledgement".
func TestValidateSourceNormalizesAutoStop(t *testing.T) {
	src := &Source{Name: "p", Enabled: true, Ingest: autoStopIngest(AutoStopSettings{Enabled: false, Acknowledged: true})}
	if err := validateSource(src); err != nil {
		t.Fatal(err)
	}
	if src.Ingest.AutoStop.Acknowledged {
		t.Error("validateSource left the acknowledgement on a setting that is off")
	}

	d := testDB(t)
	row := &Source{Name: "q", Enabled: true, Ingest: autoStopIngest(AutoStopSettings{Enabled: true, Acknowledged: true})}
	if err := d.CreateSource(row); err != nil {
		t.Fatal(err)
	}
	row.Ingest.AutoStop.Enabled = false // acknowledged left true, as a stale merge would
	if err := d.UpdateSource(row); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := d.sql.QueryRow(`SELECT ingest FROM sources WHERE id = ?`, row.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var stored struct {
		AutoStop AutoStopSettings `json:"autoStop"`
	}
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.AutoStop.Acknowledged {
		t.Errorf("the stored ingest JSON still carries the acknowledgement: %s", raw)
	}
}

// LAYER 2, WHAT IS READ: a row already stored with the acknowledgement left on
// a setting that is off (written by an older build, or by hand) reads back
// withdrawn. The row is planted in SQL so only scanSource is in play.
//
// Mutation: drop the normalized() call from scanSource. Observed to fail with
// "a stored row's acknowledgement survived the read".
func TestScanSourceNormalizesAutoStop(t *testing.T) {
	d := testDB(t)
	src := &Source{Name: "p", Enabled: true, Ingest: autoStopIngest(AutoStopSettings{})}
	if err := d.CreateSource(src); err != nil {
		t.Fatal(err)
	}
	planted := src.Ingest
	planted.AutoStop = AutoStopSettings{Enabled: false, WaitSeconds: 300, Acknowledged: true}
	raw, err := json.Marshal(planted)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.sql.Exec(`UPDATE sources SET ingest = ? WHERE id = ?`, string(raw), src.ID); err != nil {
		t.Fatal(err)
	}
	got, err := d.GetSource(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ingest.AutoStop.Acknowledged {
		t.Error("a stored row's acknowledgement survived the read")
	}
	if got.Ingest.AutoStop.WaitSeconds != 300 {
		t.Errorf("normalizing lost the wait: %+v", got.Ingest.AutoStop)
	}
}

// LAYER 3, THE SETTINGS DOOR: UpdateSettings stores the withdrawn shape. Read
// from the stored JSON, since GetSettings does not normalize.
//
// Mutation: drop the normalized() call from UpdateSettings. Observed to fail
// with "the stored settings JSON still carries the acknowledgement".
func TestUpdateSettingsStoresTheAcknowledgementWithdrawn(t *testing.T) {
	d := testDB(t)
	if _, err := d.UpdateSettings(func(s *Settings) error {
		s.Ingest.AutoStop = AutoStopSettings{Enabled: false, Acknowledged: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := d.sql.QueryRow(`SELECT json FROM settings WHERE id = 1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Ingest struct {
			AutoStop AutoStopSettings `json:"autoStop"`
		} `json:"ingest"`
	}
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Ingest.AutoStop.Acknowledged {
		t.Errorf("the stored settings JSON still carries the acknowledgement: %s", raw)
	}
}

// The effective-wait reason is the same rule as the wait: what raised it, and
// nothing when only the floor or the saved value applies.
//
// Mutation: report the failover reason for a pull-raised wait. Observed to fail.
func TestAutoStopEffectiveWaitWhy(t *testing.T) {
	a := AutoStopSettings{Enabled: true, Acknowledged: true}
	for _, c := range []struct {
		name              string
		as                AutoStopSettings
		grace, pull, want int
		wantBy            string
	}{
		{"saved wait stands", a, 5, 30, DefaultAutoStopWaitSeconds, ""},
		{"floor alone is not a reason", AutoStopSettings{Enabled: true, Acknowledged: true, WaitSeconds: 60}, 0, 0, 60, ""},
		{"failover grace", a, 300, 0, 330, AutoStopRaisedByFailover},
		{"pull reconnect", a, 0, 3600, 3630, AutoStopRaisedByPull},
		{"the larger of the two", a, 300, 600, 630, AutoStopRaisedByPull},
		{"the larger, other way", a, 900, 600, 930, AutoStopRaisedByFailover},
	} {
		got, by := c.as.EffectiveWaitWhy(c.grace, c.pull)
		if got != c.want || by != c.wantBy {
			t.Errorf("%s: got %d %q, want %d %q", c.name, got, by, c.want, c.wantBy)
		}
		if got != c.as.EffectiveWait(c.grace, c.pull) {
			t.Errorf("%s: EffectiveWait disagrees with EffectiveWaitWhy", c.name)
		}
	}
}
