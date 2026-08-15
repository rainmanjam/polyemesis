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
//
// THE HTTP SESSION IS scripts/internal/driverlib's, NOT THIS FILE'S. Six
// drivers had already grown their own copy of the same cookie jar, CSRF dance
// and error handling; driverlib's package comment records what the second copy
// cost when a fix landed in one of them and not the other. The seventh copy is
// not free either, so there is not one here.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/scripts/internal/driverlib"
)

var (
	facts = map[string]string{}
	// factsFile is written on the way out, successful or not, so a failed run
	// still tells the shell script which assertion it got to.
	factsFile string

	// The external programs this driver runs, resolved to absolute paths once
	// at startup. See toolPath.
	ffmpegBin string
	psBin     string
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

// ------------------------------------------------------- what the API answers
//
// Decoded into TYPES rather than map[string]any, and that is not a style
// preference. A `m["renditions"].([]any)[0].(map[string]any)["consumers"]
// .(float64)` chain has one failure mode -- a silent zero -- for every one of
// its four assertions, and this suite's whole business is telling a real zero
// apart from a measurement that broke. A struct field that does not decode is
// visibly absent; a type assertion that does not hold is a panic or a zero, and
// which one it is depends on punctuation.

type processState struct {
	State string `json:"state"`
}

type statusDoc struct {
	Relay struct {
		Port int `json:"port"`
	} `json:"relay"`
	Destinations []struct {
		ID            int64         `json:"id"`
		Name          string        `json:"name"`
		Summary       string        `json:"summary"`
		RenditionName string        `json:"renditionName"`
		Process       *processState `json:"process"`
	} `json:"destinations"`
	Renditions []renditionStatus `json:"renditions"`
}

type renditionStatus struct {
	ID        int64         `json:"id"`
	Consumers int           `json:"consumers"`
	RelayPort int           `json:"relayPort"`
	Error     string        `json:"error"`
	Process   *processState `json:"process"`
}

type sourceDoc struct {
	Probed bool `json:"probed"`
	Tracks []struct {
		Index int `json:"index"`
	} `json:"tracks"`
	Video struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"video"`
}

type renditionResp struct {
	Rendition struct {
		ID     int64 `json:"id"`
		Width  int   `json:"width"`
		Height int   `json:"height"`
	} `json:"rendition"`
}

type destResp struct {
	Destination struct {
		ID int64 `json:"id"`
	} `json:"destination"`
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

	// Resolved before anything else runs, so a machine missing either one is
	// told so immediately rather than sixty seconds into a measurement.
	ffmpegBin = toolPath("ffmpeg")
	psBin = toolPath("ps")

	driverlib.Init("http://127.0.0.1:" + port)
	defer writeFacts()

	driverlib.WaitUp()
	fmt.Println("first-run setup")
	driverlib.Setup("admin", "acceptance-pw")

	// Recording and metering off. Both spawn FFmpeg processes of their own, and
	// this suite's central number is how much CPU the ENCODERS use; a recorder
	// competing for the same cores would move it for a reason that has nothing
	// to do with renditions.
	//
	// The WHOLE document is read and written back -- see driverlib.LoadSettings.
	// A PUT of one block resets every other to defaults, which here would move
	// the ingest listener off the port the source is publishing to.
	settings := driverlib.LoadSettings()
	blockOff(settings, "recording")
	blockOff(settings, "meters")
	driverlib.SaveSettings(settings, "disable recording and meters")

	fmt.Printf("starting synthetic %sx%s@%s 3-tone source (300 / 900 / 2000 Hz)\n", srcW, srcH, srcFPS)
	relayPort, _ := strconv.Atoi(relay)
	fps, _ := strconv.Atoi(srcFPS)
	src := exec.Command(ffmpegBin, "-hide_banner", "-loglevel", "error", "-re",
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
		var created renditionResp
		post("/renditions", map[string]any{
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
		}, &created)
		t.id = created.Rendition.ID
		facts["TIER_"+t.label+"_ID"] = strconv.FormatInt(t.id, 10)
		// Read back rather than trusting the request. If the store dropped a
		// dimension the encoder would come up at the wrong size and the pixel
		// checks downstream would fail somewhere nobody could locate.
		facts["TIER_"+t.label+"_W_STORED"] = strconv.Itoa(created.Rendition.Width)
		facts["TIER_"+t.label+"_H_STORED"] = strconv.Itoa(created.Rendition.Height)
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
	facts["DEST_A_ID"] = newDest("A 1080p — tracks 1+2", "ladder-1080.mkv", tierOf("1080").id, 0, 1)
	facts["DEST_B_ID"] = newDest("B 720p — tracks 1+3", "ladder-720.mkv", tierOf("720").id, 0, 2)
	facts["DEST_C_ID"] = newDest("C 480p — tracks 2+3", "ladder-480.mkv", tierOf("480").id, 1, 2)

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
	st := status()
	facts["INGEST_RELAY_PORT"] = strconv.Itoa(st.Relay.Port)
	for _, t := range tiers {
		r := st.rendition(t.id)
		facts["RELAY_"+t.label] = strconv.Itoa(r.RelayPort)
		facts["CONSUMERS_"+t.label+"_A0"] = strconv.Itoa(r.Consumers)
	}

	// ------------------------- phase A1: a FOURTH destination on an EXISTING rung
	fmt.Println("adding a fourth destination that selects the EXISTING 720p rung")
	// "Five destinations on one 1080p tier is one encode, not five", reduced to
	// the smallest case that can distinguish it: two. Track 1 alone, so this
	// destination's audio differs from the other 720p destination's even though
	// both are copying the same video bitstream.
	facts["DEST_D_ID"] = newDest("D 720p second subscriber — track 1", "ladder-720b.mkv", tierOf("720").id, 0)
	waitForRunning(4)

	fmt.Printf("sampling again for %s (4 destinations, still 3 tiers)\n", sampleWindow)
	a1 := sample(sampleWindow)
	a1.record("A1")
	for _, t := range tiers {
		facts["PID_"+t.label+"_A1"] = strings.Join(pidsFor(t.mark), ",")
	}
	st = status()
	for _, t := range tiers {
		facts["CONSUMERS_"+t.label+"_A1"] = strconv.Itoa(st.rendition(t.id).Consumers)
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
	remove(facts["DEST_B_ID"])

	// TWO WAITS, SPELLED SEPARATELY, and #370 is why. That fix found a sleep in
	// acceptance-failover doing two different jobs: letting time elapse, and
	// waiting for a condition. Six seconds was "how long it happened to take on
	// the machine where the line was written", and on the OVH box it was not
	// enough -- the suite refused to measure and reported two failures against a
	// feature that was working. The same six seconds was sitting here.
	//
	// (a) A CONDITION: has the engine PROCESSED the removal yet? The ref count
	//     is the engine saying so, and polling it is bounded by the answer
	//     rather than by someone's laptop.
	facts["CONSUMERS_720_AFTER_DROP_ONE"] = strconv.Itoa(
		waitForConsumers(tierOf("720").id, 1, 30*time.Second))
	// (b) ELAPSED TIME, genuinely, and this one has to stay a sleep. The
	//     assertion below is that the 720p encode SURVIVED, and an absence of an
	//     event cannot be polled for: reading the instant the ref count moved
	//     would pass against an engine that killed the tier a second later.
	//     holdStill is the window in which that mistake would become visible.
	time.Sleep(holdStill)
	facts["PROCS_AFTER_DROP_ONE"] = strconv.Itoa(totalEncoders())
	for _, t := range tiers {
		facts["N_"+t.label+"_AFTER_DROP_ONE"] = strconv.Itoa(countEncoders(t.mark))
		facts["PID_"+t.label+"_AFTER_DROP_ONE"] = strings.Join(pidsFor(t.mark), ",")
	}

	fmt.Println("removing destination D — the LAST subscriber of the 720p rung")
	remove(facts["DEST_D_ID"])
	facts["CONSUMERS_720_AFTER_DROP_LAST"] = strconv.Itoa(
		waitForConsumers(tierOf("720").id, 0, 30*time.Second))
	// A condition this time, because here the tier is supposed to GO -- and the
	// helper reports what it LAST SAW rather than what it was waiting for, which
	// is the whole difference between a check and a formality. An encode that
	// leaked never reaches zero, the poll times out, and the count recorded
	// below is the real one. Mutating the engine's ref-count gate is what proved
	// that: the poll runs its full 30s and the suite still says "1 encoder".
	waitForEncoders(tierOf("720").mark, 0, 30*time.Second)
	facts["PROCS_AFTER_DROP_LAST"] = strconv.Itoa(totalEncoders())
	for _, t := range tiers {
		facts["N_"+t.label+"_AFTER_DROP_LAST"] = strconv.Itoa(countEncoders(t.mark))
		facts["PID_"+t.label+"_AFTER_DROP_LAST"] = strings.Join(pidsFor(t.mark), ",")
	}
	r720 := status().rendition(tierOf("720").id)
	facts["RENDITION_720_RUNNING_AFTER_DROP_LAST"] = boolStr(r720.Process != nil)

	// ------------------------------------------------------------- shut down
	// Stopped rather than deleted, and last, because stopping is what flushes
	// and closes a file destination's output. The shell probes all four files
	// after this returns, so what is waited for here is the WRITER going away --
	// a condition, observable by name in the process table, rather than a guess
	// at how long a flush takes on this machine.
	fmt.Println("stopping the surviving destinations so their files close")
	stop(facts["DEST_A_ID"])
	stop(facts["DEST_C_ID"])
	for _, out := range []string{"ladder-1080.mkv", "ladder-480.mkv"} {
		waitForGone(out, 30*time.Second)
	}
	waitForEncoders(tierOf("1080").mark, 0, 30*time.Second)
	waitForEncoders(tierOf("480").mark, 0, 30*time.Second)
	facts["PROCS_AFTER_ALL_STOPPED"] = strconv.Itoa(totalEncoders())
	fmt.Println("driver done")
}

// ------------------------------------------------------------- API shorthands

// post sends a create and decodes the answer, dying on any refusal.
//
// 200 AND 201 both count, the same pair driverlib.Setup accepts and for the
// same reason: the API has answered both over its life and no suite should
// hold an opinion about which.
func post(path string, body, out any) {
	code, raw := driverlib.Do(http.MethodPost, path, body)
	if code != http.StatusOK && code != http.StatusCreated {
		die("POST %s -> %d: %s", path, code, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			die("POST %s answered unreadable JSON: %v", path, err)
		}
	}
}

// newDest creates one file destination on a rung and returns its id.
//
// The track rows come from driverlib.Sel, which emits EVERY row -- enabled or
// not -- because a profile is a full declaration of the width it was authored
// against and a short list leaves the server guessing.
func newDest(name, file string, rendition int64, tracks ...int) string {
	var out destResp
	post("/destinations", map[string]any{
		"name": name, "kind": "file", "platform": "custom", "url": file,
		"enabled": true, "audioBitrate": 160, "renditionId": rendition,
		"profile": map[string]any{
			"mode": "simple", "tracks": driverlib.Sel(tracks...),
			"normalize": "auto", "sampleRate": 48000,
		},
	}, &out)
	if out.Destination.ID == 0 {
		die("create destination %q came back with no id", name)
	}
	return strconv.FormatInt(out.Destination.ID, 10)
}

func remove(id string) {
	if code, raw := driverlib.Do(http.MethodDelete, "/destinations/"+id, nil); code != http.StatusOK {
		die("DELETE destination %s -> %d: %s", id, code, raw)
	}
}

func stop(id string) {
	if code, raw := driverlib.Do(http.MethodPost, "/destinations/"+id+"/stop", nil); code != http.StatusOK {
		die("stop destination %s -> %d: %s", id, code, raw)
	}
}

func status() statusDoc {
	var st statusDoc
	driverlib.GetJSON("/status", "status", &st)
	return st
}

// rendition finds one rung in a status document, and refuses to invent one.
//
// A missing rendition returns a zero struct if you let it, and a zero struct
// reads as "0 consumers, no process" -- which is exactly what this suite
// asserts after a tier is released. Dying instead means the two can never be
// confused.
func (s statusDoc) rendition(id int64) renditionStatus {
	for _, r := range s.Renditions {
		if r.ID == id {
			return r
		}
	}
	die("rendition %d missing from /status", id)
	return renditionStatus{}
}

// blockOff turns off one named settings block, and says so if it is absent.
//
// The absence matters: PUT /settings replaces the document, so a block this
// silently skipped would leave recording enabled and put a recorder's FFmpeg
// into the CPU figures this suite prints.
func blockOff(settings map[string]any, name string) {
	block, ok := settings[name].(map[string]any)
	if !ok {
		die("settings carried no %q block to disable", name)
	}
	block["enabled"] = false
}

// ------------------------------------------------------- process-table reading

// toolPath resolves an external program to an ABSOLUTE path, once, at startup.
//
// exec.Command("ffmpeg", ...) searches $PATH at spawn time, so what actually
// runs is whatever the first matching directory on the path happens to contain
// -- go:S4036, and a real hazard for a process this file then reads a command
// line back out of. Resolving once and refusing anything non-absolute closes
// it.
//
// It buys a second thing worth having on its own: a machine without one of
// these tools is told so here, by name, instead of failing opaquely sixty
// seconds into a measurement window.
func toolPath(name string) string {
	found, err := exec.LookPath(name)
	if err != nil {
		die("%s is not on PATH: %v", name, err)
	}
	abs, err := filepath.Abs(found)
	if err != nil {
		die("cannot resolve %s (%q) to an absolute path: %v", name, found, err)
	}
	if !filepath.IsAbs(abs) {
		die("%s resolved to %q, which is not an absolute path", name, abs)
	}
	return abs
}

// proc is one encoder seen in the process table, with its cumulative CPU time.
type proc struct {
	pid string
	cpu float64 // seconds of CPU consumed since the process started
}

// psRow is one line of the process table: a process, and its full argv.
type psRow struct {
	proc
	args string
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
func psProcs() []psRow {
	out, err := exec.Command(psBin, "-Ao", "pid=,time=,args=").Output()
	if err != nil {
		die("ps: %v", err)
	}
	var rows []psRow
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		rows = append(rows, psRow{proc{pid: f[0], cpu: parseCPUTime(f[1])}, strings.Join(f[2:], " ")})
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
	totalMin, totalMax := 0, 0
	for _, t := range tiers {
		facts["N_"+t.label+"_MIN_"+phase] = strconv.Itoa(w.min[t.label])
		facts["N_"+t.label+"_MAX_"+phase] = strconv.Itoa(w.max[t.label])
		facts["RATE_"+t.label+"_"+phase] = f2(w.rate[t.label])
		totalMin += w.min[t.label]
		totalMax += w.max[t.label]
	}
	facts["PROCS_MIN_"+phase] = strconv.Itoa(totalMin)
	facts["PROCS_MAX_"+phase] = strconv.Itoa(totalMax)
	facts["WINDOW_SECS_"+phase] = f2(w.secs)
	fmt.Printf("  window %s over %.0fs:", phase, w.secs)
	for _, t := range tiers {
		fmt.Printf("  %sp=%d..%d @ %.2f cores", t.label, w.min[t.label], w.max[t.label], w.rate[t.label])
	}
	fmt.Printf("  (total %.2f cores)\n", w.totalRate())
}

// ------------------------------------------------------- waiting on the engine

// holdStill is elapsed time ON PURPOSE, and the only sleep left in this file
// that is not a poll.
//
// It exists for the one assertion that cannot be polled: that an encode
// SURVIVED a removal. A condition can be waited for; the absence of an event
// cannot, so proving nothing happened means giving it a window in which to
// happen and then looking. Five seconds is comfortably longer than a measured
// reconcile-plus-reap, which is what a wrong engine would need to tear the tier
// down in.
const holdStill = 5 * time.Second

// EVERY POLL BELOW RETURNS WHAT IT LAST SAW, NEVER WHAT IT WANTED.
//
// This is the property that keeps them from turning checks into formalities. A
// helper that waited for a value and then handed that value back would make its
// caller's assertion unfalsifiable -- it would be asserting the thing the
// helper had already guaranteed. Returning the final observation means a
// timeout produces the REAL number, the caller records it, and the shell fails
// with the truth in the message. See scripts/acceptance-failover.sh's
// wait_for_dest_process, which hands back the last read for the same reason.

// waitForConsumers polls a rung's ref count until it reaches want.
func waitForConsumers(id int64, want int, d time.Duration) int {
	deadline := time.Now().Add(d)
	got := -1
	for {
		got = status().rendition(id).Consumers
		if got == want || !time.Now().Before(deadline) {
			if got != want {
				fmt.Printf("  consumers on rendition %d settled at %d, not the %d expected, after %s\n",
					id, got, want, d)
			}
			return got
		}
		time.Sleep(time.Second)
	}
}

// waitForEncoders polls one rung's encoder count until it reaches want.
func waitForEncoders(mark string, want int, d time.Duration) int {
	return waitForCount("encoders matching "+mark, func() int { return countEncoders(mark) }, want, d)
}

// waitForGone polls until no process carries substr in its argv -- used to wait
// for a file destination's writer to exit, which is what closes its output.
func waitForGone(substr string, d time.Duration) int {
	return waitForCount("processes writing "+substr, func() int {
		n := 0
		for _, r := range psProcs() {
			if strings.Contains(r.args, substr) && strings.Contains(r.args, "ffmpeg") {
				n++
			}
		}
		return n
	}, 0, d)
}

func waitForCount(what string, count func() int, want int, d time.Duration) int {
	deadline := time.Now().Add(d)
	for {
		got := count()
		if got == want || !time.Now().Before(deadline) {
			if got != want {
				fmt.Printf("  %s settled at %d, not the %d expected, after %s\n", what, got, want, d)
			}
			return got
		}
		time.Sleep(time.Second)
	}
}

func waitForProbe() {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(1500 * time.Millisecond)
		var s sourceDoc
		driverlib.GetJSON("/source", "source", &s)
		if s.Probed && len(s.Tracks) == 3 {
			fmt.Printf("probed: %d audio tracks, video %dx%d\n",
				len(s.Tracks), s.Video.Width, s.Video.Height)
			facts["SOURCE_WIDTH"] = strconv.Itoa(s.Video.Width)
			facts["SOURCE_HEIGHT"] = strconv.Itoa(s.Video.Height)
			return
		}
	}
	die("engine never probed 3 audio tracks")
}

func waitForRunning(want int) {
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(1500 * time.Millisecond)
		st := status()
		running := 0
		for _, d := range st.Destinations {
			if d.Process != nil && d.Process.State == "running" {
				running++
			}
		}
		if running == want {
			for _, d := range st.Destinations {
				via := d.RenditionName
				if via == "" {
					via = "passthrough"
				}
				fmt.Printf("  %-36s %-22s via %s\n", d.Name, d.Summary, via)
			}
			return
		}
	}
	die("destinations never all reached running (wanted %d)", want)
}

// ------------------------------------------------------------------ helpers

func f2(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }

func boolStr(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// writeFacts flushes everything measured so far to the file the shell sources.
//
// Sorted, so two runs of the same suite produce diffable files -- which is how
// the CPU figures get compared across runs at all.
func writeFacts() {
	if factsFile == "" {
		return
	}
	keys := make([]string, 0, len(facts))
	for k := range facts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%q\n", k, facts[k])
	}
	if err := os.WriteFile(factsFile, []byte(b.String()), 0o644); err != nil {
		fmt.Printf("WARNING: cannot write facts: %v\n", err)
	}
}

// die reports a driver-level failure, having first flushed what it measured.
//
// THE FLUSH IS THE POINT and it is why this is not simply driverlib.Die.
// os.Exit skips deferred calls, so without writing the facts here a failing run
// would tell the shell script nothing about how far it got -- and the shell
// distinguishes "the driver aborted" from "the assertion failed" precisely so a
// broken harness is not reported as a broken product.
func die(f string, a ...any) {
	msg := fmt.Sprintf(f, a...)
	facts["DRIVER_FAILED"] = msg
	writeFacts()
	// driverlib.Die's "driver: " prefix is load-bearing across the suites, so
	// the exit goes through it rather than around it.
	driverlib.Die(msg)
}
