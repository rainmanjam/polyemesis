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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"
	"time"
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

var (
	client *http.Client
	base   string
	csrf   string
)

func main() {
	if len(os.Args) < 2 {
		die("usage: acceptance_failover_driver.go <base-url> [subcommand]")
	}
	base = strings.TrimSuffix(os.Args[1], "/") + "/api/v1"
	jar, _ := cookiejar.New(nil)
	client = &http.Client{Jar: jar, Timeout: 30 * time.Second}
	waitUp()

	cmd := ""
	if len(os.Args) > 2 {
		cmd = os.Args[2]
	}
	switch cmd {
	case "":
		setup()
		enableFailover()
		dest()
	case "status":
		login()
		status()
	case "stopall":
		login()
		stopAll()
	case "pin":
		if len(os.Args) < 4 {
			die("usage: pin <primary|backup|slate|auto>")
		}
		login()
		pin(os.Args[3])
	case "playlist":
		if len(os.Args) < 5 {
			die("usage: playlist <on|off> <stored-upload-name>...")
		}
		login()
		playlist(os.Args[3] == "on", os.Args[4:])
	case "plready":
		login()
		playlistReady()
	case "adddest":
		if len(os.Args) < 5 {
			die("usage: adddest <name> <file-url>")
		}
		login()
		addDest(os.Args[3], os.Args[4])
	case "restarts":
		if len(os.Args) < 4 {
			die("usage: restarts <destination-name>")
		}
		login()
		restarts(os.Args[3])
	case "outtime":
		if len(os.Args) < 4 {
			die("usage: outtime <destination-name>")
		}
		login()
		outtime(os.Args[3])
	case "publishkey":
		login()
		publishKey()
	default:
		die("unknown subcommand " + cmd)
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

// enableFailover turns on the selector tier with a COLOUR slate.
//
// Colour rather than an image on purpose: an image would prove the file loader
// and nothing about the switch, and the suite's real subject is what happens to
// the timeline when the feed underneath a destination is replaced.
func enableFailover() {
	_, out := do(http.MethodGet, "/settings", nil)
	var s map[string]any
	if err := json.Unmarshal(out, &s); err != nil {
		die("settings unreadable: " + err.Error())
	}
	// Move the ingest off the default port so a stray listener from another
	// suite cannot be mistaken for this one's encoder.
	ing, _ := s["ingest"].(map[string]any)
	if ing == nil {
		die("settings carried no ingest block")
	}
	// RTMP rather than SRT. This suite is about what happens to the TIMELINE
	// when the feed under a destination is replaced, and the ingest transport
	// is incidental to that -- but a host FFmpeg built without libsrt cannot
	// listen or publish on SRT at all, and Homebrew's is. SRT ingest is covered
	// by the container suites, which ship an FFmpeg that has it.
	ing["mode"] = "rtmp"
	rtmp, _ := ing["rtmp"].(map[string]any)
	if rtmp == nil {
		rtmp = map[string]any{}
		ing["rtmp"] = rtmp
	}
	rtmp["app"] = "live"
	rtmp["streamKey"] = ""
	// The port is install-wide now, not a property of the source.
	s["listeners"] = map[string]any{"srtPort": 6000, "rtmpPort": ingestPort}

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
	code, body := do(http.MethodPut, "/settings", s)
	if code != http.StatusOK {
		die(fmt.Sprintf("enable failover failed: %d %s", code, body))
	}
	fmt.Println("FAILOVER_OK")
}

func sel(on ...int) []map[string]any {
	want := map[int]bool{}
	for _, t := range on {
		want[t] = true
	}
	rows := make([]map[string]any, 0, 6)
	for i := 0; i < 6; i++ {
		rows = append(rows, map[string]any{"track": i, "enabled": want[i], "gain": 1.0})
	}
	return rows
}

func dest() { addDest("onair", "onair.ts") }

// addDest creates one file destination.
//
// Named and parameterised rather than hard-coded, because the mismatch ratchet
// needs a destination of its OWN. That case expects restarts, and a restart
// truncates the file the destination is writing -- pointed at onair.mkv it
// would erase the very recording the timeline checks measured.
func addDest(name, url string) {
	code, out := do(http.MethodPost, "/destinations", map[string]any{
		"name": name, "kind": "file", "url": url,
		"enabled": true, "audioBitrate": 160,
		"profile": map[string]any{
			"mode": "simple", "tracks": sel(0), "matrix": []any{},
			// Normalisation off: a limiter between the source and the file
			// would smooth exactly the level difference the suite uses to tell
			// the primary apart from the slate.
			"normalize": "off", "sampleRate": 48000,
		},
	})
	if code != http.StatusOK && code != http.StatusCreated {
		die(fmt.Sprintf("create destination failed: %d %s", code, out))
	}
	fmt.Println("DEST_OK")
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
		Name    string `json:"name"`
		Process *struct {
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

func readStatus() statusDoc {
	_, out := do(http.MethodGet, "/status", nil)
	var st statusDoc
	if err := json.Unmarshal(out, &st); err != nil {
		die("status unreadable: " + err.Error())
	}
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

// publishKey prints the stream key an encoder must use to reach the source.
//
// This exists because the RTMP ingest stopped being "whatever turns up on the
// port". There is now one shared RTMP listener for the whole install, and it
// addresses sources BY KEY -- so a publisher with no key, or the wrong one, is
// refused at the handshake and the suite sees an encoder that connected and
// then died with a broken pipe.
//
// The token is what the UI puts in the publish URL, so this is also the address
// a real operator would be given. Reading it from the API rather than pinning a
// constant here keeps the suite honest about rotation: if the token changes
// shape, this follows it.
func publishKey() {
	code, out := do(http.MethodGet, "/sources", nil)
	if code != http.StatusOK {
		die(fmt.Sprintf("cannot read sources: %d %s", code, out))
	}
	var rows []struct {
		Source struct {
			ID    int64  `json:"id"`
			Token string `json:"token"`
		} `json:"source"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		die("sources unreadable: " + err.Error())
	}
	for _, r := range rows {
		// The list endpoint wraps each row, but has carried the token at both
		// levels over its life. Take whichever is populated rather than
		// silently publishing to an empty key, which is the exact failure this
		// function exists to prevent.
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

func pin(kind string) {
	// "auto" is accepted by name and clears the pin; no translation needed.
	code, out := do(http.MethodPost, "/failover/source", map[string]any{"source": kind})
	if code != http.StatusOK {
		die(fmt.Sprintf("pin %s failed: %d %s", kind, code, out))
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
// does. PUT /settings REPLACES the settings, so posting a lone failover block
// would reset the ingest to its defaults, move the listener off port 1938 and
// strand the publisher -- which would look from the outside like the failover
// this suite is measuring.
func playlist(enabled bool, uploads []string) {
	_, out := do(http.MethodGet, "/settings", nil)
	var s map[string]any
	if err := json.Unmarshal(out, &s); err != nil {
		die("settings unreadable: " + err.Error())
	}
	f, _ := s["failover"].(map[string]any)
	if f == nil {
		die("settings carried no failover block")
	}
	// Bare stored names, never paths: db.PlaylistSettings.PlaylistFileProblem
	// refuses anything carrying a separator, and the engine resolves what is
	// left through uploads.Store.Resolve.
	items := make([]any, 0, len(uploads))
	for _, u := range uploads {
		items = append(items, map[string]any{"upload": u})
	}
	f["playlist"] = map[string]any{"enabled": enabled, "items": items}
	code, body := do(http.MethodPut, "/settings", s)
	if code != http.StatusOK {
		die(fmt.Sprintf("save playlist failed: %d %s", code, body))
	}
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
	_, out := do(http.MethodGet, "/failover/playlist", nil)
	var st struct {
		Ready bool `json:"ready"`
		Items []struct {
			Upload string `json:"upload"`
			State  string `json:"state"`
			Detail string `json:"detail"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &st); err != nil {
		die("playlist status unreadable: " + err.Error())
	}
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

// stopAll stops every destination so its file finalises.
//
// The list body is [{"destination": {...}, "routing": {...}}], NOT a bare array
// of destinations. Decoding it as the latter was silently reading id 0 off every
// row, POSTing /destinations/0/stop, taking the 404 without looking and printing
// STOPPED -- so no destination was ever stopped and the recording the timeline
// checks read was always an unfinalised Matroska. The checks were written around
// that damage rather than against it: the duration one reads the last decode
// timestamp because "format=duration" came back N/A, which is what an
// unfinalised file reports.
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

func die(msg string) {
	fmt.Fprintln(os.Stderr, "driver: "+msg)
	os.Exit(1)
}
