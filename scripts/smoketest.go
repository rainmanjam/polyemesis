// Backend smoke test: drives the real HTTP API, pushes a synthetic 3-track
// SRT stream, and verifies each file destination received exactly its selected
// mix by measuring per-frequency energy in the output.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	srt "github.com/datarhei/gosrt"
)

// A var rather than a const so smoketest_stop_test.go can point the stop poll
// at an httptest server. Nothing in main() reassigns it.
var base = "http://127.0.0.1:8099/api/v1"

var client *http.Client
var csrf string

func main() {
	jar, _ := cookiejar.New(nil)
	client = &http.Client{Jar: jar, Timeout: 30 * time.Second}

	step("waiting for server")
	waitUp()

	step("first-run setup")
	do("POST", "/setup", map[string]any{"username": "admin", "password": "hunter2hunter2"})
	grabCSRF()

	step("quiet the recorder and the preview")
	settings := doGet("/settings")
	ing := settings["ingest"].(map[string]any)
	srt := ing["srt"].(map[string]any)
	// No srt.port here on purpose: with one-port ingest the SRT listener's
	// port is process-wide and a source is addressed by its publish token, so
	// SRTSettings carries no port at all. Writing one is now a 400.
	srt["latencyMs"] = 120
	rec := settings["recording"].(map[string]any)
	rec["enabled"] = false
	prev := settings["preview"].(map[string]any)
	prev["enabled"] = false
	do("PUT", "/settings", settings)

	// The stream is injected straight into the relay hub rather than through
	// the SRT listener, which is what makes this runnable on any FFmpeg
	// whatever its protocol list -- Homebrew's has no libsrt, and neither
	// build is guaranteed on a CI runner. Everything downstream of the ingest
	// -- relay fan-out, layout probing, routing compilation, destination
	// muxing and the audio measurement -- is exercised identically; only the
	// `-c copy` SRT hop is substituted.
	//
	// So this proves the broadcast path. It does NOT prove SRT ingest.
	relayPort := int(doGet("/stats")["relay"].(map[string]any)["port"].(float64))
	fmt.Printf("     relay hub listening on udp://127.0.0.1:%d\n", relayPort)

	step("push synthetic 3-track MPEG-TS stream (300 / 900 / 2000 Hz)")
	src := exec.Command(ffmpegPath(), "-hide_banner", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=300:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=900:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=2000:sample_rate=48000",
		"-map", "0:v", "-map", "1:a", "-map", "2:a", "-map", "3:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-g", "30", "-b:v", "800k",
		"-c:a", "aac", "-b:a", "128k",
		"-t", "30",
		"-map", "0", "-f", "mpegts", "-flush_packets", "1",
		fmt.Sprintf("udp://127.0.0.1:%d?pkt_size=1316", relayPort))
	src.Stderr = os.Stderr
	if err := src.Start(); err != nil {
		fail("start synthetic source: %v", err)
	}

	step("waiting for the engine to probe the ingest layout")
	probeDeadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(probeDeadline) {
		time.Sleep(1500 * time.Millisecond)
		si := doGet("/source")
		tr, _ := si["tracks"].([]any)
		fmt.Printf("     probed=%v tracks=%d\n", si["probed"], len(tr))
		if si["probed"] == true && len(tr) == 3 {
			break
		}
	}
	si := doGet("/source")
	if si["probed"] != true {
		fail("engine never probed the ingest layout")
	}
	if tr, _ := si["tracks"].([]any); len(tr) != 3 {
		fail("probe found %d audio tracks, want 3", len(tr))
	}
	fmt.Println("     layout probed correctly: 3 audio tracks")

	step("create destination A: tracks 1+2")
	do("POST", "/destinations", destBody("A-tracks-1-2", "a.mkv", []int{0, 1}))
	step("create destination B: tracks 1+3")
	do("POST", "/destinations", destBody("B-tracks-1-3", "b.mkv", []int{0, 2}))

	step("waiting for destinations to start")
	deadline := time.Now().Add(25 * time.Second)
	running := 0
	for time.Now().Before(deadline) {
		time.Sleep(1500 * time.Millisecond)
		st := doGet("/status")
		srcInfo := st["source"].(map[string]any)
		tracks, _ := srcInfo["tracks"].([]any)
		dests, _ := st["destinations"].([]any)
		_ = tracks
		running = 0
		for _, d := range dests {
			dm := d.(map[string]any)
			if p, ok := dm["process"].(map[string]any); ok && p["state"] == "running" {
				running++
			}
		}
		fmt.Printf("     destinations running=%d\n", running)
		_ = srcInfo
		if running == 2 {
			break
		}
	}
	if running != 2 {
		fail("destinations never reached running state")
	}

	step("report each destination's compiled routing")
	st := doGet("/status")
	for _, d := range st["destinations"].([]any) {
		dm := d.(map[string]any)
		fmt.Printf("     %-14s %-22s %s\n", dm["name"], dm["summary"], dm["filterComplex"])
	}

	step("streaming for 10s so each destination accumulates audio")
	time.Sleep(10 * time.Second)

	step("shutting the server down cleanly")
	do("POST", "/destinations/1/stop", nil)
	do("POST", "/destinations/2/stop", nil)
	waitStopped([]string{"1", "2"}, 10*time.Second)
	_ = src.Process.Kill()
	_ = src.Wait()

	fmt.Println("\n=== VERIFICATION ===")
	ok := true
	ok = verify("a.mkv", "A (tracks 1+2)", map[int]bool{300: true, 900: true, 2000: false}) && ok
	ok = verify("b.mkv", "B (tracks 1+3)", map[int]bool{300: true, 900: false, 2000: true}) && ok

	// ---------------------------------------------------------------- E-RTMP
	//
	// Phase 1 above injects into the relay hub, which deliberately substitutes
	// the ingest hop so it runs on any FFmpeg whatever its protocol list. That
	// leaves the one-port RTMP listener -- admission, the Ready gate, the setup
	// cache, and multitrack FLV demux -- covered only by the bash suites, which
	// run on ubuntu alone.
	//
	// It does not have to be that way. internal/rtmpserver is pure Go
	// (gortmplib): no libsrt, no native dependency, and FLV muxing is in every
	// FFmpeg build. So the RTMP leg runs on macOS and Windows too, and it is
	// where the riskiest machinery lives. SRT stays out for the toolchain
	// reason above; RTMP has no such excuse.
	ok = ertmpPhase() && ok
	ok = srtPhase() && ok

	if !ok {
		fmt.Println("\nSMOKE TEST FAILED")
		os.Exit(1)
	}
	fmt.Println("\nSMOKE TEST PASSED")
}

// ertmpPhase publishes multitrack FLV at the one-port listener and measures what
// each destination made of it.
//
// Three things it proves that phase 1 cannot: the publisher is ADMITTED (the
// Ready gate consults the listener for a live subscriber, so a source whose
// ingest child never dialled in is refused); several audio tracks survive
// Enhanced RTMP, whose sequence starts for tracks 2..N arrive wrapped in
// AudioExMultitrack and were once not recognised as setup; and the routing
// graph compiled from an RTMP-probed layout is the same graph an SRT-probed one
// produces.
func ertmpPhase() bool {
	step("E-RTMP: switching the source to the one-port RTMP listener")

	srcs := doGetList("/sources")
	if len(srcs) == 0 {
		fmt.Println("     no sources; cannot exercise RTMP ingest")
		return false
	}
	src0 := srcs[0].(map[string]any)
	sid := int(src0["id"].(float64))
	token, _ := src0["token"].(string)
	if token == "" {
		fmt.Println("     source has no publish token; RTMP has no address")
		return false
	}

	ing, _ := src0["ingest"].(map[string]any)
	if ing == nil {
		ing = map[string]any{}
	}
	ing["mode"] = "rtmp"
	do("PUT", fmt.Sprintf("/sources/%d", sid), map[string]any{"ingest": ing})

	port := 1935
	if ls, ok := doGet("/settings")["listeners"].(map[string]any); ok {
		if p, ok := ls["rtmpPort"].(float64); ok && p > 0 {
			port = int(p)
		}
	}
	fmt.Printf("     source %d on rtmp, listener port %d\n", sid, port)

	// Wait for the listener to ACCEPT, not for a log line. The mode change
	// restarts the source's engine and its ingest child dials the listener on
	// loopback, so there is a window where the port is open and nothing is
	// subscribed -- publishing into it is refused by design.
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	up := false
	for i := 0; i < 40; i++ {
		if c, err := net.DialTimeout("tcp", addr, 2*time.Second); err == nil {
			_ = c.Close()
			up = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !up {
		fmt.Printf("     nothing accepting on %s\n", addr)
		return false
	}
	time.Sleep(6 * time.Second)

	step("E-RTMP: publishing 3 audio tracks as multitrack FLV")
	// -f flv, not mpegts. Pointing an MPEG-TS muxer at an rtmp:// URL sends TS
	// bytes down an RTMP connection and the server drops the session in 0s with
	// "invalid message type: 255". FFmpeg 7.1+ writes multitrack FLV whenever
	// more than one audio stream is mapped, so this is E-RTMP without asking
	// for it by name.
	pub := exec.Command(ffmpegPath(), "-hide_banner", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=300:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=900:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=2000:sample_rate=48000",
		"-map", "0:v", "-map", "1:a", "-map", "2:a", "-map", "3:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-g", "30", "-pix_fmt", "yuv420p", "-b:v", "800k",
		"-c:a", "aac", "-b:a", "128k", "-ac", "2", "-t", "40", "-f", "flv",
		fmt.Sprintf("rtmp://127.0.0.1:%d/live/%s", port, token))
	pub.Stderr = os.Stderr
	if err := pub.Start(); err != nil {
		fmt.Printf("     start publisher: %v\n", err)
		return false
	}
	defer func() { _ = pub.Process.Kill(); _ = pub.Wait() }()

	step("E-RTMP: waiting for the layout to be probed")
	// THREE, not ">= 1". Six is the placeholder layout a source reports when
	// nothing has been probed, so a loose check passes on an ingest that never
	// happened -- and one track is what a REFUSED multitrack publish looks like
	// after FFmpeg falls back.
	got := -1
	for i := 0; i < 40; i++ {
		time.Sleep(1500 * time.Millisecond)
		si := doGet("/source")
		tr, _ := si["tracks"].([]any)
		if si["probed"] == true {
			got = len(tr)
			if got == 3 {
				break
			}
		}
	}
	if got != 3 {
		fmt.Printf("     E-RTMP probed %d audio tracks, want 3\n", got)
		fmt.Println("     1 can mean the publisher was refused; 6 means no probe landed at all")
		return false
	}
	fmt.Println("     E-RTMP layout probed correctly: 3 audio tracks")

	step("E-RTMP: two destinations, differently routed")
	do("POST", "/destinations", destBody("C-rtmp-1-2", "c.mkv", []int{0, 1}))
	do("POST", "/destinations", destBody("D-rtmp-1-3", "d.mkv", []int{0, 2}))

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(1500 * time.Millisecond)
		n := 0
		for _, d := range doGet("/status")["destinations"].([]any) {
			dm := d.(map[string]any)
			if p, ok := dm["process"].(map[string]any); ok && p["state"] == "running" {
				n++
			}
		}
		if n >= 2 {
			break
		}
	}
	step("E-RTMP: streaming 12s so each destination accumulates audio")
	time.Sleep(12 * time.Second)
	var stopped []string
	for _, d := range doGet("/status")["destinations"].([]any) {
		dm := d.(map[string]any)
		if n, _ := dm["name"].(string); n == "C-rtmp-1-2" || n == "D-rtmp-1-3" {
			do("POST", fmt.Sprintf("/destinations/%v/stop", dm["id"]), nil)
			stopped = append(stopped, fmt.Sprintf("%v", dm["id"]))
		}
	}
	waitStopped(stopped, 10*time.Second)

	fmt.Println("\n=== E-RTMP VERIFICATION ===")
	okc := verify("c.mkv", "C over E-RTMP (tracks 1+2)", map[int]bool{300: true, 900: true, 2000: false})
	okd := verify("d.mkv", "D over E-RTMP (tracks 1+3)", map[int]bool{300: true, 900: false, 2000: true})
	return okc && okd
}

// srtPhase publishes over the one-port SRT listener WITHOUT needing libsrt.
//
// The hub injection in phase 1 exists because a runner's FFmpeg is not
// guaranteed to carry the srt protocol -- Homebrew's genuinely does not, checked
// -- so the SRT hop was the one part of the product that only ever ran on
// ubuntu, in the bash suites.
//
// It does not need FFmpeg at all. internal/srtserver is built on
// github.com/datarhei/gosrt, already a direct dependency, so the publisher can
// be a Go SRT client speaking to the server's own listener. FFmpeg is left doing
// the one thing every build can do -- muxing MPEG-TS to stdout -- and the SRT
// hop itself is pure Go on every platform.
func srtPhase() bool {
	step("SRT: switching the source back to the SRT listener")

	srcs := doGetList("/sources")
	if len(srcs) == 0 {
		fmt.Println("     no sources")
		return false
	}
	src0 := srcs[0].(map[string]any)
	sid := int(src0["id"].(float64))
	token, _ := src0["token"].(string)
	ing, _ := src0["ingest"].(map[string]any)
	if ing == nil {
		ing = map[string]any{}
	}
	ing["mode"] = "srt"
	do("PUT", fmt.Sprintf("/sources/%d", sid), map[string]any{"ingest": ing})

	port := 6000
	if ls, ok := doGet("/settings")["listeners"].(map[string]any); ok {
		if p, ok := ls["srtPort"].(float64); ok && p > 0 {
			port = int(p)
		}
	}
	fmt.Printf("     source %d on srt, listener port %d\n", sid, port)
	time.Sleep(8 * time.Second)

	step("SRT: dialling the listener from Go (gosrt), no libsrt anywhere")
	cfg := srt.DefaultConfig()
	cfg.StreamId = token
	cfg.Latency = 200 * time.Millisecond
	var conn srt.Conn
	var err error
	for i := 0; i < 20; i++ {
		conn, err = srt.Dial("srt", fmt.Sprintf("127.0.0.1:%d", port), cfg)
		if err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		fmt.Printf("     SRT dial refused: %v\n", err)
		return false
	}
	defer conn.Close()
	fmt.Println("     SRT publisher ADMITTED by the one-port listener")

	// FFmpeg muxes TS to STDOUT -- no protocol support required -- and this
	// copies it into the SRT connection.
	mux := exec.Command(ffmpegPath(), "-hide_banner", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=300:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=900:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=2000:sample_rate=48000",
		"-map", "0:v", "-map", "1:a", "-map", "2:a", "-map", "3:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-g", "30", "-pix_fmt", "yuv420p", "-b:v", "800k",
		"-c:a", "aac", "-b:a", "128k", "-t", "45",
		"-f", "mpegts", "-flush_packets", "1", "pipe:1")
	out, err := mux.StdoutPipe()
	if err != nil {
		fmt.Printf("     stdout pipe: %v\n", err)
		return false
	}
	mux.Stderr = os.Stderr
	if err := mux.Start(); err != nil {
		fmt.Printf("     start muxer: %v\n", err)
		return false
	}
	defer func() { _ = mux.Process.Kill(); _ = mux.Wait() }()

	// 1316 is the SRT payload size; larger writes are rejected outright.
	go func() {
		buf := make([]byte, 1316)
		for {
			n, rerr := io.ReadFull(out, buf)
			if n > 0 {
				if _, werr := conn.Write(buf[:n]); werr != nil {
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	step("SRT: waiting for the layout to be probed")
	got := -1
	for i := 0; i < 40; i++ {
		time.Sleep(1500 * time.Millisecond)
		si := doGet("/source")
		if si["probed"] == true {
			tr, _ := si["tracks"].([]any)
			got = len(tr)
			if got == 3 {
				break
			}
		}
	}
	if got != 3 {
		fmt.Printf("     SRT probed %d audio tracks, want 3 (6 = no probe landed)\n", got)
		return false
	}
	fmt.Println("     SRT layout probed correctly: 3 audio tracks")

	step("SRT: two destinations, differently routed")
	do("POST", "/destinations", destBody("E-srt-1-2", "e.mkv", []int{0, 1}))
	do("POST", "/destinations", destBody("F-srt-1-3", "f.mkv", []int{0, 2}))
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(1500 * time.Millisecond)
		n := 0
		for _, d := range doGet("/status")["destinations"].([]any) {
			dm := d.(map[string]any)
			if p, ok := dm["process"].(map[string]any); ok && p["state"] == "running" {
				n++
			}
		}
		if n >= 2 {
			break
		}
	}
	step("SRT: streaming 12s so each destination accumulates audio")
	time.Sleep(12 * time.Second)
	var stopped []string
	for _, d := range doGet("/status")["destinations"].([]any) {
		dm := d.(map[string]any)
		if n, _ := dm["name"].(string); n == "E-srt-1-2" || n == "F-srt-1-3" {
			do("POST", fmt.Sprintf("/destinations/%v/stop", dm["id"]), nil)
			stopped = append(stopped, fmt.Sprintf("%v", dm["id"]))
		}
	}
	waitStopped(stopped, 10*time.Second)

	fmt.Println("\n=== SRT VERIFICATION ===")
	oke := verify("e.mkv", "E over SRT (tracks 1+2)", map[int]bool{300: true, 900: true, 2000: false})
	okf := verify("f.mkv", "F over SRT (tracks 1+3)", map[int]bool{300: true, 900: false, 2000: true})
	return oke && okf
}

// stopPollInterval is how often waitStopped asks. Named so the wait and the
// tests agree on one number.
var stopPollInterval = 250 * time.Millisecond

// waitStopped polls the destination states until none of the given ids reports a
// running process, and returns the ids still running when it gives up.
//
// ISSUE #195. This was `time.Sleep(3 * time.Second)` in three places, and a
// fixed sleep is the wrong instrument here for three separate reasons. It runs
// on WINDOWS -- ci.yml drives this program on all three matrix OSes -- where a
// guessed interval is the least reliable thing available. It costs three
// seconds of every run of the step whether or not the processes are gone. And
// when it is not enough, it says nothing at all: the verification that follows
// reads a file that is still being written and fails somewhere else entirely.
//
// A BOUNDED POLL IS FASTER IN THE NORMAL CASE AND DIAGNOSTIC IN THE ABNORMAL
// ONE. "destination 1 was still running 10s after POST /stop" is a sentence; a
// three-second sleep is not.
//
// It does NOT decide the verdict, deliberately -- the same discipline
// lib-observe.sh states for the shell suites. verify() is what passes or fails
// this program; a wait that overrode it would be a timeout nobody could trust.
func waitStopped(ids []string, within time.Duration) []string {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	deadline := time.Now().Add(within)
	started := time.Now()
	for {
		still := runningAmong(want)
		if len(still) == 0 {
			fmt.Printf("     destination(s) %v stopped %s after /stop\n",
				ids, time.Since(started).Round(time.Millisecond))
			return nil
		}
		if !time.Now().Before(deadline) {
			fmt.Printf("     WARNING: destination(s) %v were STILL RUNNING %s after POST /stop.\n",
				still, within)
			fmt.Println("     Whatever the verification below reports, it read files that were " +
				"still being written.")
			return still
		}
		time.Sleep(stopPollInterval)
	}
}

// runningAmong reports which of the wanted destination ids currently carry a
// running process. Ids nobody asked about are ignored: in the E-RTMP and SRT
// phases the earlier phases' destinations are still in /status, and a wait that
// counted those would never finish.
func runningAmong(want map[string]bool) []string {
	var still []string
	dests, _ := doGet("/status")["destinations"].([]any)
	for _, d := range dests {
		dm, ok := d.(map[string]any)
		if !ok {
			continue
		}
		id := fmt.Sprintf("%v", dm["id"])
		if !want[id] {
			continue
		}
		if p, ok := dm["process"].(map[string]any); ok && p["state"] == "running" {
			still = append(still, id)
		}
	}
	return still
}

func destBody(name, file string, tracks []int) map[string]any {
	rows := []map[string]any{}
	on := map[int]bool{}
	for _, t := range tracks {
		on[t] = true
	}
	for i := 0; i < 6; i++ {
		rows = append(rows, map[string]any{"track": i, "enabled": on[i], "gain": 1.0})
	}
	return map[string]any{
		"name": name, "kind": "file", "platform": "custom",
		"url": file, "enabled": true, "audioBitrate": 160,
		"profile": map[string]any{
			"mode": "simple", "tracks": rows, "normalize": "auto", "sampleRate": 48000,
		},
	}
}

// verify measures energy in a narrow band around each frequency and checks it
// against expectation. A present tone reads far above a rejected one.
func verify(file, label string, want map[int]bool) bool {
	path := "data/recordings/" + file
	if _, err := os.Stat(path); err != nil {
		fmt.Printf("  %-18s MISSING (%v)\n", label, err)
		return false
	}

	// Confirm the container really carries one stereo AAC track.
	probe, _ := exec.Command("ffprobe", "-v", "error", "-select_streams", "a",
		"-show_entries", "stream=codec_name,channels", "-of", "csv=p=0", path).Output()
	streams := strings.Fields(strings.TrimSpace(string(probe)))
	fmt.Printf("\n  %s  (%s)\n", label, file)
	fmt.Printf("    audio streams: %v\n", streams)
	if len(streams) != 1 {
		fmt.Printf("    FAIL: expected exactly 1 audio stream, got %d\n", len(streams))
		return false
	}

	ok := true
	var levels []struct {
		f  int
		db float64
	}
	for _, f := range []int{300, 900, 2000} {
		db := bandEnergy(path, f)
		levels = append(levels, struct {
			f  int
			db float64
		}{f, db})
	}

	// Compare against the loudest band rather than an absolute threshold, so
	// the check does not depend on encoder gain staging.
	loudest := -200.0
	for _, l := range levels {
		if l.db > loudest {
			loudest = l.db
		}
	}
	for _, l := range levels {
		rel := l.db - loudest
		present := rel > -20 // within 20 dB of the loudest band == present
		mark := "ok"
		if present != want[l.f] {
			mark = "FAIL"
			ok = false
		}
		fmt.Printf("    %4d Hz  %7.1f dB (%+6.1f rel)  present=%-5v want=%-5v  %s\n",
			l.f, l.db, rel, present, want[l.f], mark)
	}
	return ok
}

// bandEnergy returns overall RMS dBFS after a narrow bandpass at f.
// astats logs at info level, so -v error would silently suppress it.
func bandEnergy(path string, f int) float64 {
	out, err := exec.Command(ffmpegPath(), "-v", "info", "-i", path,
		"-af", fmt.Sprintf("bandpass=frequency=%d:width_type=h:width=50,astats=metadata=0:measure_perchannel=none", f),
		"-f", "null", "-").CombinedOutput()
	if err != nil {
		return -200
	}
	last := -200.0
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "RMS level dB") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if v, err := strconv.ParseFloat(fields[len(fields)-1], 64); err == nil {
			last = v
		}
	}
	return last
}

// ------------------------------------------------------------------ helpers

func waitUp() {
	for i := 0; i < 60; i++ {
		resp, err := client.Get(base + "/health")
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	fail("server never came up")
}

func grabCSRF() {
	u, _ := http.NewRequest("GET", base+"/health", nil)
	for _, c := range client.Jar.Cookies(u.URL) {
		if c.Name == "polyemesis_csrf" {
			csrf = c.Value
		}
	}
	if csrf == "" {
		fail("no CSRF cookie issued")
	}
}

func do(method, path string, body any) map[string]any {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, base+path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	resp, err := client.Do(req)
	if err != nil {
		fail("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		fail("%s %s -> %d: %s", method, path, resp.StatusCode, raw)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

// doGetList is doGet for endpoints that answer a bare JSON ARRAY. /sources does,
// and decoding that into a map fails with "cannot unmarshal array into Go value
// of type map[string]interface {}" -- which is how this was found.
func doGetList(path string) []any {
	resp, err := client.Get(base + path)
	if err != nil {
		fail("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		fail("GET %s -> %d: %s", path, resp.StatusCode, raw)
	}
	var out []any
	if err := json.Unmarshal(raw, &out); err != nil {
		fail("GET %s: decode: %v (%s)", path, err, raw)
	}
	return out
}

func doGet(path string) map[string]any {
	resp, err := client.Get(base + path)
	if err != nil {
		fail("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		fail("GET %s -> %d: %s", path, resp.StatusCode, raw)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		fail("GET %s: decode: %v (%s)", path, err, raw)
	}
	return out
}

// ffmpegPath resolves the binary ONCE, to an absolute path.
//
// exec.Command("ffmpeg", ...) leaves resolution to PATH at exec time, which
// SonarCloud flags under go:S4036 and is right to: this program is run by CI
// and by operators, and a writable directory earlier in PATH than the real
// ffmpeg is a way to have something else entirely run with their privileges.
// LookPath resolves it here, once, and every later call names the result.
func ffmpegPath() string {
	p, err := exec.LookPath("ffmpeg")
	if err != nil {
		fail("ffmpeg is not on PATH: %v", err)
	}
	return p
}

func step(s string) { fmt.Printf("\n>> %s\n", s) }

func fail(f string, a ...any) {
	fmt.Printf("\nFATAL: "+f+"\n", a...)
	os.Exit(1)
}
