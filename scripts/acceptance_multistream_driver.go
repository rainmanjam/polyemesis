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
// The HTTP session, login, setup, the settings read-modify-write, the profile
// track rows, destination creation and stopall live in
// scripts/internal/driverlib, shared with the failover driver. What stays here
// is what this suite alone does: reading the compiled routing back off /status,
// and the credential sweep.
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
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/rainmanjam/polyemesis/scripts/internal/driverlib"
)

const (
	// Named because Sonar counts a literal repeated three times as a
	// maintenance hazard, and it is right here: a typo in one of three
	// copies of a message is a driver that reports a different fault on one
	// code path only.
	noSuchDest = "no destination named "
	user       = "admin"
	pass       = "MultistreamAcceptance!7q"
)

func main() {
	if len(os.Args) < 3 {
		driverlib.Die("usage: acceptance_multistream_driver.go <base-url> <subcommand> [args]")
	}
	driverlib.Init(os.Args[1])
	driverlib.WaitUp()

	args := os.Args[3:]
	switch os.Args[2] {
	case "setup":
		if len(args) < 1 {
			driverlib.Die("usage: setup <rtmp-port>")
		}
		driverlib.Setup(user, pass)
		ingest(args[0])
	case "publishkey":
		driverlib.Login(user, pass)
		driverlib.PublishKey()
	case "srctracks":
		driverlib.Login(user, pass)
		srcTracks()
	case "adddest":
		if len(args) < 5 {
			driverlib.Die("usage: adddest <name> <platform> <url> <key-env> <tracks-csv>")
		}
		driverlib.Login(user, pass)
		addDest(args[0], args[1], args[2], args[3], args[4])
	case "tracks":
		if len(args) < 1 {
			driverlib.Die("usage: tracks <name>")
		}
		driverlib.Login(user, pass)
		printTracks(args[0])
	case "graph":
		if len(args) < 1 {
			driverlib.Die("usage: graph <name>")
		}
		driverlib.Login(user, pass)
		printGraph(args[0])
	case "deststat":
		if len(args) < 1 {
			driverlib.Die("usage: deststat <name>")
		}
		driverlib.Login(user, pass)
		destStat(args[0])
	case "stopall":
		driverlib.Login(user, pass)
		driverlib.StopAll()
	case "leakscan":
		if len(args) < 1 {
			driverlib.Die("usage: leakscan <key-env>...")
		}
		driverlib.Login(user, pass)
		leakScan(args)
	default:
		driverlib.Die("unknown subcommand " + os.Args[2])
	}
}

// ingest puts the install's shared RTMP listener on a port of this suite's own.
//
// The transport choice and the empty stream key are driverlib.UseRTMPIngest's
// to explain; what belongs here is only that the port is this suite's, so a
// stray listener from another suite cannot be mistaken for this one's encoder.
func ingest(rtmpPort string) {
	port, err := strconv.Atoi(rtmpPort)
	if err != nil {
		driverlib.Die("rtmp port is not a number: " + rtmpPort)
	}
	s := driverlib.LoadSettings()
	driverlib.UseRTMPIngest(s, port)
	driverlib.SaveSettings(s, "ingest settings")
	fmt.Println("INGEST_OK")
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

func parseTracks(csv string) []int {
	var out []int
	for _, f := range strings.Split(csv, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		n, err := strconv.Atoi(f)
		if err != nil {
			driverlib.Die("not a track index: " + f)
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		driverlib.Die("a destination with no tracks selected would prove nothing")
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
// os.Getenv is the only path in this file that touches the value, and nothing
// downstream of it -- including driverlib.CreateDest's failure report, which
// prints only the server's own already-scrubbed body -- echoes it back.
//
// NORMALISATION OFF, deliberately. A limiter between the ingest and the
// platform would compress exactly the level difference every assertion in this
// suite reads, so a routing fault that sent the wrong tracks could be masked by
// the very stage meant to make the mix consistent.
func addDest(name, platform, url, keyEnv, tracksCSV string) {
	key := os.Getenv(keyEnv)
	if strings.TrimSpace(key) == "" {
		driverlib.Die(keyEnv + " is empty; a destination with no credential is not a measurement")
	}
	driverlib.CreateDest(name, map[string]any{
		"name": name, "kind": "rtmp", "platform": platform,
		"url": url, "streamKey": key,
		"enabled": true, "audioBitrate": 160,
		"profile": map[string]any{
			"mode": "simple", "tracks": driverlib.Sel(parseTracks(tracksCSV)...), "matrix": []any{},
			"normalize": "off", "sampleRate": 48000,
		},
	})
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

// readStatus is local rather than shared because the DOCUMENT is local: this
// suite decodes the source's tracks and each destination's compiled routing,
// and the failover driver decodes the failover block and a restart count. The
// fetch-and-decode underneath both is driverlib.GetJSON.
func readStatus() statusDoc {
	var st statusDoc
	driverlib.GetJSON("/status", "status", &st)
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
	driverlib.Die(noSuchDest + name)
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
	driverlib.Die(noSuchDest + name)
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
	driverlib.Die(noSuchDest + name)
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
		{"GET /settings", driverlib.SettingsPath},
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
		_, out := driverlib.Do(http.MethodGet, t.path, nil)
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
