// Command faketool stands in for BOTH ffmpeg and ffprobe in the API tests.
//
// It exists because of what its absence hid. Every credential-disclosure guard
// in internal/api ran against a fixture whose Tools pointed at
// /nonexistent/ffmpeg, so no destination child ever existed, so three separate
// egresses that render a running process -- GET /processes, GET
// /processes/{name}/logs and the /ws log stream -- were each excused from the
// sweep with the words "needs a running child process". Three excuses, and a
// live stream key travelling through all three. A fixture that cannot start a
// process cannot observe what a running process discloses, and an excuse that
// nothing can ever discharge is a permanent blind spot wearing a reason.
//
// It is a real compiled binary, built by the test into t.TempDir() and never
// found on PATH, so the tests stay hermetic: nothing here depends on FFmpeg
// being installed, on its version, or on anything outside the module.
//
// It lives under testdata/ so `go build ./...`, `go vet ./...` and the package
// list ignore it; the test compiles it explicitly.
//
// Two behaviours, chosen from the argv:
//
//   - ffprobe: `-show_streams` present. Prints one video and one stereo audio
//     stream as ffprobe's JSON and exits 0, which is what makes the engine's
//     probe SUCCEED, sets measured, and lifts the hold that otherwise keeps
//     every destination from starting at all.
//   - ffmpeg: anything else. Echoes its own command line to stderr the way
//     FFmpeg does -- which is the point, because that is how a credential
//     spliced into an argv reaches the log ring, process.log and the WebSocket
//     -- then pumps datagrams at the udp:// target in its argv so the relay
//     hub's RxBytes advances and the probe loop has something to probe. Runs
//     until it is killed.
package main

import (
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

func main() {
	argv := os.Args[1:]
	for _, a := range argv {
		if a == "-show_streams" {
			probe()
			return
		}
	}
	transcode(argv)
}

// probe prints a layout the engine can measure: one h264 video track and one
// stereo AAC track. Deliberately ordinary -- the point is that the probe
// succeeds, not that it describes anything unusual.
func probe() {
	fmt.Print(`{"streams":[` +
		`{"codec_name":"h264","codec_type":"video","width":1920,"height":1080,` +
		`"pix_fmt":"yuv420p","avg_frame_rate":"30/1","bit_rate":"4000000"},` +
		`{"codec_name":"aac","codec_type":"audio","channels":2,` +
		`"channel_layout":"stereo","sample_rate":"48000","bit_rate":"128000"}` +
		`]}`)
}

func transcode(argv []string) {
	// FFmpeg's banner, and the reason this test binary matters: the real one
	// prints the arguments it was given, so a stream key spliced into an argv
	// is ALSO a stream key in stderr, in the log ring, in process.log, and in
	// every /ws log frame. Reproducing that is the whole job.
	fmt.Fprintln(os.Stderr, "faketool version 0.0-test")
	fmt.Fprintln(os.Stderr, "  configuration: "+strings.Join(argv, " "))
	// A second line in the shape of the one that actually leaks in production:
	// FFmpeg reports the output it failed to open, credentials and all.
	for _, a := range argv {
		if strings.Contains(a, "://") {
			fmt.Fprintf(os.Stderr, "[out#0] Opening '%s' for writing\n", a)
		}
	}

	conns := dialUDPTargets(argv)
	// No udp target: still a long-lived process with stderr on the record,
	// which is what the log egresses need. Sleeping rather than exiting keeps
	// the supervisor out of its restart loop.
	payload := make([]byte, 1316)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for range tick.C {
		for _, c := range conns {
			_, _ = c.Write(payload)
		}
	}
}

// dialUDPTargets opens a socket to every udp:// element of the argv.
//
// This is what makes the fixture LIVE rather than merely spawned: the engine
// probes only while the relay hub is receiving bytes, so without real datagrams
// the layout is never measured and destinations are held down for ever.
func dialUDPTargets(argv []string) []net.Conn {
	var out []net.Conn
	for _, a := range argv {
		if !strings.HasPrefix(a, "udp://") {
			continue
		}
		addr := strings.TrimPrefix(a, "udp://")
		if i := strings.IndexAny(addr, "?/"); i >= 0 {
			addr = addr[:i]
		}
		c, err := net.Dial("udp", addr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "faketool: cannot dial %s: %v\n", addr, err)
			continue
		}
		out = append(out, c)
	}
	return out
}
