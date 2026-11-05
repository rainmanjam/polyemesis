package api

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/analytics"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/engine"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// THE STREAM DECK PLUGIN'S RESPONSES, RECORDED FROM THE REAL SERVER.
//
// The plugin's tests run against a fake install whose answers were written by
// hand from the Go structs, which is a copy, and copies drift. So the answers
// are recorded here instead, from the real handlers on the seeded install in
// streamdeck_contract_test.go, into integrations/streamdeck/test/contract/:
//
//   - one file per case: the request the plugin sends (route, path, query,
//     body, which token) and the status and body the server answered. Every
//     GET the plugin reads, and the POST answers and refusals it branches on.
//   - schema.json: per route, every field each 2xx answer can carry and its
//     JSON type; and under "refusal", every field a 4xx or 5xx answer can. It is the union of what the recorded answers carry and, where
//     the handler writes a Go type, every json-tagged field of that type, so a
//     field this install never fills (platformHealth, restartStorm) is still
//     listed under the name the server would send it.
//
// Without the flag, a recorded answer that no longer matches its golden fails
// here: the server changed under the plugin. With it the goldens are
// rewritten, and the plugin's suite (test/contract.test.ts) then fails if the
// plugin cannot parse the new answers, or if a hand-written fake in its tests
// sends a field the schema does not list -- a renamed field fails both sides.
//
//	go test ./internal/api/ -run TestTheStreamDeckContract -update-streamdeck-contract
//
// Values that move between runs (times, pids, uptimes, buffer fill, random
// token prefixes) are normalised by name, and every GET is recorded twice and
// must normalise to the same bytes, so a volatile field nobody listed fails
// here on the first run rather than on CI later.

var updateDeckGoldens = flag.Bool("update-streamdeck-contract", false,
	"rewrite integrations/streamdeck/test/contract/ from the live handlers")

const deckGoldenDir = "../../integrations/streamdeck/test/contract"

// deckCase is one recorded request.
type deckCase struct {
	Name   string
	Method string
	// Route is the apiContract.json path this request is an instance of.
	Route string
	// IDs fills Route's params; anything unnamed takes concrete's default.
	IDs   map[string]string
	Query url.Values
	Body  any
	// Token is "read", "admin" or "unknown" (a token the install never issued).
	Token string
	// ZeroSource records against an install that has no programme yet.
	ZeroSource bool
	// Keep, when set, is the only fields recorded: of the answer's object, or of
	// each element when the answer is a list. For a body most of which the
	// plugin never reads and which changes for reasons that are not the
	// plugin's business: the whole of GET /settings, /status's relay counters
	// and loudness meters, /destinations' compiled filter graphs. The schema is
	// cut the same way.
	Keep []string
	// Volatile lists dotted paths (with * for any array index) whose values are
	// timing-dependent in this fixture and are normalised.
	Volatile []string
	// Check, when set, asserts what the request did to the install, beyond
	// what it answered.
	Check func(t *testing.T, f *deckFixture, g deckGolden)
}

// deckVolatileKeys are normalised wherever they appear: their values move
// between two reads of an install that has not changed.
var deckVolatileKeys = map[string]bool{
	"pid": true, "uptimeSec": true, "nextRetryIn": true, "stalledSec": true, "progress": true,
	"fill": true, "bufferedSeconds": true, "memBytes": true, "diskBytes": true, "prefix": true,
	"rxBytes": true, "txBytes": true, "rxPackets": true, "txPackets": true, "receivingSec": true,
	"relayPort": true, "port": true, "switches": true,
}

var (
	deckTimeLike = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}`)
	deckLoopback = regexp.MustCompile(`127\.0\.0\.1:\d+`)
)

func (f *deckFixture) cases() []deckCase {
	src := url.Values{"source": {strconv.FormatInt(f.source, 10)}}
	with := func(extra map[string]string) url.Values {
		q := url.Values{}
		for k, v := range src {
			q[k] = v
		}
		for k, v := range extra {
			q.Set(k, v)
		}
		return q
	}
	id := func(n int64) string { return strconv.FormatInt(n, 10) }
	return []deckCase{
		// The steady state a deck reads every tick, before anything changes it.
		{Name: "connection-read", Method: "GET", Route: "/auth/connection", Token: "read"},
		{Name: "connection-admin", Method: "GET", Route: "/auth/connection", Token: "admin"},
		{Name: "connection-unknown-token", Method: "GET", Route: "/auth/connection", Token: "unknown"},
		{Name: "sources", Method: "GET", Route: "/sources", Token: "read"},
		{Name: "status", Method: "GET", Route: "/status", Query: src, Token: "read", Keep: deckStatusKeep},
		{Name: "levels", Method: "GET", Route: "/levels", Query: src, Token: "read"},
		{Name: "settings", Method: "GET", Route: "/settings", Token: "read", Keep: []string{"meters"}},
		{Name: "stop-impact", Method: "GET", Route: "/destinations/stop-impact", Query: src, Token: "admin"},
		{Name: "stop-impact-destination", Method: "GET", Route: "/destinations/stop-impact",
			Query: with(map[string]string{"destination": id(f.live)}), Token: "admin"},
		{Name: "recording", Method: "GET", Route: "/recording", Token: "read"},
		{Name: "routing-presets", Method: "GET", Route: "/routing/presets", Token: "read"},
		{Name: "destinations", Method: "GET", Route: "/destinations", Token: "read", Keep: []string{"destination"}},
		{Name: "source", Method: "GET", Route: "/source", Query: src, Token: "read"},
		{Name: "analytics-live", Method: "GET", Route: "/analytics/live", Token: "admin"},
		{Name: "analytics-live-read-token", Method: "GET", Route: "/analytics/live", Token: "read"},

		// The poll key: start, read while it runs, end.
		{Name: "poll-start", Method: "POST", Route: "/polls", Body: deckBodies["POST /polls"], Token: "admin"},
		{Name: "polls-running", Method: "GET", Route: "/polls", Query: url.Values{"limit": {"1"}}, Token: "read"},
		{Name: "poll-end", Method: "POST", Route: "/polls/{id}/end", IDs: map[string]string{"{id}": "1"}, Token: "admin"},
		{Name: "polls-none", Method: "GET", Route: "/polls", Query: url.Values{"limit": {"1"}}, Token: "read"},

		{Name: "recording-on", Method: "POST", Route: "/recording", Body: map[string]any{"enabled": true}, Token: "admin"},
		{Name: "recording-off", Method: "POST", Route: "/recording", Body: map[string]any{"enabled": false}, Token: "admin"},

		// The track mute key and the gain dial, on the live destination.
		// The reason is the server's prose for how the level reached the output,
		// and it is the platform's: live gain is not verified on Windows.
		{Name: "track-mute", Method: "POST", Route: "/destinations/{destinationId}/tracks/{track}/gain",
			Query: src, Body: map[string]any{"muted": true}, Token: "admin", Volatile: []string{"reason"}},
		{Name: "track-gain", Method: "POST", Route: "/destinations/{destinationId}/tracks/{track}/gain",
			Query: src, Body: map[string]any{"gain": 0.5}, Token: "admin", Volatile: []string{"reason"}},

		// The dump keys: one destination, the programme, and one with no delay.
		{Name: "delay-dump", Method: "POST", Route: "/destinations/{id}/delay/dump",
			IDs: map[string]string{"{id}": id(f.delayed)}, Query: src, Body: map[string]any{"seconds": 10}, Token: "admin"},
		{Name: "delay-dump-nothing", Method: "POST", Route: "/destinations/{id}/delay/dump", Query: src, Token: "admin"},
		{Name: "delay-dump-all", Method: "POST", Route: "/destinations/delay/dump-all", Query: src,
			Body: map[string]any{"confirm": true}, Token: "admin"},

		// The failover key hands control back to automatic.
		{Name: "failover-auto", Method: "POST", Route: "/failover/source", Query: src,
			Body: map[string]any{"source": "auto"}, Token: "admin"},
		// The routing preset key. It must APPLY: the compile route it used to send
		// answered 200 and stored nothing.
		{Name: "preset-apply", Method: "POST", Route: "/routing/presets/{presetId}/apply", Query: src, Token: "admin",
			Check: deckPresetApplied},

		// The destination key's taps, and its refusals.
		{Name: "destination-retry", Method: "POST", Route: "/destinations/{id}/restart",
			IDs: map[string]string{"{id}": id(f.failed)}, Query: src, Token: "admin"},
		{Name: "destination-retry-refused", Method: "POST", Route: "/destinations/{id}/restart",
			IDs: map[string]string{"{id}": "999"}, Query: src, Token: "admin"},
		{Name: "destination-start", Method: "POST", Route: "/destinations/{id}/start",
			IDs: map[string]string{"{id}": id(f.off)}, Query: src, Token: "admin", Volatile: []string{"state"}},
		{Name: "destination-start-read-token", Method: "POST", Route: "/destinations/{id}/start",
			IDs: map[string]string{"{id}": id(f.off)}, Query: src, Token: "read"},
		{Name: "destination-stop", Method: "POST", Route: "/destinations/{id}/stop", Query: src, Token: "admin"},
		{Name: "destination-stop-now", Method: "POST", Route: "/destinations/{id}/stop",
			IDs: map[string]string{"{id}": id(f.delayed)}, Query: with(map[string]string{"now": "true"}), Token: "admin"},

		// Go live and stop all, last: they move every destination.
		{Name: "start-all", Method: "POST", Route: "/destinations/start-all", Query: src, Token: "admin",
			Volatile: []string{"results.*.state"}},
		{Name: "stop-all", Method: "POST", Route: "/destinations/stop-all", Query: src,
			Body: map[string]any{"confirm": true}, Token: "admin"},
		{Name: "stop-all-unconfirmed", Method: "POST", Route: "/destinations/stop-all", Query: src, Token: "admin"},

		// A fresh install: no programme to read or act on.
		{Name: "no-source-status", Method: "GET", Route: "/status", Token: "read", ZeroSource: true, Keep: deckStatusKeep},
		{Name: "no-source-start-all", Method: "POST", Route: "/destinations/start-all", Token: "admin", ZeroSource: true},
	}
}

// deckRefusalKey is schema.json's entry for every 4xx and 5xx answer.
const deckRefusalKey = "refusal"

// deckStatusKeep is what the plugin reads of GET /status (src/model.ts's
// StatusPayload).
var deckStatusKeep = []string{"destinations", "destinationHold", "failover", "source"}

// deckGolden is one committed file.
type deckGolden struct {
	Case    string        `json:"case"`
	Request deckGoldenReq `json:"request"`
	Status  int           `json:"status"`
	Body    any           `json:"body"`
}

type deckGoldenReq struct {
	Method string            `json:"method"`
	Route  string            `json:"route"`
	Path   string            `json:"path"`
	Query  map[string]string `json:"query,omitempty"`
	Body   any               `json:"body,omitempty"`
	Token  string            `json:"token"`
}

// deckTypes are the Go types the handlers write, by contract route, for the
// schema's reflected half. A route not here is described by its recorded
// answers alone.
var deckTypes = map[string]reflect.Type{
	"GET /status":                            reflect.TypeFor[engine.Status](),
	"GET /source":                            reflect.TypeFor[engine.SourceInfo](),
	"GET /analytics/live":                    reflect.TypeFor[analytics.Live](),
	"GET /recording":                         reflect.TypeFor[recordingState](),
	"POST /recording":                        reflect.TypeFor[recordingState](),
	"GET /polls":                             reflect.TypeFor[pollsResponse](),
	"GET /destinations":                      reflect.TypeFor[[]deckDestinationRow](),
	"GET /auth/connection":                   reflect.TypeFor[connectionCheckBody](),
	"POST /routing/presets/{presetId}/apply": reflect.TypeFor[deckPresetApply](),
}

// deckPresetApply is POST /routing/presets/{preset}/apply's answer, as
// handleApplyPresetToProgramme builds it from a map.
type deckPresetApply struct {
	Preset          string           `json:"preset"`
	Mix             string           `json:"mix"`
	DryRun          bool             `json:"dryRun,omitempty"`
	Results         []presetApplyRow `json:"results"`
	Warnings        []string         `json:"warnings,omitempty"`
	ReconcileFailed bool             `json:"reconcileFailed,omitempty"`
}

// deckOffGain is the level the fixture's off destination starts at on track 0.
// The preset key's case puts it back to unity.
const deckOffGain = 0.5

// deckPresetApplied is the preset key's case asserting that the preset was
// APPLIED, not compiled: the off destination's stored track 0 is back at unity,
// and the answer says so for it. The running destination held what the preset
// builds already, so it kept its process.
//
// MUTATION: point the plugin back at POST /routing/presets/{presetId} (the
// compile route) in api.ts and apiContract.json. Observed: FAIL, nothing stored
// and the answer carries no results.
func deckPresetApplied(t *testing.T, f *deckFixture, g deckGolden) {
	t.Helper()
	if g.Status != http.StatusOK {
		t.Fatalf("the preset key's apply answered %d: %v", g.Status, g.Body)
	}
	raw, err := json.Marshal(g.Body)
	if err != nil {
		t.Fatal(err)
	}
	var ans deckPresetApply
	if err := json.Unmarshal(raw, &ans); err != nil {
		t.Fatalf("the preset key's apply answered %s: %v", raw, err)
	}
	outcome := map[int64]presetApplyRow{}
	for _, r := range ans.Results {
		outcome[r.ID] = r
	}
	if r := outcome[f.off]; r.Outcome != presetSaved {
		t.Errorf("the off destination's row is %+v, want saved", r)
	}
	if r := outcome[f.live]; r.Outcome != presetSaved || r.Applied != "live" {
		t.Errorf("the live destination's row is %+v, want saved and kept running (live)", r)
	}
	off, err := f.store.GetDestination(f.off)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range off.Profile.Tracks {
		if s.Track == 0 && s.Gain != 1 {
			t.Fatalf("the preset key answered 200 and the off destination's track 0 is still at %v: "+
				"the routing did not change", s.Gain)
		}
	}
}

// deckDestinationRow is one element of GET /destinations, as
// handleListDestinations builds it from a map.
type deckDestinationRow struct {
	Destination        db.Destination `json:"destination"`
	Routing            routing.Result `json:"routing"`
	RoutingProvisional bool           `json:"routingProvisional,omitempty"`
	RoutingError       string         `json:"routingError,omitempty"`
}

func checkDeckGoldens(t *testing.T, f *deckFixture, routes []deckRoute) {
	_, zh, zsign := zeroSourceServer(t)
	zero := &deckFixture{h: zh, admin: createScopedToken(t, zh, zsign, "streamdeck-admin", db.ScopeAdmin)}
	zero.read = createScopedToken(t, zh, zsign, "streamdeck", db.ScopeRead)

	got := map[string]deckGolden{}
	samples := map[string][]any{}
	// Every refusal the cases record, whatever the route: the one error shape
	// the plugin reads (error, and code for no_source).
	refusals := schemaOfType(reflect.TypeFor[apiError](), map[reflect.Type]bool{})
	keeps := map[string][]string{}
	for _, c := range f.cases() {
		target := f
		if c.ZeroSource {
			target = zero
		}
		g := target.record(t, c)
		if c.Check != nil {
			c.Check(t, target, g)
		}
		got[c.Name] = g
		if g.Status >= 400 && g.Body != nil {
			refusals = refusals.merge(schemaOfValue(g.Body))
		}
		if g.Status >= 200 && g.Status < 300 {
			key := c.Method + " " + c.Route
			samples[key] = append(samples[key], g.Body)
			if prev, ok := keeps[key]; ok && strings.Join(prev, ",") != strings.Join(c.Keep, ",") {
				t.Fatalf("%s: every case of %s must keep the same fields, or its schema means nothing", c.Name, key)
			}
			keeps[key] = c.Keep
		}
	}
	schema := map[string]*deckSchema{}
	for key, bodies := range samples {
		var s *deckSchema
		for _, b := range bodies {
			s = s.merge(schemaOfValue(b))
		}
		if typ, ok := deckTypes[key]; ok {
			s = s.merge(schemaOfType(typ, map[reflect.Type]bool{}).keep(keeps[key]))
		}
		schema[key] = s
	}
	schema[deckRefusalKey] = refusals
	for key := range deckTypes {
		if schema[key] == nil {
			t.Errorf("deckTypes names %s, which no case records a 2xx answer for", key)
		}
	}
	// Every route the plugin calls has a recorded success, so the plugin's
	// schema check has something to hold its fakes for that route to.
	for _, r := range routes {
		if schema[r.key()] == nil {
			t.Errorf("no case records a 2xx answer of %s, which the plugin calls; add one to deckFixture.cases", r.key())
		}
	}

	files := map[string][]byte{"schema.json": deckJSON(t, schema)}
	for name, g := range got {
		files[name+".json"] = deckJSON(t, g)
	}

	if *updateDeckGoldens {
		writeDeckGoldens(t, files)
		return
	}
	compareDeckGoldens(t, files)
}

// record performs one case and returns it normalised. A GET is performed
// twice, and the two must normalise identically.
func (f *deckFixture) record(t *testing.T, c deckCase) deckGolden {
	t.Helper()
	path := f.concrete(t, c.Route, c.IDs)
	full := "/api/v1" + path
	if len(c.Query) > 0 {
		full += "?" + c.Query.Encode()
	}
	token := map[string]string{"read": f.read, "admin": f.admin, "unknown": "pmk_not_issued_by_this_install"}[c.Token]
	if token == "" {
		t.Fatalf("%s: no %q token", c.Name, c.Token)
	}
	code, raw := f.do(t, token, c.Method, full, c.Body)
	body := f.normalise(t, c, raw)
	if c.Method == http.MethodGet {
		again, raw2 := f.do(t, token, c.Method, full, c.Body)
		if again != code || !bytes.Equal(deckJSON(t, body), deckJSON(t, f.normalise(t, c, raw2))) {
			t.Fatalf("%s: two reads of an unchanged install differ after normalising; add the moving field "+
				"to deckVolatileKeys or the case's Volatile.\nfirst  %d %s\nsecond %d %s", c.Name, code, raw, again, raw2)
		}
	}
	g := deckGolden{Case: c.Name, Status: code, Body: body,
		Request: deckGoldenReq{Method: c.Method, Route: c.Route, Path: path, Body: c.Body, Token: c.Token}}
	if len(c.Query) > 0 {
		g.Request.Query = map[string]string{}
		for k := range c.Query {
			g.Request.Query[k] = c.Query.Get(k)
		}
	}
	// The request body goes through JSON too, so the golden compares as data.
	if c.Body != nil {
		var b any
		decodeInto(t, deckJSON(t, c.Body), &b)
		g.Request.Body = b
	}
	return g
}

// deckKeep cuts an object, or each element of a list, down to the named
// fields. A named field the answer does not carry is simply absent: an
// omitempty field such as destinationHold comes and goes with the state.
func deckKeep(v any, keep []string) any {
	if len(keep) == 0 {
		return v
	}
	switch x := v.(type) {
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = deckKeep(e, keep)
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for _, k := range keep {
			if e, ok := x[k]; ok {
				out[k] = e
			}
		}
		return out
	}
	return v
}

// keep is deckKeep for a schema, except that a field cut from the recording is
// still listed, as Any: the server does send it, and a fake that carries it is
// not wrong; only its shape is not pinned here.
func (s *deckSchema) keep(keep []string) *deckSchema {
	if s == nil || len(keep) == 0 {
		return s
	}
	if s.Items != nil {
		c := *s
		c.Items = s.Items.keep(keep)
		return &c
	}
	if s.Fields == nil {
		return s
	}
	kept := map[string]bool{}
	for _, k := range keep {
		kept[k] = true
	}
	c := *s
	c.Fields = map[string]*deckSchema{}
	for k, f := range s.Fields {
		if kept[k] {
			c.Fields[k] = f
		} else {
			c.Fields[k] = &deckSchema{Any: true}
		}
	}
	return &c
}

// normalise decodes an answer and blanks what moves between runs.
func (f *deckFixture) normalise(t *testing.T, c deckCase, raw []byte) any {
	t.Helper()
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s: the answer is not JSON: %v: %s", c.Name, err, raw)
	}
	v = deckKeep(v, c.Keep)
	volatile := map[string]bool{}
	for _, p := range c.Volatile {
		volatile[p] = true
	}
	return f.blank(v, "", false, volatile)
}

func (f *deckFixture) blank(v any, path string, under bool, volatile map[string]bool) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			p := k
			if path != "" {
				p = path + "." + k
			}
			out[k] = f.blank(val, p, under || deckVolatileKeys[k] || volatile[p], volatile)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			p := path + ".*"
			out[i] = f.blank(val, p, under || volatile[p], volatile)
		}
		return out
	case string:
		if deckTimeLike.MatchString(x) {
			if _, err := time.Parse(time.RFC3339Nano, x); err == nil {
				return "2026-01-01T00:00:00Z"
			}
		}
		if under {
			return "volatile"
		}
		if f.dir != "" {
			x = strings.ReplaceAll(x, f.dir, "<data>")
		}
		return deckLoopback.ReplaceAllString(x, "127.0.0.1:0")
	case float64:
		if under {
			return 0.0
		}
		return x
	}
	return v
}

func deckJSON(t *testing.T, v any) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		t.Fatalf("encode golden: %v", err)
	}
	return buf.Bytes()
}

func writeDeckGoldens(t *testing.T, files map[string][]byte) {
	t.Helper()
	if err := os.MkdirAll(deckGoldenDir, 0o755); err != nil {
		t.Fatal(err)
	}
	old, _ := filepath.Glob(filepath.Join(deckGoldenDir, "*.json"))
	for _, p := range old {
		if _, keep := files[filepath.Base(p)]; !keep {
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
		}
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(deckGoldenDir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("wrote %d goldens to %s; review the diff, then run the plugin's tests", len(files), deckGoldenDir)
}

// compareDeckGoldens compares as decoded JSON, not bytes, so a checkout that
// rewrote line endings is not a drift.
func compareDeckGoldens(t *testing.T, files map[string][]byte) {
	t.Helper()
	const regen = "go test ./internal/api/ -run TestTheStreamDeckContract -update-streamdeck-contract"
	on, _ := filepath.Glob(filepath.Join(deckGoldenDir, "*.json"))
	for _, p := range on {
		if _, ok := files[filepath.Base(p)]; !ok {
			t.Errorf("%s is committed but no case records it; delete it or regenerate: %s", p, regen)
		}
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(deckGoldenDir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: %v; generate it with: %s", path, err, regen)
			continue
		}
		var want, got any
		if err := json.Unmarshal(raw, &want); err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		decodeInto(t, files[name], &got)
		if diffs := deckDiff("", want, got, nil); len(diffs) > 0 {
			if len(diffs) > 12 {
				diffs = append(diffs[:12], fmt.Sprintf("... and %d more", len(diffs)-12))
			}
			t.Errorf("%s: the server's answer no longer matches what the Stream Deck plugin was built against:\n  %s\n"+
				"If the change is meant, regenerate (%s), review the diff, and run the plugin's tests.",
				name, strings.Join(diffs, "\n  "), regen)
		}
	}
}

// deckDiff lists where two decoded JSON values differ, by path.
func deckDiff(path string, want, got any, out []string) []string {
	at := path
	if at == "" {
		at = "$"
	}
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return append(out, fmt.Sprintf("%s: was an object, now %s", at, deckBrief(got)))
		}
		keys := map[string]bool{}
		for k := range w {
			keys[k] = true
		}
		for k := range g {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			wv, wok := w[k]
			gv, gok := g[k]
			switch {
			case !gok:
				out = append(out, fmt.Sprintf("%s.%s: the server no longer sends it (was %s)", at, k, deckBrief(wv)))
			case !wok:
				out = append(out, fmt.Sprintf("%s.%s: new, %s", at, k, deckBrief(gv)))
			default:
				out = deckDiff(at+"."+k, wv, gv, out)
			}
		}
		return out
	case []any:
		g, ok := got.([]any)
		if !ok {
			return append(out, fmt.Sprintf("%s: was an array, now %s", at, deckBrief(got)))
		}
		if len(w) != len(g) {
			out = append(out, fmt.Sprintf("%s: %d elements, now %d", at, len(w), len(g)))
		}
		for i := 0; i < len(w) && i < len(g); i++ {
			out = deckDiff(fmt.Sprintf("%s[%d]", at, i), w[i], g[i], out)
		}
		return out
	}
	if !reflect.DeepEqual(want, got) {
		out = append(out, fmt.Sprintf("%s: was %s, now %s", at, deckBrief(want), deckBrief(got)))
	}
	return out
}

func deckBrief(v any) string {
	b, _ := json.Marshal(v)
	if len(b) > 80 {
		return string(b[:77]) + "..."
	}
	return string(b)
}

// ---------------------------------------------------------------- the schema

// deckSchema describes one JSON value: the types it may take and, for an
// object, its fields; for an array, its elements. Values set means an object
// with arbitrary keys (a Go map). Any means nothing is known.
type deckSchema struct {
	Types  []string               `json:"types,omitempty"`
	Fields map[string]*deckSchema `json:"fields,omitempty"`
	Items  *deckSchema            `json:"items,omitempty"`
	Values *deckSchema            `json:"values,omitempty"`
	Any    bool                   `json:"any,omitempty"`
}

func (s *deckSchema) merge(o *deckSchema) *deckSchema {
	if s == nil {
		return o
	}
	if o == nil {
		return s
	}
	out := &deckSchema{Any: s.Any || o.Any}
	if out.Any {
		return out
	}
	types := map[string]bool{}
	for _, t := range append(append([]string{}, s.Types...), o.Types...) {
		types[t] = true
	}
	for t := range types {
		out.Types = append(out.Types, t)
	}
	sort.Strings(out.Types)
	if s.Fields != nil || o.Fields != nil {
		out.Fields = map[string]*deckSchema{}
		for k, v := range s.Fields {
			out.Fields[k] = v
		}
		for k, v := range o.Fields {
			out.Fields[k] = out.Fields[k].merge(v)
		}
	}
	out.Items = s.Items.merge(o.Items)
	out.Values = s.Values.merge(o.Values)
	return out
}

func schemaOfValue(v any) *deckSchema {
	switch x := v.(type) {
	case nil:
		return &deckSchema{Types: []string{"null"}}
	case bool:
		return &deckSchema{Types: []string{"boolean"}}
	case float64:
		return &deckSchema{Types: []string{"number"}}
	case string:
		return &deckSchema{Types: []string{"string"}}
	case []any:
		s := &deckSchema{Types: []string{"array"}}
		for _, e := range x {
			s.Items = s.Items.merge(schemaOfValue(e))
		}
		return s
	case map[string]any:
		s := &deckSchema{Types: []string{"object"}, Fields: map[string]*deckSchema{}}
		for k, e := range x {
			s.Fields[k] = schemaOfValue(e)
		}
		return s
	}
	return &deckSchema{Any: true}
}

var (
	deckJSONMarshaler = reflect.TypeFor[json.Marshaler]()
	deckTextMarshaler = reflect.TypeFor[interface{ MarshalText() ([]byte, error) }]()
	deckTimeType      = reflect.TypeFor[time.Time]()
)

// schemaOfType is the JSON encoding/json would produce for t, by its json
// tags. A type that marshals itself is unknown (Any), except time.Time.
func schemaOfType(t reflect.Type, seen map[reflect.Type]bool) *deckSchema {
	if t == deckTimeType {
		return &deckSchema{Types: []string{"string"}}
	}
	if t.Kind() != reflect.Pointer && (t.Implements(deckJSONMarshaler) || reflect.PointerTo(t).Implements(deckJSONMarshaler)) {
		return &deckSchema{Any: true}
	}
	if t.Kind() != reflect.Pointer && (t.Implements(deckTextMarshaler) || reflect.PointerTo(t).Implements(deckTextMarshaler)) {
		return &deckSchema{Types: []string{"string"}}
	}
	switch t.Kind() {
	case reflect.Pointer:
		return schemaOfType(t.Elem(), seen).merge(&deckSchema{Types: []string{"null"}})
	case reflect.Bool:
		return &deckSchema{Types: []string{"boolean"}}
	case reflect.String:
		return &deckSchema{Types: []string{"string"}}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return &deckSchema{Types: []string{"number"}}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return &deckSchema{Types: []string{"string"}}
		}
		s := &deckSchema{Types: []string{"array"}, Items: schemaOfType(t.Elem(), seen)}
		if t.Kind() == reflect.Slice {
			s.Types = append(s.Types, "null")
		}
		return s
	case reflect.Map:
		return &deckSchema{Types: []string{"null", "object"}, Values: schemaOfType(t.Elem(), seen)}
	case reflect.Struct:
		if seen[t] {
			return &deckSchema{Any: true}
		}
		seen[t] = true
		defer delete(seen, t)
		s := &deckSchema{Types: []string{"object"}, Fields: map[string]*deckSchema{}}
		for i := 0; i < t.NumField(); i++ {
			fld := t.Field(i)
			tag := fld.Tag.Get("json")
			if tag == "-" || (!fld.IsExported() && !fld.Anonymous) {
				continue
			}
			name, _, _ := strings.Cut(tag, ",")
			if fld.Anonymous && name == "" {
				inner := fld.Type
				if inner.Kind() == reflect.Pointer {
					inner = inner.Elem()
				}
				if inner.Kind() == reflect.Struct {
					for k, v := range schemaOfType(inner, seen).Fields {
						s.Fields[k] = v
					}
					continue
				}
			}
			if name == "" {
				name = fld.Name
			}
			s.Fields[name] = schemaOfType(fld.Type, seen)
		}
		return s
	}
	return &deckSchema{Any: true}
}
