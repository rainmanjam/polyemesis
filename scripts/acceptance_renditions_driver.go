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
	facts  = map[string]string{}
	// factsFile is written on the way out, successful or not, so a failed run
	// still tells the shell script which assertion it got to.
	factsFile string
	// sourceID is the programme this driver created, read back off the create
	// response rather than assumed to be 1. Every create below has to name it:
	// the server no longer fills an omitted sourceId with the first source,
	// because on a multi-source install that silently attaches a destination to
	// a programme nobody chose. Hardcoding 1 would pass only until a driver
	// makes two sources or runs against a database that has seen a delete.
	sourceID int64
)

// encoderMark appears in the rendition encoder's command line and in no other
// process: destinations are -c:v copy and the source is a testsrc2 generator.
// Counting it in the process table is the only check that actually proves one
// encode is shared rather than duplicated per destination.
const encoderMark = "scale=1280:720"

// cappedMark identifies the SECOND rendition -- the one carrying an explicit
// maxrate/bufsize -- in the same process table.
//
// A different frame size from encoderMark, and that is not cosmetic: the count
// above asserts there is exactly ONE encoder for the shared 720p30 tier, so a
// second rendition at 1280x720 would be indistinguishable from a ref-counting
// bug and would fail that assertion for a reason that has nothing to do with
// ref counting.
const cappedMark = "scale=854:480"

// waitUp, grabCSRF, call and get live in driverhelpers.go, compiled in by
// naming it on the `go run` line. See that file for why it is not a package.
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

	// The programme everything below hangs off. A fresh install has none since
	// #387; see acceptance_driver.go's copy of this note for the full reason.
	fmt.Println("creating the first source")
	sourceID = createdSourceID(call("POST", "/sources", map[string]any{"name": "Main", "enabled": true}))
	fmt.Printf("  source %d\n", sourceID)

	// Recording and metering off: neither is under test here, and both are
	// extra FFmpeg processes competing with the encode this test is timing.
	settings := get("/settings")
	settings["recording"].(map[string]any)["enabled"] = false
	settings["meters"].(map[string]any)["enabled"] = false
	call("PUT", "/settings", settings)

	fmt.Println("starting synthetic 1080p60 3-tone source (300 / 900 / 2000 Hz)")
	// The shell's lsof is a hint: with no seeded source there may have been
	// no relay socket to find when it looked. ResolveRelayPort asks the
	// server when the hint is empty -- see its comment for the cycle.
	relayPort := resolveRelayPort(relay)
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
	// Kill AND Wait. Kill only asks; until something reaps the child it is a
	// zombie holding a slot in this process's table, and on a driver that
	// starts several children in sequence that is a leak with a name (#197).
	defer func() { _ = src.Process.Kill(); _ = src.Wait() }()

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
		"sourceId":     sourceID,
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

	fmt.Println("creating the 480p30 rendition with a capped rate control")
	// A SECOND rendition whose only reason to exist is its rate control.
	//
	// 4500 target, 8000 ceiling, 12000 buffer -- a capped-VBR triple no default
	// can produce. RenditionArgs reads a zero maxrate as "derive CBR", so an
	// unset field yields 4500k/9000k; those two numbers are what this test is
	// really about, because they are what the command line said before #341 no
	// matter what the operator stored. The three numbers are deliberately all
	// different from each other and from the derived pair, so no single wrong
	// value can be mistaken for a right one.
	//
	// No overlay and no text: those compile the argv through -filter_complex
	// instead of -vf, and the point here is the rate-control flags, not the
	// filter graph the other rendition already covers.
	capped := call("POST", "/renditions", map[string]any{
		"name":         "480p30 capped",
		"sourceId":     sourceID,
		"width":        854,
		"height":       480,
		"fps":          30,
		"videoBitrate": 4500,
		"maxrateKbps":  8000,
		"bufsizeKbps":  12000,
		"encoder":      "libx264",
		"preset":       "veryfast",
		"gopSeconds":   2,
	})
	cappedRend := mapOf(capped["rendition"])
	cid := int64(intOf(cappedRend["id"]))
	facts["CAPPED_RENDITION_ID"] = strconv.FormatInt(cid, 10)
	// The stored value, read back. This alone proves nothing about the encoder
	// -- it is here so a failure downstream can be located: a column the store
	// dropped and a mapping that never read it look identical from the argv.
	facts["CAPPED_MAXRATE_STORED"] = strconv.Itoa(intOf(cappedRend["maxrateKbps"]))
	facts["CAPPED_BUFSIZE_STORED"] = strconv.Itoa(intOf(cappedRend["bufsizeKbps"]))
	fmt.Printf("  rendition %d created (maxrate %s, bufsize %s)\n",
		cid, facts["CAPPED_MAXRATE_STORED"], facts["CAPPED_BUFSIZE_STORED"])

	// A brand-new rendition nothing selects must not be burning CPU.
	facts["PROCS_BEFORE_SELECT"] = strconv.Itoa(countEncoders())

	fmt.Println("creating destinations: 1 passthrough, 2 sharing the rendition, 1 capped")
	pass := call("POST", "/destinations", dest("Passthrough — tracks 1+2", "passthrough.mkv", []int{0, 1}, nil))
	a := call("POST", "/destinations", dest("720p30 A — tracks 1+3", "rendition-a.mkv", []int{0, 2}, &rid))
	b := call("POST", "/destinations", dest("720p30 B — tracks 2+3", "rendition-b.mkv", []int{1, 2}, &rid))
	// The capped rendition needs a destination for the same reason the others
	// do: an encode with no consumer is never started, so without this the
	// stored rate control would have no process to be absent from.
	c := call("POST", "/destinations", dest("480p30 capped — track 1", "capped.mkv", []int{0}, &cid))

	facts["PASSTHROUGH_ID"] = destID(pass)
	facts["REND_A_ID"] = destID(a)
	facts["REND_B_ID"] = destID(b)
	facts["CAPPED_DEST_ID"] = destID(c)

	// R4's warning made concrete: a passthrough destination OMITS renditionId
	// rather than sending null, so a client must read "absent" as passthrough.
	_, present := pass["destination"].(map[string]any)["renditionId"]
	facts["PASSTHROUGH_HAS_RENDITION_KEY"] = boolStr(present)

	fmt.Println("waiting for all four destinations to run")
	waitForRunning(4)

	fmt.Println("reading the capped encoder's command line out of the process table")
	// The whole reason this test exists, and the reason it is here rather than
	// in a Go unit test: the value is read off a process the ENGINE started,
	// from the row the API wrote. renditionSpecOf can be called directly and
	// asserted on -- rendition_ratecontrol_test.go does exactly that -- but a
	// spec built by hand cannot tell you whether the engine builds one the same
	// way from a stored row, and #341 was precisely a mapping that did not.
	argv := waitForCappedArgv(30 * time.Second)
	facts["CAPPED_ARGV_FOUND"] = boolStr(argv != "")
	facts["CAPPED_ARGV_BV"] = argAfter(argv, "-b:v")
	facts["CAPPED_ARGV_MAXRATE"] = argAfter(argv, "-maxrate")
	facts["CAPPED_ARGV_BUFSIZE"] = argAfter(argv, "-bufsize")
	fmt.Printf("  -b:v %q  -maxrate %q  -bufsize %q\n",
		facts["CAPPED_ARGV_BV"], facts["CAPPED_ARGV_MAXRATE"], facts["CAPPED_ARGV_BUFSIZE"])

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

	fmt.Println("stopping the rendition destinations; the shared encode must be released")
	call("POST", "/destinations/"+facts["REND_A_ID"]+"/stop", nil)
	call("POST", "/destinations/"+facts["REND_B_ID"]+"/stop", nil)
	// Stopped alongside them so its file is flushed and closed before the shell
	// probes it. It plays no part in the ref-count assertions below -- those
	// count encoderMark, and this encoder carries cappedMark.
	call("POST", "/destinations/"+facts["CAPPED_DEST_ID"]+"/stop", nil)
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
		"enabled": true, "audioBitrate": 160, "sourceId": sourceID,
		"profile": map[string]any{
			"mode": "simple", "tracks": rows, "normalize": "auto", "sampleRate": 48000,
		},
	}
	if rendition != nil {
		d["renditionId"] = *rendition
	}
	return d
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

func destID(resp map[string]any) string {
	d, ok := resp["destination"].(map[string]any)
	if !ok {
		die("create destination returned %v", resp)
	}
	return strconv.Itoa(intOf(d["id"]))
}

// psLines returns every command line in the process table.
//
// Piped rather than printed to a terminal, which is what keeps the lines whole:
// ps truncates to the terminal width only when its output IS a terminal, and a
// truncated line here would drop the flags this file reads off the end of it.
func psLines() []string {
	out, err := exec.Command("ps", "-Ao", "args=").Output()
	if err != nil {
		die("ps: %v", err)
	}
	return strings.Split(string(out), "\n")
}

// countEncoders counts the rendition encoders in the process table. Reading the
// real process table rather than the API is the point: the API could report one
// rendition while the engine had spawned an encoder per destination.
func countEncoders() int {
	n := 0
	for _, line := range psLines() {
		if strings.Contains(line, encoderMark) && strings.Contains(line, "ffmpeg") {
			n++
		}
	}
	return n
}

// waitForCappedArgv returns the running capped encoder's command line.
//
// It polls rather than sampling once because /status reporting "running" and
// the process appearing in the table are two different events, and a single
// read that lost the race would report an empty argv -- which reads as "the
// rate control never reached the encoder" and would be a lie. The shell
// distinguishes the two outcomes for exactly this reason.
func waitForCappedArgv(d time.Duration) string {
	deadline := time.Now().Add(d)
	for {
		for _, line := range psLines() {
			if strings.Contains(line, cappedMark) && strings.Contains(line, "ffmpeg") {
				return line
			}
		}
		if !time.Now().Before(deadline) {
			return ""
		}
		time.Sleep(time.Second)
	}
}

// argAfter returns the token following flag on a command line, or "".
func argAfter(cmdline, flag string) string {
	fields := strings.Fields(cmdline)
	for i, f := range fields {
		if f == flag && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
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

func die(f string, a ...any) {
	fmt.Printf("FATAL: "+f+"\n", a...)
	// os.Exit skips deferred calls, so the facts gathered so far have to be
	// flushed here or a failing run tells the shell script nothing.
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
