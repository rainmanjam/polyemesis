//go:build ignore

// Driver for scripts/acceptance-encoders.sh.
//
// Reads the encoder list through exactly the endpoint the rendition editor
// calls, and — in the two modes that need it — drives a real ingest and real
// renditions so that "refused" can be told apart from "never got that far".
//
// Three modes, one per thing the shell script is asserting:
//
//	inspect   report what this machine says about every encoder
//	refuse    create a rendition on an encoder that LISTS but cannot run, and
//	          one on libx264 beside it, and record what happened to each
//	fallback  create a libx264 rendition on an FFmpeg whose detection commands
//	          all failed, and prove it still encodes
//	hevc      create a libx265 rendition and prove an HEVC encoder's own flags
//	          are right, which no H.264 leg can show
//
// Everything observed is written to a facts file as KEY=value, so the shell
// script does the asserting and this stays a recorder. The file is written on
// the way out whether the run succeeded or not, so a failed run still says how
// far it got.
package main

import (
	"fmt"
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
	client    *http.Client
	base      string
	csrf      string
	facts     = map[string]string{}
	factsFile string
	// sourceID is the programme this driver created, read back off the create
	// response rather than assumed to be 1. Every create below has to name it:
	// the server no longer fills an omitted sourceId with the first source,
	// because on a multi-source install that silently attaches a destination to
	// a programme nobody chose. Hardcoding 1 would pass only until a driver
	// makes two sources or runs against a database that has seen a delete.
	sourceID int64
)

// Each rendition is identified in the process table by its scale filter, which
// appears in the encoder's command line and nowhere else — destinations are
// -c:v copy and the source is a testsrc2 generator.
const (
	hwMark = "scale=1280:720"
	swMark = "scale=854:480"
)

// waitUp, grabCSRF, call and get live in driverhelpers.go, compiled in by
// naming it on the `go run` line. See that file for why it is not a package.
func main() {
	if len(os.Args) < 5 {
		die("usage: acceptance_encoders_driver.go <http-port> <relay-port> <mode> <facts-file>")
	}
	port, relay, mode := os.Args[1], os.Args[2], os.Args[3]
	factsFile = os.Args[4]
	base = "http://127.0.0.1:" + port + "/api/v1"

	jar, _ := cookiejar.New(nil)
	client = &http.Client{Jar: jar, Timeout: 60 * time.Second}
	defer writeFacts()

	waitUp()
	call("POST", "/setup", map[string]any{"username": "admin", "password": "acceptance-pw"})
	grabCSRF()

	// The programme everything below hangs off. A fresh install has none since
	// #387; see acceptance_driver.go's copy of this note for the full reason.
	fmt.Println("creating the first source")
	sourceID = createdSourceID(call("POST", "/sources", map[string]any{"name": "Main", "enabled": true}))
	fmt.Printf("  source %d\n", sourceID)

	// Neither recording nor metering is under test, and both are extra FFmpeg
	// processes competing with the encode being counted.
	settings := get("/settings")
	settings["recording"].(map[string]any)["enabled"] = false
	settings["meters"].(map[string]any)["enabled"] = false
	call("PUT", "/settings", settings)

	recordEncoders()

	switch mode {
	case "inspect":
		// The list is the whole story here; no stream needed.
	case "refuse":
		refuseCase(relay)
	case "fallback":
		fallbackCase(relay)
	case "hevc":
		hevcCase(relay)
	default:
		die("unknown mode %q", mode)
	}
	fmt.Println("driver done")
}

// ------------------------------------------------------------ the endpoint

// recordEncoders flattens GET /encoders into facts, one group per encoder.
//
// Every field the editor renders is recorded, not just the ones a given mode
// asserts on: when this suite fails on a machine nobody can reach, the facts
// file is the whole evidence.
func recordEncoders() {
	resp := get("/encoders")

	facts["DEFAULT"] = str(resp["default"])
	facts["PROBED"] = boolStr(resp["probed"] == true)
	facts["TESTED"] = boolStr(resp["tested"] == true)

	hw, _ := resp["hardware"].([]any)
	facts["HARDWARE_COUNT"] = strconv.Itoa(len(hw))
	var hwNames []string
	for _, h := range hw {
		hwNames = append(hwNames, str(h))
	}
	facts["HARDWARE"] = strings.Join(hwNames, " ")

	list, _ := resp["encoders"].([]any)
	if len(list) == 0 {
		die("/encoders returned no encoders at all")
	}

	var names []string
	var maxMS int
	fmt.Println("encoder                 avail  works  measured  ms  reason")
	for _, e := range list {
		m := mapOf(e)
		name := str(m["name"])
		names = append(names, name)
		ms := intOf(m["durationMs"])
		if ms > maxMS {
			maxMS = ms
		}
		facts[name+"_AVAILABLE"] = boolStr(m["available"] == true)
		facts[name+"_WORKS"] = boolStr(m["works"] == true)
		facts[name+"_MEASURED"] = boolStr(m["measured"] == true)
		facts[name+"_MS"] = strconv.Itoa(ms)
		// Newlines would break the KEY=value file, and an FFmpeg reason is
		// always one line anyway.
		facts[name+"_REASON"] = strings.ReplaceAll(str(m["reason"]), "\n", " ")
		fmt.Printf("%-22s  %-5v  %-5v  %-8v  %4d  %s\n",
			name, m["available"], m["works"], m["measured"], ms, str(m["reason"]))
	}
	sort.Strings(names)
	facts["ALL_ENCODERS"] = strings.Join(names, " ")
	facts["MAX_PROBE_MS"] = strconv.Itoa(maxMS)

	gpu := mapOf(resp["gpu"])
	facts["GPU_PLATFORM"] = str(gpu["platform"])
	var vendors []string
	for _, v := range sliceOf(gpu["vendors"]) {
		vendors = append(vendors, str(v))
	}
	facts["GPU_VENDORS"] = strings.Join(vendors, " ")
	var notes []string
	for _, n := range sliceOf(gpu["notes"]) {
		notes = append(notes, str(n))
	}
	facts["GPU_NOTES"] = strings.ReplaceAll(strings.Join(notes, " | "), "\n", " ")
	fmt.Printf("hardware scan: platform=%s vendors=[%s] nvidia=%v\n",
		facts["GPU_PLATFORM"], facts["GPU_VENDORS"], gpu["nvidia"] == true)
	for _, n := range notes {
		fmt.Printf("  note: %s\n", n)
	}
}

// --------------------------------------------------------------- the cases

// refuseCase puts two renditions on the same FFmpeg: one on the encoder the
// build lists but cannot run, one on libx264. The first must be refused with
// FFmpeg's reason and must not be retried into a crash loop; the second must
// behave exactly as it always did, which is what makes the refusal a judgement
// about one encoder rather than a blanket failure.
func refuseCase(relay string) {
	src := startSource(relay)
	// Kill AND Wait. Kill only asks; until something reaps the child it is a
	// zombie holding a slot in this process's table, and on a driver that
	// starts several children in sequence that is a leak with a name (#197).
	defer func() { _ = src.Process.Kill(); _ = src.Wait() }()
	waitForProbe()

	hwID := newRendition("nvenc-720p", "h264_nvenc", 1280, 720)
	swID := newRendition("x264-480p", "libx264", 854, 480)

	call("POST", "/destinations", dest("via nvenc", "nvenc.mkv", hwID))
	call("POST", "/destinations", dest("via x264", "x264.mkv", swID))

	// Long enough that a supervisor which retried a failed start would have
	// done it several times over by now.
	fmt.Println("sampling the process table for 20s")
	_, hwMax := sampleProcs(hwMark, 20*time.Second)
	facts["NVENC_PROC_SAMPLES_MAX"] = strconv.Itoa(hwMax)

	st := get("/status")
	hw := renditionStatus(st, hwID)
	sw := renditionStatus(st, swID)

	facts["NVENC_REND_ERROR"] = strings.ReplaceAll(str(hw["error"]), "\n", " ")
	facts["NVENC_REND_RUNNING"] = boolStr(hasProcess(hw))
	facts["X264_REND_ERROR"] = strings.ReplaceAll(str(sw["error"]), "\n", " ")
	facts["X264_REND_RUNNING"] = boolStr(hasProcess(sw))

	fmt.Printf("nvenc rendition: running=%v err=%q\n", hasProcess(hw), str(hw["error"]))
	fmt.Printf("x264  rendition: running=%v err=%q\n", hasProcess(sw), str(sw["error"]))
}

// fallbackCase is the no-hardware path with detection removed entirely: the
// encoder list would not load and no probe could run. The product must behave
// as though it had never asked.
func fallbackCase(relay string) {
	src := startSource(relay)
	// Kill AND Wait. Kill only asks; until something reaps the child it is a
	// zombie holding a slot in this process's table, and on a driver that
	// starts several children in sequence that is a leak with a name (#197).
	defer func() { _ = src.Process.Kill(); _ = src.Wait() }()
	waitForProbe()

	id := newRendition("fallback-720p", "libx264", 1280, 720)
	destID := destIDOf(call("POST", "/destinations", dest("fallback", "fallback.mkv", id)))

	waitForRenditionRunning(id)
	fmt.Println("encoding for 12s")
	time.Sleep(12 * time.Second)

	st := get("/status")
	r := renditionStatus(st, id)
	facts["X264_REND_RUNNING"] = boolStr(hasProcess(r))
	facts["X264_REND_ERROR"] = strings.ReplaceAll(str(r["error"]), "\n", " ")

	// Stopped rather than killed, so the file is finalised and ffprobe can
	// read a real width and height off it.
	call("POST", "/destinations/"+destID+"/stop", nil)
	time.Sleep(5 * time.Second)
}

// hevcCase runs a real rendition on libx265.
//
// Every other leg of this suite encodes H.264, and H.264 cannot show the defect
// that matters here: `-profile:v high` is an H.264 profile name, and the HEVC
// encoders REFUSE it rather than ignoring it -- `x265 [error]: unknown profile
// <high>` -- so an HEVC row copied from its H.264 sibling is a rendition that
// can be saved and can never start. Six of the twelve encoders the editor
// offers are HEVC and, before #343, all six were unconfigured.
//
// libx265 is the one HEVC encoder that needs no particular silicon, so this
// runs everywhere the suite runs. It is skipped, not failed, on a build without
// it: an FFmpeg compiled without libx265 is an environment fact.
func hevcCase(relay string) {
	if facts["libx265_AVAILABLE"] != "true" {
		facts["HEVC_REND_SKIPPED"] = "true"
		fmt.Println("this build has no libx265; skipping the HEVC leg")
		return
	}
	facts["HEVC_REND_SKIPPED"] = "false"

	src := startSource(relay)
	defer func() { _ = src.Process.Kill(); _ = src.Wait() }()
	waitForProbe()

	id := newRendition("hevc-720p", "libx265", 1280, 720)
	destID := destIDOf(call("POST", "/destinations", dest("hevc", "hevc.mkv", id)))

	waitForRenditionRunning(id)
	fmt.Println("encoding for 12s")
	time.Sleep(12 * time.Second)

	r := renditionStatus(get("/status"), id)
	facts["HEVC_REND_RUNNING"] = boolStr(hasProcess(r))
	facts["HEVC_REND_ERROR"] = strings.ReplaceAll(str(r["error"]), "\n", " ")

	// Stopped rather than killed, so the file is finalised and ffprobe can read
	// a real codec name off it.
	call("POST", "/destinations/"+destID+"/stop", nil)
	time.Sleep(5 * time.Second)
}

// ------------------------------------------------------------------ helpers

func startSource(relay string) *exec.Cmd {
	// Its own copy of the resolution the other drivers got, because this one
	// discovers the port here rather than in main. Same cycle: without a seeded
	// source the shell's lsof may have found nothing, since the relay socket
	// exists only while an engine runs and an engine runs only for a source.
	// By the time this is called the source has been created, so the server can
	// answer.
	relayPort := resolveRelayPort(relay)
	fmt.Println("starting synthetic 1080p30 source")
	src := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "testsrc2=size=1920x1080:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=300:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=900:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=2000:sample_rate=48000",
		"-map", "0:v", "-map", "1:a", "-map", "2:a", "-map", "3:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-g", "60", "-b:v", "4000k", "-c:a", "aac", "-b:a", "128k",
		"-metadata", "comment=acceptance-source", "-t", "120",
		"-map", "0", "-f", "mpegts", "-flush_packets", "1",
		fmt.Sprintf("udp://127.0.0.1:%d?pkt_size=1316", relayPort))
	if err := src.Start(); err != nil {
		die("start source: %v", err)
	}
	return src
}

// createdSourceID reads the id off a POST /sources response. The source view
// embeds db.Source, so the id is at the top level.
func createdSourceID(resp map[string]any) int64 {
	id := int64(intOf(resp["id"]))
	if id == 0 {
		die("create source returned no id: %v", resp)
	}
	return id
}

func newRendition(name, encoder string, w, h int) int64 {
	created := call("POST", "/renditions", map[string]any{
		"name": name, "width": w, "height": h, "fps": 30,
		"videoBitrate": 2500, "encoder": encoder,
		"preset": "veryfast", "gopSeconds": 2,
		"sourceId": sourceID,
	})
	r, ok := created["rendition"].(map[string]any)
	if !ok {
		die("create rendition %q returned %v", name, created)
	}
	id := int64(intOf(r["id"]))
	fmt.Printf("rendition %d %q on %s\n", id, name, encoder)
	return id
}

func dest(name, file string, rendition int64) map[string]any {
	rows := []map[string]any{}
	for i := 0; i < 6; i++ {
		rows = append(rows, map[string]any{"track": i, "enabled": i < 2, "gain": 1.0})
	}
	return map[string]any{
		"name": name, "kind": "file", "platform": "custom", "url": file,
		"enabled": true, "audioBitrate": 160, "renditionId": rendition,
		"sourceId": sourceID,
		"profile": map[string]any{
			"mode": "simple", "tracks": rows, "normalize": "auto", "sampleRate": 48000,
		},
	}
}

func destIDOf(resp map[string]any) string {
	d, ok := resp["destination"].(map[string]any)
	if !ok {
		die("create destination returned %v", resp)
	}
	return strconv.Itoa(intOf(d["id"]))
}

func hasProcess(r map[string]any) bool {
	_, ok := r["process"].(map[string]any)
	return ok
}

func countProcs(mark string) int {
	out, err := exec.Command("ps", "-Ao", "args=").Output()
	if err != nil {
		die("ps: %v", err)
	}
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, mark) && strings.Contains(line, "ffmpeg") {
			n++
		}
	}
	return n
}

func sampleProcs(mark string, d time.Duration) (min, max int) {
	deadline := time.Now().Add(d)
	min, max = -1, 0
	for time.Now().Before(deadline) {
		n := countProcs(mark)
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
			fmt.Printf("probed: %d audio tracks\n", len(tracks))
			return
		}
	}
	die("engine never probed the source")
}

func waitForRenditionRunning(id int64) {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(1500 * time.Millisecond)
		r := renditionStatus(get("/status"), id)
		if hasProcess(r) {
			return
		}
		if e := str(r["error"]); e != "" {
			// Recorded rather than fatal: a refusal here is a finding for the
			// shell script to assert on, not a reason to abandon the run.
			facts["X264_REND_ERROR"] = strings.ReplaceAll(e, "\n", " ")
		}
	}
	die("rendition %d never started", id)
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

// ------------------------------------------------------------------- plumbing

func mapOf(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

func sliceOf(v any) []any {
	s, _ := v.([]any)
	return s
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
		return "true"
	}
	return "false"
}

func writeFacts() {
	var b strings.Builder
	keys := make([]string, 0, len(facts))
	for k := range facts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, facts[k])
	}
	if err := os.WriteFile(factsFile, []byte(b.String()), 0o644); err != nil {
		fmt.Printf("could not write facts: %v\n", err)
	}
}

func die(f string, a ...any) {
	fmt.Printf("FATAL: "+f+"\n", a...)
	facts["DRIVER_FAILED"] = fmt.Sprintf(f, a...)
	writeFacts()
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
