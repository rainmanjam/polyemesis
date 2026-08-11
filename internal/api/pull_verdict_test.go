package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/uploads"
)

// savePullSource round-trips the settings document with a pull source set,
// exactly as a client has to: PUT /settings REPLACES the document, so a lone
// ingest block would blank everything else.
func savePullSource(t *testing.T, h http.Handler, sign func(*http.Request),
	section, url string, wantStatus int) []byte {
	t.Helper()
	var s map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &s)

	block := s
	if section == "backup" {
		fo, _ := s["failover"].(map[string]any)
		if fo == nil {
			t.Fatal("settings carried no failover block")
		}
		block, _ = fo["backup"].(map[string]any)
		if block == nil {
			t.Fatal("settings carried no failover.backup block")
		}
	} else {
		block, _ = s["ingest"].(map[string]any)
		if block == nil {
			t.Fatal("settings carried no ingest block")
		}
	}
	block["mode"] = string(db.IngestPull)
	pull, _ := block["pull"].(map[string]any)
	if pull == nil {
		pull = map[string]any{}
		block["pull"] = pull
	}
	pull["url"] = url

	r := jsonRequest(t, http.MethodPut, "/api/v1/settings", s)
	sign(r)
	w := do(t, h, r)
	if w.Code != wantStatus {
		t.Fatalf("PUT /api/v1/settings with %s pull=%q: status %d, want %d, body %s",
			section, url, w.Code, wantStatus, w.Body.String())
	}
	return w.Body.Bytes()
}

// seedVerdict records a conclusion beside a seeded upload, the way the upload
// handler does through Pending.Commit.
func seedVerdict(t *testing.T, srv *Server, name string, v uploads.Verdict) {
	t.Helper()
	store, err := uploads.New(srv.cfg.DataDir)
	if err != nil {
		t.Fatalf("uploads.New: %v", err)
	}
	if err := store.PutVerdict(name, v); err != nil {
		t.Fatalf("seed a verdict for %q: %v", name, err)
	}
}

// #201. uploads.File.PullURL is "file://uploads/<name>", offered copyable in
// the Library, and pasting it into Settings -> Ingest -> Pull hands the path to
// the ENGINE's FFmpeg -- which is not ffmpeg.ProbeFile and carries neither the
// format allowlist nor -protocol_whitelist. So the third consumer of "stored
// does not imply checked" had no gate: an upload a dropped connection kept this
// server from inspecting could be routed to air with nothing having read it.
//
// THE CONTROL IS FIRST. Two of the three assertions below are that a save was
// REFUSED, and a handler that refused every pull source would satisfy both.
func TestSavingAPullSourceNamingAnUncheckedUploadIsRefused(t *testing.T) {
	h, _, sign := sourceServer(t)
	srv := serverUnderTest(t, h)

	seedUpload(t, srv, "checked-abcd1234.ts")
	seedUpload(t, srv, "unchecked-abcd1234.ts")
	seedUpload(t, srv, "norecord-abcd1234.ts")
	seedVerdict(t, srv, "checked-abcd1234.ts",
		uploads.VerifiedVerdict(uploads.MediaInfo{AudioTracks: 2}))
	seedVerdict(t, srv, "unchecked-abcd1234.ts",
		uploads.UnverifiedVerdict(uploads.ReasonInterrupted))

	// (a) THE CONTROL: an upload this server inspected and accepted.
	savePullSource(t, h, sign, "ingest", uploads.PullURL("checked-abcd1234.ts"), http.StatusOK)

	// (b) THE CONTROL FOR THE OTHER HALF OF THE RULE: an upload with NO record
	// at all is every file an install stored before verdicts existed, and
	// refusing those would strand media an operator has had for a year. The
	// gate tests "recorded as unverified", not "not recorded as verified".
	savePullSource(t, h, sign, "ingest", uploads.PullURL("norecord-abcd1234.ts"), http.StatusOK)

	// (c) THE REFUSAL, and the message has to name the file and the reason --
	// the operator's only remedy is to upload it again, and a bare "invalid
	// settings" does not tell them which of the fields they touched was wrong.
	body := savePullSource(t, h, sign, "ingest",
		uploads.PullURL("unchecked-abcd1234.ts"), http.StatusBadRequest)
	for _, want := range []string{"unchecked-abcd1234.ts", uploads.ReasonInterrupted} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the refusal does not mention %q: %s", want, body)
		}
	}

	// (d) THE BACKUP PULL SOURCE IS THE SAME PATH. It is a full ingest
	// configuration rather than "the primary with a different port", so it has
	// its own URL and its own way to reach the engine's FFmpeg.
	savePullSource(t, h, sign, "backup", uploads.PullURL("checked-abcd1234.ts"), http.StatusOK)
	savePullSource(t, h, sign, "backup",
		uploads.PullURL("unchecked-abcd1234.ts"), http.StatusBadRequest)
}

// The scoping rule playlistUploadProblems already follows: validation refuses
// what the operator is INTRODUCING and must not punish them for state they
// inherited. The settings page PUTs the whole document back on every unrelated
// change, so an unconditional check would refuse a save about an alert
// threshold because of a pull source configured before the gate existed.
func TestAnAlreadySavedPullSourceDoesNotBlockAnUnrelatedSave(t *testing.T) {
	h, store, sign := sourceServer(t)
	srv := serverUnderTest(t, h)
	seedUpload(t, srv, "inherited-abcd1234.ts")

	// Stored while it still had a passing record, then downgraded underneath --
	// which is how an install that predates this gate arrives at the state.
	seedVerdict(t, srv, "inherited-abcd1234.ts",
		uploads.VerifiedVerdict(uploads.MediaInfo{AudioTracks: 1}))
	savePullSource(t, h, sign, "ingest", uploads.PullURL("inherited-abcd1234.ts"), http.StatusOK)
	seedVerdict(t, srv, "inherited-abcd1234.ts",
		uploads.UnverifiedVerdict(uploads.ReasonInterrupted))

	// An unrelated change, with the offending URL still in the document
	// because that is what the settings page sends.
	var s map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/settings", nil, http.StatusOK), &s)
	fo, _ := s["failover"].(map[string]any)
	if fo == nil {
		t.Fatal("settings carried no failover block")
	}
	fo["graceSeconds"] = 7
	send(t, h, sign, http.MethodPut, "/api/v1/settings", s, http.StatusOK)

	got, err := store.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got.Failover.GraceSeconds != 7 {
		t.Errorf("the unrelated change was not stored: graceSeconds = %d",
			got.Failover.GraceSeconds)
	}
	if got.Ingest.Pull.URL != uploads.PullURL("inherited-abcd1234.ts") {
		t.Errorf("the inherited pull source was rewritten: %q", got.Ingest.Pull.URL)
	}
}
