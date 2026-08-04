package oauth

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
)

func ptrBool(v bool) *bool { return &v }

// facebookKeyForLiveVideo builds a stream key in the shape FacebookLiveVideoID
// actually parses: the live video id followed by the query string a real
// ingest URL carries. See TestFacebookLiveVideoIDIsRecoverableFromTheStoredStreamKey.
func facebookKeyForLiveVideo(id string) string {
	return id + "?s_bl=1&s_psm=1"
}

// The trap this whole file exists for.
//
// YouTube's liveBroadcasts.update is destructive BY PART, not by field: sending
// `part=status` without a privacyStatus does not leave the current value alone,
// it "will remove the existing privacy setting and revert to the default". A
// naive PATCH-shaped implementation can therefore make a private broadcast
// public, and the operator finds out from the audience.
func TestAStatusWriteAlwaysCarriesPrivacyStatus(t *testing.T) {
	var log []capture
	ytStub(t, &log, ytOneUpcoming)

	y := &YouTube{}
	if _, err := y.PushCompliance(context.Background(), "cid", "tok", ComplianceTarget{},
		db.Compliance{Privacy: db.PrivacyUnlisted}); err != nil {
		t.Fatalf("PushCompliance: %v", err)
	}

	var wrote bool
	for _, c := range log {
		if c.Method != "PUT" || c.Path != "/liveBroadcasts" {
			continue
		}
		if !strings.Contains(c.Query, "part=status") {
			continue
		}
		wrote = true
		st, _ := c.Body["status"].(map[string]any)
		if st == nil || st["privacyStatus"] != "unlisted" {
			t.Errorf("a part=status write carried %v; without privacyStatus YouTube "+
				"reverts the broadcast to its default visibility", c.Body)
		}
	}
	if !wrote {
		t.Fatal("no part=status write happened, so the privacy setting did nothing")
	}
}

// The zero value must touch nothing at all. A destination that has never been
// given a compliance setting has to produce exactly the API calls it produced
// before this existed.
func TestAnEmptyComplianceBlockWritesNothing(t *testing.T) {
	var log []capture
	ytStub(t, &log, ytOneUpcoming)

	y := &YouTube{}
	res, err := y.PushCompliance(context.Background(), "cid", "tok", ComplianceTarget{}, db.Compliance{})
	if err != nil {
		t.Fatalf("PushCompliance: %v", err)
	}
	if len(res.Applied) != 0 {
		t.Errorf("an empty block reported %v as applied", res.Applied)
	}
	for _, c := range log {
		if c.Method == "PUT" {
			t.Errorf("an empty compliance block still wrote: %s %s?%s", c.Method, c.Path, c.Query)
		}
	}
}

// selfDeclaredMadeForKids is settable on liveBroadcasts.insert and is ABSENT
// from update's settable list, so for a broadcast that already exists it has to
// go through videos.update. Anyone who assumes symmetry writes a call that
// returns 200 and changes nothing.
func TestMadeForKidsGoesThroughVideosNotLiveBroadcasts(t *testing.T) {
	for _, want := range []bool{true, false} {
		var log []capture
		ytStub(t, &log, ytOneUpcoming)

		y := &YouTube{}
		if _, err := y.PushCompliance(context.Background(), "cid", "tok", ComplianceTarget{},
			db.Compliance{MadeForKids: ptrBool(want)}); err != nil {
			t.Fatalf("PushCompliance: %v", err)
		}

		var found bool
		for _, c := range log {
			if c.Method != "PUT" {
				continue
			}
			if c.Path == "/liveBroadcasts" {
				t.Errorf("made-for-kids was sent to liveBroadcasts, which ignores it: %v", c.Body)
			}
			if c.Path != "/videos" {
				continue
			}
			found = true
			st, _ := c.Body["status"].(map[string]any)
			if st == nil || st["selfDeclaredMadeForKids"] != want {
				t.Errorf("videos.update carried %v, want selfDeclaredMadeForKids=%v", c.Body, want)
			}
		}
		if !found {
			t.Errorf("made-for-kids=%v produced no videos.update at all", want)
		}
	}
}

// false is a real declaration -- "this is not for children" -- and has to be
// distinguishable from "the operator has not said". That is the whole reason
// the field is a pointer.
func TestMadeForKidsFalseIsDistinctFromUnset(t *testing.T) {
	if (db.Compliance{MadeForKids: ptrBool(false)}).Empty() {
		t.Error("an explicit made-for-kids=false reads as an empty block, so it would never be sent")
	}
	if !(db.Compliance{}).Empty() {
		t.Error("an unset block does not read as empty")
	}
}

// Twitch reads labels back as a flat list and WRITES them as
// [{"id":..,"is_enabled":..}]. Copying the read shape into a write produces a
// request Twitch rejects, and the operator sees a go-live that failed for no
// visible reason.
func TestTwitchLabelsUseTheWriteShape(t *testing.T) {
	got := twitchLabelPayload(map[string]bool{
		"Gambling":           true,
		"SexualThemes":       false,
		"NotARealLabelAtAll": true,
	})
	if len(got) != 2 {
		t.Fatalf("payload has %d entries, want 2 (the unknown label dropped): %v", len(got), got)
	}
	// Sorted, so the body is deterministic and therefore assertable.
	if got[0]["id"] != "Gambling" || got[0]["is_enabled"] != true {
		t.Errorf("first entry = %v, want Gambling enabled", got[0])
	}
	// false is not the same as absent: it actively CLEARS the label, which is
	// how an operator removes one.
	if got[1]["id"] != "SexualThemes" || got[1]["is_enabled"] != false {
		t.Errorf("second entry = %v, want SexualThemes explicitly disabled", got[1])
	}
}

// MatureGame appears when a channel is READ and Twitch will not accept it on a
// write. Offering it would give the operator a control that silently never
// applies, so validation names it specifically.
func TestMatureGameIsRefusedByName(t *testing.T) {
	probs := (db.Compliance{Labels: map[string]bool{"MatureGame": true}}).Problems()
	if len(probs) == 0 {
		t.Fatal("MatureGame was accepted")
	}
	if !strings.Contains(probs[0], "read but never set") {
		t.Errorf("problem was %q; it should say WHY rather than just refusing", probs[0])
	}
	if db.ValidTwitchLabel("MatureGame") {
		t.Error("MatureGame is in the writable set")
	}

	// The positive case: every label we DO offer must validate.
	for _, id := range db.TwitchLabels {
		if p := (db.Compliance{Labels: map[string]bool{id: true}}).Problems(); len(p) != 0 {
			t.Errorf("%s is offered and refused: %v", id, p)
		}
	}
}

func TestUnknownPrivacyIsRefused(t *testing.T) {
	if p := (db.Compliance{Privacy: "semi-public"}).Problems(); len(p) == 0 {
		t.Error("an unknown privacy value was accepted")
	}
	for _, v := range db.PrivacyStatuses {
		if p := (db.Compliance{Privacy: v}).Problems(); len(p) != 0 {
			t.Errorf("%s is offered and refused: %v", v, p)
		}
	}
}

func TestComplianceForFindsOnlyThePlatformsThatHaveOne(t *testing.T) {
	// Kick has no compliance surface. It must be ABSENT rather than present
	// and refusing, so the caller handles "this platform does not do this"
	// once instead of at every call site.
	for _, p := range []db.Platform{db.PlatformYouTube, db.PlatformTwitch, db.PlatformFacebook} {
		if _, ok := ComplianceFor(p); !ok {
			t.Errorf("ComplianceFor(%s) found nothing; its stored compliance can never be sent", p)
		}
	}
	if _, ok := ComplianceFor(db.PlatformKick); ok {
		t.Error("ComplianceFor(kick) claims a capability Kick does not have")
	}
}

func TestFacebookComplianceGoesThroughTheConfirmedPrivacyPath(t *testing.T) {
	// Graph documents no update surface for LiveVideo, so the only honest
	// report is one the platform confirmed. This must not grow a second,
	// unconfirmed path just because it is reached from somewhere new.
	fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/9":
			writeJSONBody(t, w, http.StatusOK, map[string]any{"success": true})
		case r.Method == http.MethodGet && r.URL.Path == "/9":
			writeJSONBody(t, w, http.StatusOK, map[string]any{
				"id": "9", "privacy": map[string]any{"value": "SELF"},
			})
		default:
			http.Error(w, "{}", http.StatusNotFound)
		}
	})
	cp, ok := ComplianceFor(db.PlatformFacebook)
	if !ok {
		t.Fatal("Facebook has no compliance capability")
	}
	res, err := cp.PushCompliance(context.Background(), "cid", "user-token",
		ComplianceTarget{AccountRef: "user:1000", StreamKey: facebookKeyForLiveVideo("9")},
		db.Compliance{FacebookPrivacy: db.FBPrivacySelf})
	if err != nil {
		t.Fatalf("PushCompliance: %v", err)
	}
	if !slices.Contains(res.Applied, FieldPrivacy) {
		t.Errorf("applied = %v, want FieldPrivacy after a confirmed read-back", res.Applied)
	}
}

func TestAnEmptyComplianceSendsNothingAtAll(t *testing.T) {
	// A destination that has never been given a compliance setting must produce
	// exactly the API calls it produced before this existed.
	log := fbServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSONBody(t, w, http.StatusOK, map[string]any{"success": true})
	})
	cp, _ := ComplianceFor(db.PlatformFacebook)
	if _, err := cp.PushCompliance(context.Background(), "cid", "user-token",
		ComplianceTarget{AccountRef: "user:1000"}, db.Compliance{}); err != nil {
		t.Fatalf("PushCompliance: %v", err)
	}
	if len(*log) != 0 {
		t.Errorf("an empty compliance made %d requests: %+v", len(*log), *log)
	}
}
