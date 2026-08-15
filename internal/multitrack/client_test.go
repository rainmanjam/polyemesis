package multitrack

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadFixture reads a response body captured VERBATIM from the live endpoint.
//
// Both fixtures in testdata were produced by an actual POST to
// ingest.twitch.tv on 2026-08-13 and pasted unedited; the config_id values are
// the ones Twitch minted for those calls. That provenance is the only thing
// that makes them worth asserting against -- a fixture somebody wrote by hand
// from a struct definition proves the struct definition.
func loadFixture(t *testing.T, name string) *Config {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
	return &cfg
}

// serve stands up a stub at Client.BaseURL. It returns the request body the
// client sent, so a test can assert on what went out as well as what came back.
func serve(t *testing.T, status int, body string) (*Client, *[]byte) {
	t.Helper()
	var sent []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = readAll(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &Client{HTTP: srv.Client(), BaseURL: srv.URL}, &sent
}

func readAll(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1024)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			return buf, nil
		}
	}
}

// TestAnHTTP200CarryingAStatusErrorIsARefusal is the central claim of this
// package: the HTTP status code is not the verdict.
//
// The body is the no-GPU refusal captured from the live endpoint, served with
// the 200 Twitch really sends. A client that looked at the status code would
// call this a success and publish to a configuration with nothing in it.
//
// Proven able to fail against the committed tree by changing the StatusError
// case in Config.Verdict (client.go) from `return Refused, ...` to
// `verdict, advice = Advisory, ...`, which made the test report
// "verdict = advisory, want refused". Restored from a /tmp copy; git diff
// --stat clean.
func TestAnHTTP200CarryingAStatusErrorIsARefusal(t *testing.T) {
	cfg := loadFixture(t, "refused-no-gpu.json")

	// The premise. If the fixture ever stops being a 200-shaped refusal this
	// test is asserting about something else, and would go on passing.
	if cfg.Status == nil || cfg.Status.Result != StatusError {
		t.Fatalf("fixture is not a status-error response: %+v", cfg.Status)
	}

	verdict, advice := cfg.Verdict()
	if verdict != Refused {
		t.Errorf("verdict = %s, want %s", verdict, Refused)
	}
	// The refusal has to carry Twitch's own sentence, because it is the only
	// explanation of the refusal that exists -- there is no error code.
	if !strings.Contains(advice, "did not send GPU Information") {
		t.Errorf("advice does not quote Twitch's reason: %q", advice)
	}
}

// TestAConfigWithNoRenditionsIsRefusedWhateverTheStatusSays covers the case the
// status field cannot: a response that says nothing wrong and contains nothing
// to publish. Without this, "status absent means success" would be the last
// word, and an empty ladder would be Negotiated.
//
// Proven able to fail against the committed tree by deleting the
// `if len(c.EncoderConfigurations) == 0` block from Config.Verdict
// (client.go), which made the test report "verdict = negotiated, want refused"
// for the no-status subtest. Restored from a /tmp copy; git diff --stat clean.
func TestAConfigWithNoRenditionsIsRefusedWhateverTheStatusSays(t *testing.T) {
	live := []AudioEncoderConfig{{Codec: "aac", TrackID: 0, Channels: 2}}
	rendition := []VideoEncoderConfig{{Type: "obs_nvenc_h264_tex", Width: 1920, Height: 1080}}

	for _, tc := range []struct {
		name   string
		cfg    Config
		want   Verdict
		detail string
	}{
		{
			name: "no status and no renditions",
			cfg: Config{
				AudioConfigurations: AudioConfigurations{Live: live},
			},
			want:   Refused,
			detail: "no video renditions",
		},
		{
			name: "explicit success but no renditions",
			cfg: Config{
				Status:              &Status{Result: StatusSuccess},
				AudioConfigurations: AudioConfigurations{Live: live},
			},
			want:   Refused,
			detail: "no video renditions",
		},
		{
			name: "renditions but no live audio track",
			cfg: Config{
				EncoderConfigurations: rendition,
			},
			want:   Refused,
			detail: "no live audio track",
		},
		{
			name: "a warning with a usable ladder is advisory, not fatal",
			cfg: Config{
				Status:                &Status{Result: StatusWarning, HTMLEnUS: "your driver is old"},
				EncoderConfigurations: rendition,
				AudioConfigurations:   AudioConfigurations{Live: live},
			},
			want:   Advisory,
			detail: "your driver is old",
		},
		{
			name: "a status this build does not know is advisory, not fatal",
			cfg: Config{
				Status:                &Status{Result: "someNewThing"},
				EncoderConfigurations: rendition,
				AudioConfigurations:   AudioConfigurations{Live: live},
			},
			want:   Advisory,
			detail: "someNewThing",
		},
		{
			name: "a complete configuration is negotiated with nothing to say",
			cfg: Config{
				EncoderConfigurations: rendition,
				AudioConfigurations:   AudioConfigurations{Live: live},
			},
			want: Negotiated,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verdict, advice := tc.cfg.Verdict()
			if verdict != tc.want {
				t.Errorf("verdict = %s, want %s (advice %q)", verdict, tc.want, advice)
			}
			if tc.detail == "" {
				if advice != "" {
					t.Errorf("advice = %q, want empty for a clean negotiation", advice)
				}
				return
			}
			if !strings.Contains(advice, tc.detail) {
				t.Errorf("advice = %q, want it to mention %q", advice, tc.detail)
			}
		})
	}
}

// TestTheStreamKeyGoesInTheBodyAndNeverInTheURL pins the one placement decision
// that would be invisible if it were wrong. A key in a query string reaches
// every proxy log between here and Twitch, and reaches OUR logs too, because
// *url.Error carries the request URL -- which is exactly how a key got into
// server.log in #310.
//
// Proven able to fail against the committed tree by changing Client.Fetch
// (client.go) to build the request against `c.url()+"?authentication="+
// streamKey`, which made the test report "the request URL carries the stream
// key". Restored from a /tmp copy; git diff --stat clean.
func TestTheStreamKeyGoesInTheBodyAndNeverInTheURL(t *testing.T) {
	const key = "live_424242_thisisthekeyandmustnotescape"

	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"meta":{},"encoder_configurations":[],"audio_configurations":{"live":[]}}`))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), BaseURL: srv.URL}

	if _, err := c.Fetch(context.Background(), key, NewRequest(Ask{Version: "test"})); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if strings.Contains(gotURL, key) {
		t.Errorf("the request URL carries the stream key: %q", gotURL)
	}
	if strings.Contains(gotURL, "authentication") {
		t.Errorf("the request URL carries an authentication parameter: %q", gotURL)
	}
}

// TestTheStreamKeyIsScrubbedFromEveryErrorFetchCanReturn walks each error path
// rather than sampling one, because the leak in #310 was on a path nobody had
// walked. A path added later without a scrub fails here.
//
// Proven able to fail against the committed tree by removing the `scrub(...)`
// wrapper from the transport-error return in Client.Fetch (client.go) -- the
// `unreachable host` subtest then reported "error text contains the stream
// key", because *url.Error had rendered the whole URL including the key the
// mutated code had put there. Restored from a /tmp copy; git diff --stat clean.
func TestTheStreamKeyIsScrubbedFromEveryErrorFetchCanReturn(t *testing.T) {
	const key = "live_999_averydistinctivestreamkeyvalue"

	for _, tc := range []struct {
		name string
		// build returns a client whose Fetch will fail, and it is given the key
		// so a stub can plant it in whatever it sends back.
		build func(t *testing.T) *Client
	}{
		{
			name: "a 5xx whose body quotes the key back",
			build: func(t *testing.T) *Client {
				c, _ := serve(t, http.StatusInternalServerError,
					`{"error":"we could not handle authentication `+key+`"}`)
				return c
			},
		},
		{
			name: "a 200 whose body is not JSON at all",
			build: func(t *testing.T) *Client {
				c, _ := serve(t, http.StatusOK, `<html>nope `+key+`</html>`)
				return c
			},
		},
		{
			name: "an unreachable host",
			build: func(t *testing.T) *Client {
				// A server that is closed before use, so Do fails in the
				// transport and returns a *url.Error carrying the URL.
				srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
				client := srv.Client()
				base := srv.URL
				srv.Close()
				return &Client{HTTP: client, BaseURL: base}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.build(t)
			_, err := c.Fetch(context.Background(), key, NewRequest(Ask{Version: "test"}))
			if err == nil {
				t.Fatal("Fetch succeeded; this case is supposed to fail, so the test below proves nothing")
			}
			if strings.Contains(err.Error(), key) {
				t.Errorf("error text contains the stream key: %v", err)
			}
			if !strings.Contains(err.Error(), redactedPlaceholder) &&
				strings.Contains(tc.name, "quotes the key back") {
				t.Errorf("the key was removed but left no trace it had been there: %v", err)
			}
		})
	}
}

// TestRedactedRemovesTheKeyTwitchSendsBackWithoutTouchingTheOriginal is the
// other half of the leak defence. The response carries a key too, and the
// obvious way to log a config -- marshal it -- publishes it.
//
// The aliasing half matters as much as the redaction half: a Redacted that
// shared the endpoint slice would blank the key in the config the caller is
// about to publish with, turning a logging call into a broadcast failure.
//
// Proven able to fail against the committed tree by replacing the
// make+copy of out.IngestEndpoints in Config.Redacted (multitrack.go) with
// `out.IngestEndpoints = c.IngestEndpoints`, which made the test report
// "Redacted() reached back and blanked the caller's config". Restored from a
// /tmp copy; git diff --stat clean.
func TestRedactedRemovesTheKeyTwitchSendsBackWithoutTouchingTheOriginal(t *testing.T) {
	const minted = "v1_sig_manifesthex_live_424242_theoriginalkey"
	cfg := &Config{
		Status: &Status{Result: StatusError, HTMLEnUS: "no"},
		IngestEndpoints: []IngestEndpoint{
			{Protocol: "RTMPS", URLTemplate: "rtmps://h/app/{stream_key}", Authentication: minted},
		},
	}

	red := cfg.Redacted()
	if red.IngestEndpoints[0].Authentication != redactedPlaceholder {
		t.Errorf("Redacted() left the key in place: %q", red.IngestEndpoints[0].Authentication)
	}
	if cfg.IngestEndpoints[0].Authentication != minted {
		t.Error("Redacted() reached back and blanked the caller's config")
	}

	// The realistic failure is not reading the field, it is marshalling the
	// whole thing into a log line, so assert on that.
	blob, err := json.Marshal(red)
	if err != nil {
		t.Fatalf("marshal redacted config: %v", err)
	}
	if strings.Contains(string(blob), minted) {
		t.Errorf("a marshalled redacted config still contains the key: %s", blob)
	}
}

// TestFetchAlwaysSendsTheServiceAndSchemaVersionTwitchRequires guards the two
// fields whose absence Twitch answers with a refusal naming them. They are set
// by Fetch rather than trusted from the caller's Request, so a caller that
// builds a Request by hand cannot omit them.
//
// Proven able to fail against the committed tree by deleting the
// `req.SchemaVersion = SchemaVersion` line from Client.Fetch (client.go),
// which made the test report `schema_version = "", want "2025-01-25"`.
// Restored from a /tmp copy; git diff --stat clean.
func TestFetchAlwaysSendsTheServiceAndSchemaVersionTwitchRequires(t *testing.T) {
	c, sent := serve(t, http.StatusOK,
		`{"meta":{},"encoder_configurations":[],"audio_configurations":{"live":[]}}`)

	// A Request built by hand with both fields deliberately wrong.
	req := Request{Service: "WRONG", SchemaVersion: "1999-01-01"}
	if _, err := c.Fetch(context.Background(), "k", req); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	var body struct {
		Service        string `json:"service"`
		SchemaVersion  string `json:"schema_version"`
		Authentication string `json:"authentication"`
	}
	if err := json.Unmarshal(*sent, &body); err != nil {
		t.Fatalf("decode what the client sent: %v", err)
	}
	if body.Service != ServiceIVS {
		t.Errorf("service = %q, want %q", body.Service, ServiceIVS)
	}
	if body.SchemaVersion != SchemaVersion {
		t.Errorf("schema_version = %q, want %q", body.SchemaVersion, SchemaVersion)
	}
	if body.Authentication != "k" {
		t.Errorf("authentication = %q, want the stream key Fetch was given", body.Authentication)
	}
}

// TestTheLiveFixtureDecodesIntoEveryFieldTheFeatureDependsOn asserts the
// negotiated fixture parses into the values the rest of the package acts on. It
// is the one test that would catch a struct tag typo, which is otherwise
// silent: a mistyped tag yields a zero value, not an error.
//
// Proven able to fail against the committed tree by changing the json tag on
// AudioConfigurations.VOD (multitrack.go) from `vod` to `vod_tracks`, which
// made the test report "VOD audio tracks = 0, want 1". Restored from a /tmp
// copy; git diff --stat clean.
func TestTheLiveFixtureDecodesIntoEveryFieldTheFeatureDependsOn(t *testing.T) {
	cfg := loadFixture(t, "negotiated-one-rendition.json")

	if got := len(cfg.EncoderConfigurations); got != 1 {
		t.Fatalf("renditions = %d, want 1", got)
	}
	if got := len(cfg.AudioConfigurations.Live); got != 1 {
		t.Fatalf("live audio tracks = %d, want 1", got)
	}
	// The whole feature. One video track, TWO audio tracks, the second marked
	// for VOD -- which is the configuration issue #326 recorded as not known to
	// be obtainable.
	if got := len(cfg.AudioConfigurations.VOD); got != 1 {
		t.Fatalf("VOD audio tracks = %d, want 1", got)
	}
	if got, want := cfg.AudioConfigurations.Live[0].TrackID, uint32(0); got != want {
		t.Errorf("live track id = %d, want %d", got, want)
	}
	if got, want := cfg.AudioConfigurations.VOD[0].TrackID, uint32(1); got != want {
		t.Errorf("VOD track id = %d, want %d", got, want)
	}
	if got, ok := cfg.AudioConfigurations.VOD[0].BitrateKbps(); !ok || got != 160 {
		t.Errorf("VOD bitrate = %d (ok=%v), want 160", got, ok)
	}
	if got, ok := cfg.EncoderConfigurations[0].BitrateKbps(); !ok || got != 6000 {
		t.Errorf("video bitrate = %d (ok=%v), want 6000", got, ok)
	}
	if cfg.EncoderConfigurations[0].Framerate == nil ||
		cfg.EncoderConfigurations[0].Framerate.Numerator != 30 {
		t.Errorf("framerate = %+v, want 30/1", cfg.EncoderConfigurations[0].Framerate)
	}
	if cfg.Meta.ConfigID == "" {
		t.Error("config_id is empty; the publish could not be correlated to this negotiation")
	}
	if v, _ := cfg.Verdict(); v != Negotiated {
		t.Errorf("verdict = %s, want %s", v, Negotiated)
	}
}

// TestABodyWithAnUnexpectedInterpolationShapeStillDecodes covers the reason
// BitrateInterpolationPoints is a json.RawMessage. If it were []int, a shape
// change in one field nothing reads would fail the unmarshal of the whole
// config and lose the negotiation.
//
// Proven able to fail against the committed tree by changing
// VideoEncoderConfig.BitrateInterpolationPoints (multitrack.go) from
// json.RawMessage to []int, which made the test report "decode: json: cannot
// unmarshal object into Go struct field ... of type int". Restored from a /tmp
// copy; git diff --stat clean.
func TestABodyWithAnUnexpectedInterpolationShapeStillDecodes(t *testing.T) {
	body := `{"meta":{"config_id":"c"},
	  "ingest_endpoints":[{"protocol":"RTMPS","url_template":"rtmps://h/app/{stream_key}"}],
	  "encoder_configurations":[{"type":"x","width":1920,"height":1080,
	    "bitrate_interpolation_points":[{"at":0,"kbps":3960}],"settings":{"bitrate":6000}}],
	  "audio_configurations":{"live":[{"codec":"aac","track_id":0,"channels":2}],"vod":[]}}`

	var cfg Config
	if err := json.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v, _ := cfg.Verdict(); v != Negotiated {
		t.Errorf("verdict = %s, want %s -- an unreadable field lost the whole negotiation", v, Negotiated)
	}
}

// TestAContextCancellationIsReportedAsAFailureNotARefusal keeps the two
// outcomes apart. A refusal is Twitch's answer and is the operator's to read; a
// cancelled call is not an answer at all, and reporting it as a refusal would
// tell the operator that Twitch declined something it was never asked.
//
// Proven able to fail against the committed tree by changing Client.Fetch
// (client.go) to `return &Config{}, nil` on the transport error path, which
// made the test report "Fetch returned no error for a cancelled context".
// Restored from a /tmp copy; git diff --stat clean.
func TestAContextCancellationIsReportedAsAFailureNotARefusal(t *testing.T) {
	c, _ := serve(t, http.StatusOK, `{}`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cfg, err := c.Fetch(ctx, "k", NewRequest(Ask{Version: "test"}))
	if err == nil {
		t.Fatal("Fetch returned no error for a cancelled context")
	}
	if cfg != nil {
		t.Errorf("Fetch returned a config alongside its error: %+v", cfg)
	}
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("error does not identify the cancellation: %v", err)
	}
}

// TestFetchDoesNotFollowARedirectThatWouldReplayTheKeyToAnotherHost.
//
// net/http follows up to ten redirects by default and strips Authorization and
// Cookie when the hop crosses to another host -- which protects a client whose
// credential is in a HEADER and does nothing for this one. Fetch puts the
// stream key in the BODY, deliberately, so it stays out of URLs and proxy logs,
// and 307/308 preserve the method and REPLAY that body through
// Request.GetBody. So a single 307 from anything answering for
// ingest.twitch.tv delivers the operator's stream key to a host of its own
// choosing, with no interception and no downgrade.
//
// 301/302/303 are covered too, and they are expected NOT to leak -- net/http
// rewrites those to GET and drops the body. They are in the table so the test
// says which codes are dangerous and which are not, rather than asserting a
// uniform outcome that is not the real one.
//
// Mutation: delete `CheckRedirect: refuseRedirect` from defaultHTTP AND the
// `cp.CheckRedirect = refuseRedirect` line from Client.http (client.go).
// Observed to fail on the 307 and 308 subtests with "the far end received the
// stream key in a replayed request body (684 bytes)". 301/302/303 failed only
// on "the redirect was followed" and NOT on the key assertion -- which is the
// measurement, not a weaker test: net/http really does rewrite those to GET and
// drop the body, so only 307/308 carry the credential across. Restored with
// `command cp -f` from a file backup; `diff` against the backup reported
// IDENTICAL.
//
// Mutation: set CheckRedirect on defaultHTTP only, dropping the
// `cp.CheckRedirect = refuseRedirect` line from Client.http. Observed to fail
// exactly as above -- which is the point of reapplying the policy to an
// injected client, since every test in this file injects one and the version
// that trusted the seam would have been untestable.
func TestFetchDoesNotFollowARedirectThatWouldReplayTheKeyToAnotherHost(t *testing.T) {
	const key = "live_424242_thekeythatmustnotbereplayedelsewhere"

	for _, code := range []int{
		http.StatusMovedPermanently,  // 301
		http.StatusFound,             // 302
		http.StatusSeeOther,          // 303
		http.StatusTemporaryRedirect, // 307
		http.StatusPermanentRedirect, // 308
	} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			// The far end. A different host from the one Fetch was pointed at,
			// which is what makes this the cross-domain case net/http believes
			// it is already handling.
			var farEndBody []byte
			var farEndHit bool
			farEnd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				farEndHit = true
				farEndBody, _ = readAll(r)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"meta":{}}`))
			}))
			defer farEnd.Close()

			near := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, farEnd.URL, code)
			}))
			defer near.Close()

			c := &Client{HTTP: near.Client(), BaseURL: near.URL}
			_, err := c.Fetch(context.Background(), key, NewRequest(Ask{Version: "test"}))

			if strings.Contains(string(farEndBody), key) {
				t.Errorf("the far end received the stream key in a replayed request body (%d bytes). "+
					"net/http strips Authorization and Cookie across hosts; it has no notion of a "+
					"sensitive BODY, and %d preserves the method and replays one through GetBody",
					len(farEndBody), code)
			}
			if farEndHit {
				t.Errorf("the redirect was followed to a host the response chose")
			}
			// The 3xx is surfaced rather than swallowed: a caller has to be able
			// to tell "Twitch redirected us" from "Twitch negotiated", and
			// ErrUseLastResponse hands Fetch the 3xx to report.
			if err == nil {
				t.Error("Fetch reported success on a redirect it did not follow, so the caller " +
					"would publish against a configuration Twitch never sent")
			} else if !strings.Contains(err.Error(), "Twitch returned") {
				t.Errorf("error does not report the status Twitch returned: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), key) {
				t.Errorf("the redirect error carries the stream key: %v", err)
			}
		})
	}
}

// TestAKeyStraddlingTheSnippetBoundaryIsStillMasked.
//
// The call site read `scrub(snippet(raw), streamKey)` -- truncate at 300 bytes,
// THEN look for the key. A key that begins before offset 300 and ends after it
// is handed to scrub as a haystack shorter than the needle, so nothing matches
// and the surviving prefix goes straight into the error text. Worse than a
// partial leak: no placeholder appears anywhere in the line, so the output does
// not look redacted and reads as though the scrub had nothing to do.
//
// THIS IS #306's FAILURE MODE, in the newest code in the repository. There a
// 65-byte configured value was searched for inside a 56-byte printed one; here
// a whole key is searched for inside its own truncated prefix. The fix is the
// same shape: make the scrub see the untruncated text.
//
// Mutation: restore `scrub(snippet(raw), streamKey)` in Client.Fetch
// (client.go). Observed to fail with "30 bytes of the stream key survived into
// the error text ... and masked=false". Restored with `command cp -f`; `diff`
// against the backup reported IDENTICAL.
func TestAKeyStraddlingTheSnippetBoundaryIsStillMasked(t *testing.T) {
	// 55 bytes, placed so it starts at offset 270 and ends at 325 -- across the
	// 300-byte cut. The arithmetic is the test: at any other offset the old
	// code passes.
	const key = "live_424242_aStreamKeyLongEnoughToStraddleTheBoundary_x"
	const at = 270
	if len(key) != 55 {
		t.Fatalf("the fixture key is %d bytes, not 55: the offsets below are chosen so the key "+
			"straddles snippet's 300-byte cut, and this test proves nothing if it does not", len(key))
	}

	body := strings.Repeat("p", at) + key + strings.Repeat("q", 200)
	if at+len(key) <= 300 {
		t.Fatalf("the key ends at offset %d, before snippet's cut at 300: it does not straddle", at+len(key))
	}

	c, _ := serve(t, http.StatusInternalServerError, body)
	_, err := c.Fetch(context.Background(), key, NewRequest(Ask{Version: "test"}))
	if err == nil {
		t.Fatal("Fetch succeeded on a 500; this case is supposed to fail")
	}
	got := err.Error()

	if strings.Contains(got, key) {
		t.Errorf("the whole stream key is in the error text: %v", got)
	}
	// The prefix is the actual leak, and asserting only on the whole key would
	// miss it entirely -- which is how this shipped.
	for n := len(key); n >= 12; n-- {
		if strings.Contains(got, key[:n]) {
			t.Errorf("%d bytes of the stream key survived into the error text: %q ... and masked=%v, "+
				"so the line does not even look redacted", n, key[:n],
				strings.Contains(got, redactedPlaceholder))
			break
		}
	}
	if !strings.Contains(got, redactedPlaceholder) {
		t.Errorf("the error carries no placeholder, so nothing records that a key was removed: %v", got)
	}
	// Still bounded. The fix moves the truncation inside the scrub; it must not
	// remove it, or a megabyte of response body becomes a log line.
	if len(got) > 600 {
		t.Errorf("the error is %d bytes: scrubbing outermost dropped the bound rather than "+
			"reordering it", len(got))
	}
}
