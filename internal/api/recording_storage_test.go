package api

import (
	"net/http"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// THE RECORDINGS PAGE HAS TO BE ABLE TO EXPLAIN A RECORDER THAT STOPPED ITSELF,
// and this is the test that says the verdict reaches it.
//
// When free space falls below settings.Recording.MinFreeGB the engine's own
// recording manager halts the recorder child -- deliberately, because a
// recorder left running until writes fail takes the database and the HLS
// preview down with it. The only place an operator is ever told is the banner
// on the recordings page, which renders from usage.storage.halted and
// usage.storage.reason.
//
// The trap this pins is specific and was live: the usage FIGURES come off the
// manager's shared read-only recording manager, which is never swept and has
// no storage guard, so its own StorageState is the zero value for the life of
// the process. Serve that alongside the figures and the page reports a healthy
// recorder that stopped an hour ago. Hence the second assertion, which is not
// redundant: it is what distinguishes "the handler asked the engine" from "the
// zero value happened to be right".
//
// Mutation: drop `usage.Storage = s.storageVerdict()` from
// handleRecordingUsage. Observed to fail here.
func TestRecordingUsageReportsARecorderTheFreeSpaceFloorHalted(t *testing.T) {
	s, h, _, sign := managerServer(t, defaultTools())

	engines := s.mgr.Engines()
	if len(engines) == 0 {
		t.Fatal("the fixture started no engine, so there is no storage guard to halt and " +
			"this case would pass on the zero value alone")
	}
	// A floor no volume can clear, which is how this fills a disk without one.
	halt := db.RecordingSettings{Enabled: true, SegmentSeconds: 3600, MinFreeGB: 1e9}
	for _, eng := range engines {
		eng.Recordings().CheckFreeSpace(halt)
		if !eng.Recordings().Storage().Halted {
			t.Fatalf("source %d's own recording manager did not halt on a %.0f GB floor, so "+
				"the state under test was never reached", eng.SourceID(), halt.MinFreeGB)
		}
	}
	if s.recordings().Storage().Halted {
		t.Fatal("the shared read-only recording manager reports halted, so it is being " +
			"swept by something after all and the assertion below no longer discriminates")
	}

	var usage map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/recordings/usage", nil, http.StatusOK), &usage)

	storage, ok := usage["storage"].(map[string]any)
	if !ok {
		t.Fatalf("GET /recordings/usage carried no storage block at all: %v", usage)
	}
	if storage["halted"] != true {
		t.Errorf("recording is HALTED and GET /recordings/usage reports storage %v: the "+
			"recordings page shows no banner, so an operator sees a recorder that stopped "+
			"on its own and nothing anywhere that says why", storage)
	}
	// The banner prints the reason verbatim; a bare true would name a floor the
	// operator then has to go and find in the settings.
	if reason, _ := storage["reason"].(string); reason == "" {
		t.Errorf("the halt is reported with no reason, so the banner can say that recording "+
			"stopped but not that the volume is below the floor: %v", storage)
	}
}

// The other half: no engine means no recorder, so nothing has been halted and
// the endpoint says so rather than panicking on the way to finding out.
//
// It is the honest answer and not a fallback. The figures still come from the
// shared manager -- an operator clearing disk after their last source went away
// is exactly the caller this route exists for.
func TestRecordingUsageReportsNoHaltWithNoEngineRunning(t *testing.T) {
	s, h, _, sign := managerServerWithoutEngines(t, defaultTools())
	if s.eng() != nil {
		t.Fatal("the fixture left an engine running, so this case is not about its absence")
	}

	var usage map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/recordings/usage", nil, http.StatusOK), &usage)

	storage, ok := usage["storage"].(map[string]any)
	if !ok {
		t.Fatalf("GET /recordings/usage carried no storage block: %v", usage)
	}
	if storage["halted"] != false {
		t.Errorf("an install with no engine reports recording halted (%v), which names a "+
			"recorder that does not exist", storage)
	}
}
