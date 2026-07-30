package api

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// sourceServer is renditionServer under a name that says what these tests are
// about. Same fixture: a real store, a real manager, and an FFmpeg path that
// cannot exec, so a reconcile logs a failed spawn instead of binding a real
// port from a unit test.
func sourceServer(t *testing.T) (http.Handler, *db.DB, func(*http.Request)) {
	t.Helper()
	return renditionServer(t, defaultTools())
}

type sourceRow struct {
	ID            int64             `json:"id"`
	Name          string            `json:"name"`
	Token         string            `json:"token"`
	Enabled       bool              `json:"enabled"`
	Publishing    bool              `json:"publishing"`
	IsDefault     bool              `json:"isDefault"`
	TokenEnforced bool              `json:"tokenEnforced"`
	PublishURLs   map[string]string `json:"publishUrls"`
}

func listSources(t *testing.T, h http.Handler, sign func(*http.Request)) []sourceRow {
	t.Helper()
	var rows []sourceRow
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/sources", nil, http.StatusOK), &rows)
	return rows
}

func TestSourcesListStartsWithTheMigratedDefault(t *testing.T) {
	h, _, sign := sourceServer(t)

	rows := listSources(t, h, sign)
	if len(rows) != 1 {
		t.Fatalf("got %d sources on a fresh install, want 1", len(rows))
	}
	if !rows[0].IsDefault {
		t.Error("the only source is not marked default; unscoped requests would have nowhere to go")
	}
	if rows[0].Token == "" {
		t.Error("source has no publish token")
	}
}

func TestCreatingASourceNeedsOnlyAName(t *testing.T) {
	h, _, sign := sourceServer(t)

	// The point of defaulting the ingest block: an operator adds a programme
	// and then edits its ports, rather than having to supply a complete valid
	// ingest before the row can exist at all.
	var created sourceRow
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/sources",
		map[string]any{"name": "Vertical"}, http.StatusCreated), &created)

	if created.Name != "Vertical" {
		t.Errorf("name = %q, want Vertical", created.Name)
	}
	if created.IsDefault {
		t.Error("a second source became the default; unscoped requests would change programme")
	}
	if created.Token == "" {
		t.Error("created source has no publish token")
	}
}

// The token IS the address now, so it has to appear in the SRT publish URL --
// the operator pastes that URL into OBS and nothing else identifies the source.
// The inverse of what this test used to assert, and the change is the feature.
func TestTheSRTPublishURLCarriesTheToken(t *testing.T) {
	h, _, sign := sourceServer(t)

	rows := listSources(t, h, sign)
	if rows[0].Token == "" {
		t.Fatal("source has no token, so it has no address")
	}
	srt := rows[0].PublishURLs["srt"]
	if srt == "" {
		t.Fatal("no SRT publish URL offered")
	}
	if !strings.Contains(srt, rows[0].Token) {
		t.Errorf("the SRT publish URL does not carry the token, so it addresses nothing: %s", srt)
	}
}
func TestUpdatingASourceWithoutATokenKeepsTheStoredOne(t *testing.T) {
	h, _, sign := sourceServer(t)

	before := listSources(t, h, sign)[0]
	// A rename, which is what the UI sends. It carries no token, and blanking
	// a secret an encoder is already using would take the ingest down.
	var got sourceRow
	decodeInto(t, send(t, h, sign, http.MethodPut,
		"/api/v1/sources/"+strconv.FormatInt(before.ID, 10),
		map[string]any{"name": "Horizontal"}, http.StatusOK), &got)

	if got.Name != "Horizontal" {
		t.Errorf("name = %q, want Horizontal", got.Name)
	}
	if got.Token != before.Token {
		t.Errorf("token changed on a rename: %q, want %q", got.Token, before.Token)
	}
}

func TestTheLastSourceCannotBeDeletedThroughTheAPI(t *testing.T) {
	h, _, sign := sourceServer(t)

	only := listSources(t, h, sign)[0]
	// 400 rather than 204: an install with no sources has no ingest at all and
	// no way back through the UI.
	send(t, h, sign, http.MethodDelete,
		"/api/v1/sources/"+strconv.FormatInt(only.ID, 10), nil, http.StatusBadRequest)
}

func TestDeletingASourceIsAllowedOnceASecondExists(t *testing.T) {
	h, _, sign := sourceServer(t)

	var extra sourceRow
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/sources",
		map[string]any{"name": "Vertical"}, http.StatusCreated), &extra)

	send(t, h, sign, http.MethodDelete,
		"/api/v1/sources/"+strconv.FormatInt(extra.ID, 10), nil, http.StatusNoContent)

	if rows := listSources(t, h, sign); len(rows) != 1 {
		t.Fatalf("got %d sources after deleting one, want 1", len(rows))
	}
}

func TestADestinationCreatedWithoutASourceLandsOnTheDefault(t *testing.T) {
	h, store, sign := sourceServer(t)

	// This is every API client written before sources existed.
	createDestination(t, h, sign, map[string]any{
		"name": "legacy", "kind": "rtmp", "url": "rtmp://example.test/live",
		"audioBitrate": 160,
	})

	want, err := store.DefaultSourceID()
	if err != nil {
		t.Fatalf("DefaultSourceID: %v", err)
	}
	rows, err := store.ListDestinationsBySource(want)
	if err != nil {
		t.Fatalf("ListDestinationsBySource: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("default source has %d destinations, want 1 -- a NULL source_id "+
			"leaves a destination created but never started", len(rows))
	}
}

func TestSourceValidationSurfacesThroughTheAPI(t *testing.T) {
	h, _, sign := sourceServer(t)

	body := send(t, h, sign, http.MethodPost, "/api/v1/sources", map[string]any{
		"name": "Broken",
		"ingest": map[string]any{"mode": "srt", "srt": map[string]any{
			"passphrase": "short", "latencyMs": 200,
		}},
	}, http.StatusBadRequest)

	if !strings.Contains(string(body), "passphrase") {
		t.Errorf("error body %s, want it to name the passphrase", body)
	}
}

func TestANewSourceIsEnabledUnlessTheCallerSaysOtherwise(t *testing.T) {
	h, _, sign := sourceServer(t)

	// The UI sends only a name. A source that arrives disabled refuses the
	// encoder with "source disabled", and nothing on screen suggests the thing
	// you just created is off.
	var created sourceRow
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/sources",
		map[string]any{"name": "Vertical"}, http.StatusCreated), &created)
	if !created.Enabled {
		t.Error("a source created with just a name came out disabled")
	}

	// An explicit false still wins.
	var off sourceRow
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/sources",
		map[string]any{"name": "Standby", "enabled": false}, http.StatusCreated), &off)
	if off.Enabled {
		t.Error(`"enabled": false was ignored on create`)
	}
}

// tokenEnforced must follow the SOCKET, never configuration.
//
// There is no longer a flag to read: tokens are how every SRT source is
// addressed, so the only question is whether the listener is actually bound.
// Reporting "enforced" while nothing is listening is false assurance about the
// one control standing between a stranger and an operator's programme.
func TestTokenEnforcedTracksTheListenerNotTheSetting(t *testing.T) {
	h, store, sign := sourceServer(t)
	srv := serverUnderTest(t, h)

	// The fixture runs a manager, so the listener is up and the field says so.
	if !listSources(t, h, sign)[0].TokenEnforced {
		// Distinguish "the product is wrong" from "this machine's port is busy",
		// because the two need completely different responses and the old message
		// asserted the first while the second was far more likely.
		//
		// The default SRT port is 6000, and on a developer machine that is a
		// popular number -- Docker Desktop publishes on it, among others. When
		// something else holds it the fixture's listener cannot bind, so
		// tokenEnforced is correctly false and this test has nothing to measure.
		// The previous message read "tokenEnforced is false while the listener is
		// bound" at the exact moment the listener was NOT bound, which sends the
		// reader looking for a bug in the wrong half of the system.
		//
		// Skip rather than fail, and only on a real port conflict: CI runs in a
		// clean container where nothing else wants 6000, so this never fires there
		// and the assertion stays strict where it counts.
		st, err := store.GetSettings()
		if err != nil {
			t.Fatalf("GetSettings: %v", err)
		}
		port := st.Listeners.SRTPort
		if pc, lerr := net.ListenPacket("udp", fmt.Sprintf(":%d", port)); lerr != nil {
			t.Skipf("udp/%d is already in use on this machine (%v), so the fixture's "+
				"SRT listener could not bind and tokenEnforced is correctly false. "+
				"Free the port -- on a developer machine it is usually Docker -- or "+
				"run this in a container", port, lerr)
		} else {
			pc.Close()
			t.Fatalf("the SRT listener is not bound even though udp/%d is free, so "+
				"tokenEnforced is false for a reason inside the product rather than "+
				"outside it", port)
		}
	}

	// Port 0 specifically. To the kernel :0 is not an error, it means "any free
	// port" -- so without a guard the listener binds something arbitrary and
	// still calls itself listening.
	st, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	st.Listeners.SRTPort = 0
	if err := store.PutSettings(st); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	if srv.mgr == nil {
		t.Fatal("no manager in the fixture")
	}
	if err := srv.mgr.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if listSources(t, h, sign)[0].TokenEnforced {
		t.Error("tokenEnforced stayed true with nothing bound")
	}
}
