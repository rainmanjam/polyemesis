package chat

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// UX Council A48: the user card could not tell whether a viewer was banned, so
// Lift ban was offered for people who were not. The Hub is the one place every
// ban passes through -- the operator's and automod's alike -- so it is where a
// successful one is recorded.

type sanctionStore struct {
	recordingStore
	mu       sync.Mutex
	recorded []db.ChatSanction
	cleared  []string
}

func (s *sanctionStore) RecordChatSanction(c db.ChatSanction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recorded = append(s.recorded, c)
	return nil
}

func (s *sanctionStore) ClearChatSanction(p db.Platform, account, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleared = append(s.cleared, string(p)+"/"+account+"/"+userID)
	return nil
}

func (s *sanctionStore) snapshot() ([]db.ChatSanction, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]db.ChatSanction(nil), s.recorded...), append([]string(nil), s.cleared...)
}

func sanctionHub(t *testing.T, store *sanctionStore, now time.Time, banErr error) *Hub {
	t.Helper()
	h := testHub(t, WithStore(store), WithClock(func() time.Time { return now }))
	a := &failingBanner{banningAdapter: &banningAdapter{fakeAdapter: &fakeAdapter{
		platform: db.PlatformTwitch, account: "acct",
	}}, err: banErr}
	if err := h.Attach(context.Background(), a); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	return h
}

type failingBanner struct {
	*banningAdapter
	err   error
	tries atomic.Int32
}

func (f *failingBanner) attempted() int { return int(f.tries.Load()) }

func (f *failingBanner) Ban(ctx context.Context, userID string, d time.Duration, reason string) error {
	f.tries.Add(1)
	if f.err != nil {
		return f.err
	}
	return f.banningAdapter.Ban(ctx, userID, d, reason)
}

func TestATimeoutIsRecordedWithItsExpiry(t *testing.T) {
	store := &sanctionStore{}
	now := time.UnixMilli(1_800_000_000_000)
	h := sanctionHub(t, store, now, nil)

	if err := h.Ban(context.Background(), db.PlatformTwitch, "acct", "u1", 10*time.Minute, "spam"); err != nil {
		t.Fatalf("Ban: %v", err)
	}
	got, _ := store.snapshot()
	if len(got) != 1 {
		t.Fatalf("recorded %d sanctions, want 1", len(got))
	}
	s := got[0]
	if s.Action != ModTimeout || !s.Until.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("recorded %+v, want a timeout until %v", s, now.Add(10*time.Minute))
	}
	if s.Platform != db.PlatformTwitch || s.Account != "acct" || s.UserID != "u1" ||
		s.By != ActorOperator || s.Reason != "spam" || !s.At.Equal(now) {
		t.Fatalf("recorded %+v", s)
	}
}

func TestAPermanentBanIsRecordedWithNoExpiryAndItsActor(t *testing.T) {
	store := &sanctionStore{}
	h := sanctionHub(t, store, time.UnixMilli(1_800_000_000_000), nil)

	if err := h.Ban(WithActor(context.Background(), ActorAutomod), db.PlatformTwitch, "acct", "u1", 0, ""); err != nil {
		t.Fatalf("Ban: %v", err)
	}
	got, _ := store.snapshot()
	if len(got) != 1 || got[0].Action != ModBan || !got[0].Until.IsZero() || got[0].By != ActorAutomod {
		t.Fatalf("recorded %+v, want one permanent ban by automod", got)
	}
}

// A refused ban is not a sanction. Recording it would disable nothing and show
// a ban the platform never applied.
func TestARefusedBanIsNotRecorded(t *testing.T) {
	store := &sanctionStore{}
	h := sanctionHub(t, store, time.Now(), errors.New("platform said no"))

	if err := h.Ban(context.Background(), db.PlatformTwitch, "acct", "u1", 0, ""); err == nil {
		t.Fatal("a refused ban reported success")
	}
	if got, _ := store.snapshot(); len(got) != 0 {
		t.Fatalf("a refused ban was recorded: %+v", got)
	}
}

func TestLiftingABanClearsTheRecord(t *testing.T) {
	store := &sanctionStore{}
	h := sanctionHub(t, store, time.Now(), nil)

	if err := h.Unban(context.Background(), db.PlatformTwitch, "acct", "u1"); err != nil {
		t.Fatalf("Unban: %v", err)
	}
	if _, cleared := store.snapshot(); len(cleared) != 1 || cleared[0] != "twitch/acct/u1" {
		t.Fatalf("cleared %v, want twitch/acct/u1", cleared)
	}
}

// review #5. Kick counts timeouts in whole minutes and rounds UP, so a 30-second
// timeout runs a full minute there. The record has to say what the platform is
// enforcing, or the card reads "none" while the viewer still cannot talk.
type roundingBanner struct{ *banningAdapter }

func (roundingBanner) EffectiveTimeout(d time.Duration) time.Duration {
	return (d + time.Minute - 1) / time.Minute * time.Minute
}

func TestATimeoutIsRecordedAsThePlatformEnforcesIt(t *testing.T) {
	store := &sanctionStore{}
	now := time.UnixMilli(1_800_000_000_000)
	h := testHub(t, WithStore(store), WithClock(func() time.Time { return now }))
	a := roundingBanner{&banningAdapter{fakeAdapter: &fakeAdapter{platform: db.PlatformKick, account: "acct"}}}
	if err := h.Attach(context.Background(), a); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := h.Ban(context.Background(), db.PlatformKick, "acct", "u1", 30*time.Second, ""); err != nil {
		t.Fatalf("Ban: %v", err)
	}
	got, _ := store.snapshot()
	if len(got) != 1 || !got[0].Until.Equal(now.Add(time.Minute)) {
		t.Fatalf("recorded %+v, want the timeout to end a full minute later, as Kick enforces it", got)
	}
}

// review #6. A ban and a lift on the same viewer, racing, must leave the record
// matching the order the PLATFORM saw them in. Without serialisation the lift
// can finish -- platform call and clear -- while the ban is still in flight,
// and the ban's record then lands last: the card says banned, the platform
// says not.
type gatedBanner struct {
	*banningAdapter
	entered chan struct{}
	release chan struct{}
}

func (g *gatedBanner) Ban(ctx context.Context, userID string, d time.Duration, reason string) error {
	close(g.entered)
	<-g.release
	return g.banningAdapter.Ban(ctx, userID, d, reason)
}

func TestABanAndALiftOnOneViewerAreSerialised(t *testing.T) {
	store := &sanctionStore{}
	h := testHub(t, WithStore(store))
	a := &gatedBanner{
		banningAdapter: &banningAdapter{fakeAdapter: &fakeAdapter{platform: db.PlatformTwitch, account: "acct"}},
		entered:        make(chan struct{}), release: make(chan struct{}),
	}
	if err := h.Attach(context.Background(), a); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	banned := make(chan error, 1)
	go func() { banned <- h.Ban(context.Background(), db.PlatformTwitch, "acct", "u1", 0, "") }()
	<-a.entered // the ban is at the platform

	lifted := make(chan error, 1)
	go func() { lifted <- h.Unban(context.Background(), db.PlatformTwitch, "acct", "u1") }()
	select {
	case <-lifted:
		t.Fatal("the lift completed while a ban on the same viewer was still in flight")
	case <-time.After(50 * time.Millisecond):
	}
	close(a.release)
	if err := <-banned; err != nil {
		t.Fatalf("Ban: %v", err)
	}
	if err := <-lifted; err != nil {
		t.Fatalf("Unban: %v", err)
	}
	rec, cleared := store.snapshot()
	if len(rec) != 1 || len(cleared) != 1 {
		t.Fatalf("recorded %d, cleared %d; want one of each", len(rec), len(cleared))
	}
}

// Different viewers are not serialised against each other.
func TestBansOnDifferentViewersDoNotWait(t *testing.T) {
	store := &sanctionStore{}
	h := testHub(t, WithStore(store))
	a := &gatedBanner{
		banningAdapter: &banningAdapter{fakeAdapter: &fakeAdapter{platform: db.PlatformTwitch, account: "acct"}},
		entered:        make(chan struct{}), release: make(chan struct{}),
	}
	if err := h.Attach(context.Background(), a); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	go func() { _ = h.Ban(context.Background(), db.PlatformTwitch, "acct", "u1", 0, "") }()
	<-a.entered
	defer close(a.release)
	done := make(chan error, 1)
	go func() { done <- h.Unban(context.Background(), db.PlatformTwitch, "acct", "someone-else") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Unban: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a lift on one viewer waited for a ban on another")
	}
}

func TestKickReportsTheTimeoutItEnforces(t *testing.T) {
	k := &KickAdapter{}
	for _, c := range []struct{ in, want time.Duration }{
		{30 * time.Second, time.Minute},
		{time.Minute, time.Minute},
		{61 * time.Second, 2 * time.Minute},
		{0, 0},
	} {
		if got := k.EffectiveTimeout(c.in); got != c.want {
			t.Errorf("EffectiveTimeout(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
