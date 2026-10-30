package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// MCP is opt-in: a switch that is off on every install, three levels under it,
// and a token scope of its own. These tests drive each layer through the real
// mux, and the guards are mutation-checked (named on each test).

// mcpRawPost is a JSON-RPC initialize as an MCP client sends it, so a 404 here
// is the switch and not a malformed body.
func mcpRawPost(token string) *http.Request {
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18",` +
		`"capabilities":{},"clientInfo":{"name":"probe","version":"1"}}}`
	r := httptest.NewRequest(http.MethodPost, MCPPath, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

// TestAFreshOrUpgradedInstallHasMCPOff: no row is OFF, with Read as the level
// the first enable will use. This is what an upgraded database reads -- the
// table is created empty by schema.sql on open -- and what a fresh one reads.
//
// Mutation: default Enabled to true in GetMCPAccess's no-row branch. Observed
// FAIL.
func TestAFreshOrUpgradedInstallHasMCPOff(t *testing.T) {
	_, store, _ := plantedServer(t)
	a, err := store.GetMCPAccess()
	if err != nil {
		t.Fatal(err)
	}
	if a.Enabled || !reflect.DeepEqual(a.Levels, []string{db.MCPLevelRead}) {
		t.Fatalf("a database that never chose MCP reads %+v; want off, with read as the first-enable level", a)
	}
}

// TestASwitchedOffMCPEndpointIsAnUnknownEndpoint: every method, anonymous and
// with an admin MCP token, gets the bytes an unknown endpoint gets. Not a 403,
// not a 401, not a 405 for GET: any of those says the route exists.
//
// Mutation: disable mcpGate, leaving the route registered for POST. Observed
// FAIL (and the ledger's POST /mcp excuse fails with it).
func TestASwitchedOffMCPEndpointIsAnUnknownEndpoint(t *testing.T) {
	h, store, sign := plantedServer(t)
	tok := createScopedToken(t, h, sign, "assistant", db.ScopeMCPAdmin)
	if a, _ := store.GetMCPAccess(); a.Enabled {
		t.Fatal("the fixture has MCP on; this test is about off")
	}
	unknown := func(method string) *httptest.ResponseRecorder {
		return do(t, h, httptest.NewRequest(method, "/api/v1/no-such-endpoint", nil))
	}
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		want := unknown(method)
		if want.Code != http.StatusNotFound {
			t.Fatalf("the control is wrong: an unknown endpoint answered %d to %s", want.Code, method)
		}
		for _, who := range []string{"", tok} {
			r := httptest.NewRequest(method, MCPPath, nil)
			if method == http.MethodPost {
				r = mcpRawPost(who)
			} else if who != "" {
				r.Header.Set("Authorization", "Bearer "+who)
			}
			got := do(t, h, r)
			if got.Code != want.Code || got.Body.String() != want.Body.String() ||
				got.Header().Get("Content-Type") != want.Header().Get("Content-Type") {
				t.Errorf("%s /mcp (token=%v) answered %d %q (%s); an unknown endpoint answers %d %q (%s)",
					method, who != "", got.Code, got.Body.String(), got.Header().Get("Content-Type"),
					want.Code, want.Body.String(), want.Header().Get("Content-Type"))
			}
		}
	}
}

// TestOnlyAnMCPTokenReachesTheMCPEndpoint: with MCP on, an ordinary read or
// admin token is refused with a 403 that says what to do, and an MCP token is
// served.
//
// Mutation: drop the IsMCPScope check in mcpEndpoint. Observed FAIL.
func TestOnlyAnMCPTokenReachesTheMCPEndpoint(t *testing.T) {
	rig := newMCPRig(t)
	for _, scope := range []string{db.ScopeRead, db.ScopeAdmin} {
		tok := createScopedToken(t, rig.h, rig.sign, "not-mcp-"+scope, scope)
		w := do(t, rig.h, mcpRawPost(tok))
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "not an MCP token") {
			t.Errorf("a %s token at /mcp answered %d: %s", scope, w.Code, w.Body.String())
		}
	}
	if w := do(t, rig.h, mcpRawPost(rig.admin)); w.Code != http.StatusOK {
		t.Fatalf("an MCP token at /mcp answered %d: %s", w.Code, w.Body.String())
	}
}

// TestAnMCPTokenConfersNothingAnywhereElse is the overlay-key isolation test
// for the MCP token, with MCP ON and every level on -- so /mcp is live and the
// token is good there, which is the condition under which a leak elsewhere
// would matter. On every other route, in every presentation, the answer must
// equal what an anonymous caller gets.
//
// Mutation: drop the path check in authenticate (accept an MCP token
// anywhere). Observed FAIL.
func TestAnMCPTokenConfersNothingAnywhereElse(t *testing.T) {
	rig := newMCPRig(t)
	tok := rig.admin
	presentations := map[string]func(*http.Request){
		"bearer": func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) },
		"query":  func(r *http.Request) { q := r.URL.Query(); q.Set("token", tok); r.URL.RawQuery = q.Encode() },
		"cookie": func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "polyemesis_session", Value: tok}) },
	}
	ip := 0
	request := func(method, path string, mut func(*http.Request)) int {
		ip++
		r := httptest.NewRequest(method, path, bytes.NewReader([]byte("{}")))
		r.Header.Set("Content-Type", "application/json")
		r.RemoteAddr = "198.51.100." + strconv.Itoa(ip%250+1) + ":5" + strconv.Itoa(ip%1000)
		if mut != nil {
			mut(r)
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		w := httptest.NewRecorder()
		rig.h.ServeHTTP(w, r.WithContext(ctx))
		return w.Code
	}
	n := 0
	for pair := range recordRegistrations(t, rig.s).expandedPairs() {
		method, pattern, _ := strings.Cut(pair, " ")
		if pattern == MCPPath {
			continue // the one route the token is for; driven above
		}
		path := concretePath(pattern)
		anon := request(method, path, nil)
		for name, mut := range presentations {
			if got := request(method, path, mut); got != anon {
				t.Errorf("%s %s with the MCP token as %s: %d, anonymous gets %d", method, pattern, name, got, anon)
			}
			n++
		}
	}
	if n < 500 {
		t.Fatalf("only %d requests were made; the population did not enumerate", n)
	}
}

// TestTheMCPToolLevelMatrix pins which tool is in which level -- written out,
// because a test that asks the catalogue for its own grouping cannot fail.
func TestTheMCPToolLevelMatrix(t *testing.T) {
	got := map[string][]string{}
	for _, tool := range mcpCatalogue() {
		l := mcpLevelOf(tool.tier)
		got[l] = append(got[l], tool.name)
	}
	for _, names := range got {
		sort.Strings(names)
	}
	want := map[string][]string{
		db.MCPLevelRead:    mcpWantReadTools,
		db.MCPLevelContent: mcpWantContentTools,
		db.MCPLevelControl: mcpWantWriteTools,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the tool/level matrix is\n  %v\nwant\n  %v", got, want)
	}
}

// TestALevelThatIsOffHasNoToolsAndRefusesADirectCall: the effective set is the
// token's tier intersected with the enabled levels, in tools/list and on a
// direct call, which is answered exactly as a read token's call of a write
// tool is.
//
// Mutation: make mcpToolAllowed ignore the levels. Observed FAIL.
func TestALevelThatIsOffHasNoToolsAndRefusesADirectCall(t *testing.T) {
	rig := newMCPRig(t)
	set := func(levels ...string) {
		if _, err := rig.store.PutMCPAccess(true, levels); err != nil {
			t.Fatal(err)
		}
	}
	join := func(lists ...[]string) []string {
		var out []string
		for _, l := range lists {
			out = append(out, l...)
		}
		sort.Strings(out)
		return out
	}
	cases := []struct {
		levels            []string
		wantRead, wantAdm []string
	}{
		{[]string{db.MCPLevelRead}, mcpWantReadTools, mcpWantReadTools},
		{[]string{db.MCPLevelRead, db.MCPLevelControl}, mcpWantReadTools, join(mcpWantReadTools, mcpWantWriteTools)},
		{[]string{db.MCPLevelContent}, nil, mcpWantContentTools},
		{db.MCPLevels(), mcpWantReadTools, join(mcpWantReadTools, mcpWantContentTools, mcpWantWriteTools)},
		{[]string{}, nil, nil},
	}
	for _, c := range cases {
		set(c.levels...)
		for _, who := range []struct {
			tok  string
			want []string
		}{{rig.read, c.wantRead}, {rig.admin, c.wantAdm}} {
			got := toolNames(t, mcpClient(t, rig.h, who.tok))
			if len(got) == 0 && len(who.want) == 0 {
				continue
			}
			if !reflect.DeepEqual(got, who.want) {
				t.Errorf("levels %v, token %s: tools/list\n  %v\nwant\n  %v", c.levels,
					map[bool]string{true: "read", false: "admin"}[who.tok == rig.read], got, who.want)
			}
		}
	}

	// The direct call. A read token naming a write tool is the reference
	// answer; an admin token naming a tool whose level is off must get the
	// same one.
	set(db.MCPLevelRead)
	_, refErr := callTool(rig.h, rig.read, "stop_destination", map[string]any{"destinationId": 1, "confirm": true})
	_, offErr := callTool(rig.h, rig.admin, "stop_destination", map[string]any{"destinationId": 1, "confirm": true})
	if refErr == nil || offErr == nil {
		t.Fatalf("a call to a tool that is not offered succeeded (read: %v, admin with control off: %v)", refErr, offErr)
	}
	if refErr.Error() != offErr.Error() {
		t.Errorf("a switched-off level answers %q; a read token's write call answers %q -- they must not differ",
			offErr, refErr)
	}
	if evs := rig.mcpEvents(); len(evs) != 0 {
		t.Fatalf("a call to a switched-off tool dispatched a write: %+v", evs)
	}

	// And the resources are Read: off with it.
	set(db.MCPLevelControl)
	cs := mcpClient(t, rig.h, rig.admin)
	if res, err := cs.ListResources(context.Background(), nil); err == nil && len(res.Resources) != 0 {
		t.Errorf("resources were listed with Read off: %v", res.Resources)
	}
}

// callTool is one tools/call as its own session, returning the protocol error
// a tool that is not registered produces.
func callTool(h http.Handler, token, name string, args map[string]any) (*mcp.CallToolResult, error) {
	srv := httptest.NewServer(h)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "probe", Version: "1"}, nil)
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             srv.URL + MCPPath,
		HTTPClient:           &http.Client{Transport: bearerTransport{token: token, base: http.DefaultTransport}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cs.Close() }()
	return cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
}

// TestNoTokenCanChangeTheMCPSettings: the settings route is session-only. An
// admin API token -- REST or MCP -- is refused, so a stolen token cannot turn
// MCP on for itself. The session is the positive control.
func TestNoTokenCanChangeTheMCPSettings(t *testing.T) {
	h, store, sign := plantedServer(t)
	for _, scope := range []string{db.ScopeAdmin, db.ScopeMCPAdmin} {
		tok := createScopedToken(t, h, sign, "t-"+scope, scope)
		for _, method := range []string{http.MethodGet, http.MethodPut} {
			r := jsonRequest(t, method, "/api/v1/mcp/access", map[string]any{"enabled": true})
			r.Header.Set("Authorization", "Bearer "+tok)
			if w := do(t, h, r); w.Code < 400 {
				t.Errorf("%s /api/v1/mcp/access with a %s token answered %d", method, scope, w.Code)
			}
		}
	}
	if a, _ := store.GetMCPAccess(); a.Enabled {
		t.Fatal("a token switched MCP on")
	}
	r := jsonRequest(t, http.MethodPut, "/api/v1/mcp/access", map[string]any{"enabled": true})
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("the signed-in operator could not switch MCP on: %d %s", w.Code, w.Body.String())
	}
}

// TestTheFirstEnableTurnsOnReadOnlyAndEveryChangeIsAudited: switching MCP on
// without naming levels gives Read and nothing else; every change is an audit
// event naming before and after; enabling Control is critical; a save that
// changes nothing is not audited.
//
// Mutation: drop the publishAudit call. Observed FAIL. Mutation: make every
// change a warning. Observed FAIL.
func TestTheFirstEnableTurnsOnReadOnlyAndEveryChangeIsAudited(t *testing.T) {
	h, store, sign := plantedServer(t)
	audit := captureAudit(t, serverUnderTest(t, h))
	put := func(body map[string]any) {
		t.Helper()
		r := jsonRequest(t, http.MethodPut, "/api/v1/mcp/access", body)
		sign(r)
		if w := do(t, h, r); w.Code != http.StatusOK {
			t.Fatalf("PUT %v answered %d: %s", body, w.Code, w.Body.String())
		}
	}
	events := func() []alerts.Event {
		var out []alerts.Event
		for _, ev := range *audit {
			if ev.Type == alerts.TypeSettingsChanged && ev.Title == "MCP access changed" {
				out = append(out, ev)
			}
		}
		return out
	}

	put(map[string]any{"enabled": true})
	if a, _ := store.GetMCPAccess(); !a.Enabled || !reflect.DeepEqual(a.Levels, []string{db.MCPLevelRead}) {
		t.Fatalf("the first enable stored %+v; want on with read only", a)
	}
	put(map[string]any{"enabled": true, "levels": []string{"read", "control"}})
	put(map[string]any{"enabled": true, "levels": []string{"control", "read"}}) // the same set
	put(map[string]any{"enabled": false})

	evs := events()
	if len(evs) != 3 {
		t.Fatalf("%d MCP audit events for three changes and one no-op: %+v", len(evs), evs)
	}
	want := []struct {
		sev           alerts.Severity
		before, after string
	}{
		{alerts.SeverityWarning, "off", "on (read)"},
		{alerts.SeverityCritical, "on (read)", "on (read, control)"},
		{alerts.SeverityWarning, "on (read, control)", "off"},
	}
	for i, w := range want {
		if evs[i].Severity != w.sev || fieldOf(evs[i], "Before") != w.before || fieldOf(evs[i], "After") != w.after {
			t.Errorf("audit %d = %s %q -> %q; want %s %q -> %q", i, evs[i].Severity,
				fieldOf(evs[i], "Before"), fieldOf(evs[i], "After"), w.sev, w.before, w.after)
		}
	}

	r := jsonRequest(t, http.MethodPut, "/api/v1/mcp/access", map[string]any{"enabled": true, "levels": []string{"everything"}})
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusBadRequest {
		t.Errorf("an unknown level answered %d", w.Code)
	}
}
