//go:build ignore

// Second driver for scripts/acceptance-postprod.sh: the crash-recovery half.
//
// The queue writes a job's outcome durably BEFORE it releases the concurrency
// slot, so a process killed mid-job leaves a row that says "running" and
// nothing alive that will ever finish it. Recovery at startup is the only thing
// standing between that and a job stranded forever, and the only honest way to
// test it is to actually kill the process.
//
// Two phases, because the server dies in between:
//
//	arm    submit a job, get it running, print its id on stdout
//	check  after the restart, assert nothing is still marked running
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
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
		die("usage: acceptance_postprod_restart.go <http-port> arm|check [job-id]")
	}
	base = "http://127.0.0.1:" + os.Args[1] + "/api/v1"
	phase := os.Args[2]

	jar, _ := cookiejar.New(nil)
	client = &http.Client{Jar: jar, Timeout: 60 * time.Second}
	waitUp()
	call("POST", "/auth/login", map[string]any{"username": "admin", "password": "acceptance-pw"})
	grabCSRF()

	switch phase {
	case "arm":
		arm()
	case "check":
		if len(os.Args) < 4 {
			die("check needs the job id")
		}
		id, _ := strconv.ParseInt(os.Args[3], 10, 64)
		check(id)
	default:
		die("unknown phase %q", phase)
	}
}

// arm gets one job genuinely running, so the kill that follows interrupts real
// work rather than an empty queue.
func arm() {
	var recs []map[string]any
	_ = json.Unmarshal(getRaw("/recordings"), &recs)
	if len(recs) == 0 {
		die("no recordings to submit a job against")
	}

	// Wait for the queue to DRAIN first.
	//
	// Step 2 submits five 1080p media.proxy jobs to measure the governor, and
	// leaves whatever is still running when it finishes. This section then adds
	// one more and waits 60s for it to start -- but it is behind those, and a
	// 1080p transcode on a two-core runner does not clear in a minute. The job
	// never reached "running" and the section reported that it could not stage
	// a crash, which reads as a product fault and is not one: it is one test
	// section inheriting another's work.
	//
	// Cheap on a fast machine (the queue is usually already empty) and the
	// difference between a reliable section and an intermittent one elsewhere.
	drainDeadline := time.Now().Add(180 * time.Second)
	for {
		running := 0
		if items, isList := get("/jobs?state=running")["jobs"].([]any); isList {
			for _, it := range items {
				if m, isMap := it.(map[string]any); isMap && m["state"] == "running" {
					running++
				}
			}
		}
		if running == 0 {
			break
		}
		if time.Now().After(drainDeadline) {
			die("%d job(s) from the previous section never finished, so this one "+
				"cannot get a clean slot", running)
		}
		fmt.Fprintf(os.Stderr, "waiting for %d job(s) to drain\n", running)
		time.Sleep(2 * time.Second)
	}

	// Manual mode first, so the job cannot start before we are watching, and
	// so the stream gate plays no part in this measurement.
	policy := get("/jobs/policy")["policy"].(map[string]any)
	policy["defaultMode"] = "manual"
	policy["kinds"] = []map[string]any{}
	call("PUT", "/jobs/policy", policy)

	rid := int64(recs[0]["id"].(float64))
	// Deliberately EXPENSIVE. This section has to catch the job in "running"
	// long enough to kill the server underneath it, and a cheap proxy of a
	// short test recording finishes between two polls -- the job went
	// queued -> running -> done unobserved, and the section reported "the
	// released job never started" about a job that had already finished.
	// Upscaling to 2160 at crf 0 buys seconds of encode instead of
	// milliseconds.
	out := call("POST", fmt.Sprintf("/library/recordings/%d/jobs/media.proxy", rid),
		map[string]any{"height": 2160, "crf": 0})
	id := int64(out["job"].(map[string]any)["id"].(float64))
	fmt.Fprintf(os.Stderr, "queued job %d in manual mode\n", id)

	// "Run it anyway" releases this one job from the mode gates without
	// changing the policy for every other job of its kind.
	call("POST", fmt.Sprintf("/jobs/%d/release", id), nil)

	// 100ms, not a second. "running" is a state this job passes THROUGH, and
	// the sampling interval has to be short relative to how long it lasts --
	// polling once a second for a state that lasts under a second is a
	// coin flip dressed up as a check.
	deadline := time.Now().Add(60 * time.Second)
	var last map[string]any
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		last = get(fmt.Sprintf("/jobs/%d", id))
		if last["state"] == "running" {
			fmt.Println(id) // stdout is the contract with the shell
			return
		}
	}
	// The queue explains itself; quoting it turns "it did not start" into
	// something actionable rather than a sentence that needs re-derivation.
	// "done" here means the opposite of what the message used to imply: the
	// job ran and finished, and this poller was too slow to see it.
	die("never observed the job in 'running' (last state %v, reason %v) -- if "+
		"that state is 'done' the job was too quick to catch, not stuck",
		last["state"], last["reason"])
}

// check runs after the restart.
func check(id int64) {
	j := get(fmt.Sprintf("/jobs/%d", id))
	state, _ := j["state"].(string)
	switch state {
	case "running":
		fmt.Printf("FAIL job %d is still 'running' after a restart — it is stranded forever\n", id)
		os.Exit(1)
	case "queued", "deferred", "done", "failed":
		fmt.Printf("PASS job %d was recovered to %q rather than stranded in 'running'\n", id, state)
	default:
		fmt.Printf("FAIL job %d is in an unexpected state %q after a restart\n", id, state)
		os.Exit(1)
	}

	// And nothing else is stranded either. A queue that recovered the one job
	// we watched but left five others behind has not recovered.
	stranded := 0
	if items, isList := get("/jobs?state=running")["jobs"].([]any); isList {
		for _, it := range items {
			if m, isMap := it.(map[string]any); isMap && m["state"] == "running" {
				stranded++
			}
		}
	}
	if stranded > 0 {
		fmt.Printf("FAIL %d job(s) are still marked running after the crash restart\n", stranded)
		os.Exit(1)
	}
	fmt.Println("PASS no job is left marked running after the crash restart")

	// The library must still read. It does not need a queue at all, and a
	// restart that lost the sessions would be a regression nobody would notice
	// until they went looking for a broadcast.
	lib := get("/library")
	if _, has := lib["sessions"]; !has {
		fmt.Println("FAIL the library did not return sessions after the restart")
		os.Exit(1)
	}
	fmt.Println("PASS the library still groups recordings into sessions after the restart")

	ov := get("/jobs/overview")
	if ov["available"] != true {
		fmt.Println("FAIL the jobs overview reports no queue after the restart")
		os.Exit(1)
	}
	fmt.Println("PASS the jobs page reports a live queue after the restart")

	// Whisper's absence must be reported, not thrown. This is the fail-open
	// rule: a machine without the optional tool says so and keeps working.
	if w, isMap := ov["whisper"].(map[string]any); isMap {
		if w["available"] == true {
			fmt.Println("PASS whisper is installed and reported available")
		} else {
			fmt.Printf("PASS transcription reports itself unavailable rather than erroring: %v\n", w["unavailable"])
		}
	} else {
		fmt.Println("FAIL the overview carries no whisper block at all")
		os.Exit(1)
	}
}

// ------------------------------------------------------------------- plumbing

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

// die reports to STDERR, never stdout.
//
// stdout is the contract with the shell: `arm` prints the job id there and the
// caller does JOB=$(... | tail -1). Printing a fatal error to stdout too made
// the error message BECOME the job id -- so the caller's `[ -z "$JOB" ]` guard
// saw a non-empty value, announced "job FATAL: the released job never started
// ... is running" as a PASS, and then 404'd on GET /jobs/0.
//
// A real failure was reported as a pass and then surfaced as an unrelated
// symptom five checks later. That is worse than the failure it was hiding.
func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "FATAL: "+f+"\n", a...)
	os.Exit(1)
}
