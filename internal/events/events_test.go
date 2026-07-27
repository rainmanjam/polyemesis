package events

import (
	"sync"
	"testing"
	"time"
)

// The broker sits between every producer in the pipeline and every browser
// watching. It is read from meter goroutines, destination supervisors and HTTP
// handlers at once, so the interesting tests here are the concurrent ones.

func drain(t *testing.T, s *Subscription, want int, within time.Duration) []Event {
	t.Helper()
	var got []Event
	deadline := time.After(within)
	for len(got) < want {
		select {
		case ev, ok := <-s.C:
			if !ok {
				return got
			}
			got = append(got, ev)
		case <-deadline:
			return got
		}
	}
	return got
}

func TestSubscribeWithNoTypesReceivesEverything(t *testing.T) {
	b := NewBroker()
	s := b.Subscribe()
	defer s.Close()

	b.Publish(TypeStatus, "a")
	b.Publish(TypeLevels, "b")

	got := drain(t, s, 2, time.Second)
	if len(got) != 2 {
		t.Fatalf("received %d events, want 2", len(got))
	}
	if got[0].Data != "a" || got[1].Data != "b" {
		t.Errorf("payloads = %v, %v; want a, b", got[0].Data, got[1].Data)
	}
	if got[0].Time.IsZero() {
		t.Error("event carries no timestamp")
	}
}

func TestSubscribeWithTypesFiltersTheRest(t *testing.T) {
	b := NewBroker()
	s := b.Subscribe(TypeLevels)
	defer s.Close()

	b.Publish(TypeStatus, "ignored")
	b.Publish(TypeLevels, "wanted")

	got := drain(t, s, 1, time.Second)
	if len(got) != 1 {
		t.Fatalf("received %d events, want exactly 1", len(got))
	}
	if got[0].Data != "wanted" {
		t.Errorf("received %v, want the filtered-in event", got[0].Data)
	}
}

func TestEverySubscriberGetsItsOwnCopy(t *testing.T) {
	b := NewBroker()
	a, c := b.Subscribe(), b.Subscribe()
	defer a.Close()
	defer c.Close()

	b.Publish(TypeStatus, "fanned out")

	for name, s := range map[string]*Subscription{"a": a, "c": c} {
		got := drain(t, s, 1, time.Second)
		if len(got) != 1 {
			t.Errorf("subscriber %s received %d events, want 1", name, len(got))
		}
	}
}

func TestASlowSubscriberIsDroppedRatherThanBlockingThePipeline(t *testing.T) {
	b := NewBroker()
	s := b.Subscribe()
	defer s.Close()

	// Never read. Publishing must not block: a browser that stops reading its
	// websocket must never be able to stall the metering goroutine that feeds
	// it, which would take the stream down to keep a UI happy.
	done := make(chan struct{})
	go func() {
		for i := 0; i < subBuffer*3; i++ {
			b.Publish(TypeLevels, i)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on a full subscriber channel")
	}

	_, dropped := b.Stats()
	if dropped == 0 {
		t.Error("nothing was reported dropped after overflowing a subscriber")
	}
}

func TestCloseUnsubscribesAndIsIdempotent(t *testing.T) {
	b := NewBroker()
	s := b.Subscribe()

	if n, _ := b.Stats(); n != 1 {
		t.Fatalf("subscribers = %d, want 1", n)
	}
	s.Close()
	if n, _ := b.Stats(); n != 0 {
		t.Errorf("subscribers = %d after Close, want 0", n)
	}
	// A second Close must not panic on an already-closed channel. Handlers use
	// defer alongside an explicit close on the error path, so this happens.
	s.Close()

	// Publishing to nobody is fine.
	b.Publish(TypeStatus, "into the void")
}

func TestPublishingToAClosedSubscriptionDoesNotPanic(t *testing.T) {
	b := NewBroker()
	s := b.Subscribe()
	s.Close()
	// Sending on a closed channel panics, so the broker must have dropped the
	// subscription rather than merely marked it. A panic here takes down
	// whichever pipeline goroutine happened to be publishing.
	b.Publish(TypeStatus, "after close")
}

// The concurrency tests. These are the reason this package needed tests: the
// broker is published to from many goroutines at once, and `go test -race`
// only finds what is actually exercised concurrently.

func TestConcurrentPublishersDoNotRaceOnTheDropCounter(t *testing.T) {
	b := NewBroker()
	// One subscriber that never reads, so every publish takes the drop path
	// and every publisher touches the counter.
	s := b.Subscribe()
	defer s.Close()
	for i := 0; i < subBuffer; i++ {
		b.Publish(TypeLevels, i) // fill the buffer
	}

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				b.Publish(TypeLevels, i)
			}
		}()
	}
	wg.Wait()

	// 8 goroutines x 200 publishes, all dropped. An unsynchronised counter
	// loses increments as well as racing, so the count is checked too.
	_, dropped := b.Stats()
	if dropped != 8*200 {
		t.Errorf("dropped = %d, want %d -- increments were lost", dropped, 8*200)
	}
}

func TestConcurrentSubscribeCloseAndPublish(t *testing.T) {
	b := NewBroker()
	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Publishers.
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					b.Publish(TypeStatus, "x")
				}
			}
		}()
	}
	// Subscribers churning, which is what a browser reloading repeatedly does.
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				s := b.Subscribe()
				go func() {
					for range s.C {
					}
				}()
				s.Close()
			}
		}()
	}
	time.Sleep(150 * time.Millisecond)
	close(stop)
	wg.Wait()

	if n, _ := b.Stats(); n != 0 {
		t.Errorf("subscribers = %d after every subscriber closed, want 0", n)
	}
}

func TestStatsIsSafeWhilePublishing(t *testing.T) {
	b := NewBroker()
	s := b.Subscribe()
	defer s.Close()

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				b.Publish(TypeLevels, 1)
			}
		}
	}()
	// The status endpoint calls this while the pipeline publishes.
	for i := 0; i < 500; i++ {
		b.Stats()
	}
	close(stop)
	wg.Wait()
}
