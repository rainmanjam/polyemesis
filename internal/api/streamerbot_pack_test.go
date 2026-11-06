package api

import (
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// THE STREAMER.BOT PACK, REPLAYED. integrations/streamerbot/actions.json lists
// every action the pack ships and the one REST call each makes; this drives
// each call against the real router with a token of the scope the manifest
// says it needs -- which is what Streamer.bot does when the action runs -- and
// requires an answer that is the route's own, not the router's or the scope
// check's. Then it holds Polyemesis.cs to the manifest, so the code cannot
// call a path the manifest (and so this test) does not know about.
//
// Streamer.bot itself is not run: nothing in this repository can. This is the
// polyemesis side of the contract only.

type packAction struct {
	Name    string          `json:"name"`
	Method  string          `json:"method"`
	Request string          `json:"request"`
	Path    string          `json:"path"`
	Body    json.RawMessage `json:"body"`
	Scope   string          `json:"scope"`
}

func loadPack(t *testing.T) []packAction {
	t.Helper()
	raw, err := os.ReadFile("../../integrations/streamerbot/actions.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Actions []packAction `json:"actions"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("actions.json: %v", err)
	}
	if len(m.Actions) < 5 {
		t.Fatalf("actions.json lists %d actions; it is not the pack", len(m.Actions))
	}
	return m.Actions
}

func TestEveryStreamerbotPackActionReachesItsRouteWithTheScopeItDeclares(t *testing.T) {
	o := obsServer(t)
	dest := o.createDest(t, "youtube")
	src := onlySourceID(t, o.h, o.sign)
	tokens := map[string]string{
		db.ScopeAdmin: createScopedToken(t, o.h, o.sign, "streamer.bot", db.ScopeAdmin),
		db.ScopeRead:  createScopedToken(t, o.h, o.sign, "streamer.bot status", db.ScopeRead),
	}
	row, _ := o.s.store.GetDestination(dest)
	row.Profile = simpleProfile(0, 1)
	if _, err := o.s.store.UpdateDestination(row); err != nil {
		t.Fatal(err)
	}
	fill := strings.NewReplacer(
		"{destinationId}", strconv.FormatInt(dest, 10),
		"{track}", "0",
		"{source}", strconv.FormatInt(src, 10),
	)
	for _, a := range loadPack(t) {
		tok, ok := tokens[a.Scope]
		if !ok {
			t.Errorf("%s declares scope %q; the pack uses read or admin", a.Name, a.Scope)
			continue
		}
		var body any
		if len(a.Body) > 0 && string(a.Body) != "null" {
			body = json.RawMessage(a.Body)
		}
		w := do(t, o.h, withBearer(jsonRequest(t, a.Request, fill.Replace(a.Path), body), tok))
		if w.Code >= 300 {
			t.Errorf("%s: %s %s answered %d %s", a.Name, a.Request, a.Path, w.Code, w.Body.String())
		}
		// The negative: with a weaker token than declared, an admin action is
		// refused -- so the scope column is a fact, not a label.
		if a.Scope == db.ScopeAdmin {
			w := do(t, o.h, withBearer(jsonRequest(t, a.Request, fill.Replace(a.Path), body), tokens[db.ScopeRead]))
			if w.Code != http.StatusForbidden {
				t.Errorf("%s with a read token answered %d; the manifest says it needs admin", a.Name, w.Code)
			}
		}
	}
}

// csCall is one Send/Request call site in Polyemesis.cs, reduced to what goes
// on the wire: the method, the path with its dynamic parts named as the
// manifest names them, and the body choices the call can send.
type csCall struct {
	method string
	path   string
	bodies []string // JSON texts; empty means no body
}

// splitTopLevel splits s on sep outside string literals and parentheses.
func splitTopLevel(s string, sep byte) []string {
	var parts []string
	depth, start := 0, 0
	inStr := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inStr:
			if c == '\\' {
				i++
			} else if c == '"' {
				inStr = false
			}
		case c == '"':
			inStr = true
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == sep && depth == 0:
			parts = append(parts, strings.TrimSpace(s[start:i]))
			start = i + 1
		}
	}
	return append(parts, strings.TrimSpace(s[start:]))
}

func csString(t *testing.T, lit string) string {
	t.Helper()
	u, err := strconv.Unquote(lit) // C# and Go agree on the escapes this file uses
	if err != nil {
		t.Fatalf("not a string literal: %s", lit)
	}
	return u
}

// csPath turns `"/api/v1/destinations/" + id + "/start"` into the manifest's
// `/api/v1/destinations/{destinationId}/start`.
func csPath(t *testing.T, expr string) string {
	t.Helper()
	dyn := map[string]string{"id": "{destinationId}", "track": "{track}", "SourceQuery()": "?source={source}"}
	var b strings.Builder
	for _, piece := range splitTopLevel(expr, '+') {
		if strings.HasPrefix(piece, `"`) {
			b.WriteString(csString(t, piece))
		} else if v, ok := dyn[piece]; ok {
			b.WriteString(v)
		} else {
			t.Fatalf("path part %q is not a literal or a known argument", piece)
		}
	}
	return b.String()
}

// csCalls finds every Send/Request call in a method body.
func csCalls(t *testing.T, body string) []csCall {
	t.Helper()
	var out []csCall
	for _, m := range regexp.MustCompile(`\b(?:Send|Request)\(`).FindAllStringIndex(body, -1) {
		end := m[1]
		depth := 1
		inStr := false
		for ; depth > 0; end++ {
			c := body[end]
			switch {
			case inStr:
				if c == '\\' {
					end++
				} else if c == '"' {
					inStr = false
				}
			case c == '"':
				inStr = true
			case c == '(':
				depth++
			case c == ')':
				depth--
			}
		}
		args := splitTopLevel(body[m[1]:end-1], ',')
		if len(args) < 3 {
			continue // Request(method, path, json, out body): handled by its four args below
		}
		c := csCall{method: csString(t, args[0]), path: csPath(t, args[1])}
		switch {
		case args[2] == "null":
		case strings.HasPrefix(args[2], `"`):
			c.bodies = []string{csString(t, args[2])}
		default: // muted ? "{...}" : "{...}"
			q := strings.SplitN(args[2], "?", 2)
			if len(q) != 2 {
				t.Fatalf("body %q is not null, a literal or a ternary of literals", args[2])
			}
			for _, alt := range splitTopLevel(q[1], ':') {
				c.bodies = append(c.bodies, csString(t, alt))
			}
		}
		out = append(out, c)
	}
	return out
}

// csMethods maps each method of Polyemesis.cs to its body.
func csMethods(code string) map[string]string {
	re := regexp.MustCompile(`(?m)^    (?:public|private)(?: static)? \w+ (\w+)\(`)
	idx := re.FindAllStringSubmatchIndex(code, -1)
	out := map[string]string{}
	for i, m := range idx {
		end := len(code)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		out[code[m[2]:m[3]]] = code[m[0]:end]
	}
	return out
}

// Every action in the manifest is exactly one call in the code: same HTTP
// method, same path (with the manifest's placeholders where the code splices an
// argument), and a body that parses to the manifest's body. A prefix match let
// the code drift from the manifest in method, trailing path or body unseen.
func TestPolyemesisCsMakesExactlyTheCallsTheManifestLists(t *testing.T) {
	raw, err := os.ReadFile("../../integrations/streamerbot/Polyemesis.cs")
	if err != nil {
		t.Fatal(err)
	}
	code := string(raw)
	methods := csMethods(code)
	pack := loadPack(t)
	covered := map[string]bool{}
	for _, a := range pack {
		name := strings.TrimPrefix(a.Method, "Execute C# Method: ")
		body, ok := methods[name]
		if !ok || !strings.Contains(body, "public bool "+name+"()") {
			t.Errorf("%s names method %s, which Polyemesis.cs does not define", a.Name, name)
			continue
		}
		calls := csCalls(t, body)
		if len(calls) == 0 {
			// MuteTrack and UnmuteTrack call the shared helper.
			arg := regexp.MustCompile(`TrackMute\((true|false)\)`).FindStringSubmatch(body)
			if arg == nil {
				t.Errorf("%s: %s makes no request and calls no helper that does", a.Name, name)
				continue
			}
			calls = csCalls(t, methods["TrackMute"])
			if len(calls) != 1 || len(calls[0].bodies) != 2 {
				t.Fatalf("TrackMute should be one call with a two-way body, got %+v", calls)
			}
			pick := 1
			if arg[1] == "true" {
				pick = 0
			}
			calls[0].bodies = calls[0].bodies[pick : pick+1]
			covered["TrackMute"] = true
		}
		covered[name] = true
		if len(calls) != 1 {
			t.Errorf("%s: %s makes %d requests, the manifest lists one", a.Name, name, len(calls))
			continue
		}
		c := calls[0]
		if c.method != a.Request {
			t.Errorf("%s: code sends %s, manifest says %s", a.Name, c.method, a.Request)
		}
		if c.path != a.Path {
			t.Errorf("%s: code calls %q, manifest says %q", a.Name, c.path, a.Path)
		}
		wantBody := string(a.Body)
		if wantBody == "null" {
			wantBody = ""
		}
		switch {
		case wantBody == "" && len(c.bodies) != 0:
			t.Errorf("%s: code sends a body %q, manifest sends none", a.Name, c.bodies)
		case wantBody != "" && len(c.bodies) != 1:
			t.Errorf("%s: code sends no body, manifest sends %s", a.Name, wantBody)
		case wantBody != "":
			var got, want any
			if err := json.Unmarshal([]byte(c.bodies[0]), &got); err != nil {
				t.Errorf("%s: body %q is not JSON: %v", a.Name, c.bodies[0], err)
				continue
			}
			_ = json.Unmarshal([]byte(wantBody), &want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s: code sends %s, manifest says %s", a.Name, c.bodies[0], wantBody)
			}
		}
	}
	// No request the manifest does not account for: every method that makes
	// one is a manifest action or the helper one is.
	for name, body := range methods {
		if name == "Request" || name == "Send" {
			continue
		}
		if len(csCalls(t, body)) > 0 && !covered[name] {
			t.Errorf("Polyemesis.cs method %s makes a request actions.json does not list", name)
		}
	}
	if strings.Contains(code, "LogInfo(token") || strings.Contains(code, "+ token") {
		t.Error("Polyemesis.cs logs or concatenates the token")
	}
}

// The token goes only over https://, or http:// to this machine: the rule the
// Stream Deck plugin (config.ts parseServerUrl) and the OBS script
// (parse_server) enforce. Nothing here can run Streamer.bot's C#, so this holds
// the source to the rule: the loopback set is the OBS script's, the refusal
// says what OBS's says, and Request consults the guard before it builds the
// request that carries the token.
func TestPolyemesisCsRefusesToSendTheTokenInTheClear(t *testing.T) {
	raw, err := os.ReadFile("../../integrations/streamerbot/Polyemesis.cs")
	if err != nil {
		t.Fatal(err)
	}
	code := string(raw)
	obs, err := os.ReadFile("../../integrations/obs/polyemesis_obs.py")
	if err != nil {
		t.Fatal(err)
	}
	set := regexp.MustCompile(`LOOPBACK = \{([^}]*)\}`).FindStringSubmatch(string(obs))
	if set == nil {
		t.Fatal("cannot find LOOPBACK in polyemesis_obs.py")
	}
	cs := regexp.MustCompile(`Loopback = \{([^}]*)\}`).FindStringSubmatch(code)
	if cs == nil {
		t.Fatal("Polyemesis.cs has no Loopback host list")
	}
	hosts := func(list string) map[string]bool {
		m := map[string]bool{}
		for _, h := range strings.Split(list, ",") {
			// Uri.Host spells IPv6 with brackets; the OBS script's hostname does not.
			m[strings.Trim(strings.TrimSpace(h), `"'[]`)] = true
		}
		return m
	}
	csHosts := hosts(cs[1])
	for h := range hosts(set[1]) {
		if !csHosts[h] {
			t.Errorf("the OBS script allows http:// to %q; Polyemesis.cs does not", h)
		}
	}
	for h := range csHosts {
		if !hosts(set[1])[h] {
			t.Errorf("Polyemesis.cs allows http:// to %q; the OBS script does not", h)
		}
	}
	// Uri.Host is "[::1]" for http://[::1]:8080, so a bare "::1" entry would
	// never match; the list must carry the bracketed form (checked on mono).
	if !strings.Contains(cs[1], `"[::1]"`) || strings.Contains(cs[1], `"::1"`) {
		t.Errorf("Polyemesis.cs's Loopback list must spell IPv6 loopback \"[::1]\", as Uri.Host does: %s", cs[1])
	}
	for _, want := range []string{
		`u.Scheme == "https"`,
		`u.Scheme == "http" && Array.IndexOf(Loopback, u.Host) >= 0`,
		"the API token would cross the network unencrypted",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("Polyemesis.cs does not contain %q", want)
		}
	}
	req := csMethods(code)["Request"]
	guard, build := strings.Index(req, "Refusal(url)"), strings.Index(req, "new HttpRequestMessage")
	if guard < 0 || build < 0 || guard > build {
		t.Error("Request must refuse a cleartext address before it builds the request that carries the token")
	}
}
