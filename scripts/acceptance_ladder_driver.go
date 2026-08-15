//go:build ignore

// Driver for scripts/acceptance-ladder.sh.
//
// acceptance-renditions.sh proves a rendition works. It cannot prove a LADDER
// works, because every test in it uses ONE rendition: its central assertion is
// "exactly ONE encoder process served both destinations", and one is the only
// number it ever counts.
//
// docs/ENCODING.md makes a claim about money that nothing measures:
//
//	"Cost scales with distinct renditions, not with destinations... A rendition
//	is shared and ref-counted: one encode feeds every destination that selected
//	it. Five destinations on one 1080p tier is one encode, not five."
//
// This drives the shape that claim is about: ONE 1080p ingest, THREE tiers, and
// FOUR destinations arranged so that ref counting up, ref counting down and
// per-destination audio are each separable from one another.
//
//	1920x1080 3-tone ingest
//	  ├─ tier "1080p"  scale=1920:1080  ← dest A   tracks 1+2
//	  ├─ tier "720p"   scale=1280:720   ← dest B   tracks 1+3
//	  │                                  ← dest D   track  1      (added later)
//	  └─ tier "480p"   scale=854:480    ← dest C   tracks 2+3
//
// Four destinations, three tiers, and the two on the 720p tier want DIFFERENT
// audio out of the SAME video encode. Everything this driver writes to
// facts.env is read out of the live process table or off the delivered media,
// never out of the API's own answer about itself.
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
	facts  = map[string]string{}
	// factsFile is written on the way out, successful or not, so a failed run
	// still tells the shell script which assertion it got to.
	factsFile string
)

// A tier is one rung of the ladder: a rendition, and the string that identifies
// its encoder in the process table.
//
// THE MARK IS THE WHOLE MEASUREMENT. RenditionSpec with both dimensions set
// compiles to a verbatim `-vf scale=W:H` (internal/ffmpeg/rendition.go,
// AspectStretch), so each rung carries a string that appears on exactly one
// process's command line and on no other. Counting those strings is the only
// check that can tell three shared encodes apart from one encode handed to
// everybody, or from four encodes because ref counting broke.
//
// The three sizes are deliberately all different from each other AND from the
// source, so no single wrong answer can pass: a ladder that gave every
// destination the top rung would show three processes at scale=1920:1080, and a
// ladder that ignored the rendition entirely would show none at all.
type tier struct {
	label   string // short name used in fact keys: 1080, 720, 480
	name    string // rendition name
	w, h    int
	bitrate int
	mark    string // the argv fragment that identifies this rung's encoder
	id      int64
	destIDs []string
}

var tiers = []*tier{
	{label: "1080", name: "ladder 1080p", w: 1920, h: 1080, bitrate: 6000, mark: "scale=1920:1080"},
	{label: "720", name: "ladder 720p", w: 1280, h: 720, bitrate: 3000, mark: "scale=1280:720"},
	{label: "480", name: "ladder 480p", w: 854, h: 480, bitrate: 1200, mark: "scale=854:480"},
}

func tierOf(label string) *tier {
	for _, t := range tiers {
		if t.label == label {
			return t
		}
	}
	die("no tier %q", label)
	return nil
}

// How long each measurement window runs. Two of them, back to back, are the
// bulk of this suite's wall clock.
//
// 18s rather than something shorter because of what is being measured: the CPU
// figures below are a DIFFERENCE of two cumulative counters, and `ps` reports
// that counter to the nearest 10ms on darwin and to the nearest SECOND on
// Linux. A 6s window on Linux would carry ±17% of quantisation error on its own
// and the comparison between the two windows would be noise. 18s puts it under
// 6%, which is small against the thing the comparison has to detect -- a whole
// extra encoder, which would be a change of tens of percent.
const sampleWindow = 18 * time.Second

func main() {
	if len(os.Args) < 7 {
		die("usage: acceptance_ladder_driver.go <http-port> <relay-port> <facts-file> <width> <height> <fps>")
	}
	port, relay := os.Args[1], os.Args[2]
	factsFile = os.Args[3]
	srcW, srcH, srcFPS := os.Args[4], os.Args[5], os.Args[6]
	base = "http://127.0.0.1:" + port + "/api/v1"

	jar, _ := cookiejar.New(nil)
	client = &http.Client{Jar: jar, Timeout: 30 * time.Second}
	defer writeFacts()

	waitUp()
	fmt.Println("first-run setup")
	call("POST", "/setup", map[string]any{"username": "admin", "password": "acceptance-pw"})
	grabCSRF()

	// Recording and metering off. Both spawn FFmpeg processes of their own, and
	// this suite's central number is how much CPU the ENCODERS use; a recorder
	// competing for the same cores would move it for a reason that has nothing
	// to do with renditions.
	settings := get("/settings")
	settings["recording"].(map[string]any)["enabled"] = false
	settings["meters"].(map[string]any)["enabled"] = false
	call("PUT", "/settings", settings)

	fmt.Printf("starting synthetic %sx%s@%s 3-tone source (300 / 900 / 2000 Hz)\n", srcW, srcH, srcFPS)
	relayPort, _ := strconv.Atoi(relay)
	fps, _ := strconv.Atoi(srcFPS)
	src := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%sx%s:rate=%d", srcW, srcH, fps),
		"-f", "lavfi", "-i", "sine=frequency=300:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=900:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=2000:sample_rate=48000",
		"-map", "0:v", "-map", "1:a", "-map", "2:a", "-map", "3:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-g", strconv.Itoa(fps*2), "-b:v", "6000k", "-c:a", "aac", "-b:a", "128k",
		"-metadata", "comment=acceptance-source", "-t", "200",
		"-map", "0", "-f", "mpegts", "-flush_packets", "1",
		fmt.Sprintf("udp://127.0.0.1:%d?pkt_size=1316", relayPort))
	if err := src.Start(); err != nil {
		die("start source: %v", err)
	}
	// Kill AND Wait. Kill only asks; until something reaps the child it is a
	// zombie holding a slot in this process's table (#197).
	defer func() { _ = src.Process.Kill(); _ = src.Wait() }()

	fmt.Println("waiting for the engine to probe the track layout")
	waitForProbe()

	// ------------------------------------------------------- the three rungs
	fmt.Println("creating three renditions: 1080p, 720p, 480p")
	for _, t := range tiers {
		created := call("POST", "/renditions", map[string]any{
			"name":         t.name,
			"width":        t.w,
			"height":       t.h,
			"fps":          fps,
			"videoBitrate": t.bitrate,
			"encoder":      "libx264",
			// ultrafast, NOT the veryfast acceptance-renditions uses, and the
			// reason is the ladder rather than taste: three concurrent encodes
			// of a 1080p source is roughly three times the work that suite
			// does, and on a two-core CI runner veryfast would not hold
			// realtime. What is under test here is how MANY encodes there are
			// and what shape each one is, neither of which the preset changes.
			"preset":     "ultrafast",
			"gopSeconds": 2,
		})
		rend := mapOf(created["rendition"])
		t.id = int64(intOf(rend["id"]))
		facts["TIER_"+t.label+"_ID"] = strconv.FormatInt(t.id, 10)
		// Read back rather than trusting the POST. If the store dropped a
		// dimension the encoder would come up at the wrong size and the pixel
		// checks downstream would fail somewhere nobody could locate.
		facts["TIER_"+t.label+"_W_STORED"] = strconv.Itoa(intOf(rend["width"]))
		facts["TIER_"+t.label+"_H_STORED"] = strconv.Itoa(intOf(rend["height"]))
		fmt.Printf("  rendition %d %q %dx%d (mark %q)\n", t.id, t.name, t.w, t.h, t.mark)
	}

	// Three renditions exist and nothing has selected any of them. A ladder
	// that spun up its rungs on creation would burn three encodes for an
	// operator who is still filling in the form.
	facts["PROCS_BEFORE_SELECT"] = strconv.Itoa(totalEncoders())

	// ------------------------------------------- phase A0: one dest per rung
	fmt.Println("creating three destinations, one per rung")
	// Track selections are all different, and that is not decoration: a
	// rendition re-encodes video ONLY and copies every audio track through, so
	// the destination is still the thing that mixes. If a shared video encode
	// ever flattened audio, these three files would stop differing.
	a := call("POST", "/destinations", dest("A 1080p — tracks 1+2", "ladder-1080.mkv", []int{0, 1}, tierOf("1080").id))
	b := call("POST", "/destinations", dest("B 720p — tracks 1+3", "ladder-720.mkv", []int{0, 2}, tierOf("720").id))
	c := call("POST", "/destinations", dest("C 480p — tracks 2+3", "ladder-480.mkv", []int{1, 2}, tierOf("480").id))
	facts["DEST_A_ID"] = destID(a)
	facts["DEST_B_ID"] = destID(b)
	facts["DEST_C_ID"] = destID(c)

	fmt.Println("waiting for all three destinations to run")
	waitForRunning(3)

	fmt.Printf("sampling the process table for %s (3 destinations, 3 tiers)\n", sampleWindow)
	a0 := sample(sampleWindow)
	a0.record("A0")
	// The pid of each rung's encoder, taken once the window is over, so the
	// ref-count checks below can ask whether the SAME process survived rather
	// than merely whether A process is there. A restarted encode and a reused
	// one are indistinguishable by count and are very different things: a
	// restart drops frames on every destination already on that rung.
	for _, t := range tiers {
		facts["PID_"+t.label+"_A0"] = strings.Join(pidsFor(t.mark), ",")
	}

	// Relay hubs, read off /status. Each rung publishes to its OWN hub -- that
	// is the mechanism by which one encode can feed several destinations -- so
	// three rungs must show three distinct ports, none of them the ingest's.
	st := get("/status")
	facts["INGEST_RELAY_PORT"] = strconv.Itoa(intOf(mapOf(st["relay"])["port"]))
	for _, t := range tiers {
		r := renditionStatus(st, t.id)
		facts["RELAY_"+t.label] = strconv.Itoa(intOf(r["relayPort"]))
		facts["CONSUMERS_"+t.label+"_A0"] = strconv.Itoa(intOf(r["consumers"]))
	}

	// ------------------------- phase A1: a FOURTH destination on an EXISTING rung
	fmt.Println("adding a fourth destination that selects the EXISTING 720p rung")
	// "Five destinations on one 1080p tier is one encode, not five", reduced to
	// the smallest case that can distinguish it: two. Track 1 alone, so this
	// destination's audio differs from the other 720p destination's even though
	// both are copying the same video bitstream.
	d := call("POST", "/destinations", dest("D 720p second subscriber — track 1", "ladder-720b.mkv", []int{0}, tierOf("720").id))
	facts["DEST_D_ID"] = destID(d)
	waitForRunning(4)

	fmt.Printf("sampling again for %s (4 destinations, still 3 tiers)\n", sampleWindow)
	a1 := sample(sampleWindow)
	a1.record("A1")
	for _, t := range tiers {
		facts["PID_"+t.label+"_A1"] = strings.Join(pidsFor(t.mark), ",")
	}
	st = get("/status")
	for _, t := range tiers {
		facts["CONSUMERS_"+t.label+"_A1"] = strconv.Itoa(intOf(renditionStatus(st, t.id)["consumers"]))
	}

	// THE COST CLAIM, MEASURED. The cheapest rung's own CPU rate is the yardstick
	// for "one more encode", and it is measured on this machine in this run
	// rather than picked. A ref-counting failure would start a second 720p
	// encoder, so the total would rise by roughly a 720p encoder's worth --
	// which is at least the cheapest rung's. The shell asserts the rise is
	// BELOW that, so the bound is derived rather than invented.
	facts["RATE_CHEAPEST_TIER_A0"] = f2(a0.cheapestRate())
	facts["RATE_TOTAL_A0"] = f2(a0.totalRate())
	facts["RATE_TOTAL_A1"] = f2(a1.totalRate())

	// ------------------------------------------------- ref counting DOWNWARDS
	fmt.Println("removing destination B — the 720p rung still has D on it")
	call("DELETE", "/destinations/"+facts["DEST_B_ID"], nil)
	// Long enough for Reconcile to have run and for a doomed child to have been
	// reaped. Measured teardown of one FFmpeg is a few seconds.
	time.Sleep(6 * time.Second)
	facts["PROCS_AFTER_DROP_ONE"] = strconv.Itoa(totalEncoders())
	for _, t := range tiers {
		facts["N_"+t.label+"_AFTER_DROP_ONE"] = strconv.Itoa(countEncoders(t.mark))
		facts["PID_"+t.label+"_AFTER_DROP_ONE"] = strings.Join(pidsFor(t.mark), ",")
	}
	st = get("/status")
	facts["CONSUMERS_720_AFTER_DROP_ONE"] = strconv.Itoa(intOf(renditionStatus(st, tierOf("720").id)["consumers"]))

	fmt.Println("removing destination D — the LAST subscriber of the 720p rung")
	call("DELETE", "/destinations/"+facts["DEST_D_ID"], nil)
	time.Sleep(8 * time.Second)
	facts["PROCS_AFTER_DROP_LAST"] = strconv.Itoa(totalEncoders())
	for _, t := range tiers {
		facts["N_"+t.label+"_AFTER_DROP_LAST"] = strconv.Itoa(countEncoders(t.mark))
		facts["PID_"+t.label+"_AFTER_DROP_LAST"] = strings.Join(pidsFor(t.mark), ",")
	}
	st = get("/status")
	r720 := renditionStatus(st, tierOf("720").id)
	facts["CONSUMERS_720_AFTER_DROP_LAST"] = strconv.Itoa(intOf(r720["consumers"]))
	_, hasProc := r720["process"].(map[string]any)
	facts["RENDITION_720_RUNNING_AFTER_DROP_LAST"] = boolStr(hasProc)

	// ------------------------------------------------------------- shut down
	// Stopped rather than deleted, and last, because stopping is what flushes
	// and closes a file destination's output. The shell probes all four files
	// after this returns.
	fmt.Println("stopping the surviving destinations so their files close")
	call("POST", "/destinations/"+facts["DEST_A_ID"]+"/stop", nil)
	call("POST", "/destinations/"+facts["DEST_C_ID"]+"/stop", nil)
	time.Sleep(5 * time.Second)
	facts["PROCS_AFTER_ALL_STOPPED"] = strconv.Itoa(totalEncoders())
	fmt.Println("driver done")
}

// ------------------------------------------------------- process-table reading

// proc is one encoder seen in the process table, with its cumulative CPU time.
type proc struct {
	pid string
	cpu float64 // seconds of CPU consumed since the process started
}

// psProcs returns every process in the table as pid, cumulative CPU, argv.
//
// Piped rather than printed to a terminal, which is what keeps the argv whole:
// ps truncates to the terminal width only when its output IS a terminal, and a
// truncated line here would drop the scale filter this file matches on.
//
// `time` rather than `%cpu`, and that is the difference between a measurement
// and a guess: `%cpu` on both platforms is an average over the process's whole
// LIFETIME, so an encoder that has been running for a minute barely moves it,
// while `time` is a monotonic counter whose difference over a known interval is
// the rate during that interval and nothing else.
func psProcs() []struct {
	proc
	args string
} {
	out, err := exec.Command("ps", "-Ao", "pid=,time=,args=").Output()
	if err != nil {
		die("ps: %v", err)
	}
	var rows []struct {
		proc
		args string
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		rows = append(rows, struct {
			proc
			args string
		}{proc{pid: f[0], cpu: parseCPUTime(f[1])}, strings.Join(f[2:], " ")})
	}
	return rows
}

// parseCPUTime reads ps's TIME column on both platforms this runs on.
//
// darwin prints MM:SS.CC and Linux prints [DD-]HH:MM:SS, so the fractional part
// exists on one and not the other and the number of colons differs. Parsed from
// the RIGHT -- seconds, then minutes, then hours -- which is the one reading
// that is correct for both. Returning 0 for an unparseable value is safe here:
// the shell asserts every encoder consumed measurable CPU, so a parser that
// silently returned zero would turn that check red rather than green.
func parseCPUTime(s string) float64 {
	if i := strings.Index(s, "-"); i >= 0 { // DD-HH:MM:SS
		days, _ := strconv.ParseFloat(s[:i], 64)
		return days*86400 + parseCPUTime(s[i+1:])
	}
	parts := strings.Split(s, ":")
	mult := 1.0
	total := 0.0
	for i := len(parts) - 1; i >= 0; i-- {
		v, err := strconv.ParseFloat(parts[i], 64)
		if err != nil {
			return 0
		}
		total += v * mult
		mult *= 60
	}
	return total
}

// encodersFor returns the encoder processes carrying one rung's mark.
func encodersFor(mark string) []proc {
	var out []proc
	for _, r := range psProcs() {
		if strings.Contains(r.args, mark) && strings.Contains(r.args, "ffmpeg") {
			out = append(out, r.proc)
		}
	}
	return out
}

func countEncoders(mark string) int { return len(encodersFor(mark)) }

// totalEncoders counts every ladder encoder, across all three rungs.
func totalEncoders() int {
	n := 0
	for _, t := range tiers {
		n += countEncoders(t.mark)
	}
	return n
}

func pidsFor(mark string) []string {
	var out []string
	for _, p := range encodersFor(mark) {
		out = append(out, p.pid)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------- sampling

// window is what one measurement window saw: how many encoders each rung had at
// its thinnest and thickest moment, and how much CPU each one burned.
type window struct {
	min, max map[string]int     // per rung label
	rate     map[string]float64 // per rung label, CPU-seconds per wall-second
	secs     float64
}

// sample watches the process table for d, then reports.
//
// BOTH ENDS OF THE COUNT, not just the last reading. A ref-counting bug that
// spawns a second encoder and reaps it a second later is a real bug and a
// single sample at the end would miss it entirely; min and max make a count
// that ever moved impossible to miss.
func sample(d time.Duration) window {
	w := window{
		min:  map[string]int{},
		max:  map[string]int{},
		rate: map[string]float64{},
	}
	start := map[string]map[string]float64{} // rung -> pid -> cpu at t0
	for _, t := range tiers {
		w.min[t.label] = -1
		start[t.label] = map[string]float64{}
		for _, p := range encodersFor(t.mark) {
			start[t.label][p.pid] = p.cpu
		}
	}

	t0 := time.Now()
	deadline := t0.Add(d)
	for time.Now().Before(deadline) {
		for _, t := range tiers {
			n := countEncoders(t.mark)
			if w.min[t.label] < 0 || n < w.min[t.label] {
				w.min[t.label] = n
			}
			if n > w.max[t.label] {
				w.max[t.label] = n
			}
		}
		time.Sleep(time.Second)
	}
	w.secs = time.Since(t0).Seconds()

	for _, t := range tiers {
		if w.min[t.label] < 0 {
			w.min[t.label] = 0
		}
		// Only pids present at BOTH ends contribute. A process that appeared or
		// vanished mid-window has no rate over this window, and inventing one
		// from a partial interval would report a number the interval does not
		// support. Such a rung is caught by min != max above anyway.
		for _, p := range encodersFor(t.mark) {
			if c0, ok := start[t.label][p.pid]; ok {
				w.rate[t.label] += (p.cpu - c0) / w.secs
			}
		}
	}
	return w
}

func (w window) totalRate() float64 {
	sum := 0.0
	for _, v := range w.rate {
		sum += v
	}
	return sum
}

// cheapestRate is the smallest per-rung CPU rate in this window -- the measured
// price of ONE more encode on this machine, and the yardstick the shell uses.
func (w window) cheapestRate() float64 {
	min := -1.0
	for _, v := range w.rate {
		if min < 0 || v < min {
			min = v
		}
	}
	if min < 0 {
		return 0
	}
	return min
}

func (w window) record(phase string) {
	for _, t := range tiers {
		facts["N_"+t.label+"_MIN_"+phase] = strconv.Itoa(w.min[t.label])
		facts["N_"+t.label+"_MAX_"+phase] = strconv.Itoa(w.max[t.label])
		facts["RATE_"+t.label+"_"+phase] = f2(w.rate[t.label])
	}
	total := 0
	for _, t := range tiers {
		total += w.max[t.label]
	}
	facts["PROCS_MAX_"+phase] = strconv.Itoa(total)
	total = 0
	for _, t := range tiers {
		total += w.min[t.label]
	}
	facts["PROCS_MIN_"+phase] = strconv.Itoa(total)
	facts["WINDOW_SECS_"+phase] = f2(w.secs)
	fmt.Printf("  window %s over %.0fs:", phase, w.secs)
	for _, t := range tiers {
		fmt.Printf("  %sp=%d..%d @ %.2f cores", t.label, w.min[t.label], w.max[t.label], w.rate[t.label])
	}
	fmt.Printf("  (total %.2f cores)\n", w.totalRate())
}

// ------------------------------------------------------------------ helpers

func f2(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }

func dest(name, file string, tracks []int, rendition int64) map[string]any {
	on := map[int]bool{}
	for _, t := range tracks {
		on[t] = true
	}
	rows := []map[string]any{}
	for i := 0; i < 6; i++ {
		rows = append(rows, map[string]any{"track": i, "enabled": on[i], "gain": 1.0})
	}
	return map[string]any{
		"name": name, "kind": "file", "platform": "custom", "url": file,
		"enabled": true, "audioBitrate": 160, "renditionId": rendition,
		"profile": map[string]any{
			"mode": "simple", "tracks": rows, "normalize": "auto", "sampleRate": 48000,
		},
	}
}

func destID(resp map[string]any) string {
	d, ok := resp["destination"].(map[string]any)
	if !ok {
		die("create destination returned %v", resp)
	}
	return strconv.Itoa(intOf(d["id"]))
}

func waitForProbe() {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(1500 * time.Millisecond)
		s := get("/source")
		tracks, _ := s["tracks"].([]any)
		if s["probed"] == true && len(tracks) == 3 {
			v := mapOf(s["video"])
			fmt.Printf("probed: %d audio tracks, video %vx%v\n", len(tracks), v["width"], v["height"])
			facts["SOURCE_WIDTH"] = strconv.Itoa(intOf(v["width"]))
			facts["SOURCE_HEIGHT"] = strconv.Itoa(intOf(v["height"]))
			return
		}
	}
	die("engine never probed 3 audio tracks")
}

func waitForRunning(want int) {
	deadline := time.Now().Add(90 * time.Second)
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
				fmt.Printf("  %-36s %-22s via %s\n", dm["name"], dm["summary"], via)
			}
			return
		}
	}
	die("destinations never all reached running (wanted %d)", want)
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
	keys := make([]string, 0, len(facts))
	for k := range facts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%q\n", k, facts[k])
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
