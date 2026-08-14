package multitrack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// operatorKey is the key an operator typed into a destination row.
const operatorKey = "live_123456789_OperatorTypedThisOne"

// mintedKey is a SYNTHETIC stand-in for the 312-character credential Twitch
// mints on a successful negotiation.
//
// Synthetic on purpose and it cannot be otherwise: the captured fixture
// testdata/negotiated-one-rendition.json has its `authentication` emptied,
// because a real minted key is a live credential and committing one would put
// a working stream key in the repository -- which is the thing #310 and #324
// were about. So the SHAPE is reproduced from the measurement written down in
// IngestEndpoint.Authentication -- v1_<64 hex>_<8 hex>_<hex manifest>_<the
// original key> -- and the property under test is structural: whatever came
// back in that field is what gets published, and the operator's own key is not.
// A test built on the real value would assert the same thing.
var mintedKey = "v1_" + strings.Repeat("a1b2c3d4", 8) + "_deadbeef_" +
	"7b2276223a312c2262223a343832307d_" + operatorKey

// gpuAsk is an Ask that will actually reach the network: Negotiate short
// circuits an Ask with no GPU without making the call at all, so every test of
// the request path has to supply one.
func gpuAsk() Ask {
	return Ask{
		Version:  "test",
		VODAudio: true,
		Canvas: Canvas{Width: 1920, Height: 1080, CanvasWidth: 1920, CanvasHeight: 1080,
			Framerate: Framerate{Numerator: 30, Denominator: 1}},
		Hardware: Capabilities{GPU: []GPU{{
			Model: "NVIDIA GeForce RTX 4080", VendorID: 4318, DeviceID: 9988,
			DedicatedVideoMemory: 16 * 1024 * 1024 * 1024,
		}}},
	}
}

// serveFixture answers every request with the named testdata file, and records
// the body it was sent so a test can assert on what went out.
func serveFixture(t *testing.T, name string, mutate func(map[string]any)) (*Client, *[]byte) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	if mutate != nil {
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("fixture %s is not JSON: %v", name, err)
		}
		mutate(doc)
		if raw, err = json.Marshal(doc); err != nil {
			t.Fatalf("re-encode fixture: %v", err)
		}
	}
	var sent []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		sent = b
		w.Header().Set("Content-Type", "application/json")
		// 200 on every path, including the refusal, because that is what the
		// live endpoint does and it is the whole hazard this package exists for.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
	}))
	t.Cleanup(srv.Close)
	return &Client{BaseURL: srv.URL}, &sent
}

// withMintedKey puts a minted credential into both ingest endpoints of the
// negotiated fixture, which ships with the field emptied.
func withMintedKey(doc map[string]any) {
	for _, e := range doc["ingest_endpoints"].([]any) {
		e.(map[string]any)["authentication"] = mintedKey
	}
}

// TestASuccessfulNegotiationPublishesWithTheMintedKey is the most important
// assertion in this package, and it is about a failure that WORKS.
//
// On success Twitch mints a new stream key carrying the agreed ladder signed
// inside it, ending with the operator's original. Publishing with the
// operator's own key instead does not fail loudly: it CONNECTS, and sends a
// ladder the ingest never agreed to. So "did it publish?" cannot tell the two
// apart, and neither can a test that only checks the hostname moved. The
// assertion has to be that the minted value specifically is what came out, and
// that the operator's own key is not the whole of it.
//
// MUTATION: Resolve's `if ep.Authentication != ""` disabled, so the minted key
// is ignored and the operator's own is published. Observed: FAIL, "published
// key is neither the minted key nor the operator's:
// \"live_123456789_OperatorTypedThisOne?clientConfigId=...\"" -- the config id
// is still appended, which is precisely why the assertion checks the signed
// v1_ prefix rather than equality with the operator's key. Restored from /tmp
// backup; `git diff --stat` clean.
func TestASuccessfulNegotiationPublishesWithTheMintedKey(t *testing.T) {
	c, _ := serveFixture(t, "negotiated-one-rendition.json", withMintedKey)

	out := Negotiate(context.Background(), c, operatorKey, gpuAsk())

	if !out.Use {
		t.Fatalf("a negotiated configuration was not used; note: %s", out.Note)
	}
	if out.Verdict != Negotiated {
		t.Errorf("verdict = %q, want %q", out.Verdict, Negotiated)
	}
	if !strings.HasPrefix(out.Target.Key, mintedKey) {
		if out.Target.Key == operatorKey || strings.HasPrefix(operatorKey, out.Target.Key) {
			t.Fatalf("published key is the operator's own, not the minted one: %q", out.Target.Key)
		}
		t.Fatalf("published key is neither the minted key nor the operator's: %q", out.Target.Key)
	}
	// The minted key ENDS with the operator's own, so a test asserting only
	// "contains the operator key" would pass on the wrong value too. What
	// separates them is the signed prefix.
	if !strings.HasPrefix(out.Target.Key, "v1_") {
		t.Errorf("published key has lost its signature prefix: %q", out.Target.Key)
	}
	// The config id travels to the ingest on the key, which is how Twitch knows
	// which negotiated ladder is arriving.
	if !strings.Contains(out.Target.Key, "49456f79-a985-4011-941f-3cde9897a0c6") {
		t.Errorf("the negotiated config id is not on the published key: %q", out.Target.Key)
	}
	// And the endpoint is the OTHER host -- not live.twitch.tv.
	if !strings.Contains(out.Target.URL, "global-contribute") {
		t.Errorf("target URL is not the multitrack ingest: %q", out.Target.URL)
	}
}

// TestAGPULessHostFallsBackQuietlyAndNeverCallsTwitch is the majority install:
// a rented VPS with no GPU. It must fall back, must not look like a fault, and
// must not spend a network round trip at go-live to be told what is already
// measured.
//
// The "never called" half is the load-bearing one. A test that only checked
// Use==false would pass on an implementation that made the call, waited for the
// timeout, and fell back -- which is the same answer arrived at slowly, on the
// path between the operator pressing go-live and anything reaching a viewer.
//
// MUTATION: the `len(a.Hardware.GPU) == 0` short circuit deleted from
// Negotiate. Observed: FAIL, "a GPU-less host still called Twitch". Restored
// from /tmp backup; `git diff --stat` clean.
func TestAGPULessHostFallsBackQuietlyAndNeverCallsTwitch(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	ask := gpuAsk()
	ask.Hardware.GPU = nil // the VPS

	out := Negotiate(context.Background(), &Client{BaseURL: srv.URL}, operatorKey, ask)

	if called {
		t.Error("a GPU-less host still called Twitch; the answer is already measured and the call costs go-live latency")
	}
	if out.Use {
		t.Error("a GPU-less host was told to publish to the multitrack ingest")
	}
	if out.Target.Key != "" || out.Target.URL != "" {
		t.Errorf("a fallback outcome carries a target: %+v", out.Target)
	}
	if out.Note == "" {
		t.Fatal("the fallback said nothing at all; the operator has to be told which ingest is in use")
	}
	// The wording is the requirement, not decoration: this is the normal path
	// and must not read as a fault.
	for _, forbidden := range []string{"error", "failed", "fault", "invalid"} {
		if strings.Contains(strings.ToLower(out.Note), forbidden) {
			t.Errorf("the ordinary GPU-less fallback note reads as a fault (%q): %s", forbidden, out.Note)
		}
	}
}

// TestARefusalIsATwoHundredAndFallsBack is the hazard the whole package exists
// for: Twitch answers a refusal with HTTP 200 and puts the verdict in
// status.result. A client that read the status code would publish to a
// configuration it was refused.
//
// MUTATION: Config.Verdict's StatusError case changed to fall through to
// Negotiated. Observed: FAIL, "a refused configuration was used". Restored from
// /tmp backup; `git diff --stat` clean.
func TestARefusalIsATwoHundredAndFallsBack(t *testing.T) {
	c, _ := serveFixture(t, "refused-no-gpu.json", nil)

	out := Negotiate(context.Background(), c, operatorKey, gpuAsk())

	if out.Use {
		t.Fatal("a refused configuration was used; the HTTP status was 200 and the refusal is in status.result")
	}
	if out.Verdict != Refused {
		t.Errorf("verdict = %q, want %q", out.Verdict, Refused)
	}
	// Twitch's own explanation has to reach the operator -- it is the only
	// statement of why that exists.
	if !strings.Contains(out.Note, "GPU") {
		t.Errorf("the refusal note does not carry Twitch's explanation: %s", out.Note)
	}
	if strings.Contains(out.Note, operatorKey) {
		t.Error("the operator's stream key is in the note")
	}
}

// TestNoOutcomeEverCarriesTheStreamKeyInItsNote is the security guard, and it is
// swept across every path rather than asserted on one: a note is built from
// Twitch's own text, which QUOTES REQUEST FIELDS BACK, and the request carries
// the stream key. That is the shape of leak #310 and #324 were.
//
// MUTATION: the transport-error branch changed to append the target URL and key
// to the note. Observed: FAIL on the "transport failure" subcase, "note carries
// the stream key". Restored from /tmp backup; `git diff --stat` clean.
func TestNoOutcomeEverCarriesTheStreamKeyInItsNote(t *testing.T) {
	// A server that echoes the key back inside Twitch's own explanation field,
	// which is exactly what the live endpoint does.
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"meta":{"service":"IVS"},"status":{"result":"error",` +
			`"html_en_us":"Your key ` + operatorKey + ` was rejected"},` +
			`"encoder_configurations":[],"audio_configurations":{}}`))
	}))
	t.Cleanup(echo.Close)

	// A server that is not there at all, for the transport-error path where the
	// *url.Error carries the full URL.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	for _, tc := range []struct {
		name string
		c    *Client
	}{
		{"twitch echoes the key back", &Client{BaseURL: echo.URL}},
		{"transport failure", &Client{BaseURL: deadURL}},
		{"malformed response", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.c
			if c == nil {
				bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"meta":`))
				}))
				t.Cleanup(bad.Close)
				c = &Client{BaseURL: bad.URL}
			}
			out := Negotiate(context.Background(), c, operatorKey, gpuAsk())
			if out.Use {
				t.Error("a broken negotiation was used")
			}
			if out.Note == "" {
				t.Fatal("no note")
			}
			if strings.Contains(out.Note, operatorKey) {
				t.Errorf("note carries the stream key: %s", out.Note)
			}
		})
	}
}

// TestTheRequestAsksForTheVODTrack pins the one preference the whole feature
// turns on. Twitch populates audio_configurations.vod only when it is asked to,
// so a request that quietly dropped this would negotiate successfully, publish
// happily, and carry one audio track.
//
// MUTATION: NewRequest's `VODTrackAudio: a.VODAudio` changed to a literal
// false. Observed: FAIL, "the request did not ask for the VOD audio track".
// Restored from /tmp backup; `git diff --stat` clean.
func TestTheRequestAsksForTheVODTrack(t *testing.T) {
	c, sent := serveFixture(t, "negotiated-one-rendition.json", withMintedKey)

	out := Negotiate(context.Background(), c, operatorKey, gpuAsk())
	if !out.Use {
		t.Fatalf("negotiation did not succeed: %s", out.Note)
	}
	if len(*sent) == 0 {
		t.Fatal("no request body was captured, so this test is asserting nothing")
	}

	var body map[string]any
	if err := json.Unmarshal(*sent, &body); err != nil {
		t.Fatalf("request body is not JSON: %v\n%s", err, *sent)
	}
	prefs, ok := body["preferences"].(map[string]any)
	if !ok {
		t.Fatalf("no preferences in the request: %s", *sent)
	}
	if vod, _ := prefs["vod_track_audio"].(bool); !vod {
		t.Errorf("the request did not ask for the VOD audio track: %v", prefs["vod_track_audio"])
	}
	// And the key really did travel, or the fixture proves nothing about auth.
	if body["authentication"] != operatorKey {
		t.Errorf("authentication = %v, want the operator's stream key", body["authentication"])
	}
}
