package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/uploads"
)

// THE THIRD STATE, at the two validators that answer for it.
//
// Both gates already refuse an upload recorded as uninspected, and both say
// "upload it again" -- which is the correct remedy for a check that never
// finished and is ACTIVELY WRONG for a file that was checked and rejected. The
// same bytes fail the same way, so an operator who follows that advice re-sends
// a large file to earn the identical refusal.
//
// That is the whole reason the state had to exist before the job that writes it
// (#202): a re-verify job cannot resolve a refusal by deleting the file -- it is
// published by then, and handleDeleteMedia answers 409 while a playlist item
// names it -- so it has to RECORD the refusal, and recording it as "uninspected"
// would make these two sentences state something the server knows is false.
//
// Each test carries its CONTROL, because "the save was refused" is satisfied by
// a validator that refuses everything, and the assertion that matters here is
// about WHICH sentence came back rather than about the status code.

// TestARefusedUploadIsRefusedAsAPlaylistItemWithItsOwnRemedy
//
// The mutation: delete the `case v.Outcome == uploads.OutcomeRefused` arm in
// playlistUploadProblems, so a refusal falls through to the uninspected arm.
func TestARefusedUploadIsRefusedAsAPlaylistItemWithItsOwnRemedy(t *testing.T) {
	h, _, srv, _ := playlistJobServer(t)
	seedUpload(t, srv, "refused-abcd1234.ts")
	seedUpload(t, srv, "checked-abcd1234.ts")
	const why = "this file carries no video or audio stream"
	seedVerdict(t, srv, "refused-abcd1234.ts", uploads.RefusedVerdict(why))
	seedVerdict(t, srv, "checked-abcd1234.ts",
		uploads.VerifiedVerdict(uploads.MediaInfo{AudioTracks: 2}))
	_ = h

	// THE CONTROL FIRST: an ordinary inspected upload is accepted, so the
	// refusal below is about the verdict rather than about the validator.
	if err := srv.playlistUploadProblems(
		db.PlaylistSettings{Items: []db.PlaylistItem{{Upload: "checked-abcd1234.ts"}}},
		db.PlaylistSettings{}); err != nil {
		t.Fatalf("the validator refuses an inspected upload too, so nothing below "+
			"proves anything: %v", err)
	}

	err := srv.playlistUploadProblems(
		db.PlaylistSettings{Items: []db.PlaylistItem{{Upload: "refused-abcd1234.ts"}}},
		db.PlaylistSettings{})
	if err == nil {
		t.Fatal("a playlist item may name an upload this server inspected and refused")
	}
	msg := err.Error()
	if !strings.Contains(msg, "inspected and refused") {
		t.Errorf("the refusal does not say the file WAS inspected: %v", err)
	}
	if !strings.Contains(msg, why) {
		t.Errorf("the refusal does not carry the reason the operator has to act on: %v", err)
	}
	// THE ASSERTION THE STATE EXISTS FOR. "Upload it again" is the uninspected
	// sentence; reaching for it here sends the operator to re-transfer a file
	// that will be refused identically.
	if strings.Contains(msg, "upload it again") || strings.Contains(msg, "Upload it again") {
		t.Errorf("a refused file is answered with the uninspected remedy: %v", err)
	}
	if strings.Contains(msg, "without being checked") {
		t.Errorf("a file this server DID check is described as unchecked: %v", err)
	}
}

// TestARefusedUploadIsRefusedAsAPullSourceWithItsOwnRemedy
//
// The same split at the other validator, driven over HTTP because this one is
// reached through PUT /api/v1/settings and its sentence goes into the response
// body an operator reads.
//
// The mutation: delete the `case v.Outcome == uploads.OutcomeRefused` arm in
// pullSourceUploadProblems.
func TestARefusedUploadIsRefusedAsAPullSourceWithItsOwnRemedy(t *testing.T) {
	h, _, sign := sourceServer(t)
	srv := serverUnderTest(t, h)
	seedUpload(t, srv, "refused-abcd1234.ts")
	seedUpload(t, srv, "checked-abcd1234.ts")
	const why = "this file carries no video or audio stream"
	seedVerdict(t, srv, "refused-abcd1234.ts", uploads.RefusedVerdict(why))
	seedVerdict(t, srv, "checked-abcd1234.ts",
		uploads.VerifiedVerdict(uploads.MediaInfo{AudioTracks: 2}))

	// THE CONTROL: an inspected upload is a legal pull source.
	savePullSource(t, h, sign, "ingest", uploads.PullURL("checked-abcd1234.ts"), http.StatusOK)

	body := string(savePullSource(t, h, sign, "ingest",
		uploads.PullURL("refused-abcd1234.ts"), http.StatusBadRequest))
	for _, want := range []string{"refused-abcd1234.ts", why, "inspected and refused"} {
		if !strings.Contains(body, want) {
			t.Errorf("the refusal does not mention %q: %s", want, body)
		}
	}
	if strings.Contains(body, "upload it again") {
		t.Errorf("a refused file is answered with the uninspected remedy: %s", body)
	}
	if strings.Contains(body, "without being checked") {
		t.Errorf("a file this server DID check is described as unchecked: %s", body)
	}
}

// AND THE LISTING SAYS WHICH OF THE FOUR STATES EACH FILE IS IN, over the wire.
//
// The API is where the two UI consumers get their answer, and before this field
// they got `verified:false` for three different situations and had to guess
// which from whether a reason happened to be present. A refused upload carries a
// reason, so that guess would have HAPPENED to work today -- and would break the
// moment any recorded state arrived without a sentence. The listing states it.
//
// The mutation: drop `Outcome` from the File literal in uploads.Store.List.
func TestTheMediaListingNamesWhichOfTheFourStatesEachUploadIsIn(t *testing.T) {
	h, _, sign := sourceServer(t)
	srv := serverUnderTest(t, h)
	for _, n := range []string{"refused-abcd1234.ts", "unchecked-abcd1234.ts",
		"checked-abcd1234.ts", "legacy-abcd1234.ts"} {
		seedUpload(t, srv, n)
	}
	seedVerdict(t, srv, "refused-abcd1234.ts", uploads.RefusedVerdict("not media"))
	seedVerdict(t, srv, "unchecked-abcd1234.ts",
		uploads.UnverifiedVerdict(uploads.ReasonInterrupted))
	seedVerdict(t, srv, "checked-abcd1234.ts",
		uploads.VerifiedVerdict(uploads.MediaInfo{AudioTracks: 2}))
	// legacy- deliberately gets no verdict: that is the state an install made
	// before verdicts existed, and it must stay distinct from every other one.

	var listed []map[string]any
	decodeInto(t, send(t, h, sign, http.MethodGet, "/api/v1/media", nil, http.StatusOK), &listed)
	got := map[string]any{}
	for _, f := range listed {
		name, _ := f["name"].(string)
		got[name] = f["outcome"]
	}
	for name, want := range map[string]string{
		"refused-abcd1234.ts":   "refused",
		"unchecked-abcd1234.ts": "unverified",
		"checked-abcd1234.ts":   "verified",
		"legacy-abcd1234.ts":    "unrecorded",
	} {
		if got[name] != want {
			t.Errorf("%s is listed as outcome %v, want %q", name, got[name], want)
		}
	}
}
