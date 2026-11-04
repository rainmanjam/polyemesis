package rtmpserver

import (
	"context"
	"errors"
	"net/url"
	"sync"
	"testing"
	"time"

	gortmplib "github.com/bluenviron/gortmplib"
)

// A superseded publisher's teardown used to end the subscribers of the stream
// its successor now feeds: the stream struct is per slot and is reused, so the
// engine's ingest child was cut off a live publish. releaseSlot ends them only
// for the session that still owns the slot.
func TestAStaleTeardownDoesNotEndTheSuccessorsSubscribers(t *testing.T) {
	s := New(quiet(), "127.0.0.1:0", nil)
	key := PublisherKey{SourceID: 2}
	stale := &session{key: key}
	successor := &session{key: key}
	sub := &subscriber{done: make(chan struct{})}
	st := &stream{subs: map[*subscriber]struct{}{sub: {}}, slots: map[string]int{}}

	s.mu.Lock()
	s.live[key] = successor
	s.streams[key] = st
	s.mu.Unlock()

	if got := s.releaseSlot(stale); got != nil {
		t.Fatalf("a superseded publisher's teardown returned the live stream to end: "+
			"its successor's %d subscriber(s) would be cut off", got.subscriberCount())
	}
	s.mu.Lock()
	still := s.live[key] == successor
	s.mu.Unlock()
	if !still {
		t.Fatal("the stale teardown removed the successor from the slot")
	}

	// The owner's own teardown still ends them (#674).
	if got := s.releaseSlot(successor); got != st {
		t.Fatalf("the owning publisher's teardown returned %p, want its stream %p", got, st)
	}
}

// mutableKeys is a lookup a test can change after a publisher has connected.
type mutableKeys struct {
	mu   sync.Mutex
	keys map[string]Target
}

func (m *mutableKeys) set(keys map[string]Target) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keys = keys
}

func (m *mutableKeys) lookup(key string) (Target, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.keys[key]
	return t, ok
}

// holdPublish connects a publisher and keeps it connected until the test ends.
func holdPublish(t *testing.T, addr, key string) {
	t.Helper()
	u, err := url.Parse("rtmp://" + addr + "/live/" + key)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	c := &gortmplib.Client{URL: u, Publish: true}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Initialize(ctx); err != nil {
		t.Fatalf("publish: %v", err)
	}
	t.Cleanup(c.Close)
}

func waitUntil(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

func startMutable(t *testing.T, m *mutableKeys) (*Server, string) {
	t.Helper()
	s := New(quiet(), "127.0.0.1:0", m.lookup)
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(s.Stop)
	s.mu.Lock()
	addr := s.ln.Addr().String()
	s.mu.Unlock()
	return s, addr
}

// A connected publisher used to keep feeding the programme after its key was
// rotated or its source disabled: admission was the only check.
func TestRevalidateClosesAPublisherWhoseKeyNoLongerAdmits(t *testing.T) {
	live := Target{SourceID: 9, Name: "Main", Enabled: true, Ready: true}
	for _, tc := range []struct {
		name  string
		after map[string]Target
	}{
		{"key rotated", map[string]Target{"new-key": live}},
		{"source disabled", map[string]Target{"old-key": {SourceID: 9, Name: "Main", Ready: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &mutableKeys{}
			m.set(map[string]Target{"old-key": live})
			s, addr := startMutable(t, m)
			holdPublish(t, addr, "old-key")
			if !waitUntil(3*time.Second, func() bool { return s.Publishing(9) }) {
				t.Fatal("the publisher never went live")
			}
			if n := s.Revalidate(); n != 0 {
				t.Fatalf("Revalidate closed %d publishers whose key still admits", n)
			}

			m.set(tc.after)
			if n := s.Revalidate(); n != 0 {
				t.Fatalf("the first refusing pass closed %d publishers; it must take two", n)
			}
			if n := s.Revalidate(); n != 1 {
				t.Fatalf("Revalidate closed %d publishers on the second refusing pass, want 1", n)
			}
			if !waitUntil(3*time.Second, func() bool { return !s.Publishing(9) }) {
				t.Error("the revoked publisher is still live")
			}
		})
	}
}

// Start runs Revalidate on a ticker.
func TestTheRTMPRevalidationTickerClosesARevokedPublisher(t *testing.T) {
	prev := revalidateInterval
	revalidateInterval = 50 * time.Millisecond
	t.Cleanup(func() { revalidateInterval = prev })

	m := &mutableKeys{}
	m.set(map[string]Target{"old-key": {SourceID: 4, Name: "Main", Enabled: true, Ready: true}})
	s, addr := startMutable(t, m)
	holdPublish(t, addr, "old-key")
	if !waitUntil(3*time.Second, func() bool { return s.Publishing(4) }) {
		t.Fatal("the publisher never went live")
	}
	m.set(map[string]Target{})
	if !waitUntil(3*time.Second, func() bool { return !s.Publishing(4) }) {
		t.Fatal("the ticker never closed the publisher of a rotated key")
	}
}

// One failed lookup (the sources table unreadable) between admitting passes
// must not close a healthy encoder, and the count starts over.
func TestATransientRTMPRefusalDoesNotCloseAPublisher(t *testing.T) {
	live := map[string]Target{"key": {SourceID: 8, Name: "Main", Enabled: true, Ready: true}}
	m := &mutableKeys{}
	m.set(live)
	s, addr := startMutable(t, m)
	holdPublish(t, addr, "key")
	if !waitUntil(3*time.Second, func() bool { return s.Publishing(8) }) {
		t.Fatal("the publisher never went live")
	}
	for i := 0; i < 3; i++ {
		m.set(map[string]Target{})
		if n := s.Revalidate(); n != 0 {
			t.Fatalf("pass %d: one failed lookup closed %d publishers", i, n)
		}
		m.set(live)
		if n := s.Revalidate(); n != 0 {
			t.Fatalf("pass %d: an admitting pass closed %d publishers", i, n)
		}
	}
	if !s.Publishing(8) {
		t.Error("alternating transient failures took a healthy encoder off the air")
	}
}

// A store that cannot be read is not a revocation, however many passes fail.
func TestRTMPLookupErrorsNeverCloseAPublisher(t *testing.T) {
	m := &mutableKeys{}
	m.set(map[string]Target{"key": {SourceID: 13, Name: "Main", Enabled: true, Ready: true}})
	s, addr := startMutable(t, m)
	holdPublish(t, addr, "key")
	if !waitUntil(3*time.Second, func() bool { return s.Publishing(13) }) {
		t.Fatal("the publisher never went live")
	}
	s.SetRevalidateLookup(func(string) (Target, bool, error) {
		return Target{}, false, errors.New("database is locked")
	})
	for i := 0; i < 5; i++ {
		if n := s.Revalidate(); n != 0 {
			t.Fatalf("pass %d: an unreadable store closed %d publishers", i, n)
		}
	}
	if !s.Publishing(13) {
		t.Error("lookup errors took a healthy encoder off the air")
	}
}
