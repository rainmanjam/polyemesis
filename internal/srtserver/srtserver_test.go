package srtserver

import (
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	srt "github.com/datarhei/gosrt"
	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// These are real SRT connections over a real socket, not fakes. That is worth
// stating because it has not been possible before: every SRT proof in this
// project has had to run inside Docker, since the FFmpeg on a developer machine
// is routinely built without libsrt. Owning the listener means the ingest path
// is finally testable where the code is written.

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// recorder is a Sink that remembers what it was handed.
type recorder struct {
	mu   sync.Mutex
	data []byte
	n    int
}

func (r *recorder) Deliver(pkt []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data = append(r.data, pkt...)
	r.n++
}

func (r *recorder) bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.data...)
}

// serve starts a server over a fixed target set.
//
// #211: the port is HELD until the statement before Start, rather than being
// handed back by a helper and re-bound several statements later. The window in
// which something else can take it is now one line long. It is not zero, and
// internal/testenv/ports.go says why that cannot honestly be arranged.
func serve(t *testing.T, targets ...Target) (*Server, string) {
	t.Helper()
	res := testenv.ReserveUDP(t)
	addr := fmt.Sprintf("127.0.0.1:%d", res.Port())

	lookup := ConstantTimeLookup(
		func() []Target { return targets },
		func(tg Target) []string { return []string{tokenFor(tg)} },
	)
	s := New(quietLog(), addr, lookup)
	res.Release()
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(s.Stop)
	return s, addr
}

// tokenFor keeps the test's token convention in one place.
func tokenFor(tg Target) string { return "tok-" + tg.Name }

func dial(t *testing.T, addr, streamID string) (srt.Conn, error) {
	t.Helper()
	cfg := srt.DefaultConfig()
	cfg.StreamId = streamID
	cfg.ConnectionTimeout = 3 * time.Second
	return srt.Dial("srt", addr, cfg)
}

// waitFor polls until cond holds or the window runs out. SRT delivery is not
// synchronous with Write, so every assertion about arrival has to wait.
func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

func TestAValidTokenPublishesIntoItsOwnSink(t *testing.T) {
	sink := &recorder{}
	tg := Target{SourceID: 7, Name: "horizontal", Enabled: true, Sink: sink}
	_, addr := serve(t, tg)

	conn, err := dial(t, addr, tokenFor(tg))
	if err != nil {
		t.Fatalf("dial with a valid token was refused: %v", err)
	}
	want := []byte("mpegts-ish payload")
	if _, err := conn.Write(want); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Give the read loop a moment; SRT delivery is not synchronous with Write.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(sink.bytes()) == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	conn.Close()

	if got := string(sink.bytes()); got != string(want) {
		t.Errorf("sink received %q, want %q", got, want)
	}
}

func TestAnUnknownTokenIsRefused(t *testing.T) {
	tg := Target{SourceID: 1, Name: "horizontal", Enabled: true, Sink: &recorder{}}
	_, addr := serve(t, tg)

	if conn, err := dial(t, addr, "not-a-real-token"); err == nil {
		conn.Close()
		t.Fatal("an unknown token was accepted; anyone could publish")
	}
}

func TestAnEmptyStreamIDIsRefused(t *testing.T) {
	tg := Target{SourceID: 1, Name: "horizontal", Enabled: true, Sink: &recorder{}}
	_, addr := serve(t, tg)

	// A publisher that sends no streamid must not fall through to whichever
	// source happens to have an empty token stored.
	if conn, err := dial(t, addr, ""); err == nil {
		conn.Close()
		t.Fatal("an empty streamid was accepted")
	}
}

func TestADisabledSourceIsRefused(t *testing.T) {
	tg := Target{SourceID: 1, Name: "paused", Enabled: false, Sink: &recorder{}}
	_, addr := serve(t, tg)

	if conn, err := dial(t, addr, tokenFor(tg)); err == nil {
		conn.Close()
		t.Fatal("a disabled source accepted a publisher")
	}
}

func TestASourceWithNoPipelineIsRefusedRatherThanSwallowed(t *testing.T) {
	// The source exists and the token is right, but no engine is running for
	// it. Accepting would take the stream and drop it on the floor, which
	// presents to the operator as "it says connected but nothing works".
	tg := Target{SourceID: 1, Name: "orphan", Enabled: true, Sink: nil}
	_, addr := serve(t, tg)

	if conn, err := dial(t, addr, tokenFor(tg)); err == nil {
		conn.Close()
		t.Fatal("a source with no pipeline accepted a publisher")
	}
}

// The behaviour that differs from datarhei Core, in both directions.

func TestALivePublisherBlocksASecondOne(t *testing.T) {
	sink := &recorder{}
	tg := Target{SourceID: 3, Name: "horizontal", Enabled: true, Sink: sink}
	srv, addr := serve(t, tg)

	first, err := dial(t, addr, tokenFor(tg))
	if err != nil {
		t.Fatalf("first publisher refused: %v", err)
	}
	defer first.Close()
	// Keep it demonstrably alive.
	if _, err := first.Write([]byte("alive")); err != nil {
		t.Fatalf("write: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !srv.Publishing(tg.SourceID) {
		time.Sleep(20 * time.Millisecond)
	}
	if !srv.Publishing(tg.SourceID) {
		t.Fatal("the first publisher never registered")
	}

	// Genuine double-publishing is still refused.
	if second, err := dial(t, addr, tokenFor(tg)); err == nil {
		second.Close()
		t.Fatal("a second publisher was accepted while the first was live")
	}
}

func TestAStalePublisherIsTakenOver(t *testing.T) {
	// This is the case Core gets wrong. An uplink drops, the dead socket has
	// not been reaped, the encoder reconnects — and Core refuses it, leaving
	// the operator off the air with nothing to act on.
	sink := &recorder{}
	tg := Target{SourceID: 5, Name: "horizontal", Enabled: true, Sink: sink}
	srv, addr := serve(t, tg)

	first, err := dial(t, addr, tokenFor(tg))
	if err != nil {
		t.Fatalf("first publisher refused: %v", err)
	}
	if _, err := first.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !srv.Publishing(tg.SourceID) {
		time.Sleep(20 * time.Millisecond)
	}

	// Go quiet for longer than StaleAfter without closing: exactly what a
	// half-dead connection looks like from the server's side.
	time.Sleep(StaleAfter + 500*time.Millisecond)

	second, err := dial(t, addr, tokenFor(tg))
	if err != nil {
		t.Fatalf("reconnect after the incumbent went quiet was refused: %v -- "+
			"this is the lockout the takeover exists to prevent", err)
	}
	defer second.Close()

	if _, err := second.Write([]byte("world")); err != nil {
		t.Fatalf("write after takeover: %v", err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := string(sink.bytes()); len(got) >= len("helloworld") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := string(sink.bytes()); got != "helloworld" {
		t.Errorf("sink has %q, want %q -- the taken-over stream should continue "+
			"into the same source", got, "helloworld")
	}
	first.Close()
}

func TestTwoSourcesNeverReceiveEachOthersBytes(t *testing.T) {
	// The separation the whole feature rests on, at the listener level.
	sinkA, sinkB := &recorder{}, &recorder{}
	a := Target{SourceID: 1, Name: "horizontal", Enabled: true, Sink: sinkA}
	b := Target{SourceID: 2, Name: "vertical", Enabled: true, Sink: sinkB}
	_, addr := serve(t, a, b)

	ca, err := dial(t, addr, tokenFor(a))
	if err != nil {
		t.Fatalf("dial a: %v", err)
	}
	defer ca.Close()
	cb, err := dial(t, addr, tokenFor(b))
	if err != nil {
		t.Fatalf("dial b: %v", err)
	}
	defer cb.Close()

	if _, err := ca.Write([]byte("AAAA")); err != nil {
		t.Fatalf("write a: %v", err)
	}
	if _, err := cb.Write([]byte("BBBB")); err != nil {
		t.Fatalf("write b: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(sinkA.bytes()) > 0 && len(sinkB.bytes()) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if got := string(sinkA.bytes()); got != "AAAA" {
		t.Errorf("source A received %q, want %q", got, "AAAA")
	}
	if got := string(sinkB.bytes()); got != "BBBB" {
		t.Errorf("source B received %q, want %q", got, "BBBB")
	}
}

func TestConstantTimeLookupAcceptsAnyOfASourcesTokens(t *testing.T) {
	// Several tokens per source is what lets a rotation keep the previous one
	// alive for a grace window, so rotating does not kill a live stream.
	tg := Target{SourceID: 9, Name: "horizontal", Enabled: true, Sink: &recorder{}}
	lookup := ConstantTimeLookup(
		func() []Target { return []Target{tg} },
		func(Target) []string { return []string{"current-token", "previous-token"} },
	)
	for _, tok := range []string{"current-token", "previous-token"} {
		if _, ok := lookup(tok); !ok {
			t.Errorf("lookup(%q) missed; a rotation would drop a live publisher", tok)
		}
	}
	if _, ok := lookup("expired-token"); ok {
		t.Error("lookup accepted a token that is not current or previous")
	}
	if _, ok := lookup(""); ok {
		t.Error("lookup accepted the empty token")
	}
}

// A source presents two targets on one listener: its primary and its failover
// standby. They must not be reachable by each other's token — a publisher that
// could take the primary's slot by presenting the backup address (or the
// reverse) would silently put the standby feed on air, which is exactly the
// mix-up failover exists to prevent.
func TestPrimaryAndBackupTokensDoNotCrossOver(t *testing.T) {
	primary := Target{SourceID: 1, Name: "Main", Enabled: true}
	backup := Target{SourceID: 1, Name: "Main (backup)", Enabled: true, Backup: true}
	toks := map[bool][]string{
		false: {"tok"},
		true:  {"tok.backup"},
	}
	lookup := ConstantTimeLookup(
		func() []Target { return []Target{primary, backup} },
		func(t Target) []string { return toks[t.Backup] },
	)

	got, ok := lookup("tok")
	if !ok || got.Backup {
		t.Errorf(`"tok" resolved to backup=%v ok=%v, want the primary`, got.Backup, ok)
	}
	got, ok = lookup("tok.backup")
	if !ok || !got.Backup {
		t.Errorf(`"tok.backup" resolved to backup=%v ok=%v, want the standby`, got.Backup, ok)
	}
	if _, ok := lookup("tok.BACKUP"); ok {
		t.Error("token matching is case-insensitive; it must not be")
	}
	if _, ok := lookup("nonsense"); ok {
		t.Error("an unknown token resolved to a target")
	}
}

// A source's primary and its failover standby are two publishers on one
// listener, feeding two deliberately different sinks (eng.Hub() and
// eng.BackupHub()). They exist in order to run at the same time. Every gate in
// the listener therefore keys on (source, role); keying on the source id alone
// made them contend for one slot, so whichever encoder connected first locked
// the other out and a quiet incumbent was displaced by the wrong role.

// rolePair builds a primary and a standby for one source. tokenFor keys off the
// name, so the two get distinct tokens.
func rolePair(sourceID int64) (Target, Target, *recorder, *recorder) {
	primarySink, backupSink := &recorder{}, &recorder{}
	primary := Target{SourceID: sourceID, Name: "Main", Enabled: true, Sink: primarySink}
	backup := Target{SourceID: sourceID, Name: "Main (backup)", Enabled: true,
		Sink: backupSink, Backup: true}
	return primary, backup, primarySink, backupSink
}

func TestAPrimaryAndItsBackupPublishAtTheSameTime(t *testing.T) {
	// MUTATION (run against the committed tree): in handleConnect, replace
	//	incumbent, busy := s.live[target.Key()]
	// with
	//	incumbent, busy := s.live[PublisherKey{SourceID: target.SourceID}]
	// which is the admission check as it was, keyed by source id alone.
	// Observed: FAIL -- "the standby was refused while the primary was live".
	primary, backup, primarySink, backupSink := rolePair(4)
	srv, addr := serve(t, primary, backup)

	pc, err := dial(t, addr, tokenFor(primary))
	if err != nil {
		t.Fatalf("the primary encoder was refused: %v", err)
	}
	defer pc.Close()
	if _, err := pc.Write([]byte("PPPP")); err != nil {
		t.Fatalf("write primary: %v", err)
	}

	bc, err := dial(t, addr, tokenFor(backup))
	if err != nil {
		t.Fatalf("the standby was refused while the primary was live: %v -- "+
			"failover cannot work if the two encoders exclude each other", err)
	}
	defer bc.Close()
	if _, err := bc.Write([]byte("BBBB")); err != nil {
		t.Fatalf("write backup: %v", err)
	}

	if !waitFor(3*time.Second, func() bool {
		return len(primarySink.bytes()) > 0 && len(backupSink.bytes()) > 0
	}) {
		t.Fatalf("only one feed arrived: primary %q, backup %q",
			primarySink.bytes(), backupSink.bytes())
	}
	if got := string(primarySink.bytes()); got != "PPPP" {
		t.Errorf("the primary hub received %q, want %q", got, "PPPP")
	}
	if got := string(backupSink.bytes()); got != "BBBB" {
		t.Errorf("the backup hub received %q, want %q", got, "BBBB")
	}
	if !srv.PublishingRole(4, false) || !srv.PublishingRole(4, true) {
		t.Errorf("PublishingRole says primary=%v backup=%v; both are connected",
			srv.PublishingRole(4, false), srv.PublishingRole(4, true))
	}
	if !srv.Publishing(4) {
		t.Error("Publishing says the source is idle while two encoders feed it")
	}
}

func TestAQuietStandbysReconnectDoesNotEvictTheLivePrimary(t *testing.T) {
	// MUTATION (run against the committed tree): in handlePublish, replace
	//	if old, busy := s.live[target.Key()]; busy {
	// with
	//	if old, busy := s.live[PublisherKey{SourceID: target.SourceID}]; busy {
	// which is the takeover check as it was, keyed by source id alone.
	// Observed: FAIL -- "the reconnecting standby's bytes never arrived".
	primary, backup, primarySink, backupSink := rolePair(6)
	srv, addr := serve(t, primary, backup)

	// The standby connects first so that the mutation above cannot refuse it on
	// the way in: what is under test here is the takeover, not the admission.
	first, err := dial(t, addr, tokenFor(backup))
	if err != nil {
		t.Fatalf("the standby was refused: %v", err)
	}
	if _, err := first.Write([]byte("B1")); err != nil {
		t.Fatalf("write backup: %v", err)
	}
	if !waitFor(3*time.Second, func() bool { return len(backupSink.bytes()) > 0 }) {
		t.Fatal("the standby's first bytes never arrived")
	}

	pc, err := dial(t, addr, tokenFor(primary))
	if err != nil {
		t.Fatalf("the primary encoder was refused: %v", err)
	}
	defer pc.Close()
	// The primary keeps sending throughout, which is the whole point: it is the
	// feed on air, and nothing the standby does may take it off.
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := pc.Write([]byte("P")); err != nil {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
	}()
	defer func() { close(stop); <-done }()

	// The standby's uplink half-dies: quiet for longer than StaleAfter without
	// closing, while the primary carries on.
	time.Sleep(StaleAfter + 500*time.Millisecond)

	second, err := dial(t, addr, tokenFor(backup))
	if err != nil {
		t.Fatalf("the standby's reconnect was refused: %v", err)
	}
	defer second.Close()
	before := len(primarySink.bytes())
	if _, err := second.Write([]byte("B2")); err != nil {
		t.Fatalf("write after standby takeover: %v", err)
	}

	if !waitFor(3*time.Second, func() bool { return string(backupSink.bytes()) == "B1B2" }) {
		t.Errorf("the reconnecting standby's bytes never arrived: backup hub has %q, want %q -- "+
			"the live primary must not be what blocks or displaces it",
			backupSink.bytes(), "B1B2")
	}
	if !waitFor(3*time.Second, func() bool { return len(primarySink.bytes()) > before }) {
		t.Error("the primary stopped delivering when the standby reconnected; " +
			"it was evicted by the wrong role's takeover")
	}
	if !srv.PublishingRole(6, false) {
		t.Error("the primary is no longer publishing after the standby reconnected")
	}
	first.Close()
}

func TestStatsReportsThePrimaryAndTheStandbySeparately(t *testing.T) {
	// MUTATION (run against the committed tree): in Stats, replace
	//	Backup:     sess.key.Backup,
	// with
	//	Backup:     false,
	// Observed: FAIL -- "both links report the same role".
	primary, backup, _, _ := rolePair(11)
	srv, addr := serve(t, primary, backup)

	pc, err := dial(t, addr, tokenFor(primary))
	if err != nil {
		t.Fatalf("the primary encoder was refused: %v", err)
	}
	defer pc.Close()
	bc, err := dial(t, addr, tokenFor(backup))
	if err != nil {
		t.Fatalf("the standby was refused: %v", err)
	}
	defer bc.Close()

	var links []LinkStats
	if !waitFor(3*time.Second, func() bool {
		links = srv.Stats()
		return len(links) == 2
	}) {
		t.Fatalf("Stats reported %d links, want 2 -- one per encoder", len(links))
	}
	roles := map[bool]string{}
	for _, l := range links {
		if l.SourceID != 11 {
			t.Errorf("link for source %d, want 11", l.SourceID)
		}
		if prev, dup := roles[l.Backup]; dup {
			t.Fatalf("both links report the same role (backup=%v): peers %s and %s -- "+
				"an operator cannot tell which encoder is breaking up",
				l.Backup, prev, l.Peer)
		}
		roles[l.Backup] = l.Peer
	}
}

func TestPublishingDistinguishesTheRoleItIsAskedAbout(t *testing.T) {
	// MUTATION (run against the committed tree): in PublishingRole, replace
	//	sess, ok := s.live[PublisherKey{SourceID: sourceID, Backup: backup}]
	// with
	//	sess, ok := s.live[PublisherKey{SourceID: sourceID}]
	// Observed: FAIL -- "PublishingRole reports the standby live; only the
	// primary is connected".
	primary, backup, _, _ := rolePair(12)
	srv, addr := serve(t, primary, backup)

	pc, err := dial(t, addr, tokenFor(primary))
	if err != nil {
		t.Fatalf("the primary encoder was refused: %v", err)
	}
	defer pc.Close()
	if _, err := pc.Write([]byte("P")); err != nil {
		t.Fatalf("write primary: %v", err)
	}
	if !waitFor(3*time.Second, func() bool { return srv.PublishingRole(12, false) }) {
		t.Fatal("the primary never registered")
	}
	if srv.PublishingRole(12, true) {
		t.Error("PublishingRole reports the standby live; only the primary is connected")
	}
	// Publishing is the question about the SOURCE, and bytes are arriving for
	// it, so it stays true whichever single encoder is up.
	if !srv.Publishing(12) {
		t.Error("Publishing says the source is idle while its primary feeds it")
	}
}
