package db

import (
	"errors"
	"github.com/rainmanjam/polyemesis/internal/jobs"
	"testing"
	"time"
)

func highlightRecording(t *testing.T, d *DB) int64 {
	t.Helper()
	r := &Recording{Filename: "show-2026-11-02.mkv", StartedAt: time.UnixMilli(1_800_000_000_000), DurationMS: 600_000}
	if err := d.UpsertRecording(r); err != nil {
		t.Fatal(err)
	}
	all, err := d.ListRecordings()
	if err != nil || len(all) == 0 {
		t.Fatalf("ListRecordings = %v, %v", all, err)
	}
	return all[0].ID
}

func TestClipHighlightTablesAreCreatedIdempotentlyAndLeaveTheVersionAlone(t *testing.T) {
	d := testDB(t)
	before := SchemaVersion()
	for i := 0; i < 2; i++ {
		if err := d.MigrateClipHighlights(); err != nil {
			t.Fatalf("migration %d: %v", i, err)
		}
	}
	if SchemaVersion() != before {
		t.Errorf("an additive table moved the schema version")
	}
}

// A rescan replaces what nobody has looked at, keeps what somebody decided, and
// does not offer a moment again that the operator already dismissed or clipped.
func TestARescanReplacesOnlyPendingSuggestions(t *testing.T) {
	d := testDB(t)
	rec := highlightRecording(t, d)
	first := []Highlight{
		{StartMS: 10_000, EndMS: 40_000, Score: 0.9, Signals: []byte(`[{"kind":"chat"}]`), Excerpt: "one"},
		{StartMS: 100_000, EndMS: 130_000, Score: 0.5, Signals: []byte(`[]`), Excerpt: "two"},
		{StartMS: 200_000, EndMS: 230_000, Score: 0.4, Signals: []byte(`[]`)},
	}
	if n, err := d.ReplacePendingHighlights(rec, first); err != nil || n != 3 {
		t.Fatalf("ReplacePendingHighlights = %d, %v", n, err)
	}
	list, err := d.ListHighlights(rec)
	if err != nil || len(list) != 3 || list[0].Excerpt != "one" || list[0].State != HighlightPending {
		t.Fatalf("ListHighlights = %+v, %v", list, err)
	}
	if _, err := d.DecideHighlight(list[0].ID, HighlightDismissed); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DecideHighlight(list[1].ID, HighlightAccepted); err != nil {
		t.Fatal(err)
	}

	second := []Highlight{
		{StartMS: 12_000, EndMS: 38_000, Score: 0.95, Signals: []byte(`[]`)},  // the dismissed moment again
		{StartMS: 300_000, EndMS: 330_000, Score: 0.3, Signals: []byte(`[]`)}, // new
	}
	if n, err := d.ReplacePendingHighlights(rec, second); err != nil || n != 1 {
		t.Fatalf("rescan kept %d, want 1 (the dismissed moment is not offered again): %v", n, err)
	}
	list, _ = d.ListHighlights(rec)
	states := map[string]int{}
	for _, h := range list {
		states[h.State]++
		if h.StartMS == 200_000 {
			t.Errorf("an undecided suggestion from the first scan survived the rescan")
		}
	}
	if states[HighlightPending] != 1 || states[HighlightDismissed] != 1 || states[HighlightAccepted] != 1 {
		t.Errorf("states after rescan: %v", states)
	}
}

// DECIDING IS ONE-SHOT. Two accepts racing for the same suggestion: exactly one
// wins, and the loser is told it was already decided rather than creating a
// second clip.
//
// MUTATION: the UPDATE drops "AND state = 'pending'". Observed: FAIL, the second
// accept succeeds.
func TestASuggestionIsDecidedExactlyOnce(t *testing.T) {
	d := testDB(t)
	rec := highlightRecording(t, d)
	if _, err := d.ReplacePendingHighlights(rec, []Highlight{{StartMS: 0, EndMS: 30_000, Signals: []byte(`[]`)}}); err != nil {
		t.Fatal(err)
	}
	list, _ := d.ListHighlights(rec)
	id := list[0].ID
	h, err := d.DecideHighlight(id, HighlightAccepted)
	if err != nil || h.State != HighlightAccepted || h.DecidedAt.IsZero() {
		t.Fatalf("first decide = %+v, %v", h, err)
	}
	if _, err := d.DecideHighlight(id, HighlightAccepted); !errors.Is(err, ErrHighlightDecided) {
		t.Errorf("second accept = %v, want ErrHighlightDecided", err)
	}
	if _, err := d.DecideHighlight(id, HighlightDismissed); !errors.Is(err, ErrHighlightDecided) {
		t.Errorf("dismissing an accepted suggestion = %v, want ErrHighlightDecided", err)
	}
	if _, err := d.DecideHighlight(999_999, HighlightAccepted); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id = %v, want ErrNotFound", err)
	}
	if _, err := d.DecideHighlight(id, "maybe"); err == nil {
		t.Errorf("an unknown state was accepted")
	}
	if err := d.SetHighlightJob(id, 42); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.GetHighlight(id); got.JobID != 42 {
		t.Errorf("job id %d, want 42", got.JobID)
	}
	// A clip that could not be queued hands the suggestion back.
	if err := d.ReopenHighlight(id); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.GetHighlight(id); got.State != HighlightPending || got.JobID != 0 {
		t.Errorf("reopened suggestion %+v", got)
	}
}

func TestSuggestionsGoWithTheirRecording(t *testing.T) {
	d := testDB(t)
	rec := highlightRecording(t, d)
	if _, err := d.ReplacePendingHighlights(rec, []Highlight{{StartMS: 0, EndMS: 30_000, Signals: []byte(`[]`)}}); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteRecording(rec); err != nil {
		t.Fatal(err)
	}
	if list, err := d.ListHighlights(rec); err != nil || len(list) != 0 {
		t.Errorf("suggestions outlived their recording: %+v %v", list, err)
	}
}

// The chat detector's input: arrival times inside a window, echoes of the
// broadcaster's own relayed messages excluded.
func TestChatStampsBetweenReturnsArrivalsInTheWindow(t *testing.T) {
	d := testDB(t)
	base := time.UnixMilli(1_800_000_000_000)
	var msgs []ChatMessage
	for i, off := range []int64{-1000, 0, 5000, 9999, 10_000} {
		msgs = append(msgs, ChatMessage{Platform: PlatformTwitch, MessageID: "m" + string(rune('a'+i)),
			Text: "hi", At: base.Add(time.Duration(off) * time.Millisecond)})
	}
	msgs = append(msgs, ChatMessage{Platform: PlatformTwitch, MessageID: "echo", Text: "relayed", Echo: true, At: base.Add(time.Second)})
	if _, err := d.AppendChatMessages(msgs); err != nil {
		t.Fatal(err)
	}
	got, err := d.ChatStampsBetween(base.UnixMilli(), base.UnixMilli()+10_000)
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{0, 5000, 9999}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i].AtMS != base.UnixMilli()+want[i] || got[i].ID == 0 {
			t.Errorf("got %v, want offsets %v", got, want)
		}
	}
}

func TestClipTemplatesAreUpsertedByName(t *testing.T) {
	d := testDB(t)
	a, err := d.PutClipTemplate("Stream layout", []byte(`{"layout":"stacked"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := d.PutClipTemplate("Stream layout", []byte(`{"layout":"blur"}`))
	if err != nil || b.ID != a.ID || string(b.Body) != `{"layout":"blur"}` {
		t.Fatalf("upsert by name = %+v, %v", b, err)
	}
	if _, err := d.PutClipTemplate("Second", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	list, err := d.ListClipTemplates()
	if err != nil || len(list) != 2 || list[0].Name != "Second" {
		t.Fatalf("ListClipTemplates = %+v, %v (sorted by name)", list, err)
	}
	if err := d.DeleteClipTemplate(a.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteClipTemplate(a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting twice = %v, want ErrNotFound", err)
	}
}

// A DECIDED MOMENT INSIDE A LONGER NEW SUGGESTION IS STILL THAT MOMENT. A
// dismissed 15 s span wholly inside a new 60 s one is only 25% of the new span,
// which the old rule offered again; measured against the decided span it is
// 100%.
//
// MUTATION: overlap measured against the new span only. Observed: FAIL.
func TestARescanDoesNotReofferADecidedMomentInsideALongerSpan(t *testing.T) {
	d := testDB(t)
	rec := highlightRecording(t, d)
	if _, err := d.ReplacePendingHighlights(rec, []Highlight{{StartMS: 100_000, EndMS: 115_000, Signals: []byte(`[]`)}}); err != nil {
		t.Fatal(err)
	}
	list, _ := d.ListHighlights(rec)
	if _, err := d.DecideHighlight(list[0].ID, HighlightDismissed); err != nil {
		t.Fatal(err)
	}
	n, err := d.ReplacePendingHighlights(rec, []Highlight{{StartMS: 90_000, EndMS: 150_000, Signals: []byte(`[]`)}})
	if err != nil || n != 0 {
		t.Errorf("a 60 s span around a dismissed 15 s moment was offered again (stored %d, %v)", n, err)
	}
}

// A queued job's params can be upgraded; a running one's cannot -- the worker
// already read them.
//
// MUTATION: the UPDATE drops its state condition. Observed: FAIL, a running
// job's params changed under its worker.
func TestOnlyAJobThatHasNotStartedCanHaveItsParamsUpgraded(t *testing.T) {
	d := testDB(t)
	j, err := d.EnqueueJob(jobs.Job{Kind: "highlights.scan", Target: "recording:1", Params: []byte(`{"useModel":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	ok, err := d.UpgradeQueuedJobParams(j.ID, []byte(`{"useModel":true}`))
	if err != nil || !ok {
		t.Fatalf("a queued job was not upgraded: %v %v", ok, err)
	}
	got, _ := d.GetJob(j.ID)
	if string(got.Params) != `{"useModel":true}` {
		t.Errorf("params %s", got.Params)
	}
	if _, err := d.ClaimJob([]jobs.Kind{"highlights.scan"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	ok, err = d.UpgradeQueuedJobParams(j.ID, []byte(`{"useModel":false}`))
	if err != nil || ok {
		t.Errorf("a running job's params were changed: %v %v", ok, err)
	}
}
