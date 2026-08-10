package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/events"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// The recurrence guard for #150's argv disclosure.
//
// The credential travelled from db.Destination.ExtraInputArgs, through
// expertArgv, into the destination's spawn argv, and out through FOUR read-
// reachable egresses whose only masking was alerts.Redact applied PER ARGUMENT:
//
//	GET /api/v1/processes              processSummary -> CommandString
//	GET /api/v1/processes/{name}/logs  processDetail  -> CommandString AND Logs
//	the /ws TypeLog stream             appendLog -> bus -> writeEvent
//	GET /api/v1/status  + /ws TypeStatus + the retained MQTT topic
//	                                   Status.LastError
//
// Nothing saw it because /processes was swept against a fixture that started no
// destination, and the other three were EXCUSED -- "needs a running child
// process", "not an HTTP response body". Three excuses over one live credential.
//
// This test drives all of them against a destination that is actually running.

// TestRunningDestinationLeaksNoSentinelOnAnyEgress is the disclosure half.
func TestRunningDestinationLeaksNoSentinelOnAnyEgress(t *testing.T) {
	h, _, sign := runningDestServer(t)
	read := createScopedToken(t, h, sign, "monitoring", db.ScopeRead)
	s := serverUnderTest(t, h)

	// POSITIVE CONTROL FIRST, and it must come first.
	//
	// Every assertion below is "this sentinel is absent". Absence proves nothing
	// unless the credential was PRESENT to leak, and a fixture that quietly
	// failed to splice the expert arguments would pass the whole test while
	// asserting nothing at all -- which is the exact pathology, five instances
	// deep, that this PR exists to end. So: prove the argv really carries the
	// sentinels, through the one route that is entitled to show them.
	assertExpertShowsTheRawArgv(t, h, sign)

	egresses := []struct {
		name  string
		bytes func() string
	}{
		{"GET /api/v1/processes", func() string {
			return bodyOf(t, h, bearer(read), "/api/v1/processes")
		}},
		{"GET /api/v1/processes/dest:1/logs", func() string {
			return bodyOf(t, h, bearer(read), "/api/v1/processes/dest:1/logs")
		}},
		{"GET /api/v1/status", func() string {
			return bodyOf(t, h, bearer(read), "/api/v1/status")
		}},
		{"the /ws frames", func() string {
			return strings.Join(wsFrames(t, s, "Bearer "+read, 3*time.Second), "\n")
		}},
		{"the on-disk process.log", func() string {
			return processLogFile(t, s)
		}},
	}

	for _, e := range egresses {
		body := e.bytes()
		if strings.TrimSpace(body) == "" {
			t.Errorf("%s produced nothing to inspect; an empty egress cannot demonstrate "+
				"the absence of anything", e.name)
			continue
		}
		for _, secret := range argvSentinels() {
			if strings.Contains(body, secret) {
				t.Errorf("%s handed a read-scoped principal the credential %s.\nbody: %s",
					e.name, secret, truncateForFailure(body))
			}
		}
	}
}

// TestTheCommandFieldLeaksBeforeTheChildEverSpawns is the half the verifiers
// under-weighted and the judge corrected.
//
// currentArgs falls back to spec.Args when nothing has spawned yet, so
// CommandString renders the credential on a destination that merely EXISTS. The
// "command" field of BOTH /processes and /processes/{name}/logs is therefore
// reachable with no child, no stderr and no relay -- which means a fixture that
// only proved the log lines clean would have left half the leak covered by
// nothing.
//
// Driven against the ORIGINAL planted fixture, whose destinations are disabled
// and whose FFmpeg does not exist, precisely to show that the child is
// irrelevant to this half.
func TestTheCommandFieldLeaksBeforeTheChildEverSpawns(t *testing.T) {
	h, _, sign := plantedServer(t)
	read := createScopedToken(t, h, sign, "monitoring", db.ScopeRead)

	for _, path := range []string{"/api/v1/processes", "/api/v1/processes/playout:source/logs"} {
		body := bodyOf(t, h, bearer(read), path)
		for _, secret := range allSentinels() {
			if strings.Contains(body, secret) {
				t.Errorf("GET %s leaked %s from a command line with no running child",
					path, secret)
			}
		}
	}
}

// TestSecretSetIsExactNotHeuristic tables the FFmpeg credential idioms and, as
// its discriminator, asserts that alerts.Redact ALONE still fails the marked
// ones.
//
// The discriminator is the load-bearing half. Without it this suite could go
// green because somebody added a regex to the matcher, and the whole argument of
// the fix is that no set of regexes closes an open `-flag value` namespace. If
// Redact ever starts passing the rows marked redactAlonefails, that assertion
// fires and asks whoever changed it to say what they think they proved.
func TestSecretSetIsExactNotHeuristic(t *testing.T) {
	const secret = "SENTINEL-exactness-table-value-8b2e"

	cases := []struct {
		name string
		raw  string
		// redactAloneFails records whether the residual matcher, applied the way
		// the shipped code applied it, leaves the credential in the clear.
		redactAloneFails bool
	}{
		{"the backslash-escaped bearer header", `-headers Authorization:Bearer\ ` + secret, true},
		{"the rtmp connect parameter", "-rtmp_conn S:" + secret, true},
		{"an srt passphrase flag", "-passphrase " + secret, true},
		{"an arbitrary metadata value", "-metadata channelkey=" + secret, true},
		{"a bare positional value", secret, true},
		{"the canonical spaced bearer header", "-headers Authorization: Bearer " + secret, true},
		{"a publish url carrying the key as its last segment", "rtmp://h.example/app/" + secret, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			argv, err := ffmpeg.SplitArgs(c.raw)
			if err != nil {
				t.Fatalf("SplitArgs(%q): %v", c.raw, err)
			}
			set := alerts.NewSecretSet(nil, alerts.OpaqueArgvValues(argv)...)
			if set.Len() == 0 {
				t.Fatalf("the secret set built from %q is EMPTY, so the assertion below "+
					"would pass without masking anything", c.raw)
			}
			for _, got := range set.ScrubArgv(argv) {
				if strings.Contains(got, secret) {
					t.Errorf("the exact set left %s in the argv element %q", secret, got)
				}
			}

			// The discriminator.
			perArgument := ""
			for _, a := range argv {
				perArgument += alerts.Redact(a) + " "
			}
			leaked := strings.Contains(perArgument, secret)
			if leaked != c.redactAloneFails {
				t.Errorf("alerts.Redact applied per argument leaked=%v, want %v.\n"+
					"raw:    %q\nmasked: %q\n"+
					"If this now masks the credential, the matcher has grown a rule. That "+
					"does not make it a boundary -- FFmpeg's flag namespace is open and the "+
					"next idiom is still unmatched -- so update this row deliberately rather "+
					"than to make the suite green.",
					leaked, c.redactAloneFails, c.raw, perArgument)
			}
		})
	}
}

// ------------------------------------------------------------------ helpers

func bodyOf(t *testing.T, h http.Handler, sign func(*http.Request), path string) string {
	t.Helper()
	r := jsonRequest(t, http.MethodGet, path, nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Errorf("GET %s returned %d to a read token; an egress this guard claims to "+
			"cover must actually answer: %s", path, w.Code, strings.TrimSpace(w.Body.String()))
		return ""
	}
	return w.Body.String()
}

// assertExpertShowsTheRawArgv is the differential positive control.
//
// GET /destinations/{id}/expert is admin-only and reads Args() rather than
// CommandString, so it is the one place the raw text is meant to be visible. If
// the sentinels are not here, they were never on the command line and every
// absence asserted elsewhere in this file is vacuous.
func assertExpertShowsTheRawArgv(t *testing.T, h http.Handler, sign func(*http.Request)) {
	t.Helper()
	r := jsonRequest(t, http.MethodGet, "/api/v1/destinations/1/expert", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("the positive control could not read GET /destinations/1/expert: %d %s",
			w.Code, w.Body.String())
	}
	for _, secret := range []string{argvBackslashBearer, argvRTMPConn, argvPassphrase} {
		if !strings.Contains(w.Body.String(), secret) {
			t.Fatalf("the positive control FAILED: %s is not in the admin's expert view, so "+
				"the fixture never planted it on the command line and every 'no sentinel "+
				"here' assertion in this file is proving nothing.\nbody: %s",
				secret, w.Body.String())
		}
	}
}

// wsFrames opens a REAL WebSocket with the given Authorization header and
// collects the frames that arrive within the window.
//
// A real socket over a real httptest listener rather than a call to writeEvent:
// the excuse this replaces was "not an HTTP response body", and the only way to
// retire that honestly is to read the bytes that actually left through the
// upgrade.
func wsFrames(t *testing.T, s *Server, authz string, window time.Duration) []string {
	t.Helper()
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/ws"
	conn, resp, err := websocket.DefaultDialer.Dial(url, http.Header{"Authorization": {authz}})
	if err != nil {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		t.Fatalf("dial %s: %v (status %d)", url, err, code)
	}
	defer conn.Close()

	var out []string
	deadline := time.Now().Add(window)
	_ = conn.SetReadDeadline(deadline)
	for time.Now().Before(deadline) {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}
		out = append(out, string(msg))
	}
	if len(out) == 0 {
		t.Error("the websocket delivered no frames at all inside the window, so the frame " +
			"assertions have nothing to inspect")
	}
	return out
}

// processLogFile reads the sink every process writes to.
//
// It is an egress with NO principal and never will have one: it is the file that
// goes into a support tarball. A masking scheme that consulted the caller would
// have left this one carrying the credential permanently, which is why the
// supervisor's scrub is unconditional.
func processLogFile(t *testing.T, s *Server) string {
	t.Helper()
	path := filepath.Join(s.cfg.DataDir, "logs", "process.log")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("read %s: %v", path, err)
		return ""
	}
	return string(b)
}

func truncateForFailure(s string) string {
	if len(s) <= 4000 {
		return s
	}
	return s[:4000] + "... (truncated)"
}

// TestEveryEventTypeHasAWebSocketPolicy is the fail-closed guard for /ws.
//
// A thirteenth events.Type fails the build here rather than being silently sent
// to every principal, which was the old default. events.AllTypes is itself
// AST-checked against its const block, so this cannot be defeated by adding a
// type and forgetting the list.
func TestEveryEventTypeHasAWebSocketPolicy(t *testing.T) {
	for _, typ := range events.AllTypes() {
		if _, ok := wsEventPolicy[typ]; !ok {
			t.Errorf("events.%s has no entry in wsEventPolicy. Classify it: what may a "+
				"READ-scoped socket see of this payload? Until it is classified the socket "+
				"drops it, which is the safe direction but not an answer.", typ)
		}
	}
	for typ := range wsEventPolicy {
		found := false
		for _, known := range events.AllTypes() {
			if known == typ {
				found = true
			}
		}
		if !found {
			t.Errorf("wsEventPolicy classifies %q, which is not a declared events.Type", typ)
		}
	}
}

// TestReadSocketAndAdminSocketDivergeOnTheSameEvent proves the copy.
//
// events.Broker fans ONE Event value to every subscriber. If eventView redacted
// in place, the admin socket would receive the blanked payload whenever the read
// socket happened to be first in the fan-out -- a data-dependent, iteration-order
// bug that a single-subscriber test cannot see. Two concurrent sockets on one
// broker is the only shape that can.
func TestReadSocketAndAdminSocketDivergeOnTheSameEvent(t *testing.T) {
	h, _, sign := renditionServer(t, defaultTools())
	s := serverUnderTest(t, h)
	read := createScopedToken(t, h, sign, "monitoring", db.ScopeRead)
	admin := createScopedToken(t, h, sign, "deploy", db.ScopeAdmin)

	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/ws"

	open := func(tok string) *websocket.Conn {
		c, _, err := websocket.DefaultDialer.Dial(url, http.Header{"Authorization": {"Bearer " + tok}})
		if err != nil {
			t.Fatalf("dial as %s: %v", tok[:6], err)
		}
		return c
	}
	readConn, adminConn := open(read), open(admin)
	defer readConn.Close()
	defer adminConn.Close()

	// A payload with a credential-NAMED field, published once to both.
	const secret = "SENTINEL-ws-fanout-shared-payload-7d10"
	shared := map[string]any{"streamKey": secret, "note": "unchanged"}
	time.Sleep(100 * time.Millisecond)
	s.bus.Publish(events.TypeChat, shared)

	adminFrame := waitForChatFrame(t, adminConn)
	readFrame := waitForChatFrame(t, readConn)

	if !strings.Contains(adminFrame, secret) {
		t.Errorf("the ADMIN socket did not receive the credential; frame %q. This is the "+
			"positive control: without it the read socket's clean frame proves nothing, "+
			"because there may have been nothing to redact.", adminFrame)
	}
	if strings.Contains(readFrame, secret) {
		t.Errorf("the READ socket received %s in %q", secret, readFrame)
	}
	if !strings.Contains(readFrame, "unchanged") {
		t.Errorf("the READ socket's frame lost the non-secret field too; frame %q. The "+
			"redaction is meant to blank a credential, not the payload around it.", readFrame)
	}
	// And the shared payload must be untouched, which is the in-place-mutation
	// bug stated directly.
	if shared["streamKey"] != secret {
		t.Errorf("the SHARED event payload was mutated to %v. events.Broker fans one value "+
			"to every subscriber, so redacting in place blanks the field for the admin "+
			"socket too, depending on fan-out order.", shared["streamKey"])
	}
}

// waitForChatFrame returns the first chat frame, or "" if none arrives. The
// initial status/source/stats burst is skipped by type rather than by count, so
// a change to what the burst contains does not silently make this read the
// wrong frame.
func waitForChatFrame(t *testing.T, c *websocket.Conn) string {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		_, msg, err := c.ReadMessage()
		if err != nil {
			return ""
		}
		var ev struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(msg, &ev); err != nil || ev.Type != string(events.TypeChat) {
			continue
		}
		return string(msg)
	}
}
