package db

import (
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/analytics"
)

func aip(v int) *int { return &v }

func TestWave4AnalyticsMigrationIsIdempotentAndLeavesTheVersionAlone(t *testing.T) {
	d := testDB(t)
	before, err := d.UserVersion()
	if err != nil {
		t.Fatal(err)
	}
	for pass := 1; pass <= 2; pass++ {
		if err := d.MigrateWave4Analytics(); err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
	}
	for _, table := range []string{"analytics_streams", "analytics_samples", "analytics_chatters"} {
		var n int
		if err := d.sql.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s missing (%d, %v)", table, n, err)
		}
	}
	after, _ := d.UserVersion()
	if after != before {
		t.Fatalf("schema version moved %d -> %d", before, after)
	}
}

// TestAnalyticsStoresNotReportedAsNull: a reading with no count is stored as
// NULL and read back as nil, never as 0.
func TestAnalyticsStoresNotReportedAsNull(t *testing.T) {
	d := testDB(t)
	t0 := time.UnixMilli(1_800_000_000_000)
	id, err := d.OpenAnalyticsStream(t0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.RecordAnalyticsTick(id, t0, []analytics.Reading{
		{Platform: "kick", Account: "k", Name: "K", Reason: analytics.ReasonHidden},
		{Platform: "twitch", Account: "t", Viewers: aip(0)},
	}, "First title", nil); err != nil {
		t.Fatal(err)
	}
	if err := d.RecordAnalyticsTick(id, t0.Add(time.Minute), nil, "Second title", nil); err != nil {
		t.Fatal(err)
	}
	s, err := d.AnalyticsSamples(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(s) != 2 || s[0].Viewers != nil || s[1].Viewers == nil || *s[1].Viewers != 0 {
		t.Fatalf("samples = %+v, want kick nil and twitch a real 0", s)
	}
	row, err := d.GetAnalyticsStream(id)
	if err != nil {
		t.Fatal(err)
	}
	if row.Stream.Title != "First title" || row.Stream.EndedAt != nil || row.Stream.DurationMs != 60_000 {
		t.Fatalf("stream = %+v, want the first title kept, open, 60 s so far", row.Stream)
	}
	if _, err := d.GetAnalyticsStream(id + 99); err != ErrNotFound {
		t.Fatalf("missing stream: %v, want ErrNotFound", err)
	}
}

// TestAnalyticsTalliesChatOnceByIDAndSkipsEchoes: chat that arrived before
// the stream is not counted, a message flushed late is counted once, and
// polyemesis's own echoes are not audience chat.
func TestAnalyticsTalliesChatOnceByIDAndSkipsEchoes(t *testing.T) {
	d := testDB(t)
	now := time.Now()
	msg := func(id, author, name string, echo bool) ChatMessage {
		return ChatMessage{Platform: PlatformTwitch, Account: "acct", MessageID: id, AuthorID: author,
			AuthorName: name, Text: "hello there", Echo: echo, At: now}
	}
	if _, err := d.AppendChatMessages([]ChatMessage{msg("before", "a1", "early bird", false)}); err != nil {
		t.Fatal(err)
	}
	id, err := d.OpenAnalyticsStream(now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.AppendChatMessages([]ChatMessage{
		msg("m1", "a1", "early bird", false),
		msg("m2", "a2", "regular", false),
		msg("m3", "a2", "regular", false),
		msg("m4", "self", "polyemesis", true),
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.RecordAnalyticsTick(id, now, nil, "", nil); err != nil {
		t.Fatal(err)
	}
	// A second tick with nothing new must not count anything twice.
	if err := d.RecordAnalyticsTick(id, now.Add(time.Minute), nil, "", nil); err != nil {
		t.Fatal(err)
	}
	kick := ChatMessage{Platform: PlatformKick, Account: "k", MessageID: "k1", AuthorID: "a3", AuthorName: "kicker", Text: "hi", At: now}
	if _, err := d.AppendChatMessages([]ChatMessage{kick}); err != nil {
		t.Fatal(err)
	}
	// The close tallies what arrived after the last tick.
	if err := d.CloseAnalyticsStream(id, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	counts, top, err := d.AnalyticsChat(id, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(counts) != 2 || counts[0].Platform != "kick" || counts[0].Messages != 1 ||
		counts[1].Platform != "twitch" || counts[1].Messages != 3 {
		t.Fatalf("counts = %+v, want kick 1 and twitch 3 (pre-stream and echo left out)", counts)
	}
	if len(top) != 3 || top[0].Name != "regular" || top[0].Messages != 2 {
		t.Fatalf("top = %+v, want regular first with 2", top)
	}
}

// TestAnalyticsPruneDeletesOnlyEndedStreamsPastTheCutoff, with their rows.
func TestAnalyticsPruneDeletesOnlyEndedStreamsPastTheCutoff(t *testing.T) {
	d := testDB(t)
	t0 := time.UnixMilli(1_800_000_000_000)
	old, _ := d.OpenAnalyticsStream(t0, nil)
	_ = d.RecordAnalyticsTick(old, t0, []analytics.Reading{{Platform: "kick", Viewers: aip(1)}}, "", nil)
	_ = d.CloseAnalyticsStream(old, t0.Add(time.Hour))
	recent, _ := d.OpenAnalyticsStream(t0.Add(48*time.Hour), nil)
	_ = d.CloseAnalyticsStream(recent, t0.Add(49*time.Hour))
	// Open, and older than the cutoff: never pruned while it is running.
	open, _ := d.OpenAnalyticsStream(t0.Add(-time.Hour), nil)

	cutoff := t0.Add(24 * time.Hour)
	n, err := d.PruneAnalytics(cutoff)
	if err != nil || n != 1 {
		t.Fatalf("pruned %d (%v), want 1", n, err)
	}
	if _, err := d.GetAnalyticsStream(old); err != ErrNotFound {
		t.Fatalf("the old stream survived: %v", err)
	}
	for _, id := range []int64{recent, open} {
		if _, err := d.GetAnalyticsStream(id); err != nil {
			t.Fatalf("stream %d was pruned: %v", id, err)
		}
	}
	var samples int
	_ = d.sql.QueryRow(`SELECT COUNT(*) FROM analytics_samples WHERE stream_id = ?`, old).Scan(&samples)
	if samples != 0 {
		t.Fatalf("%d samples of a pruned stream remain; the cascade did not run", samples)
	}
	// A stream that ended exactly at the cutoff is inside the retention.
	edge, _ := d.OpenAnalyticsStream(t0, nil)
	_ = d.CloseAnalyticsStream(edge, cutoff)
	if n, _ := d.PruneAnalytics(cutoff); n != 0 {
		t.Fatalf("a stream ending at the cutoff was pruned")
	}
}

func TestAnalyticsOpenStateAndQoERoundTrip(t *testing.T) {
	d := testDB(t)
	if _, ok, err := d.OpenAnalyticsStreamState(); ok || err != nil {
		t.Fatalf("empty: ok=%v err=%v", ok, err)
	}
	t0 := time.UnixMilli(1_800_000_000_000)
	id, _ := d.OpenAnalyticsStream(t0, &analytics.PlayerQoE{Sessions: 2})
	_ = d.RecordAnalyticsTick(id, t0.Add(time.Minute), nil, "", &analytics.PlayerQoE{Sessions: 5})
	st, ok, err := d.OpenAnalyticsStreamState()
	if !ok || err != nil || st.ID != id || !st.LastAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("open state = %+v ok=%v err=%v", st, ok, err)
	}
	row, _ := d.GetAnalyticsStream(id)
	if row.QoEStart == nil || row.QoEStart.Sessions != 2 || row.QoELast == nil || row.QoELast.Sessions != 5 {
		t.Fatalf("qoe = %+v / %+v", row.QoEStart, row.QoELast)
	}
	if err := d.ForgetAnalyticsQoE(id); err != nil {
		t.Fatal(err)
	}
	row, _ = d.GetAnalyticsStream(id)
	if row.QoEStart != nil {
		t.Fatal("the baseline survived ForgetAnalyticsQoE")
	}
	list, err := d.ListAnalyticsStreams(10)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %d (%v)", len(list), err)
	}
}
