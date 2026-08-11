package api

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// THE NON-GET DIFFERENTIAL CENSUS. #157's residual, and #168's
// invariant-is-weaker-than-differential problem applied to the write surface.
//
// WHAT WAS ALREADY TRUE, so that this file is not sold as more than it is.
// Every one of the 123 non-GET method+pattern pairs is already DRIVEN with a
// read-scoped bearer: 83 of them by readScopeIsRefused, which issues a real
// request and classifies on an executed 403; 38 by driveExcuse, which issues one
// request per pair; 2 by readScopeWriteSweep, which reads the bytes. The
// issue's headline -- "enumerated but never read" -- stopped being true of the
// enumeration some rounds ago.
//
// WHAT WAS NOT TRUE, and is what this file adds. "denied-by-method" is an
// INVARIANT: it records that a read principal was refused. It records nothing
// about whether anything was being withheld. Blank every credential in the
// fixture and all 83 pairs stay green, because 403 does not depend on the
// database having anything in it. That is the precise shape counterpartProofs
// refuses for the routes it covers -- proofResult.High "MUST contain a
// sentinel: that is the differential positive control, and without it 'the read
// principal saw no sentinel' is a statement about an empty fixture" -- and the
// write surface had no equivalent.
//
// So each row below drives the SAME pair twice, against the same fixture, with
// the same request body:
//
//   - as ADMIN, and every sentinel named on the row must be PRESENT. This is
//     the positive control. It is what makes the 403 next to it mean
//     "withheld" rather than "there was nothing there".
//   - as READ, and the status must be exactly 403 with no sentinel in the body.
//
// PUT /api/v1/settings is why this matters rather than being a formality. It
// answers an admin 200 with the stored db.Settings inlined at the top level --
// eight planted credentials, unmasked, in the response to a write. That is the
// exact shape #157 named as what would make the deferral unsafe ("a POST/PUT
// that echoes the stored row back in its 200 body, the normal REST idiom, and
// the exact shape that leaked on GET"), and it is live in this API today. It is
// safe only because requireScope refuses the method. Nothing was asserting the
// pairing of those two facts, so the day the pattern joins
// readScopeWritePatterns -- a one-line edit, and the list already grew once for
// a route whose answer was a stream key -- eight credentials reach a read token
// and this package stays green.

// nonGetDifferentialRow is one pair, driven at two privilege levels.
type nonGetDifferentialRow struct {
	Method, Pattern string
	// Body is the request payload. An empty JSON object is deliberate on the
	// PUT rows: every one of those handlers decodes over the STORED document,
	// so {} is the payload that changes nothing -- which is what lets the whole
	// census share one fixture and what makes the witness check at the end an
	// assertion rather than a formality.
	Body any
	// Sentinels MUST ALL appear in the admin response. A row with an empty list
	// is rejected: it would drive two requests and assert nothing about
	// disclosure, which is the vacuity this ledger exists to refuse.
	Sentinels []string
	// Destroys is every planted credential that is NO LONGER witnessed anywhere
	// in the fixture after this row's admin drive, and it is asserted EXACTLY:
	// a row that destroys something it does not declare fails, and so does a
	// row that declares something it does not destroy.
	//
	// It is not bookkeeping. This is what a write route DOES to stored
	// configuration, measured rather than reasoned about, and it is the half of
	// this census that a GET-only sweep can never have: PUT /api/v1/settings
	// writes its ingest block through to the default source row -- deliberately,
	// see handlePutSettings -- so an empty-body PUT replaces the source's
	// planted SRT passphrase, RTMP key and pull password with the settings-level
	// ones. A write route that begins clobbering a credential it did not
	// clobber before fails here, naming it.
	Destroys []string
	// Why records what the admin body is, in one line, so a reviewer can tell
	// an echo of stored configuration from a status envelope.
	Why string
}

// nonGetDifferentialCensus is the tier-1 set: every non-GET pair MEASURED to
// hand an admin a planted credential.
//
// MEASURED, NOT CHOSEN, and COMPLETE over that measurement. All 83 non-GET
// pairs the ledger classifies as denied were driven as admin with {} against a
// per-pair fixture; these seven are every pair that came back 2xx with a
// planted credential in the body. The other 76 are recorded in the #157
// deferral row with what the measurement could and could not see, rather than
// being swept anyway: asserting absence over a status envelope or a 404 is
// #165's defect, and it would fill this census with rows that cannot fail.
//
// SORTED by (pattern, method), and asserted sorted below.
func nonGetDifferentialCensus() []nonGetDifferentialRow {
	return []nonGetDifferentialRow{
		{
			Method: http.MethodPut, Pattern: "/api/v1/destinations/{id}",
			Body: map[string]any{},
			Sentinels: []string{
				sentinelDestKey, sentinelDestBackupKey, sentinelExpertArgs,
			},
			Why: "echoes the stored destination, expert argv included",
		},
		{
			Method: http.MethodDelete, Pattern: "/api/v1/destinations/{id}/expert",
			Body:      map[string]any{},
			Sentinels: []string{sentinelDestKey},
			// It clears the expert argv columns and answers with the
			// destination as it now stands, so the argv sentinel that only
			// those columns carry stops being witnessed anywhere. Declared
			// rather than avoided: this row is why the census cannot share a
			// fixture, and it is in the census rather than excluded from it
			// because per-row fixtures made that a free choice.
			Destroys: []string{sentinelExpertArgs},
			Why:      "answers a clear-expert-mode with the whole destination row",
		},
		{
			Method: http.MethodPost, Pattern: "/api/v1/destinations/{id}/expert/preview",
			Body:      map[string]any{},
			Sentinels: []string{sentinelDestKey},
			Why:       "renders the resolved ffmpeg argv, which embeds the stream key",
		},
		{
			Method: http.MethodPut, Pattern: "/api/v1/playout/publish",
			Body:      map[string]any{},
			Sentinels: []string{sentinelPlayoutToken},
			Why:       "echoes the saved playout config, playback token included",
		},
		{
			Method: http.MethodPut, Pattern: "/api/v1/settings",
			Body: map[string]any{},
			Sentinels: []string{
				sentinelSetSRT, sentinelSetRTMP, sentinelSetPullPwd,
				sentinelBackupSRT, sentinelBackupRTMP, sentinelBackupPullPwd,
				sentinelMQTTPwd, sentinelAutomodKey,
			},
			// THE ONE ROW WITH A SIDE EFFECT, and it is a documented one. The
			// ingest block reaches the default SOURCE as well as the settings
			// document, because before sources existed settings.ingest WAS the
			// ingest and the editor on the settings page would otherwise be
			// dead. This fixture plants a different credential in each of the
			// two places, so ingestEqual is false and the write-through fires
			// on an empty body; a production install where the two already
			// agree sees no write at all.
			Destroys: []string{
				sentinelSourceSRT, sentinelSourceRTMP, sentinelSourcePullPwd,
			},
			Why: "inlines the whole stored db.Settings at the top level of the 200",
		},
		{
			Method: http.MethodPut, Pattern: "/api/v1/sources/{id}",
			Body: map[string]any{},
			Sentinels: []string{
				sentinelSourceSRT, sentinelSourceRTMP, sentinelSourcePullPwd,
			},
			Why: "echoes the stored source row",
		},
		{
			Method: http.MethodPost, Pattern: "/api/v1/sources/{id}/token",
			Body: map[string]any{},
			Sentinels: []string{
				sentinelSourceSRT, sentinelSourceRTMP, sentinelSourcePullPwd,
			},
			Why: "rotates the publish secret and echoes the whole source row back",
		},
	}
}

// nonGetDifferentialPairs is the census as a lookup, so classifyRoutes can give
// these pairs their own coverage word.
//
// That word is what wires this file into the machinery that already exists:
// assertRouteSetsEqual compares the coverage string of every pair on every
// plain run, so deleting a row here moves six pairs back to
// "denied-by-method" in the live classification and fails by name against the
// committed artifact -- independently of, and in addition to, the floor below.
// One detector is a thing somebody can regenerate around; the floor is
// max()-clamped and the equality is not launderable by -update-coverage in the
// direction that matters, so a deletion has to survive both.
func nonGetDifferentialPairs() map[string]bool {
	m := map[string]bool{}
	for _, row := range nonGetDifferentialCensus() {
		m[row.Method+" "+row.Pattern] = true
	}
	return m
}

// assertCensusPairsAreClassified is the orphan check, and it is the same defect
// assertSweptCounterpartsNameSweptRoutes catches one section over: a row naming
// a pair the router does not serve, or one the ledger classifies some other
// way, is a row whose two drives prove something about a route nobody reaches
// through this ledger. concretePath would still build a path for it and the
// requests would still be issued, so nothing else here would notice.
func assertCensusPairsAreClassified(t *testing.T, enumerated []coverageRoute) {
	t.Helper()
	coverage := map[string]string{}
	for _, r := range enumerated {
		coverage[r.Method+" "+r.Pattern] = r.Coverage
	}
	for _, row := range nonGetDifferentialCensus() {
		key := row.Method + " " + row.Pattern
		switch cov, ok := coverage[key]; {
		case !ok:
			t.Errorf("nonGetDifferentialCensus names %s, which the router does not serve. "+
				"Delete the row rather than leaving a census that drives a path chi never "+
				"matched.", key)
		case cov != "denied-differential":
			t.Errorf("nonGetDifferentialCensus names %s and the ledger classifies it as "+
				"%q. The census exists to upgrade a pair from denied-by-method to a real "+
				"differential; a pair that is swept or excused is covered elsewhere and "+
				"this row is asserting the same bytes twice under a name that claims "+
				"otherwise.", key, cov)
		}
	}
}

// assertNonGetDifferential runs the census and returns the number of
// (pair, sentinel) witnesses observed.
//
// ONE THROWAWAY FIXTURE PER ROW, and that is a correction the first run of this
// census forced rather than a preference. The design it replaces was the one
// the round that specified this census asked for -- a single fixture, driven in
// sorted order, with assertEverySentinelIsWitnessed at the end -- and it went
// red immediately, on the check that was meant to be the tripwire:
//
//	THE POSITIVE CONTROL FELL ON PUT /api/v1/sources/{id}. The census records
//	that this pair hands an admin SENTINEL-source-srt-passphrase-9f3a [...] and
//	the 646-byte 2xx body does not contain it.
//
// PUT /api/v1/settings sorts before /api/v1/sources/{id} and writes its ingest
// block through to the default source row, so by the time the two source rows
// ran, their planted credentials had been replaced by the settings-level ones.
// The tripwire fired as designed; a shared fixture in sorted order is simply
// not available over this route set. Per-row fixtures make the hazard
// structurally impossible rather than merely detected -- and the detection is
// not thrown away, it moves into Destroys, asserted EXACTLY per row against the
// same sweep the shared design would have run once.
//
// The rows stay sorted and the sort stays asserted: a census that reorders
// itself produces an artifact diff nobody can read, and a committed ledger
// nobody reads is the failure mode one level up from this one.
func assertNonGetDifferential(t *testing.T) int {
	t.Helper()

	rows := nonGetDifferentialCensus()
	if len(rows) == 0 {
		t.Fatal("the non-GET differential census is empty. An empty census reports zero " +
			"witnesses, the floor is zero, and every sentence in this file describes a " +
			"loop that does not execute.")
	}
	sorted := sort.SliceIsSorted(rows, func(i, j int) bool {
		if rows[i].Pattern != rows[j].Pattern {
			return rows[i].Pattern < rows[j].Pattern
		}
		return rows[i].Method < rows[j].Method
	})
	if !sorted {
		t.Errorf("nonGetDifferentialCensus is not in (pattern, method) order. Sort the " +
			"literal: a census that reorders itself produces an artifact diff nobody can " +
			"read, and the whole value of committing this ledger is that a reviewer can " +
			"see exactly what moved.")
	}

	witnesses := 0
	for _, row := range rows {
		key := row.Method + " " + row.Pattern
		path := concretePath(row.Pattern)

		if len(row.Sentinels) == 0 {
			t.Errorf("the census row for %s names no sentinel. Such a row drives two "+
				"requests and asserts nothing about disclosure -- it is an invariant "+
				"wearing a differential's name, which is the whole defect this file "+
				"exists to close. Delete the row or name what the admin body carries.", key)
			continue
		}

		// A FIXTURE OF THIS ROW'S OWN. See the doc comment for the measurement
		// that made per-row isolation the only workable arrangement.
		h, _, sign := plantedServer(t)
		admin := createScopedToken(t, h, sign, "nonget-differential-admin", db.ScopeAdmin)
		read := createScopedToken(t, h, sign, "nonget-differential-read", db.ScopeRead)

		// THE DISCLOSURE HALF. Admin, and the sentinels must be THERE.
		hi := jsonRequest(t, row.Method, path, row.Body)
		bearer(admin)(hi)
		hw := do(t, h, hi)
		if hw.Code/100 != 2 {
			t.Errorf("%s answered an ADMIN principal %d with %d bytes, and this census "+
				"claims it hands that principal a stored credential (%s). A non-2xx here "+
				"means the positive control never reached the handler -- the request body "+
				"in nonGetDifferentialCensus is wrong, or the fixture no longer has the "+
				"row this addresses -- and every sentence about the 403 below it is a "+
				"statement about an error message.\nbody: %s",
				key, hw.Code, hw.Body.Len(), row.Why, truncateForFailure(hw.Body.String()))
			continue
		}
		for _, secret := range row.Sentinels {
			if !strings.Contains(hw.Body.String(), secret) {
				t.Errorf("THE POSITIVE CONTROL FELL ON %s. The census records that this "+
					"pair hands an admin %s (%s) and the %d-byte 2xx body does not contain "+
					"it. Either the fixture stopped planting that credential -- in which "+
					"case the 403 this pair also receives is withholding nothing and the "+
					"ledger's denied-differential verdict for it is empty -- or the "+
					"handler stopped emitting it, which is a real improvement that has to "+
					"be recorded by editing the row and lowering "+
					"nonGetDifferentialFloor in %s by hand.",
					key, secret, row.Why, hw.Body.Len(), coveragePath)
				continue
			}
			witnesses++
		}

		// THE DENIAL HALF. Same pair, same body, same fixture, read scope.
		lo := jsonRequest(t, row.Method, path, row.Body)
		bearer(read)(lo)
		lw := do(t, h, lo)
		if lw.Code != http.StatusForbidden {
			t.Errorf("%s answered a READ-scoped token %d with %d bytes, and the ledger "+
				"classifies it as denied. An admin receives %d planted credentials from "+
				"this exact request. If this pair has joined readScopeWritePatterns it "+
				"must move to readScopeWriteSweep and have its bytes read; it may not stay "+
				"here.\nbody: %s",
				key, lw.Code, lw.Body.Len(), len(row.Sentinels),
				truncateForFailure(lw.Body.String()))
			continue
		}
		for _, secret := range allSentinels() {
			if strings.Contains(lw.Body.String(), secret) {
				t.Errorf("%s refused a read-scoped token with 403 AND put %s in the refusal "+
					"body.\nbody: %s", key, secret, lw.Body.String())
			}
		}

		// WHAT THE WRITE DID TO STORED CONFIGURATION. The same sweep
		// assertEverySentinelIsWitnessed runs, on the fixture this row has just
		// driven, compared EXACTLY against the row's declaration.
		//
		// Both directions matter. An undeclared loss is a write route that has
		// started clobbering a credential -- the finding. A declared loss that
		// does not happen is a Destroys list that has gone stale, which is the
		// prose-that-outlived-the-code failure this ledger keeps catching: a
		// list of side effects nobody re-measures becomes a list of side effects
		// somebody quotes.
		gone := sentinelsNotWitnessed(t, h, sign, "nonget-differential-witness")
		declaredButIntact, destroyedUndeclared := stringSetDiff(row.Destroys, gone)
		for _, secret := range destroyedUndeclared {
			t.Errorf("%s destroyed %s: after this one request the sweep over every swept "+
				"route finds that credential in NO high-privilege body, and the census row "+
				"does not declare it. A write route that clobbers stored configuration it "+
				"did not clobber before is the finding this line exists for -- confirm the "+
				"handler means to do it, then add the sentinel to that row's Destroys.",
				key, secret)
		}
		for _, secret := range declaredButIntact {
			t.Errorf("%s declares that it destroys %s and it does not: after the request "+
				"the credential is still witnessed. The declaration is now prose that "+
				"outlived the code, which is worse than no declaration because it gets "+
				"quoted. Re-measure and drop it from that row's Destroys.", key, secret)
		}
	}
	return witnesses
}

// TestNonGetPairsWithheldFromReadScopeAreRealDifferentials is the standalone
// entry point. The work is in assertNonGetDifferential, which TestLedgerPreflight
// CALLS -- so this file cannot be deleted without breaking the build, and the
// census runs under the -run filter the preflight forces. Both holes were
// measured in this package before: `rm internal/api/ledger_ratchet_test.go`
// deleted 152 lines and left the suite green.
func TestNonGetPairsWithheldFromReadScopeAreRealDifferentials(t *testing.T) {
	assertNonGetDifferential(t)
}
