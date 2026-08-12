package api

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// ISSUE #157, CONJUNCT 2. A 403 IS A REFUSAL AND A REFUSAL IS NOT EVIDENCE.
//
// The write surface has two words. `denied-differential` is the strong one:
// nonGetDifferentialCensus drives the pair at both privilege levels, requires
// the planted credential PRESENT for the admin and the 403 beside it, so the
// refusal is withholding something demonstrable. Seven pairs have it.
//
// The other 76 are `denied-by-method`, and until this file their entire evidence
// was an executed 403. That is an INVARIANT. It records that a read principal was
// refused and records nothing at all about whether anything was being withheld:
// blank every credential in the fixture and all 76 stay green, because 403 does
// not depend on the database holding anything. The G3 deferral said so in prose
// and summarised a one-off measurement -- "18 answered 2xx carrying no planted
// credential, 58 answered 4xx or 5xx" -- which is a number somebody ran once and
// typed. It went stale twice inside one review round (123, then 83, then 76).
//
// So the measurement is EXECUTED, every run, over a population DERIVED from the
// live classification rather than typed: every pair the ledger calls
// denied-by-method is driven as ADMIN with the same empty body, and no planted
// sentinel may appear in what comes back. That is the counter-experiment, and
// "it ran and found no differential" is now a fact about this build rather than
// a sentence in the artifact.
//
// THE WEAKNESS THIS DESIGN OWNS, stated here because the criterion this ledger
// is measured against names it as the weakest part of the whole thing: a
// negative measurement is BYTE-IDENTICAL TO WHAT A VACUOUS HARNESS PRODUCES. A
// loop that issues no requests, a fixture that plants no credentials, a token
// that is refused before any handler runs -- each of them reports "no
// differential found" exactly as loudly as a correct run.
//
// The answer is a positive control ON THE DETECTOR, run through THE SAME CODE
// PATH, twice:
//
//   - BEFORE the sweep, so a detector that cannot see a disclosure is caught
//     before its silence is read as evidence.
//   - AFTER the sweep, so a fixture that was POISONED by one of the 76 drives
//     is caught too. That hazard is not hypothetical: the first version of the
//     differential census shared one fixture in sorted order and went red
//     because PUT /api/v1/settings writes its ingest block through to the
//     default source row, and every measurement after it was taken against
//     credentials that had already been replaced.
//
// What it still does not answer is "the declaration is wrong about what it
// declares" -- a pair that discloses through a path the admin drive does not
// reach, because {} is not a payload it accepts. Those are counted and reported
// on every run rather than left implicit; see the reachedHandler tally.

// declaredInvarianceControl is the pair the detector is tested against.
//
// It is READ OUT OF the differential census rather than written here, so the two
// cannot drift: the control is by construction a pair this package has
// independently measured to hand an admin a stored credential. A control typed
// separately is a control that goes stale in the direction of passing.
func declaredInvarianceControl(t *testing.T) nonGetDifferentialRow {
	t.Helper()
	rows := nonGetDifferentialCensus()
	for _, r := range rows {
		// Not a row with a Destroys list: the control runs twice, and a control
		// that clobbers the fixture would make its own second run fail.
		if len(r.Destroys) == 0 && len(r.Sentinels) > 0 {
			return r
		}
	}
	t.Fatal("no row in nonGetDifferentialCensus is usable as a positive control: every " +
		"one either names no sentinel or destroys stored configuration. Without a " +
		"control the invariance sweep below is a negative measurement with nothing " +
		"establishing that its detector can see anything, which is the exact failure " +
		"mode this file is written against.")
	return nonGetDifferentialRow{}
}

// assertDeclaredInvariance is the counter-experiment, over a DERIVED population.
//
// It takes the classified route list rather than a literal, so the set it drives
// is whatever the ledger currently calls denied-by-method. A pair that moves
// into or out of that word moves into or out of this sweep on the same run, with
// no list to remember to edit -- which is the same rule route_population_test.go
// applies to the mux's terminals, one word further in.
//
// Returns how many of the pairs REACHED a handler (2xx), which is reported
// rather than asserted. It is not a criterion: it is the honest width of the
// measurement, and a count asserted for equality here would be falsified by
// regeneration carrying no information, which is what happened to G3's 123.
func assertDeclaredInvariance(t *testing.T, enumerated []coverageRoute) int {
	t.Helper()

	var pairs []coverageRoute
	for _, r := range enumerated {
		if r.Coverage == "denied-by-method" {
			pairs = append(pairs, r)
		}
	}
	if len(pairs) == 0 {
		t.Fatal("no route is classified denied-by-method, so this counter-experiment drove " +
			"nothing. Either every non-GET pair has acquired a real differential -- in " +
			"which case delete this file and say so -- or classifyRoutes has stopped " +
			"producing that word, and every sentence about the 76 invariants is now about " +
			"an empty set.")
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Pattern != pairs[j].Pattern {
			return pairs[i].Pattern < pairs[j].Pattern
		}
		return pairs[i].Method < pairs[j].Method
	})

	// A FIXTURE OF THIS SWEEP'S OWN, RE-PLANTED WHENEVER A DRIVE POISONS IT.
	//
	// The bracket design -- control, seventy-six drives, control -- was written
	// first and went red on the very first run: DELETE /api/v1/destinations/{id}
	// is in this population, and after it the control's own row is a 404. That
	// is the tripwire working, and it is also proof that the bracket is not
	// enough: it says "something in there poisoned the fixture" without saying
	// which measurements were taken before it and which after.
	//
	// So the control runs after EVERY drive, which costs one extra request per
	// pair and makes each measurement individually valid: a silence is only ever
	// recorded on a fixture whose disclosure was demonstrable at that moment.
	// When the control falls, the pair that just ran is what destroyed it, the
	// fixture is re-planted, and the re-planted control must hold or the
	// detector -- not the fixture -- is what is broken.
	control := declaredInvarianceControl(t)
	h, admin := plantInvarianceFixture(t)
	driveControl(t, h, admin, control, "BEFORE the sweep")

	reached := 0
	var destructive []string
	for _, p := range pairs {
		r := jsonRequest(t, p.Method, concretePath(p.Pattern), map[string]any{})
		bearer(admin)(r)
		w := do(t, h, r)
		if w.Code/100 == 2 {
			reached++
		}
		body := w.Body.String()
		for _, secret := range append(allSentinels(), argvSentinels()...) {
			if !strings.Contains(body, secret) {
				continue
			}
			t.Errorf("THE COUNTER-EXPERIMENT FOUND A DIFFERENTIAL. %s %s answered an ADMIN "+
				"principal %d carrying %s, and the ledger classifies that pair as "+
				"denied-by-method -- which is a claim that its 403 to a read scope is an "+
				"invariant with nothing behind it.\n"+
				"It is not an invariant. A credential an admin receives and a read scope "+
				"is refused is a DIFFERENTIAL, and it belongs in nonGetDifferentialCensus "+
				"with its sentinels named, so the 403 is asserted next to the disclosure "+
				"it is withholding and nonGetDifferentialFloor counts it.\nbody: %s",
				p.Method, p.Pattern, w.Code, secret, truncateForFailure(body))
		}

		if controlHolds(t, h, admin, control) {
			continue
		}
		// This pair destroyed the thing the detector reads. Its own measurement
		// above is still sound -- the control held immediately before it -- and
		// nothing after it would be, so the fixture is rebuilt here.
		destructive = append(destructive, p.Method+" "+p.Pattern)
		h, admin = plantInvarianceFixture(t)
		driveControl(t, h, admin, control,
			"after re-planting the fixture that "+p.Method+" "+p.Pattern+" destroyed")
	}

	t.Logf("declared invariance: %d denied-by-method pairs driven as admin with {}, each "+
		"on a fixture whose disclosure was demonstrable immediately before and after "+
		"the drive; none disclosed a planted credential. %d reached a handler and "+
		"answered 2xx; the other %d answered 4xx or 5xx -- no such row in this fixture, "+
		"subsystem not running, or {} is not a payload they accept -- so for those the "+
		"counter-experiment ran and could not reach a body. That residual is REPORTED "+
		"rather than asserted: it is the honest width of the measurement, and a count "+
		"asserted for equality here is exactly the stale 123 this replaces.\n"+
		"pairs that destroyed the control's row and forced a re-plant: %v",
		len(pairs), reached, len(pairs)-reached, destructive)
	return reached
}

// plantInvarianceFixture is one throwaway server with an admin bearer.
func plantInvarianceFixture(t *testing.T) (http.Handler, string) {
	t.Helper()
	h, _, sign := plantedServer(t)
	return h, createScopedToken(t, h, sign, "declared-invariance-admin", db.ScopeAdmin)
}

// controlHolds is driveControl without the failure: it reports whether the
// disclosure is still visible, so the caller can tell "the fixture was
// destroyed" from "the detector is broken".
func controlHolds(t *testing.T, h http.Handler, admin string, row nonGetDifferentialRow) bool {
	t.Helper()
	r := jsonRequest(t, row.Method, concretePath(row.Pattern), row.Body)
	bearer(admin)(r)
	w := do(t, h, r)
	if w.Code/100 != 2 {
		return false
	}
	for _, secret := range row.Sentinels {
		if !strings.Contains(w.Body.String(), secret) {
			return false
		}
	}
	return true
}

// driveControl runs the positive control through the SAME request path the sweep
// uses and requires the disclosure to be visible.
func driveControl(t *testing.T, h http.Handler, admin string, row nonGetDifferentialRow, when string) {
	t.Helper()
	r := jsonRequest(t, row.Method, concretePath(row.Pattern), row.Body)
	bearer(admin)(r)
	w := do(t, h, r)
	if w.Code/100 != 2 {
		t.Fatalf("THE INVARIANCE DETECTOR'S POSITIVE CONTROL DID NOT REACH A HANDLER (%s the "+
			"sweep). %s %s answered an admin %d with %d bytes, and the differential census "+
			"records it as handing that principal %s.\n"+
			"Every result in this file is a NEGATIVE measurement -- \"no sentinel was "+
			"found\" -- and a negative measurement from a detector that cannot see a "+
			"positive is indistinguishable from a loop that issued no requests. Nothing "+
			"below this line means anything until it passes.\nbody: %s",
			when, row.Method, row.Pattern, w.Code, w.Body.Len(), row.Why,
			truncateForFailure(w.Body.String()))
	}
	for _, secret := range row.Sentinels {
		if !strings.Contains(w.Body.String(), secret) {
			t.Fatalf("THE INVARIANCE DETECTOR'S POSITIVE CONTROL FELL (%s the sweep). %s %s "+
				"hands an admin a %d-byte 2xx and it does not contain %s, which the "+
				"differential census records it as carrying (%s).\n"+
				"%s: if this is the BEFORE control the detector cannot see a disclosure at "+
				"all and every silence in this file is vacuous; if it is the AFTER control "+
				"the detector worked and one of the drives above CLOBBERED the fixture, so "+
				"every measurement taken after that drive was made against credentials that "+
				"were no longer there.\nbody: %s",
				when, row.Method, row.Pattern, w.Body.Len(), secret, row.Why, when,
				truncateForFailure(w.Body.String()))
		}
	}
}

// TestEveryDeniedByMethodPairHasAnExecutedCounterExperiment is the standalone
// entry point. The work is in assertDeclaredInvariance, which TestLedgerPreflight
// CALLS, for the two reasons measured on ledger_ratchet_test.go: a file nothing
// references is deletable in silence, and TestMain forces only
// ^TestLedgerPreflight$.
func TestEveryDeniedByMethodPairHasAnExecutedCounterExperiment(t *testing.T) {
	h, _, sign := plantedServer(t)
	// classifyRoutes mints a read token through this handler, and readScopeToken
	// reaches the session through ledgerSessions. The preflight registers it;
	// this entry point has to as well, or the classification panics on a nil
	// signer rather than reporting anything.
	ledgerSessions[h] = sign
	enumerated, _ := classifyRoutes(t, h, serverUnderTest(t, h))
	assertDeclaredInvariance(t, enumerated)
}
