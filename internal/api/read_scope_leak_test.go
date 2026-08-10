package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// The recurrence guard for #150's disclosure findings.
//
// Everything in this file drives the REAL chi router with a REAL bearer token
// minted through POST /auth/tokens, and asserts on the BYTES that left the
// process. Nothing here reads production source text (#107): a grep would have
// passed just as happily against the code that shipped the leak, because every
// one of those leaks was a correct-looking handler serialising a struct whose
// name says nothing about what is inside it.
//
// The previous audit missed three credentials by SAMPLING handlers. This test
// enumerates the router instead, and refuses to pass while a GET route exists
// that it has neither driven nor been told, in writing, to skip.

// Sentinels. Improbable enough that a substring match is a real match, and
// distinct per field so a failure names which credential escaped rather than
// just that one did.
const (
	sentinelSourceSRT     = "SENTINEL-source-srt-passphrase-9f3a"
	sentinelSourceRTMP    = "SENTINEL-source-rtmp-streamkey-9f3a"
	sentinelSourcePullPwd = "SENTINEL-source-pull-password-9f3a"
	sentinelSetSRT        = "SENTINEL-settings-srt-passphrase-9f3a"
	sentinelSetRTMP       = "SENTINEL-settings-rtmp-streamkey-9f3a"
	sentinelSetPullPwd    = "SENTINEL-settings-pull-password-9f3a"
	sentinelBackupSRT     = "SENTINEL-backup-srt-passphrase-9f3a"
	sentinelBackupRTMP    = "SENTINEL-backup-rtmp-streamkey-9f3a"
	sentinelBackupPullPwd = "SENTINEL-backup-pull-password-9f3a"
	sentinelMQTTPwd       = "SENTINEL-mqtt-broker-password-9f3a"
	sentinelDestKey       = "SENTINEL-destination-streamkey-9f3a"
	sentinelDestBackupKey = "SENTINEL-destination-backupkey-9f3a"
	sentinelIcecastPwd    = "SENTINEL-icecast-password-9f3a"
	sentinelPlayoutToken  = "SENTINEL-playout-watch-token-9f3a"
	sentinelAutomodKey    = "SENTINEL-automod-endpoint-apikey-9f3a"
	sentinelExpertArgs    = "SENTINEL-expert-argv-streamkey-9f3a"
)

// destFileName is a file destination's filename, and it is here as a
// NON-secret sentinel: the one thing the sweep must find INTACT.
//
// alerts.RedactURL is conservative about anything it cannot parse as a URL,
// which is right for a log line and wrong for this field: "shows/monday-night.
// mp4" came back as the bare word "[redacted]". Destination.url is a filename
// for kind:file and for the file form of kind:audio, so redacting it destroyed
// a field that never held a credential. See maskDestinationTarget.
const destFileName = "shows/monday-night-9f3a.mp4"

// allSentinels is what every response body is swept for. One list, so a
// credential added to the fixture is automatically checked on every route
// rather than only on the route whose author remembered it.
func allSentinels() []string {
	return []string{
		sentinelSourceSRT, sentinelSourceRTMP, sentinelSourcePullPwd,
		sentinelSetSRT, sentinelSetRTMP, sentinelSetPullPwd,
		sentinelBackupSRT, sentinelBackupRTMP, sentinelBackupPullPwd,
		sentinelMQTTPwd, sentinelDestKey, sentinelDestBackupKey,
		sentinelIcecastPwd, sentinelPlayoutToken,
		sentinelAutomodKey, sentinelExpertArgs,
	}
}

// plantedServer is the leak harness: a real engine-backed server whose every
// credential-bearing column holds a sentinel.
//
// Engine-backed rather than store-only on purpose. A store-only fixture cannot
// serve /system, /status or the destination routes at all, and a harness that
// silently 500s on the routes under test would report a clean sweep — which is
// the fail-open shape this whole exercise exists to refuse.
func plantedServer(t *testing.T) (http.Handler, *db.DB, func(*http.Request)) {
	t.Helper()
	h, store, sign := sourceServer(t)

	src, err := store.GetSource(1)
	if err != nil {
		t.Fatalf("fixture source: %v", err)
	}
	src.Ingest.SRT.Passphrase = sentinelSourceSRT
	src.Ingest.RTMP.StreamKey = sentinelSourceRTMP
	src.Ingest.Pull.URL = "rtsp://camuser:" + sentinelSourcePullPwd + "@10.0.0.9/stream1"
	if err := store.UpdateSource(src); err != nil {
		t.Fatalf("plant source credentials: %v", err)
	}

	st, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	st.Ingest.SRT.Passphrase = sentinelSetSRT
	st.Ingest.RTMP.StreamKey = sentinelSetRTMP
	st.Ingest.Pull.URL = "rtsp://camuser:" + sentinelSetPullPwd + "@10.0.0.9/stream1"
	st.Failover.Backup.SRT.Passphrase = sentinelBackupSRT
	st.Failover.Backup.RTMP.StreamKey = sentinelBackupRTMP
	st.Failover.Backup.Pull.URL = "rtsp://camuser:" + sentinelBackupPullPwd + "@10.0.0.9/stream1"
	// MQTT stays DISABLED, and that is the point rather than a convenience:
	// MQTTSettings.problems returns nil when it is off, so the validator's
	// explicit "no credentials in the broker URL" rule never runs and a URL
	// carrying userinfo can be stored and then round-tripped straight back out
	// of GET /settings. The masking has to cover that gap without leaning on
	// the validator.
	st.MQTT.Enabled = false
	st.MQTT.BrokerURL = "mqtt://mqttuser:" + sentinelMQTTPwd + "@broker.example:1883"
	// The automod model ENDPOINT, which the table called public because the
	// block also carries a derived hasApiKey boolean and the sealed key lives
	// in its own table. The endpoint is free text an operator pastes, and a
	// self-hosted or proxied inference endpoint most often arrives carrying the
	// key in the query string -- which reached a read token verbatim.
	st.Automod.Model.Endpoint = "https://llm.example/v1/chat/completions?api_key=" + sentinelAutomodKey
	if err := store.PutSettings(st); err != nil {
		t.Fatalf("plant settings credentials: %v", err)
	}

	dest, err := store.CreateDestination(&db.Destination{
		Name: "twitch", Kind: db.DestRTMP, URL: "rtmp://ingest.example/app",
		StreamKey:       sentinelDestKey,
		BackupURL:       "rtmp://backup.example/app",
		BackupStreamKey: sentinelDestBackupKey,
		// Expert mode. The resolved argv is why GET
		// /destinations/{id}/expert is 403 to a read token, and these are that
		// argv as the operator typed it, reachable through GET /destinations
		// by the same principal.
		ExtraOutputArgs: "-f flv rtmp://ingest.example/app/" + sentinelExpertArgs,
		ExtraInputArgs:  "-headers Authorization:Bearer\\ " + sentinelExpertArgs,
		AudioBitrate:    160, Enabled: false,
	})
	if err != nil {
		t.Fatalf("create destination: %v", err)
	}
	radio, err := store.CreateDestination(&db.Destination{
		Name: "radio", Kind: db.DestAudio,
		URL:          "icecast://source:" + sentinelIcecastPwd + "@radio.example:8000/live",
		AudioBitrate: 128, Enabled: false,
	})
	if err != nil {
		t.Fatalf("create audio destination: %v", err)
	}
	// The sweep and the round-trip tests address these by literal URL, because
	// the reconciliation below compares chi PATTERNS and a path built from a
	// variable is harder to read against the route table. Assert the ids rather
	// than assume them, so a fixture change that renumbers them fails here
	// instead of turning every /destinations/{id} assertion into a 404 nobody
	// reads carefully.
	// A FILE destination, whose url is a filename and not a URL at all. It is
	// here as the negative of everything else in this fixture: the sweep proves
	// no sentinel survives, and TestFileDestinationKeepsItsFilename proves this
	// one does. Without it, a redaction that replaced the whole field with
	// "[redacted]" -- which is what shipped -- looked like a clean sweep.
	file, err := store.CreateDestination(&db.Destination{
		Name: "archive", Kind: db.DestFile, URL: destFileName,
		AudioBitrate: 128, Enabled: false,
	})
	if err != nil {
		t.Fatalf("create file destination: %v", err)
	}
	if dest.ID != 1 || radio.ID != 2 || file.ID != 3 {
		t.Fatalf("fixture destinations are %d, %d and %d, but the tests address 1, 2 and 3",
			dest.ID, radio.ID, file.ID)
	}

	s := serverUnderTest(t, h)
	if _, err := s.playoutStore().save(playoutPublish{
		Protection: PlayoutProtectToken,
		Token:      sentinelPlayoutToken,
		Title:      "private",
	}); err != nil {
		t.Fatalf("plant playout token: %v", err)
	}
	// Public, so playoutURLsFor actually embeds the token in the three URLs. A
	// fixture that left it unpublished would exercise the branch where there is
	// nothing to leak.
	set := st.Playout
	set.Enabled = true
	set.Public = true
	st2, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	st2.Playout = set
	if err := store.PutSettings(st2); err != nil {
		t.Fatalf("enable playout: %v", err)
	}
	// GET /system answers from the ENGINE's settings snapshot rather than from
	// the store, so a fixture that only wrote the row would leave that route
	// serving the defaults and the sweep would pass over a URL that never had a
	// passphrase in it to begin with.
	if err := s.mgr.Reconcile(); err != nil {
		t.Fatalf("reconcile so the engine sees the planted settings: %v", err)
	}

	return h, store, sign
}

// bearer stamps a token onto a request the way a script would.
func bearer(tok string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
}

// leakRoutes are the GET routes a read-scoped token reaches in this fixture,
// each of which must come back 200 AND carry no sentinel.
//
// A 200 is REQUIRED, not merely accepted. A sweep that shrugged at a 404 would
// pass on a fixture where nothing was ever built, and would keep passing after
// a refactor silently broke the route it was meant to be watching. That is the
// fail-open bug this list's contract exists to refuse.
func leakRoutes() []string {
	return []string{
		"/api/v1/sources",
		"/api/v1/sources/1",
		"/api/v1/settings",
		"/api/v1/system",
		"/api/v1/status",
		"/api/v1/source",
		"/api/v1/destinations",
		"/api/v1/destinations/1",
		"/api/v1/destinations/2",
		"/api/v1/destinations/3",
		"/api/v1/playout",
		"/api/v1/failover/playlist",
		"/api/v1/processes",
		"/api/v1/metadata",
		"/api/v1/renditions",
		"/api/v1/encoders",
		"/api/v1/stats",
		"/api/v1/levels",
		"/api/v1/metrics",
		"/api/v1/version",
		"/api/v1/automod/matrix",
		"/api/v1/media",
		"/api/v1/recordings",
		"/api/v1/clips",
		"/api/v1/hooks",
		"/api/v1/alerts/rules",
		"/api/v1/platforms/credentials",
		"/api/v1/platforms/accounts",
		"/api/v1/platforms/capabilities",
		"/api/v1/platforms/guides",
		"/api/v1/platforms/presets",
		"/api/v1/library",
		"/api/v1/alerts/meta",
		"/api/v1/auth/me",
		"/api/v1/automod/stats",
		"/api/v1/chat",
		"/api/v1/chat/messages",
		"/api/v1/chat/search?q=hello",
		"/api/v1/chat/users?platform=twitch&authorId=1",
		"/api/v1/fonts",
		"/api/v1/hooks/meta",
		"/api/v1/loudness",
		"/api/v1/recordings/stems",
		"/api/v1/recordings/usage",
		"/api/v1/renditions/presets",
		"/api/v1/routing/presets",
		"/api/v1/schedules",
		"/api/v1/schedules/runs",
		"/api/v1/tls",
		"/api/v1/upgrade/plan",
	}
}

// TestReadTokenReceivesNoCredentialOnAnyRoute is the disclosure guard.
func TestReadTokenReceivesNoCredentialOnAnyRoute(t *testing.T) {
	h, _, sign := plantedServer(t)
	read := createScopedToken(t, h, sign, "monitoring", db.ScopeRead)

	for _, path := range leakRoutes() {
		r := jsonRequest(t, http.MethodGet, path, nil)
		bearer(read)(r)
		w := do(t, h, r)
		if w.Code != http.StatusOK {
			t.Errorf("GET %s returned %d to a read token; a route this sweep claims to "+
				"cover must actually answer, or the sweep is proving nothing about it: %s",
				path, w.Code, strings.TrimSpace(w.Body.String()))
			continue
		}
		body := w.Body.String()
		for _, secret := range allSentinels() {
			if strings.Contains(body, secret) {
				t.Errorf("GET %s handed a read-scoped token the credential %s.\n"+
					"body: %s", path, secret, body)
			}
		}
	}
}

// THE RECONCILIATION HALF NOW LIVES IN route_ledger_test.go, and this note is
// what stands where TestReadTokenSweepCoversEveryReachableGET used to.
//
// That test walked the router and required every GET pattern to be swept or
// excused, which was the right idea and three things short of enough:
//
//   - It asserted `len(walked) < 60` as its own sanity check, against a router
//     with 88 GET patterns. A floor 28 below reality would not have noticed
//     two-thirds of the API disappearing.
//   - It filtered to GET, leaving 123 method+pattern pairs outside the frame.
//   - Its excuses were bare strings. "needs a running child process" and "a
//     WebSocket upgrade; its frames are asserted in ws_test.go" -- the second
//     naming a file that does not exist in this package -- were between them
//     hiding a live stream key on three egresses.
//
// The ledger asserts EQUALITY over all 211 pairs, drives the NotFound and 405
// surfaces chi.Walk cannot see, and discharges an excuse only on a runtime proof
// with a differential positive control. leakRoutes() below is still the VALUE
// sweep and is still where a new read-reachable GET goes.

// patternOf resolves a concrete path back to the chi pattern it matches, so
// leakRoutes can stay readable as URLs while the reconciliation compares
// patterns.
func patternOf(t *testing.T, s *Server, path string) string {
	t.Helper()
	rctx := chi.NewRouteContext()
	// The sweep drives some routes with a query string, because the handler
	// refuses without one; chi matches on the path alone.
	path, _, _ = strings.Cut(path, "?")
	if !s.Handler().(chi.Routes).Match(rctx, http.MethodGet, path) {
		t.Fatalf("GET %s matches no route; the sweep is driving a URL that does not exist", path)
	}
	return rctx.RoutePattern()
}

// TestSessionAndAdminStillReceiveEveryCredential is the other half of the fix.
//
// Redaction that also blinded the console would be a regression wearing a
// security fix's clothes: the operator opens the Sources page precisely to copy
// the key into OBS, and an admin token can rotate every one of these secrets
// anyway, so withholding them from it is a lock with the key taped to the
// front. Both principals are asserted, because they take different code paths
// -- a session has no token at all, an admin has one whose scope is checked.
func TestSessionAndAdminStillReceiveEveryCredential(t *testing.T) {
	h, _, sign := plantedServer(t)
	admin := createScopedToken(t, h, sign, "deploy", db.ScopeAdmin)

	want := []struct {
		path   string
		secret string
	}{
		{"/api/v1/sources", sentinelSourceSRT},
		{"/api/v1/sources", sentinelSourceRTMP},
		{"/api/v1/sources/1", sentinelSourcePullPwd},
		{"/api/v1/settings", sentinelSetSRT},
		{"/api/v1/settings", sentinelSetRTMP},
		{"/api/v1/settings", sentinelBackupSRT},
		{"/api/v1/settings", sentinelBackupRTMP},
		{"/api/v1/settings", sentinelMQTTPwd},
		{"/api/v1/destinations", sentinelDestKey},
		{"/api/v1/destinations", sentinelDestBackupKey},
		{"/api/v1/destinations", sentinelIcecastPwd},
		{"/api/v1/destinations/1", sentinelDestKey},
		{"/api/v1/destinations/1", sentinelExpertArgs},
		{"/api/v1/settings", sentinelAutomodKey},
		{"/api/v1/playout", sentinelPlayoutToken},
	}

	for _, principal := range []struct {
		name string
		sign func(*http.Request)
	}{
		{"session", sign},
		{"admin token", bearer(admin)},
	} {
		for _, c := range want {
			r := jsonRequest(t, http.MethodGet, c.path, nil)
			principal.sign(r)
			w := do(t, h, r)
			if w.Code != http.StatusOK {
				t.Fatalf("%s: GET %s returned %d: %s",
					principal.name, c.path, w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), c.secret) {
				t.Errorf("%s was denied %s on GET %s; the redaction is meant to be "+
					"principal-dependent and this principal is entitled to it",
					principal.name, c.secret, c.path)
			}
		}
	}
}

// TestFileDestinationKeepsItsFilename is the other direction of the redaction,
// and it is the one the sweep can never assert.
//
// Every other test in this file asks whether a secret got OUT. This asks
// whether something that was never a secret got DESTROYED, and the answer
// shipped as no. Destination.url is a URL for kind rtmp and srt and a FILENAME
// for kind:file and for the file form of kind:audio, and readSafeDestination
// ran the whole field through alerts.RedactURL. That function is deliberately
// conservative about strings it cannot parse -- right for a log line, and here
// it turned "shows/monday-night-9f3a.mp4" into the bare word "[redacted]". A
// read-only console showed a file destination with no filename.
//
// It is also a shape the drift table cannot catch: the leaf is classified
// sMasked and it WAS masked. Nothing about the classification is wrong. The
// question the table does not ask is whether what came out is still useful,
// and that has to be asserted with a value.
func TestFileDestinationKeepsItsFilename(t *testing.T) {
	h, _, sign := plantedServer(t)
	read := createScopedToken(t, h, sign, "monitoring", db.ScopeRead)
	admin := createScopedToken(t, h, sign, "deploy", db.ScopeAdmin)

	for _, principal := range []struct {
		name string
		sign func(*http.Request)
	}{
		{"read token", bearer(read)},
		{"admin token", bearer(admin)},
		{"session", sign},
	} {
		for _, path := range []string{"/api/v1/destinations", "/api/v1/destinations/3"} {
			r := jsonRequest(t, http.MethodGet, path, nil)
			principal.sign(r)
			w := do(t, h, r)
			if w.Code != http.StatusOK {
				t.Fatalf("%s: GET %s returned %d: %s",
					principal.name, path, w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), destFileName) {
				t.Errorf("%s: GET %s lost the file destination's filename %q. "+
					"Masking a filename does not protect anything; it deletes the field.\n"+
					"body: %s", principal.name, path, destFileName, w.Body.String())
			}
		}
	}
}

// TestSystemIngestURLIsMaskedForReadTokensOnly pins the one CONSTRUCTED
// credential, which no struct tag could ever have covered: PublicIngestURL
// builds srt://host:port?...&passphrase=<cleartext> out of settings that are
// themselves redacted elsewhere.
func TestSystemIngestURLIsMaskedForReadTokensOnly(t *testing.T) {
	h, store, sign := plantedServer(t)

	st, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	st.Ingest.Mode = db.IngestSRT
	if err := store.PutSettings(st); err != nil {
		t.Fatalf("choose SRT: %v", err)
	}

	ingestURL := func(sign func(*http.Request)) string {
		t.Helper()
		r := jsonRequest(t, http.MethodGet, "/api/v1/system", nil)
		sign(r)
		w := do(t, h, r)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /system: %d %s", w.Code, w.Body.String())
		}
		var body struct {
			IngestURL string `json:"ingestUrl"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode /system: %v", err)
		}
		return body.IngestURL
	}

	// The passphrase in this URL comes from the SOURCE's ingest block, not the
	// settings singleton: effectiveSettings overlays the source's ingest onto
	// the install-wide document, and that is the value the listener enforces.
	operator := ingestURL(sign)
	if !strings.Contains(operator, sentinelSourceSRT) {
		t.Fatalf("the operator's own ingest URL lost its passphrase, so this test "+
			"cannot prove anything about the read token's: %q", operator)
	}

	read := createScopedToken(t, h, sign, "monitoring", db.ScopeRead)
	masked := ingestURL(bearer(read))
	if strings.Contains(masked, sentinelSourceSRT) {
		t.Errorf("GET /system handed a read token the SRT passphrase in its ingest URL: %q", masked)
	}
	// Still useful: the point of masking rather than blanking is that a
	// monitoring script can still see WHICH endpoint is meant.
	if !strings.HasPrefix(masked, "srt://") {
		t.Errorf("the masked ingest URL is no longer recognisable as an endpoint: %q", masked)
	}
}

// TestPrincipalVaryingResponsesAreNotCacheable.
//
// These bodies now differ by principal on the SAME url, which is a new property
// and one a shared cache in front of the server gets wrong in the worst
// direction: a session's response, stored under the URL alone, replayed to the
// read token the redaction exists for.
func TestPrincipalVaryingResponsesAreNotCacheable(t *testing.T) {
	h, _, sign := plantedServer(t)

	for _, path := range []string{
		"/api/v1/sources", "/api/v1/sources/1", "/api/v1/settings",
		"/api/v1/system", "/api/v1/destinations", "/api/v1/destinations/1",
		"/api/v1/playout",
	} {
		r := jsonRequest(t, http.MethodGet, path, nil)
		sign(r)
		w := do(t, h, r)
		if got := w.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
			t.Errorf("GET %s: Cache-Control = %q, want no-store — this body depends on "+
				"who asked", path, got)
		}
		// BOTH headers a principal can arrive on. Authorization was asserted
		// from the start; Cookie was not, and it is the one that carries the
		// SESSION -- the principal that receives the unredacted body, and so
		// the one response that must never be replayed to anybody else. A cache
		// keyed on Authorization alone files the signed-in operator's settings
		// blob under the same key as every anonymous caller's.
		for _, header := range []string{"Authorization", "Cookie"} {
			if got := w.Header().Values("Vary"); !containsFold(got, header) {
				t.Errorf("GET %s: Vary = %v, want it to name %s — this body depends on "+
					"a principal that arrives in that header", path, got, header)
			}
		}
	}
}

func containsFold(hay []string, needle string) bool {
	for _, h := range hay {
		for _, part := range strings.Split(h, ",") {
			if strings.EqualFold(strings.TrimSpace(part), needle) {
				return true
			}
		}
	}
	return false
}
