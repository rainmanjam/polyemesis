package oauth

import (
	"context"
	"strings"
	"testing"
)

// A broadcast that is still editable, WITH contentDetails populated to
// non-default values. The non-defaults are the point: a write that carries
// fields through correctly is indistinguishable from one that zeroes them if
// the stored values happen to be zero.
const ytEditableWithDetails = `{"items":[{"id":"bcast",
	"snippet":{"title":"old title","description":"old body",
		"scheduledStartTime":"2026-07-01T10:00:00Z"},
	"status":{"lifeCycleStatus":"ready"},
	"contentDetails":{"enableDvr":true,"enableAutoStart":true,"enableAutoStop":false,
		"monitorStream":{"enableMonitorStream":true,"broadcastStreamDelayMs":30000}}}]}`

// The same broadcast once it has gone live, which is when the contentDetails
// toggles freeze.
const ytLiveWithDetails = `{"items":[{"id":"bcast",
	"snippet":{"title":"old title","description":"old body",
		"scheduledStartTime":"2026-07-01T10:00:00Z"},
	"status":{"lifeCycleStatus":"live"},
	"contentDetails":{"enableDvr":true,"enableAutoStart":true,"enableAutoStop":false,
		"monitorStream":{"enableMonitorStream":true,"broadcastStreamDelayMs":30000}}}]}`

// TRAP 1. liveBroadcasts.update requires FOUR properties on every call and
// rejects a partial update. Asserted on the request body rather than on a
// helper, because the request is the only thing YouTube sees.
func TestEveryBroadcastUpdateCarriesTheFourRequiredProperties(t *testing.T) {
	var log []capture
	y, _ := ytStub(t, &log, ytEditableWithDetails)

	dvr := false
	if _, err := y.PushBroadcastSettings(context.Background(), "cid", "tok",
		BroadcastSettings{EnableDvr: &dvr}); err != nil {
		t.Fatalf("PushBroadcastSettings: %v", err)
	}

	var seen bool
	for _, c := range log {
		if c.Method != "PUT" || c.Path != "/liveBroadcasts" {
			continue
		}
		seen = true
		if c.Body["id"] != "bcast" {
			t.Errorf("no id on the update: %v", c.Body)
		}
		snip, _ := c.Body["snippet"].(map[string]any)
		if snip == nil || snip["scheduledStartTime"] == "" || snip["scheduledStartTime"] == nil {
			t.Errorf("no scheduledStartTime; YouTube rejects the update: %v", c.Body)
		}
		cd, _ := c.Body["contentDetails"].(map[string]any)
		ms, _ := cd["monitorStream"].(map[string]any)
		if ms == nil {
			t.Fatalf("no monitorStream; YouTube rejects the update: %v", c.Body)
		}
		if _, ok := ms["enableMonitorStream"]; !ok {
			t.Errorf("monitorStream has no enableMonitorStream: %v", ms)
		}
		if _, ok := ms["broadcastStreamDelayMs"]; !ok {
			t.Errorf("monitorStream has no broadcastStreamDelayMs: %v", ms)
		}
	}
	if !seen {
		t.Fatal("no liveBroadcasts update happened at all")
	}
}

// TRAP 2, and the one that silently damages an operator's channel.
//
// liveBroadcasts.update is destructive BY PART. A write that changes one field
// of a part must carry every OTHER field of that part through unchanged, or
// YouTube reverts them to defaults. Changing only the DVR flag must therefore
// leave the title, the description and the other two toggles exactly as they
// were.
func TestAPartialChangeCarriesTheRestOfThePartThrough(t *testing.T) {
	var log []capture
	y, _ := ytStub(t, &log, ytEditableWithDetails)

	dvr := false
	if _, err := y.PushBroadcastSettings(context.Background(), "cid", "tok",
		BroadcastSettings{EnableDvr: &dvr}); err != nil {
		t.Fatalf("PushBroadcastSettings: %v", err)
	}

	for _, c := range log {
		if c.Method != "PUT" || c.Path != "/liveBroadcasts" {
			continue
		}
		snip, _ := c.Body["snippet"].(map[string]any)
		if snip["title"] != "old title" {
			t.Errorf("title = %v; omitting it from part=snippet blanks it", snip["title"])
		}
		if snip["description"] != "old body" {
			t.Errorf("description = %v; omitting it from part=snippet blanks it", snip["description"])
		}
		cd, _ := c.Body["contentDetails"].(map[string]any)
		// The field the operator DID change.
		if cd["enableDvr"] != false {
			t.Errorf("enableDvr = %v, want the requested false", cd["enableDvr"])
		}
		// The fields they did not. Stored true, and must arrive true.
		if cd["enableAutoStart"] != true {
			t.Errorf("enableAutoStart = %v, want the stored true carried through; "+
				"a default would silently disable the operator's auto-start", cd["enableAutoStart"])
		}
		ms, _ := cd["monitorStream"].(map[string]any)
		if ms["enableMonitorStream"] != true {
			t.Errorf("enableMonitorStream = %v, want the stored true", ms["enableMonitorStream"])
		}
		if ms["broadcastStreamDelayMs"] != float64(30000) {
			t.Errorf("broadcastStreamDelayMs = %v, want the stored 30000; a zero here "+
				"removes the operator's stream delay", ms["broadcastStreamDelayMs"])
		}
	}
}

// The same rule seen from the other side: changing ONLY the schedule must not
// disturb the toggles, even though trap 1 forces contentDetails onto the
// request anyway.
func TestChangingOnlyTheScheduleDoesNotDisturbTheToggles(t *testing.T) {
	var log []capture
	y, _ := ytStub(t, &log, ytEditableWithDetails)

	when := "2026-08-01T20:00:00Z"
	if _, err := y.PushBroadcastSettings(context.Background(), "cid", "tok",
		BroadcastSettings{ScheduledStart: &when}); err != nil {
		t.Fatalf("PushBroadcastSettings: %v", err)
	}
	for _, c := range log {
		if c.Method != "PUT" || c.Path != "/liveBroadcasts" {
			continue
		}
		snip, _ := c.Body["snippet"].(map[string]any)
		if snip["scheduledStartTime"] != when {
			t.Errorf("scheduledStartTime = %v, want %q", snip["scheduledStartTime"], when)
		}
		cd, _ := c.Body["contentDetails"].(map[string]any)
		if cd["enableDvr"] != true || cd["enableAutoStart"] != true {
			t.Errorf("a schedule-only change altered the toggles: %v", cd)
		}
	}
}

// TRAP 3. The toggles freeze once a broadcast leaves created/ready.
func TestTheEditableWindowIsAnAllowlist(t *testing.T) {
	for _, tc := range []struct {
		status     string
		wantFrozen bool
	}{
		{"created", false},
		{"ready", false},
		{"READY", false}, // case is YouTube's, not ours
		{"testing", true},
		{"live", true},
		{"complete", true},
		// An unknown value must read as LOCKED. Offering an edit that fails is
		// worse than withholding one that might have worked, and a lifecycle
		// state this build has never heard of is exactly where that applies.
		{"someFutureState", true},
		{"", true},
	} {
		t.Run(tc.status, func(t *testing.T) {
			if got := contentDetailsFrozen(tc.status); got != tc.wantFrozen {
				t.Errorf("contentDetailsFrozen(%q) = %v, want %v", tc.status, got, tc.wantFrozen)
			}
		})
	}
}

func TestBroadcastWindowReportsWhatIsLockedAndWhy(t *testing.T) {
	var log []capture
	y, _ := ytStub(t, &log, ytLiveWithDetails)

	w, err := y.BroadcastWindow(context.Background(), "tok")
	if err != nil {
		t.Fatalf("BroadcastWindow: %v", err)
	}
	if !w.ContentDetailsLocked {
		t.Error("a live broadcast reports its contentDetails as editable")
	}
	// The status is passed through in YouTube's own words, so an operator
	// comparing this against YouTube Studio sees the same term.
	if w.LifeCycleStatus != "live" {
		t.Errorf("LifeCycleStatus = %q, want YouTube's own %q", w.LifeCycleStatus, "live")
	}
	if !strings.Contains(w.LockedReason, "live") {
		t.Errorf("the reason does not name the state that caused it: %q", w.LockedReason)
	}

	// And the editable case must NOT claim a lock, or every control is
	// disabled forever.
	var log2 []capture
	y2, _ := ytStub(t, &log2, ytEditableWithDetails)
	w2, err := y2.BroadcastWindow(context.Background(), "tok")
	if err != nil {
		t.Fatalf("BroadcastWindow: %v", err)
	}
	if w2.ContentDetailsLocked || w2.LockedReason != "" {
		t.Errorf("a ready broadcast reports as locked: %+v", w2)
	}
}

// TRAP 4 is covered by setTags reusing the existing snippet read. videos.update
// replaces the WHOLE snippet part, so a tags write must carry title,
// description and category through.
func TestSettingTagsDoesNotEraseTheRestOfTheVideoSnippet(t *testing.T) {
	var log []capture
	y, _ := ytStub(t, &log, ytEditableWithDetails)

	tags := []string{"live", "house"}
	res, err := y.PushBroadcastSettings(context.Background(), "cid", "tok",
		BroadcastSettings{Tags: &tags})
	if err != nil {
		t.Fatalf("PushBroadcastSettings: %v", err)
	}

	var wrote bool
	for _, c := range log {
		if c.Method != "PUT" || c.Path != "/videos" {
			continue
		}
		wrote = true
		snip, _ := c.Body["snippet"].(map[string]any)
		got, _ := snip["tags"].([]any)
		if len(got) != 2 || got[0] != "live" {
			t.Errorf("tags = %v, want the two requested", snip["tags"])
		}
		// The stub's video has these; losing them is the bug.
		if snip["title"] != "old" {
			t.Errorf("title = %v; videos.update replaces the whole snippet, so omitting "+
				"it blanks the video's title", snip["title"])
		}
		if snip["categoryId"] != "1" {
			t.Errorf("categoryId = %v; omitting it is rejected and dropping it loses "+
				"the operator's category", snip["categoryId"])
		}
	}
	if !wrote {
		t.Fatal("no videos.update happened, so the tags went nowhere")
	}
	if len(res.Applied) == 0 {
		t.Error("the result reported nothing applied")
	}
}

// Tags REPLACE rather than merge, which is what snippet.tags[] does. Asserted
// so nobody later "fixes" it into an append and produces a tag list that only
// ever grows.
func TestTagsReplaceRatherThanMerge(t *testing.T) {
	var log []capture
	y, _ := ytStub(t, &log, ytEditableWithDetails)

	tags := []string{"only"}
	if _, err := y.PushBroadcastSettings(context.Background(), "cid", "tok",
		BroadcastSettings{Tags: &tags}); err != nil {
		t.Fatalf("PushBroadcastSettings: %v", err)
	}
	for _, c := range log {
		if c.Method != "PUT" || c.Path != "/videos" {
			continue
		}
		snip, _ := c.Body["snippet"].(map[string]any)
		got, _ := snip["tags"].([]any)
		if len(got) != 1 {
			t.Errorf("tags = %v, want exactly the one requested; the stub's video "+
				"already had two and they must not survive", snip["tags"])
		}
	}
}

// The zero value must touch nothing at all.
func TestEmptyBroadcastSettingsWriteNothing(t *testing.T) {
	var log []capture
	y, _ := ytStub(t, &log, ytEditableWithDetails)

	res, err := y.PushBroadcastSettings(context.Background(), "cid", "tok", BroadcastSettings{})
	if err != nil {
		t.Fatalf("PushBroadcastSettings: %v", err)
	}
	if len(res.Applied) != 0 {
		t.Errorf("an empty block reported %v as applied", res.Applied)
	}
	for _, c := range log {
		if c.Method == "PUT" {
			t.Errorf("an empty block still wrote: %s %s", c.Method, c.Path)
		}
	}
}

// false is a real setting -- "turn the DVR off" -- and has to be
// distinguishable from "the operator did not mention it". That is the whole
// reason every field is a pointer.
func TestFalseIsDistinctFromUnset(t *testing.T) {
	off := false
	if (BroadcastSettings{EnableDvr: &off}).Empty() {
		t.Error("an explicit enableDvr=false reads as empty, so it would never be sent")
	}
	if !(BroadcastSettings{}).Empty() {
		t.Error("an unset block does not read as empty")
	}
	if !(BroadcastSettings{EnableDvr: &off}).TouchesContentDetails() {
		t.Error("a DVR change does not register as touching contentDetails")
	}
	when := "2026-08-01T20:00:00Z"
	if (BroadcastSettings{ScheduledStart: &when}).TouchesContentDetails() {
		t.Error("a schedule change registers as touching contentDetails, which would " +
			"wrongly attract the frozen-window advice")
	}
}
