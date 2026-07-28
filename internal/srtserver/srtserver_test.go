package srtserver

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	srt "github.com/datarhei/gosrt"
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

func freePort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}

// serve starts a server over a fixed target set.
func serve(t *testing.T, targets ...Target) (*Server, string) {
	t.Helper()
	port := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	lookup := ConstantTimeLookup(
		func() []Target { return targets },
		func(tg Target) []string { return []string{tokenFor(tg)} },
	)
	s := New(quietLog(), addr, lookup)
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
