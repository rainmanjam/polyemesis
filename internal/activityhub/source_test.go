package activityhub

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/activity"
	"github.com/rainmanjam/polyemesis/internal/chat"
)

// fakeSource is a scripted Source: each Run takes the next script step.
type fakeSource struct {
	platform, account string
	runs              atomic.Int32
	script            func(ctx context.Context, emit func(activity.Event), n int) error
	health            *chat.Health
	kinds             []activity.KindStatus
}

func (f *fakeSource) Platform() string { return f.platform }
func (f *fakeSource) Account() string  { return f.account }
func (f *fakeSource) Run(ctx context.Context, emit func(activity.Event)) error {
	n := int(f.runs.Add(1))
	return f.script(ctx, emit, n)
}

type healthySource struct{ *fakeSource }

func (h healthySource) Health() chat.Health { return *h.health }

type kindSource struct{ *fakeSource }

func (k kindSource) Kinds() []activity.KindStatus { return k.kinds }

func untilCtx(ctx context.Context, _ func(activity.Event), _ int) error {
	<-ctx.Done()
	return ctx.Err()
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestASourceEmitsThroughTheHubAndIsStampedWithItsAccount(t *testing.T) {
	r := newRig(t)
	src := &fakeSource{platform: "twitch", account: "42", script: func(ctx context.Context, emit func(activity.Event), n int) error {
		e := real(activity.KindFollow, "", "from-source")
		e.Platform = ""
		emit(e)
		<-ctx.Done()
		return nil
	}}
	if err := r.hub.Attach(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the emitted event", func() bool { return len(r.bus.events()) == 1 })
	got := r.bus.events()[0]
	if got.Platform != "twitch" || got.Account != "42" {
		t.Errorf("not stamped: %q %q", got.Platform, got.Account)
	}
	eventually(t, "live", func() bool { s := r.hub.Statuses(); return len(s) == 1 && s[0].State == chat.StateLive })
	s := r.hub.Statuses()[0]
	if s.Transport != TransportOwn || s.Events != 1 || s.Kinds == nil {
		t.Errorf("status: %+v", s)
	}
}

func TestASecondSourceForTheSameAccountIsRefused(t *testing.T) {
	r := newRig(t)
	a := &fakeSource{platform: "twitch", account: "1", script: untilCtx}
	if err := r.hub.Attach(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if err := r.hub.Attach(context.Background(), &fakeSource{platform: "twitch", account: "1", script: untilCtx}); !errors.Is(err, ErrDuplicateSource) {
		t.Errorf("duplicate attach: %v", err)
	}
	if err := r.hub.Attach(context.Background(), &fakeSource{platform: "twitch", account: "2", script: untilCtx}); err != nil {
		t.Errorf("another account: %v", err)
	}
}

func TestDetachStopsTheSourceAndFreesTheAccount(t *testing.T) {
	r := newRig(t)
	stopped := make(chan struct{})
	a := &fakeSource{platform: "kick", account: "1", script: func(ctx context.Context, _ func(activity.Event), _ int) error {
		<-ctx.Done()
		close(stopped)
		return nil
	}}
	if err := r.hub.Attach(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	eventually(t, "running", func() bool { return a.runs.Load() == 1 })
	r.hub.Detach("kick", "1")
	select {
	case <-stopped:
	default:
		t.Error("Detach returned before the source stopped")
	}
	if len(r.hub.Statuses()) != 0 {
		t.Error("a detached source is still listed")
	}
	if err := r.hub.Attach(context.Background(), &fakeSource{platform: "kick", account: "1", script: untilCtx}); err != nil {
		t.Errorf("re-attach after detach: %v", err)
	}
	r.hub.Detach("nobody", "home") // not an error
}

func TestAFailingSourceIsRestartedWithBackoffAndReportsFailed(t *testing.T) {
	var waits []int
	r := newRig(t, WithBackoff(func(attempt int) time.Duration { waits = append(waits, attempt); return time.Millisecond }))
	a := &fakeSource{platform: "twitch", account: "1", script: func(ctx context.Context, _ func(activity.Event), n int) error {
		if n < 3 {
			return errors.New("socket closed https://example.test/?token=abc123SECRETVALUE")
		}
		<-ctx.Done()
		return nil
	}}
	if err := r.hub.Attach(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	eventually(t, "third run", func() bool { return a.runs.Load() >= 3 })
	if len(waits) < 2 || waits[0] != 1 || waits[1] != 2 {
		t.Errorf("backoff attempts = %v, want 1 then 2", waits)
	}
	eventually(t, "live again", func() bool { s := r.hub.Statuses(); return len(s) == 1 && s[0].State == chat.StateLive })
}

func TestAPanickingSourceRestartsAndNothingElseNotices(t *testing.T) {
	r := newRig(t)
	a := &fakeSource{platform: "twitch", account: "1", script: func(ctx context.Context, _ func(activity.Event), n int) error {
		if n == 1 {
			panic("boom")
		}
		<-ctx.Done()
		return nil
	}}
	if err := r.hub.Attach(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	eventually(t, "restart after panic", func() bool { return a.runs.Load() >= 2 })
}

func TestFailedStatusNamesTheReasonWithoutASecret(t *testing.T) {
	block := make(chan struct{})
	r := newRig(t, WithBackoff(func(int) time.Duration { return time.Hour }))
	defer close(block)
	a := &fakeSource{platform: "twitch", account: "1", script: func(ctx context.Context, _ func(activity.Event), n int) error {
		return errors.New("dial wss://host/?access_token=ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 refused")
	}}
	_ = r.hub.Attach(context.Background(), a)
	eventually(t, "failed", func() bool { s := r.hub.Statuses(); return len(s) == 1 && s[0].State == chat.StateFailed })
	d := r.hub.Statuses()[0].Detail
	if d == "" || strings.Contains(d, "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") {
		t.Errorf("detail leaks or is empty: %q", d)
	}
}

func TestHealtherAndKindReporterOverrideTheDefaults(t *testing.T) {
	r := newRig(t)
	h := &chat.Health{State: chat.StateDegraded, Detail: "9 of 14 kinds on; subs need Affiliate"}
	base := &fakeSource{platform: "twitch", account: "1", script: untilCtx, health: h,
		kinds: []activity.KindStatus{{Kind: activity.KindSubscribe, State: activity.KindUnavailable, Detail: "needs Affiliate"}}}
	src := struct {
		Source
		Healther
		KindReporter
	}{base, healthySource{base}, kindSource{base}}
	if err := r.hub.Attach(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	eventually(t, "status", func() bool { return len(r.hub.Statuses()) == 1 })
	s := r.hub.Statuses()[0]
	if s.State != chat.StateDegraded || s.Detail == "" || len(s.Kinds) != 1 || s.Kinds[0].State != activity.KindUnavailable {
		t.Errorf("status: %+v", s)
	}
}

func TestStatusProvidersAreListedAndSorted(t *testing.T) {
	r := newRig(t)
	r.hub.AddStatusProvider(func() []SourceStatus {
		return []SourceStatus{
			{Platform: "youtube", Account: "b", State: chat.StateLive, Transport: TransportChat},
			{Platform: "kick", Account: "a", State: chat.StateLive, Transport: TransportChat, Kinds: []activity.KindStatus{{Kind: activity.KindTip, State: activity.KindOn}}},
		}
	})
	r.hub.AddStatusProvider(nil) // ignored
	_ = r.hub.Attach(context.Background(), &fakeSource{platform: "twitch", account: "1", script: untilCtx})
	eventually(t, "three statuses", func() bool { return len(r.hub.Statuses()) == 3 })
	got := r.hub.Statuses()
	if got[0].Platform != "kick" || got[1].Platform != "twitch" || got[2].Platform != "youtube" {
		t.Errorf("order: %v %v %v", got[0].Platform, got[1].Platform, got[2].Platform)
	}
	if got[2].Kinds == nil {
		t.Error("kinds should be [] not null on the wire")
	}
}

func TestCloseStopsEverySourceAndRefusesMore(t *testing.T) {
	r := newRig(t)
	var stopped, started atomic.Int32
	for _, acct := range []string{"1", "2"} {
		a := &fakeSource{platform: "twitch", account: acct, script: func(ctx context.Context, _ func(activity.Event), _ int) error {
			started.Add(1)
			<-ctx.Done()
			stopped.Add(1)
			return nil
		}}
		if err := r.hub.Attach(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "both running", func() bool { return started.Load() == 2 })
	r.hub.Close()
	if stopped.Load() != 2 {
		t.Errorf("stopped %d of 2", stopped.Load())
	}
	if err := r.hub.Attach(context.Background(), &fakeSource{platform: "kick", account: "1", script: untilCtx}); err == nil {
		t.Error("attach after Close succeeded")
	}
}

func TestParentContextCancelStopsTheSource(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	a := &fakeSource{platform: "twitch", account: "1", script: untilCtx}
	_ = r.hub.Attach(ctx, a)
	eventually(t, "running", func() bool { return a.runs.Load() == 1 })
	cancel()
	eventually(t, "stopped", func() bool {
		s := r.hub.Statuses()
		return len(s) == 1 && s[0].State == chat.StateStopped
	})
}

func TestDefaultBackoffStaysUnderItsCeilingAndGrows(t *testing.T) {
	prev := time.Duration(0)
	for attempt := 1; attempt <= 12; attempt++ {
		d := defaultBackoff(attempt)
		if d <= 0 || d > DefaultMaxBackoff {
			t.Fatalf("attempt %d: %v", attempt, d)
		}
		prev = d
	}
	if prev < DefaultMaxBackoff*4/5 {
		t.Errorf("never reached the ceiling: %v", prev)
	}
}
