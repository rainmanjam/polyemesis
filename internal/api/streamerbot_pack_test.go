package api

import (
	"encoding/json"
	"net/http"
	"os"
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

func TestPolyemesisCsCallsOnlyThePathsTheManifestLists(t *testing.T) {
	raw, err := os.ReadFile("../../integrations/streamerbot/Polyemesis.cs")
	if err != nil {
		t.Fatal(err)
	}
	code := string(raw)
	pack := loadPack(t)
	// Every method the manifest names exists in the code.
	for _, a := range pack {
		m := strings.TrimPrefix(a.Method, "Execute C# Method: ")
		if !strings.Contains(code, "public bool "+m+"()") {
			t.Errorf("%s names method %s, which Polyemesis.cs does not define", a.Name, m)
		}
	}
	// Every path literal in the code is the leading part of a manifest path.
	lit := regexp.MustCompile(`"(/api/v1/[^"]*)"`)
	found := lit.FindAllStringSubmatch(code, -1)
	if len(found) < 5 {
		t.Fatalf("found %d path literals in Polyemesis.cs; the pattern is looking at the wrong thing", len(found))
	}
	for _, m := range found {
		known := false
		for _, a := range pack {
			if strings.HasPrefix(a.Path, m[1]) {
				known = true
				break
			}
		}
		if !known {
			t.Errorf("Polyemesis.cs calls %s, which actions.json does not list", m[1])
		}
	}
	if strings.Contains(code, "LogInfo(token") || strings.Contains(code, "+ token") {
		t.Error("Polyemesis.cs logs or concatenates the token")
	}
}
