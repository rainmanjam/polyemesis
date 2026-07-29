//go:build ignore

// Driver for scripts/acceptance-renditions.sh.
//
// Drives the rendition feature through exactly the REST API the web UI uses:
// first-run setup, a synthetic 1080p60 three-tone source, one "720p30"
// rendition, and three local-file destinations — one on passthrough and TWO
// sharing the rendition, each with a different track selection.
//
// The point of three destinations rather than two is the ref count: two of them
// select the same rendition, so a correct engine runs ONE encoder for both. The
// driver samples the process table while the stream is live and again after
// both are stopped, and writes what it saw to facts.env for the shell script to
// assert on.
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

var (
	client *http.Client
	base   string
	csrf   string
	facts  = map[string]string{}
	// factsFile is written on the way out, successful or not, so a failed run
	// still tells the shell script which assertion it got to.
	factsFile string
)

// encoderMark appears in the rendition encoder's command line and in no other
// process: destinations are -c:v copy and the source is a testsrc2 generator.
// Counting it in the process table is the only check that actually proves one
// encode is shared rather than duplicated per destination.
const encoderMark = "scale=1280:720"

func main() {
	if len(os.Args) < 4 {
		die("usage: acceptance_renditions_driver.go <http-port> <relay-port> <facts-file>")
	}
	port, relay := os.Args[1], os.Args[2]
	factsFile = os.Args[3]
	base = "http://127.0.0.1:" + port + "/api/v1"

	jar, _ := cookiejar.New(nil)
	client = &http.Client{Jar: jar, Timeout: 30 * time.Second}
	defer writeFacts()

	waitUp()
	fmt.Println("first-run setup")
	call("POST", "/setup", map[string]any{"username": "admin", "password": "acceptance-pw"})
	grabCSRF()

	// Recording and metering off: neither is under test here, and both are
	// extra FFmpeg processes competing with the encode this test is timing.
	settings := get("/settings")
	settings["recording"].(map[string]any)["enabled"] = false
	settings["meters"].(map[string]any)["enabled"] = false
	call("PUT", "/settings", settings)

	fmt.Println("starting synthetic 1080p60 3-tone source (300 / 900 / 2000 Hz)")
	relayPort, _ := strconv.Atoi(relay)
	src := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "testsrc2=size=1920x1080:rate=60",
		"-f", "lavfi", "-i", "sine=frequency=300:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=900:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=2000:sample_rate=48000",
		"-map", "0:v", "-map", "1:a", "-map", "2:a", "-map", "3:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-g", "120", "-b:v", "6000k", "-c:a", "aac", "-b:a", "128k",
		"-metadata", "comment=acceptance-source", "-t", "90",
		"-map", "0", "-f", "mpegts", "-flush_packets", "1",
		fmt.Sprintf("udp://127.0.0.1:%d?pkt_size=1316", relayPort))
	if err := src.Start(); err != nil {
		die("start source: %v", err)
	}
	defer func() { _ = src.Process.Kill() }()

	fmt.Println("waiting for the engine to probe the track layout")
	waitForProbe()

	// The presets are offered, never seeded, so this is also a check that a
	// user can start from one. The disclaimer must travel with it.
	presets := get("/renditions/presets")
	if d, _ := presets["disclaimer"].(string); d == "" {
		die("/renditions/presets returned no disclaimer")
	} else {
		facts["DISCLAIMER"] = d
	}

	fmt.Println("creating the 720p30 rendition")
	// Burned-in text rides along on this rendition.
	//
	// A WHITE BOX at full opacity, top-left, 12% of the frame height. That
	// shape is chosen to be measurable rather than to be pretty: the shell
	// crops exactly that corner and asserts its mean luma is near white.
	// testsrc2 puts a mid-grey/colour-bar field there, so a crop that comes
	// back bright can only be the box -- and a drawtext that silently rendered
	// nothing (the failure mode that exits 0) leaves it dark.
	//
	// No font is named, so this also exercises the built-in default and the
	// path that materialises it into <data>/fonts at startup. On a build with
	// no drawtext filter the server refuses nothing and simply draws nothing,
	// which is why the shell reports whether the filter exists before judging.
	created := call("POST", "/renditions", map[string]any{
		"name":         "720p30",
		"width":        1280,
		"height":       720,
		"fps":          30,
		"videoBitrate": 3000,
		"encoder":      "libx264",
		"preset":       "veryfast",
		"gopSeconds":   2,
		// The image watermark, anchored BOTTOM-RIGHT at half opacity.
		//
		// Deliberately the opposite corner from the text above, because that is
		// what makes the anchor testable: a shell that finds the logo in the
		// bottom-right and background in the top-right has proved the anchor
		// was honoured, where a full-frame check would pass even if the filter
		// ignored the anchor entirely.
		//
		// 50% opacity, because the alpha path is a DIFFERENT filter graph:
		// colourchannelmixer is omitted entirely at 100%, so an opaque overlay
		// never exercises it and a broken alpha would ship unnoticed.
		"overlay": map[string]any{
			"image":      "overlays/logo.png",
			"anchor":     "bottom-right",
			"widthPct":   0.2,
			"marginXPct": 0.0,
			"marginYPct": 0.0,
			"opacity":    0.5,
		},
		"text": map[string]any{
			"content":    "POLYEMESIS",
			"anchor":     "top-left",
			"sizePct":    0.12,
			"color":      "black",
			"marginXPct": 0.0,
			"marginYPct": 0.0,
			"box":        true,
			"boxColor":   "white",
			"boxOpacity": 1.0,
		},
	})
	rid := int64(created["rendition"].(map[string]any)["id"].(float64))
	facts["RENDITION_ID"] = strconv.FormatInt(rid, 10)
	// Read back rather than trusting the POST: a field that round-trips to ""
	// is a column the store dropped, and the pixel check further down would
	// then fail for a reason nobody could locate.
	ovl := mapOf(created["rendition"].(map[string]any)["overlay"])
	facts["OVERLAY_IMAGE_STORED"] = str(ovl["image"])
	facts["OVERLAY_ANCHOR_STORED"] = str(ovl["anchor"])
	txt := mapOf(created["rendition"].(map[string]any)["text"])
	facts["TEXT_CONTENT_STORED"] = str(txt["content"])
	facts["TEXT_BOX_STORED"] = boolStr(txt["box"] == true)
	fonts := get("/fonts")
	facts["TEXT_SUPPORTED"] = boolStr(fonts["textSupported"] == true)
	facts["DEFAULT_FONT"] = str(fonts["defaultFont"])
	nf, _ := fonts["fonts"].([]any)
	facts["FONT_COUNT"] = strconv.Itoa(len(nf))
	fmt.Printf("  rendition %d created\n", rid)

	// A brand-new rendition nothing selects must not be burning CPU.
	facts["PROCS_BEFORE_SELECT"] = strconv.Itoa(countEncoders())

	fmt.Println("creating destinations: 1 passthrough, 2 sharing the rendition")
	pass := call("POST", "/destinations", dest("Passthrough — tracks 1+2", "passthrough.mkv", []int{0, 1}, nil))
	a := call("POST", "/destinations", dest("720p30 A — tracks 1+3", "rendition-a.mkv", []int{0, 2}, &rid))
	b := call("POST", "/destinations", dest("720p30 B — tracks 2+3", "rendition-b.mkv", []int{1, 2}, &rid))

	facts["PASSTHROUGH_ID"] = destID(pass)
	facts["REND_A_ID"] = destID(a)
	facts["REND_B_ID"] = destID(b)

	// R4's warning made concrete: a passthrough destination OMITS renditionId
	// rather than sending null, so a client must read "absent" as passthrough.
	_, present := pass["destination"].(map[string]any)["renditionId"]
	facts["PASSTHROUGH_HAS_RENDITION_KEY"] = boolStr(present)

	fmt.Println("waiting for all three destinations to run")
	waitForRunning(3)

	fmt.Println("streaming for 20s, sampling the process table")
	minProcs, maxProcs := sampleEncoders(20 * time.Second)
	facts["PROCS_MIN"] = strconv.Itoa(minProcs)
	facts["PROCS_MAX"] = strconv.Itoa(maxProcs)

	st := get("/status")
	rend := renditionStatus(st, rid)
	facts["CONSUMERS"] = strconv.Itoa(intOf(rend["consumers"]))
	_, hasProc := rend["process"].(map[string]any)
	facts["RENDITION_RUNNING"] = boolStr(hasProc)
	facts["RENDITION_ERROR"] = str(rend["error"])
	facts["RELAY_PORT"] = strconv.Itoa(intOf(rend["relayPort"]))
	facts["INGEST_RELAY_PORT"] = strconv.Itoa(intOf(mapOf(st["relay"])["port"]))

	fmt.Println("stopping the two rendition destinations; the encode must be released")
	call("POST", "/destinations/"+facts["REND_A_ID"]+"/stop", nil)
	call("POST", "/destinations/"+facts["REND_B_ID"]+"/stop", nil)
	// Long enough for the supervisor's stop to complete and the files to flush.
	time.Sleep(6 * time.Second)

	facts["PROCS_AFTER_RELEASE"] = strconv.Itoa(countEncoders())
	st = get("/status")
	rend = renditionStatus(st, rid)
	facts["CONSUMERS_AFTER_RELEASE"] = strconv.Itoa(intOf(rend["consumers"]))
	_, hasProc = rend["process"].(map[string]any)
	facts["RENDITION_RUNNING_AFTER_RELEASE"] = boolStr(hasProc)

	// The passthrough destination is untouched by any of this; stopping it last
	// is what closes its file cleanly.
	call("POST", "/destinations/"+facts["PASSTHROUGH_ID"]+"/stop", nil)
	time.Sleep(4 * time.Second)
	fmt.Println("driver done")
}

// ------------------------------------------------------------------ helpers

func dest(name, file string, tracks []int, rendition *int64) map[string]any {
	on := map[int]bool{}
	for _, t := range tracks {
		on[t] = true
	}
	rows := []map[string]any{}
	for i := 0; i < 6; i++ {
		rows = append(rows, map[string]any{"track": i, "enabled": on[i], "gain": 1.0})
	}
	d := map[string]any{
		"name": name, "kind": "file", "platform": "custom", "url": file,
		"enabled": true, "audioBitrate": 160,
		"profile": map[string]any{
			"mode": "simple", "tracks": rows, "normalize": "auto", "sampleRate": 48000,
		},
	}
	if rendition != nil {
		d["renditionId"] = *rendition
	}
	return d
}

func destID(resp map[string]any) string {
	d, ok := resp["destination"].(map[string]any)
	if !ok {
		die("create destination returned %v", resp)
	}
	return strconv.Itoa(intOf(d["id"]))
}

// countEncoders counts the rendition encoders in the process table. Reading the
// real process table rather than the API is the point: the API could report one
// rendition while the engine had spawned an encoder per destination.
func countEncoders() int {
	out, err := exec.Command("ps", "-Ao", "args=").Output()
	if err != nil {
		die("ps: %v", err)
	}
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, encoderMark) && strings.Contains(line, "ffmpeg") {
			n++
		}
	}
	return n
}

func sampleEncoders(d time.Duration) (min, max int) {
	deadline := time.Now().Add(d)
	min, max = -1, 0
	for time.Now().Before(deadline) {
		n := countEncoders()
		if min < 0 || n < min {
			min = n
		}
		if n > max {
			max = n
		}
		time.Sleep(time.Second)
	}
	if min < 0 {
		min = 0
	}
	return min, max
}

func waitForProbe() {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(1500 * time.Millisecond)
		s := get("/source")
		tracks, _ := s["tracks"].([]any)
		if s["probed"] == true && len(tracks) == 3 {
			v := mapOf(s["video"])
			fmt.Printf("probed: %d audio tracks, video %vx%v\n",
				len(tracks), v["width"], v["height"])
			facts["SOURCE_WIDTH"] = strconv.Itoa(intOf(v["width"]))
			facts["SOURCE_HEIGHT"] = strconv.Itoa(intOf(v["height"]))
			return
		}
	}
	die("engine never probed 3 audio tracks")
}

func waitForRunning(want int) {
	deadline := time.Now().Add(60 * time.Second)
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
		if running == want {
			for _, d := range st["destinations"].([]any) {
				dm := d.(map[string]any)
				via := "passthrough"
				if n, ok := dm["renditionName"].(string); ok && n != "" {
					via = n
				}
				fmt.Printf("  %-26s %-22s via %s\n", dm["name"], dm["summary"], via)
			}
			return
		}
	}
	die("destinations never all reached running")
}

func renditionStatus(st map[string]any, id int64) map[string]any {
	list, _ := st["renditions"].([]any)
	for _, r := range list {
		rm := r.(map[string]any)
		if int64(intOf(rm["id"])) == id {
			return rm
		}
	}
	die("rendition %d missing from /status", id)
	return nil
}

func mapOf(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

func intOf(v any) int {
	f, _ := v.(float64)
	return int(f)
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func boolStr(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func writeFacts() {
	if factsFile == "" {
		return
	}
	var b strings.Builder
	for k, v := range facts {
		fmt.Fprintf(&b, "%s=%q\n", k, v)
	}
	if err := os.WriteFile(factsFile, []byte(b.String()), 0o644); err != nil {
		fmt.Printf("WARNING: cannot write facts: %v\n", err)
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
	// os.Exit skips deferred calls, so the facts gathered so far have to be
	// flushed here or a failing run tells the shell script nothing.
	facts["DRIVER_FAILED"] = fmt.Sprintf(f, a...)
	writeFacts()
	os.Exit(1)
}
