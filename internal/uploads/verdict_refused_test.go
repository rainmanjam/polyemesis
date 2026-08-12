package uploads

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THE THIRD STATE, at the level that stores it -- see verdict_test.go for the
// first two.
//
// "Inspected and refused" had nowhere to live. The record carried one boolean,
// so it had two states, and a refusal was therefore never stored at all: the
// upload handler returns an error and the staged bytes are discarded. That
// works exactly once, at upload time, when nothing references the file yet. It
// does not work for anything that inspects an upload LATER (#202), because by
// then the file is published, a playlist item may name it, and DELETE answers
// 409 while one does -- so the refusal has to be RECORDED, and the only state
// available to record it in was the one that says "nobody read this", which
// every consumer answers by telling the operator to upload the file again.
//
// The compatibility half matters as much as the state itself: THE SIDECAR
// FORMAT IS ON DISK IN EVERY INSTALL. Records written before this change must
// read exactly as they did, records written now must remain legible to a build
// that only knows `verified`, and "no record at all" must stay distinguishable
// from every recorded state, because refusing every upload made before verdicts
// existed would strand media an operator has had for a year.

// seedFile puts a plain file in the store so a verdict has something to be
// about -- verdictTarget refuses a record for a name no upload has.
func seedFile(t *testing.T, s *Store, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.dir, name), []byte("bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// seedSidecar writes a raw sidecar, which is how a record from another version
// of this product -- older or newer -- arrives.
func seedSidecar(t *testing.T, s *Store, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.dir, sidecarName(name)), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A refusal survives a round trip as a refusal, and is NOT any of the other
// three answers. All four are asserted in one place because the whole defect is
// that the states collapse into each other: a test that only checked
// "Verified() is false" would pass with the refusal recorded as uninspected,
// which is the exact bug.
func TestARefusalIsRecordedAsItsOwnStateAndNotAsUninspected(t *testing.T) {
	s := newStore(t)
	seedFile(t, s, "bad-abcd1234.ts")
	const why = "this file carries no video or audio stream"
	if err := s.PutVerdict("bad-abcd1234.ts", RefusedVerdict(why)); err != nil {
		t.Fatalf("PutVerdict: %v", err)
	}

	v, recorded := s.Verdict("bad-abcd1234.ts")
	if !recorded {
		t.Fatal("a recorded refusal reads as no record at all, which is the state that is still allowed")
	}
	if v.Outcome != OutcomeRefused {
		t.Errorf("outcome = %q, want %q: a refusal recorded as anything else makes every "+
			"consumer tell the operator to upload the same bytes again", v.Outcome, OutcomeRefused)
	}
	if v.Verified() {
		t.Error("a refusal reads as verified")
	}
	if v.Reason != why {
		t.Errorf("reason = %q, want %q", v.Reason, why)
	}

	// And the listing -- the transport every UI consumer reads -- says the same
	// word rather than flattening it back to a boolean.
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("listing = %+v", list)
	}
	if list[0].Outcome != OutcomeRefused {
		t.Errorf("the listing reports outcome %q for a refused upload", list[0].Outcome)
	}
	if list[0].Verified {
		t.Error("the listing reports a refused upload as verified")
	}
	if list[0].UnverifiedReason != why {
		t.Errorf("the listing lost the refusal's reason: %q", list[0].UnverifiedReason)
	}
	if list[0].Media != nil {
		t.Errorf("a refused upload carries metadata: %+v", list[0].Media)
	}
}

// NO RECORD IS ITS OWN ANSWER AND THE LISTING NOW SAYS SO. The second return of
// Store.Verdict was dropped on the floor by List, so a client had to infer "no
// record" from an absent reason -- a proxy that was never the same question and
// that a third recorded state breaks outright.
func TestAFileWithNoRecordIsListedAsUnrecordedRatherThanAsUnverified(t *testing.T) {
	s := newStore(t)
	seedFile(t, s, "legacy-abcd1234.ts")
	seedFile(t, s, "checked-abcd1234.ts")
	if err := s.PutMedia("checked-abcd1234.ts", MediaInfo{AudioTracks: 2}); err != nil {
		t.Fatal(err)
	}

	byName := map[string]File{}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range list {
		byName[f.Name] = f
	}
	if got := byName["legacy-abcd1234.ts"].Outcome; got != OutcomeUnrecorded {
		t.Errorf("a file with no sidecar is listed as %q, want %q: a recorded state and no "+
			"record have different remedies and this is the only field that separates them",
			got, OutcomeUnrecorded)
	}
	// THE CONTROL. Without it, a List that hard-coded OutcomeUnrecorded for
	// every row would satisfy the assertion above.
	if got := byName["checked-abcd1234.ts"].Outcome; got != OutcomeVerified {
		t.Errorf("an inspected file is listed as %q, want %q", got, OutcomeVerified)
	}
}

// A RECORD WRITTEN BY AN OLDER BUILD READS EXACTLY AS IT ALWAYS DID. This is
// the compatibility story, and it is not hypothetical: these files are on disk
// in every install and there is no migration for them.
func TestARecordWrittenBeforeTheThirdStateReadsAsItAlwaysDid(t *testing.T) {
	s := newStore(t)
	for _, tc := range []struct {
		name    string
		sidecar string
		want    Outcome
		info    bool
	}{
		// Exactly what VerifiedVerdict used to marshal.
		{"old-pass-abcd1234.ts", `{"verified":true,"info":{"audioTracks":3,"videoCodec":"h264"}}`, OutcomeVerified, true},
		// Exactly what UnverifiedVerdict used to marshal.
		{"old-fail-abcd1234.ts", `{"verified":false,"reason":"the inspection was cut short before it finished"}`, OutcomeUnverified, false},
		// The zero Verdict, which is what a truncated-then-rewritten record and
		// an empty object both look like.
		{"old-zero-abcd1234.ts", `{}`, OutcomeUnverified, false},
	} {
		seedFile(t, s, tc.name)
		seedSidecar(t, s, tc.name, tc.sidecar)
		v, recorded := s.Verdict(tc.name)
		if !recorded {
			t.Fatalf("%s: an existing record reads as no record", tc.name)
		}
		if v.Outcome != tc.want {
			t.Errorf("%s: outcome = %q, want %q -- a record from before this change was "+
				"reinterpreted", tc.name, v.Outcome, tc.want)
		}
		if (v.Info != nil) != tc.info {
			t.Errorf("%s: info present = %v, want %v", tc.name, v.Info != nil, tc.info)
		}
	}
}

// AND A RECORD WRITTEN NOW STAYS LEGIBLE TO A BUILD THAT ONLY KNOWS `verified`.
// A format change has two directions in time. A downgrade, a rollback, or a
// second process against the same data directory mid-upgrade will read these
// files with code that has never heard of `outcome`, and the answer it gets has
// to be safe: a refusal must not read as a pass.
func TestARecordWrittenNowIsStillLegibleToABuildThatOnlyKnowsVerified(t *testing.T) {
	// The old reader, spelled out: the struct as it was before this change.
	type legacy struct {
		Verified bool   `json:"verified"`
		Reason   string `json:"reason"`
	}
	for _, tc := range []struct {
		what         string
		v            Verdict
		wantVerified bool
	}{
		{"a refusal", RefusedVerdict("this file carries no video or audio stream"), false},
		{"an uninspected file", UnverifiedVerdict(ReasonInterrupted), false},
		{"a pass", VerifiedVerdict(MediaInfo{AudioTracks: 1}), true},
	} {
		b, err := json.Marshal(tc.v)
		if err != nil {
			t.Fatalf("%s: %v", tc.what, err)
		}
		var old legacy
		if err := json.Unmarshal(b, &old); err != nil {
			t.Fatalf("%s: an old build cannot parse this record at all: %v", tc.what, err)
		}
		if old.Verified != tc.wantVerified {
			t.Errorf("%s reads as verified=%v to a build that only knows that field, want %v: %s",
				tc.what, old.Verified, tc.wantVerified, b)
		}
	}
	// The refusal also arrives with its sentence, so the old build's "not
	// checked (%s)" line says something rather than trailing off.
	b, _ := json.Marshal(RefusedVerdict("this file carries no video or audio stream"))
	var old legacy
	if err := json.Unmarshal(b, &old); err != nil {
		t.Fatal(err)
	}
	if old.Reason == "" {
		t.Error("a refusal reaches an older build with no reason at all")
	}
}

// A STATE A LATER VERSION INVENTS FALLS BACK TO THE FIELD EVERY VERSION
// CARRIES, rather than being trusted or exploding. The same rule that reads an
// absent `outcome` reads an unknown one, which is why there is one fallback and
// not two.
func TestAnOutcomeThisBuildDoesNotKnowFallsBackToTheLegacyField(t *testing.T) {
	s := newStore(t)
	for _, tc := range []struct {
		name    string
		sidecar string
		want    Outcome
	}{
		{"future-bad-abcd1234.ts", `{"verified":false,"outcome":"quarantined","reason":"held"}`, OutcomeUnverified},
		{"future-ok-abcd1234.ts", `{"verified":true,"outcome":"deep-checked","info":{"audioTracks":2}}`, OutcomeVerified},
	} {
		seedFile(t, s, tc.name)
		seedSidecar(t, s, tc.name, tc.sidecar)
		v, recorded := s.Verdict(tc.name)
		if !recorded {
			t.Fatalf("%s: reads as no record", tc.name)
		}
		if v.Outcome != tc.want {
			t.Errorf("%s: outcome = %q, want %q -- an outcome this build cannot interpret "+
				"was acted on rather than degraded to the field it can", tc.name, v.Outcome, tc.want)
		}
	}
	// The unknown state's own reason survives, because it is the only thing an
	// operator on a downgraded build has to go on.
	if v, _ := s.Verdict("future-bad-abcd1234.ts"); v.Reason != "held" {
		t.Errorf("the unknown state's reason was dropped: %q", v.Reason)
	}
}

// A RECORD THAT CONTRADICTS ITSELF FAILS CLOSED. The sidecar is a plain file in
// the data directory: a hand edit, a partial restore from backup, or a merge of
// two data directories can produce one that claims a pass in one field and
// denies it in the other. `verified:true` is what opens a gate, so a
// disagreement is refused rather than half-believed.
func TestARecordThatContradictsItselfIsNotAPass(t *testing.T) {
	s := newStore(t)
	seedFile(t, s, "forged-abcd1234.ts")
	seedSidecar(t, s, "forged-abcd1234.ts",
		`{"verified":false,"outcome":"verified","info":{"audioTracks":9,"videoCodec":"h264"}}`)

	v, recorded := s.Verdict("forged-abcd1234.ts")
	if !recorded {
		t.Fatal("reads as no record")
	}
	if v.Verified() {
		t.Fatalf("a record that says verified:false and outcome:verified reads as a pass: %+v", v)
	}
	if v.Outcome != OutcomeUnverified {
		t.Errorf("outcome = %q, want %q", v.Outcome, OutcomeUnverified)
	}
	if v.Reason != ReasonUnreadableRecord {
		t.Errorf("reason = %q, want %q -- an operator shown a file with no explanation "+
			"cannot act on it", v.Reason, ReasonUnreadableRecord)
	}
	if v.Info != nil {
		t.Errorf("the contradictory record's metadata was kept: %+v", v.Info)
	}
	if list, _ := s.List(); list[0].Media != nil || list[0].Verified {
		t.Errorf("the listing believed the contradictory record: %+v", list[0])
	}
}

// "THERE IS NO RECORD" IS NOT SOMETHING A RECORD MAY SAY. OutcomeUnrecorded is
// the listing's word for the absence of a sidecar, and writing one would both
// be a contradiction and destroy the distinction the settings validators depend
// on -- a file with no record is still usable, and one recorded as anything
// else is not.
//
// The zero Verdict is refused for the same reason: it is what a caller produces
// by forgetting, and it used to mean "unverified, no reason" silently.
func TestAStoreWillNotRecordTheAbsenceOfARecord(t *testing.T) {
	s := newStore(t)
	seedFile(t, s, "x-abcd1234.ts")
	for _, tc := range []struct {
		what string
		v    Verdict
	}{
		{"the absence of a record", Verdict{Outcome: OutcomeUnrecorded}},
		{"a zero verdict", Verdict{}},
		{"a state this build invented", Verdict{Outcome: Outcome("probably-fine")}},
	} {
		if err := s.PutVerdict("x-abcd1234.ts", tc.v); err == nil {
			t.Errorf("PutVerdict accepted %s", tc.what)
		}
	}
	if _, err := os.Stat(filepath.Join(s.dir, sidecarName("x-abcd1234.ts"))); err == nil {
		t.Error("a refused PutVerdict wrote a sidecar anyway")
	}
	// THE CONTROL: the same store, the same name, a storable outcome.
	if err := s.PutVerdict("x-abcd1234.ts", RefusedVerdict("not media")); err != nil {
		t.Fatalf("PutVerdict refuses a legitimate refusal too, so the assertions "+
			"above prove nothing: %v", err)
	}
	if _, recorded := s.Verdict("x-abcd1234.ts"); !recorded {
		t.Error("the control's record was not written")
	}
}

// A refusal is permanent, so the sentence is the operator's ONLY route to
// acting on it: there is no "try again" that could reveal what went wrong. A
// refusal with nothing to say would leave a file unusable and unexplained.
func TestARefusalWithNothingToSayIsRefused(t *testing.T) {
	s := newStore(t)
	seedFile(t, s, "y-abcd1234.ts")
	err := s.PutVerdict("y-abcd1234.ts", RefusedVerdict(""))
	if err == nil {
		t.Fatal("PutVerdict recorded a refusal carrying no reason")
	}
	if !strings.Contains(err.Error(), "no reason") {
		t.Errorf("the error does not say what is missing: %v", err)
	}
	// An UNINSPECTED record with no reason is still allowed, and that asymmetry
	// is deliberate: Store.Verdict's own fail-closed reading produces one, and a
	// file nobody read is answered by "upload it again" with or without a
	// sentence. Kept as a control so the guard above cannot widen unnoticed.
	if err := s.PutVerdict("y-abcd1234.ts", UnverifiedVerdict("")); err != nil {
		t.Errorf("PutVerdict now refuses an uninspected record with no reason: %v", err)
	}
}
