//go:build ignore

// Driver for scripts/acceptance-multistream.sh.
//
// The product's core promise is one ingest fanned out to several platforms at
// once, each of them receiving the mix ITS OWN routing.Profile names. The
// dangerous failure is not "a destination went down" -- that is loud. It is
// per-destination routing quietly sending the SAME mix everywhere, which from
// the sending side is indistinguishable from success: every process is up,
// every platform is ingesting, every card is green.
//
// So this driver exists to answer two questions the shell cannot ask on its
// own, and to answer them without ever putting a credential on a command line.
//
// THE CREDENTIAL NEVER TOUCHES ARGV. adddest takes the NAME OF AN ENVIRONMENT
// VARIABLE, not a key. This process reads the value with os.Getenv and puts it
// straight into a JSON body over loopback. Process arguments are world-readable
// through ps(1) on every platform this ships to, so a key spelled on a command
// line is disclosed to every local user for as long as the process lives -- and
// to anything that scrapes ps, which on a build machine is most of CI. The
// product already draws this line (engine.SecretSet, engine/secrets.go); a
// harness that measures the product must not be the thing that breaks it.
//
// THE COMPILED SELECTION IS READABLE, AND THAT IS THE HALF THAT SURVIVES A REAL
// PLATFORM. engine.DestStatus carries Tracks and FilterComplex -- what routing
// actually compiled for this destination -- so "twitch was sent track 0 and
// youtube was sent track 1" is a measurement even when the far end is Twitch
// and nothing local can hear what arrived. The received-audio half needs a sink
// we control, which is what the dry-run path is for.
//
//	setup <rtmp-port>        first-run setup; put the ingest on that RTMP port
//	publishkey               print the source's publish token
//	srctracks                print how many AUDIO tracks the ingest was probed with
//	adddest <name> <platform> <url> <key-env> <tracks-csv>
//	                         create one RTMP destination whose profile selects
//	                         exactly those ingest tracks. The key is read from
//	                         the named environment variable, never from argv.
//	tracks <name>            print the compiled track selection ("0", "0,1", "-")
//	graph <name>             print the compiled filter_complex on one line
//	deststat <name>          print "<state> <restarts> <outTimeMs>"
//	stopall                  stop every destination so its far end finalises
//	leakscan <key-env>...    fetch every read-reachable rendering and report
//	                         SAFE, or LEAK <env-var> <endpoint>
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// Named because Sonar counts a literal repeated three times as a
	// maintenance hazard, and it is right here: a typo in one of three
	// copies of a path is a driver that talks to the wrong endpoint on one
	// code path only.
	settingsPath = "/settings"
	noSuchDest   = "no destination named "
	user         = "admin"
	pass         = "MultistreamAcceptance!7q"
	// profileTracks is how many track rows a profile declares. The ingest this
	// suite publishes carries two; six is what OBS sends and what
	// routing.PlaceholderTracks guesses, so a profile of that width is the
	// ordinary shape rather than one tailored to the fixture. Rows past the
	// ingest's width compile to nothing.
	profileTracks = 6
)

var (
	client *http.Client
	base   string
	csrf   string
)

func main() {
	if len(os.Args) < 3 {
		die("usage: acceptance_multistream_driver.go <base-url> <subcommand> [args]")
	}
	base = strings.TrimSuffix(os.Args[1], "/") + "/api/v1"
	jar, _ := cookiejar.New(nil)
	client = &http.Client{Jar: jar, Timeout: 30 * time.Second}
	waitUp()

	args := os.Args[3:]
	switch os.Args[2] {
	case "setup":
		if len(args) < 1 {
			die("usage: setup <rtmp-port>")
		}
		setup()
		ingest(args[0])
	case "publishkey":
		login()
		publishKey()
	case "srctracks":
		login()
		srcTracks()
	case "adddest":
		if len(args) < 5 {
			die("usage: adddest <name> <platform> <url> <key-env> <tracks-csv>")
		}
		login()
		addDest(args[0], args[1], args[2], args[3], args[4])
	case "tracks":
		if len(args) < 1 {
			die("usage: tracks <name>")
		}
		login()
		printTracks(args[0])
	case "graph":
		if len(args) < 1 {
			die("usage: graph <name>")
		}
		login()
		printGraph(args[0])
	case "deststat":
		if len(args) < 1 {
			die("usage: deststat <name>")
		}
		login()
		destStat(args[0])
	case "stopall":
		login()
		stopAll()
	case "leakscan":
		if len(args) < 1 {
			die("usage: leakscan <key-env>...")
		}
		login()
		leakScan(args)
	default:
		die("unknown subcommand " + os.Args[2])
	}
}

func waitUp() {
	for i := 0; i < 60; i++ {
		resp, err := client.Get(base + "/health")
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(time.Second)
	}
	die("server never became healthy at " + base)
}

func do(method, path string, body any) (int, []byte) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, base+path, rdr)
	if err != nil {
		die(err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	for _, c := range client.Jar.Cookies(req.URL) {
		if c.Name == "polyemesis_csrf" {
			csrf = c.Value
		}
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	resp, err := client.Do(req)
	if err != nil {
		die(err.Error())
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func login() {
	if code, out := do(http.MethodPost, "/auth/login",
		map[string]string{"username": user, "password": pass}); code != http.StatusOK {
		die(fmt.Sprintf("login failed: %d %s", code, out))
	}
}

func setup() {
	code, out := do(http.MethodPost, "/setup", map[string]string{"username": user, "password": pass})
	if code != http.StatusOK && code != http.StatusCreated {
		die(fmt.Sprintf("setup failed: %d %s", code, out))
	}
	fmt.Println("SETUP_OK")
}

// ingest puts the install's shared RTMP listener on a port of this suite's own.
//
// RTMP rather than SRT for the reason acceptance-failover.sh gives: the host
// FFmpeg on macOS is built without libsrt, so it can neither listen nor publish
// on SRT, and this suite has to run on a developer's machine before it is ever
// pointed at a real platform. What is measured -- which mix each destination
// receives -- is independent of how the bytes arrived.
//
// Read-modify-write of the whole settings document. PUT /settings REPLACES the
// settings, so posting a lone ingest block would reset everything else.
func ingest(rtmpPort string) {
	port, err := strconv.Atoi(rtmpPort)
	if err != nil {
		die("rtmp port is not a number: " + rtmpPort)
	}
	_, out := do(http.MethodGet, settingsPath, nil)
	var s map[string]any
	if err := json.Unmarshal(out, &s); err != nil {
		die("settings unreadable: " + err.Error())
	}
	ing, _ := s["ingest"].(map[string]any)
	if ing == nil {
		die("settings carried no ingest block")
	}
	ing["mode"] = "rtmp"
	rtmp, _ := ing["rtmp"].(map[string]any)
	if rtmp == nil {
		rtmp = map[string]any{}
		ing["rtmp"] = rtmp
	}
	rtmp["app"] = "live"
	// Empty: the listener addresses sources by the per-source publish TOKEN
	// now, which publishkey below reads back. See the failover suite's note --
	// a keyless publish reaches nothing.
	rtmp["streamKey"] = ""
	s["listeners"] = map[string]any{"srtPort": 6000, "rtmpPort": port}
	code, body := do(http.MethodPut, settingsPath, s)
	if code != http.StatusOK {
		die(fmt.Sprintf("ingest settings failed: %d %s", code, body))
	}
	fmt.Println("INGEST_OK")
}

func publishKey() {
	code, out := do(http.MethodGet, "/sources", nil)
	if code != http.StatusOK {
		die(fmt.Sprintf("cannot read sources: %d %s", code, out))
	}
	var rows []struct {
		Source struct {
			Token string `json:"token"`
		} `json:"source"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		die("sources unreadable: " + err.Error())
	}
	for _, r := range rows {
		if t := r.Source.Token; t != "" {
			fmt.Println(t)
			return
		}
		if r.Token != "" {
			fmt.Println(r.Token)
			return
		}
	}
	die("no source carried a publish token")
}

// srcTracks prints how many AUDIO tracks the running ingest was probed with.
//
// It exists because every per-destination assertion downstream is vacuous
// without it. A profile that selects track 1 on an ingest that carries only
// track 0 compiles to a graph that quietly routes track 0 instead, or to
// nothing at all -- and either way "youtube received a different mix from
// twitch" stops being a statement about routing and becomes a statement about
// the publisher. The suite refuses to interpret anything until this reads 2.
func srcTracks() {
	st := readStatus()
	fmt.Println(len(st.Source.Tracks))
}

// sel builds a profile's track rows with exactly the named tracks enabled.
func sel(on []int) []map[string]any {
	want := map[int]bool{}
	for _, t := range on {
		want[t] = true
	}
	rows := make([]map[string]any, 0, profileTracks)
	for i := 0; i < profileTracks; i++ {
		rows = append(rows, map[string]any{"track": i, "enabled": want[i], "gain": 1.0})
	}
	return rows
}

func parseTracks(csv string) []int {
	var out []int
	for _, f := range strings.Split(csv, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		n, err := strconv.Atoi(f)
		if err != nil {
			die("not a track index: " + f)
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		die("a destination with no tracks selected would prove nothing")
	}
	return out
}

// addDest creates one RTMP destination.
//
// keyEnv NAMES an environment variable; the value is read here. That is the
// whole point of this subcommand existing rather than the shell POSTing JSON
// with curl: a key interpolated into a shell command line is visible in ps(1)
// to every user on the machine for as long as the command runs, and a key in a
// heredoc is visible in the shell's own /proc entry and in any xtrace output.
// os.Getenv is the only path in this file that touches the value.
//
// NORMALISATION OFF, deliberately. A limiter between the ingest and the
// platform would compress exactly the level difference every assertion in this
// suite reads, so a routing fault that sent the wrong tracks could be masked by
// the very stage meant to make the mix consistent.
func addDest(name, platform, url, keyEnv, tracksCSV string) {
	key := os.Getenv(keyEnv)
	if strings.TrimSpace(key) == "" {
		die(keyEnv + " is empty; a destination with no credential is not a measurement")
	}
	code, out := do(http.MethodPost, "/destinations", map[string]any{
		"name": name, "kind": "rtmp", "platform": platform,
		"url": url, "streamKey": key,
		"enabled": true, "audioBitrate": 160,
		"profile": map[string]any{
			"mode": "simple", "tracks": sel(parseTracks(tracksCSV)), "matrix": []any{},
			"normalize": "off", "sampleRate": 48000,
		},
	})
	if code != http.StatusOK && code != http.StatusCreated {
		// The body is the server's, which scrubs its own credentials before it
		// renders anything; printing it is how a validation failure becomes
		// readable. Nothing here echoes `key`.
		die(fmt.Sprintf("create destination %s failed: %d %s", name, code, out))
	}
	fmt.Println("DEST_OK")
}

// destProcess is the supervised child as /status reports it. Named rather than
// nested inline: three fields deep in an anonymous struct is where a reader
// stops being able to say what shape the endpoint actually returns.
type destProcess struct {
	State    string `json:"state"`
	Restarts int    `json:"restarts"`
	Progress struct {
		OutTimeMS int64 `json:"outTimeMs"`
	} `json:"progress"`
}

type statusDoc struct {
	Source struct {
		Tracks []struct {
			Index    int    `json:"index"`
			Channels int    `json:"channels"`
			Codec    string `json:"codec"`
		} `json:"tracks"`
	} `json:"source"`
	Destinations []struct {
		ID            int64        `json:"id"`
		Name          string       `json:"name"`
		Tracks        []int        `json:"tracks"`
		FilterComplex string       `json:"filterComplex"`
		Error         string       `json:"error,omitempty"`
		Process       *destProcess `json:"process"`
	} `json:"destinations"`
}

func readStatus() statusDoc {
	code, out := do(http.MethodGet, "/status", nil)
	if code != http.StatusOK {
		die(fmt.Sprintf("cannot read status: %d %s", code, out))
	}
	var st statusDoc
	if err := json.Unmarshal(out, &st); err != nil {
		die("status unreadable: " + err.Error())
	}
	return st
}

// printTracks prints the COMPILED selection, not the stored profile.
//
// The stored profile is what was asked for; the compiled selection is what
// routing.Compile decided after seeing the real ingest, and only the second one
// can be wrong in the way this suite is looking for. A track a profile names
// but the ingest does not carry is dropped here and nowhere else, so reading
// the request back would report agreement with itself.
//
// "-" means the destination exists and compiled to no tracks at all, which is a
// finding and must not print as an empty line the shell would read as absence.
func printTracks(name string) {
	for _, d := range readStatus().Destinations {
		if d.Name != name {
			continue
		}
		if len(d.Tracks) == 0 {
			fmt.Println("-")
			return
		}
		ts := append([]int(nil), d.Tracks...)
		sort.Ints(ts)
		parts := make([]string, 0, len(ts))
		for _, t := range ts {
			parts = append(parts, strconv.Itoa(t))
		}
		fmt.Println(strings.Join(parts, ","))
		return
	}
	die(noSuchDest + name)
}

// printGraph prints the compiled filter_complex on ONE line.
//
// Newlines are collapsed rather than preserved because the shell compares this
// as a string, and a multi-line value would make `[ "$a" = "$b" ]` compare only
// what the command substitution kept.
func printGraph(name string) {
	for _, d := range readStatus().Destinations {
		if d.Name == name {
			g := strings.Join(strings.Fields(d.FilterComplex), " ")
			if g == "" {
				g = "-"
			}
			fmt.Println(g)
			return
		}
	}
	die(noSuchDest + name)
}

// destStat prints "<state> <restarts> <outTimeMs>".
//
// "none -1 -1" for a destination with no process at all, which is a different
// finding from "running, restarted 0 times, produced 0 ms" and must not be
// collapsed into it -- the failover suite's own note on this distinction is
// what made its restart checks readable.
func destStat(name string) {
	for _, d := range readStatus().Destinations {
		if d.Name != name {
			continue
		}
		if d.Process == nil {
			fmt.Println("none -1 -1")
			return
		}
		fmt.Printf("%s %d %d\n", d.Process.State, d.Process.Restarts, d.Process.Progress.OutTimeMS)
		return
	}
	die(noSuchDest + name)
}

func stopAll() {
	_, out := do(http.MethodGet, "/destinations", nil)
	var rows []struct {
		Destination struct {
			ID int64 `json:"id"`
		} `json:"destination"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		die("destinations unreadable: " + err.Error())
	}
	for _, r := range rows {
		if r.Destination.ID == 0 {
			die("a destination came back with no id; the list shape has changed")
		}
		if code, body := do(http.MethodPost,
			fmt.Sprintf("/destinations/%d/stop", r.Destination.ID), nil); code != http.StatusOK {
			die(fmt.Sprintf("stop %d failed: %d %s", r.Destination.ID, code, body))
		}
	}
	fmt.Println("STOPPED")
}

// leakScan asks every read-reachable rendering of a running destination whether
// it carries a credential verbatim.
//
// THE ENDPOINT LIST IS #150'S, and that is why it is this list. That disclosure
// travelled out of four egresses at once and survived review because /processes
// had only ever been swept against a fixture that started no destination, while
// the others were excused as "needs a running child" -- see
// internal/api/argv_leak_test.go, which is the unit-level guard for the same
// class. This is the same sweep against a server that is really publishing to
// four endpoints, which is the state the unit guard has to simulate.
//
// GET /destinations IS NOT ON THE LIST, and leaving it off is a decision rather
// than an oversight. That is the admin CONFIGURATION route: it hands an
// admin-scoped session the destination row entire, streamKey included, because
// the editor has to populate the field the operator typed it into. The guard
// there is scope, not masking -- api.readScopeCannotSeePublishTokens blanks the
// credential for a read-scoped principal, and internal/api/read_scope_leak_test.go
// is the recurrence guard for it. Sweeping it here would report a designed
// behaviour as a leak on every run, which is how a suite teaches its readers to
// ignore it. What IS swept is every route that renders a destination for
// OBSERVATION, where the credential was never meant to appear at all.
//
// The values come from the environment, never from argv, for the reason
// addDest gives. A hit prints WHERE, because "a key leaked somewhere" is not
// something anyone can act on.
func leakScan(envs []string) {
	type target struct{ label, path string }
	targets := []target{
		{"GET /status", "/status"},
		{"GET /processes", "/processes"},
		{"GET /settings", settingsPath},
	}
	for _, d := range readStatus().Destinations {
		if d.Process == nil {
			continue
		}
		p := fmt.Sprintf("/processes/dest:%d/logs", d.ID)
		targets = append(targets, target{"GET " + p, p})
	}
	bodies := make(map[string]string, len(targets))
	for _, t := range targets {
		_, out := do(http.MethodGet, t.path, nil)
		bodies[t.label] = string(out)
	}
	// Sorted so a run that finds two leaks reports them in a stable order; an
	// unstable one reads as a different fault on every run.
	sort.Strings(envs)
	found := false
	for _, e := range envs {
		v := strings.TrimSpace(os.Getenv(e))
		// A short or empty value cannot be searched for honestly: engine's own
		// alerts.MinSecretLen refuses to mask anything under 8 characters, so a
		// "SAFE" here would mean "we did not look" rather than "it is not
		// there". Said out loud rather than passed over.
		if len(v) < 8 {
			fmt.Printf("UNCHECKED %s (value shorter than 8 chars; nothing would mask it either)\n", e)
			found = true
			continue
		}
		for _, t := range targets {
			if strings.Contains(bodies[t.label], v) {
				fmt.Printf("LEAK %s %s\n", e, t.label)
				found = true
			}
		}
	}
	if !found {
		fmt.Println("SAFE")
	}
}

func die(msg string) {
	fmt.Fprintln(os.Stderr, "driver: "+msg)
	os.Exit(1)
}
