//go:build ignore

// Driver for scripts/acceptance-failover.sh.
//
// The source-selector tier's whole promise is that a destination NEVER learns
// the source changed: the hub it reads stays the same and only the bytes on it
// change. Everything about that promise is invisible to a unit test, because
// what breaks it is real timestamps arriving from real encoders.
//
// The engine's own design notes name the two things that decide whether it
// works at all -- PTS continuity across a switch, and a destination riding the
// switch without restarting -- and both were covered only against fakes.
//
// The HTTP session, login, setup, the settings read-modify-write, the profile
// track rows, destination creation and stopall live in
// scripts/internal/driverlib, shared with the multistream driver. What stays
// here is what this suite alone does: the failover tier's settings, the
// playlist subcommands, and the four numbers status prints.
//
//	(no subcommand)      set up, enable failover with a slate, add one destination
//	status               print "<active> <switches> <primaryLive> <destRestarts>"
//	stopall              stop every destination so its file finalises
//	pin <kind>           put a source on air by hand (primary|backup|slate|auto)
//	playlist <on|off> <upload>...
//	                     store the playlist's items, with the tier on or off
//	plready              print READY once every item has a derivative, else why not
//	adddest <name> <file>  add a second file destination, so a case that expects a
//	                     restart cannot damage the first one's recording
//	restarts <name>      print one named destination's restart count, or -1
//	outtime <name>       print its produced media in ms, or -1 when it has no process
package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/rainmanjam/polyemesis/scripts/internal/driverlib"
)

const (
	user = "admin"
	pass = "FailoverAcceptance!9x"
	// graceSeconds is deliberately short. The suite has to wait it out twice,
	// and a realistic 10s would only make the run slower without exercising
	// anything a 2s grace does not.
	graceSeconds = 2
	// returnStable is how long the primary must deliver before an automatic
	// return trusts it. Also short, for the same reason.
	returnStable = 3
	// ingestPort must match INGEST in acceptance-failover.sh, which is what the
	// publisher dials.
	ingestPort = 1938
)

func main() {
	if len(os.Args) < 2 {
		driverlib.Die("usage: acceptance_failover_driver.go <base-url> [subcommand]")
	}
	driverlib.Init(os.Args[1])
	driverlib.WaitUp()

	cmd := ""
	if len(os.Args) > 2 {
		cmd = os.Args[2]
	}
	switch cmd {
	case "":
		driverlib.Setup(user, pass)
		// The programme every step below acts on. Since #387 a fresh install
		// has none; see driverlib.EnsureSource.
		driverlib.EnsureSource("Main")
		enableFailover()
		dest()
	case "status":
		driverlib.Login(user, pass)
		status()
	case "stopall":
		driverlib.Login(user, pass)
		driverlib.StopAll()
	case "pin":
		if len(os.Args) < 4 {
			driverlib.Die("usage: pin <primary|backup|slate|auto>")
		}
		driverlib.Login(user, pass)
		pin(os.Args[3])
	case "playlist":
		if len(os.Args) < 5 {
			driverlib.Die("usage: playlist <on|off> <stored-upload-name>...")
		}
		driverlib.Login(user, pass)
		playlist(os.Args[3] == "on", os.Args[4:])
	case "plready":
		driverlib.Login(user, pass)
		playlistReady()
	case "adddest":
		if len(os.Args) < 5 {
			driverlib.Die("usage: adddest <name> <file-url>")
		}
		driverlib.Login(user, pass)
		addDest(os.Args[3], os.Args[4])
	case "restarts":
		if len(os.Args) < 4 {
			driverlib.Die("usage: restarts <destination-name>")
		}
		driverlib.Login(user, pass)
		restarts(os.Args[3])
	case "outtime":
		if len(os.Args) < 4 {
			driverlib.Die("usage: outtime <destination-name>")
		}
		driverlib.Login(user, pass)
		outtime(os.Args[3])
	case "publishkey":
		driverlib.Login(user, pass)
		driverlib.PublishKey()
	default:
		driverlib.Die("unknown subcommand " + cmd)
	}
}

// enableFailover turns on the selector tier with a COLOUR slate.
//
// Colour rather than an image on purpose: an image would prove the file loader
// and nothing about the switch, and the suite's real subject is what happens to
// the timeline when the feed underneath a destination is replaced.
func enableFailover() {
	s := driverlib.LoadSettings()
	// The ingest moves off the default port so a stray listener from another
	// suite cannot be mistaken for this one's encoder. driverlib.UseRTMPIngest
	// records why the transport is RTMP rather than SRT.
	driverlib.UseRTMPIngest(s, ingestPort)

	s["failover"] = map[string]any{
		"enabled":             true,
		"graceSeconds":        graceSeconds,
		"return":              "auto",
		"returnStableSeconds": returnStable,
		"backup":              map[string]any{"enabled": false},
		"slate": map[string]any{
			"enabled": true,
			// No imagePath: a flat colour has no file to fail to open, which is
			// the right thing for a tier that starts when everything else has
			// already failed. Blue rather than black so a black frame from a
			// dying encoder cannot be mistaken for the slate.
			"color":     "blue",
			"videoKbps": 800,
		},
	}
	driverlib.SaveSettings(s, "enable failover")
	fmt.Println("FAILOVER_OK")
}

func dest() { addDest("onair", "onair.ts") }

// addDest creates one file destination.
//
// Named and parameterised rather than hard-coded, because the mismatch ratchet
// needs a destination of its OWN. That case expects restarts, and a restart
// truncates the file the destination is writing -- pointed at onair.mkv it
// would erase the very recording the timeline checks measured.
func addDest(name, url string) {
	driverlib.CreateDest(name, map[string]any{
		"name": name, "kind": "file", "url": url,
		"enabled": true, "audioBitrate": 160,
		"profile": map[string]any{
			"mode": "simple", "tracks": driverlib.Sel(0), "matrix": []any{},
			// Normalisation off: a limiter between the source and the file
			// would smooth exactly the level difference the suite uses to tell
			// the primary apart from the slate.
			"normalize": "off", "sampleRate": 48000,
		},
	})
}

// status prints the four numbers the suite makes its decisions on.
//
// destRestarts is the one that matters most. The tier exists so a destination
// never restarts on a switch, so counting restarts is the only direct evidence
// that it worked -- "the file has bytes in it" would pass just as happily on a
// destination that died and came back.
func status() {
	st := readStatus()
	active, switches, live := "none", -1, false
	if st.Failover != nil {
		active, switches, live = st.Failover.Active, st.Failover.Switches, st.Failover.PrimaryLive
	}
	// -1 means "no destination process at all", which is a different failure
	// from "restarted 0 times" and must not be reported as the same number.
	//
	// The FIRST destination carrying a process, which for every check that reads
	// this field is "onair" -- it is created before any other and the store lists
	// in creation order. A case that adds a second destination reads it by name
	// through `restarts` below rather than trusting that ordering.
	n := -1
	for _, d := range st.Destinations {
		if d.Process != nil {
			n = d.Process.Restarts
			break
		}
	}
	fmt.Printf("%s %d %t %d\n", active, switches, live, n)
}

type statusDoc struct {
	Failover *struct {
		Active      string `json:"active"`
		Switches    int    `json:"switches"`
		PrimaryLive bool   `json:"primaryLive"`
	} `json:"failover"`
	Destinations []struct {
		Name string `json:"name"`
		// Set while a destination has left the running set but its child has not
		// been confirmed dead. Decoded so a run can tell a teardown in flight from
		// a destination that died -- see #462.
		Transitioning bool `json:"transitioning"`
		Process       *struct {
			Restarts int    `json:"restarts"`
			State    string `json:"state"`
			// Progress is what the child has actually PRODUCED, and it was on
			// the wire all along -- engine.DestStatus.Process is a whole
			// supervisor.Status, which carries ffmpeg.Progress. This struct
			// simply did not decode it.
			//
			// It matters because the file on disk is not an observable of
			// delivery. An MKV muxer buffers, so a destination that is running
			// perfectly shows 0 bytes for the whole run and then one 256 KiB
			// flush at close -- measured, on a healthy local run. out_time
			// counts media produced and moves with delivery rather than with
			// the muxer's flush schedule. See issue #275.
			Progress struct {
				OutTimeMS int64 `json:"outTimeMs"`
			} `json:"progress"`
		} `json:"process"`
	} `json:"destinations"`
}

// readStatus is local rather than shared because the DOCUMENT is local: this
// suite decodes the failover block and a destination's restart count, and the
// multistream driver decodes the source's tracks and each destination's
// compiled routing. The fetch-and-decode underneath both is driverlib.GetJSON.
func readStatus() statusDoc {
	var st statusDoc
	driverlib.GetJSON("/status", "status", &st)
	return st
}

// restarts prints ONE named destination's restart count.
//
// By name, not by position. The mismatch ratchet runs alongside the destination
// the earlier steps used, and reading "the first one with a process" there would
// answer about whichever the store happened to list first -- a number that looks
// exactly like the one being asked for and means something else. -1 keeps its
// meaning from status: no process at all, which is not "restarted 0 times".
func restarts(name string) {
	for _, d := range readStatus().Destinations {
		if d.Name == name && d.Process != nil {
			fmt.Println(d.Process.Restarts)
			return
		}
	}
	fmt.Println(-1)
}

// outtime prints how many milliseconds of media a destination has produced, or
// -1 when there is no such process.
//
// -1 rather than 0 for "no process", and the distinction is the whole reason
// this exists: 0 means "it is running and has produced nothing", which is a
// finding, and -1 means "there is nothing to ask", which is a different one.
// Collapsing them is how the byte count this replaces became unreadable.
func outtime(name string) {
	for _, d := range readStatus().Destinations {
		if d.Name == name && d.Process != nil {
			fmt.Println(d.Process.Progress.OutTimeMS)
			return
		}
	}
	fmt.Println(-1)
}

func pin(kind string) {
	// "auto" is accepted by name and clears the pin; no translation needed.
	code, out := driverlib.Do(http.MethodPost, "/failover/source", map[string]any{"source": kind})
	if code != http.StatusOK {
		driverlib.Die(fmt.Sprintf("pin %s failed: %d %s", kind, code, out))
	}
	fmt.Println("PIN_OK")
}

// playlist stores the playlist's items, with the tier on or off.
//
// UPLOAD NAMES, not paths. Items stopped being paths because
// uploads.Store.Resolve is the single boundary that turns an operator-supplied
// name into a file inside the uploads directory, and a path field made that
// boundary optional. Posting the old "filePath" now fails the settings
// decoder's unknown-field check outright, which is the intended answer.
//
// SEVERAL items, not one. A single-item playlist cannot tell sequencing apart
// from B1's play-item-0-forever: both look like one file on air. The suite
// names three of DIFFERENT LENGTHS for the same reason -- with three equal
// clips a boundary in the wrong place is indistinguishable from one in the
// right place.
//
// ON AND OFF ARE SEPARATE CALLS, and the off call is why the suite covers the
// production enqueue path at all. Saving the ITEMS is what makes
// api.Server.enqueuePlaylistNormalisation submit one normalisation per upload;
// it does that whether or not the tier is enabled. So the suite can stage the
// items -- and let the real job write the real derivatives -- while the tier
// itself stays off until the run is ready for it. Enabling it early would take
// the slate's place in the failover cycle the suite was originally written to
// measure, because the playlist outranks the slate.
//
// Read-modify-write of the whole settings document, exactly as enableFailover
// does, and driverlib.LoadSettings records why that is not optional.
func playlist(enabled bool, uploads []string) {
	s := driverlib.LoadSettings()
	f, _ := s["failover"].(map[string]any)
	if f == nil {
		driverlib.Die("settings carried no failover block")
	}
	// Bare stored names, never paths: db.PlaylistSettings.PlaylistFileProblem
	// refuses anything carrying a separator, and the engine resolves what is
	// left through uploads.Store.Resolve.
	items := make([]any, 0, len(uploads))
	for _, u := range uploads {
		items = append(items, map[string]any{"upload": u})
	}
	f["playlist"] = map[string]any{"enabled": enabled, "items": items}
	driverlib.SaveSettings(s, "save playlist")
	fmt.Println("PLAYLIST_OK")
}

// playlistReady prints READY once every item has a derivative, and otherwise
// prints what each item is waiting on.
//
// GET /failover/playlist, the endpoint Task 6 added, rather than stat-ing the
// derivative directory from the shell. Two reasons, and the second is the one
// that matters: the endpoint is the only thing that knows a job is DEFERRED
// rather than merely missing, so a suite that stalls can say whether the
// governor is holding the work back or the transcode failed; and a path built
// in the shell is a second copy of playlistmedia.DerivativePath, which already
// carries a profile version this suite got wrong once -- it hand-copied a
// derivative to a name the code had stopped looking for, and every check
// downstream went on passing.
func playlistReady() {
	var st struct {
		Ready bool `json:"ready"`
		Items []struct {
			Upload string `json:"upload"`
			State  string `json:"state"`
			Detail string `json:"detail"`
		} `json:"items"`
	}
	driverlib.GetJSON("/failover/playlist", "playlist status", &st)
	if st.Ready {
		fmt.Println("READY")
		return
	}
	parts := make([]string, 0, len(st.Items))
	for _, it := range st.Items {
		p := it.Upload + "=" + it.State
		if it.Detail != "" {
			p += "(" + it.Detail + ")"
		}
		parts = append(parts, p)
	}
	fmt.Println("NOTREADY " + strings.Join(parts, " "))
}
