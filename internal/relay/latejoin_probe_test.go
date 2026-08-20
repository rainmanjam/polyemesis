package relay

import (
	"bytes"
	"net"
	"testing"
	"time"
)

/* A SUBSCRIBER THAT JOINS MID-STREAM STARTS RECEIVING AT ONCE.
 *
 * #460 said the hub "hands a late subscriber a stream it cannot start decoding
 * -- no SPS/PPS until the next keyframe", and that claim is now measured and
 * REFUTED at both ends:
 *
 *   the encoder side  the ingest's remux (-c copy -f mpegts, exactly
 *                     IngestArgs) puts an SPS in-band before EVERY IDR --
 *                     6/6 keyframes, including across a mid-stream resolution
 *                     change, which carries both geometries' parameter sets.
 *
 *                     ON EVERY BINARY THE PRODUCT ACTUALLY RUNS, because the
 *                     first pass used whatever was on PATH and that is not the
 *                     same thing. BtbN n8.1.2-44 (the exact artefact ci.yml
 *                     pins), Alpine 8.1.2 (what the released container gets
 *                     from the Dockerfile's FFMPEG_VERSION), BtbN n8.1.2-34 (a
 *                     deploy box), and 9.0.1 (a laptop). The pinned CI build
 *                     and the box build were TEN COMMITS APART, which is
 *                     exactly the gap that makes "I tested 8.1" not mean much
 *                     on its own -- BtbN's `latest` tag is rolling, as ci.yml
 *                     says in its own comment.
 *
 *   the fanout        driving this Hub with that stream and subscribing part
 *                     way through, the late consumer held a parameter set
 *                     853ms after joining. One GOP is 2s. It was never
 *                     starved.
 *
 * That measurement needed a real transport stream and a real encoder, so it is
 * recorded here rather than run here. What IS run here is the property the hub
 * itself owns and could regress: a subscriber registered while traffic is
 * already flowing gets the very next datagram, with nothing withheld and
 * nothing replayed.
 *
 * Byte-agnostic on purpose. The hub parses nothing -- that is its design -- so
 * a synthetic pattern tests the delivery property exactly as well as H.264
 * would, and it does it without an encoder, without a fixture file, and without
 * a skip. The marker stands where a parameter set would.
 */
func TestASubscriberJoiningMidStreamStartsReceivingImmediately(t *testing.T) {
	h, err := New(testLogger(), 0)
	if err != nil {
		t.Fatalf("hub: %v", err)
	}
	defer h.Close()

	in, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: h.Port()})
	if err != nil {
		t.Fatalf("dial hub: %v", err)
	}
	defer in.Close()

	// 188*7 is the datagram the MPEG-TS muxer emits over UDP; the marker sits
	// at the head of every tenth one, standing in for a keyframe's parameter
	// set at a GOP boundary.
	const dgram, every = 188 * 7, 10
	marker := []byte{0x00, 0x00, 0x01, 0x67}

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		pkt := make([]byte, dgram)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			for j := range pkt {
				pkt[j] = byte(i)
			}
			if i%every == 0 {
				copy(pkt, marker)
			}
			if _, err := in.Write(pkt); err != nil {
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()

	// Let the stream run first. Subscribing before any traffic would test the
	// easy case and is not what #460 is about.
	time.Sleep(150 * time.Millisecond)

	sub, port := boundSubscriber(t)
	h.Subscribe("late", port)
	joined := time.Now()

	var total int
	var sawMarker bool
	buf := make([]byte, 64<<10)
	_ = sub.SetReadDeadline(time.Now().Add(3 * time.Second))
	for !sawMarker {
		n, err := sub.Read(buf)
		if err != nil {
			break
		}
		total += n
		if bytes.HasPrefix(buf[:n], marker) {
			sawMarker = true
		}
	}
	waited := time.Since(joined)

	if total == 0 {
		t.Fatal("a subscriber that joined mid-stream received NOTHING. The fanout " +
			"is not reaching it at all, which is worse than a slow start: every " +
			"destination added to a running programme would be dead on arrival")
	}
	if !sawMarker {
		t.Fatalf("a late subscriber received %d bytes and never a marker. Standing "+
			"where a parameter set stands, that is a consumer which cannot begin "+
			"decoding no matter how long it waits -- #460 as written", total)
	}
	// One marker interval is 10 datagrams at 2ms. A second is two orders of
	// magnitude beyond that, so this fails on a real stall rather than on a
	// loaded machine.
	if waited > time.Second {
		t.Errorf("a late subscriber waited %v for its first marker, which is far "+
			"longer than the %v interval between them", waited.Round(time.Millisecond),
			every*2*time.Millisecond)
	}
	t.Logf("late subscriber reached its first marker in %v, after %d bytes",
		waited.Round(time.Millisecond), total)
}
