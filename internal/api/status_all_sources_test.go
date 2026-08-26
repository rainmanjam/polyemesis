package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/engine"
)

// The status payload describes the BOX, so its destination list must carry
// every programme's destinations -- not the default engine's.
//
// Engine.Status is scoped to its own source on purpose (#515): it compiles each
// destination against the layout of the programme that owns it, so a 2-track
// programme's destination is never described using a 6-track one's count.
// Before that fix the default engine's status happened to carry every row on
// the machine, and three callers were relying on the leak: the dashboard's
// grouped destination list, the Prometheus scrape, and the WebSocket push.
//
// The metrics case is the one that justifies a test rather than a comment. A
// scrape covering only the default programme does not fail -- the series for
// every other destination simply stops existing, which looks exactly like a
// destination nobody configured, so an alert that should fire on a dead
// destination never evaluates at all.
//
// Mutation: return s.eng().Status() from statusPayload without replacing
// Destinations. Observed to fail with "destination on programme 2 is missing".
func TestStatusPayloadCarriesEveryProgrammesDestinations(t *testing.T) {
	s, _, _, _ := managerServer(t, defaultTools())
	store := s.store

	mine, err := store.CreateDestination(&db.Destination{
		Name: "on the default programme", Kind: db.DestFile, URL: "a.mkv",
		Enabled: false, AudioBitrate: 160,
	})
	if err != nil {
		t.Fatalf("CreateDestination(default): %v", err)
	}

	other := &db.Source{Name: "second programme", Enabled: true, Ingest: db.DefaultSettings().Ingest}
	if err := store.CreateSource(other); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	if err := s.mgr.Sync(); err != nil {
		t.Fatalf("sync after creating a second source: %v", err)
	}
	if got := len(s.mgr.Engines()); got < 2 {
		t.Fatalf("the manager runs %d engine(s); this test needs two or it asserts nothing", got)
	}
	foreign, err := store.CreateDestination(&db.Destination{
		Name: "on the second programme", Kind: db.DestFile, URL: "b.mkv",
		Enabled: false, AudioBitrate: 160, SourceID: &other.ID,
	})
	if err != nil {
		t.Fatalf("CreateDestination(second): %v", err)
	}

	seen := map[int64]bool{}
	for _, d := range s.statusPayload(s.eng()).Destinations {
		seen[d.ID] = true
	}
	if !seen[foreign.ID] {
		t.Error("destination on programme 2 is missing from the status payload. " +
			"The dashboard shows that programme as empty and Prometheus stops " +
			"emitting a series for it, which reads as a destination nobody made.")
	}
	// The control: without it, a payload that returned every destination in the
	// database regardless of engine would pass, and so would an empty one.
	if !seen[mine.ID] {
		t.Fatal("the default programme's own destination is missing, so the assertion above proves nothing")
	}
}

// And the Prometheus exposition must carry them too, which is the half of this
// that actually hurts.
//
// A wrong dashboard is visible: the operator sees a programme with no
// destinations under it and asks why. A scrape that quietly covers one source
// looks like nothing at all. The series for every destination on every other
// programme simply stops existing, and a series that does not exist is
// indistinguishable from a destination nobody ever configured -- so an alerting
// rule written to fire when a destination dies never evaluates, and the silence
// reads as health.
//
// Asserted through the real exposition rather than through the snapshot struct,
// because the snapshot is built from st.Destinations and a test of the builder
// would pass while /metrics went on scraping one programme.
//
// Mutation: return s.eng().Status() from statusPayload and drop the
// st.Destinations assignment in handleMetrics. Observed to fail with
// "the exposition names 1 of 2 destinations".
func TestTheScrapeCarriesEveryProgrammesDestinations(t *testing.T) {
	s, h, _, auth := managerServer(t, defaultTools())

	if _, err := s.store.CreateDestination(&db.Destination{
		Name: "default-programme-dest", Kind: db.DestFile, URL: "a.mkv",
		Enabled: false, AudioBitrate: 160,
	}); err != nil {
		t.Fatalf("CreateDestination(default): %v", err)
	}

	other := &db.Source{Name: "second programme", Enabled: true, Ingest: db.DefaultSettings().Ingest}
	if err := s.store.CreateSource(other); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	if err := s.mgr.Sync(); err != nil {
		t.Fatalf("sync after creating a second source: %v", err)
	}
	if got := len(s.mgr.Engines()); got < 2 {
		t.Fatalf("the manager runs %d engine(s); this test needs two or it asserts nothing", got)
	}
	if _, err := s.store.CreateDestination(&db.Destination{
		Name: "second-programme-dest", Kind: db.DestFile, URL: "b.mkv",
		Enabled: false, AudioBitrate: 160, SourceID: &other.ID,
	}); err != nil {
		t.Fatalf("CreateDestination(second): %v", err)
	}

	r := jsonRequest(t, http.MethodGet, "/api/v1/metrics", nil)
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/metrics: %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	found := 0
	for _, name := range []string{"default-programme-dest", "second-programme-dest"} {
		if strings.Contains(body, name) {
			found++
		}
	}
	if found != 2 {
		t.Errorf("the exposition names %d of 2 destinations. A destination whose "+
			"series is absent cannot be alerted on, and reads as one nobody "+
			"configured rather than as a gap in the scrape", found)
	}
}

// AND THE INGEST SERIES, WHICH IS THE UPSTREAM CAUSE OF EVERY ONE OF THEM
// GOING DOWN (#528).
//
// The destination half was swept above. The ingest, relay, bitrate and restart
// series were left as unlabelled scalars taken off the default engine, so on a
// two-programme install `polyemesis_ingest_up` described programme 1 and there
// was no series anywhere that could say programme 2's encoder had stopped. That
// is not a wrong number an operator might question: it is the alert the metric's
// own comment tells you to write -- `ingest_up == 0 and on() sources > 0` --
// never evaluating for the programme that is off air, while the destination
// series it explains do go down.
//
// The assertion is on the LABEL rather than on a value, because the value of a
// series that does not exist is not wrong, it is absent, and absence is what
// made this invisible.
//
// Mutation: rebuild snap.Ingests from the default engine alone (`e :=
// s.eng()`), which is what handleMetrics did. Observed to fail with "the
// exposition carries no ingest series for programme 2".
func TestTheScrapeCarriesEveryProgrammesIngest(t *testing.T) {
	s, h, _, auth := managerServer(t, defaultTools())
	if s.eng() == nil {
		t.Fatal("no default engine in the fixture, so there is no wrong programme to be " +
			"reported and this test would pass having observed nothing")
	}
	other := secondProgramme(t, s)

	r := jsonRequest(t, http.MethodGet, "/api/v1/metrics", nil)
	auth(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/metrics: %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	for _, series := range []string{
		"polyemesis_ingest_up",
		"polyemesis_ingest_state",
		"polyemesis_ingest_bitrate_bits_per_second",
		"polyemesis_ingest_restarts_total",
		"polyemesis_relay_received_bytes_total",
		"polyemesis_relay_dropped_packets_total",
	} {
		for _, id := range []int64{s.eng().SourceID(), other.ID} {
			want := series + `{id="` + itoa(id) + `"`
			if !strings.Contains(body, want) {
				t.Errorf("the exposition carries no %s series for programme %d. Nothing "+
					"in the scrape can distinguish that programme's ingest being up "+
					"from its ingest not being represented, so the alert simply never "+
					"evaluates", series, id)
			}
		}
	}

	// THE CONTROL, and it is what makes the labels worth having: an unlabelled
	// sample beside the labelled ones would satisfy every assertion above while
	// leaving the old ambiguous series in place for an alert to match.
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "polyemesis_ingest_up ") ||
			strings.HasPrefix(line, "polyemesis_relay_received_bytes_total ") {
			t.Errorf("an UNLABELLED sample survives: %q. An alert matching it reads one "+
				"programme's number as the install's", line)
		}
	}
}

// A DESTINATION WHOSE PROGRAMME HAS NO ENGINE IS REPORTED, WITH THE REASON
// (#540).
//
// Manager.Sync logs and carries on when engine.New or Engine.Start fails for
// one source, so its rows stay in the database with no engine to describe them.
// DestinationStatuses concatenated over REGISTERED engines only, so those
// destinations vanished from the dashboard, from the WebSocket push and from
// the scrape -- while GET /destinations, which is store-backed, went on listing
// them. Two screens disagreeing, and the disappearance looks exactly like a
// destination nobody configured.
//
// The fixture creates the source WITHOUT a manager sync, which is the same
// "the manager has not been told about it" state the restart test uses. It
// stands in for the failed-to-build case because the observable is identical:
// a source row with destinations and no entry in m.engines.
//
// Mutation: drop the store sweep from DestinationStatuses. Observed to fail
// with "the status payload omits destination ... entirely".
func TestADestinationOnAProgrammeWithNoEngineIsStillReported(t *testing.T) {
	s, _, _, _ := managerServer(t, defaultTools())
	if s.eng() == nil {
		t.Fatal("no default engine in the fixture, so the concatenation under test has " +
			"nothing to concatenate and this test would prove nothing")
	}

	orphaned := &db.Source{Name: "failed to build", Enabled: true, Ingest: db.DefaultSettings().Ingest}
	if err := s.store.CreateSource(orphaned); err != nil {
		t.Fatalf("create the source: %v", err)
	}
	if s.mgr.Engine(orphaned.ID) != nil {
		t.Fatalf("source %d has an engine without a sync, so this test cannot reach the "+
			"state it is named for", orphaned.ID)
	}
	row, err := s.store.CreateDestination(&db.Destination{
		Name: "on the engineless programme", Kind: db.DestFile, URL: "c.mkv",
		Enabled: true, AudioBitrate: 160, SourceID: &orphaned.ID,
	})
	if err != nil {
		t.Fatalf("create the destination: %v", err)
	}

	var got *engine.DestStatus
	for _, d := range s.statusPayload(s.eng()).Destinations {
		if d.ID == row.ID {
			ds := d
			got = &ds
		}
	}
	if got == nil {
		t.Fatalf("the status payload omits destination %d entirely. Its programme has no "+
			"engine, so nothing reports it -- while GET /destinations still lists it, "+
			"which is two screens disagreeing about whether it exists", row.ID)
	}
	if got.Error == "" {
		t.Errorf("destination %d is reported with no error, so it is indistinguishable "+
			"from one that is merely stopped. The reason it has no process is that its "+
			"programme is not running, and nothing else on this payload says so", row.ID)
	}
	if got.Process != nil {
		t.Errorf("destination %d reports a process; there is no engine to have started one", row.ID)
	}

	// THE CONTROL. A sweep that appended every row in the table regardless of
	// engine would satisfy the assertions above and would ALSO report the
	// default programme's destinations twice, each described differently.
	seen := map[int64]int{}
	for _, d := range s.statusPayload(s.eng()).Destinations {
		seen[d.ID]++
	}
	for id, n := range seen {
		if n > 1 {
			t.Errorf("destination %d appears %d times in the payload; the dashboard draws "+
				"one card per entry and Prometheus rejects a duplicate series", id, n)
		}
	}
}
