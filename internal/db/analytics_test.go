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
	id, err := d.OpenAnalyticsStream(t0, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.RecordAnalyticsTick(id, tickOf(t0, []analytics.Reading{
		{Platform: "kick", Account: "k", Name: "K", Reason: analytics.ReasonHidden},
		{Platform: "twitch", Account: "t", Viewers: aip(0)},
	}, "First title", nil)); err != nil {
		t.Fatal(err)
	}
	if err := d.RecordAnalyticsTick(id, tickOf(t0.Add(time.Minute), nil, "Second title", nil)); err != nil {
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
	early := msg("before", "a1", "early bird", false)
	early.At = now.Add(-time.Second)
	if _, err := d.AppendChatMessages([]ChatMessage{early}); err != nil {
		t.Fatal(err)
	}
	id, err := d.OpenAnalyticsStream(now, nil, 0)
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
	// The stream's accounts are the ones its readings name; only their chat is
	// the stream's (#7).
	accts := []analytics.Reading{{Platform: "twitch", Account: "acct"}, {Platform: "kick", Account: "k"}}
	if err := d.RecordAnalyticsTick(id, tickOf(now, accts, "", nil)); err != nil {
		t.Fatal(err)
	}
	// A second tick with nothing new must not count anything twice.
	if err := d.RecordAnalyticsTick(id, tickOf(now.Add(time.Minute), nil, "", nil)); err != nil {
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
	old, _ := d.OpenAnalyticsStream(t0, nil, 0)
	_ = d.RecordAnalyticsTick(old, tickOf(t0, []analytics.Reading{{Platform: "kick", Viewers: aip(1)}}, "", nil))
	_ = d.CloseAnalyticsStream(old, t0.Add(time.Hour))
	recent, _ := d.OpenAnalyticsStream(t0.Add(48*time.Hour), nil, 0)
	_ = d.CloseAnalyticsStream(recent, t0.Add(49*time.Hour))
	// Open, and older than the cutoff: never pruned while it is running.
	open, _ := d.OpenAnalyticsStream(t0.Add(-time.Hour), nil, 0)

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
	edge, _ := d.OpenAnalyticsStream(t0, nil, 0)
	_ = d.CloseAnalyticsStream(edge, cutoff)
	if n, _ := d.PruneAnalytics(cutoff); n != 0 {
		t.Fatalf("a stream ending at the cutoff was pruned")
	}
}

func TestAnalyticsOpenStateAndQoERoundTrip(t *testing.T) {
	d := testDB(t)
	if open, err := d.OpenAnalyticsStreams(); len(open) != 0 || err != nil {
		t.Fatalf("empty: %d open, err=%v", len(open), err)
	}
	t0 := time.UnixMilli(1_800_000_000_000)
	id, _ := d.OpenAnalyticsStream(t0, &analytics.PlayerQoE{Sessions: 2}, 0)
	_ = d.RecordAnalyticsTick(id, tickOf(t0.Add(time.Minute), nil, "", &analytics.PlayerQoE{Sessions: 5}))
	open, err := d.OpenAnalyticsStreams()
	if err != nil || len(open) != 1 || open[0].ID != id || !open[0].LastAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("open = %+v err=%v", open, err)
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

// TestAnalyticsTalliesOnlyTheStreamsOwnAccounts (#7): chat in a connected
// channel this stream is not on is not this stream's audience.
func TestAnalyticsTalliesOnlyTheStreamsOwnAccounts(t *testing.T) {
	d := testDB(t)
	now := time.Now()
	id, err := d.OpenAnalyticsStream(now, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.AppendChatMessages([]ChatMessage{
		{Platform: PlatformTwitch, Account: "ours", MessageID: "1", AuthorID: "a", AuthorName: "viewer", Text: "hi", At: now},
		{Platform: PlatformTwitch, Account: "elsewhere", MessageID: "2", AuthorID: "b", AuthorName: "stranger", Text: "hi", At: now},
		{Platform: PlatformKick, Account: "ours", MessageID: "3", AuthorID: "c", AuthorName: "wrong platform", Text: "hi", At: now},
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.RecordAnalyticsTick(id, tickOf(now, []analytics.Reading{{Platform: "twitch", Account: "ours"}}, "", nil)); err != nil {
		t.Fatal(err)
	}
	counts, top, err := d.AnalyticsChat(id, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(counts) != 1 || counts[0].Messages != 1 || len(top) != 1 || top[0].Name != "viewer" {
		t.Fatalf("counts = %+v top = %+v, want only the stream's own Twitch channel", counts, top)
	}
}

// TestAnalyticsChatIsBoundedToTheStream (#8): chat from the go-live lead is
// the stream's; chat after its last on-air tick, tallied at close, is not.
func TestAnalyticsChatIsBoundedToTheStream(t *testing.T) {
	d := testDB(t)
	t0 := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	msg := func(id string, at time.Time) ChatMessage {
		return ChatMessage{Platform: PlatformTwitch, Account: "ours", MessageID: id, AuthorID: id, AuthorName: id, Text: "hi", At: at}
	}
	if _, err := d.AppendChatMessages([]ChatMessage{
		msg("long-before", t0.Add(-10*time.Minute)),
		msg("in-the-lead", t0.Add(-30*time.Second)),
	}); err != nil {
		t.Fatal(err)
	}
	id, err := d.OpenAnalyticsStream(t0, nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.AppendChatMessages([]ChatMessage{msg("during", t0.Add(time.Minute))}); err != nil {
		t.Fatal(err)
	}
	last := t0.Add(2 * time.Minute)
	if err := d.RecordAnalyticsTick(id, tickOf(last, []analytics.Reading{{Platform: "twitch", Account: "ours"}}, "", nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.AppendChatMessages([]ChatMessage{msg("after-the-end", last.Add(2*time.Minute))}); err != nil {
		t.Fatal(err)
	}
	if err := d.CloseAnalyticsStream(id, last); err != nil {
		t.Fatal(err)
	}
	_, top, err := d.AnalyticsChat(id, 10)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, c := range top {
		got[c.Name] = true
	}
	if !got["in-the-lead"] || !got["during"] || got["long-before"] || got["after-the-end"] || len(top) != 2 {
		t.Fatalf("chatters = %+v, want the lead and the stream, not before or after", top)
	}
}

// TestAnalyticsPruneDropsOldSamplesOfAnOpenStream (#11).
func TestAnalyticsPruneDropsOldSamplesOfAnOpenStream(t *testing.T) {
	d := testDB(t)
	t0 := time.UnixMilli(1_800_000_000_000)
	id, _ := d.OpenAnalyticsStream(t0, nil, 0)
	_ = d.RecordAnalyticsTick(id, tickOf(t0, []analytics.Reading{{Platform: "kick", Viewers: aip(1)}}, "", nil))
	_ = d.RecordAnalyticsTick(id, tickOf(t0.Add(48*time.Hour), []analytics.Reading{{Platform: "kick", Viewers: aip(2)}}, "", nil))
	if _, err := d.PruneAnalytics(t0.Add(24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	s, _ := d.AnalyticsSamples(id)
	if len(s) != 1 || *s[0].Viewers != 2 {
		t.Fatalf("samples = %+v, want only the one inside the retention", s)
	}
	if _, err := d.GetAnalyticsStream(id); err != nil {
		t.Fatalf("the open stream itself was pruned: %v", err)
	}
}

// TestAnalyticsCloseStoresTheTotals (#12): the list reads a closed stream's
// peak and average from its row, not from its samples.
func TestAnalyticsCloseStoresTheTotals(t *testing.T) {
	d := testDB(t)
	t0 := time.UnixMilli(1_800_000_000_000)
	id, _ := d.OpenAnalyticsStream(t0, nil, 0)
	_ = d.RecordAnalyticsTick(id, tickOf(t0, []analytics.Reading{{Platform: "kick", Viewers: aip(10)}}, "", nil))
	_ = d.RecordAnalyticsTick(id, tickOf(t0.Add(time.Minute), []analytics.Reading{{Platform: "kick", Viewers: aip(20)}}, "", nil))
	if err := d.CloseAnalyticsStream(id, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// The samples are gone; the totals stay with the stream.
	if _, err := d.sql.Exec(`DELETE FROM analytics_samples WHERE stream_id = ?`, id); err != nil {
		t.Fatal(err)
	}
	rows, err := d.ListAnalyticsStreams(10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("list = %d (%v)", len(rows), err)
	}
	st := rows[0].Stream
	if st.PeakTotal == nil || *st.PeakTotal != 20 || st.AverageTotal == nil || *st.AverageTotal != 15 {
		t.Fatalf("stored totals = %v / %v, want 20 / 15", st.PeakTotal, st.AverageTotal)
	}
}

func TestAnalyticsRecentSamplesAreTheLastTicks(t *testing.T) {
	d := testDB(t)
	t0 := time.UnixMilli(1_800_000_000_000)
	id, _ := d.OpenAnalyticsStream(t0, nil, 0)
	for i := 0; i < 5; i++ {
		_ = d.RecordAnalyticsTick(id, tickOf(t0.Add(time.Duration(i)*time.Minute), []analytics.Reading{
			{Platform: "kick", Viewers: aip(i)}, {Platform: "twitch", Reason: analytics.ReasonQuota},
		}, "", nil))
	}
	s, err := d.AnalyticsRecentSamples(id, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(s) != 4 || !s[0].At.Equal(t0.Add(3*time.Minute)) || s[1].Reason != analytics.ReasonQuota {
		t.Fatalf("recent = %+v, want the last two ticks, both samples each, reasons kept", s)
	}
}

// tickOf is a tick whose chat accounts are its readings' accounts.
func tickOf(at time.Time, rs []analytics.Reading, title string, qoe *analytics.PlayerQoE) analytics.Tick {
	t := analytics.Tick{At: at, Readings: rs, TallyChat: true, Title: title, QoE: qoe}
	for _, r := range rs {
		if r.Account != "" {
			t.ChatAccounts = append(t.ChatAccounts, analytics.ChatAccount{Platform: r.Platform, Account: r.Account})
		}
	}
	return t
}

// ---------------------------------------------------------- review round 2

// TestAnalyticsChatAccountsComeFromTheTickNotTheReadings (R2): a platform
// with no viewer count -- Facebook here -- still has its chat counted.
func TestAnalyticsChatAccountsComeFromTheTickNotTheReadings(t *testing.T) {
	d := testDB(t)
	now := time.Now()
	id, _ := d.OpenAnalyticsStream(now, nil, 0)
	if _, err := d.AppendChatMessages([]ChatMessage{
		{Platform: PlatformFacebook, Account: "page", MessageID: "1", AuthorID: "f", AuthorName: "fan", Text: "hi", At: now},
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.RecordAnalyticsTick(id, analytics.Tick{At: now, TallyChat: true,
		ChatAccounts: []analytics.ChatAccount{{Platform: "facebook", Account: "page"}}}); err != nil {
		t.Fatal(err)
	}
	counts, _, _ := d.AnalyticsChat(id, 10)
	if len(counts) != 1 || counts[0].Platform != "facebook" || counts[0].Messages != 1 {
		t.Fatalf("counts = %+v, want Facebook's chat counted", counts)
	}
}

// TestAnalyticsATickThatCouldNotNameItsChatsTalliesNothing (R2): the cursor
// does not move past chat that was not counted.
func TestAnalyticsATickThatCouldNotNameItsChatsTalliesNothing(t *testing.T) {
	d := testDB(t)
	now := time.Now()
	id, _ := d.OpenAnalyticsStream(now, nil, 0)
	acct := []analytics.ChatAccount{{Platform: "twitch", Account: "ours"}}
	// The first tick knows no chat yet (its target list came back empty).
	_ = d.RecordAnalyticsTick(id, analytics.Tick{At: now, TallyChat: true})
	if _, err := d.AppendChatMessages([]ChatMessage{
		{Platform: PlatformTwitch, Account: "ours", MessageID: "1", AuthorID: "a", AuthorName: "viewer", Text: "hi", At: now},
	}); err != nil {
		t.Fatal(err)
	}
	_ = d.RecordAnalyticsTick(id, analytics.Tick{At: now.Add(time.Minute), TallyChat: false})
	_ = d.RecordAnalyticsTick(id, analytics.Tick{At: now.Add(2 * time.Minute), TallyChat: true, ChatAccounts: acct})
	counts, _, _ := d.AnalyticsChat(id, 10)
	if len(counts) != 1 || counts[0].Messages != 1 {
		t.Fatalf("counts = %+v, want the message counted by the next good tick", counts)
	}
}

// TestAnalyticsOpenStreamsListsEveryOpenRow (R6), with each one's wait (R5).
func TestAnalyticsOpenStreamsListsEveryOpenRow(t *testing.T) {
	d := testDB(t)
	t0 := time.UnixMilli(1_800_000_000_000)
	a, _ := d.OpenAnalyticsStream(t0, nil, 0)
	b, _ := d.OpenAnalyticsStream(t0.Add(time.Hour), nil, 0)
	_ = d.RecordAnalyticsTick(b, analytics.Tick{At: t0.Add(time.Hour), Wait: 7 * time.Minute})
	c, _ := d.OpenAnalyticsStream(t0.Add(2*time.Hour), nil, 0)
	_ = d.CloseAnalyticsStream(c, t0.Add(2*time.Hour))
	open, err := d.OpenAnalyticsStreams()
	if err != nil || len(open) != 2 || open[0].ID != b || open[1].ID != a || open[0].Wait != 7*time.Minute {
		t.Fatalf("open = %+v (%v), want b then a, b with its 7m wait", open, err)
	}
}

// TestAnalyticsCloseNeverEndsBeforeItStarted (R8).
func TestAnalyticsCloseNeverEndsBeforeItStarted(t *testing.T) {
	d := testDB(t)
	t0 := time.UnixMilli(1_800_000_000_000)
	id, _ := d.OpenAnalyticsStream(t0, nil, 0)
	if err := d.CloseAnalyticsStream(id, time.Time{}); err != nil {
		t.Fatal(err)
	}
	row, _ := d.GetAnalyticsStream(id)
	if row.Stream.EndedAt == nil || row.Stream.EndedAt.Before(t0) || row.Stream.DurationMs < 0 {
		t.Fatalf("stream = %+v, want it ended at its start, not before", row.Stream)
	}
}

func TestAnalyticsSamplesAreIndexedByTime(t *testing.T) {
	d := testDB(t)
	var n int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_analytics_samples_at'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("no index on analytics_samples(at) for the hourly prune (%d, %v)", n, err)
	}
}
