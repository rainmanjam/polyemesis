package engine

import (
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	srt "github.com/datarhei/gosrt"

	"github.com/rainmanjam/polyemesis/internal/srtserver"
	"github.com/rainmanjam/polyemesis/internal/testenv"
)

type discardSink struct{}

func (discardSink) Deliver([]byte) {}

// The Manager's probe is how an engine's degraded-link monitor learns RTT and
// loss: it must answer for the programme and role asked about, over the real
// shared listener, and answer false -- not zeros -- when there is nothing to
// measure.
func TestTheManagersLinkProbeAnswersForTheRequestedPublisherOnly(t *testing.T) {
	m := &Manager{}
	if _, ok := m.srtLinkHealth(1, false); ok {
		t.Fatal("the probe answered with no SRT listener at all")
	}

	target := srtserver.Target{SourceID: 7, Name: "Main", Enabled: true, Sink: discardSink{}}
	lookup := srtserver.ConstantTimeLookup(
		func() []srtserver.Target { return []srtserver.Target{target} },
		func(srtserver.Target) []string { return []string{"tok-main"} },
	)
	res := testenv.ReserveUDP(t)
	addr := fmt.Sprintf("127.0.0.1:%d", res.Port())
	srv := srtserver.New(slog.New(slog.NewTextHandler(io.Discard, nil)), addr, lookup)
	res.Release()
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	m.srt = srv

	if _, ok := m.srtLinkHealth(7, false); ok {
		t.Fatal("the probe answered for a programme nobody is publishing")
	}
	cfg := srt.DefaultConfig()
	cfg.StreamId = "tok-main"
	cfg.ConnectionTimeout = 3 * time.Second
	c, err := srt.Dial("srt", addr, cfg)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := m.srtLinkHealth(7, false); ok {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, ok := m.srtLinkHealth(7, false); !ok {
		t.Fatal("the probe never reported the publishing primary")
	}
	if _, ok := m.srtLinkHealth(7, true); ok {
		t.Error("the probe answered for the backup role, which nobody is publishing")
	}
	if _, ok := m.srtLinkHealth(8, false); ok {
		t.Error("the probe answered for another programme")
	}
}
