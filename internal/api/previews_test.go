package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The dashboard grid's two reads: GET /previews for what to draw, and
// /hls/{source}/... for the picture itself.
//
// Both are SOURCE-SCOPED, and that is the whole feature. The always-on status
// feed is not: every engine publishes onto the same broker, so a grid built on
// it redraws every tile from whichever engine spoke last and shows the wrong
// picture's state under the right picture's name. The assertions below are
// therefore about which programme an answer belongs to, never about the shape of
// the payload -- a shape assertion passes just as happily on a handler that
// hands every tile the same source's answer.

// previewTileRow is the tile as the grid decodes it.
type previewTileRow struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	OutputLive bool   `json:"outputLive"`
	IngestLive bool   `json:"ingestLive"`
	OnAir      string `json:"onAir"`
}

func previewTilesOf(t *testing.T, h http.Handler, sign func(*http.Request)) []previewTileRow {
	t.Helper()
	var rows []previewTileRow
	decodeInto(t, send(t, h, sign, http.MethodGet, "/previews", nil, http.StatusOK), &rows)
	return rows
}

// addSource creates a programme through the real route, because that is what
// builds an engine for it -- the store alone does not.
func addSource(t *testing.T, h http.Handler, sign func(*http.Request), name string) sourceRow {
	t.Helper()
	var created sourceRow
	decodeInto(t, send(t, h, sign, http.MethodPost, "/api/v1/sources",
		map[string]any{"name": name}, http.StatusCreated), &created)
	return created
}

// One tile per programme, each carrying its OWN programme's name.
//
// A grid drawn from a handler that reports only the default source, or that
// names every tile after the first one, is the bug this endpoint exists to make
// impossible -- and neither is visible in a well-formed payload. So the tiles are
// checked against GET /api/v1/sources, which is where the operator read those
// names in the first place.
func TestEveryProgrammeGetsItsOwnPreviewTile(t *testing.T) {
	h, _, sign := sourceServer(t)

	if got := len(previewTilesOf(t, h, sign)); got != 1 {
		t.Fatalf("a fresh install with one programme reported %d tiles, want 1", got)
	}

	second := addSource(t, h, sign, "Vertical")

	tiles := previewTilesOf(t, h, sign)
	if len(tiles) != 2 {
		t.Fatalf("got %d tiles after adding a second programme, want 2: a source the "+
			"grid cannot see is a source nobody can watch", len(tiles))
	}

	named := map[int64]string{}
	for _, src := range listSources(t, h, sign) {
		named[src.ID] = src.Name
	}
	seen := map[int64]bool{}
	for _, tile := range tiles {
		want, ok := named[tile.ID]
		if !ok {
			t.Errorf("tile %d names no programme in /api/v1/sources", tile.ID)
			continue
		}
		if tile.Name != want {
			t.Errorf("tile %d is labelled %q; the operator created it as %q, and a tile "+
				"under the wrong name is a picture attributed to the wrong programme",
				tile.ID, tile.Name, want)
		}
		seen[tile.ID] = true
	}
	if !seen[second.ID] {
		t.Errorf("the programme just created (%d) has no tile", second.ID)
	}
}

// A tile must not claim a picture that nothing has sent.
//
// SCOPE, because the neighbouring comments overstate what a fixture like this
// can prove: nothing has ever published here, so a `RxBytes() > 0` gate would
// answer false too and pass this test. What is pinned here is only that the
// endpoint invents nothing on a fresh install -- no picture, no tier, no
// geometry. That the gate is a byte DELTA rather than a total, and so clears
// again when a stream ends, is pinned where a hub can actually be driven:
// TestOutputLiveWaitsToSeeBytesArriveOnTheHubTheDestinationsRead and
// TestOutputStopsBeingLiveOnceTheStreamEnds in internal/engine.
func TestAPreviewTileClaimsNothingBeforeAnythingArrives(t *testing.T) {
	h, _, sign := sourceServer(t)

	tiles := previewTilesOf(t, h, sign)
	if len(tiles) == 0 {
		t.Fatal("no tiles at all, so there is nothing to assert about")
	}
	for _, tile := range tiles {
		if tile.OutputLive {
			t.Errorf("tile %d reports output on air with no encoder ever having connected; "+
				"the grid would draw a player against a stream that does not exist", tile.ID)
		}
		if tile.IngestLive {
			t.Errorf("tile %d reports the operator's own encoder arriving on a fixture "+
				"where nothing has published", tile.ID)
		}
		if tile.OnAir != "" {
			t.Errorf("tile %d labels itself %q with no failover tier running; the tile "+
				"would name a tier that does not exist", tile.ID, tile.OnAir)
		}
		// Geometry comes from a probe. Inventing one before anything has been
		// measured letterboxes a vertical source into a box it never filled.
		if tile.Width != 0 || tile.Height != 0 {
			t.Errorf("tile %d carries geometry %dx%d before a probe has landed",
				tile.ID, tile.Width, tile.Height)
		}
	}
}

// writePreviewPlaylist puts a playlist where ONE source's preview encoder would
// have written it.
func writePreviewPlaylist(t *testing.T, s *Server, sourceID int64, marker string) {
	t.Helper()
	dir := s.cfg.HLSDirFor(sourceID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("preview directory for source %d: %v", sourceID, err)
	}
	body := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n" + marker + "\n"
	if err := os.WriteFile(filepath.Join(dir, "index.m3u8"), []byte(body), 0o644); err != nil {
		t.Fatalf("write playlist for source %d: %v", sourceID, err)
	}
}

func previewPath(id int64) string {
	return "/hls/" + strconv.FormatInt(id, 10) + "/index.m3u8"
}

// Each programme's playlist comes back from its OWN directory.
//
// Two distinct playlists are written and both are fetched, because one alone
// cannot tell a per-source directory apart from a shared one: a handler that
// serves everybody the default source's directory answers the first request
// perfectly.
func TestEachProgrammesPreviewIsServedFromItsOwnDirectory(t *testing.T) {
	h, _, sign := sourceServer(t)
	srv := serverUnderTest(t, h)
	second := addSource(t, h, sign, "Vertical")

	markers := map[int64]string{1: "#-- horizontal --", second.ID: "#-- vertical --"}
	for id, marker := range markers {
		writePreviewPlaylist(t, srv, id, marker)
	}

	for id, marker := range markers {
		path := previewPath(id)
		r := jsonRequest(t, http.MethodGet, path, nil)
		sign(r)
		w := do(t, h, r)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), marker) {
			t.Errorf("GET %s served:\n%s\nwant the playlist written for source %d. A "+
				"shared preview directory is what let one engine's start clear another's "+
				"live playlist", path, w.Body.String(), id)
		}
		// hls.js is handed the URL directly, so the two headers a media element
		// depends on have to survive the per-source path.
		if got := w.Header().Get("Content-Type"); got != "application/vnd.apple.mpegurl" {
			t.Errorf("GET %s: Content-Type %q; a player will not parse a manifest it is "+
				"not told is one", path, got)
		}
		if got := w.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("GET %s: Cache-Control %q, want no-store -- the playlist is rewritten "+
				"every segment and a cached one freezes the preview a few seconds in",
				path, got)
		}
	}
}

// A programme that is not there answers the way an empty one does.
//
// The player is a tab somebody left open, polling a playlist every few seconds.
// A deleted source, and an id too large to parse as one, are both ORDINARY
// there -- not a malformed request and certainly not a server fault. 404 says
// exactly that; a 400 or a 500 would put a steady stream of apparent faults
// into the logs and the alerting for a tab nobody closed.
//
// Note what this does NOT rest on: PreviewPlayer does not read the status. It
// schedules the same retry for any fatal hls.js error, so the choice of code is
// about what the server reports, not about whether the tile comes back.
func TestAPreviewPathForAProgrammeThatIsNotThereIsNotAnError(t *testing.T) {
	h, _, sign := sourceServer(t)
	srv := serverUnderTest(t, h)

	// The default source's preview does exist, so a handler that ignores the id
	// in the path and serves the default directory would be caught here rather
	// than passing on an empty install.
	writePreviewPlaylist(t, srv, 1, "#-- horizontal --")

	tests := []struct {
		name string
		path string
	}{
		{"a source id no programme has", previewPath(4242)},
		{"a source id too large to be one", "/hls/99999999999999999999/index.m3u8"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := jsonRequest(t, http.MethodGet, tc.path, nil)
			sign(r)
			w := do(t, h, r)
			if w.Code != http.StatusNotFound {
				t.Errorf("GET %s returned %d, want 404: a left-open tab polls this path "+
					"for ever, and a deleted source is not a bad request or a server "+
					"fault to raise on every poll", tc.path, w.Code)
			}
			if strings.Contains(w.Body.String(), "horizontal") {
				t.Errorf("GET %s served the default programme's playlist, so the source in "+
					"the path is not being read at all", tc.path)
			}
		})
	}
}

// A tile names the tier the picture is coming from.
//
// This is the disagreement the payload exists to carry. With nothing publishing
// and the slate on air, the destinations ARE carrying a programme while the
// operator's own encoder is gone -- so the honest tile is the slate's picture
// with a line saying the input is missing, not a blank panel hiding the thing
// currently being broadcast. A tile that cannot name the tier cannot draw that
// line.
func TestAPreviewTileNamesTheTierThatIsOnAir(t *testing.T) {
	h, _, sign := sourceServer(t)

	var s map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &s)
	fo, ok := s["failover"].(map[string]any)
	if !ok {
		t.Fatal("settings carried no failover block")
	}
	slate, ok := fo["slate"].(map[string]any)
	if !ok {
		t.Fatal("the failover block carried no slate")
	}
	fo["enabled"], slate["enabled"] = true, true
	send(t, h, sign, http.MethodPut, "/api/v1/settings", s, http.StatusOK)

	// The tier is built by the reconcile the save triggers and picks a source on
	// its own sweep, so this polls rather than guessing an interval.
	var tile previewTileRow
	deadline := time.Now().Add(5 * time.Second)
	for {
		tiles := previewTilesOf(t, h, sign)
		if len(tiles) == 1 && tiles[0].OnAir != "" {
			tile = tiles[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the failover tier has been running for five seconds and the tile still " +
				"names no tier on air; the grid would draw the slate's picture with nothing " +
				"to say where it came from")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if tile.OnAir != "slate" {
		t.Errorf("onAir = %q; nothing is publishing, so the slate is what the destinations "+
			"are riding", tile.OnAir)
	}

	// And it is the SAME answer /status gives. Two readings of one fact that can
	// disagree are worse than one, because the pipeline page and the grid would
	// then contradict each other about the same programme.
	var st struct {
		Failover *struct {
			Active string `json:"active"`
		} `json:"failover"`
	}
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/status", nil, http.StatusOK), &st)
	if st.Failover == nil {
		t.Fatal("/status reports no failover tier while /previews names one")
	}
	if st.Failover.Active != tile.OnAir {
		t.Errorf("the tile says %q is on air and /status says %q", tile.OnAir, st.Failover.Active)
	}
}
