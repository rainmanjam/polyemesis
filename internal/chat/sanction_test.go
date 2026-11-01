package chat

import (
	"context"
	"errors"
	"sync"
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
	err error
}

func (f *failingBanner) Ban(ctx context.Context, userID string, d time.Duration, reason string) error {
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
