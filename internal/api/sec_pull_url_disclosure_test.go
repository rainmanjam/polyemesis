package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/db"
)

// The recurrence guard for #229: a pull source's mid-path and non-table query
// credentials rendered verbatim into GET /api/v1/processes, a route a
// READ-SCOPED token reaches.
//
// Third instance of the class after #150 and #162, and the same shape each
// time: the mask was built from a rule about WHERE a credential usually lives
// -- userinfo, the last path segment, twenty known query names -- rather than
// from what the argv actually carries. An RTSP camera and an Akamai/Wowza CDN
// both put the credential in the MIDDLE of the path with the filename last, and
// name their query token whatever they chose.
//
// WHY THE ASSERTION IS ON THE RENDERED HTTP BODY AND NOT ON urlSecrets.
// Measured on main at cffd20c, the two layers together produced this:
//
//	rtsp://cam.example/live/SUPERSECRETPATHSEG/[redacted]
//
// The credential is printed in full and the harmless filename beside it is
// masked in its place, so the line LOOKS redacted. A unit test over the secret
// set alone would have shown a plausible-looking set and missed that entirely;
// only the bytes that leave the process settle it.
//
// WHY THE PRINCIPAL IS A READ-SCOPED BEARER TOKEN. Thirteen routes 403 a read
// token before the handler runs, and several groups are session-only. A test
// with the wrong principal gets its 403 in the middle of the mux and passes
// having exercised nothing. This one asserts 200 first and the body second.

// The three fixture credentials, each a different half of the defect. Named for
// the SHAPE they occupy in the URL rather than as `somethingKey = "<high
// entropy>"`, which is the spelling gitleaks' generic-api-key rule matches --
// the right answer to a scanner finding a fixture is to stop writing fixtures
// in a credential's shape, never to widen the allowlist.
const (
	// A path segment in the MIDDLE, with an ordinary filename after it. This is
	// the half urlSecrets' last-segment-only rule inverted: it masked
	// `stream1` and rendered this one.
	pullMidPathSentinel = "SENTINEL-pull-midpath-2f8c"
	// A query value under a name outside alerts.secretParam's twenty. `hdnts`
	// is Akamai's; `authcode` and `policy` are two more that already exist in
	// the wild. SecretName is an exact lookup with no fallback, so all three
	// were rendered.
	pullQuerySentinel = "SENTINEL-pull-hdnts-2f8c"
	// SIX CHARACTERS, and that is the whole point of this one.
	//
	// alerts.NewSecretSet REFUSES any literal shorter than alerts.MinSecretLen
	// (8). So the fix the issue suggests first -- add every path segment to the
	// set -- drops this on the floor, and because the neighbouring filename IS
	// long enough the mask relocates there and the output still reads as
	// redacted. Only a literal spanning the whole of the URL below the
	// authority carries a segment this short. Lengthen this string and the
	// guard stops guarding the thing it was written for.
	pullShortSentinel = "Q7wR2z"
)

// pullURLUnderTest carries all three in one URL, because they arrive in one
// URL: an operator pastes a single CDN or camera address.
const pullURLUnderTest = "rtsp://cam.example/live/" + pullMidPathSentinel + "/" +
	pullShortSentinel + "/stream1?hdnts=" + pullQuerySentinel + "&format=ts"

func TestPullSourceCredentialsAreNotRenderedToAReadScopedToken(t *testing.T) {
	if len(pullShortSentinel) >= alerts.MinSecretLen {
		t.Fatalf("the short-segment fixture is %d characters, at or above "+
			"alerts.MinSecretLen (%d). It exists to be BELOW that floor -- above it "+
			"the segment enters the secret set on its own and this test no longer "+
			"distinguishes the shipped fix from the one that leaks.",
			len(pullShortSentinel), alerts.MinSecretLen)
	}

	h, store, sign := pullSourceServer(t)
	read := createScopedToken(t, h, sign, "monitoring", db.ScopeRead)

	// POSITIVE CONTROLS FIRST, and they must come first.
	//
	// Every assertion below is "this sentinel is absent", and absence proves
	// nothing unless the credential was PRESENT to leak. A fixture that quietly
	// failed to select pull ingest would spawn no ingest child, render no
	// command, and pass the whole test having read an empty page -- which is
	// this repository's documented failure mode, eight tests deep.
	assertPullURLIsReallyStored(t, store)
	command := assertIngestCommandIsRendered(t, h, bearer(read))

	// The disclosure half.
	for _, sentinel := range []string{pullMidPathSentinel, pullQuerySentinel, pullShortSentinel} {
		if strings.Contains(command, sentinel) {
			t.Errorf("GET /api/v1/processes handed a read-scoped principal the pull "+
				"credential %q verbatim in the rendered command line.\ncommand: %s",
				sentinel, command)
		}
	}
	// The same argv reaches the second egress through processDetail, and it is
	// a separate route with a separate handler rather than a second reading of
	// the first one.
	logsBody := bodyOf(t, h, bearer(read), "/api/v1/processes/ingest/logs")
	for _, sentinel := range []string{pullMidPathSentinel, pullQuerySentinel, pullShortSentinel} {
		if strings.Contains(logsBody, sentinel) {
			t.Errorf("GET /api/v1/processes/ingest/logs handed a read-scoped principal "+
				"the pull credential %q verbatim.\nbody: %s",
				sentinel, truncateForFailure(logsBody))
		}
	}
}

// TestTheRenderedPullCommandStaysDiagnosable is the other half, and the one
// worth breaking the fix over.
//
// A renderer that blanked the whole argument would satisfy the test above and
// be useless: an operator opens GET /processes to find out WHICH camera is
// refusing them. The scheme and the authority are what answer that, and the fix
// deliberately keeps them -- it masks everything BELOW the authority and
// nothing above it.
func TestTheRenderedPullCommandStaysDiagnosable(t *testing.T) {
	h, _, sign := pullSourceServer(t)
	read := createScopedToken(t, h, sign, "monitoring", db.ScopeRead)

	command := assertIngestCommandIsRendered(t, h, bearer(read))
	for _, want := range []string{"rtsp://cam.example", "-i"} {
		if !strings.Contains(command, want) {
			t.Errorf("the rendered ingest command has lost %q, which is how an operator "+
				"tells this source from another one.\ncommand: %s", want, command)
		}
	}
}

// pullSourceServer is the engine-backed fixture with a PULL source whose URL
// carries the three sentinels.
//
// renditionServer's FFmpeg path cannot exec, which is what this test wants: the
// "command" field is rendered from Spec.Args before anything spawns (see
// TestTheCommandFieldLeaksBeforeTheChildEverSpawns), so the leak is reachable
// with no child, no stderr and no real binary on the machine.
func pullSourceServer(t *testing.T) (http.Handler, *db.DB, func(*http.Request)) {
	t.Helper()
	h, store, sign := sourceServer(t)

	src, err := store.GetSource(1)
	if err != nil {
		t.Fatalf("fixture source: %v", err)
	}
	src.Ingest.Mode = db.IngestPull
	src.Ingest.Pull.URL = pullURLUnderTest
	if err := store.UpdateSource(src); err != nil {
		t.Fatalf("fixture: choose pull ingest: %v", err)
	}

	s := serverUnderTest(t, h)
	if err := s.mgr.Reconcile(); err != nil {
		t.Fatalf("reconcile so the engine builds the ingest spec: %v", err)
	}
	return h, store, sign
}

// assertPullURLIsReallyStored reads the credential back out of the store, which
// is the one place it is meant to be whole. If it is not here the fixture never
// planted anything and every absence asserted downstream is vacuous.
func assertPullURLIsReallyStored(t *testing.T, store *db.DB) {
	t.Helper()
	src, err := store.GetSource(1)
	if err != nil {
		t.Fatalf("positive control: GetSource: %v", err)
	}
	if src.Ingest.Mode != db.IngestPull {
		t.Fatalf("positive control: the fixture source is on ingest mode %q, not pull, "+
			"so no ingest child exists and nothing renders a pull URL at all",
			src.Ingest.Mode)
	}
	for _, sentinel := range []string{pullMidPathSentinel, pullQuerySentinel, pullShortSentinel} {
		if !strings.Contains(src.Ingest.Pull.URL, sentinel) {
			t.Fatalf("positive control: the stored pull URL does not carry %q, so the "+
				"absence of it downstream proves nothing.\nstored: %s",
				sentinel, src.Ingest.Pull.URL)
		}
	}
}

// assertIngestCommandIsRendered drives GET /api/v1/processes with the given
// principal, insists on 200, and returns the ingest row's rendered command.
//
// The 200 is asserted rather than assumed. A read-scoped token is denied before
// the handler on thirteen patterns; if /processes ever joined them this guard
// would go silently vacuous, which is exactly the shape it exists to refuse.
func assertIngestCommandIsRendered(t *testing.T, h http.Handler, sign func(*http.Request)) string {
	t.Helper()
	r := jsonRequest(t, http.MethodGet, "/api/v1/processes", nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/processes answered %d to a read-scoped token. The "+
			"principal never reached the handler, so this test asserts nothing: %s",
			w.Code, strings.TrimSpace(w.Body.String()))
	}
	var rows []procRow
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode /processes: %v\nbody: %s", err, w.Body.String())
	}
	for _, row := range rows {
		if row.Status.Name != "ingest" {
			continue
		}
		if strings.TrimSpace(row.Command) == "" {
			t.Fatalf("the ingest process rendered an EMPTY command; an empty egress " +
				"cannot demonstrate the absence of anything")
		}
		return row.Command
	}
	t.Fatalf("no ingest process appears in GET /api/v1/processes, so the pull URL "+
		"never reached an argv and every assertion here would be vacuous.\nbody: %s",
		truncateForFailure(w.Body.String()))
	return ""
}
