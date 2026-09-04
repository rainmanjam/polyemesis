//go:build ignore

// Run a broadcast long enough for the slow faults to show, and watch the things
// that only move slowly.
//
// #380's DURATION gap: "the longest suite is 75s; real broadcasts run for hours.
// Backoff crawl toward the 30s ceiling, memory growth, disk fill and reconnect
// churn are duration bugs and nothing would currently see them."
//
// Every one of those is invisible to a 75-second suite by construction. A leak
// of 200 kB a minute is 12 MB an hour and 0.25 MB in 75 seconds, which is
// indistinguishable from allocator noise. A backoff that doubles on each
// failure needs several failures to reach the ceiling. Reconnect churn is a
// COUNT that only becomes a rate once there is enough time to divide by.
//
// WHAT MAKES A DURATION TEST WORTHLESS, and it is the reason for the first
// check below rather than an afterthought: a long run that measures a system
// which was not doing anything. Every trend here reads flat on an idle server
// -- no memory growth, no restarts, no disk growth -- so the suite would report
// perfect health for a broadcast that died in its first minute. The concurrency
// suite next door learned the same lesson from a per-destination cost that
// halved beautifully because the processes had died. So this asserts DELIVERY
// FIRST: the relay's byte counter must advance across every single sample, and
// nothing else is believed until it has.
//
// TRENDS, NOT POINT VALUES. Peak RSS is a property of the machine; RSS that
// climbs monotonically across an hour is a property of the code. The checks are
// on slope and on growth between the first and last thirds, so a big machine and
// a small one can run the same suite and disagree only about the constant.
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

// sample is one observation of everything that moves slowly.
type sample struct {
	at time.Time
	// rxBytes is the relay's cumulative received bytes. THE LIVENESS SIGNAL:
	// every other number here is meaningless if this stops advancing.
	rxBytes int64
	// rssKB is the server process's resident set. The leak signal.
	rssKB int64
	// restarts is every restart the server reports, ingest plus destinations,
	// summed. The churn signal.
	restarts int
	// dataKB is the data directory on disk. The fill signal.
	dataKB int64
	// destsUp is how many destinations report a running process.
	destsUp int
}

// statusDoc is the subset of engine.Status this suite trends on.
//
// READ OFF THE REAL TYPE, after the first version of this file was written
// against the wrong one. `metrics.Snapshot` has an `Ingests` slice with the
// relay nested inside each entry; `/status` returns `engine.Status`, where the
// relay is TOP LEVEL and the ingest is singular. Decoding one as the other is
// silent -- encoding/json leaves absent fields zero -- so the whole run
// reported rxBytes of 0 and looked exactly like a broadcast that never
// started. The delivery check caught it, which is the entire reason that check
// runs before the others.
type statusDoc struct {
	Ingest *struct {
		State    string `json:"state"`
		Restarts int    `json:"restarts"`
	} `json:"ingest,omitempty"`
	Relay struct {
		RxBytes uint64 `json:"rxBytes"`
	} `json:"relay"`
	Destinations []struct {
		Name    string `json:"name"`
		Process *struct {
			State    string `json:"state"`
			Restarts int    `json:"restarts"`
		} `json:"process,omitempty"`
	} `json:"destinations"`
}

func main() {
	if len(os.Args) < 7 {
		die("usage: acceptance_duration_driver.go <http-port> <relay-port> <facts-file> <minutes> <dest-count> <data-dir>")
	}
	port, relay := os.Args[1], os.Args[2]
	factsFile = os.Args[3]
	minutes, err := strconv.ParseFloat(os.Args[4], 64)
	if err != nil || minutes < 1 {
		die("minutes %q must be a number >= 1", os.Args[4])
	}
	nDest, err := strconv.Atoi(os.Args[5])
	if err != nil || nDest < 1 {
		die("dest-count %q must be an integer >= 1", os.Args[5])
	}
	// ABSOLUTE, AND PASSED IN, because this driver runs from the repo root
	// rather than from the workdir -- `go run` resolves module imports against
	// the current directory's go.mod, so the suite cd's to $ROOT before
	// invoking it. A relative "./data" here therefore measured $ROOT/data,
	// which does not exist, and the disk-fill check passed with +0 kB on every
	// run while the destinations wrote megabytes. Vacuous, and it would have
	// stayed vacuous: disk fill is one of the four faults this suite claims to
	// watch for, and it was watching the wrong directory.
	dataDir := os.Args[6]
	if fi, err := os.Stat(dataDir); err != nil || !fi.IsDir() {
		die("data dir %q is not a readable directory; the disk trend would be a "+
			"constant zero and every run would pass check 6 without measuring it", dataDir)
	}

	ffmpegBin = toolPath("ffmpeg")
	psBin = toolPath("ps")

	driverlib.Init("http://127.0.0.1:" + port)
	defer writeFacts()
	driverlib.WaitUp()
	driverlib.Setup("admin", "acceptance-pw")
	sourceID = driverlib.EnsureSource("Main")

	relayPort, err := driverlib.ResolveRelayPort(relay, func(p string) map[string]any {
		var doc map[string]any
		driverlib.GetJSON(p, "status", &doc)
		return doc
	})
	if err != nil {
		die("resolve relay port: %v", err)
	}

	// The publisher outlives the measurement window by a margin, so the run
	// ends because the suite decided to and not because the source ran out.
	// A source that stops early would show as a delivery failure and blame the
	// product for the harness.
	srcSeconds := int(minutes*60) + 60
	src := exec.Command(ffmpegBin, "-hide_banner", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-map", "0:v", "-map", "1:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-g", "60", "-b:v", "3000k", "-c:a", "aac", "-b:a", "128k",
		"-metadata", "comment=acceptance-duration-source",
		"-t", strconv.Itoa(srcSeconds),
		"-f", "mpegts", "-flush_packets", "1",
		fmt.Sprintf("udp://127.0.0.1:%d?pkt_size=1316", relayPort))
	if err := src.Start(); err != nil {
		die("start source: %v", err)
	}
	defer func() {
		_ = src.Process.Kill()
		_ = src.Wait()
	}()

	for i := 1; i <= nDest; i++ {
		driverlib.CreateDest(fmt.Sprintf("dur-%d", i), map[string]any{
			"sourceId": sourceID,
			"name":     fmt.Sprintf("dur-%d", i), "kind": "file", "platform": "custom",
			"url": fmt.Sprintf("dur-%d.mkv", i), "enabled": true, "audioBitrate": 160,
			"profile": map[string]any{
				"mode": "simple", "tracks": driverlib.Sel(0),
				"normalize": "auto", "sampleRate": 48000,
			},
		})
	}

	srvPID := serverPID(port)
	if srvPID == "" {
		die("could not find the server process; every trend here is read off it")
	}

	// Settle before the first sample. Startup allocates, probes and opens
	// files, and a first sample taken inside that would make the whole run look
	// like a leak that stopped.
	fmt.Printf("settling, then sampling for %.0f minute(s)\n", minutes)
	time.Sleep(20 * time.Second)

	const every = 15 * time.Second
	deadline := time.Now().Add(time.Duration(minutes * float64(time.Minute)))
	var samples []sample
	for time.Now().Before(deadline) {
		samples = append(samples, observe(srvPID, dataDir))
		time.Sleep(every)
	}
	samples = append(samples, observe(srvPID, dataDir))

	if len(samples) < 4 {
		die("only %d samples in %.1f minutes; the window is too short to trend", len(samples), minutes)
	}
	report(samples, nDest)
}

func observe(srvPID, dataDir string) sample {
	var st statusDoc
	driverlib.GetJSON("/status", "status", &st)

	s := sample{at: time.Now(), rssKB: rssKB(srvPID), dataKB: dirKB(dataDir)}
	s.rxBytes = int64(st.Relay.RxBytes)
	if st.Ingest != nil {
		s.restarts += st.Ingest.Restarts
	}
	for _, d := range st.Destinations {
		if d.Process == nil {
			continue
		}
		s.restarts += d.Process.Restarts
		if d.Process.State == "running" {
			s.destsUp++
		}
	}
	return s
}

func report(s []sample, nDest int) {
	first, last := s[0], s[len(s)-1]
	span := last.at.Sub(first.at).Minutes()

	// ------------------------------------------------- delivery, first
	//
	// Every sample, not just the endpoints. A run that stalled in the middle
	// and recovered has still had a stall, and endpoints alone would hide it --
	// which is the shape of the reconnect churn this suite is looking for.
	stalls := 0
	for i := 1; i < len(s); i++ {
		if s[i].rxBytes <= s[i-1].rxBytes {
			stalls++
		}
	}
	fmt.Printf("\n  samples            %d over %.1f min\n", len(s), span)
	fmt.Printf("  relay rx           %.1f MB -> %.1f MB\n",
		float64(first.rxBytes)/1e6, float64(last.rxBytes)/1e6)
	fmt.Printf("  intervals stalled  %d of %d\n", stalls, len(s)-1)

	// ------------------------------------------------- the slow trends
	rssGrowth := last.rssKB - first.rssKB
	rssPerMin := 0.0
	if span > 0 {
		rssPerMin = float64(rssGrowth) / span
	}
	// Thirds, because a leak is monotonic and a warm-up is not. A cache filling
	// in the first minute shows as growth between the first and second third
	// and nothing after; a leak shows in both.
	third := len(s) / 3
	early := s[third].rssKB - s[0].rssKB
	late := s[len(s)-1].rssKB - s[len(s)-1-third].rssKB

	// THE RATE THAT MATTERS IS THE ONE AFTER WARM-UP. A server's first minute
	// allocates caches, opens files and starts children; including it makes the
	// overall kB/min a measure of startup, which on a short run dominates
	// completely -- a two-minute run read 1535 kB/min overall against 912 kB
	// across its whole last third. Gating on the overall figure would therefore
	// need a ceiling loose enough to be worthless, or would fail on run LENGTH
	// rather than on a leak. This is the slope over the last two thirds.
	tailFrom := s[third]
	tailMin := last.at.Sub(tailFrom.at).Minutes()
	tailPerMin := 0.0
	if tailMin > 0 {
		tailPerMin = float64(last.rssKB-tailFrom.rssKB) / tailMin
	}

	fmt.Printf("  server rss         %d kB -> %d kB (%+d kB, %+.0f kB/min)\n",
		first.rssKB, last.rssKB, rssGrowth, rssPerMin)
	fmt.Printf("  rss first third    %+d kB\n", early)
	fmt.Printf("  rss last third     %+d kB\n", late)
	fmt.Printf("  rss after warm-up  %+.0f kB/min over %.1f min\n", tailPerMin, tailMin)
	fmt.Printf("  restarts           %d -> %d\n", first.restarts, last.restarts)
	fmt.Printf("  data dir           %d kB -> %d kB (%+d kB)\n",
		first.dataKB, last.dataKB, last.dataKB-first.dataKB)
	fmt.Printf("  destinations up    %d of %d at the end\n", last.destsUp, nDest)

	facts["DUR_SAMPLES"] = strconv.Itoa(len(s))
	facts["DUR_MINUTES"] = fmt.Sprintf("%.2f", span)
	facts["DUR_STALLS"] = strconv.Itoa(stalls)
	facts["DUR_RX_START"] = strconv.FormatInt(first.rxBytes, 10)
	facts["DUR_RX_END"] = strconv.FormatInt(last.rxBytes, 10)
	facts["DUR_RSS_START_KB"] = strconv.FormatInt(first.rssKB, 10)
	facts["DUR_RSS_END_KB"] = strconv.FormatInt(last.rssKB, 10)
	facts["DUR_RSS_PER_MIN_KB"] = fmt.Sprintf("%.1f", rssPerMin)
	facts["DUR_RSS_EARLY_KB"] = strconv.FormatInt(early, 10)
	facts["DUR_RSS_LATE_KB"] = strconv.FormatInt(late, 10)
	facts["DUR_RSS_TAIL_PER_MIN_KB"] = fmt.Sprintf("%.1f", tailPerMin)
	facts["DUR_RESTARTS_START"] = strconv.Itoa(first.restarts)
	facts["DUR_RESTARTS_END"] = strconv.Itoa(last.restarts)
	facts["DUR_DATA_GROWTH_KB"] = strconv.FormatInt(last.dataKB-first.dataKB, 10)
	facts["DUR_DESTS_UP_END"] = strconv.Itoa(last.destsUp)
	facts["DUR_DESTS_WANTED"] = strconv.Itoa(nDest)
}

// rssKB reads the server's resident set. `rss` is in kilobytes on both
// platforms this runs on.
func rssKB(pid string) int64 {
	out, err := exec.Command(psBin, "-o", "rss=", "-p", pid).Output()
	if err != nil {
		return 0
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func dirKB(path string) int64 {
	out, err := exec.Command("du", "-sk", path).Output()
	if err != nil {
		return 0
	}
	f := strings.Fields(string(out))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseInt(f[0], 10, 64)
	return v
}

func serverPID(port string) string {
	out, err := exec.Command("pgrep", "-f", "polyemesis -addr :"+port).Output()
	if err != nil {
		return ""
	}
	f := strings.Fields(string(out))
	if len(f) == 0 {
		return ""
	}
	return f[0]
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
