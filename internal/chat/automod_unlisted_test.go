package chat

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/automod"
	"github.com/rainmanjam/polyemesis/internal/db"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// Under a raid brake every signal line is declined. Once the review list holds
// its cap of unchecked entries, further lines must not each log a Warn or
// rescan the list: one Warn, the rest counted, and the lines still held.
func TestUnlistedUncheckedLinesWarnOnceAndAreCounted(t *testing.T) {
	var buf lockedBuf
	mod := hardEngineModerator(t, automod.GateConfig{RaidMax: 1}, true, nil)
	h := testHub(t, WithLogger(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))))
	sink := &displayRecorder{}
	h.SetDisplay(sink)
	h.SetModerator(mod)
	var msgs []Message
	total := 1 + unjudgedHoldCap + 200 // one admitted, then declined by the brake
	for i := 0; i < total; i++ {
		msgs = append(msgs, msg(fmt.Sprintf("m%d", i), fmt.Sprintf("u%d", i), abusiveLine))
	}
	attach(t, h, &fakeAdapter{platform: db.PlatformTwitch, account: "acct", messages: msgs})

	last := fmt.Sprintf("m%d", total-1)
	sink.waitFor(t, fmt.Sprintf("offer %s %d", last, DisplayHeld))
	waitFor(t, "the listed cap", func() bool { return len(h.Flagged(db.PlatformTwitch, 0)) == unjudgedHoldCap })
	waitFor(t, "the rest counted", func() bool { return h.ModelStats().UncheckedUnlisted == 200 })
	if got := strings.Count(buf.String(), "too many unchecked lines"); got != 1 {
		t.Fatalf("%d warnings for 200 unlisted lines, want 1:\n%s", got, buf.String())
	}
	for _, c := range sink.got() {
		if c == fmt.Sprintf("offer %s %d", last, DisplayClear) {
			t.Fatalf("an unlisted line was shown as clear")
		}
	}
}
