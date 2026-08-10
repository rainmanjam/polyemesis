package api

import (
	"net/http"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// apiDocPath is docs/API.md relative to this package.
const apiDocPath = "../../docs/API.md"

// undocumentedByDesign is the ONE route that may be absent from docs/API.md.
//
// /api/v1/chat/kick/{secret} is the route whose EXISTENCE is the credential: it
// is mounted outside every authenticated group because Kick posts to it without
// one, and the unguessable path segment is the whole of the protection. Writing
// it into the published route table would hand away the fact that
// TestWrongKickSecretIsIndistinguishableFromAnUnroutedPath spends four axes of
// assertion keeping.
//
// It is a map with one entry rather than a `strings.Contains(p, "kick")` so that
// a second entry is a deliberate act somebody has to write down. Anything else
// missing from the table is a documentation gap, not a design decision.
var undocumentedByDesign = map[string]bool{
	"/api/v1/chat/kick/{secret}": true,
}

// TestEveryRouteIsInTheAPIDocument enforces the premise #158 was closed on.
//
// #158 -- chi answers 405 before any middleware runs, so an anonymous caller can
// tell a registered (method, path) pair from an unregistered one -- was closed
// as working-as-intended, and the FIRST of the four reasons is:
//
//	"Everything the oracle discloses about /api/v1 is published in docs/API.md's
//	route table. An oracle that reproduces a published document is not a leak."
//
// That reason is a claim about a file, and nothing kept it true. It had already
// drifted false by the time it was written: an audit for this PR found four
// routes missing and shipped them, and a review of that audit found a fifth,
// POST /platforms/credentials/{platform}/check, still absent. An accepted risk
// whose justification is false is not an accepted risk -- it is an unexamined
// one -- and a premise that drifted twice will drift again.
//
// So the premise is executable from here on. If you add a route to /api/v1, add
// it to docs/API.md; if you believe it must not be published, the alternative is
// not to leave it out of the document, it is to say so in undocumentedByDesign
// above and explain why, as the kick route does.
//
// WHAT THIS CHECKS AND WHAT IT DOES NOT. It compares PATHS, not (method, path)
// pairs. The Method column of those tables is prose written for a reader -- it
// merges verbs across rows, and spells one row's method inline in the Path cell
// -- and a parser strict enough to read it would fail on the next legitimate
// edit to the document's formatting, which is a bad trade for a check that
// blocks merges. Path coverage is what closes the drift that actually happened:
// every one of the five misses was a whole route absent from the table, not a
// verb missing from a row that was there.
func TestEveryRouteIsInTheAPIDocument(t *testing.T) {
	documented := documentedAPIPaths(t)
	if len(documented) < 100 {
		t.Fatalf("only %d paths were parsed out of %s. The document's table format has "+
			"changed and this check has gone vacuous -- fix the parser rather than the "+
			"threshold.", len(documented), apiDocPath)
	}

	seen := map[string]bool{}
	var missing []string
	for _, r := range registeredAPIRoutes(t) {
		if seen[r] {
			continue
		}
		seen[r] = true
		if undocumentedByDesign[r] {
			continue
		}
		if !documented[strings.TrimPrefix(r, "/api/v1")] {
			missing = append(missing, r)
		}
	}
	sort.Strings(missing)

	if len(missing) > 0 {
		t.Errorf("%d route(s) are registered on /api/v1 and absent from %s:\n  %s\n\n"+
			"The 405 method oracle discloses that these exist to any anonymous caller, and "+
			"#158 is closed on the grounds that everything it discloses is already "+
			"published. Publish them, or add them to undocumentedByDesign with the reason.",
			len(missing), apiDocPath, strings.Join(missing, "\n  "))
	}
}

// TestTheAPIDocumentDescribesNoRouteThatIsGone is the other direction.
//
// A table row for a route that no longer exists is the same failure read from
// the other end: the document stops being a description of this build, and the
// next person to check the premise above against it draws the wrong conclusion.
//
// Only literals written out IN FULL are checked. The tables abbreviate -- a cell
// reads `/destinations/{id}/start`, `/stop`, `/restart` -- and those
// continuations are expanded generously for the forward check above, on purpose:
// over-generating there can only ever forgive a route, never invent a failure.
// Running the same generous expansion backwards would invent paths and then
// complain they do not exist, so this direction uses only what the document
// actually spells.
func TestTheAPIDocumentDescribesNoRouteThatIsGone(t *testing.T) {
	registered := map[string]bool{}
	for _, r := range registeredAPIRoutes(t) {
		registered[strings.TrimPrefix(r, "/api/v1")] = true
	}

	var stale []string
	for _, p := range spelledAPIPaths(t) {
		if !registered[p] {
			stale = append(stale, p)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("%s documents %d path(s) this build does not serve:\n  %s\n\n"+
			"A route table that describes routes which are gone is worse than an incomplete "+
			"one: it reads as authoritative.", apiDocPath, len(stale), strings.Join(stale, "\n  "))
	}
}

// registeredAPIRoutes is every /api/v1 route pattern chi knows, deduplicated
// across methods.
func registeredAPIRoutes(t *testing.T) []string {
	t.Helper()
	h, _, _ := sourceServer(t)
	mux, ok := h.(*chi.Mux)
	if !ok {
		t.Fatalf("Server.Handler returned %T, not *chi.Mux; this check walks the real "+
			"router and cannot fall back to a list", h)
	}
	seen := map[string]bool{}
	var out []string
	err := chi.Walk(mux, func(_ string, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(route, "/api/v1/") || seen[route] {
			return nil
		}
		seen[route] = true
		out = append(out, route)
		return nil
	})
	if err != nil {
		t.Fatalf("walk the router: %v", err)
	}
	if len(out) < 100 {
		t.Fatalf("the walk found only %d routes under /api/v1, which cannot be right; this "+
			"check would pass against almost anything", len(out))
	}
	sort.Strings(out)
	return out
}

// docTableLiteral matches one backticked cell literal. Anchored away from
// newlines so a fenced code block cannot swallow the rest of the file -- which
// it does, silently, if the pattern is allowed to span lines.
var docTableLiteral = regexp.MustCompile("`([^`\n]+)`")

// docTableRows returns the backticked literals of every markdown table row,
// row by row. Prose is skipped entirely: a path named in a sentence is an
// explanation, and this check is about the tables that claim to be the list.
func docTableRows(t *testing.T) [][]string {
	t.Helper()
	b, err := os.ReadFile(apiDocPath)
	if err != nil {
		t.Fatalf("read %s: %v", apiDocPath, err)
	}
	var rows [][]string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		var lits []string
		for _, m := range docTableLiteral.FindAllStringSubmatch(line, -1) {
			lits = append(lits, strings.TrimSpace(m[1]))
		}
		if len(lits) > 0 {
			rows = append(rows, lits)
		}
	}
	return rows
}

// spelledAPIPaths is every path written out in full in a table row.
func spelledAPIPaths(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{}
	var out []string
	for _, row := range docTableRows(t) {
		first := true
		for _, lit := range row {
			if !strings.HasPrefix(lit, "/") {
				continue
			}
			// A continuation -- `/stop` after `/destinations/{id}/start` -- is
			// not a path in its own right, and asserting it exists would fail on
			// a document that is entirely correct. It is recognised by being a
			// SINGLE segment that is not the first path in its row: the first
			// one is always written in full, however short (`/status`,
			// `/health`), and every abbreviation is a bare last segment.
			full := first || strings.Count(lit, "/") > 1
			first = false
			if !full || seen[lit] {
				continue
			}
			seen[lit] = true
			out = append(out, lit)
		}
	}
	return out
}

// documentedAPIPaths expands a table's shorthand into the set of paths it
// names.
//
// The tables abbreviate a run of sibling routes, in two shapes:
//
//	`/destinations/{id}/start`, `/stop`, `/restart`   -- replace the last segment
//	`/clipper/recordings/{id}`, `/keyframes`          -- append a segment
//
// Both are generated for every continuation, and the union is what counts as
// documented. Generating both is deliberate: a spurious entry can only forgive a
// route that is genuinely in the table under the other reading, whereas guessing
// wrong in one direction would fail a merge over a document that is correct.
func documentedAPIPaths(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, row := range docTableRows(t) {
		prev := ""
		for _, lit := range row {
			if !strings.HasPrefix(lit, "/") {
				continue // a method, a header word, a type name
			}
			out[lit] = true
			if prev != "" {
				out[path.Join(path.Dir(prev), lit)] = true
				out[path.Join(prev, lit)] = true
			}
			// Only a FULL path becomes the base for the next continuation, so
			// `/a/b`, `/c`, `/d` reads as {/a/b, /a/c, /a/d} rather than
			// chaining off the expansion of /c.
			if strings.Count(lit, "/") > 1 {
				prev = lit
			}
		}
	}
	return out
}
