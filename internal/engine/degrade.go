// Degraded-link failover (#6): judging a primary that is still delivering, but
// delivering badly.
//
// Liveness answers "is the encoder sending anything". This answers the next
// question, "is what it sends fit to put on air", from three measurements:
// bitrate (from the primary hub's own byte counter, so every ingest mode has
// it), and RTT and loss (from the SRT protocol's statistics, so only an SRT or
// SRTLA-bonded primary has them). The answer is collapsed into ONE bit for the
// pure decision -- see sourceChoice.primaryDegraded -- and the measured detail
// is appended to the reason afterwards, so the golden table stays exhaustive.
package engine

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
)

// linkHealth is what the SRT layer can say about the primary's uplink. Absent
// (nil) for an RTMP or pull primary, where the protocol measures neither.
type linkHealth struct {
	rttMs   float64
	lossPct float64
}

// degradeThresholds is db.DegradedSettings resolved for one sweep. A zero
// threshold is OFF.
type degradeThresholds struct {
	minKbps    float64
	maxRTTMs   float64
	maxLossPct float64
	sustain    time.Duration
}

func thresholdsFor(d db.DegradedSettings) degradeThresholds {
	return degradeThresholds{
		minKbps:    float64(d.MinBitrateKbps),
		maxRTTMs:   float64(d.MaxRTTMs),
		maxLossPct: d.MaxLossPercent,
		sustain:    time.Duration(d.Sustain()) * time.Second,
	}
}

// LinkReading is the primary's last measurement, as status reports it. Each
// figure is a pointer because "not measured" -- no interval elapsed yet, or an
// RTMP primary that has no RTT -- must not read as zero, which is below every
// bitrate threshold and better than every RTT one.
type LinkReading struct {
	BitrateKbps *float64 `json:"bitrateKbps,omitempty"`
	RTTMs       *float64 `json:"rttMs,omitempty"`
	LossPercent *float64 `json:"lossPercent,omitempty"`
}

// degradeMonitor turns readings into the degraded bit, with a sustain window on
// the way in and the same window on the way out.
//
// THE WAY OUT HAS A WINDOW TOO, and that is a decision rather than symmetry for
// its own sake. chooseFrom's slate and playlist branches return to a primary
// the instant it is available, without consulting the return mode, because
// for a DEAD primary that is right: bytes arriving again is unambiguous. A
// degraded link is not: it hovers. Clearing on the first healthy reading would
// flap the broadcast slate -> primary -> slate on a link sitting at the
// threshold, each flap a visible cut.
type degradeMonitor struct {
	// The bitrate window: the counter and time at the last rate computation.
	rateAt   time.Time
	rateRx   uint64
	kbps     float64
	haveKbps bool

	// badSince is when the current unbroken breach began; goodSince is when the
	// current unbroken healthy run began while tripped.
	badSince  time.Time
	goodSince time.Time
	tripped   bool
	// detail is the measured explanation of the trip, e.g. "bitrate 640 kbps
	// below 800 kbps for 5s". Empty while healthy.
	detail string

	last LinkReading
}

// observe folds one reading in. It returns +1 on the sweep the link trips, -1
// on the sweep it clears, and 0 otherwise.
//
// alive is the primary's liveness. A primary that is not delivering is DEAD,
// which liveness already handles, so the monitor resets rather than carrying a
// stale trip to greet the encoder when it reconnects. That reset reports no
// edge: nothing recovered, the primary went away.
func (m *degradeMonitor) observe(th degradeThresholds, now time.Time, alive bool, rx uint64, link *linkHealth) int {
	if !alive {
		*m = degradeMonitor{}
		return 0
	}
	m.sampleRate(now, rx)

	m.last = LinkReading{}
	if m.haveKbps {
		k := m.kbps
		m.last.BitrateKbps = &k
	}
	if link != nil {
		r, l := link.rttMs, link.lossPct
		m.last.RTTMs, m.last.LossPercent = &r, &l
	}

	breach := m.breach(th, link)
	if breach != "" {
		m.goodSince = time.Time{}
		if m.badSince.IsZero() {
			m.badSince = now
		}
		if !m.tripped && now.Sub(m.badSince) >= th.sustain {
			m.tripped = true
			m.detail = breach + " for " + th.sustain.String()
			return +1
		}
		return 0
	}
	m.badSince = time.Time{}
	if !m.tripped {
		return 0
	}
	if m.goodSince.IsZero() {
		m.goodSince = now
	}
	if now.Sub(m.goodSince) >= th.sustain {
		m.tripped, m.detail, m.goodSince = false, "", time.Time{}
		return -1
	}
	return 0
}

// sampleRate updates the bitrate once at least a second has passed, so the
// 500 ms sweep does not difference half-intervals into a figure that jumps.
func (m *degradeMonitor) sampleRate(now time.Time, rx uint64) {
	if m.rateAt.IsZero() || rx < m.rateRx {
		m.rateAt, m.rateRx, m.haveKbps = now, rx, false
		return
	}
	dt := now.Sub(m.rateAt)
	if dt < time.Second {
		return
	}
	m.kbps = float64(rx-m.rateRx) * 8 / dt.Seconds() / 1000
	m.haveKbps = true
	m.rateAt, m.rateRx = now, rx
}

// breach names every threshold the current reading crosses, or "".
func (m *degradeMonitor) breach(th degradeThresholds, link *linkHealth) string {
	var out []string
	if th.minKbps > 0 && m.haveKbps && m.kbps < th.minKbps {
		out = append(out, fmt.Sprintf("bitrate %s kbps below %s kbps", num(m.kbps), num(th.minKbps)))
	}
	if link != nil && th.maxRTTMs > 0 && link.rttMs > th.maxRTTMs {
		out = append(out, fmt.Sprintf("RTT %s ms above %s ms", num(link.rttMs), num(th.maxRTTMs)))
	}
	if link != nil && th.maxLossPct > 0 && link.lossPct > th.maxLossPct {
		out = append(out, fmt.Sprintf("loss %s%% above %s%%", num(link.lossPct), num(th.maxLossPct)))
	}
	return strings.Join(out, ", ")
}

// num prints a measurement without float noise: whole numbers bare, anything
// else to one decimal place.
func num(v float64) string {
	if v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}
