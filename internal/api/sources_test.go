package api

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/config"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// sourceServer is renditionServer under a name that says what these tests are
// about. Same fixture: a real store, a real manager, and an FFmpeg path that
// cannot exec, so a reconcile logs a failed spawn instead of binding a real
// port from a unit test.
func sourceServer(t *testing.T) (http.Handler, *db.DB, func(*http.Request)) {
	t.Helper()
	h, store, sign := renditionServer(t, defaultTools())

	// Say SRT out loud.
	//
	// A fresh install now starts at db.IngestUnset — nothing is chosen for the
	// operator — so a source created by the fixture has no ingest mode and
	// therefore no publish URL and no token enforcement. That is the point of
	// the change, and it means a test that wants SRT behaviour has to ask for
	// it rather than inherit it from a default that no longer exists.
	src, err := store.GetSource(1)
	if err != nil {
		t.Fatalf("fixture source: %v", err)
	}
	src.Ingest.Mode = db.IngestSRT
	if err := store.UpdateSource(src); err != nil {
		t.Fatalf("fixture: choose SRT: %v", err)
	}
	return h, store, sign
}

type sourceRow struct {
	ID             int64             `json:"id"`
	Name           string            `json:"name"`
	Token          string            `json:"token"`
	Enabled        bool              `json:"enabled"`
	Publishing     bool              `json:"publishing"`
	IsDefault      bool              `json:"isDefault"`
	TokenEnforced  bool              `json:"tokenEnforced"`
	PublishURLs    map[string]string `json:"publishUrls"`
	Running        bool              `json:"running"`
	ListenerHealth *struct {
		State  string `json:"state"`
		Detail string `json:"detail"`
	} `json:"listenerHealth"`
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

// RTMP is addressed by the same token, split across OBS's two boxes: the map
// value is the server half and streamKey carries the address.
//
// It used to emit ingest.rtmp.streamKey, which was "stream" for every source
// created from the defaults. Handing that to an operator now would give them a
// key that reaches nothing -- and if it did reach something, it would be
// whichever source the map happened to yield.
func TestTheRTMPPublishURLsCarryTheToken(t *testing.T) {
	h, store, sign := sourceServer(t)

	src, err := store.GetSource(1)
	if err != nil {
		t.Fatalf("GetSource: %v", err)
	}
	src.Ingest.Mode = db.IngestRTMP
	src.Ingest.RTMP.App = "live"
	src.Ingest.RTMP.StreamKey = "stream" // what an older build stored
	if err := store.UpdateSource(src); err != nil {
		t.Fatalf("UpdateSource: %v", err)
	}

	row := listSources(t, h, sign)[0]
	if got := row.PublishURLs["streamKey"]; got != row.Token {
		t.Errorf("streamKey = %q, want the token %q: anything else addresses nothing", got, row.Token)
	}
	server := row.PublishURLs["rtmp"]
	if server == "" {
		t.Fatal("no RTMP publish URL offered")
	}
	// The server half must NOT also carry the address. An operator who fills in
	// both boxes would publish to /live/<token>/<token>, which reaches nothing
	// and looks exactly like it should work.
	if strings.Contains(server, row.Token) {
		t.Errorf("the RTMP server URL carries the token as well as the key box: %s", server)
	}
	if !strings.HasSuffix(server, "/live") {
		t.Errorf("rtmp server URL = %q, want it to end at the app", server)
	}
}

// tokenEnforced must be true for RTMP too. It used to be hard-coded to SRT,
// which was honest while RTMP was addressed by a stream key and is a lie now:
// an RTMP source told "your token is not enforced" would have its operator
// leave a rotated token alone believing it protects nothing.
func TestTokenEnforcedCoversRTMPSources(t *testing.T) {
	h, store, sign := sourceServer(t)

	src, err := store.GetSource(1)
	if err != nil {
		t.Fatalf("GetSource: %v", err)
	}
	src.Ingest.Mode = db.IngestRTMP
	src.Ingest.RTMP.App = "live"
	if err := store.UpdateSource(src); err != nil {
		t.Fatalf("UpdateSource: %v", err)
	}
	srv := serverUnderTest(t, h)
	if srv.mgr == nil {
		t.Fatal("no manager in the fixture")
	}
	if err := srv.mgr.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if !srv.mgr.ListenerBound(db.IngestRTMP) {
		t.Skip("the RTMP listener could not bind in this environment; nothing to assert")
	}
	if !listSources(t, h, sign)[0].TokenEnforced {
		t.Error("tokenEnforced is false for an RTMP source while its listener is bound")
	}
}

// #105: a half-bound SRT listener has to reach the operator, not just the log.
//
// srtserver.Start binds one socket per address family for a wildcard and
// survives one of them failing, deliberately -- a container without IPv6 is a
// legitimate deployment. What it did not do was tell anything downstream, so
// the source card showed running, token-enforced and healthy while every
// encoder on the family that never bound could not connect.
//
// The test occupies the IPv6 wildcard on a real port first, then points the
// install's SRT listener at it and reconciles, so what is asserted is the
// response of the production handler to a genuinely degraded listener.
func TestDegradedSRTListenerIsReportedOnTheSourceCard(t *testing.T) {
	occupied, err := net.ListenPacket("udp6", "[::]:0")
	if err != nil {
		t.Skipf("SKIPPING: this host cannot bind udp6 at all (%v), so a "+
			"partial bind cannot be staged here", err)
	}
	defer occupied.Close()
	_, portStr, err := net.SplitHostPort(occupied.LocalAddr().String())
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port %q: %v", portStr, err)
	}

	h, store, sign := sourceServer(t)
	st, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	st.Listeners.SRTPort = port
	if err := store.PutSettings(st); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}

	srv := serverUnderTest(t, h)
	if srv.mgr == nil {
		t.Fatal("no manager in the fixture")
	}
	if err := srv.mgr.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !srv.mgr.ListenerBound(db.IngestSRT) {
		t.Skip("the SRT listener bound no family at all here; nothing to assert")
	}

	row := listSources(t, h, sign)[0]
	if row.ListenerHealth == nil {
		t.Fatal("the source card carried no listenerHealth for a half-bound listener")
	}
	if row.ListenerHealth.State != "degraded" {
		t.Errorf("listenerHealth.state = %q, want %q", row.ListenerHealth.State, "degraded")
	}
	// A bare "degraded" sends the operator to the logs, which is the situation
	// this field exists to end.
	if !strings.Contains(row.ListenerHealth.Detail, "::") {
		t.Errorf("the detail does not name the address family that failed: %q",
			row.ListenerHealth.Detail)
	}
	// And the source is still running and still enforcing its token, because
	// both are true. Folding this into those booleans would answer a different
	// question wrongly.
	if !row.Running {
		t.Error("a half-bound listener reported the source as not running; the " +
			"IPv4 half is serving and its publishers are fine")
	}
}

// A read-scoped token must not be able to read its way to publishing.
//
// The scope model refuses writes by HTTP method, which is the right shape for a
// rule about routes and blind to a GET whose RESPONSE is a credential. A
// source's publish token is exactly that: the token IS the address on both
// listeners, so a monitoring credential that could read it could inject video
// into a live programme using only a GET it was explicitly allowed to make.
//
// Both shapes are asserted, because the publish URLs embed the token and
// blanking only the field would hand the same secret back in a different form.
func TestReadScopedTokenCannotReadAPublishToken(t *testing.T) {
	h, store, sign := sourceServer(t)

	// The fixture used to leave the ingest block empty, and that made this test
	// a FALSE POSITIVE: it asserted on Source.Token alone, over a body that
	// carried an empty passphrase and an empty legacy RTMP key, so it passed
	// happily while both of those credentials were being handed to a read token
	// on any real install. Plant them, so the assertion below is made against a
	// response that has something to leak.
	src, err := store.GetSource(1)
	if err != nil {
		t.Fatalf("fixture source: %v", err)
	}
	src.Ingest.SRT.Passphrase = "fixture-srt-passphrase-1234"
	src.Ingest.RTMP.StreamKey = "fixture-legacy-rtmp-key"
	if err := store.UpdateSource(src); err != nil {
		t.Fatalf("plant ingest credentials: %v", err)
	}

	// What the operator's own session sees, which must be unchanged: the
	// console needs the token to show it.
	seen := listSources(t, h, sign)[0]
	if seen.Token == "" {
		t.Fatal("the fixture source has no token, so this test proves nothing")
	}

	read := createScopedToken(t, h, sign, "monitoring", db.ScopeRead)
	r := jsonRequest(t, http.MethodGet, "/api/v1/sources", nil)
	r.Header.Set("Authorization", "Bearer "+read)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("read token could not list sources: %d %s", w.Code, w.Body.String())
	}
	// Against the raw body, not the decoded struct: the point is that the
	// secret does not appear ANYWHERE in what left the process.
	if strings.Contains(w.Body.String(), seen.Token) {
		t.Errorf("the publish token reached a read-scoped token: %s", w.Body.String())
	}
	// And neither does the stored ingest block the embedded *db.Source carries.
	// The legacy RTMP key is the sharper of the two: engine.Manager honours a
	// stored one as a publish address on an install upgraded from a pre-one-port
	// build, so it is a working ingest credential, and the redaction that only
	// blanked the DERIVED legacyRtmpKey field was handing back the identical
	// string two JSON fields away.
	for _, secret := range []string{"fixture-srt-passphrase-1234", "fixture-legacy-rtmp-key"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Errorf("the ingest credential %q reached a read-scoped token: %s",
				secret, w.Body.String())
		}
	}

	var rows []sourceRow
	decodeInto(t, w.Body.Bytes(), &rows)
	if rows[0].Token != "" {
		t.Errorf("token field = %q, want empty for a read-scoped principal", rows[0].Token)
	}
	// The listing still has to be worth making, or the redaction has just
	// broken the use case the read scope exists for.
	if rows[0].Name == "" {
		t.Error("redaction emptied the source listing; a monitoring token still needs to see its sources")
	}

	// An admin token keeps the old behaviour: it can rotate the token anyway,
	// so withholding it would be a lock with the key taped to it.
	admin := createScopedToken(t, h, sign, "deploy", db.ScopeAdmin)
	ra := jsonRequest(t, http.MethodGet, "/api/v1/sources", nil)
	ra.Header.Set("Authorization", "Bearer "+admin)
	if wa := do(t, h, ra); !strings.Contains(wa.Body.String(), seen.Token) {
		t.Error("an admin token was denied the publish token it can already rotate")
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

// The last source may be deleted, and the install it leaves behind is one an
// operator can still use.
//
// This answered 400 until the store guard came off. The refusal was written
// when zero sources was an unreachable state that nothing in the product could
// answer in; a fresh install now boots into exactly that state, so the delete
// is a route to somewhere the operator already is on their first day.
//
// The second half is the half worth asserting. A 204 that left the Sources page
// unable to answer would be the same trap under a better status code, so this
// reads the list back through the API rather than the store: it is the screen
// the operator lands on, and the create form is on it.
func TestTheLastSourceCanBeDeletedAndTheSourcesPageStillAnswers(t *testing.T) {
	h, _, sign := sourceServer(t)

	only := listSources(t, h, sign)[0]
	send(t, h, sign, http.MethodDelete,
		"/api/v1/sources/"+strconv.FormatInt(only.ID, 10), nil, http.StatusNoContent)

	if rows := listSources(t, h, sign); len(rows) != 0 {
		t.Fatalf("got %d sources after deleting the only one, want 0", len(rows))
	}

	// And a repeat of the same delete says the row is gone rather than
	// describing a rule -- a client retrying a request it never saw the answer
	// to, which is the ordinary way this arrives.
	send(t, h, sign, http.MethodDelete,
		"/api/v1/sources/"+strconv.FormatInt(only.ID, 10), nil, http.StatusNotFound)
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

	// The fixture binds the shared SRT listener on a port it picked and owns --
	// see freeUDPPort in renditions_test.go -- so the listener really is up here
	// and a false reading is the product's fault, not the machine's.
	//
	// The message used to read "tokenEnforced is false while the listener is
	// bound", which was the one thing that could not be true at that moment and
	// sent me looking in the wrong half of the system for half an hour. It said
	// "bound" because the fixture used the 6000 default, and on a developer
	// machine 6000 is a popular number.
	if !listSources(t, h, sign)[0].TokenEnforced {
		t.Fatalf("tokenEnforced is false, so the shared SRT listener is not bound "+
			"on udp/%d -- a port this fixture chose because it was free. Nothing "+
			"external explains this one", srtPortOf(t, store))
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

// The delete warning has to count what a delete would actually take.
//
// ISSUE #304. sourceView declares Destinations and Renditions with the comment
// "what a delete would take with it", and viewSource never assigned either, so
// every source reported 0 however many it owned.
//
// Zero is not a neutral wrong answer here. It is the REASSURING one: it tells a
// caller the delete is free, in precisely the case where the warning is the
// point. A UI asking "delete this source?" got "nothing else goes with it" for a
// source owning five destinations.
//
// THE ASSERTION IS ON A NON-ZERO COUNT, and that is the whole design of this
// test. A field hard-wired to 0 satisfies any `>= 0` check, so the only
// assertion that can fail against the old code is one that names the number.
// Two destinations rather than one, so an implementation returning a bare
// boolean-ish 1 is caught as well.
//
// Mutation: delete the ListDestinationsBySource block in viewSource.
// Observed to fail with "destinations = 0, want 2".
func TestASourceReportsWhatADeleteWouldTakeWithIt(t *testing.T) {
	srv, _, store := testServer(t, config.Config{})

	src := &db.Source{Name: "counted", Enabled: true, Ingest: db.DefaultSettings().Ingest}
	if err := store.CreateSource(src); err != nil {
		t.Fatalf("create source: %v", err)
	}
	for i, name := range []string{"one", "two"} {
		if _, err := store.CreateDestination(&db.Destination{
			SourceID: &src.ID, Name: name, Kind: db.DestFile, URL: name + ".mkv",
		}); err != nil {
			t.Fatalf("create destination %d: %v", i, err)
		}
	}

	// A second source with NOTHING attached, so a implementation that counted
	// every destination in the install rather than this source's would fail
	// here even while the count above looked right.
	bare := &db.Source{Name: "bare", Enabled: true, Ingest: db.DefaultSettings().Ingest}
	if err := store.CreateSource(bare); err != nil {
		t.Fatalf("create bare source: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sources", nil)
	got := srv.viewSource(req, src, 0)
	if got.Destinations != 2 {
		t.Errorf("destinations = %d, want 2. This number is what a caller is told "+
			"a delete would take with it, and 0 is the answer that says the delete "+
			"is free", got.Destinations)
	}

	empty := srv.viewSource(req, bare, 0)
	if empty.Destinations != 0 {
		t.Errorf("a source with no destinations reported %d; the count is not scoped "+
			"to the source", empty.Destinations)
	}
}
