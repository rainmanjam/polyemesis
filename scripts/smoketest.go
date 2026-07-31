// Backend smoke test: drives the real HTTP API, pushes a synthetic 3-track
// SRT stream, and verifies each file destination received exactly its selected
// mix by measuring per-frequency energy in the output.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const base = "http://127.0.0.1:8099/api/v1"

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
	src := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-re",
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
	time.Sleep(3 * time.Second)
	_ = src.Process.Kill()
	_ = src.Wait()

	fmt.Println("\n=== VERIFICATION ===")
	ok := true
	ok = verify("a.mkv", "A (tracks 1+2)", map[int]bool{300: true, 900: true, 2000: false}) && ok
	ok = verify("b.mkv", "B (tracks 1+3)", map[int]bool{300: true, 900: false, 2000: true}) && ok

	if !ok {
		fmt.Println("\nSMOKE TEST FAILED")
		os.Exit(1)
	}
	fmt.Println("\nSMOKE TEST PASSED")
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
	out, err := exec.Command("ffmpeg", "-v", "info", "-i", path,
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

func step(s string) { fmt.Printf("\n>> %s\n", s) }

func fail(f string, a ...any) {
	fmt.Printf("\nFATAL: "+f+"\n", a...)
	os.Exit(1)
}
