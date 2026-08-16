//go:build ignore

// Driver for acceptance-playlist-phase0.sh.
//
// Everything here goes through the same REST API the UI uses, because the
// point of the suite is that scheduled pre-recorded broadcast is reachable
// TODAY with no new code -- and a driver that reached past the API would prove
// something weaker than that.
//
//	go run scripts/acceptance_playlist_driver.go http://127.0.0.1:PORT <cmd> [args]
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	user = "admin"
	pass = "acceptance-pw-1"
)

var (
	base string
	jar  []*http.Cookie
	csrf string
)

// die writes to STDERR, never stdout.
//
// Learned from acceptance-postprod: stdout here is a value channel -- the shell
// reads an id or a boolean off it. A fatal message printed to stdout becomes
// the value, and the suite then reports a PASS whose text is the error.
func die(msg string) {
	fmt.Fprintln(os.Stderr, "FATAL: "+msg)
	os.Exit(1)
}

func main() {
	if len(os.Args) < 3 {
		die("usage: driver <baseURL> <cmd> [args]")
	}
	base = strings.TrimSuffix(os.Args[1], "/") + "/api/v1"
	cmd := os.Args[2]
	args := os.Args[3:]

	switch cmd {
	case "setup":
		setup()
		return
	case "waitup":
		waitUp()
		return
	}

	waitUp()
	login()

	switch cmd {
	case "pullmode":
		pullMode(args[0])
	case "dest":
		fmt.Println(createDest())
	case "schedule":
		schedule(args[0], args[1])
	case "enabled":
		fmt.Println(destEnabled(args[0]))
	case "ingestlive":
		fmt.Println(ingestLive())
	case "tslost":
		tsLost()
	case "stopall":
		stopAll()
	default:
		die("unknown command " + cmd)
	}
}

func waitUp() {
	for i := 0; i < 80; i++ {
		resp, err := http.Get(base + "/health")
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	die("server never became reachable")
}

func do(method, path string, body any) (int, []byte) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			die("marshal: " + err.Error())
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, base+path, rdr)
	if err != nil {
		die("request: " + err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	for _, c := range jar {
		req.AddCookie(c)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		die(method + " " + path + ": " + err.Error())
	}
	defer resp.Body.Close()
	if cs := resp.Cookies(); len(cs) > 0 {
		jar = append(jar, cs...)
		for _, c := range cs {
			if c.Name == "polyemesis_csrf" {
				csrf = c.Value
			}
		}
	}
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func setup() {
	waitUp()
	code, out := do(http.MethodPost, "/setup", map[string]string{"username": user, "password": pass})
	if code != http.StatusOK && code != http.StatusCreated {
		die(fmt.Sprintf("setup failed: %d %s", code, out))
	}
	// The programme every step below acts on. A fresh install has none since
	// #387; see acceptance_driver.go for the full reason.
	if code, out := do(http.MethodPost, "/sources",
		map[string]any{"name": "Main", "enabled": true}); code != http.StatusOK &&
		code != http.StatusCreated {
		die(fmt.Sprintf("create the first source: %d %s", code, out))
	}
	fmt.Println("SETUP_OK")
}

func login() {
	if code, out := do(http.MethodPost, "/auth/login",
		map[string]string{"username": user, "password": pass}); code != http.StatusOK {
		die(fmt.Sprintf("login failed: %d %s", code, out))
	}
}

// pullMode points the ingest at a file inside the data directory. This is the
// whole of "broadcast from a file with no encoder attached" -- there is no
// playlist type, no new ingest mode, and no code that did not already ship.
func pullMode(rel string) {
	_, out := do(http.MethodGet, "/settings", nil)
	var s map[string]any
	if err := json.Unmarshal(out, &s); err != nil {
		die("settings unreadable: " + err.Error())
	}
	ing, _ := s["ingest"].(map[string]any)
	if ing == nil {
		die("settings carried no ingest block")
	}
	ing["mode"] = "pull"
	pull, _ := ing["pull"].(map[string]any)
	if pull == nil {
		pull = map[string]any{}
		ing["pull"] = pull
	}
	pull["url"] = "file://" + rel

	code, body := do(http.MethodPut, "/settings", s)
	if code != http.StatusOK {
		die(fmt.Sprintf("switch to pull failed: %d %s", code, body))
	}
	fmt.Println("PULL_MODE_OK")
}

// createDest makes ONE file destination and leaves it DISABLED, because the
// whole point is that the schedule is what turns it on.
func createDest() int64 {
	tracks := make([]map[string]any, 0, 6)
	for i := 0; i < 6; i++ {
		tracks = append(tracks, map[string]any{"track": i, "enabled": i == 0, "gain": 1.0})
	}
	code, out := do(http.MethodPost, "/destinations", map[string]any{
		"name": "scheduled", "kind": "file", "url": "scheduled.mkv",
		"enabled": false, "audioBitrate": 160,
		"profile": map[string]any{
			"mode": "simple", "tracks": tracks, "matrix": []any{},
			// Off, so the suite measures the tone's own level rather than a
			// limiter's opinion of it.
			"normalize": "off", "sampleRate": 48000,
		},
	})
	if code != http.StatusOK && code != http.StatusCreated {
		die(fmt.Sprintf("create destination failed: %d %s", code, out))
	}
	// The create response wraps the row: {"destination": {...}}. Unwrapped
	// first, because a bare {...} would decode into the wrapper as a zero id
	// and report "no id" for a request that actually worked.
	var wrapped struct {
		Destination struct {
			ID int64 `json:"id"`
		} `json:"destination"`
	}
	if err := json.Unmarshal(out, &wrapped); err == nil && wrapped.Destination.ID != 0 {
		return wrapped.Destination.ID
	}
	var d struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(out, &d); err != nil || d.ID == 0 {
		die("destination response carried no id: " + string(out))
	}
	return d.ID
}

// schedule arms a one-shot `start` that many seconds from now.
//
// RunAt is UTC because that is what the scheduler stores and compares; the
// wall-clock fields with an IANA zone are for the recurring kinds.
func schedule(destID, inSeconds string) {
	id, err := strconv.ParseInt(destID, 10, 64)
	if err != nil {
		die("bad destination id " + destID)
	}
	secs, err := strconv.Atoi(inSeconds)
	if err != nil {
		die("bad delay " + inSeconds)
	}
	code, out := do(http.MethodPost, "/schedules", map[string]any{
		"name": "phase0", "enabled": true, "action": "start", "kind": "once",
		"destinationIds": []int64{id},
		"runAt":          time.Now().UTC().Add(time.Duration(secs) * time.Second).Format(time.RFC3339),
		// The floor. A generous grace would let a late fire still count and
		// blur exactly the before/after boundary this suite measures.
		"graceSeconds": 30,
	})
	if code != http.StatusOK && code != http.StatusCreated {
		die(fmt.Sprintf("create schedule failed: %d %s", code, out))
	}
	fmt.Println("SCHEDULE_OK")
}

// listDests decodes GET /destinations, whose rows are wrapped so each can
// carry its compiled routing alongside the row itself:
//
//	[{"destination": {...}, "routing": {...}}, ...]
func listDests() []struct {
	ID      int64
	Enabled bool
} {
	_, out := do(http.MethodGet, "/destinations", nil)
	var rows []struct {
		Destination struct {
			ID      int64 `json:"id"`
			Enabled bool  `json:"enabled"`
		} `json:"destination"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		die("destinations unreadable: " + err.Error())
	}
	ds := make([]struct {
		ID      int64
		Enabled bool
	}, 0, len(rows))
	for _, r := range rows {
		ds = append(ds, struct {
			ID      int64
			Enabled bool
		}{r.Destination.ID, r.Destination.Enabled})
	}
	return ds
}

func destEnabled(destID string) bool {
	for _, d := range listDests() {
		if strconv.FormatInt(d.ID, 10) == destID {
			return d.Enabled
		}
	}
	die("no destination " + destID)
	return false
}

// relayStats is the honest liveness measure: bytes actually arriving, rather
// than a process existing. A pull ingest that dialled a missing file has a
// process too, for a moment.
type relayStats struct {
	RxBytes   uint64 `json:"rxBytes"`
	TSPackets uint64 `json:"tsPackets"`
	TSLost    uint64 `json:"tsLost"`
}

func stats() relayStats {
	_, out := do(http.MethodGet, "/status", nil)
	var st struct {
		Relay relayStats `json:"relay"`
	}
	if err := json.Unmarshal(out, &st); err != nil {
		die("status unreadable: " + err.Error())
	}
	return st.Relay
}

func ingestLive() bool { return stats().RxBytes > 0 }

// tsLost is the count of MPEG-TS continuity-counter breaks the relay has seen.
//
// This is what makes the loop seam measurable for free. -stream_loop rewinds
// the file, and whether that rewind is visible downstream as a discontinuity is
// the difference between "looping works" and "looping works and nothing
// notices". The relay has counted this all along; nothing new is needed.
func tsLost() {
	s := stats()
	fmt.Printf("%d %d\n", s.TSPackets, s.TSLost)
}

func stopAll() {
	for _, d := range listDests() {
		do(http.MethodPost, fmt.Sprintf("/destinations/%d/stop", d.ID), nil)
	}
	fmt.Println("STOPPED")
}
