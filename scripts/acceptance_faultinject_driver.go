//go:build ignore

// Break a healthy broadcast on purpose, and see whether it comes back.
//
// #380 names three gaps. This is the MID-STREAM FAILURE one: "acceptance-failover
// covers a switch; nothing covers a platform refusing at minute 30 of a healthy
// broadcast, a network drop, or the encoder dying mid-stream."
//
// The distinction is the whole point. Failover is a DECISION the engine makes
// when a source is absent at the moment it looks. This is a fault that ARRIVES
// into something already working -- everything probed, every process up, bytes
// moving -- which is the state a real broadcast is in for all but its first few
// seconds, and the state nothing here has ever tested.
//
// THE TRAP THIS SUITE EXISTS TO AVOID is the exact mirror of the concurrency
// driver's. That one learned that a per-destination cost halves beautifully when
// the processes are dead, so it asserts liveness before it reports a number.
// Here the lie runs the other way: a recovery suite whose fault DID NOT LAND
// reports a perfect recovery, at length, with every check green. Nothing was
// broken, so of course nothing stayed broken.
//
// It is not a hypothetical. Killing "the destination's ffmpeg" by name matches
// the acceptance source publisher too on a box where both are running; killing
// a listener the destination has not connected to yet breaks nothing; and a
// supervisor that respawns in 200ms hides a fault entirely from a sampler that
// looks once a second. Each of those produces a green run against a system that
// was never tested.
//
// So every injection here is a PAIR:
//
//  1. the fault, and a POSITIVE CONTROL that proves it landed -- an observable
//     that MUST change. A restart counter that moved, a byte counter that
//     stalled, a process that went away. If the control does not fire, the run
//     reports THE FAULT DID NOT LAND and fails. It never reports recovery.
//  2. the recovery, asserted on DELIVERY rather than on state. "running" is
//     what a destination subscribed to nothing also says.
//
// WHAT IS DELIBERATELY NOT ASSERTED: how long recovery takes. Backoff is
// deliberate, its ceiling is thirty seconds, and a suite that pinned a duration
// would fail on a slow runner rather than on a regression. What is stable is
// that it happens at all, and that the system SAW it -- an outage nothing
// counted is one nobody can be alerted to.
package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rainmanjam/polyemesis/scripts/internal/driverlib"
)

// sinkConnects counts TCP connections the controllable endpoint has accepted.
//
// THE ONLY UNFAKEABLE PROOF THAT THE DESTINATION RECONNECTED. The first version
// of this driver asserted recovery on the destination being "running", and a
// mutation that never restored the endpoint PASSED: the supervisor respawns
// FFmpeg, FFmpeg fails to connect and dies, and the sampler catches it in the
// window where it is up. A crash loop reports "running" about as often as a
// healthy process does.
//
// That is the failure this driver's own header warns about -- "running is what
// a destination subscribed to nothing also says" -- reproduced by the author in
// the same file. The sink counting its own accepts is the fix: a connection
// arriving is the destination having found the endpoint, and nothing else
// produces one.
var sinkConnects atomic.Int64

var (
	factsFile string
	facts     = map[string]string{}
	ffmpegBin string
	sourceID  int64
)

func die(format string, a ...any) {
	facts["DRIVER_FAILED"] = fmt.Sprintf(format, a...)
	writeFacts()
	fmt.Fprintf(os.Stderr, "  driver: "+format+"\n", a...)
	os.Exit(1)
}

func writeFacts() {
	if factsFile == "" {
		return
	}
	keys := make([]string, 0, len(facts))
	for k := range facts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%q\n", k, facts[k])
	}
	_ = os.WriteFile(factsFile, []byte(b.String()), 0o644)
}

// ---------------------------------------------------------------- status

type statusDoc struct {
	Ingest *struct {
		State    string `json:"state"`
		Restarts int    `json:"restarts"`
	} `json:"ingest,omitempty"`
	Relay struct {
		RxBytes uint64 `json:"rxBytes"`
	} `json:"relay"`
	Destinations []struct {
		Name    string `json:"name"`
		Process *struct {
			State    string `json:"state"`
			Restarts int    `json:"restarts"`
			PID      int    `json:"pid"`
		} `json:"process,omitempty"`
	} `json:"destinations"`
}

type snapshot struct {
	at       time.Time
	rxBytes  int64
	restarts int
	up       int
	pid      int
}

func observe() snapshot {
	var st statusDoc
	driverlib.GetJSON("/status", "status", &st)
	s := snapshot{at: time.Now(), rxBytes: int64(st.Relay.RxBytes)}
	if st.Ingest != nil {
		s.restarts += st.Ingest.Restarts
	}
	for _, d := range st.Destinations {
		if d.Process == nil {
			continue
		}
		s.restarts += d.Process.Restarts
		if d.Process.State == "running" {
			s.up++
		}
		if s.pid == 0 {
			s.pid = d.Process.PID
		}
	}
	return s
}

// waitFor polls until cond holds or the budget runs out. Returns whether it
// held, so a caller can report a timeout as a finding rather than dying.
func waitFor(what string, budget time.Duration, cond func(snapshot) bool) bool {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if cond(observe()) {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	fmt.Printf("    timed out after %s waiting for %s\n", budget, what)
	return false
}

// delivering reports whether the relay's byte counter advanced across a window.
//
// THE ONLY HONEST RECOVERY SIGNAL. A destination reports "running" the instant
// its process starts, before it has subscribed to anything, and a destination
// subscribed to nothing reports "running" for ever. Bytes moving is the claim
// that matters and it is the one a green card cannot fake.
func delivering(window time.Duration) bool {
	before := observe()
	time.Sleep(window)
	return observe().rxBytes > before.rxBytes
}

func main() {
	if len(os.Args) < 4 {
		die("usage: acceptance_faultinject_driver.go <http-port> <relay-port> <facts-file>")
	}
	port, relay := os.Args[1], os.Args[2]
	factsFile = os.Args[3]

	ffmpegBin = toolPath("ffmpeg")

	driverlib.Init("http://127.0.0.1:" + port)
	defer writeFacts()
	driverlib.WaitUp()
	driverlib.Setup("admin", "acceptance-pw")
	sourceID = driverlib.EnsureSource("Main")

	// Recording and meters off: both spawn FFmpeg of their own, and a restart
	// counter that this suite reads as "the fault landed" must not be moved by
	// something unrelated to the fault.
	settings := driverlib.LoadSettings()
	if rec, ok := settings["recording"].(map[string]any); ok {
		rec["enabled"] = false
	}
	if m, ok := settings["meters"].(map[string]any); ok {
		m["enabled"] = false
	}
	driverlib.SaveSettings(settings, "recording and meters off")

	relayPort, err := driverlib.ResolveRelayPort(relay, func(p string) map[string]any {
		var doc map[string]any
		driverlib.GetJSON(p, "status", &doc)
		return doc
	})
	if err != nil {
		die("resolve relay port: %v", err)
	}

	// A SINK WE CONTROL, so "the platform refused" can be made to happen on
	// purpose. A real endpoint cannot be told to go away at a chosen moment,
	// and pointing at one that is already down tests the wrong thing entirely:
	// a destination that never connected is not a destination that lost its
	// connection.
	sink, sinkPort := listenTCP()
	fmt.Printf("controllable sink listening on 127.0.0.1:%d\n", sinkPort)

	src := exec.Command(ffmpegBin, "-hide_banner", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-map", "0:v", "-map", "1:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-g", "60", "-b:v", "3000k", "-c:a", "aac", "-b:a", "128k",
		"-metadata", "comment=acceptance-faults-source", "-t", "600",
		"-f", "mpegts", "-flush_packets", "1",
		fmt.Sprintf("udp://127.0.0.1:%d?pkt_size=1316", relayPort))
	if err := src.Start(); err != nil {
		die("start source: %v", err)
	}
	defer func() {
		_ = src.Process.Kill()
		_ = src.Wait()
	}()

	driverlib.CreateDest("fault-target", map[string]any{
		"sourceId": sourceID,
		"name":     "fault-target", "kind": "rtmp", "platform": "custom",
		"url":       fmt.Sprintf("rtmp://127.0.0.1:%d/live", sinkPort),
		"streamKey": "k", "enabled": true, "audioBitrate": 160,
		"profile": map[string]any{
			"mode": "simple", "tracks": driverlib.Sel(0),
			"normalize": "auto", "sampleRate": 48000,
		},
	})

	// ------------------------------------------------- steady state FIRST
	//
	// Everything below is a claim about a HEALTHY broadcast being broken. If it
	// was not healthy, the run has nothing to say and must not pretend
	// otherwise -- which is the one failure this suite is built around.
	fmt.Println("\nestablishing steady state")
	if !waitFor("the destination to come up", 60*time.Second, func(s snapshot) bool {
		return s.up >= 1
	}) {
		die("the destination never came up, so there is no healthy broadcast to break")
	}
	if !delivering(6 * time.Second) {
		die("the relay took no bytes before any fault was injected; every recovery " +
			"check below would be measuring a broadcast that was never working")
	}
	base := observe()
	facts["FAULT_BASE_RESTARTS"] = strconv.Itoa(base.restarts)
	fmt.Printf("  healthy: %d destination(s) up, relay advancing, %d restart(s) so far\n",
		base.up, base.restarts)

	// ------------------------------------------------- 1. the endpoint goes
	//
	// A platform refusing at minute 30. The sink closes; the destination's
	// FFmpeg loses its connection and exits; the supervisor is what has to
	// notice and bring it back.
	fmt.Println("\ninjecting: the destination's endpoint disappears")
	_ = sink.Close()

	landed := waitFor("the supervisor to notice the endpoint had gone", 90*time.Second,
		func(s snapshot) bool { return s.restarts > base.restarts })
	facts["FAULT_ENDPOINT_LANDED"] = strconv.FormatBool(landed)
	if !landed {
		// REPORTED AS A NON-EVENT, NOT AS A RECOVERY. The whole suite turns on
		// this distinction: nothing was broken, so "it survived" would be a
		// green run against a system that was never tested.
		die("closing the endpoint changed no restart counter within 90s, so the fault " +
			"never reached the product. Every recovery claim below would be vacuous. " +
			"Check that the destination had actually connected to the sink")
	}
	after := observe()
	fmt.Printf("  landed: restarts %d -> %d\n", base.restarts, after.restarts)

	// The sink comes back, and the destination has to find it again. This is
	// the RECOVERY half: a supervisor that gives up, or backs off past the
	// window, leaves a broadcast that never returns without anything crashing.
	sink2, err := listenTCPOn(sinkPort)
	if err != nil {
		die("could not reopen the sink on %d: %v", sinkPort, err)
	}
	defer sink2.Close()
	fmt.Println("  endpoint restored; waiting for the destination to reconnect")

	// ASSERTED ON THE SINK'S OWN ACCEPT COUNT, not on process state. See
	// sinkConnects: a crash-looping destination reports "running" too.
	connectsBefore := sinkConnects.Load()
	back := waitForConnect(connectsBefore, 120*time.Second)
	facts["FAULT_ENDPOINT_RECOVERED"] = strconv.FormatBool(back)
	facts["FAULT_SINK_CONNECTS"] = strconv.FormatInt(sinkConnects.Load(), 10)
	if back {
		fmt.Printf("  recovered: the destination reconnected to the endpoint (%d accepts)\n",
			sinkConnects.Load())
	}

	// ------------------------------------------------- 2. the ingest drops
	//
	// A network drop on the publisher's side. Distinct from failover: there is
	// no second source to switch to, so what is being tested is that the
	// broadcast RESUMES when the bytes come back rather than needing a restart.
	fmt.Println("\ninjecting: the publisher stops mid-stream")
	beforeDrop := observe()
	_ = src.Process.Kill()
	_ = src.Wait()

	stalled := waitFor("the relay to stop advancing", 30*time.Second, func(s snapshot) bool {
		return s.rxBytes == beforeDrop.rxBytes || !delivering(3*time.Second)
	})
	facts["FAULT_INGEST_LANDED"] = strconv.FormatBool(stalled)
	if !stalled {
		die("the publisher was killed and the relay kept advancing, so something else "+
			"is still publishing to udp/%d. The fault did not land", relayPort)
	}
	fmt.Println("  landed: the relay stopped advancing")

	src2 := exec.Command(ffmpegBin, "-hide_banner", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-map", "0:v", "-map", "1:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-g", "60", "-b:v", "3000k", "-c:a", "aac", "-b:a", "128k",
		"-metadata", "comment=acceptance-faults-source", "-t", "300",
		"-f", "mpegts", "-flush_packets", "1",
		fmt.Sprintf("udp://127.0.0.1:%d?pkt_size=1316", relayPort))
	if err := src2.Start(); err != nil {
		die("restart source: %v", err)
	}
	defer func() {
		_ = src2.Process.Kill()
		_ = src2.Wait()
	}()
	fmt.Println("  publisher restored; waiting for delivery to resume")

	resumed := false
	for i := 0; i < 12 && !resumed; i++ {
		resumed = delivering(5 * time.Second)
	}
	facts["FAULT_INGEST_RECOVERED"] = strconv.FormatBool(resumed)
	if resumed {
		fmt.Println("  recovered: the relay is advancing again")
	}

	final := observe()
	facts["FAULT_FINAL_RESTARTS"] = strconv.Itoa(final.restarts)
	facts["FAULT_FINAL_UP"] = strconv.Itoa(final.up)
	facts["FAULT_DELIVERING_AT_END"] = strconv.FormatBool(delivering(6 * time.Second))
}

// listenTCP opens a socket on an ephemeral port and returns it with its number.
func listenTCP() (net.Listener, int) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		die("listen: %v", err)
	}
	go acceptForever(l)
	return l, l.Addr().(*net.TCPAddr).Port
}

func listenTCPOn(port int) (net.Listener, error) {
	// A CLOSED LISTENER'S PORT IS NOT INSTANTLY REBINDABLE, so this retries
	// rather than failing the run for a TIME_WAIT that clears on its own. The
	// concurrency and duration suites hit the same thing with UDP ports and
	// solved it the same way; see testenv.ReleaseAndSettle.
	var lastErr error
	for i := 0; i < 40; i++ {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			go acceptForever(l)
			return l, nil
		}
		lastErr = err
		time.Sleep(250 * time.Millisecond)
	}
	return nil, lastErr
}

// acceptForever drains connections so an RTMP client's TCP connect succeeds.
//
// It never speaks RTMP, and does not need to: what this suite injects is the
// endpoint GOING AWAY, and the destination's FFmpeg exits on a failed handshake
// exactly as it does on a refused connection. The supervisor's response -- the
// thing under test -- is identical either way.
func acceptForever(l net.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		sinkConnects.Add(1)
		go func() {
			buf := make([]byte, 4096)
			for {
				if _, err := c.Read(buf); err != nil {
					_ = c.Close()
					return
				}
			}
		}()
	}
}

// waitForConnect polls the sink's accept counter, which only moves when the
// destination has actually reached the endpoint.
func waitForConnect(was int64, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if sinkConnects.Load() > was {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	fmt.Printf("    timed out after %s waiting for the destination to reconnect "+
		"(sink accepted %d connection(s), unchanged)\n", budget, sinkConnects.Load())
	return false
}

func toolPath(name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		die("%s not on PATH", name)
	}
	return p
}
