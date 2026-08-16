//go:build ignore

// Driver for scripts/acceptance.sh: completes first-run setup, creates one
// local-file and one custom-RTMP destination with different track selections,
// starts a synthetic 3-tone stream, and waits while the destinations run.
//
// It talks to exactly the same REST API the web UI uses, so a pass here means
// the UI's happy path works too.
package main

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

var (
	client *http.Client
	base   string
	csrf   string
)

// waitUp, grabCSRF, call and get live in driverhelpers.go, compiled in by
// naming it on the `go run` line. See that file for why it is not a package.
func main() {
	if len(os.Args) < 3 {
		die("usage: acceptance_driver.go <http-port> <relay-port>")
	}
	port, relay := os.Args[1], os.Args[2]
	base = "http://127.0.0.1:" + port + "/api/v1"

	jar, _ := cookiejar.New(nil)
	client = &http.Client{Jar: jar, Timeout: 30 * time.Second}

	waitUp()
	fmt.Println("first-run setup")
	call("POST", "/setup", map[string]any{"username": "admin", "password": "acceptance-pw"})
	grabCSRF()

	// The programme everything below hangs off.
	//
	// A fresh install has none since #387: the migration used to seed a source
	// called Main on first open, so every suite in this directory was silently
	// inheriting a programme nobody had created. Creating it here is not a
	// workaround for that removal -- it is the flow an operator actually
	// performs, and it means these runs now exercise POST /sources, which no
	// acceptance suite drove before because nothing ever needed to.
	fmt.Println("creating the first source")
	call("POST", "/sources", map[string]any{"name": "Main", "enabled": true})

	// Recording off: this test is about destinations, and the recorder would
	// only add noise to the disk check.
	settings := get("/settings")
	settings["recording"].(map[string]any)["enabled"] = false
	call("PUT", "/settings", settings)

	fmt.Println("starting synthetic 3-tone source (300 / 900 / 2000 Hz)")
	// The shell's lsof is a hint: with no seeded source there may have been
	// no relay socket to find when it looked. ResolveRelayPort asks the
	// server when the hint is empty -- see its comment for the cycle.
	relayPort := resolveRelayPort(relay)
	src := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=300:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=900:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=2000:sample_rate=48000",
		"-map", "0:v", "-map", "1:a", "-map", "2:a", "-map", "3:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-g", "60", "-b:v", "2000k", "-c:a", "aac", "-b:a", "128k",
		"-metadata", "comment=acceptance-source", "-t", "60",
		"-map", "0", "-f", "mpegts", "-flush_packets", "1",
		fmt.Sprintf("udp://127.0.0.1:%d?pkt_size=1316", relayPort))
	if err := src.Start(); err != nil {
		die("start source: %v", err)
	}
	// Kill AND Wait. Kill only asks; until something reaps the child it is a
	// zombie holding a slot in this process's table, and on a driver that
	// starts several children in sequence that is a leak with a name (#197).
	defer func() { _ = src.Process.Kill(); _ = src.Wait() }()

	fmt.Println("waiting for the engine to probe the track layout")
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(1500 * time.Millisecond)
		s := get("/source")
		tracks, _ := s["tracks"].([]any)
		if s["probed"] == true && len(tracks) == 3 {
			fmt.Printf("probed: %d audio tracks\n", len(tracks))
			goto probed
		}
	}
	die("engine never probed 3 audio tracks")

probed:
	fmt.Println("creating destinations")
	call("POST", "/destinations", dest("File — tracks 1+2", "file",
		"file-dest.mkv", []int{0, 1}))
	call("POST", "/destinations", dest("RTMP — tracks 1+3", "rtmp",
		"rtmp://127.0.0.1:1937/live/acceptance", []int{0, 2}))

	fmt.Println("waiting for both destinations to run")
	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(1500 * time.Millisecond)
		st := get("/status")
		running := 0
		for _, d := range st["destinations"].([]any) {
			dm := d.(map[string]any)
			if p, ok := dm["process"].(map[string]any); ok && p["state"] == "running" {
				running++
			}
		}
		if running == 2 {
			for _, d := range st["destinations"].([]any) {
				dm := d.(map[string]any)
				fmt.Printf("  %-22s %s\n", dm["name"], dm["summary"])
			}
			goto running
		}
	}
	die("destinations never both reached running")

running:
	fmt.Println("streaming for 15s")
	time.Sleep(15 * time.Second)

	fmt.Println("stopping destinations cleanly")
	call("POST", "/destinations/1/stop", nil)
	call("POST", "/destinations/2/stop", nil)
	time.Sleep(4 * time.Second)
	fmt.Println("driver done")
}

func dest(name, kind, url string, tracks []int) map[string]any {
	on := map[int]bool{}
	for _, t := range tracks {
		on[t] = true
	}
	rows := []map[string]any{}
	for i := 0; i < 6; i++ {
		rows = append(rows, map[string]any{"track": i, "enabled": on[i], "gain": 1.0})
	}
	return map[string]any{
		"name": name, "kind": kind, "platform": "custom", "url": url,
		"enabled": true, "audioBitrate": 160,
		"profile": map[string]any{
			"mode": "simple", "tracks": rows, "normalize": "auto", "sampleRate": 48000,
		},
	}
}

func die(f string, a ...any) {
	fmt.Printf("FATAL: "+f+"\n", a...)
	os.Exit(1)
}

// resolveRelayPort: the shell's lsof is a hint, not a precondition. Without a
// seeded source no relay socket exists until this driver creates one, so an
// empty value means "ask the server", not "fail". The full account of the cycle
// is in driverlib.ResolveRelayPort; this file cannot import it, because `go run`
// resolves module imports against the cwd and these suites run from /tmp.
func resolveRelayPort(fromShell string) int {
	if p, err := strconv.Atoi(strings.TrimSpace(fromShell)); err == nil && p > 0 {
		return p
	}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		relay, _ := get("/stats")["relay"].(map[string]any)
		if pf, ok := relay["port"].(float64); ok && pf > 0 {
			return int(pf)
		}
	}
	die("no relay port after 30s; the source was probably never created")
	return 0
}
