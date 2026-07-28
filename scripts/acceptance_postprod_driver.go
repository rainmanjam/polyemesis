//go:build ignore

// Driver for scripts/acceptance-postprod.sh.
//
// It MEASURES the governing principle of the post-production tier: heavy work
// yields to the live stream. Reading the governor's code proves nothing — the
// question is whether a real job, submitted over the real API to a real queue,
// stays queued while real bytes are arriving on the relay, and then runs when
// they stop.
//
// Nothing here needs whisper. Proxy generation is the heavy job, because
// FFmpeg is already a hard requirement of the product and a proxy encode is
// long enough to observe.
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
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	client *http.Client
	base   string
	csrf   string

	relayPort int
	pass, bad int
)

func ok(f string, a ...any)   { fmt.Printf("  PASS  "+f+"\n", a...); pass++ }
func fail(f string, a ...any) { fmt.Printf("  FAIL  "+f+"\n", a...); bad++ }
func step(f string, a ...any) { fmt.Printf("\n"+f+"\n", a...) }

func main() {
	if len(os.Args) < 3 {
		die("usage: acceptance_postprod_driver.go <http-port> <relay-port>")
	}
	base = "http://127.0.0.1:" + os.Args[1] + "/api/v1"
	relayPort, _ = strconv.Atoi(os.Args[2])

	jar, _ := cookiejar.New(nil)
	client = &http.Client{Jar: jar, Timeout: 60 * time.Second}

	waitUp()
	call("POST", "/setup", map[string]any{"username": "admin", "password": "acceptance-pw"})
	grabCSRF()

	prepare()
	recs := recordSomething()
	if len(recs) == 0 {
		die("no recordings were produced; nothing to measure")
	}
	fmt.Printf("  %d recordings indexed\n", len(recs))

	testYieldsToTheStream(recs)
	testRunsOnceTheStreamStops(recs)
	testNoNewJobStartsWhenTheStreamReturns(recs)
	testScheduledWindow(recs)

	fmt.Printf("\nDRIVER SUMMARY %d passed, %d failed\n", pass, bad)
	if bad > 0 {
		os.Exit(1)
	}
}

// ------------------------------------------------------------------ fixtures

// prepare turns the box into the one this test can measure: the shortest legal
// recording segments so several exist within a minute, and a governor whose
// ONLY live gate is the ingest one. The CPU ceiling is switched off on purpose
// — a proxy encode will breach it, and a test that cannot tell which gate fired
// has measured nothing.
func prepare() {
	settings := get("/settings")
	rec := settings["recording"].(map[string]any)
	rec["enabled"] = true
	rec["segmentSeconds"] = 10
	call("PUT", "/settings", settings)

	policy := map[string]any{
		"enabled": true, "concurrency": 1, "defaultMode": "deferred",
		"yieldToStream": true,
		// Every gate but the stream, off. Named explicitly rather than left to
		// the defaults so a change to a default cannot silently turn this into
		// a test of something else.
		"cpuCeilingPercent": 0, "cpuResumePercent": 0,
		"cpuSustainedSeconds": 5, "cpuSettleSeconds": 5,
		"avoidGpuWhenStreaming": false, "gpuBusy": false,
		"batteryFloorPercent": 0, "thermalCeilingC": 0,
		"niceLevel": 10, "idleIo": true,
		// Short so the measurement does not spend its life waiting: the linger
		// is what keeps a reconnect from being raced by a transcode, and two
		// seconds is enough to prove it exists.
		"ingestLingerSeconds": 2, "deferSeconds": 2,
		"retainDays": 30, "retainJobs": 200,
	}
	call("PUT", "/jobs/policy", policy)

	ov := get("/jobs/overview")
	if ov["available"] != true {
		die("the job queue is not running; nothing was wired up")
	}
	ok("the job queue is running and reachable over the API")
}

// recordSomething streams for long enough to close several segments, then stops
// and waits for the scanner to index them.
func recordSomething() []map[string]any {
	step("Recording source material")
	src := startSource(70)
	defer stop(src)

	fmt.Println("  streaming 70s at 10s segments")
	time.Sleep(75 * time.Second)
	stop(src)

	// The scanner sweeps every 30s and never measures the segment it believes
	// the recorder is still appending to, so this waits for the sweep rather
	// than assuming one just happened.
	deadline := time.Now().Add(70 * time.Second)
	for time.Now().Before(deadline) {
		recs := recordings()
		if len(recs) >= 3 {
			return recs
		}
		time.Sleep(3 * time.Second)
	}
	return recordings()
}

func recordings() []map[string]any {
	var list []map[string]any
	_ = json.Unmarshal(getRaw("/recordings"), &list)
	return list
}

// ------------------------------------------------------------------ the tests

// testYieldsToTheStream is the headline measurement: with bytes arriving, a
// submitted job must not run.
func testYieldsToTheStream(recs []map[string]any) {
	step("A queued job must NOT run while the ingest is live")
	src := startSource(90)
	defer stop(src)

	if !waitForIngest(true, 40*time.Second) {
		fail("the engine never saw the ingest go live; the rest of this test would be meaningless")
		return
	}
	ok("the ingest is live (the engine reports bytes arriving)")

	id := submitProxy(recs[0])
	fmt.Printf("  submitted proxy job %d\n", id)

	// Long enough for several governor ticks and several queue ticks. A job
	// that was going to run would have started well inside this.
	ran := false
	blocked := false
	reason := ""
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		j := job(id)
		if state(j) != "queued" && state(j) != "deferred" {
			ran = true
			break
		}
		if j["blocked"] == true {
			blocked = true
			reason, _ = j["reason"].(string)
		}
	}
	if ran {
		fail("a job RAN while the ingest was live (state %q) — the stream is not being protected", state(job(id)))
	} else {
		ok("the job stayed queued for 25s while the stream was up")
	}
	if blocked {
		ok("and the queue says why: %q", reason)
	} else {
		fail("the job was held back but reported no reason; an operator would call that a hang")
	}
	if !strings.Contains(strings.ToLower(reason), "stream") && !strings.Contains(strings.ToLower(reason), "ingest") &&
		!strings.Contains(strings.ToLower(reason), "live") {
		fail("the reason %q does not mention the stream; the wrong gate may have fired", reason)
	} else {
		ok("the reason names the stream gate, not another one")
	}
	cancel(id)
}

// testRunsOnceTheStreamStops is the other half: the gate must OPEN.
func testRunsOnceTheStreamStops(recs []map[string]any) {
	step("The same job must START once the stream stops")
	if !waitForIngest(false, 40*time.Second) {
		fail("the ingest never went quiet")
		return
	}

	id := submitProxy(recs[0])
	fmt.Printf("  submitted proxy job %d with no stream running\n", id)

	began := time.Now()
	deadline := began.Add(60 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		switch state(job(id)) {
		case "running":
			ok("the job started %.0fs after submission with no stream running", time.Since(began).Seconds())
			waitTerminal(id, 120*time.Second)
			return
		case "done":
			ok("the job ran to completion once the stream stopped")
			return
		case "failed", "cancelled":
			fail("the job reached %q rather than running: %v", state(job(id)), job(id)["error"])
			return
		}
	}
	j := job(id)
	fail("the job never started with no stream running (state %q, reason %q)", state(j), j["reason"])
}

// testNoNewJobStartsWhenTheStreamReturns pins the behaviour the governor
// DOCUMENTS for a kind with no Suspender: finish-then-yield. The running job is
// never cancelled, and no NEW one starts.
func testNoNewJobStartsWhenTheStreamReturns(recs []map[string]any) {
	step("When the stream returns, a running job finishes and no NEW job starts")

	var ids []int64
	for _, r := range recs {
		ids = append(ids, submitProxy(r))
	}
	fmt.Printf("  submitted %d proxy jobs with concurrency 1\n", len(ids))

	// Wait for the queue to be genuinely busy before disturbing it, or the
	// measurement would be of an idle queue.
	started := map[int64]bool{}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) && len(started) == 0 {
		time.Sleep(time.Second)
		for _, id := range ids {
			if s := state(job(id)); s == "running" || s == "done" {
				started[id] = true
			}
		}
	}
	if len(started) == 0 {
		fail("no job ever started, so nothing could be observed yielding")
		cancelAll(ids)
		return
	}
	ok("%d of %d jobs had started before the stream returned", len(started), len(ids))

	src := startSource(60)
	defer stop(src)
	if !waitForIngest(true, 40*time.Second) {
		fail("the ingest never came back up")
		cancelAll(ids)
		return
	}

	// Anything already running or finished is allowed to be running or
	// finished. The measurement is whether a job OUTSIDE that set starts.
	before := map[int64]bool{}
	for id := range started {
		before[id] = true
	}
	for _, id := range ids {
		if s := state(job(id)); s == "running" || s == "done" {
			before[id] = true
		}
	}

	newlyStarted := []int64{}
	end := time.Now().Add(25 * time.Second)
	for time.Now().Before(end) {
		time.Sleep(2 * time.Second)
		for _, id := range ids {
			if before[id] {
				continue
			}
			if s := state(job(id)); s == "running" || s == "done" {
				newlyStarted = append(newlyStarted, id)
				before[id] = true
			}
		}
	}
	if len(newlyStarted) > 0 {
		sort.Slice(newlyStarted, func(i, j int) bool { return newlyStarted[i] < newlyStarted[j] })
		fail("%d NEW job(s) started while the ingest was live: %v", len(newlyStarted), newlyStarted)
	} else {
		ok("no new job started in the 25s the stream was back up")
	}

	// Finish-then-yield, not kill: nothing may have been cancelled to enforce
	// the gate. A cancelled job would mean throwing away work the governor
	// promised only to postpone.
	killed := []int64{}
	for _, id := range ids {
		if state(job(id)) == "cancelled" {
			killed = append(killed, id)
		}
	}
	if len(killed) > 0 {
		fail("the governor cancelled %d job(s) to enforce a gate: %v", len(killed), killed)
	} else {
		ok("nothing was cancelled to enforce the gate (finish-then-yield, as documented)")
	}

	// And the held-back ones say so.
	explained := 0
	for _, id := range ids {
		j := job(id)
		if state(j) == "queued" && j["blocked"] == true {
			explained++
		}
	}
	if explained > 0 {
		ok("%d held-back job(s) report a block reason", explained)
	}

	stop(src)
	cancelAll(ids)
}

// testScheduledWindow pins the third gate the brief asked for: a kind whose
// mode is scheduled must not run outside its window, stream or no stream.
func testScheduledWindow(recs []map[string]any) {
	step("A scheduled kind must NOT run outside its window")
	if !waitForIngest(false, 40*time.Second) {
		fail("the ingest never went quiet, so this would have measured the stream gate instead")
		return
	}

	// A window that is open for one minute a day, twelve hours from now, in
	// UTC. Explicit zone: an empty one means UTC and never the server's, and a
	// test that relied on the server's zone would pass in one time zone only.
	start := (time.Now().UTC().Add(12*time.Hour).Hour()*60 + time.Now().UTC().Minute()) % 1440
	policy := get("/jobs/policy")["policy"].(map[string]any)
	policy["kinds"] = []map[string]any{{
		"kind": "media.thumbnails", "mode": "scheduled",
		"windows": []map[string]any{{"tz": "UTC", "startMinutes": start, "endMinutes": start + 1}},
	}}
	call("PUT", "/jobs/policy", policy)

	id := submitThumbnails(recs[0])
	fmt.Printf("  submitted thumbnails job %d, window opens in 12h\n", id)

	deadline := time.Now().Add(25 * time.Second)
	ran := false
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		if s := state(job(id)); s == "running" || s == "done" {
			ran = true
			break
		}
	}
	if ran {
		fail("a scheduled job ran outside its window")
	} else {
		ok("the scheduled job stayed queued outside its window")
	}
	j := job(id)
	if j["blocked"] == true {
		ok("and it explains itself: %q", j["reason"])
	} else {
		fail("a job held back by a window reported no reason")
	}

	// Put the policy back so the restart test that follows is not measuring
	// this window.
	policy["kinds"] = []map[string]any{}
	call("PUT", "/jobs/policy", policy)
	cancel(id)
}

// ------------------------------------------------------------------- plumbing

func submitProxy(rec map[string]any) int64 {
	id := int64(rec["id"].(float64))
	// A deliberately expensive proxy: full height and a near-lossless quality
	// target, so the encode lasts long enough to be observed rather than
	// finishing between two polls.
	out := call("POST", fmt.Sprintf("/library/recordings/%d/jobs/media.proxy", id),
		map[string]any{"height": 1080, "crf": 6})
	return int64(out["job"].(map[string]any)["id"].(float64))
}

func submitThumbnails(rec map[string]any) int64 {
	id := int64(rec["id"].(float64))
	out := call("POST", fmt.Sprintf("/library/recordings/%d/jobs/media.thumbnails", id), map[string]any{})
	return int64(out["job"].(map[string]any)["id"].(float64))
}

func job(id int64) map[string]any {
	return get(fmt.Sprintf("/jobs/%d", id))
}

func state(j map[string]any) string {
	s, _ := j["state"].(string)
	return s
}

func cancel(id int64) {
	req, _ := http.NewRequest("POST", fmt.Sprintf("%s/jobs/%d/cancel", base, id), nil)
	req.Header.Set("X-CSRF-Token", csrf)
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
	}
}

func cancelAll(ids []int64) {
	for _, id := range ids {
		cancel(id)
	}
}

func waitTerminal(id int64, d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		switch state(job(id)) {
		case "done", "failed", "cancelled":
			return
		}
		time.Sleep(2 * time.Second)
	}
}

// waitForIngest blocks until the engine agrees the ingest is (or is not) live.
// It reads the same bitrate series the governor's sensor does, so the test and
// the thing under test cannot disagree about what "live" means.
func waitForIngest(want bool, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if ingestFlowing() == want {
			return true
		}
	}
	return false
}

// ingestFlowing samples the relay twice a second apart. Bytes moving is the
// only honest answer: a listener sits in "running" waiting for a publisher.
func ingestFlowing() bool {
	a := rxBytes()
	time.Sleep(1500 * time.Millisecond)
	return rxBytes() > a
}

func rxBytes() float64 {
	st := get("/status")
	relay, isMap := st["relay"].(map[string]any)
	if !isMap {
		return 0
	}
	n, _ := relay["rxBytes"].(float64)
	return n
}

func startSource(seconds int) *exec.Cmd {
	src := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=300:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=900:sample_rate=48000",
		"-map", "0:v", "-map", "1:a", "-map", "2:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-g", "60", "-b:v", "2000k", "-c:a", "aac", "-b:a", "128k",
		"-metadata", "comment=acceptance-source", "-t", strconv.Itoa(seconds),
		"-map", "0", "-f", "mpegts", "-flush_packets", "1",
		fmt.Sprintf("udp://127.0.0.1:%d?pkt_size=1316", relayPort))
	if err := src.Start(); err != nil {
		die("start source: %v", err)
	}
	return src
}

func stop(c *exec.Cmd) {
	if c != nil && c.Process != nil {
		_ = c.Process.Kill()
		_, _ = c.Process.Wait()
	}
}

func waitUp() {
	for i := 0; i < 80; i++ {
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

func getRaw(path string) []byte {
	resp, err := client.Get(base + path)
	if err != nil {
		die("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		die("GET %s -> %d: %s", path, resp.StatusCode, raw)
	}
	return raw
}

func get(path string) map[string]any {
	var out map[string]any
	_ = json.Unmarshal(getRaw(path), &out)
	return out
}

func die(f string, a ...any) {
	fmt.Printf("FATAL: "+f+"\n", a...)
	os.Exit(1)
}
