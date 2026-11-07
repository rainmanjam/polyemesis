package db

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestChatRangeReadsAreHalfOpenSkipEchoAndHonourThePlatformAndIDBound(t *testing.T) {
	d := testDB(t)
	base := time.UnixMilli(1_700_000_000_000)
	var msgs []ChatMessage
	for i := 0; i < 5; i++ {
		msgs = append(msgs, ChatMessage{Platform: PlatformTwitch, MessageID: fmt.Sprint("t", i),
			AuthorName: "a", Text: "x", At: base.Add(time.Duration(i) * time.Second)})
	}
	msgs = append(msgs,
		ChatMessage{Platform: PlatformKick, MessageID: "k", AuthorName: "k", Text: "x", At: base.Add(time.Second)},
		ChatMessage{Platform: PlatformTwitch, MessageID: "echo", Echo: true, Text: "x", At: base.Add(time.Second)})
	if _, err := d.AppendChatMessages(msgs); err != nil {
		t.Fatal(err)
	}
	q := ChatRange{FromMS: base.UnixMilli(), ToMS: base.Add(3 * time.Second).UnixMilli()}

	sum, err := d.ChatRangeSummary(q)
	if err != nil {
		t.Fatal(err)
	}
	// [0s, 3s): t0 t1 t2 and k, but not t3 (end is exclusive) and not the echo.
	if sum.Count != 4 || len(sum.Platforms) != 2 || sum.Platforms[0] != PlatformKick || sum.MaxID == 0 {
		t.Errorf("summary = %+v", sum)
	}
	q.Platform = PlatformTwitch
	if sum, _ = d.ChatRangeSummary(q); sum.Count != 3 {
		t.Errorf("twitch summary = %+v", sum)
	}

	var got []string
	q.MaxID = sum.MaxID
	if err := d.ForEachChatMessage(q, func(m ChatMessage) error { got = append(got, m.MessageID); return nil }); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != "[t0 t1 t2]" {
		t.Errorf("got %v", got)
	}

	// The id bound hides what was stored after the summary.
	if _, err := d.AppendChatMessages([]ChatMessage{{Platform: PlatformTwitch, MessageID: "late", Text: "x", At: base.Add(time.Second)}}); err != nil {
		t.Fatal(err)
	}
	got = nil
	_ = d.ForEachChatMessage(q, func(m ChatMessage) error { got = append(got, m.MessageID); return nil })
	if len(got) != 3 {
		t.Errorf("late arrival leaked past MaxID: %v", got)
	}
	q.MaxID = 0
	got = nil
	_ = d.ForEachChatMessage(q, func(m ChatMessage) error { got = append(got, m.MessageID); return nil })
	if len(got) != 4 {
		t.Errorf("unbounded read = %v", got)
	}

	stop := errors.New("stop")
	if err := d.ForEachChatMessage(q, func(ChatMessage) error { return stop }); !errors.Is(err, stop) {
		t.Errorf("callback error not returned: %v", err)
	}
}

func TestChatOldestReportsTheEmptyTableAndTheFirstRow(t *testing.T) {
	d := testDB(t)
	if _, ok, err := d.ChatOldest(); err != nil || ok {
		t.Fatalf("empty table: ok=%v err=%v", ok, err)
	}
	at := time.UnixMilli(1_700_000_123_456)
	if _, err := d.AppendChatMessages([]ChatMessage{
		{Platform: PlatformTwitch, MessageID: "b", Text: "x", At: at.Add(time.Hour)},
		{Platform: PlatformTwitch, MessageID: "a", Text: "x", At: at}}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := d.ChatOldest()
	if err != nil || !ok || !got.Equal(at) {
		t.Errorf("oldest = %v %v %v", got, ok, err)
	}
}
