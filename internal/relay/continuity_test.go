package relay

import "testing"

// tsPkt describes one synthetic 188-byte transport packet. Building them by
// hand keeps the loss arithmetic testable without an encoder in the loop.
type tsPkt struct {
	pid       uint16
	cc        byte
	noPayload bool // adaptation field only, so the counter must not advance
	errored   bool // transport_error_indicator
	discont   bool // discontinuity_indicator in the adaptation field
}

func (p tsPkt) bytes() []byte {
	b := make([]byte, tsPacketSize)
	b[0] = tsSyncByte
	b[1] = byte(p.pid>>8) & 0x1F
	b[2] = byte(p.pid)
	if p.errored {
		b[1] |= 0x80
	}

	afc := byte(0x01)
	switch {
	case p.noPayload:
		afc = 0x02
		b[4] = tsPacketSize - 5
	case p.discont:
		afc = 0x03
		b[4] = 1
	}
	b[3] = afc<<4 | p.cc&0x0F
	if p.discont {
		b[5] = 0x80
	}
	return b
}

// datagram concatenates packets the way the ingest does: seven per datagram.
func datagram(pkts ...tsPkt) []byte {
	var out []byte
	for _, p := range pkts {
		out = append(out, p.bytes()...)
	}
	return out
}

const (
	videoPID = 0x100
	audioPID = 0x101
)

func TestContinuityCountsOnlyRealLoss(t *testing.T) {
	tests := []struct {
		name       string
		pkts       []tsPkt
		wantChecks uint64
		wantBreaks uint64
		wantLost   uint64
	}{
		{
			name:       "an in-order run of packets is clean",
			pkts:       []tsPkt{{pid: videoPID, cc: 0}, {pid: videoPID, cc: 1}, {pid: videoPID, cc: 2}},
			wantChecks: 3,
		},
		{
			name:       "the first packet of a PID sets the baseline rather than counting as loss",
			pkts:       []tsPkt{{pid: videoPID, cc: 9}},
			wantChecks: 1,
		},
		{
			name:       "one skipped counter is one break of one packet",
			pkts:       []tsPkt{{pid: videoPID, cc: 0}, {pid: videoPID, cc: 1}, {pid: videoPID, cc: 3}},
			wantChecks: 3,
			wantBreaks: 1,
			wantLost:   1,
		},
		{
			name:       "a jump of four counters reports three packets lost",
			pkts:       []tsPkt{{pid: videoPID, cc: 0}, {pid: videoPID, cc: 4}},
			wantChecks: 2,
			wantBreaks: 1,
			wantLost:   3,
		},
		{
			name:       "fourteen packets is the largest gap a four-bit counter can express",
			pkts:       []tsPkt{{pid: videoPID, cc: 1}, {pid: videoPID, cc: 0}},
			wantChecks: 2,
			wantBreaks: 1,
			wantLost:   14,
		},
		{
			name:       "the counter wrapping past fifteen is not loss",
			pkts:       []tsPkt{{pid: videoPID, cc: 14}, {pid: videoPID, cc: 15}, {pid: videoPID, cc: 0}, {pid: videoPID, cc: 1}},
			wantChecks: 4,
		},
		{
			name:       "a duplicated packet is allowed by the spec and is not loss",
			pkts:       []tsPkt{{pid: videoPID, cc: 0}, {pid: videoPID, cc: 1}, {pid: videoPID, cc: 1}, {pid: videoPID, cc: 2}},
			wantChecks: 4,
		},
		{
			name:       "loss straight after a duplicate is still caught",
			pkts:       []tsPkt{{pid: videoPID, cc: 0}, {pid: videoPID, cc: 0}, {pid: videoPID, cc: 2}},
			wantChecks: 3,
			wantBreaks: 1,
			wantLost:   1,
		},
		{
			name:       "an adaptation-field-only packet does not advance the counter",
			pkts:       []tsPkt{{pid: videoPID, cc: 0}, {pid: videoPID, cc: 7, noPayload: true}, {pid: videoPID, cc: 1}},
			wantChecks: 2,
		},
		{
			name:       "the null PID is ignored however its counter moves",
			pkts:       []tsPkt{{pid: tsNullPID, cc: 0}, {pid: tsNullPID, cc: 9}, {pid: tsNullPID, cc: 2}},
			wantChecks: 0,
		},
		{
			name:       "a packet flagged with a transport error is not evidence either way",
			pkts:       []tsPkt{{pid: videoPID, cc: 0}, {pid: videoPID, cc: 9, errored: true}, {pid: videoPID, cc: 1}},
			wantChecks: 2,
		},
		{
			name:       "a signalled discontinuity is not counted as loss",
			pkts:       []tsPkt{{pid: videoPID, cc: 0}, {pid: videoPID, cc: 5, discont: true}},
			wantChecks: 2,
		},
		{
			name:       "the stream resynchronises after a signalled discontinuity",
			pkts:       []tsPkt{{pid: videoPID, cc: 0}, {pid: videoPID, cc: 5, discont: true}, {pid: videoPID, cc: 6}},
			wantChecks: 3,
		},
		{
			name:       "PIDs are tracked independently",
			pkts:       []tsPkt{{pid: videoPID, cc: 0}, {pid: audioPID, cc: 6}, {pid: videoPID, cc: 1}, {pid: audioPID, cc: 7}},
			wantChecks: 4,
		},
		{
			name:       "loss on one PID does not implicate another",
			pkts:       []tsPkt{{pid: videoPID, cc: 0}, {pid: audioPID, cc: 0}, {pid: videoPID, cc: 2}, {pid: audioPID, cc: 1}},
			wantChecks: 4,
			wantBreaks: 1,
			wantLost:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c continuity

			checks, breaks, lost := c.inspect(datagram(tt.pkts...))

			if checks != tt.wantChecks || breaks != tt.wantBreaks || lost != tt.wantLost {
				t.Errorf("inspect() = %d checked, %d breaks, %d lost; want %d, %d, %d",
					checks, breaks, lost, tt.wantChecks, tt.wantBreaks, tt.wantLost)
			}
		})
	}
}

// State has to survive datagram boundaries or every seventh packet would look
// like a fresh PID.
func TestContinuityCarriesStateAcrossDatagrams(t *testing.T) {
	var c continuity

	if _, breaks, lost := c.inspect(datagram(tsPkt{pid: videoPID, cc: 3})); breaks != 0 || lost != 0 {
		t.Fatalf("first datagram = %d breaks, %d lost; want a clean baseline", breaks, lost)
	}

	_, breaks, lost := c.inspect(datagram(tsPkt{pid: videoPID, cc: 6}))

	if breaks != 1 || lost != 2 {
		t.Errorf("second datagram = %d breaks, %d lost; want 1, 2", breaks, lost)
	}
}

func TestContinuityIgnoresPayloadsThatAreNotTransportStream(t *testing.T) {
	tests := []struct {
		name  string
		dgram []byte
	}{
		{name: "empty datagram", dgram: nil},
		{name: "shorter than one TS packet", dgram: make([]byte, 100)},
		{name: "right length but no sync byte", dgram: make([]byte, tsPacketSize)},
		{name: "sync byte in the wrong place", dgram: append([]byte{0x00, tsSyncByte}, make([]byte, tsPacketSize)...)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c continuity

			checks, breaks, lost := c.inspect(tt.dgram)

			if checks != 0 || breaks != 0 || lost != 0 {
				t.Errorf("inspect() = %d checked, %d breaks, %d lost; want all zero", checks, breaks, lost)
			}
		})
	}
}

// A datagram that goes out of sync part way through cannot be re-aligned by
// guessing, so the packets before the break still count and the rest do not.
func TestContinuityStopsAtLostPacketAlignment(t *testing.T) {
	var c continuity
	good := datagram(tsPkt{pid: videoPID, cc: 0}, tsPkt{pid: videoPID, cc: 1})
	garbage := make([]byte, tsPacketSize)
	trailing := datagram(tsPkt{pid: videoPID, cc: 2})

	checks, breaks, lost := c.inspect(append(append(good, garbage...), trailing...))

	if checks != 2 || breaks != 0 || lost != 0 {
		t.Errorf("inspect() = %d checked, %d breaks, %d lost; want 2, 0, 0", checks, breaks, lost)
	}
}

// A datagram whose length is not a whole number of TS packets must not read off
// the end of the buffer.
func TestContinuityIgnoresATrailingPartialPacket(t *testing.T) {
	var c continuity
	dgram := append(datagram(tsPkt{pid: videoPID, cc: 0}), tsSyncByte, 0x00, 0x00, 0x11)

	checks, breaks, lost := c.inspect(dgram)

	if checks != 1 || breaks != 0 || lost != 0 {
		t.Errorf("inspect() = %d checked, %d breaks, %d lost; want 1, 0, 0", checks, breaks, lost)
	}
}

// ------------------------------------------------------------------ hub stats

func TestStatsReportsContinuityLossFromTheWire(t *testing.T) {
	h := newTestHub(t)
	clean := datagram(tsPkt{pid: videoPID, cc: 0}, tsPkt{pid: videoPID, cc: 1})
	afterALoss := datagram(tsPkt{pid: videoPID, cc: 4}, tsPkt{pid: videoPID, cc: 5})

	publish(t, h, clean, 1)
	waitForRx(t, h, 1)
	publish(t, h, afterALoss, 1)
	// Four TS packets across two datagrams -- waiting on the datagram count
	// would race the second one's measurement.
	waitForTS(t, h, 4)

	s := h.Stats()
	if s.TSPackets != 4 {
		t.Errorf("TSPackets = %d, want 4", s.TSPackets)
	}
	if s.Discontinuities != 1 {
		t.Errorf("Discontinuities = %d, want 1", s.Discontinuities)
	}
	if s.TSLost != 2 {
		t.Errorf("TSLost = %d, want 2 (counters 2 and 3 never arrived)", s.TSLost)
	}
	if want := float64(100*2) / float64(6); s.LossPercent != want {
		t.Errorf("LossPercent = %v, want %v (2 lost of 6 sent)", s.LossPercent, want)
	}
	if s.Dropped != 0 {
		t.Errorf("Dropped = %d, want 0: wire loss is not a send failure", s.Dropped)
	}
}

func TestStatsReportsNoLossForACleanStream(t *testing.T) {
	h := newTestHub(t)

	for cc := byte(0); cc < 16; cc++ {
		publish(t, h, datagram(tsPkt{pid: videoPID, cc: cc}, tsPkt{pid: audioPID, cc: cc}), 1)
	}
	// 16 datagrams x 2 TS packets.
	waitForTS(t, h, 32)

	s := h.Stats()
	if s.TSPackets != 32 || s.TSLost != 0 || s.Discontinuities != 0 || s.LossPercent != 0 {
		t.Errorf("clean stream stats = %d checked, %d lost, %d breaks, %v%%; want 32, 0, 0, 0",
			s.TSPackets, s.TSLost, s.Discontinuities, s.LossPercent)
	}
}

// Non-TS traffic must leave the loss figures at zero rather than inventing a
// number the monitoring page would show as 100% loss.
func TestStatsLeavesLossZeroForNonTSTraffic(t *testing.T) {
	h := newTestHub(t)

	publish(t, h, []byte("this is not a transport stream"), 3)
	waitForRx(t, h, 3)

	if s := h.Stats(); s.TSPackets != 0 || s.TSLost != 0 || s.LossPercent != 0 {
		t.Errorf("stats = %d checked, %d lost, %v%%; want all zero", s.TSPackets, s.TSLost, s.LossPercent)
	}
}

// The measurement sits on the hot path, so its cost is a design constraint:
// a full datagram must stay far cheaper than the syscall that delivered it.
func BenchmarkContinuityInspectDatagram(b *testing.B) {
	var c continuity
	dgram := datagram(
		tsPkt{pid: videoPID, cc: 0}, tsPkt{pid: videoPID, cc: 1}, tsPkt{pid: audioPID, cc: 0},
		tsPkt{pid: videoPID, cc: 2}, tsPkt{pid: videoPID, cc: 3}, tsPkt{pid: audioPID, cc: 1},
		tsPkt{pid: tsNullPID, cc: 0},
	)

	b.SetBytes(int64(len(dgram)))
	for i := 0; i < b.N; i++ {
		c.inspect(dgram)
	}
}
