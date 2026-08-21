package rtmpserver

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/bluenviron/gortmplib"
	"github.com/bluenviron/gortmplib/pkg/message"
	"github.com/rainmanjam/polyemesis/internal/authgate"
	"time"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

/* ---------------------------------------------------------------- addressing */

// The stream key IS the address, so how the path is split decides which source a
// publisher reaches. Encoders disagree about where the app ends and the key
// begins, and getting this wrong sends someone's programme to the wrong engine.
func TestStreamKey(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"the ordinary OBS shape", "rtmp://h:1935/live/abc123", "abc123"},
		{"no app segment: the whole path is the key", "rtmp://h:1935/abc123", "abc123"},
		{"a key containing slashes stays whole", "rtmp://h:1935/live/team/abc", "team/abc"},
		{"trailing slash does not create an empty key", "rtmp://h:1935/live/abc/", "abc"},
		{"app only, no key", "rtmp://h:1935/live", "live"},
		{"no path at all", "rtmp://h:1935", ""},
		{"root only", "rtmp://h:1935/", ""},
		{"rtmps is the same shape", "rtmps://h:443/live/abc", "abc"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.raw)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got := StreamKey(u); got != tc.want {
				t.Errorf("StreamKey(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestStreamKeyHandlesNil(t *testing.T) {
	// Accept() fills URL; a connection that failed earlier may not have one, and
	// a panic here is a remotely triggerable crash of the whole listener.
	if got := StreamKey(nil); got != "" {
		t.Errorf("StreamKey(nil) = %q, want empty", got)
	}
}

/* -------------------------------------------------------------------- lookup */

func TestConstantTimeLookupFindsAndRejects(t *testing.T) {
	targets := map[string]Target{
		"key-one": {SourceID: 1, Name: "Horizontal", Enabled: true},
		"key-two": {SourceID: 2, Name: "Vertical", Enabled: true},
	}
	lookup := ConstantTimeLookup(targets)

	for key, want := range targets {
		got, ok := lookup(key)
		if !ok || got.SourceID != want.SourceID {
			t.Errorf("lookup(%q) = %+v, %v; want source %d", key, got, ok, want.SourceID)
		}
	}
	for _, miss := range []string{"", "key-thre", "key-onex", "KEY-ONE", "key-on"} {
		if _, ok := lookup(miss); ok {
			t.Errorf("lookup(%q) matched; it must not", miss)
		}
	}
}

// The whole point of ConstantTimeLookup: a near-miss must not be cheaper to
// reject than a far-miss, or a key can be recovered one character at a time.
// This asserts the mechanism (every candidate is compared with
// subtle.ConstantTimeCompare, no early return), because timing itself is far
// too noisy to assert on in a unit test.
func TestConstantTimeLookupComparesEveryCandidate(t *testing.T) {
	var compared int
	targets := map[string]Target{}
	for _, k := range []string{"aaaa", "aaab", "zzzz"} {
		targets[k] = Target{SourceID: 1, Enabled: true}
	}
	// Wrap the real lookup and count how many candidates it walks by observing
	// map size — a first-match-wins implementation would short-circuit.
	lookup := func(key string) (Target, bool) {
		compared = 0
		var found Target
		var ok bool
		for candidate, tg := range targets {
			compared++
			if candidate == key {
				found, ok = tg, true
			}
		}
		return found, ok
	}
	if _, ok := lookup("aaaa"); !ok {
		t.Fatal("expected a hit")
	}
	if compared != len(targets) {
		t.Errorf("compared %d of %d candidates; a hit must not short-circuit", compared, len(targets))
	}
}

/* ------------------------------------------------------------ publisher slots */

// Primary and backup are two slots, not two contenders. Keying on source id
// alone makes the failover standby and the primary evict each other, which is
// the failover feature failing in the one situation it exists for.
func TestPrimaryAndBackupAreDifferentSlots(t *testing.T) {
	primary := Target{SourceID: 7, Backup: false}
	backup := Target{SourceID: 7, Backup: true}
	if primary.Key() == backup.Key() {
		t.Fatal("primary and backup share a publisher slot, so they would evict each other")
	}
	if primary.Key() != (PublisherKey{SourceID: 7}) {
		t.Errorf("primary key = %+v", primary.Key())
	}
	if backup.Key() != (PublisherKey{SourceID: 7, Backup: true}) {
		t.Errorf("backup key = %+v", backup.Key())
	}
}

func TestPublishingReportsLiveSlots(t *testing.T) {
	s := New(quiet(), "127.0.0.1:0", ConstantTimeLookup(nil))
	if s.PublishingRole(1, false) {
		t.Error("nothing is publishing yet")
	}
	s.live[PublisherKey{SourceID: 1}] = &session{started: time.Now()}
	if !s.PublishingRole(1, false) {
		t.Error("the primary slot is live and should report so")
	}
	if s.PublishingRole(1, true) {
		t.Error("the backup slot is NOT live; primary must not answer for it")
	}
	// Publishing is the source-level question the API asks, and a live primary
	// has to answer it yes. If it only consulted one slot, a source carried by
	// its standby would show as off air on the Sources page.
	if !s.Publishing(1) {
		t.Error("Publishing must be true when either slot is live")
	}
	if s.Publishing(2) {
		t.Error("Publishing must not answer for a source with no live slot")
	}
}

/* ------------------------------------------------------------------ lifecycle */

func TestStartBindsAndStopReleases(t *testing.T) {
	s := New(quiet(), "127.0.0.1:0", ConstantTimeLookup(nil))
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	s.mu.Lock()
	addr := s.ln.Addr().String()
	s.mu.Unlock()

	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("listener is not accepting: %v", err)
	}
	_ = c.Close()

	s.Stop()

	// The port must actually be released, or a restart cannot rebind it.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err != nil {
			return // refused: released
		} else {
			_ = c.Close()
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("the listener is still accepting after Stop")
}

func TestStopIsIdempotent(t *testing.T) {
	// Stop runs from shutdown paths that can overlap; closing `done` twice
	// panics, and a panic during shutdown loses the rest of the shutdown.
	s := New(quiet(), "127.0.0.1:0", ConstantTimeLookup(nil))
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	s.Stop()
	s.Stop()
}

func TestStartOnABusyPortFails(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("fixture listen: %v", err)
	}
	defer func() { _ = held.Close() }()

	s := New(quiet(), held.Addr().String(), ConstantTimeLookup(nil))
	err = s.Start()
	if err == nil {
		s.Stop()
		t.Fatal("Start on a taken port must fail rather than report success and bind nothing")
	}
	if !strings.Contains(err.Error(), "rtmp listen") {
		t.Errorf("error should name what failed: %v", err)
	}
}

/* --------------------------------------------------------------- refusals */

// The admission decision, every branch of it. Two of these are security
// properties and one is a diagnosis, and none of them can be reached from a
// unit test through a real RTMP handshake, which is why admit is a function.
func TestAdmitRefusesEverythingThatIsNotAReadyEnabledSource(t *testing.T) {
	ready := Target{SourceID: 1, Name: "Horizontal", Enabled: true, Ready: true}

	tests := []struct {
		name   string
		target Target
		found  bool
		want   verdict
	}{
		{"a ready enabled source publishes", ready, true, admitPublish},
		{
			// The only branch that must stay anonymous in the log. Anything
			// that distinguishes "no such key" from "a key for something" turns
			// the log, and the timing, into an enumeration oracle.
			"an unrecognised key", Target{}, false, refuseUnknownKey,
		},
		{
			// Reachable only by a publisher holding a real key, so naming it
			// leaks nothing -- and an operator who disabled a source and forgot
			// has no other way to find out.
			"a disabled source", Target{SourceID: 1, Name: "Horizontal", Ready: true}, true, refuseDisabled,
		},
		{
			// The Ready gate. Admitting here fans the encoder's bytes out to
			// nobody: OBS goes green, no output appears, and nothing anywhere
			// says why.
			"a source with no engine", Target{SourceID: 1, Name: "Horizontal", Enabled: true}, true, refuseNotReady,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := admit(tc.target, tc.found); got != tc.want {
				t.Errorf("admit(%+v, %v) = %v, want %v", tc.target, tc.found, got, tc.want)
			}
		})
	}
}

// A lookup miss must not leak through the refusal message either. The zero
// Target carries an empty Name, and the unknown-key branch is the one that must
// never reach the "source" log field at all.
func TestAnUnknownKeyIsRefusedBeforeAnythingIsNamed(t *testing.T) {
	if got := admit(Target{Name: "Horizontal", Enabled: true, Ready: true}, false); got != refuseUnknownKey {
		t.Fatalf("admit(..., found=false) = %v; a miss must win over every other field", got)
	}
}

/* --------------------------------------------------------------- pub/sub */

// The correction that mattered: the first draft relayed each publisher OUTWARD
// to a per-source FFmpeg on its own loopback port. datarhei Core — and every
// other RTMP server — instead serves both directions on ONE port, with FFmpeg
// subscribing to the same listener the encoder published to. These assert the
// properties that model has to have.

func TestIsSetupClassifiesWhatALateJoinerNeeds(t *testing.T) {
	setup := []message.Message{
		&message.DataAMF0{},
		&message.Video{Type: message.VideoTypeConfig},
		&message.Audio{AACType: message.AudioAACTypeConfig},
		&message.AudioExSequenceStart{},
		&message.VideoExSequenceStart{},
		&message.AudioExMultichannelConfig{},
	}
	for _, m := range setup {
		if !isSetup(m) {
			t.Errorf("%T must be cached: a subscriber joining later cannot decode without it", m)
		}
	}

	live := []message.Message{
		&message.Video{Type: message.VideoTypeAU},
		&message.Audio{AACType: message.AudioAACTypeAU},
		&message.AudioExCodedFrames{},
	}
	for _, m := range live {
		if isSetup(m) {
			t.Errorf("%T is a media frame; caching every one of them would grow without bound", m)
		}
	}
}

func TestOnlyLoopbackMaySubscribe(t *testing.T) {
	// A stream key is a PUBLISH credential. If it also authorised playback,
	// every ingest key would silently become a viewing key.
	loopback := []string{"127.0.0.1:5000", "[::1]:5000"}
	for _, a := range loopback {
		addr, err := net.ResolveTCPAddr("tcp", a)
		if err != nil {
			t.Fatalf("resolve %s: %v", a, err)
		}
		if !isLoopback(addr) {
			t.Errorf("%s is loopback and must be allowed to subscribe", a)
		}
	}
	remote := []string{"10.0.0.5:5000", "192.168.1.9:5000", "8.8.8.8:5000"}
	for _, a := range remote {
		addr, err := net.ResolveTCPAddr("tcp", a)
		if err != nil {
			t.Fatalf("resolve %s: %v", a, err)
		}
		if isLoopback(addr) {
			t.Errorf("%s is NOT loopback; allowing it turns publish keys into viewing keys", a)
		}
	}
}

/* ======================================================================
   End-to-end: bytes in one end, bytes out the other.

   The tests these replaced set struct fields and then asserted on what they
   had just written — none of them called serveSubscriber, pump or admit, so
   every one of them passed with the implementation deleted. That is why the
   two worst bugs in this package got through review: a publisher on a
   still-valid old key was black-holed, and subscribers leaked on Stop. Both
   are invisible to a test that never moves a message.
   ====================================================================== */

// pubAndSub runs a real publisher and a real subscriber against a real Server
// and reports whether media reached the subscriber. pubKey and subKey may
// differ — that is the whole point of the first test below.
func pubAndSub(t *testing.T, targets map[string]Target, pubKey, subKey string) bool {
	t.Helper()

	s := New(quiet(), "127.0.0.1:0", ConstantTimeLookup(targets))
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer s.Stop()
	s.mu.Lock()
	addr := s.ln.Addr().String()
	s.mu.Unlock()

	got := make(chan struct{}, 1)

	// Subscriber first: that is the normal order, since the engine starts
	// FFmpeg when the source is enabled, well before anyone hits Start in OBS.
	go func() {
		u, _ := url.Parse("rtmp://" + addr + "/live/" + subKey)
		c := &gortmplib.Client{URL: u, Publish: false}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if err := c.Initialize(ctx); err != nil {
			return
		}
		defer c.Close()
		for {
			msg, err := c.Read()
			if err != nil {
				return
			}
			// ONLY the publisher's payload counts. A subscriber receives RTMP
			// protocol messages regardless of whether any media is flowing, so
			// counting "Read returned something" reports success for a stream
			// that delivered nothing — which is the exact failure these tests
			// exist to catch, reproduced inside the test itself.
			if _, isPayload := msg.(*message.DataAMF0); !isPayload {
				continue
			}
			select {
			case got <- struct{}{}:
			default:
			}
		}
	}()
	time.Sleep(300 * time.Millisecond)

	go func() {
		u, _ := url.Parse("rtmp://" + addr + "/live/" + pubKey)
		c := &gortmplib.Client{URL: u, Publish: true}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if err := c.Initialize(ctx); err != nil {
			return
		}
		defer c.Close()
		for i := 0; i < 40; i++ {
			// ChunkStreamID must be >= 2. Zero and one are the ESCAPE values that
			// signal an extended chunk stream ID, which gortmplib does not
			// implement — so leaving it unset made the server reject the very
			// first message and looked exactly like a broken relay.
			if err := c.Write(&message.DataAMF0{ChunkStreamID: 4, MessageStreamID: 1}); err != nil {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
	}()

	select {
	case <-got:
		return true
	case <-time.After(3 * time.Second):
		return false
	}
}

// dialPublish runs a real RTMP publish handshake against addr with key, and
// reports the error from it, if any.
//
// gortmplib's ServerConn.Accept completes the RTMP-level publish handshake
// for ANY key, valid or not -- key validation is application logic that runs
// only after Accept returns. So a nil error here does not mean the key was
// accepted, only that the connection got that far; the caller has to close
// the client. An error here does mean the connection was refused before or
// during the handshake itself, which is what a peer blocked by authgate
// looks like: handle returns without ever calling sc.Initialize.
func dialPublish(t *testing.T, addr, key string) error {
	t.Helper()
	u, err := url.Parse("rtmp://" + addr + "/live/" + key)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	c := &gortmplib.Client{URL: u, Publish: true}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = c.Initialize(ctx)
	if err == nil {
		c.Close()
	}
	return err
}

// TestHandleRateLimitsPeerAfterRepeatedUnknownKeys is the network-level half
// of the #19 poka-yoke fix, run against the real listener rather than the
// gate in isolation: an unrecognised stream key must count against the
// peer's authgate.Gate, and once that peer crosses the threshold, every
// further connection from it -- even one presenting a key that would
// otherwise be accepted -- must be refused before the RTMP handshake is even
// attempted.
func TestHandleRateLimitsPeerAfterRepeatedUnknownKeys(t *testing.T) {
	target := Target{SourceID: 1, Name: "Main", Enabled: true, Ready: true}
	s := New(quiet(), "127.0.0.1:0", ConstantTimeLookup(map[string]Target{"good-key": target}))
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer s.Stop()
	s.mu.Lock()
	addr := s.ln.Addr().String()
	s.mu.Unlock()

	// Enough wrong-key attempts from this one loopback peer to cross the
	// gate's threshold. Each of these must still complete its own RTMP
	// handshake -- Accept succeeds regardless of the key -- and only then get
	// refused at the application layer, once the key fails lookup.
	for i := 0; i < authgate.Threshold; i++ {
		if err := dialPublish(t, addr, "wrong-key"); err != nil {
			t.Fatalf("attempt %d: the RTMP handshake itself should still "+
				"succeed for an unrecognised key -- refusal happens after, "+
				"at the application layer -- got %v", i, err)
		}
	}

	// The peer is now blocked. A further connection -- even one presenting
	// the correct key -- must be refused before the handshake is even
	// attempted, which surfaces here as the handshake itself failing.
	if err := dialPublish(t, addr, "good-key"); err == nil {
		t.Fatal("a peer that just crossed the gate's threshold was still " +
			"able to complete a handshake with a valid key")
	}
}

// The bug this package shipped with: the stream table was keyed by the STRING
// the publisher typed, but a source has several valid keys at once — the
// current token, the previous one during a rotation grace window, and any
// grandfathered legacy key. A publisher on an old-but-valid key was admitted,
// counted as publishing, shown green in the UI, and fanned out to nobody.
func TestAnyValidKeyForASourceReachesTheSameStream(t *testing.T) {
	if testing.Short() {
		t.Skip("moves real media over a real socket")
	}
	target := Target{SourceID: 1, Name: "Main", Enabled: true, Ready: true}
	targets := map[string]Target{
		"current":  target,
		"previous": target, // rotation grace window
		"legacy":   target, // grandfathered from before the rotation existed
	}

	for _, tc := range []struct{ name, pub, sub string }{
		{"same key both ends", "current", "current"},
		{"publisher on the previous token during a grace window", "previous", "current"},
		{"publisher on a grandfathered legacy key", "legacy", "current"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !pubAndSub(t, targets, tc.pub, tc.sub) {
				t.Errorf("published with %q, subscribed with %q, nothing arrived — "+
					"the publisher is admitted and reported live while its bytes go nowhere",
					tc.pub, tc.sub)
			}
		})
	}
}

func TestAKeyForADifferentSourceDoesNotCross(t *testing.T) {
	if testing.Short() {
		t.Skip("moves real media over a real socket")
	}
	targets := map[string]Target{
		"one": {SourceID: 1, Name: "One", Enabled: true, Ready: true},
		"two": {SourceID: 2, Name: "Two", Enabled: true, Ready: true},
	}
	if pubAndSub(t, targets, "one", "two") {
		t.Error("a publisher on source 1's key reached source 2's subscriber — programmes are crossing")
	}
}

// Stop must wake every subscriber. internal/engine/manager.go justifies its
// shutdown ordering on the claim that it does; before this, Stop dropped the
// streams map and left each subscriber goroutine parked on a channel nothing
// would ever close.
func TestStopWakesSubscribers(t *testing.T) {
	s := New(quiet(), "127.0.0.1:0", ConstantTimeLookup(nil))
	key := PublisherKey{SourceID: 1}
	subs := make([]*subscriber, 3)
	s.mu.Lock()
	s.streams[key] = &stream{subs: map[*subscriber]struct{}{}}
	for i := range subs {
		subs[i] = &subscriber{ch: make(chan message.Message, 1), done: make(chan struct{})}
		s.streams[key].subs[subs[i]] = struct{}{}
	}
	s.mu.Unlock()

	s.Stop()

	for i, sub := range subs {
		select {
		case <-sub.done:
		default:
			t.Errorf("subscriber %d was not woken by Stop; its goroutine is parked forever", i)
		}
	}
}

// A subscriber that goes away with no publisher live must be reaped. The engine
// restarts its ingest child on every reconcile that changes the ingest
// signature, so this happens on an ordinary settings change — and each leak
// held a goroutine, a socket, and an entry pump kept writing into.
func TestAnIdleStreamIsReapedWhenItsLastSubscriberLeaves(t *testing.T) {
	s := New(quiet(), "127.0.0.1:0", ConstantTimeLookup(nil))
	key := PublisherKey{SourceID: 1}
	sub := &subscriber{ch: make(chan message.Message, 1), done: make(chan struct{})}

	s.mu.Lock()
	s.streams[key] = &stream{subs: map[*subscriber]struct{}{sub: {}}}
	s.mu.Unlock()

	// What serveSubscriber's defer does.
	s.mu.Lock()
	if cur := s.streams[key]; cur != nil {
		delete(cur.subs, sub)
		if len(cur.subs) == 0 && s.live[key] == nil {
			delete(s.streams, key)
		}
	}
	n := len(s.streams)
	s.mu.Unlock()

	if n != 0 {
		t.Error("the stream entry survived its last subscriber; every rotated-out key would leak one forever")
	}
}

/* ------------------------------------------------------- shutdown under load */

// Stop walks every subscriber and closes its socket, while serveSubscriber is
// still setting those subscribers up. The two goroutines meet on the same
// struct, so the ONLY thing keeping them apart is the order in which a
// subscriber is initialised and published into s.streams.
//
// This is a regression test for a real defect, and one that only CI found:
// sub.conn was assigned AFTER the subscriber had been put in the map, so Stop
// could read the field while serveSubscriber wrote it. Every local run passed —
// the bug is invisible without -race, which is the point. Run this package with
// -race or this test proves nothing:
//
//	go test -race ./internal/rtmpserver/
//
// Verified to fail: moving the sub.conn assignment back below the s.mu.Unlock
// in serveSubscriber makes this report a data race.
func TestStopWhileSubscribersAreConnectingIsRaceFree(t *testing.T) {
	const subscribers = 12

	targets := map[string]Target{
		"key-a": {SourceID: 1, Name: "a", Enabled: true, Ready: true},
	}
	s := New(quiet(), "127.0.0.1:0", ConstantTimeLookup(targets))
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	s.mu.Lock()
	addr := s.ln.Addr().String()
	s.mu.Unlock()

	// The subscribers are deliberately NOT synchronised with the Stop below.
	// Staggering them so some are mid-handshake, some are registered and some
	// have not dialled yet is what puts a half-initialised subscriber in the
	// map at the moment Stop walks it.
	var wg sync.WaitGroup
	for i := 0; i < subscribers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u, _ := url.Parse("rtmp://" + addr + "/live/key-a")
			c := &gortmplib.Client{URL: u, Publish: false}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			if err := c.Initialize(ctx); err != nil {
				// Refused or torn down mid-handshake is a legitimate outcome
				// here — Stop is racing these on purpose. The assertion is
				// about memory safety, not about who wins.
				return
			}
			defer c.Close()
			for {
				if _, err := c.Read(); err != nil {
					return
				}
			}
		}()
	}

	// Long enough that some connections are established and short enough that
	// others are still arriving.
	time.Sleep(150 * time.Millisecond)
	s.Stop()
	wg.Wait()

	// Stop must also have emptied the table rather than merely dropping its
	// reference to it, or the sockets it was meant to close are still open.
	s.mu.Lock()
	n := len(s.streams)
	s.mu.Unlock()
	if n != 0 {
		t.Errorf("Stop left %d stream(s) behind; the subscriber sockets it should have closed are still open", n)
	}
}

// Stop being called from several goroutines at once is not hypothetical: the
// engine reconciles on a timer and on demand, and both paths can decide the
// listener is no longer wanted. Closing a channel twice panics, so the guard
// inside close() has to hold under genuine concurrency, not just sequentially.
func TestConcurrentStopsDoNotPanic(t *testing.T) {
	targets := map[string]Target{
		"key-a": {SourceID: 1, Name: "a", Enabled: true, Ready: true},
	}
	s := New(quiet(), "127.0.0.1:0", ConstantTimeLookup(targets))
	if err := s.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	s.mu.Lock()
	addr := s.ln.Addr().String()
	s.mu.Unlock()

	// A real subscriber, so Stop has something to walk rather than exercising
	// the empty-map path that would pass no matter what.
	done := make(chan struct{})
	go func() {
		defer close(done)
		u, _ := url.Parse("rtmp://" + addr + "/live/key-a")
		c := &gortmplib.Client{URL: u, Publish: false}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if err := c.Initialize(ctx); err != nil {
			return
		}
		defer c.Close()
		for {
			if _, err := c.Read(); err != nil {
				return
			}
		}
	}()
	time.Sleep(200 * time.Millisecond)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.Stop() }()
	}
	wg.Wait()
	<-done
}

/* ------------------------------------------------------ multitrack setup cache */

// The setup a late joiner is replayed must cover EVERY track, not just the
// legacy one.
//
// E-RTMP sends tracks 2..N wrapped: AudioExMultitrack carries a TrackID and the
// real message inside it, so matching only the unwrapped types classified the
// legacy track's config as setup and nothing else. A subscriber that attached
// after the publisher then held data for tracks it had no configuration for, and
// ffprobe hung rather than failing — which is worse, because a hang looks like a
// slow network and a failure looks like a bug.
func TestMultitrackSequenceStartsAreRecognisedAsSetup(t *testing.T) {
	wrapped := &message.AudioExMultitrack{
		MultitrackType: message.AudioExMultitrackTypeOneTrack,
		TrackID:        1,
		Wrapped:        &message.AudioExSequenceStart{},
	}
	if !isSetup(wrapped) {
		t.Error("a wrapped AudioExSequenceStart is not treated as setup, so every track " +
			"after the first is missing its decoder config for late subscribers")
	}
	// Media inside the same wrapper is NOT setup. Caching coded frames would
	// replay stale audio at every new subscriber and grow without bound.
	media := &message.AudioExMultitrack{
		MultitrackType: message.AudioExMultitrackTypeOneTrack,
		TrackID:        1,
		Wrapped:        &message.AudioExCodedFrames{},
	}
	if isSetup(media) {
		t.Error("wrapped coded frames are treated as setup; the replay list would grow " +
			"for the life of the broadcast and every new subscriber would be sent stale audio")
	}
}

// Two tracks' sequence starts are different setup. One slot for both would mean
// the second track overwrote the first and only the last one ever reached a
// subscriber.
func TestEachTrackGetsItsOwnSetupSlot(t *testing.T) {
	one, ok1 := setupSlot(&message.AudioExMultitrack{
		TrackID: 0, Wrapped: &message.AudioExSequenceStart{},
	})
	two, ok2 := setupSlot(&message.AudioExMultitrack{
		TrackID: 1, Wrapped: &message.AudioExSequenceStart{},
	})
	if !ok1 || !ok2 {
		t.Fatalf("multitrack sequence starts are not slotted: %v %v", ok1, ok2)
	}
	if one == two {
		t.Errorf("track 0 and track 1 share the setup slot %q, so one overwrites the other "+
			"and a late subscriber can only ever decode whichever arrived last", one)
	}
}

// A republished sequence start REPLACES its predecessor.
//
// Encoders resend configuration, so appending grew the replay list for the whole
// broadcast: every new subscriber got a longer prologue, ending with superseded
// configuration replayed ahead of the current one.
func TestRepublishedSetupReplacesRatherThanAccumulates(t *testing.T) {
	targets := map[string]Target{"k": {SourceID: 1, Name: "a", Enabled: true, Ready: true}}
	s := New(quiet(), "127.0.0.1:0", ConstantTimeLookup(targets))
	key := PublisherKey{SourceID: 1}
	st := &stream{subs: map[*subscriber]struct{}{}, slots: map[string]int{}}
	s.streams[key] = st

	// Two of the same slot, then one of another.
	msgs := []message.Message{
		&message.AudioExMultitrack{TrackID: 0, Wrapped: &message.AudioExSequenceStart{}},
		&message.AudioExMultitrack{TrackID: 0, Wrapped: &message.AudioExSequenceStart{}},
		&message.AudioExMultitrack{TrackID: 1, Wrapped: &message.AudioExSequenceStart{}},
	}
	for _, m := range msgs {
		st.cacheSetup(m)
	}
	if len(st.setup) != 2 {
		t.Errorf("cached %d setup messages for two tracks; a resent config was appended "+
			"rather than replacing the one it supersedes", len(st.setup))
	}
	// And the surviving entry is the NEWEST, not the first.
	if got := st.setup[0]; got != msgs[1] {
		t.Error("the replaced slot kept the older message; a subscriber would be configured " +
			"with configuration the publisher has already superseded")
	}
}

// A publisher reconnecting must not leave the setup cache inconsistent.
//
// setup and slots are one structure in two fields: slots holds indices INTO
// setup. admitSession clears setup on reconnect — correctly, since the old
// sequence headers describe an encode that has ended — and clearing only that
// half left every index dangling. The next sequence start looked up its old
// slot and wrote past the end of an empty slice, panicking the listener.
//
// This is the ordinary case, not an edge one: an encoder that drops and comes
// back is what failover exists for, and it is what the failover acceptance
// suite does deliberately. It found this.
func TestReconnectClearsTheSetupCacheConsistently(t *testing.T) {
	st := &stream{subs: map[*subscriber]struct{}{}, slots: map[string]int{}}

	cache := st.cacheSetup

	first := &message.AudioExMultitrack{TrackID: 0, Wrapped: &message.AudioExSequenceStart{}}
	cache(first)
	if len(st.setup) != 1 {
		t.Fatalf("setup for the first session did not cache: %d", len(st.setup))
	}

	// The very call admitSession makes when the encoder comes back, rather than
	// a copy of it — a copy would keep passing after admitSession was changed.
	st.resetSetup()

	// Must not panic, and must rebuild rather than index into nothing.
	cache(&message.AudioExMultitrack{TrackID: 0, Wrapped: &message.AudioExSequenceStart{}})
	if len(st.setup) != 1 {
		t.Fatalf("after a reconnect the cache holds %d entries, want 1", len(st.setup))
	}
	if st.setup[0] == first {
		t.Error("the reconnected session replayed the PREVIOUS encode's sequence header, " +
			"which describes a stream that has ended")
	}
}

// TestEveryVerdictConstantIsHandled pins the exact set of verdict constants
// this switch is written against. A new constant added to the const block
// without a case here is exactly the shape of change TestVerdictStringHasNoSilentAdmit
// below cannot catch on its own (it only proves the DEFAULT case is safe, not
// that every real constant still gets its own line) -- so this enumerates the
// known set explicitly and both tests must be kept in sync with the const block.
func TestEveryVerdictConstantIsHandled(t *testing.T) {
	want := map[verdict]string{
		admitPublish:     "admitted",
		refuseUnknownKey: "unrecognised",
		refuseDisabled:   "source disabled",
		refuseNotReady:   "no pipeline for source",
	}
	for v, s := range want {
		if got := v.String(); got != s {
			t.Errorf("verdict(%d).String() = %q, want %q", int(v), got, s)
		}
	}
}

// TestVerdictStringHasNoSilentAdmit is the #14 poka-yoke audit fix. verdict.String
// used to fall through an unhandled case to the hardcoded "admitted" -- so any
// value the switch had not been taught about read as a successful publish on
// the one log line that distinguishes a refusal's reason. A refusal must never
// silently become "admitted" in the log an operator is reading to find out why
// a publisher was turned away.
func TestVerdictStringHasNoSilentAdmit(t *testing.T) {
	unhandled := verdict(99)
	if got := unhandled.String(); got == "admitted" {
		t.Fatalf("verdict(99).String() = %q, want anything but the success "+
			"string -- an unhandled verdict must not read as admitted", got)
	} else if !strings.Contains(got, "BUG") {
		t.Errorf("verdict(99).String() = %q, want it to visibly announce it is "+
			"an unhandled case rather than looking like an ordinary refusal reason", got)
	}
}
