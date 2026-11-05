package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/failreason"
	"github.com/rainmanjam/polyemesis/internal/polls"
	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// THE STREAM DECK PLUGIN'S ROUTES, HELD TO THE ROUTER.
//
// integrations/streamdeck is a separate program that calls this API over HTTP
// with an API token. Nothing compiled the two together, so a route the plugin
// relies on could be renamed, moved behind the session-only group (#976 moved
// several routes there, and a plugin route would have gone with them unnoticed)
// or have its scope changed, and both test suites would stay green: the
// plugin's against a hand-written fake, this package's against itself.
//
// So this file reads the plugin's own source. Every this.call(...) in
// integrations/streamdeck/src/api.ts is parsed, strictly, and every route the
// plugin's contract table (src/apiContract.json) lists is driven through the
// real router with real API tokens:
//
//   - the route exists for that method, under the same pattern;
//   - an ADMIN token reaches it (requireSession refuses every token, admin
//     included, so this is the check that catches a move into that group);
//   - a READ token gets exactly what the table says: through for "read",
//     refused with the read-only 403 for "admin".
//
// The plugin side (test/apiContract.test.ts) fails when a call() is not in the
// table, and the client refuses any call() the table does not list, so the
// table cannot drift from the plugin either.

const (
	deckClientPath   = "../../integrations/streamdeck/src/api.ts"
	deckContractPath = "../../integrations/streamdeck/src/apiContract.json"
)

// deckCall is one this.call(...) site in the plugin's client.
type deckCall struct {
	Method string
	// Path is relative to /api/v1, with each ${expr} written {name}.
	Path string
	// Source is true when the call passes sourceId, which the client sends as
	// ?source=. Routes behind requireSource are reached that way.
	Source bool
	Line   int
}

var (
	deckCallToken = regexp.MustCompile(`\bcall\s*[<(]`)
	deckCommentLn = regexp.MustCompile(`^\s*(//|/\*|\*)`)
	deckVerb      = regexp.MustCompile(`^"(GET|POST)"`)
	deckParamExpr = regexp.MustCompile(`^(?:encodeURIComponent\(\s*(\w+)\s*\)|(\w+))$`)
	deckThirdArg  = regexp.MustCompile(`^(\w+)\s*[,)]`)
)

// parseDeckCalls finds every this.call(...) in the client and parses it the
// way test/apiContract.test.ts does: an optional <...> type argument, a "GET"
// or "POST" literal, a string or template literal path whose ${} is a bare
// identifier or encodeURIComponent(identifier), and an optional third argument
// that is sourceId or undefined. Anything else is an error, never a skip, so a
// call written some other way cannot slip past the contract. "call" followed by
// < or ( may otherwise appear only in the method's own definition or on a
// comment line.
func parseDeckCalls(src string) ([]deckCall, error) {
	lineOf := func(i int) int { return strings.Count(src[:i], "\n") + 1 }
	failAt := func(i int, why string, a ...any) error {
		return fmt.Errorf("src/api.ts:%d: %s", lineOf(i), fmt.Sprintf(why, a...))
	}
	skipWS := func(i int) int {
		for i < len(src) && strings.ContainsRune(" \t\r\n", rune(src[i])) {
			i++
		}
		return i
	}
	var out []deckCall
	for _, m := range deckCallToken.FindAllStringIndex(src, -1) {
		at := m[0]
		lineStart := strings.LastIndexByte(src[:at], '\n') + 1
		if deckCommentLn.MatchString(src[lineStart:at]) {
			continue
		}
		if strings.HasSuffix(strings.TrimRight(src[lineStart:at], " \t"), "private async") {
			continue // the definition
		}
		if !strings.HasSuffix(src[:at], "this.") {
			return nil, failAt(at, "a call( that is not this.call(")
		}
		i := skipWS(at + len("call"))
		if i < len(src) && src[i] == '<' {
			depth := 0
			for ; i < len(src); i++ {
				if src[i] == '<' {
					depth++
				} else if src[i] == '>' {
					if depth--; depth == 0 {
						break
					}
				}
			}
			i = skipWS(i + 1)
		}
		if i >= len(src) || src[i] != '(' {
			return nil, failAt(i, "this.call is not followed by (")
		}
		i = skipWS(i + 1)
		verb := deckVerb.FindStringSubmatch(src[i:])
		if verb == nil {
			return nil, failAt(i, `the method is not a "GET" or "POST" literal`)
		}
		i = skipWS(i + len(verb[0]))
		if i >= len(src) || src[i] != ',' {
			return nil, failAt(i, "no path after the method")
		}
		i = skipWS(i + 1)
		quote := src[i]
		if quote != '"' && quote != '`' {
			return nil, failAt(i, "the path is not a string or template literal")
		}
		var path strings.Builder
		for i++; ; i++ {
			if i >= len(src) || src[i] == '\n' || src[i] == '\\' {
				return nil, failAt(i, "an unterminated or escaped path")
			}
			if src[i] == quote {
				break
			}
			if quote == '`' && src[i] == '$' && i+1 < len(src) && src[i+1] == '{' {
				end := strings.IndexByte(src[i:], '}')
				if end < 0 {
					return nil, failAt(i, "an unterminated ${")
				}
				expr := strings.TrimSpace(src[i+2 : i+end])
				name := deckParamExpr.FindStringSubmatch(expr)
				if name == nil {
					return nil, failAt(i, "the path segment ${%s} is not an identifier", expr)
				}
				path.WriteString("{" + name[1] + name[2] + "}")
				i += end
				continue
			}
			path.WriteByte(src[i])
		}
		c := deckCall{Method: verb[1], Path: path.String(), Line: lineOf(at)}
		i = skipWS(i + 1)
		switch {
		case i < len(src) && src[i] == ')':
		case i < len(src) && src[i] == ',':
			arg := deckThirdArg.FindStringSubmatch(src[skipWS(i+1):])
			if arg == nil || (arg[1] != "sourceId" && arg[1] != "undefined") {
				return nil, failAt(i, "the third argument is not sourceId or undefined")
			}
			c.Source = arg[1] == "sourceId"
		default:
			return nil, failAt(i, "the path is not followed by , or )")
		}
		out = append(out, c)
	}
	return out, nil
}

// deckRoute is one entry of the plugin's apiContract.json.
type deckRoute struct {
	Method         string `json:"method"`
	Path           string `json:"path"`
	Scope          string `json:"scope"`
	ReadTokenSends bool   `json:"readTokenSends"`
}

func (r deckRoute) key() string { return r.Method + " " + r.Path }

func loadDeckContract(t *testing.T) []deckRoute {
	t.Helper()
	raw, err := os.ReadFile(deckContractPath)
	if err != nil {
		t.Fatalf("read the plugin's contract: %v", err)
	}
	var file struct {
		Routes []deckRoute `json:"routes"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	if err := dec.Decode(&file); err != nil {
		t.Fatalf("parse %s: %v", deckContractPath, err)
	}
	for _, r := range file.Routes {
		if r.Scope != "read" && r.Scope != "admin" {
			t.Fatalf("%s: scope %q is neither read nor admin", r.key(), r.Scope)
		}
		if r.ReadTokenSends && r.Scope != "admin" {
			t.Fatalf("%s: readTokenSends is only meaningful on an admin route", r.key())
		}
	}
	if len(file.Routes) < 20 {
		t.Fatalf("the contract lists %d routes; the plugin calls about 25, so this would check almost nothing", len(file.Routes))
	}
	return file.Routes
}

func loadDeckCalls(t *testing.T) []deckCall {
	t.Helper()
	raw, err := os.ReadFile(deckClientPath)
	if err != nil {
		t.Fatalf("read the plugin's client: %v", err)
	}
	src := string(raw)
	calls, err := parseDeckCalls(src)
	if err != nil {
		t.Fatalf("the plugin's client has a call the contract cannot read: %v", err)
	}
	if want := strings.Count(src, "this.call"); len(calls) != want {
		t.Fatalf("parsed %d call sites but src/api.ts says this.call %d times", len(calls), want)
	}
	return calls
}

// TestTheStreamDeckContractListsExactlyThePluginsCalls holds apiContract.json
// to the call sites: every call is listed under the same path, and nothing
// listed is never called. The vitest suite asserts the same from its side;
// this one is what makes the router checks below cover every call.
func TestTheStreamDeckContractListsExactlyThePluginsCalls(t *testing.T) {
	calls := loadDeckCalls(t)
	routes := loadDeckContract(t)
	listed := map[string]bool{}
	for _, r := range routes {
		if listed[r.key()] {
			t.Errorf("%s is listed twice", r.key())
		}
		listed[r.key()] = true
	}
	called := map[string]bool{}
	for _, c := range calls {
		called[c.Method+" "+c.Path] = true
		if !listed[c.Method+" "+c.Path] {
			t.Errorf("src/api.ts:%d calls %s %s, which apiContract.json does not list", c.Line, c.Method, c.Path)
		}
	}
	for _, r := range routes {
		if !called[r.key()] {
			t.Errorf("apiContract.json lists %s, which src/api.ts never calls", r.key())
		}
	}
}

func TestTheStreamDeckCallParserRefusesWhatItCannotRead(t *testing.T) {
	for _, bad := range []string{
		`this.call(method, "/status")`,
		"this.call(\"GET\", `/destinations/${dest.id}/start`)",
		`client.call("GET", "/status")`,
		`this.call("GET", "/status", this.source)`,
		`this.call("GET", "/sta` + "\n" + `tus")`,
	} {
		if got, err := parseDeckCalls(bad); err == nil {
			t.Errorf("parsed %q as %+v; want a refusal", bad, got)
		}
	}
	good := "  // call() in a comment is prose\n" +
		"  private async call<T>(\n" +
		"    const out = await this.call<Record<string, unknown>>(\n      \"POST\",\n      `/destinations/${id}/tracks/${track}/gain`,\n      sourceId,\n      body,\n    );\n" +
		"    return this.call(\"POST\", `/routing/presets/${encodeURIComponent(presetId)}`, sourceId);\n" +
		"    return this.call(\"GET\", \"/recording\");\n"
	got, err := parseDeckCalls(good)
	if err != nil {
		t.Fatal(err)
	}
	want := []deckCall{
		{Method: "POST", Path: "/destinations/{id}/tracks/{track}/gain", Source: true, Line: 3},
		{Method: "POST", Path: "/routing/presets/{presetId}", Source: true, Line: 9},
		{Method: "GET", Path: "/recording", Line: 10},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("parsed\n  %+v\nwant\n  %+v", got, want)
	}
}

// ---------------------------------------------------------------- the install

// deckFixture is an install shaped like one a Stream Deck is paired with: a
// programme with a measured layout and a live meter, destinations in every
// state a destination key paints, a running poll's worth of poll engine, and
// two tokens named the way the docs tell an operator to name them.
type deckFixture struct {
	s     *Server
	h     http.Handler
	store *db.DB
	sign  func(*http.Request)
	dir   string

	admin, read string
	source      int64
	// The destinations, by the state the fixture holds them in.
	live, off, failed, held, delayed int64
	// delayedIdle is a second delayed destination the dump line reports as
	// having nothing to dump, so a dump-all answer carries a skipped row.
	delayedIdle int64
	preset      string
}

// deckDestinations is what the fixture seeds. Low-entropy keys on purpose: the
// goldens are committed, and the admin answer of GET /destinations carries them.
func deckDestinations(source int64) map[string]*db.Destination {
	tracks := routing.Profile{
		Mode:       routing.ModeSimple,
		Tracks:     []routing.TrackSel{{Track: 0, Enabled: true, Gain: 1}},
		Normalize:  routing.NormAuto,
		SampleRate: 48000,
	}
	row := func(name string, platform db.Platform, url string, enabled bool) *db.Destination {
		return &db.Destination{
			Name: name, Kind: db.DestRTMP, Platform: platform, URL: url, StreamKey: "contract-key",
			AudioBitrate: 160, Enabled: enabled, SourceID: &source, Profile: tracks,
		}
	}
	live := row("Main stage", db.PlatformTwitch, "rtmp://live.faketool.invalid/app", true)
	off := row("Backup encoder", db.PlatformCustom, "rtmp://off.faketool.invalid/app", false)
	failed := row("Studio relay", db.PlatformCustom, "rtmp://faketool-refused.invalid/app", true)
	failed.Resilience = db.DestResilience{GiveUpAfter: 1}
	held := row("YouTube main", db.PlatformYouTube, "rtmp://faketool-rejected.invalid/app", true)
	// The ceiling, so the buffer is still filling when the test ends: "buffering"
	// is a state the key paints, and it does not flip to on-air under the test.
	delayed := row("Delayed feed", db.PlatformCustom, "rtmp://delayed.faketool.invalid/app", true)
	delayed.DelaySeconds = db.MaxDestDelaySeconds
	delayedIdle := row("Delayed backup", db.PlatformCustom, "rtmp://delayed-idle.faketool.invalid/app", true)
	delayedIdle.DelaySeconds = db.MaxDestDelaySeconds
	return map[string]*db.Destination{"live": live, "off": off, "failed": failed, "held": held, "delayed": delayed,
		"delayedIdle": delayedIdle}
}

func newDeckFixture(t *testing.T) *deckFixture {
	t.Helper()
	// Failover on, so the failover key's status and its switch to automatic are
	// the real ones rather than "failover is not enabled".
	s, h, store, sign := engineServerWithSettings(t, liveTools(t), Options{}, func(st *db.Settings) {
		st.Failover.Enabled = true
	})
	f := &deckFixture{s: s, h: h, store: store, sign: sign, dir: s.cfg.DataDir}

	src, err := store.GetSource(1)
	if err != nil {
		t.Fatalf("fixture source: %v", err)
	}
	f.source = src.ID
	// Pull, so the ingest is a child (faketool) that pumps bytes at the relay
	// hub and the layout is measured; see runningDestServer.
	src.Ingest.Mode = db.IngestPull
	src.Ingest.Pull.URL = "https://cam.example/live.m3u8"
	if err := store.UpdateSource(src); err != nil {
		t.Fatalf("fixture: choose pull ingest: %v", err)
	}

	rows := deckDestinations(f.source)
	for _, name := range []string{"live", "off", "failed", "held", "delayed", "delayedIdle"} {
		d, err := store.CreateDestination(rows[name])
		if err != nil {
			t.Fatalf("create %s destination: %v", name, err)
		}
		switch name {
		case "live":
			f.live = d.ID
		case "off":
			f.off = d.ID
		case "failed":
			f.failed = d.ID
		case "held":
			f.held = d.ID
		case "delayed":
			f.delayed = d.ID
		case "delayedIdle":
			f.delayedIdle = d.ID
		}
	}
	s.polls = polls.New(store, s.stream)
	t.Cleanup(s.polls.Wait)
	// A stand-in delay line for the dump routes: a real one needs keyframes on
	// the air, which no fixture here produces. The handler, its validation and
	// its answer are the real ones.
	s.delayDumper = &fakeDumper{dumpable: map[int64]bool{f.delayed: true}}
	if err := s.mgr.Reconcile(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	f.admin = createScopedToken(t, h, sign, "streamdeck-admin", db.ScopeAdmin)
	f.read = createScopedToken(t, h, sign, "streamdeck", db.ScopeRead)

	send(t, h, sign, http.MethodPut, fmt.Sprintf("/api/v1/source/annotations?source=%d", f.source),
		map[string]any{"annotations": []map[string]any{{"track": 0, "label": "Host mic"}}}, http.StatusOK)

	var presets struct {
		Presets []struct {
			ID string `json:"id"`
		} `json:"presets"`
	}
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/routing/presets", nil, http.StatusOK), &presets)
	if len(presets.Presets) == 0 {
		t.Fatal("the install offers no routing preset; the preset key's routes cannot be exercised")
	}
	f.preset = presets.Presets[0].ID

	f.waitSettled(t)
	return f
}

// waitSettled blocks until every destination is in the state its name says,
// and the meter is live. Each is an observation, never a fixed sleep: the held
// one needs three quick rejections and their backoff.
//
// Read from the engine's Go values, not from the JSON: the goldens are what
// pin the JSON, and a renamed field must fail there, by name, rather than
// here as a fixture that never settles.
func (f *deckFixture) waitSettled(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		ok := map[int64]bool{}
		for _, d := range f.s.mgr.DestinationStatuses() {
			state := supervisor.State("")
			if d.Process != nil {
				state = d.Process.State
			}
			failedFor := func(cause failreason.Cause, held bool) bool {
				return state == supervisor.StateFailed && d.Reason != nil && d.Reason.Cause == cause && d.Reason.Held == held
			}
			switch d.ID {
			case f.live:
				ok[d.ID] = state == supervisor.StateRunning
			case f.off:
				ok[d.ID] = d.Process == nil
			case f.failed:
				ok[d.ID] = failedFor(failreason.ConnectionRefused, false)
			case f.held:
				ok[d.ID] = failedFor(failreason.KeyRejected, true)
			case f.delayed, f.delayedIdle:
				ok[d.ID] = d.Delay != nil && d.Delay.Phase == "buffering"
			}
		}
		eng := f.s.eng()
		_, at := eng.Levels()
		feed := levelsFeedState(at, time.Now(), eng.MeterInterval())
		if ok[f.live] && ok[f.off] && ok[f.failed] && ok[f.held] && ok[f.delayed] && ok[f.delayedIdle] && feed == "live" {
			return
		}
		last = fmt.Sprintf("settled %v, levels feed %q", ok, feed)
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the install never settled into the states the contract records: %s", last)
}

// get performs a GET with a bearer token and fails unless the status matches.
func (f *deckFixture) get(t *testing.T, token, path string, want int) []byte {
	t.Helper()
	code, body := f.do(t, token, http.MethodGet, path, nil)
	if code != want {
		t.Fatalf("GET %s = %d, want %d: %s", path, code, want, body)
	}
	return body
}

// do performs one request with a bearer token, the way the plugin's transport
// does: no cookie, no CSRF header.
func (f *deckFixture) do(t *testing.T, token, method, path string, body any) (int, []byte) {
	t.Helper()
	r := jsonRequest(t, method, path, body)
	if body == nil {
		r.Header.Del("Content-Type")
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Accept", "application/json")
	w := do(t, f.h, r)
	return w.Code, w.Body.Bytes()
}

// concrete fills a contract path's {params}. ids names a value per param;
// anything it does not name takes the fixture's default: the live destination,
// track 0, the first preset. A param with no value at all fails: a new plugin
// route needs a real id, not a guess, or its 404 would read as the route being
// missing.
func (f *deckFixture) concrete(t *testing.T, route string, ids map[string]string) string {
	t.Helper()
	return regexp.MustCompile(`\{\w+\}`).ReplaceAllStringFunc(route, func(p string) string {
		if v, ok := ids[p]; ok {
			return v
		}
		switch {
		case p == "{track}":
			return "0"
		case p == "{presetId}":
			return url.PathEscape(f.preset)
		case (p == "{id}" || p == "{destinationId}") && strings.HasPrefix(route, "/destinations/"):
			return strconv.FormatInt(f.live, 10)
		}
		t.Fatalf("%s: no test value for %s; add one to deckFixture.concrete", route, p)
		return ""
	})
}

// deckBodies are the bodies the plugin's client sends, by contract route. The
// golden cases carry the same bodies, and test/contract.test.ts holds the
// client to them, so a body the client stops sending fails there.
var deckBodies = map[string]any{
	"POST /recording": map[string]any{"enabled": true},
	"POST /polls": map[string]any{"question": "Next map?", "options": []string{"Dust", "Mirage"},
		"durationSeconds": 60},
	"POST /destinations/stop-all":                            map[string]any{"confirm": true},
	"POST /destinations/delay/dump-all":                      map[string]any{"confirm": true},
	"POST /failover/source":                                  map[string]any{"source": "auto"},
	"POST /destinations/{destinationId}/tracks/{track}/gain": map[string]any{"muted": true},
}

const (
	readOnlyRefusal = "this API token is read-only"
	sessionRefusal  = "API tokens cannot reach this endpoint"
)

// TestTheStreamDeckContract builds one install and holds the plugin to it
// twice over: every response the plugin parses is the committed golden
// (streamdeck_goldens_test.go), and every route it calls exists, takes an API
// token and needs the scope its table says.
//
// One install for both because building it is most of the cost: a measured
// layout, a live meter, a destination failed and one held after three quick
// rejections.
func TestTheStreamDeckContract(t *testing.T) {
	calls := loadDeckCalls(t)
	routes := loadDeckContract(t)
	// Start-all paces its starts two seconds apart so eight encoders do not
	// spawn in one tick. Five destinations would make that eight seconds of
	// nothing for this test; the pacing has its own test.
	realPause := bulkPause
	bulkPause = func(ctx context.Context) bool { return ctx.Err() == nil }
	t.Cleanup(func() { bulkPause = realPause })

	f := newDeckFixture(t)
	t.Run("responses match the goldens", func(t *testing.T) { checkDeckGoldens(t, f, routes) })
	t.Run("routes exist, take a token, need their scope", func(t *testing.T) { checkDeckRoutes(t, f, calls, routes) })
}

// checkDeckRoutes drives every route the plugin calls through the real router.
//
// Mutation-checked: moving POST /destinations/{id}/restart into the
// session-only group fails the admin-token check by name; renaming it fails
// the router lookup; adding GET /status to readScopeDeniedPatterns fails the
// read-token check.
func checkDeckRoutes(t *testing.T, f *deckFixture, calls []deckCall, routes []deckRoute) {
	mux, ok := f.h.(*chi.Mux)
	if !ok {
		t.Fatalf("Server.Handler returned %T, not *chi.Mux; this check looks routes up in the real router", f.h)
	}
	sendsSource := map[string]bool{}
	for _, c := range calls {
		if c.Source {
			sendsSource[c.Method+" "+c.Path] = true
		}
	}
	params := regexp.MustCompile(`\{[^}]*\}`)

	// A poll to end: the plugin's poll key ends the one running.
	var started struct {
		ID int64 `json:"id"`
	}
	_, body := f.do(t, f.admin, http.MethodPost, "/api/v1/polls", deckBodies["POST /polls"])
	if err := json.Unmarshal(body, &started); err != nil || started.ID == 0 {
		t.Fatalf("start a poll: %s", body)
	}
	pollID := map[string]string{"{id}": strconv.FormatInt(started.ID, 10)}

	for _, r := range routes {
		ids := map[string]string(nil)
		if strings.HasPrefix(r.Path, "/polls/") {
			ids = pollID
		}
		path := "/api/v1" + f.concrete(t, r.Path, ids)

		// 1. The route exists, for this method, under the plugin's pattern.
		got := mux.Find(chi.NewRouteContext(), r.Method, path)
		if params.ReplaceAllString(got, "{}") != params.ReplaceAllString("/api/v1"+r.Path, "{}") {
			t.Errorf("%s: the router has no %s %s (it matched %q); the plugin's key that calls it is broken",
				r.key(), r.Method, path, got)
			continue
		}
		if sendsSource[r.key()] {
			path += fmt.Sprintf("?source=%d", f.source)
		}

		// 2. A read token gets what the table says. First, because it changes
		// nothing: an admin route refuses it before the handler runs.
		code, body := f.do(t, f.read, r.Method, path, deckBodies[r.key()])
		switch {
		case r.Scope == "admin" && (code != http.StatusForbidden || !strings.Contains(string(body), readOnlyRefusal)):
			t.Errorf("%s: a read token got %d %s; the plugin's table says this route needs an admin token "+
				"(change apiContract.json only if the server's scope change was meant)", r.key(), code, body)
		case r.Scope == "read" && (code == http.StatusForbidden || code == http.StatusUnauthorized):
			t.Errorf("%s: a read token was refused (%d %s); the plugin reads this route on a read token", r.key(), code, body)
		}

		// 3. An admin token reaches it. requireSession refuses every token, so
		// this is the check a move into the session-only group fails.
		code, body = f.do(t, f.admin, r.Method, path, deckBodies[r.key()])
		switch {
		case strings.Contains(string(body), sessionRefusal):
			t.Errorf("%s: the route is session-only now (%d %s); a Stream Deck pairs with an API token, "+
				"so every key that calls it stops working", r.key(), code, body)
		case code == http.StatusUnauthorized || code == http.StatusForbidden || code == http.StatusNotFound ||
			code == http.StatusMethodNotAllowed || code >= 500:
			t.Errorf("%s: an admin token got %d %s", r.key(), code, body)
		}
	}
}
