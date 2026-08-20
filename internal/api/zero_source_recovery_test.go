package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

/* A REFUSAL THAT NAMES AN ACTION MUST CLEAR WHEN YOU TAKE THAT ACTION, AND
 * NOTHING IN THIS PACKAGE ASSERTED THAT.
 *
 * The zero-source work is well covered from two directions and neither of them
 * is this one:
 *
 *   zero_source_guard_test.go drives the guarded routes on a fixture with NO
 *   source and asserts they refuse; then, in a SEPARATE test on a SEPARATE
 *   fixture that has a source from the moment it is built, asserts they do not.
 *
 *   fresh_install_walk_test.go walks every registered route on an install that
 *   has never had a source, looking for the route nobody considered.
 *
 * Both compare two worlds. Neither watches ONE INSTALL CROSS BETWEEN THEM, and
 * the crossing is the part an operator actually performs: they install
 * polyemesis, click something, are told "create a source on the Sources page",
 * create one, and click the same thing again.
 *
 * THE BUG THIS CATCHES AND THE OTHERS CANNOT. A guard that reads "has a source"
 * ONCE -- at construction, into a field, instead of per request -- passes both
 * existing tests perfectly. The no-source fixture never gains a source, so its
 * cached false is always right; the has-source fixture is built with one, so its
 * cached true is always right. Only a fixture that starts empty and then gains a
 * source can tell a live read from a remembered one, and an operator on a fresh
 * install is exactly that fixture.
 *
 * The failure would be about as bad as this product has: a first-run operator
 * does what the error told them to do, it does not work, and the only thing that
 * fixes it is restarting a service they have just installed and have no reason
 * to suspect.
 *
 * PORTED, NOT INVENTED. This assertion existed as an uncommitted
 * TestAFreshInstallCanCreateItsFirstSourceAndThenADestination in a two-day-old
 * worktree, 89 commits behind. Its own comment is the sentence worth keeping:
 * "A 409 that does not clear when the state it named is fixed would be a worse
 * lie than the 400 it replaced." What did NOT survive the port is its
 * expectation: it wanted 409, and this package settled on 503 with a
 * machine-readable code. The status is main's answer; the question was the old
 * test's.
 */

// noSourceCode reads the machine-readable reason out of a refusal, or "" when
// the response carries none.
//
// The CODE rather than the status, because that is the contract the UI reads:
// api.ts matches on it, and lib/no-source-code.test.ts pins the spelling against
// the Go constant. A 503 that stopped carrying the code would still be a 503 and
// would break the empty state without failing anything here.
func noSourceCode(t *testing.T, w interface{ Bytes() []byte }) string {
	t.Helper()
	var body apiError
	_ = json.Unmarshal(w.Bytes(), &body)
	return body.Code
}

func TestTheNoSourceRefusalClearsOnceTheOperatorCreatesASource(t *testing.T) {
	_, h, auth := zeroSourceServer(t)

	// One payload throughout, and the last step adds exactly one field to it.
	// A refusal that cleared only for a DIFFERENT request would not be this
	// feature working, so reusing the map is what stops that happening by
	// accident.
	//
	// The one field is sourceId, and adding it is not a weakening of this test.
	// Two different conditions are in play and only the first is its subject:
	// no_source says the INSTALL has no programme, and nothing the operator
	// types will help; source_required says this REQUEST did not pick one of
	// the programmes that now exist. Recovering from the first is what is being
	// asserted, and the last step still fails if the answer is no_source.
	dest := map[string]any{"name": "out", "kind": "rtmp", "url": "rtmp://example/live"}

	t.Run("first it refuses, and says what to do about it", func(t *testing.T) {
		r := jsonRequest(t, http.MethodPost, "/api/v1/destinations", dest)
		auth(r)
		w := do(t, h, r)

		if w.Code < 400 {
			t.Fatalf("status = %d, want a refusal: there is no programme to attach a "+
				"destination to (body %s)", w.Code, w.Body.String())
		}
		if got := noSourceCode(t, w.Body); got != codeNoSource {
			t.Fatalf("code = %q, want %q — the UI matches on this to draw the empty "+
				"state rather than a red toast (body %s)", got, codeNoSource, w.Body.String())
		}
		// The refusal has to name the next action, or an operator on their first
		// run is told no and not told why or what to do.
		if body := strings.ToLower(w.Body.String()); !strings.Contains(body, "source") {
			t.Errorf("the refusal does not mention a source, so it names no way "+
				"forward: %s", body)
		}
	})

	t.Run("then the operator does exactly what it said", func(t *testing.T) {
		r := jsonRequest(t, http.MethodPost, "/api/v1/sources", map[string]any{"name": "Main"})
		auth(r)
		if w := do(t, h, r); w.Code != http.StatusCreated {
			t.Fatalf("creating the first source: status = %d, want 201 (body %s). "+
				"If THIS is refused for want of a source the install is unrecoverable "+
				"by any action available to an operator.", w.Code, w.Body.String())
		}
	})

	t.Run("and the same request, now naming that source, works", func(t *testing.T) {
		// Named from what the server reports rather than assumed to be 1: the
		// point of this step is that the install recovered, and reading the id
		// back is part of showing that.
		dest["sourceId"] = onlySourceID(t, h, auth)
		r := jsonRequest(t, http.MethodPost, "/api/v1/destinations", dest)
		auth(r)
		w := do(t, h, r)

		if code := noSourceCode(t, w.Body); code == codeNoSource {
			t.Fatalf("still answering %q after a source was created: the guard is "+
				"reading a remembered answer rather than the current one, and the "+
				"operator's only remaining move is to restart a service they just "+
				"installed (status %d, body %s)", codeNoSource, w.Code, w.Body.String())
		}
		if w.Code >= 400 {
			t.Fatalf("status = %d, want the destination to be accepted now (body %s)",
				w.Code, w.Body.String())
		}
	})
}
