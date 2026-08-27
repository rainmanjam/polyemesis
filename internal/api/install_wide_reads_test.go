package api

import (
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// This file holds the reads that describe the INSTALL and were answered by one
// programme. They are the other half of the #497 family: scopedEngine is for a
// question about one programme that never said which, and these are questions
// about the whole box that were quietly narrowed to programme 1. Refusing them
// would be wrong -- there is nothing for the caller to name -- so the fix is to
// ask every engine.

// THE ALERT DELIVERY COUNTERS COVER EVERY PROGRAMME'S NOTIFIER (#574).
//
// Alert RULES are install-wide: one alert_rules table, read by every engine's
// notifier. The COUNTERS are not -- each notifier keeps its own -- so the rule
// editor's "sent / failed / coalesced" panel showed roughly one programme's
// share of the truth on a two-programme install, with nothing on the panel
// saying it was a share. Summing is the only reading that matches what the
// panel claims to describe.
//
// Mutation: return s.eng().Alerts().Stats() from alertStats. Observed to fail
// with "queued = 0 ... the panel is reporting one programme's share".
func TestAlertCountersCoverEveryProgrammesNotifier(t *testing.T) {
	s, h, _, sign := managerServer(t, defaultTools())
	first := s.eng()
	if first == nil {
		t.Fatal("no default engine in the fixture, so there is no wrong notifier to be " +
			"read and this test would pass having observed nothing")
	}
	second := secondProgramme(t, s)

	eng := s.engineForSource(&second.ID)
	if eng == nil {
		t.Fatalf("no engine for source %d, which is running", second.ID)
	}

	// A DELTA, not an absolute. Engines publish their own events as they come
	// up, so the default notifier's counters are already moving when this test
	// starts and an assertion on `queued > 0` would pass against a handler that
	// never looked at programme 2 at all. Measured: it does.
	before := metaStats(t, h, sign)
	ownBefore := eng.Alerts().Stats().Queued

	// Through the SECOND programme's notifier only. Publishing through the
	// default one would be counted either way and would prove nothing.
	const published = 3
	for i := 0; i < published; i++ {
		eng.Alerts().Publish(alerts.Event{
			Type: alerts.TypeIngestLost, Title: "studio b", Key: itoa(int64(i)),
		})
	}
	if got := eng.Alerts().Stats().Queued - ownBefore; got < published {
		// Not the assertion -- the precondition. If the second notifier did not
		// accept them, the endpoint has nothing to fail to report.
		t.Fatalf("programme %d's own notifier queued %d of %d events, so there is nothing "+
			"for the endpoint to be missing", second.ID, got, published)
	}

	after := metaStats(t, h, sign)
	if after.Queued-before.Queued < published {
		t.Errorf("GET /alerts/meta reports queued moving by %d after %d events were "+
			"published through programme %d's notifier. The panel presents these as the "+
			"install's delivery counters and is reporting one programme's share of them, "+
			"with nothing on the panel saying so",
			after.Queued-before.Queued, published, second.ID)
	}
}

// metaStats is the delivery counters as GET /alerts/meta reports them.
func metaStats(t *testing.T, h http.Handler, sign func(*http.Request)) alerts.Stats {
	t.Helper()
	var meta struct {
		Stats alerts.Stats `json:"stats"`
	}
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/alerts/meta", nil, http.StatusOK), &meta)
	return meta.Stats
}

// A RECORDER HALTED ON ANOTHER PROGRAMME IS REPORTED (#579).
//
// The free-space FLOOR is install-wide -- one volume, one limit -- and reading
// it off the default engine is fine. The HALT is not: it is one recorder child
// stopped by the guard on THAT engine's own recording manager. On a
// two-programme install where programme 2 is recording and programme 1 is not,
// the endpoint whose entire job is to explain a recorder that stopped on its
// own reported the default engine's zero verdict and said nothing had.
//
// The floor is one no volume can clear, which is how this reaches a full disk
// without one -- the same driver TestRecordingUsageReportsARecorderTheFreeSpaceFloorHalted
// next door uses, applied to ONE engine instead of all of them.
//
// Mutation: read e := s.mgr.Default() in storageVerdict. Observed to fail with
// "recording has halted on programme 2 and GET /recordings/usage reports
// halted=false".
func TestAHaltedRecorderOnAnyProgrammeIsReported(t *testing.T) {
	s, h, _, sign := managerServer(t, defaultTools())
	first := s.eng()
	if first == nil {
		t.Fatal("no default engine in the fixture, so there is no zero verdict to be " +
			"wrongly preferred")
	}
	second := secondProgramme(t, s)

	eng := s.engineForSource(&second.ID)
	if eng == nil {
		t.Fatalf("no engine for source %d, which is running", second.ID)
	}
	// THE DIRECTORY HAS TO EXIST FIRST, and this is why the test was flaky.
	//
	// CheckFreeSpace measures m.dir, and when that stat FAILS it logs a warning
	// and returns WITHOUT halting -- a refusal shaped exactly like "there is
	// plenty of room". A second programme's engine has not necessarily created
	// its recordings directory by the time this runs, so the halt silently did
	// not happen and the guard below reported the state as unreached. It passed
	// on a re-run and in isolation, which is what kept it looking like noise.
	if err := os.MkdirAll(eng.Recordings().Dir(), 0o755); err != nil {
		t.Fatalf("create programme %d's recordings dir: %v", second.ID, err)
	}
	halt := db.RecordingSettings{Enabled: true, SegmentSeconds: 3600, MinFreeGB: 1e9}

	// SET IT UNTIL IT HOLDS, and the reason is a sweep this test is racing.
	//
	// The engine's recording manager runs ScanAndSweep once the moment it
	// starts, and every 30s after. That sweep ends in CheckFreeSpace with the
	// SOURCE's own settings -- a 5 GB floor, so a 6.25 GB resume threshold --
	// sees a runner with far more than that, declares "free space recovered"
	// and clears the halt this test just set. Whether that happens depends
	// entirely on which side of the engine's startup sweep the line above
	// falls on: locally it lands after and the halt sticks, on a loaded runner
	// it lands before and the state under test is gone by the assertion.
	//
	// Giving the second programme a floor of its own would remove the race at
	// the source, and settings are install-wide, so it would halt the default
	// engine too -- destroying the discriminator below. Re-applying until the
	// halt survives a second look is the honest alternative: it converges as
	// soon as the startup sweep is behind us, and the guard still fails loudly
	// if the state genuinely cannot be reached.
	haltHolds := func() bool {
		eng.Recordings().CheckFreeSpace(halt)
		if !eng.Recordings().Storage().Halted {
			return false
		}
		time.Sleep(50 * time.Millisecond)
		return eng.Recordings().Storage().Halted
	}
	deadline := time.Now().Add(15 * time.Second)
	for !haltHolds() {
		if time.Now().After(deadline) {
			t.Fatalf("source %d's recording manager would not stay halted on a %.0f GB "+
				"floor, so the state under test was never reached", second.ID, halt.MinFreeGB)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// THE DISCRIMINATOR. With the default engine unhalted, an endpoint that
	// asks it answers "fine" while a recorder is stopped.
	if first.Recordings().Storage().Halted {
		t.Fatal("the DEFAULT programme halted as well, so asking either engine gives the " +
			"same answer and this test cannot tell them apart")
	}

	var usage map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/recordings/usage", nil, http.StatusOK), &usage)

	storage, ok := usage["storage"].(map[string]any)
	if !ok {
		t.Fatalf("GET /recordings/usage carried no storage block at all: %v", usage)
	}
	if storage["halted"] != true {
		t.Errorf("recording has halted on programme %d and GET /recordings/usage reports "+
			"halted=%v. The one page whose job is to explain a recorder that stopped on "+
			"its own says nothing stopped", second.ID, storage["halted"])
	}
	if storage["reason"] == "" || storage["reason"] == nil {
		t.Error("the halt is reported with no reason, so the banner has nothing to say")
	}
}

// GET /system SAYS WHICH PROGRAMME ITS INGEST URL CONFIGURES (#551).
//
// The URL carries an SRT passphrase and is built from ONE source's ingest
// block. An operator who copied it to set up Studio B's encoder was pointing
// that encoder at Main -- publishing a second feed into programme 1, or being
// rejected by a passphrase -- and nothing in the response said which programme
// it was for.
//
// Two assertions, and both are needed. The label is what makes the default a
// stated choice rather than a silent one; ?source= is what lets an operator get
// the OTHER programme's URL at all, which no amount of labelling would give
// them.
//
// Mutation: drop the ingestSourceId/ingestSourceName keys, or ignore `named` in
// handleSystem. Observed to fail on the first and second assertion in turn.
func TestSystemNamesTheProgrammeItsIngestURLConfigures(t *testing.T) {
	s, h, _, sign := managerServer(t, defaultTools())
	first := s.eng()
	if first == nil {
		t.Fatal("no default engine in the fixture, so /system has no programme to speak for")
	}
	second := secondProgramme(t, s)

	// A passphrase of its own, so the two programmes' URLs are distinguishable
	// on the wire. Without it this test would pass against a handler that
	// ignored the parameter entirely.
	row, err := s.store.GetSource(second.ID)
	if err != nil {
		t.Fatalf("read back the second source: %v", err)
	}
	row.Ingest.Mode = db.IngestSRT
	row.Ingest.SRT.Passphrase = "studio-b-passphrase"
	if err := s.store.UpdateSource(row); err != nil {
		t.Fatalf("give the second source its own passphrase: %v", err)
	}

	var unscoped map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/system", nil, http.StatusOK), &unscoped)
	if unscoped["ingestSourceId"] == nil {
		t.Fatalf("GET /system names no programme for its ingestUrl (%v). An operator "+
			"copying that URL cannot tell which encoder it configures", unscoped["ingestUrl"])
	}
	if got := int64(unscoped["ingestSourceId"].(float64)); got != first.SourceID() {
		t.Errorf("ingestSourceId = %d, want the default programme %d", got, first.SourceID())
	}

	var scoped map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet,
		"/api/v1/system?source="+itoa(second.ID), nil, http.StatusOK), &scoped)
	if got := int64(scoped["ingestSourceId"].(float64)); got != second.ID {
		t.Fatalf("ingestSourceId = %d for ?source=%d; the parameter was ignored and the "+
			"URL below is the default programme's", got, second.ID)
	}
	if scoped["ingestUrl"] == unscoped["ingestUrl"] {
		t.Errorf("?source=%d returned the SAME ingestUrl as the default programme (%v), "+
			"though the two sources carry different passphrases -- so the label says one "+
			"programme and the URL configures another, which is worse than no label",
			second.ID, scoped["ingestUrl"])
	}

	// A programme that does not exist is refused rather than quietly served the
	// default's, which is the fallback this whole family is about.
	send(t, h, sign, http.MethodGet, "/api/v1/system?source=2147483647", nil, http.StatusBadRequest)
}
