package engine

import (
	"strings"
	"testing"
	"time"
)

// The degrade monitor is the half of #6 that turns measurements into the one
// bit chooseSource sees. These tests drive it with readings at chosen instants,
// because "sustained for N seconds" is a statement about time and a wall clock
// cannot produce the boundaries.

var t0 = time.Unix(1_700_000_000, 0)

// kbpsAt is the byte counter a link delivering at kbps would have reached
// after d.
func kbpsAt(kbps float64, d time.Duration) uint64 {
	return uint64(kbps * 1000 / 8 * d.Seconds())
}

func bitrateOnly(min int) degradeThresholds {
	return degradeThresholds{minKbps: float64(min), sustain: 5 * time.Second}
}

// link is a simulated primary: a byte counter and a clock that advance one
// second per reading, so consecutive runs at different bitrates join up
// exactly as a real encoder's counter would.
type simLink struct {
	at time.Time
	rx uint64
}

func newSimLink() *simLink { return &simLink{at: t0.Add(-time.Second)} }

// run feeds n one-second readings at kbps and returns the edges reported.
func (l *simLink) run(m *degradeMonitor, th degradeThresholds, kbps float64, n int, health *linkHealth) (edges []int) {
	for i := 0; i < n; i++ {
		l.at = l.at.Add(time.Second)
		l.rx += kbpsAt(kbps, time.Second)
		if e := m.observe(th, l.at, true, l.rx, health); e != 0 {
			edges = append(edges, e)
		}
	}
	return edges
}

func TestALowBitrateTripsOnlyOnceItHasLastedTheSustainWindow(t *testing.T) {
	var m degradeMonitor
	l := newSimLink()
	th := bitrateOnly(800)

	// Four seconds below threshold: a dip, not a degraded link.
	if edges := l.run(&m, th, 300, 4, nil); len(edges) != 0 || m.tripped {
		t.Fatalf("tripped after a 4 s dip with a 5 s sustain window (edges %v)", edges)
	}
	// Past five seconds of measured breach it trips, once.
	edges := l.run(&m, th, 300, 3, nil)
	if len(edges) != 1 || edges[0] != +1 || !m.tripped {
		t.Fatalf("edges %v tripped=%v, want exactly one trip", edges, m.tripped)
	}
	if !strings.Contains(m.detail, "bitrate 300 kbps below 800 kbps") {
		t.Errorf("detail %q does not say which threshold, what was measured and against what", m.detail)
	}
	if !strings.Contains(m.detail, "for 5s") {
		t.Errorf("detail %q does not say for how long", m.detail)
	}
}

// Hysteresis. A tripped link has to read healthy for the whole sustain window
// before it clears: the slate and playlist branches go back to the primary the
// instant it is available, so one good second would otherwise put a dying link
// back on air.
func TestATrippedLinkClearsOnlyAfterReadingHealthyForTheSustainWindow(t *testing.T) {
	var m degradeMonitor
	l := newSimLink()
	th := bitrateOnly(800)
	l.run(&m, th, 300, 12, nil)
	if !m.tripped {
		t.Fatal("setup: the link never tripped")
	}
	if edges := l.run(&m, th, 3000, 3, nil); len(edges) != 0 || !m.tripped {
		t.Fatalf("cleared after 3 healthy seconds (edges %v)", edges)
	}
	edges := l.run(&m, th, 3000, 4, nil)
	if len(edges) != 1 || edges[0] != -1 || m.tripped {
		t.Fatalf("edges %v tripped=%v, want exactly one clear", edges, m.tripped)
	}
	if m.detail != "" {
		t.Errorf("a cleared link still carries detail %q", m.detail)
	}
}

// A healthy reading in the middle of a breach restarts the trip clock, and a
// bad one in the middle of a recovery restarts the clear clock: "sustained"
// means unbroken.
func TestABreachMustBeUnbrokenToCount(t *testing.T) {
	var m degradeMonitor
	l := newSimLink()
	th := bitrateOnly(800)
	l.run(&m, th, 300, 3, nil)
	l.run(&m, th, 3000, 1, nil)
	if edges := l.run(&m, th, 300, 4, nil); len(edges) != 0 {
		t.Fatalf("tripped on 3 s + 4 s of breach separated by a good reading (edges %v)", edges)
	}
}

// RTT and loss come from SRT's own statistics. With no link report -- an RTMP
// or pull primary -- they are not measured, and an unmeasured figure must
// never trip and never be reported as zero.
func TestRTTAndLossThresholdsAreIgnoredWithoutAnSRTLink(t *testing.T) {
	var m degradeMonitor
	l := newSimLink()
	th := degradeThresholds{maxRTTMs: 100, maxLossPct: 1, sustain: 5 * time.Second}
	if edges := l.run(&m, th, 3000, 10, nil); len(edges) != 0 {
		t.Fatalf("an RTMP primary tripped on RTT/loss it cannot measure (edges %v)", edges)
	}
	if m.last.RTTMs != nil || m.last.LossPercent != nil {
		t.Errorf("an unmeasured RTT/loss was reported as %v/%v instead of absent", m.last.RTTMs, m.last.LossPercent)
	}
}

func TestHighRTTAndHighLossEachTripOnTheirOwn(t *testing.T) {
	cases := []struct {
		name string
		th   degradeThresholds
		link linkHealth
		want string
	}{
		{"rtt", degradeThresholds{maxRTTMs: 500, sustain: 5 * time.Second}, linkHealth{rttMs: 900}, "RTT 900 ms above 500 ms"},
		{"loss", degradeThresholds{maxLossPct: 5, sustain: 5 * time.Second}, linkHealth{lossPct: 12.5}, "loss 12.5% above 5%"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var m degradeMonitor
			l := newSimLink()
			link := tc.link
			l.run(&m, tc.th, 3000, 6, &link)
			if !m.tripped {
				t.Fatal("did not trip")
			}
			if !strings.Contains(m.detail, tc.want) {
				t.Errorf("detail %q, want it to contain %q", m.detail, tc.want)
			}
		})
	}
}

// A primary that stops delivering is DEAD, which liveness already handles. The
// monitor resets rather than holding a stale trip that would greet the encoder
// when it reconnects -- and a reconnect is measured afresh, not against the
// previous connection's counter.
func TestASilentPrimaryResetsTheMonitor(t *testing.T) {
	var m degradeMonitor
	l := newSimLink()
	th := bitrateOnly(800)
	l.run(&m, th, 300, 8, nil)
	if !m.tripped {
		t.Fatal("setup: never tripped")
	}
	m.observe(th, t0.Add(20*time.Second), false, 0, nil)
	if m.tripped || m.detail != "" || !m.badSince.IsZero() {
		t.Errorf("a silent primary left the monitor %+v", m)
	}
}

// Before a full second has been measured there is no bitrate, and "no figure
// yet" must not read as "zero kbps, below every threshold".
func TestNoBitrateIsNotZeroBitrate(t *testing.T) {
	var m degradeMonitor
	th := degradeThresholds{minKbps: 800, sustain: time.Second}
	for i := 0; i < 5; i++ {
		at := t0.Add(time.Duration(i) * 200 * time.Millisecond)
		if e := m.observe(th, at, true, 0, nil); e != 0 {
			t.Fatalf("tripped at %v before any interval had been measured", at.Sub(t0))
		}
	}
	if m.last.BitrateKbps != nil {
		t.Errorf("reported a bitrate of %v before measuring one", *m.last.BitrateKbps)
	}
}
