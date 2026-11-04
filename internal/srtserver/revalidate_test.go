package srtserver

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// mutableTargets is a lookup whose answers a test can change after connect:
// the token rotated, the source disabled, the engine stopped.
type mutableTargets struct {
	mu      sync.Mutex
	targets []Target
}

func (m *mutableTargets) set(ts ...Target) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.targets = ts
}

func (m *mutableTargets) lookup() Lookup {
	return ConstantTimeLookup(
		func() []Target {
			m.mu.Lock()
			defer m.mu.Unlock()
			return append([]Target(nil), m.targets...)
		},
		func(tg Target) []string { return []string{tokenFor(tg)} },
	)
}

func serveMutable(t *testing.T, m *mutableTargets) (*Server, string) {
	t.Helper()
	res := testenv.ReserveUDP(t)
	addr := fmt.Sprintf("127.0.0.1:%d", res.Port())
	s := New(quietLog(), addr, m.lookup())
	res.Release()
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(s.Stop)
	return s, addr
}

// A connected publisher used to keep feeding the programme after its token was
// rotated, its source disabled, or its engine stopped: admission was the only
// check. Revalidate decides again and closes it.
func TestRevalidateClosesAPublisherThatWouldNoLongerBeAdmitted(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(tg Target) []Target
	}{
		{"token rotated", func(tg Target) []Target { tg.Name = "rotated"; return []Target{tg} }},
		{"source disabled", func(tg Target) []Target { tg.Enabled = false; return []Target{tg} }},
		{"engine stopped", func(tg Target) []Target { tg.Sink = nil; return []Target{tg} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recorder{}
			tg := Target{SourceID: 3, Name: "Main", Enabled: true, Sink: sink}
			m := &mutableTargets{}
			m.set(tg)
			srv, addr := serveMutable(t, m)

			c, err := dial(t, addr, tokenFor(tg))
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer c.Close()
			if _, err := c.Write([]byte("live")); err != nil {
				t.Fatalf("write: %v", err)
			}
			if !waitFor(3*time.Second, func() bool { return srv.Publishing(3) && len(sink.bytes()) > 0 }) {
				t.Fatal("the publisher never went live")
			}

			// Unchanged: nothing to close.
			if n := srv.Revalidate(); n != 0 {
				t.Fatalf("Revalidate closed %d sessions whose admission still holds", n)
			}

			m.set(tc.change(tg)...)
			if n := srv.Revalidate(); n != 0 {
				t.Fatalf("the first refusing pass closed %d sessions; one pass can be a "+
					"transient lookup failure, so it must take two", n)
			}
			if n := srv.Revalidate(); n != 1 {
				t.Fatalf("Revalidate closed %d sessions on the second refusing pass, want 1", n)
			}
			if srv.Publishing(3) {
				t.Error("the source still reports a publisher after it was revalidated away")
			}
		})
	}
}

// Start runs Revalidate on a ticker, so a revoked publisher is closed even if
// nothing else calls it.
func TestTheRevalidationTickerClosesARevokedPublisher(t *testing.T) {
	prev := revalidateInterval
	revalidateInterval = 50 * time.Millisecond
	t.Cleanup(func() { revalidateInterval = prev })

	sink := &recorder{}
	tg := Target{SourceID: 5, Name: "Main", Enabled: true, Sink: sink}
	m := &mutableTargets{}
	m.set(tg)
	srv, addr := serveMutable(t, m)

	c, err := dial(t, addr, tokenFor(tg))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("live")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !waitFor(3*time.Second, func() bool { return srv.Publishing(5) }) {
		t.Fatal("the publisher never went live")
	}
	disabled := tg
	disabled.Enabled = false
	m.set(disabled)
	if !waitFor(3*time.Second, func() bool { return !srv.Publishing(5) }) {
		t.Fatal("the ticker never closed the publisher of a disabled source")
	}
}

// The engine's lookup says "not found" when it cannot read the sources table.
// One such pass, followed by a pass that admits again, must not close a
// healthy encoder -- and the count starts over.
func TestATransientRefusalDoesNotCloseAPublisher(t *testing.T) {
	sink := &recorder{}
	tg := Target{SourceID: 7, Name: "Main", Enabled: true, Sink: sink}
	m := &mutableTargets{}
	m.set(tg)
	srv, addr := serveMutable(t, m)

	c, err := dial(t, addr, tokenFor(tg))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("live")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !waitFor(3*time.Second, func() bool { return srv.Publishing(7) }) {
		t.Fatal("the publisher never went live")
	}
	for i := 0; i < 3; i++ {
		m.set() // the sources table could not be read
		if n := srv.Revalidate(); n != 0 {
			t.Fatalf("pass %d: one failed lookup closed %d sessions", i, n)
		}
		m.set(tg) // and could again
		if n := srv.Revalidate(); n != 0 {
			t.Fatalf("pass %d: an admitting pass closed %d sessions", i, n)
		}
	}
	if !srv.Publishing(7) {
		t.Error("alternating transient failures took a healthy encoder off the air")
	}
}

// A store that cannot be read is not a revocation. However many passes in a
// row fail to answer, a healthy publisher stays (review follow-up: two locked
// ticks used to drop every encoder).
func TestLookupErrorsNeverCloseAPublisher(t *testing.T) {
	sink := &recorder{}
	tg := Target{SourceID: 11, Name: "Main", Enabled: true, Sink: sink}
	m := &mutableTargets{}
	m.set(tg)
	srv, addr := serveMutable(t, m)
	c, err := dial(t, addr, tokenFor(tg))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("live")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !waitFor(3*time.Second, func() bool { return srv.Publishing(11) }) {
		t.Fatal("the publisher never went live")
	}
	srv.SetRevalidateLookup(func(string) (Target, bool, error) {
		return Target{}, false, errors.New("database is locked")
	})
	for i := 0; i < 5; i++ {
		if n := srv.Revalidate(); n != 0 {
			t.Fatalf("pass %d: an unreadable store closed %d publishers", i, n)
		}
	}
	if !srv.Publishing(11) {
		t.Error("lookup errors took a healthy encoder off the air")
	}
}

// An engine replaced between two ticks gives the token a new sink; the
// session still delivers into the old one, which nothing reads. It is closed
// so the encoder reconnects to the new engine.
func TestAReplacedEngineClosesThePublisherOfTheOldOne(t *testing.T) {
	old := &recorder{}
	tg := Target{SourceID: 12, Name: "Main", Enabled: true, Sink: old}
	m := &mutableTargets{}
	m.set(tg)
	srv, addr := serveMutable(t, m)
	c, err := dial(t, addr, tokenFor(tg))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("live")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !waitFor(3*time.Second, func() bool { return srv.Publishing(12) }) {
		t.Fatal("the publisher never went live")
	}
	replaced := tg
	replaced.Sink = &recorder{}
	m.set(replaced)
	srv.Revalidate()
	if n := srv.Revalidate(); n != 1 {
		t.Fatalf("Revalidate closed %d sessions after the engine was replaced, want 1", n)
	}
}
