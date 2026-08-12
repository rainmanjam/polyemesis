package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/uploads"
)

/* #255, and the shape of the hole turned out to be sharper than "inherited".

MEASURED FIRST, on origin/main at 9176e55, with an upload carrying
uploads.UnverifiedVerdict(ReasonInterrupted):

	PUT /api/v1/sources/1  {"ingest":{"mode":"pull","pull":{"url":"file://uploads/unchecked-abcd1234.ts"}}}
	  -> 200, and the row stored it: mode="pull" pull.url="file://uploads/unchecked-abcd1234.ts"
	POST /api/v1/sources   (same ingest block)
	  -> 201

Neither route reached pullSourceUploadProblems, which is called from ONE place:
the settings handler. So the gate #201 added covered the legacy path and not the
one the Sources page uses -- and engine.effectiveSettings does
`settings.Ingest = src.Ingest`, so the source row is what the engine's FFmpeg
actually pulls. PUT /settings stayed meaningful only because it mirrors its
ingest block into the DEFAULT source; a second programme was never covered at
all.

That is not the inherited case. It is introducible in one request, today, by the
same operator in the same session. The gate below closes it. The genuinely
inherited case is the last test in this file, and it is REPORTED rather than
refused -- Server.pullUploadUnchecked carries the argument. */

// pullIngest builds an ingest block for the source routes: the fixture's own
// values with the mode and URL replaced.
//
// Copied from the stored row rather than written out here, because a hand-built
// block fails db validation on fields this test is not about -- the first
// attempt at this measurement got "srt latency 0ms out of range (20-8000)" and
// proved nothing about pull URLs.
func pullIngest(t *testing.T, store *db.DB, url string) db.IngestSettings {
	t.Helper()
	src, err := store.GetSource(1)
	if err != nil {
		t.Fatalf("fixture source: %v", err)
	}
	ing := src.Ingest
	ing.Mode = db.IngestPull
	ing.Pull.URL = url
	return ing
}

// putSourceIngest sends an ingest block to PUT /sources/{id} and returns the
// body, failing the test when the status is not the expected one.
func putSourceIngest(t *testing.T, h http.Handler, sign func(*http.Request),
	id int64, ing db.IngestSettings, wantStatus int) []byte {
	t.Helper()
	r := jsonRequest(t, http.MethodPut, "/api/v1/sources/"+strconv.FormatInt(id, 10),
		map[string]any{"name": "Main", "enabled": true, "ingest": ing})
	sign(r)
	w := do(t, h, r)
	if w.Code != wantStatus {
		t.Fatalf("PUT /api/v1/sources/%d pull=%q: status %d, want %d, body %s",
			id, ing.Pull.URL, w.Code, wantStatus, w.Body.String())
	}
	return w.Body.Bytes()
}

// The route the Sources page writes through must refuse a pull source naming an
// upload this server was never allowed to inspect.
//
// THE CONTROLS ARE FIRST, and there are two of them, because the assertion this
// test exists for is that a save was REFUSED -- and a handler that refused every
// pull source, or every source update, would satisfy that on its own.
func TestUpdatingASourceToPullFromAnUncheckedUploadIsRefused(t *testing.T) {
	h, store, sign := sourceServer(t)
	srv := serverUnderTest(t, h)

	seedUpload(t, srv, "checked-abcd1234.ts")
	seedUpload(t, srv, "unchecked-abcd1234.ts")
	seedUpload(t, srv, "norecord-abcd1234.ts")
	seedVerdict(t, srv, "checked-abcd1234.ts",
		uploads.VerifiedVerdict(uploads.MediaInfo{AudioTracks: 2}))
	seedVerdict(t, srv, "unchecked-abcd1234.ts",
		uploads.UnverifiedVerdict(uploads.ReasonInterrupted))

	// (a) an upload this server inspected and accepted.
	putSourceIngest(t, h, sign, 1,
		pullIngest(t, store, uploads.PullURL("checked-abcd1234.ts")), http.StatusOK)

	// (b) an upload with NO record at all, which is every file an install
	// stored before verdicts existed. The rule is "recorded as unverified",
	// not "not recorded as verified".
	putSourceIngest(t, h, sign, 1,
		pullIngest(t, store, uploads.PullURL("norecord-abcd1234.ts")), http.StatusOK)

	// (c) the refusal, naming the file and the reason. The operator's only
	// remedy is to upload it again, and a bare "invalid source" does not tell
	// them which of the fields on that card was the problem.
	body := putSourceIngest(t, h, sign, 1,
		pullIngest(t, store, uploads.PullURL("unchecked-abcd1234.ts")), http.StatusBadRequest)
	for _, want := range []string{"unchecked-abcd1234.ts", uploads.ReasonInterrupted} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the refusal does not mention %q: %s", want, body)
		}
	}

	// AND IT WAS NOT STORED. A 400 that writes the row anyway is the worst of
	// both: the operator is told no and the engine pulls it regardless. Checked
	// against the store rather than the response, because the response is the
	// error body and cannot say what happened to the row.
	got, err := store.GetSource(1)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Ingest.Pull.URL, "unchecked-abcd1234.ts") {
		t.Errorf("the refused pull source reached the row anyway: %q; the engine reads "+
			"its ingest from here (effectiveSettings), so the refusal changed nothing",
			got.Ingest.Pull.URL)
	}
}

// The create route is the same hole. A source POSTed straight into existence
// with an unchecked pull URL never passes through the update path at all, so a
// gate on PUT alone leaves a one-request way in.
func TestCreatingASourcePullingFromAnUncheckedUploadIsRefused(t *testing.T) {
	h, store, sign := sourceServer(t)
	srv := serverUnderTest(t, h)

	seedUpload(t, srv, "checked-abcd1234.ts")
	seedUpload(t, srv, "unchecked-abcd1234.ts")
	seedVerdict(t, srv, "checked-abcd1234.ts",
		uploads.VerifiedVerdict(uploads.MediaInfo{AudioTracks: 1}))
	seedVerdict(t, srv, "unchecked-abcd1234.ts",
		uploads.UnverifiedVerdict(uploads.ReasonNoProber))

	create := func(name, url string, want int) []byte {
		t.Helper()
		r := jsonRequest(t, http.MethodPost, "/api/v1/sources",
			map[string]any{"name": name, "ingest": pullIngest(t, store, url)})
		sign(r)
		w := do(t, h, r)
		if w.Code != want {
			t.Fatalf("POST /api/v1/sources pull=%q: status %d, want %d, body %s",
				url, w.Code, want, w.Body.String())
		}
		return w.Body.Bytes()
	}

	// THE CONTROL: a create that must still succeed. Without it a handler that
	// refused every POST would pass the refusal below.
	create("e2e checked", uploads.PullURL("checked-abcd1234.ts"), http.StatusCreated)

	body := create("e2e unchecked", uploads.PullURL("unchecked-abcd1234.ts"), http.StatusBadRequest)
	for _, want := range []string{"unchecked-abcd1234.ts", uploads.ReasonNoProber} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the refusal does not mention %q: %s", want, body)
		}
	}

	// And no row was left behind. CreateSource runs after the check, so a
	// half-created source would mean the gate is in the wrong place.
	rows, err := store.ListSources()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Name == "e2e unchecked" {
			t.Errorf("the refused source was created anyway: id=%d pull=%q",
				row.ID, row.Ingest.Pull.URL)
		}
	}
}

// The scoping rule, which is the same one playlistUploadProblems and
// pullSourceUploadProblems follow: a save is refused for what it INTRODUCES and
// never for state it inherited.
//
// The Sources card PUTs the whole ingest block on every change, so without this
// an operator editing an SRT latency would be refused because of a pull URL
// configured before this gate existed -- and nothing on that form would say
// which field was the problem.
func TestAnInheritedSourcePullSourceDoesNotBlockAnUnrelatedEdit(t *testing.T) {
	h, store, sign := sourceServer(t)
	srv := serverUnderTest(t, h)
	seedUpload(t, srv, "inherited-abcd1234.ts")

	// Stored while it still had a passing record, then downgraded underneath,
	// which is how an install that predates this gate arrives at the state.
	seedVerdict(t, srv, "inherited-abcd1234.ts",
		uploads.VerifiedVerdict(uploads.MediaInfo{AudioTracks: 1}))
	putSourceIngest(t, h, sign, 1,
		pullIngest(t, store, uploads.PullURL("inherited-abcd1234.ts")), http.StatusOK)
	seedVerdict(t, srv, "inherited-abcd1234.ts",
		uploads.UnverifiedVerdict(uploads.ReasonInterrupted))

	// An unrelated change with the offending URL still in the payload, because
	// that is what the card sends.
	ing := pullIngest(t, store, uploads.PullURL("inherited-abcd1234.ts"))
	ing.SRT.LatencyMS = 320
	putSourceIngest(t, h, sign, 1, ing, http.StatusOK)

	got, err := store.GetSource(1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ingest.SRT.LatencyMS != 320 {
		t.Errorf("the unrelated change was not stored: srt latency = %d", got.Ingest.SRT.LatencyMS)
	}
	if got.Ingest.Pull.URL != uploads.PullURL("inherited-abcd1234.ts") {
		t.Errorf("the inherited pull source was rewritten: %q", got.Ingest.Pull.URL)
	}
}

// The other half of #255, and the half that is deliberately fail-OPEN.
//
// A source that already pulls from an unchecked upload keeps running. What it
// must not do is keep running SILENTLY: the server computes the sentence and
// puts it in the /sources response, so a monitoring script sees it and not only
// an operator looking at a card. pull_verdict.go's objection to the Library's
// marker was "a warning in the UI is not a check in the server" -- the case it
// named was automation configuring a pull source from a listing, which never
// sees a row, and this is the field that automation reads.
//
// THE CONTROL IS THE POINT HERE. A field that was always populated would pass
// the positive assertion, so the checked upload and the non-pull source both
// have to come back empty.
func TestAnInheritedUncheckedPullSourceIsReportedOnTheSourceListing(t *testing.T) {
	h, store, sign := sourceServer(t)
	srv := serverUnderTest(t, h)
	seedUpload(t, srv, "inherited-abcd1234.ts")
	seedUpload(t, srv, "fine-abcd1234.ts")
	seedVerdict(t, srv, "fine-abcd1234.ts",
		uploads.VerifiedVerdict(uploads.MediaInfo{AudioTracks: 2}))

	type row struct {
		ID                  int64  `json:"id"`
		PullUploadUnchecked string `json:"pullUploadUnchecked"`
	}
	listing := func() []row {
		t.Helper()
		var rows []row
		decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/sources", nil, http.StatusOK), &rows)
		return rows
	}
	first := func() row {
		t.Helper()
		rows := listing()
		if len(rows) == 0 {
			t.Fatal("the sources listing came back empty")
		}
		return rows[0]
	}

	// (a) an SRT source, which names no upload at all. The fixture starts here.
	if got := first().PullUploadUnchecked; got != "" {
		t.Errorf("an SRT source is reported as pulling from an unchecked upload: %q", got)
	}

	// (b) a pull source naming an upload this server inspected and accepted.
	putSourceIngest(t, h, sign, 1,
		pullIngest(t, store, uploads.PullURL("fine-abcd1234.ts")), http.StatusOK)
	if got := first().PullUploadUnchecked; got != "" {
		t.Errorf("a checked upload is reported as unchecked: %q", got)
	}

	// (c) the inherited state: saved while verified, downgraded underneath.
	// This is exactly the shape the save-time gate cannot see, by design.
	seedVerdict(t, srv, "inherited-abcd1234.ts",
		uploads.VerifiedVerdict(uploads.MediaInfo{AudioTracks: 1}))
	putSourceIngest(t, h, sign, 1,
		pullIngest(t, store, uploads.PullURL("inherited-abcd1234.ts")), http.StatusOK)
	seedVerdict(t, srv, "inherited-abcd1234.ts",
		uploads.UnverifiedVerdict(uploads.ReasonProbeUnusable))

	got := first().PullUploadUnchecked
	if got == "" {
		t.Fatal("a source pulling from an upload recorded as never inspected reports " +
			"nothing at all, so the only thing standing between an uninspected file and " +
			"air is an operator remembering a Library row they may never have seen")
	}
	for _, want := range []string{"inherited-abcd1234.ts", uploads.ReasonProbeUnusable} {
		if !strings.Contains(got, want) {
			t.Errorf("the report does not mention %q: %s", want, got)
		}
	}
}
