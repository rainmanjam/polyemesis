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
//	(no subcommand)  set up, enable failover with a slate, add one destination
//	status           print "<active> <switches> <primaryLive> <destRestarts>"
//	stopall          stop every destination so its file finalises
//	pin <kind>       put a source on air by hand (primary|backup|slate|auto)
//	playlist <path>  turn the playlist tier on, pointed at a file in the data dir
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
		if len(os.Args) < 4 {
			die("usage: playlist <path-relative-to-the-data-directory>")
		}
		login()
		playlist(os.Args[3])
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

func dest() {
	code, out := do(http.MethodPost, "/destinations", map[string]any{
		"name": "onair", "kind": "file", "url": "onair.mkv",
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
	_, out := do(http.MethodGet, "/status", nil)
	var st struct {
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
			} `json:"process"`
		} `json:"destinations"`
	}
	if err := json.Unmarshal(out, &st); err != nil {
		die("status unreadable: " + err.Error())
	}
	active, switches, live := "none", -1, false
	if st.Failover != nil {
		active, switches, live = st.Failover.Active, st.Failover.Switches, st.Failover.PrimaryLive
	}
	// -1 means "no destination process at all", which is a different failure
	// from "restarted 0 times" and must not be reported as the same number.
	restarts := -1
	for _, d := range st.Destinations {
		if d.Process != nil {
			restarts = d.Process.Restarts
			break
		}
	}
	fmt.Printf("%s %d %t %d\n", active, switches, live, restarts)
}

func pin(kind string) {
	// "auto" is accepted by name and clears the pin; no translation needed.
	code, out := do(http.MethodPost, "/failover/source", map[string]any{"source": kind})
	if code != http.StatusOK {
		die(fmt.Sprintf("pin %s failed: %d %s", kind, code, out))
	}
	fmt.Println("PIN_OK")
}

// playlist turns the playlist tier on, pointed at a file inside the data
// directory.
//
// Turned on MID-RUN by its own subcommand rather than folded into
// enableFailover, because the playlist outranks the slate: a tier already
// running when the encoder is cut would take the slate's place, and the suite
// would stop measuring the slate cycle it was written for. The file on air is
// only interesting once that cycle has been proved to work without one.
//
// Read-modify-write of the whole settings document, exactly as enableFailover
// does. PUT /settings REPLACES the settings, so posting a lone failover block
// would reset the ingest to its defaults, move the listener off port 1938 and
// strand the publisher -- which would look from the outside like the failover
// this suite is measuring.
func playlist(path string) {
	_, out := do(http.MethodGet, "/settings", nil)
	var s map[string]any
	if err := json.Unmarshal(out, &s); err != nil {
		die("settings unreadable: " + err.Error())
	}
	f, _ := s["failover"].(map[string]any)
	if f == nil {
		die("settings carried no failover block")
	}
	// Relative, never absolute: db.PlaylistSettings.PlaylistFileProblem confines
	// the path to the data directory, and an absolute one is rejected by
	// validation rather than played.
	f["playlist"] = map[string]any{"enabled": true, "filePath": path}
	code, body := do(http.MethodPut, "/settings", s)
	if code != http.StatusOK {
		die(fmt.Sprintf("enable playlist failed: %d %s", code, body))
	}
	fmt.Println("PLAYLIST_OK")
}

func stopAll() {
	_, out := do(http.MethodGet, "/destinations", nil)
	var rows []struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		die("destinations unreadable: " + err.Error())
	}
	for _, r := range rows {
		do(http.MethodPost, fmt.Sprintf("/destinations/%d/stop", r.ID), nil)
	}
	fmt.Println("STOPPED")
}

func die(msg string) {
	fmt.Fprintln(os.Stderr, "driver: "+msg)
	os.Exit(1)
}
