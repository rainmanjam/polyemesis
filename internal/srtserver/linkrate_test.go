package srtserver

import (
	"math"
	"testing"
	"time"
)

// The link figures the source card and degraded-link failover (#6) read are
// RATES over the last interval, derived here from SRT's accumulated counters.
// An accumulated loss count only ever rises, so a link that lost a burst an
// hour ago would read as lossy for the rest of the broadcast; what an operator
// and the failover thresholds need is "how lossy is it now".

func TestALinkRateIsMeasuredOverTheLastIntervalNotSinceConnect(t *testing.T) {
	var w rateWindow
	t0 := time.Unix(1_700_000_000, 0)

	// The first reading has nothing to difference against.
	if kbps, loss, ok := w.update(t0, 0, 0, 0); ok || kbps != 0 || loss != 0 {
		t.Fatalf("first reading reported %v kbps / %v%% (ok=%v); there is no interval yet", kbps, loss, ok)
	}

	// 250 000 bytes in 2 s is 1000 kbps. 90 received + 10 missing is 10% loss.
	kbps, loss, ok := w.update(t0.Add(2*time.Second), 250_000, 90, 10)
	if !ok {
		t.Fatal("a 2 s interval produced no rate")
	}
	if math.Abs(kbps-1000) > 0.001 {
		t.Errorf("bitrate %v kbps, want 1000", kbps)
	}
	if math.Abs(loss-10) > 0.001 {
		t.Errorf("loss %v%%, want 10", loss)
	}

	// The next interval is clean. Cumulatively the link has still lost 10
	// packets; over THIS interval it lost none, and that is what is reported.
	_, loss, _ = w.update(t0.Add(4*time.Second), 500_000, 190, 10)
	if loss != 0 {
		t.Errorf("a clean interval after a lossy one reported %v%% loss; the rate is cumulative", loss)
	}
}

// Several callers read Stats -- the status sweep, the metrics scrape, the
// failover sweep. A reading less than a second after the last one keeps the
// last rate instead of differencing a sliver of an interval, which is where a
// 500 ms sweep and a 15 s scrape would otherwise start fighting over the window.
func TestALinkRateIsNotRecomputedOverASliverOfAnInterval(t *testing.T) {
	var w rateWindow
	t0 := time.Unix(1_700_000_000, 0)
	w.update(t0, 0, 0, 0)
	want, _, _ := w.update(t0.Add(time.Second), 125_000, 100, 0)
	got, _, ok := w.update(t0.Add(1100*time.Millisecond), 125_001, 100, 0)
	if !ok || got != want {
		t.Errorf("a reading 100 ms later reported %v kbps (ok=%v), want the held %v", got, ok, want)
	}
}

// A counter that goes backwards is a different connection, not negative
// traffic: unsigned subtraction would report it as an enormous bitrate.
func TestALinkRateSurvivesACounterThatWentBackwards(t *testing.T) {
	var w rateWindow
	t0 := time.Unix(1_700_000_000, 0)
	w.update(t0, 1_000_000, 1000, 0)
	kbps, loss, ok := w.update(t0.Add(2*time.Second), 10, 1, 0)
	if ok || kbps != 0 || loss != 0 {
		t.Errorf("a rolled-back counter reported %v kbps / %v%% (ok=%v); it should restart the window", kbps, loss, ok)
	}
}

// Link answers about one publisher, by source and role, and carries the rate.
// Over a real connection: a link that has been sending reports a bitrate.
func TestLinkReportsOnePublishersBitrateByRole(t *testing.T) {
	primary, backup, primSink, _ := rolePair(21)
	srv, addr := serve(t, primary, backup)

	if _, ok := srv.Link(21, false); ok {
		t.Fatal("Link answered for a source nobody is publishing")
	}
	c, err := dial(t, addr, tokenFor(primary))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	pkt := make([]byte, 1316)
	pkt[0] = 0x47
	stop := time.After(2500 * time.Millisecond)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	var l LinkStats
	ok := false
send:
	for {
		select {
		case <-stop:
			break send
		case <-tick.C:
			if _, err := c.Write(pkt); err != nil {
				t.Fatalf("write: %v", err)
			}
			if got, have := srv.Link(21, false); have {
				l, ok = got, true
			}
		}
	}
	if !ok {
		t.Fatal("Link never reported the publishing primary")
	}
	if l.Backup {
		t.Error("the primary's link reports itself as the backup")
	}
	if l.BitrateKbps <= 0 {
		t.Errorf("a link that has been sending ~2 Mbps for 2.5 s reports %v kbps (sink got %d bytes)",
			l.BitrateKbps, len(primSink.bytes()))
	}
	if _, ok := srv.Link(21, true); ok {
		t.Error("Link answered for the backup role, which nobody is publishing")
	}
}
