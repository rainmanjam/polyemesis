package oauth

import (
	"context"
	"github.com/rainmanjam/polyemesis/internal/db"
	"strings"
	"testing"
)

func ptrBool(v bool) *bool { return &v }

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
	if _, err := y.PushCompliance(context.Background(), "cid", "tok",
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
	res, err := y.PushCompliance(context.Background(), "cid", "tok", db.Compliance{})
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
		if _, err := y.PushCompliance(context.Background(), "cid", "tok",
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
