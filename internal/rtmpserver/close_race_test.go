package rtmpserver

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bluenviron/gortmplib/pkg/message"
)

// closers is how many goroutines pile onto one subscriber.close() at once, and
// closeRounds how many times the pile-up is repeated. Both are larger than the
// three real callers need because the bug they exist to catch is a lost race,
// not a deterministic fault: the window between `default:` and `close()` is a
// few instructions wide, so a single attempt would pass on a good day and prove
// nothing. Together these make a surviving defect vanishingly unlikely rather
// than merely unlucky.
const (
	closers     = 8
	closeRounds = 400
)

// Every teardown path may call subscriber.close(), and two of them may call it
// at the same instant -- watchPeer when the peer's socket dies, the writer's
// own defer when a write fails, and Server.Stop. close() must therefore be
// idempotent under genuine concurrency, not merely when the calls happen to be
// sequential.
//
// The predecessor was `select { case <-done: default: close(done) }`, which is
// check-then-act: both callers observe done still open, both fall to `default`,
// and the second close panics with "close of closed channel". No recover()
// exists anywhere in this package, so that panic ends the process -- and this
// process is the daemon holding every live broadcast on the install. A finished
// YouTube broadcast cannot be resumed, so the damage is permanent. See #496.
//
// A test that calls close() twice in sequence passes against the broken
// implementation and is worthless here. This one releases every caller from a
// barrier so they contend for real, and recovers in each so a panic is reported
// as a failed assertion instead of taking the test binary down with it.
func TestSubscriberCloseSurvivesConcurrentCallers(t *testing.T) {
	for round := 0; round < closeRounds; round++ {
		sub := &subscriber{ch: make(chan message.Message, 1), done: make(chan struct{})}

		// parked is released once every closer is blocked on the barrier, so
		// they are woken together rather than trickling in one at a time.
		var parked, finished sync.WaitGroup
		parked.Add(closers)
		finished.Add(closers)
		barrier := make(chan struct{})
		panics := make(chan any, closers)

		for i := 0; i < closers; i++ {
			go func() {
				defer finished.Done()
				defer func() {
					if r := recover(); r != nil {
						panics <- r
					}
				}()
				parked.Done()
				<-barrier
				sub.close()
			}()
		}

		parked.Wait()
		close(barrier)
		finished.Wait()
		close(panics)

		if r, ok := <-panics; ok {
			t.Fatalf("round %d: subscriber.close() panicked when %d teardown paths "+
				"raced for it: %v. Unrecovered, this kills the daemon and ends every "+
				"live broadcast on the install at once", round, closers, r)
		}
		select {
		case <-sub.done:
		default:
			t.Fatalf("round %d: subscriber.close() returned without waking the "+
				"subscriber; a parked serveSubscriber would never learn it was torn down", round)
		}
	}
}

// The same race, driven through the three callers that actually exist rather
// than through close() directly, so the test still bites if a future caller is
// added or one of these stops going through close(). watchPeer runs in a
// goroutine this test does not own: a panic there cannot be recovered and takes
// the whole binary with it, which is precisely the production consequence.
func TestTheThreeRealTeardownPathsCanRaceOneSubscriber(t *testing.T) {
	for round := 0; round < closeRounds/4; round++ {
		s := New(quiet(), "127.0.0.1:0", nil)
		sub, client := newSub(t, s, PublisherKey{SourceID: 1})
		go sub.watchPeer()

		var parked, finished sync.WaitGroup
		barrier := make(chan struct{})
		panics := make(chan any, 3)

		// The peer's socket dying (watchPeer), the writer's defer, and server
		// shutdown, all reaching teardown at the same instant.
		paths := []func(){
			func() { _ = client.Close() },
			func() { sub.close() },
			func() { s.Stop() },
		}
		parked.Add(len(paths))
		finished.Add(len(paths))
		for _, path := range paths {
			go func() {
				defer finished.Done()
				defer func() {
					if r := recover(); r != nil {
						panics <- r
					}
				}()
				parked.Done()
				<-barrier
				path()
			}()
		}

		parked.Wait()
		close(barrier)
		finished.Wait()
		close(panics)

		if r, ok := <-panics; ok {
			t.Fatalf("round %d: a subscriber torn down by its peer, its writer and "+
				"Stop at once panicked: %v", round, r)
		}
		// watchPeer must have noticed and returned; if it is still spinning the
		// next round would leak a goroutine onto the pile.
		if err := waitClosed(sub.done, 3*time.Second); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
	}
}

func waitClosed(done <-chan struct{}, within time.Duration) error {
	select {
	case <-done:
		return nil
	case <-time.After(within):
		return fmt.Errorf("the subscriber was never woken after every teardown path ran")
	}
}
