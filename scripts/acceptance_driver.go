//go:build ignore

// Driver for scripts/acceptance.sh: completes first-run setup, creates one
// local-file and one custom-RTMP destination with different track selections,
// starts a synthetic 3-tone stream, and waits while the destinations run.
//
// It talks to exactly the same REST API the web UI uses, so a pass here means
// the UI's happy path works too.
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
	"time"
)

var (
	client *http.Client
	base   string
	csrf   string
)

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

	// Recording off: this test is about destinations, and the recorder would
	// only add noise to the disk check.
	settings := get("/settings")
	settings["recording"].(map[string]any)["enabled"] = false
	call("PUT", "/settings", settings)

	fmt.Println("starting synthetic 3-tone source (300 / 900 / 2000 Hz)")
	relayPort, _ := strconv.Atoi(relay)
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

func waitUp() {
	for i := 0; i < 60; i++ {
		if r, err := client.Get(base + "/health"); err == nil {
			r.Body.Close()
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	die("server never came up")
}

func grabCSRF() {
	req, _ := http.NewRequest("GET", base+"/health", nil)
	for _, c := range client.Jar.Cookies(req.URL) {
		if c.Name == "polyemesis_csrf" {
			csrf = c.Value
		}
	}
	if csrf == "" {
		die("no CSRF cookie issued")
	}
}

func call(method, path string, body any) map[string]any {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, base+path, r)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", csrf)
	resp, err := client.Do(req)
	if err != nil {
		die("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		die("%s %s -> %d: %s", method, path, resp.StatusCode, raw)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func get(path string) map[string]any {
	resp, err := client.Get(base + path)
	if err != nil {
		die("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		die("GET %s -> %d: %s", path, resp.StatusCode, raw)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func die(f string, a ...any) {
	fmt.Printf("FATAL: "+f+"\n", a...)
	os.Exit(1)
}
