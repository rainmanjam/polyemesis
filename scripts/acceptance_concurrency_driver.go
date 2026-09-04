//go:build ignore

// Measure what a destination costs, and whether N of them cost N times it.
//
// #380 names three gaps. This is the CONCURRENCY one: "N sources x M
// destinations is unmeasured; the ~4% of a core per destination figure has no
// test behind it." That figure is published in README.md and docs/COMPARISON.md
// and a reader sizes a box with it.
//
// THE TRAP THIS SUITE EXISTS TO AVOID, and it is not hypothetical -- a first
// attempt at measuring this by hand produced a beautiful, entirely false
// result:
//
//	1 destination:  3.80% of a core
//	2 destinations: 2.23% each
//	4 destinations: 1.11% each
//	8 destinations: 0.55% each
//
// Per-destination cost halving with every doubling is a wonderful headline and
// it was an artefact. Every one of those runs had ONE surviving process:
// several ffmpeg readers on a single UDP unicast socket compete for packets and
// all but one die, so the total was one survivor's CPU divided by N. The shape
// of the lie is the shape of the thing you want to be true, which is what makes
// it dangerous.
//
// So this driver asserts LIVENESS FIRST and treats the cost as meaningless
// without it. Every check below that reports a number is preceded by a check
// that the processes producing it are still running. The product avoids the
// competing-reader problem properly -- internal/relay.Hub gives each
// destination its own subscription port -- which is also why this measures
// through the real server rather than against raw ffmpeg.
//
// WHAT IT ASSERTS, and deliberately not a percentage. "4% of a core" is a
// property of a machine, not of this code: the same destination measured 8.8%
// on Apple silicon against a published 4% on a six-core VPS, and neither is
// wrong. A suite that pinned a number would fail on hardware rather than on
// regressions. What is stable across machines is the SHAPE:
//
//   - every destination that was asked for is still running at the end
//   - every one of them burned measurable CPU (a destination costing nothing is
//     a destination doing nothing)
//   - the Nth costs about what the first did -- linearity, within a factor
//
// The absolute figure is REPORTED on every run, because it is what the README
// claims and somebody should be able to read it off a CI log without building
// a harness first.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rainmanjam/polyemesis/scripts/internal/driverlib"
)

var (
	ffmpegBin string
	psBin     string
	sourceID  int64
	factsFile string
	facts     = map[string]string{}
)

func main() {
	if len(os.Args) < 5 {
		die("usage: acceptance_concurrency_driver.go <http-port> <relay-port> <facts-file> <n-destinations>")
	}
	port, relay := os.Args[1], os.Args[2]
	factsFile = os.Args[3]
	n, err := strconv.Atoi(os.Args[4])
	if err != nil || n < 2 {
		die("destination count %q must be an integer >= 2", os.Args[4])
	}

	ffmpegBin = toolPath("ffmpeg")
	psBin = toolPath("ps")

	driverlib.Init("http://127.0.0.1:" + port)
	defer writeFacts()
	driverlib.WaitUp()
	driverlib.Setup("admin", "acceptance-pw")
	sourceID = driverlib.EnsureSource("Main")

	// Recording and metering off, for the same reason the ladder suite turns
	// them off: both spawn FFmpeg of their own, and this suite's whole number
	// is what the DESTINATIONS cost. A recorder competing for the same cores
	// moves it for a reason that has nothing to do with concurrency.
	settings := driverlib.LoadSettings()
	if rec, ok := settings["recording"].(map[string]any); ok {
		rec["enabled"] = false
	}
	if m, ok := settings["meters"].(map[string]any); ok {
		m["enabled"] = false
	}
	driverlib.SaveSettings(settings, "recording and meters off")

	relayPort, err := driverlib.ResolveRelayPort(relay, func(p string) map[string]any {
		var doc map[string]any
		driverlib.GetJSON(p, "status", &doc)
		return doc
	})
	if err != nil {
		die("resolve relay port: %v", err)
	}

	// -t bounds the publisher rather than trusting the teardown: a source that
	// outlives a failed run keeps a port bound and the next run cannot start.
	src := exec.Command(ffmpegBin, "-hide_banner", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-map", "0:v", "-map", "1:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-g", "60", "-b:v", "3000k", "-c:a", "aac", "-b:a", "128k",
		"-metadata", "comment=acceptance-concurrency-source", "-t", "300",
		"-f", "mpegts", "-flush_packets", "1",
		fmt.Sprintf("udp://127.0.0.1:%d?pkt_size=1316", relayPort))
	if err := src.Start(); err != nil {
		die("start source: %v", err)
	}
	defer func() {
		_ = src.Process.Kill()
		_ = src.Wait()
	}()
	fmt.Printf("source publishing to udp/%d\n", relayPort)

	// ---------------------------------------------------------------- one
	//
	// The baseline every later number is read against. Measured FIRST and on
	// its own, because a per-destination cost is only meaningful next to the
	// cost of one.
	newDest("conc-1", "conc-1.mkv")
	one := measure("1 destination", 1)

	// ---------------------------------------------------------------- N
	for i := 2; i <= n; i++ {
		newDest(fmt.Sprintf("conc-%d", i), fmt.Sprintf("conc-%d.mkv", i))
	}
	many := measure(fmt.Sprintf("%d destinations", n), n)

	// ------------------------------------------------------------- verdict
	perOne := one.cores
	perMany := many.cores / float64(n)
	ratio := 0.0
	if perOne > 0 {
		ratio = perMany / perOne
	}

	fmt.Printf("\n  cost of one destination:        %.4f cores (%.2f%% of a core)\n", perOne, perOne*100)
	fmt.Printf("  cost of %d, per destination:     %.4f cores (%.2f%% of a core)\n", n, perMany, perMany*100)
	fmt.Printf("  linearity ratio (per-N / per-1): %.2f\n", ratio)

	facts["CONC_N"] = strconv.Itoa(n)
	facts["CONC_ALIVE_1"] = strconv.Itoa(one.alive)
	facts["CONC_ALIVE_N"] = strconv.Itoa(many.alive)
	facts["CONC_CORES_1"] = fmt.Sprintf("%.5f", one.cores)
	facts["CONC_CORES_N"] = fmt.Sprintf("%.5f", many.cores)
	facts["CONC_PER_1_PCT"] = fmt.Sprintf("%.2f", perOne*100)
	facts["CONC_PER_N_PCT"] = fmt.Sprintf("%.2f", perMany*100)
	facts["CONC_RATIO"] = fmt.Sprintf("%.3f", ratio)
	facts["CONC_ZERO_CPU_N"] = strconv.Itoa(many.zero)
}

type reading struct {
	// alive is how many destination children were still running at the END of
	// the window. It is reported before cost everywhere, because a cost
	// computed over dead processes is the false result this suite exists to
	// refuse.
	alive int
	// zero counts survivors that burned no measurable CPU across the window.
	zero int
	// cores is the total, summed across every destination child.
	cores float64
}

// measure samples every destination child over a window and returns what they
// cost, having first established that they are all still there.
//
// `time` rather than `%cpu`, which is the difference between a measurement and
// a guess -- %cpu on both platforms averages over the process's whole LIFETIME,
// so a child that has been up a minute barely moves it. The ladder driver
// records the same reasoning; this is the same technique against a different
// population.
func measure(label string, want int) reading {
	// Settle before sampling. A destination's first seconds are spent probing
	// its relay subscription, which costs more than steady state and would
	// flatter or penalise whichever count is measured first.
	time.Sleep(8 * time.Second)

	before := destCPU()
	if len(before) < want {
		fmt.Printf("  %-16s FAILED to start: %d of %d destination children exist\n",
			label, len(before), want)
	}
	const window = 20 * time.Second
	t0 := time.Now()
	time.Sleep(window)
	after := destCPU()
	elapsed := time.Since(t0).Seconds()

	r := reading{alive: len(after)}
	// SURVIVORS ONLY, and pids present in BOTH samples. A child that died
	// mid-window contributed real CPU to `before` and nothing to `after`;
	// counting it would credit the survivors with its work and understate the
	// per-destination cost -- which is the exact direction the false result
	// went.
	var pids []string
	for pid := range after {
		if _, ok := before[pid]; ok {
			pids = append(pids, pid)
		}
	}
	sort.Strings(pids)
	for _, pid := range pids {
		d := after[pid] - before[pid]
		if d <= 0 {
			r.zero++
			continue
		}
		r.cores += d / elapsed
	}

	fmt.Printf("  %-16s asked for %d, alive %d, measured %d, %.4f cores total\n",
		label, want, r.alive, len(pids), r.cores)
	return r
}

// destCPU returns cumulative CPU seconds for every destination child, by pid.
//
// Matched on the output path this suite gives its destinations, which is what
// separates them from the ingest, the source publisher and anything else on the
// machine. Matching on "ffmpeg" alone would sweep in the publisher started
// above and roughly double every number here.
func destCPU() map[string]float64 {
	out := map[string]float64{}
	for _, r := range psProcs() {
		if strings.Contains(r.args, "conc-") && strings.Contains(r.args, ".mkv") &&
			strings.Contains(r.args, "ffmpeg") {
			out[r.pid] = r.cpu
		}
	}
	return out
}

type psRow struct {
	pid  string
	cpu  float64
	args string
}

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
		rows = append(rows, psRow{pid: f[0], cpu: parseCPUTime(f[1]), args: strings.Join(f[2:], " ")})
	}
	return rows
}

// parseCPUTime reads ps's TIME column on both platforms this runs on: darwin
// prints MM:SS.CC and Linux prints [DD-]HH:MM:SS, so the fractional part exists
// on one and not the other and the number of colons differs. Parsed from the
// RIGHT, which is the one reading correct for both.
func parseCPUTime(s string) float64 {
	if i := strings.Index(s, "-"); i >= 0 {
		days, _ := strconv.ParseFloat(s[:i], 64)
		return days*86400 + parseCPUTime(s[i+1:])
	}
	parts := strings.Split(s, ":")
	mult, total := 1.0, 0.0
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

// newDest creates one file destination through the API the UI uses.
//
// Through driverlib.CreateDest rather than a hand-rolled POST, so this suite
// inherits the sourceId handling every other driver has: the server refuses a
// create that does not name its programme, and filling that in each caller is
// how the other suites drifted.
func newDest(name, file string) {
	driverlib.CreateDest(name, map[string]any{
		"sourceId": sourceID,
		"name":     name, "kind": "file", "platform": "custom", "url": file,
		"enabled": true, "audioBitrate": 160,
		"profile": map[string]any{
			"mode": "simple", "tracks": driverlib.Sel(0),
			"normalize": "auto", "sampleRate": 48000,
		},
	})
}

func toolPath(name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		die("%s is required: %v", name, err)
	}
	return p
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
		fmt.Fprintf(os.Stderr, "write facts: %v\n", err)
	}
}

func die(format string, a ...any) {
	facts["DRIVER_FAILED"] = fmt.Sprintf(format, a...)
	writeFacts()
	fmt.Fprintf(os.Stderr, "driver: "+format+"\n", a...)
	os.Exit(1)
}
