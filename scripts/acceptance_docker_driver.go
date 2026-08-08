//go:build ignore

// Driver for scripts/acceptance-docker.sh.
//
// The other six acceptance suites drive a binary running on this machine. This
// one drives a binary running inside the container image we ship, over the
// published port, which is the only way to catch the things that are properties
// of the IMAGE rather than of the code: a base image whose FFmpeg lost SRT, a
// Go toolchain tag that no longer satisfies go.mod, a non-root user that cannot
// write /data.
//
// Subcommands rather than one long script, because the bash side has to
// interleave docker operations (publish a stream, restart the container)
// between the API steps.
//
//	setup     <base>            first-run admin, then prove it cannot run twice
//	security  <base>            unauthenticated + missing-CSRF mutations are refused
//	dests     <base>            create the three differently-routed destinations
//	tracks    <base>            print how many audio tracks the ingest probed
//	count     <base>            print how many destinations exist (persistence)
//	startall  <base>            start every destination
//	stopall   <base>            stop every destination
//	mode      <base> <srt|rtmp> switch the ingest mode
//	addsource <base> <name>     create a source, print its id and SRT port
//	destfor   <base> <srcID> <name> <file> <track>
//	                            create a file destination on one source
//	oneport   <base> <port>     move the SRT listener to <port>
//	tokens    <base>            print "<id> <token>" per source
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	user = "admin"
	pass = "DockerAcceptance!9x"
)

var (
	client *http.Client
	base   string
	csrf   string
)

func main() {
	if len(os.Args) < 3 {
		die("usage: acceptance_docker_driver.go <subcommand> <base-url> [args]")
	}
	cmd := os.Args[1]
	base = strings.TrimSuffix(os.Args[2], "/") + "/api/v1"

	jar, _ := cookiejar.New(nil)
	client = &http.Client{Jar: jar, Timeout: 30 * time.Second}
	waitUp()

	switch cmd {
	case "setup":
		setup()
	case "security":
		security()
	case "dests":
		login()
		dests()
	case "tracks":
		login()
		tracks()
	case "count":
		login()
		count()
	case "startall":
		login()
		all("start")
	case "stopall":
		login()
		all("stop")
	case "addsource":
		if len(os.Args) < 4 {
			die("addsource needs a name")
		}
		login()
		addSource(os.Args[3])
	case "destfor":
		if len(os.Args) < 7 {
			die("destfor needs <srcID> <name> <file> <track>")
		}
		login()
		destFor(os.Args[3], os.Args[4], os.Args[5], os.Args[6])
	case "deldest":
		if len(os.Args) < 4 {
			die("deldest needs <name>")
		}
		login()
		delDest(os.Args[3])
	case "rtmpdest":
		if len(os.Args) < 7 {
			die("rtmpdest needs <name> <url> <streamKey> <track>")
		}
		login()
		rtmpDest(os.Args[3], os.Args[4], os.Args[5], os.Args[6])
	case "oneport":
		if len(os.Args) < 4 {
			die("oneport needs a port")
		}
		login()
		onePort(os.Args[3])
	case "tokens":
		login()
		tokens()
	case "mode":
		if len(os.Args) < 4 {
			die("mode needs srt|rtmp")
		}
		login()
		setMode(os.Args[3])
	default:
		die("unknown subcommand " + cmd)
	}
}

// ---------------------------------------------------------------- plumbing

func waitUp() {
	for i := 0; i < 90; i++ {
		resp, err := client.Get(base + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(time.Second)
	}
	die("server never became healthy at " + base)
}

// do performs a request carrying the session cookie and the double-submit CSRF
// header. The token is read back off the jar rather than remembered from the
// login response, so a rotation mid-run cannot desynchronise us.
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
	if u := req.URL; u != nil {
		for _, c := range client.Jar.Cookies(u) {
			if c.Name == "polyemesis_csrf" {
				csrf = c.Value
			}
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
	code, out := do(http.MethodPost, "/auth/login", map[string]string{"username": user, "password": pass})
	if code != http.StatusOK {
		die(fmt.Sprintf("login failed: %d %s", code, out))
	}
}

// ---------------------------------------------------------------- steps

func setup() {
	code, out := do(http.MethodPost, "/setup", map[string]string{"username": user, "password": pass})
	if code != http.StatusOK && code != http.StatusCreated {
		die(fmt.Sprintf("setup failed: %d %s", code, out))
	}
	// CreateUser refuses to run twice; that is what stops a stranger who finds
	// an exposed port from taking over an install that is already configured.
	code2, _ := do(http.MethodPost, "/setup", map[string]string{"username": "intruder", "password": "Intruder!9xzq"})
	if code2 == http.StatusOK || code2 == http.StatusCreated {
		fmt.Println("SETUP_REPEATABLE")
		return
	}
	fmt.Println("SETUP_OK")
}

// security asserts the two refusals a browser-facing API has to make. Both are
// negative tests: they fail loudly if a mutation SUCCEEDS.
func security() {
	// 1. No session at all.
	fresh, _ := cookiejar.New(nil)
	anon := &http.Client{Jar: fresh, Timeout: 15 * time.Second}
	body, _ := json.Marshal(map[string]any{"name": "anon", "kind": "file", "url": "anon.mkv"})
	resp, err := anon.Post(base+"/destinations", "application/json", bytes.NewReader(body))
	if err != nil {
		die(err.Error())
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		fmt.Println("ANON_ACCEPTED")
	} else {
		fmt.Println("ANON_REFUSED", resp.StatusCode)
	}

	// 2. Valid session cookie, but no CSRF header — the exact shape of a
	//    cross-site form post, which is why the cookie alone must not be enough.
	login()
	req, _ := http.NewRequest(http.MethodPost, base+"/destinations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// deliberately no X-CSRF-Token
	resp2, err := client.Do(req)
	if err != nil {
		die(err.Error())
	}
	resp2.Body.Close()
	if resp2.StatusCode == http.StatusOK || resp2.StatusCode == http.StatusCreated {
		fmt.Println("NOCSRF_ACCEPTED")
	} else {
		fmt.Println("NOCSRF_REFUSED", resp2.StatusCode)
	}
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

func profile(on ...int) map[string]any {
	return map[string]any{
		"mode": "simple", "tracks": sel(on...), "matrix": []any{},
		// Normalisation off on purpose: the bash side proves routing by
		// measuring absolute levels, and a limiter or loudnorm in the path
		// would move the very numbers under test.
		"normalize": "off", "sampleRate": 48000,
	}
}

func dests() {
	want := []struct {
		name string
		url  string
		on   []int
	}{
		{"A-track1", "destA.mkv", []int{0}},
		{"B-track2", "destB.mkv", []int{1}},
		{"C-all", "destC.mkv", []int{0, 1, 2}},
	}
	for _, w := range want {
		code, out := do(http.MethodPost, "/destinations", map[string]any{
			"name": w.name, "kind": "file", "url": w.url,
			"enabled": true, "audioBitrate": 160, "profile": profile(w.on...),
		})
		if code != http.StatusOK && code != http.StatusCreated {
			die(fmt.Sprintf("create %s failed: %d %s", w.name, code, out))
		}
	}
	fmt.Println("DESTS_OK")
}

func tracks() {
	_, out := do(http.MethodGet, "/source", nil)
	var src struct {
		Probed bool `json:"probed"`
		Tracks []struct {
			Index int `json:"index"`
		} `json:"tracks"`
	}
	_ = json.Unmarshal(out, &src)
	// PROBED, OR THE COUNT MEANS NOTHING.
	//
	// An unprobed source still carries a track list: routing.DefaultSource() is
	// six placeholder tracks, so the routing editor has something to draw before
	// a stream arrives. This function decoded `probed` and then printed the
	// length regardless, so "6" was returned for a source that had never seen a
	// packet — and the RTMP step's `>= 1` assertion was satisfied by it. The
	// engine was answering honestly; the driver was throwing the answer away.
	if !src.Probed {
		fmt.Println("unprobed")
		return
	}
	fmt.Println(len(src.Tracks))
}

func listIDs() []int64 {
	_, out := do(http.MethodGet, "/destinations", nil)
	var rows []struct {
		Destination struct {
			ID int64 `json:"id"`
		} `json:"destination"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil
	}
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.Destination.ID)
	}
	return ids
}

func count() { fmt.Println(len(listIDs())) }

func all(action string) {
	// The status is CHECKED. This used to discard it and print _OK regardless,
	// so a 500 on start-all was reported to the shell as success -- and steps
	// 4c/4d call `drive startall` with its output redirected to /dev/null, so
	// the only thing between that and a green run was a downstream byte-count
	// assertion that not every caller has.
	for _, id := range listIDs() {
		code, out := do(http.MethodPost, fmt.Sprintf("/destinations/%d/%s", id, action), nil)
		if code != http.StatusOK && code != http.StatusNoContent && code != http.StatusAccepted {
			die(fmt.Sprintf("%s destination %d failed: %d %s", action, id, code, out))
		}
	}
	fmt.Println(strings.ToUpper(action) + "_OK")
}

// addSource creates a programme and prints "<id> <srtPort>". The port is
// printed because the server may have moved it off a clash, and the publisher
// has to be pointed at the one actually in use rather than the one requested.
func addSource(name string) {
	code, out := do(http.MethodPost, "/sources", map[string]any{"name": name})
	if code != http.StatusOK && code != http.StatusCreated {
		die(fmt.Sprintf("create source failed: %d %s", code, out))
	}
	var v struct {
		ID     int64 `json:"id"`
		Ingest struct {
			SRT struct {
				Port int `json:"port"`
			} `json:"srt"`
		} `json:"ingest"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		die("decode created source: " + err.Error())
	}
	fmt.Printf("%d %d\n", v.ID, v.Ingest.SRT.Port)
}

// destFor creates a file destination that belongs to one source and carries a
// single ingest track. This is what proves separation: two destinations on two
// sources, each mixing only its own programme's audio.
// delDest removes a destination by name.
//
// 4c/4d need it because the destination they create points at a sink container
// that is torn down with them. Left on the books, step 7's `startall` brings it
// back up against a hostname that no longer resolves, and the crash-looping
// FFmpeg that results is counted by the graceful-shutdown check as a child the
// server failed to stop -- a test polluting a later test's measurement.
func delDest(name string) {
	// Same envelope listIDs reads: the list is objects WRAPPING a destination,
	// not bare destinations. Parsing {id,name} at the top level silently yields
	// zero-valued rows and "no destination named ...", which is how this first
	// failed.
	_, out := do(http.MethodGet, "/destinations", nil)
	var rows []struct {
		Destination struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"destination"`
	}
	_ = json.Unmarshal(out, &rows)
	for _, r := range rows {
		if r.Destination.Name == name {
			code, body := do(http.MethodDelete, fmt.Sprintf("/destinations/%d", r.Destination.ID), nil)
			if code != http.StatusOK && code != http.StatusNoContent {
				die(fmt.Sprintf("delete %s failed: %d %s", name, code, body))
			}
			fmt.Println("DELDEST_OK")
			return
		}
	}
	die("no destination named " + name)
}

// rtmpDest creates a destination that PUBLISHES rather than writes a file.
//
// Every routing proof in this suite until now measured a file destination, so
// the routed audio never went through an RTMP muxer on the way out. That is a
// different code path -- a file destination writes what the routing graph
// produced, an RTMP one re-encodes and publishes it -- and "the routing works"
// was being inferred across it rather than measured through it.
func rtmpDest(name, url, key, track string) {
	tr, err := strconv.Atoi(track)
	if err != nil {
		die("bad track " + track)
	}
	code, out := do(http.MethodPost, "/destinations", map[string]any{
		"name": name, "kind": "rtmp", "url": url, "streamKey": key,
		"enabled": true, "audioBitrate": 160,
		"profile": profile(tr),
	})
	if code != http.StatusOK && code != http.StatusCreated {
		die(fmt.Sprintf("create %s failed: %d %s", name, code, out))
	}
	fmt.Println("RTMPDEST_OK")
}

func destFor(srcID, name, file, track string) {
	sid, err := strconv.ParseInt(srcID, 10, 64)
	if err != nil {
		die("bad source id " + srcID)
	}
	tr, err := strconv.Atoi(track)
	if err != nil {
		die("bad track " + track)
	}
	code, out := do(http.MethodPost, "/destinations", map[string]any{
		"name": name, "kind": "file", "url": file,
		"enabled": true, "audioBitrate": 160,
		"sourceId": sid,
		"profile":  profile(tr),
	})
	if code != http.StatusOK && code != http.StatusCreated {
		die(fmt.Sprintf("create %s failed: %d %s", name, code, out))
	}
	fmt.Println("DEST_OK")
}

// onePort enables the shared SRT listener, which is install-wide settings
// rather than per-source configuration.
func onePort(port string) {
	p, err := strconv.Atoi(port)
	if err != nil {
		die("bad port " + port)
	}
	_, out := do(http.MethodGet, "/settings", nil)
	var s map[string]any
	if err := json.Unmarshal(out, &s); err != nil {
		die("settings unreadable: " + err.Error())
	}
	// There is no longer an "enable": the SRT listener IS the SRT ingest. This
	// only moves it, which the suite does so the published container port and
	// the listener agree.
	s["listeners"] = map[string]any{"srtPort": p, "rtmpPort": 1935}
	code, body := do(http.MethodPut, "/settings", s)
	if code != http.StatusOK {
		die(fmt.Sprintf("set the srt listener port failed: %d %s", code, body))
	}
	fmt.Println("ONEPORT_OK")
}

// tokens prints each source's publish token, which is what an encoder puts in
// its SRT streamid to address that programme.
func tokens() {
	_, out := do(http.MethodGet, "/sources", nil)
	var rows []struct {
		ID    int64  `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		die("sources unreadable: " + err.Error())
	}
	for _, r := range rows {
		fmt.Printf("%d %s\n", r.ID, r.Token)
	}
}

// setMode switches every source's ingest mode.
//
// THE SOURCE ROW, NOT THE SETTINGS SINGLETON. This wrote settings.ingest.mode
// for most of its life, which does nothing: the engine reads its ingest from
// the source (`settings.Ingest = src.Ingest` in engine.go), and both the
// listener gate and rtmpserver's Target.Ready test `s.Ingest.Mode` on the row.
// So "switch to RTMP" set a field nothing consults, every RTMP publish was
// refused for having no ready target, and the suite's RTMP step still passed —
// it asserted only that the probe reported at least one track, and an
// un-probed source reports the six-track placeholder layout. Six is not zero,
// so the step was green while never once ingesting RTMP.
func setMode(mode string) {
	_, out := do(http.MethodGet, "/sources", nil)
	var rows []map[string]any
	if err := json.Unmarshal(out, &rows); err != nil {
		die("sources unreadable: " + err.Error())
	}
	if len(rows) == 0 {
		die("no sources to switch")
	}
	for _, row := range rows {
		src, _ := row["source"].(map[string]any)
		if src == nil {
			src = row // some builds return the row unwrapped
		}
		id, _ := src["id"].(float64)
		ing, _ := src["ingest"].(map[string]any)
		if ing == nil {
			die("source carried no ingest block")
		}
		ing["mode"] = mode
		// Only the ingest block. handleUpdateSource decodes over the stored row,
		// so a partial body is the supported shape — and sending the whole view
		// back fails, because /sources returns a row wrapped with fields like
		// `destinations` that the source itself does not have.
		code, body := do(http.MethodPut, fmt.Sprintf("/sources/%d", int64(id)),
			map[string]any{"ingest": ing})
		if code != http.StatusOK {
			die(fmt.Sprintf("switch source %d to %s failed: %d %s", int64(id), mode, code, body))
		}
	}
	fmt.Println("MODE_" + strings.ToUpper(mode))
}

func die(msg string) {
	fmt.Fprintln(os.Stderr, "driver: "+msg)
	os.Exit(1)
}
