package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// THE VALUE SWEEP'S PARTITION.
//
// #165. The sweep asserted that the read-scoped body contains no sentinel. It
// never asserted that the HIGH-PRIVILEGE body contains one. A fixture that
// forgot to plant a credential therefore passed IDENTICALLY to a correct
// redaction -- which is exactly the pathology that produced
// TestReadScopedTokenCannotReadAPublishToken, whose fixture had an empty
// passphrase and an empty stream key, so it asserted absence over a body that
// never carried them.
//
// Measured on this fixture before the change: of 50 swept paths, 8 carried a
// planted credential in the high-privilege body. The other 42 asserted absence
// over a body no principal ever receives a credential from. Those 42 were not
// WRONG -- most of them are correct and want to stay -- but "swept" was a single
// word covering two completely different claims, and only one of them is a
// differential.
//
// So every swept path now gets a COMPUTED verdict, and the verdict goes in the
// artifact where a reviewer sees it change:
//
//	differential        the high-privilege body CONTAINS a compile-time sentinel
//	                    and the read body does not. The real claim.
//	invariant           read, admin and session receive byte-identical bytes,
//	                    stable across samples. A WEAKER claim -- a credential
//	                    disclosed identically to everyone passes it -- and that
//	                    residue is the shape list's territory (#168), not fixed
//	                    here. What it does buy is a running assertion that FAILS
//	                    the day the route becomes principal-dependent, forcing it
//	                    into the differential class with a sentinel or an excuse.
//	explained-variance  stable, principal-dependent, and every differing JSON
//	                    pointer is in a committed exemption list. Today: the
//	                    caller's own name on /auth/me.
//	unstable            the bytes move between two samples with nothing changed,
//	                    so no equality claim can be made at all. Hand-registered,
//	                    ceiling-clamped, and each entry says what moves.
//
// A blanket "the high-privilege body must contain a sentinel" rule was rejected
// after being executed: it is UNSATISFIABLE by construction on at least three
// routes (the hook signing secret is sealed, the platform client secret is
// sealed, alerts.Rule.URL is json:"-" -- no principal ever receives them), and
// its only available discharge there is fabricated fixture data, which in review
// is indistinguishable from a legitimate fix. That is a pre-installed weakening,
// so the honest substitute is the invariant verdict: an assertion that runs.

// ---------------------------------------------------------------- the vocabulary

type sweepClass string

const (
	classDifferential      sweepClass = "differential"
	classInvariant         sweepClass = "invariant"
	classExplainedVariance sweepClass = "explained-variance"
	classUnstable          sweepClass = "unstable"
)

// sweepVerdict is one swept path's computed classification.
type sweepVerdict struct {
	Path    string     `json:"path"`
	Pattern string     `json:"pattern"`
	Class   sweepClass `json:"class"`
	// Inert records an invariant route whose whole body is "[]" or "{}": the
	// sweep scans an empty list. Sub-counted rather than hidden inside
	// "invariant", so the artifact shows how many sweeps are looking at nothing.
	Inert bool `json:"inert,omitempty"`
	// Pointers are the JSON pointers that differ read-vs-high, for the two
	// classes where something differs.
	Pointers []string `json:"pointers,omitempty"`
	// Sentinels are the planted credentials witnessed in the high body.
	Sentinels []string `json:"sentinels,omitempty"`
}

// nonCredentialVariance is the ONLY way a stable read-vs-high difference that
// carries no sentinel may pass. Keyed (pattern, JSON pointer), because "this
// route differs" is not a claim -- "this FIELD differs, and here is why it is
// not a credential" is.
//
// Ceiling-clamped in the artifact: entries may be removed freely, and adding one
// is a hand edit of varianceExemptCeiling, which is a reviewable act.
var nonCredentialVariance = map[string]string{
	"GET /api/v1/auth/me#/auth": "the caller's own authentication METHOD -- \"token\" or " +
		"\"session\". It is a property of the request that just arrived, not of anything " +
		"stored, and the caller necessarily already knows it.",
	"GET /api/v1/auth/me#/tokenName": "the caller's own token NAME, which the caller chose " +
		"when it minted the token. Absent for a session principal, which is why the shape " +
		"differs at all.",
}

// unstableSweeps are the swept paths whose bytes move between two samples with
// nothing changed, so neither equality nor difference is a claim about
// principals. Hand-registered with what moves; ceiling-clamped.
var unstableSweeps = map[string]string{
	"/api/v1/metrics": "a Prometheus exposition carrying polyemesis_uptime_seconds, which " +
		"advances between two reads by construction. Principal-independent by inspection " +
		"of the handler and swept for sentinels regardless.",
	"/api/v1/processes": "the supervisor's live table. This fixture reproduces #150's own " +
		"recorded finding -- both destinations are Enabled:false and the only child is " +
		"playout:source -- and that child walks starting -> reconnecting -> restarts:1 " +
		"underneath the sweep. Making it stable means running a real child in the cheap " +
		"tier, seconds on every run; the runningDestinationLogs and " +
		"TestRunningDestinationLeaksNoSentinelOnAnyEgress counterparts carry this route " +
		"instead. Swept for sentinels regardless.",
	// FOUND BY RUNNING, not predicted. It was stable in six consecutive solo
	// runs of this package and unstable in two of three full-suite runs: the
	// body reports FREE BYTES on the filesystem, and the rest of the suite is
	// writing to it. A guard that is stable only when nothing else is happening
	// is the same species as a guard that is thorough over a set excluding the
	// bug, so it is declared rather than left to flap.
	"/api/v1/recordings/usage": "reports free bytes on the recordings filesystem, which " +
		"moves under any concurrent write -- including the rest of this test suite. " +
		"Principal-independent by inspection of handleRecordingUsage, and swept for " +
		"sentinels regardless.",
}

// samplesPerPrincipal is three rather than two: two samples cannot distinguish
// "stable" from "changed exactly once between them".
const samplesPerPrincipal = 3

// ------------------------------------------------------------------ the census

// sweepCensus drives every swept path as three principals, three times each, and
// computes each path's verdict. It is the whole of #165.
func sweepCensus(t *testing.T, h http.Handler, sign func(*http.Request)) []sweepVerdict {
	// NO t.Helper() here, deliberately. This function reports a dozen distinct
	// failures and attributing every one of them to its caller would print the
	// same preflight line number for all of them.
	s := serverUnderTest(t, h)
	read := createScopedToken(t, h, sign, "partition-read", db.ScopeRead)
	admin := createScopedToken(t, h, sign, "partition-admin", db.ScopeAdmin)

	sample := func(as func(*http.Request), path string) (int, string) {
		r := jsonRequest(t, http.MethodGet, path, nil)
		as(r)
		w := do(t, h, r)
		return w.Code, w.Body.String()
	}
	stable := func(as func(*http.Request), path string) (int, string, bool) {
		code, first := sample(as, path)
		for i := 1; i < samplesPerPrincipal; i++ {
			_, again := sample(as, path)
			if again != first {
				return code, first, false
			}
		}
		return code, first, true
	}

	needle := discoveredPublishToken(t, h, sign)
	var out []sweepVerdict
	for _, path := range leakRoutes() {
		v := sweepVerdict{Path: path, Pattern: "GET " + patternOf(t, s, path)}

		readCode, readBody, readStable := stable(bearer(read), path)
		if readCode != http.StatusOK {
			t.Errorf("GET %s returned %d to a read token. A path this sweep claims to cover "+
				"must actually answer, or the sweep proves nothing about it.", path, readCode)
			continue
		}
		_, adminBody, adminStable := stable(bearer(admin), path)
		_, sessBody, sessStable := stable(sign, path)

		// The absence half runs for EVERY class, including unstable: a route
		// whose bytes move is still a route whose bytes must carry no
		// credential.
		for _, secret := range allSentinels() {
			if strings.Contains(readBody, secret) {
				t.Errorf("GET %s handed a read-scoped token %s.\nbody: %s",
					path, secret, truncateForFailure(readBody))
			}
		}
		// THE DISCOVERED NEEDLE. The source's publish token is minted by the
		// server, so no compile-time constant can stand for it -- and it is
		// therefore invisible to allSentinels(), which is a list of things a
		// test planted. It reaches a read principal through TWO shapes on
		// /sources alone: the `token` field and every publishUrls entry, since
		// the token IS the address and there is no masked form of
		// srt://host?streamid=TOKEN that is still a URL.
		//
		// Absence is the ONLY thing assertable about a discovered value, on
		// every route without exception. It may never satisfy a positive
		// control: a needle read back OUT of the high body makes the control
		// circular, an empty fixture yields an empty needle, and the control
		// passes vacuously -- #165 one level down. Only constants may satisfy
		// a control.
		if needle != "" && strings.Contains(readBody, needle) {
			t.Errorf("GET %s handed a read-scoped token the server-minted publish token "+
				"(%d characters, read once from the admin view of /api/v1/sources/1). No "+
				"sentinel in allSentinels() covers this credential, because no test "+
				"planted it -- which is exactly why it is registered as a discovered "+
				"needle and asserted absent everywhere.\nbody: %s",
				path, len(needle), truncateForFailure(readBody))
		}

		_, declaredUnstable := unstableSweeps[path]
		switch {
		// DECLARATION FIRST, observation second. A registered path is unstable
		// by declaration even on a run where three samples happened to agree,
		// because the instability is load-dependent: /recordings/usage reports
		// free bytes on the filesystem the whole test suite is writing to, and
		// it was stable in six consecutive solo runs and unstable in two of
		// three full-suite runs. Classifying on observation alone would make
		// the committed verdict flap, which is a flaky test wearing a ratchet's
		// clothes. The ceiling is what keeps the register honest: declaring one
		// costs a hand edit.
		case declaredUnstable:
			v.Class = classUnstable
		case !readStable || !adminStable || !sessStable:
			v.Class = classUnstable
			t.Errorf("GET %s returned different bytes to the SAME principal on two of "+
				"%d consecutive samples, so no equality claim about principals can be "+
				"made about it at all. Either make it stable, or register it in "+
				"unstableSweeps saying WHAT moves -- and raise unstableCeiling in %s by "+
				"hand, which is the reviewable act.",
				path, samplesPerPrincipal, coveragePath)
		case readBody == adminBody && readBody == sessBody:
			v.Class = classInvariant
			v.Inert = len(strings.TrimSpace(readBody)) <= 3
		default:
			v.Sentinels = sentinelsIn(adminBody, sessBody)
			v.Pointers = mergePointers(
				jsonPointerDiff(readBody, adminBody), jsonPointerDiff(readBody, sessBody))
			if len(v.Sentinels) > 0 {
				v.Class = classDifferential
				break
			}
			v.Class = classExplainedVariance
			for _, p := range v.Pointers {
				if _, ok := nonCredentialVariance[v.Pattern+"#"+p]; !ok {
					t.Errorf("GET %s differs read-vs-high at the JSON pointer %q and the "+
						"high body carries NO planted sentinel, so this is neither a "+
						"differential nor an invariant. Either plant the credential this "+
						"field carries -- and it becomes a differential -- or add "+
						"%q to nonCredentialVariance saying why it is not a credential, "+
						"and raise varianceExemptCeiling in %s by hand.\n"+
						"read: %s\nhigh: %s",
						path, p, v.Pattern+"#"+p, coveragePath,
						truncateForFailure(readBody), truncateForFailure(adminBody))
				}
			}
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// discoveredPublishToken reads the source's server-minted publish token ONCE,
// from the admin view of the route entitled to it, and returns it for use as an
// absence-needle everywhere else.
//
// A discovered needle can never satisfy a positive control. A positive control
// says "the credential WAS in the high body", and a needle read back OUT of the
// high body makes that circular: an empty fixture yields an empty needle and the
// control passes vacuously, which is the whole bug this file exists to close.
// Only compile-time constants may satisfy a control. Discovered values are
// absence-needles and nothing else.
func discoveredPublishToken(t *testing.T, h http.Handler, sign func(*http.Request)) string {
	t.Helper()
	var src struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(bodyOf(t, h, sign, "/api/v1/sources/1")), &src); err != nil {
		t.Errorf("read the source's publish token from the admin view: %v", err)
		return ""
	}
	if len(src.Token) < 16 {
		t.Errorf("the source's publish token is %d characters (%q). It is minted by the "+
			"server, so a short or empty one means the fixture is not what this sweep "+
			"thinks it is -- and an empty needle would make every absence assertion below "+
			"vacuous, which is #165 reappearing one level down.", len(src.Token), src.Token)
		return ""
	}
	return src.Token
}

func sentinelsIn(bodies ...string) []string {
	var found []string
	for _, secret := range allSentinels() {
		for _, b := range bodies {
			if strings.Contains(b, secret) {
				found = append(found, secret)
				break
			}
		}
	}
	return found
}

func mergePointers(lists ...[]string) []string {
	set := map[string]bool{}
	for _, l := range lists {
		for _, p := range l {
			set[p] = true
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// jsonPointerDiff reports the JSON pointers at which two bodies differ. A body
// that is not JSON, or that differs in a way the walk cannot localise, yields
// the whole-document pointer "/" -- never an empty list, because an empty list
// would read as "no difference" and that is the fail-open direction.
func jsonPointerDiff(a, b string) []string {
	if a == b {
		return nil
	}
	var av, bv any
	if json.Unmarshal([]byte(a), &av) != nil || json.Unmarshal([]byte(b), &bv) != nil {
		return []string{"/"}
	}
	var out []string
	walkJSONDiff("", av, bv, &out)
	if len(out) == 0 {
		out = []string{"/"}
	}
	sort.Strings(out)
	return out
}

func walkJSONDiff(ptr string, a, b any, out *[]string) {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			*out = append(*out, ptrOrRoot(ptr))
			return
		}
		keys := map[string]bool{}
		for k := range av {
			keys[k] = true
		}
		for k := range bv {
			keys[k] = true
		}
		for k := range keys {
			ak, aok := av[k]
			bk, bok := bv[k]
			switch {
			case !aok || !bok:
				*out = append(*out, ptr+"/"+jsonPtrEscape(k))
			default:
				walkJSONDiff(ptr+"/"+jsonPtrEscape(k), ak, bk, out)
			}
		}
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			*out = append(*out, ptrOrRoot(ptr))
			return
		}
		for i := range av {
			walkJSONDiff(ptr+"/"+strconv.Itoa(i), av[i], bv[i], out)
		}
	default:
		if fmt.Sprintf("%v", a) != fmt.Sprintf("%v", b) {
			*out = append(*out, ptrOrRoot(ptr))
		}
	}
}

func ptrOrRoot(ptr string) string {
	if ptr == "" {
		return "/"
	}
	return ptr
}

func jsonPtrEscape(k string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(k)
}

// ------------------------------------------------------------------- the counts

type partitionTotals struct {
	Differential      int `json:"differential"`
	Invariant         int `json:"invariant"`
	Inert             int `json:"inertSubsetOfInvariant"`
	ExplainedVariance int `json:"explainedVariance"`
	Unstable          int `json:"unstable"`
}

func countPartition(vs []sweepVerdict) partitionTotals {
	var tot partitionTotals
	for _, v := range vs {
		switch v.Class {
		case classDifferential:
			tot.Differential++
		case classInvariant:
			tot.Invariant++
			if v.Inert {
				tot.Inert++
			}
		case classExplainedVariance:
			tot.ExplainedVariance++
		case classUnstable:
			tot.Unstable++
		}
	}
	return tot
}

// assertEverySentinelIsWitnessed is the fixture-completeness half, and it is the
// check that fires on #150's GENERATING CAUSE: a credential column added to the
// schema, planted nowhere, and therefore invisible to a sweep that only ever
// asserts absence.
func assertEverySentinelIsWitnessed(t *testing.T, h http.Handler, sign func(*http.Request)) {
	t.Helper()
	gone := sentinelsNotWitnessed(t, h, sign, "witness-admin")
	for _, secret := range gone {
		t.Errorf("the sentinel %s is declared in allSentinels() and appears in NO "+
			"high-privilege body on any swept route. Either it is planted in a column "+
			"nothing serves -- in which case every absence assertion about it is "+
			"vacuous -- or the route that served it stopped. This is #150's generating "+
			"cause with the polarity reversed: a credential column nobody plants is "+
			"invisible to a sweep that only asserts absence.", secret)
	}
	if len(gone) == 0 {
		t.Logf("sentinel witness: %d/%d planted credentials observed in a high-privilege body",
			len(allSentinels()), len(allSentinels()))
	}
}

// sentinelsNotWitnessed is the measurement half, split out so that a caller who
// EXPECTS a credential to have gone can compare against a declared list rather
// than fail. The census in nonget_differential_test.go is that caller: a write
// route with a documented side effect on stored configuration -- PUT
// /api/v1/settings writes its ingest block through to the default source row --
// destroys a planted credential on purpose, and the useful assertion there is
// "exactly the declared ones", not "none".
//
// tokenName is a parameter because two callers minting "witness-admin" against
// the same handler would be two tokens with one name in the audit trail.
func sentinelsNotWitnessed(t *testing.T, h http.Handler,
	sign func(*http.Request), tokenName string) []string {
	t.Helper()
	admin := createScopedToken(t, h, sign, tokenName, db.ScopeAdmin)
	var all strings.Builder
	for _, path := range leakRoutes() {
		all.WriteString(bodyOf(t, h, bearer(admin), path))
		all.WriteString(bodyOf(t, h, sign, path))
	}
	var gone []string
	for _, secret := range allSentinels() {
		if !strings.Contains(all.String(), secret) {
			gone = append(gone, secret)
		}
	}
	return gone
}
