//go:build ignore

// Package main -- the pull-ingest driver that acceptance-pull.sh and
// acceptance-synth.sh were two forked copies of.
//
// The two drivers were 312 and 255 lines with 77 lines of difference between
// them, and almost all of that difference was DATA: a password, one pull URL,
// the destination fixtures, and the sentinel each one prints for its shell to
// grep. Ten functions -- waitUp, do, login, setup, sel, profile, tracks,
// stopAll, escape and die -- were byte-identical. The fork showed: synth's
// usage string named acceptance_pull_driver.go, its header comment described
// pull, and it carried a copy of `escape` that its own subcommand switch had no
// case for and could never reach.
//
// So the shape here is not "extract some helpers". It is one driver, with the
// two suites supplying the values that actually differ.
//
// NOT A PACKAGE, AND IT CANNOT BE ONE, for the same reason as
// driverhelpers.go beside it: these suites run from $WORK under /tmp, outside
// any module, where a module import cannot resolve. Naming the file on the
// `go run` line compiles it into the same synthesized package instead. See
// driverhelpers.go for the cmd/go wording and why //go:build ignore above is
// both honoured (by `go build ./...`) and ignored (when named).
//
// WHY NOT SHARE driverhelpers.go. Because the two families speak different
// dialects and merging them would be a behaviour change, not a tidy-up:
//
//   - `do` returns (status, body) and lets its caller decide what a non-2xx
//     means. `call` over there treats >=400 as fatal. The escape checks below
//     depend on reaching a REFUSAL and printing its code, so they need `do`.
//   - `die` here takes one string; the other is variadic with a format.
//   - `waitUp` here polls once a second for 60s and requires HTTP 200; the
//     other polls every 300ms and accepts any response.
//
// WHAT EACH DRIVER STILL DECLARES: pass, pullURL, pullModeDone, destsDone and
// destFixtures, plus whatever subcommands are its own. Those are the file's
// contract with this one; the compiler enforces it, since a driver naming this
// file without them will not build.

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

const user = "admin"

var (
	client *http.Client
	base   string
	csrf   string
)

// destSpec is one file destination a suite asks for: a name, the file it
// writes, and the single audio track it selects.
type destSpec struct {
	name, url string
	track     int
}

// run is the whole of both drivers' main().
//
// The shared subcommands are handled here; `own` gets first refusal on anything
// else so a suite can add its own without this file knowing about it, and
// returns false to fall through to the unknown-subcommand refusal.
func run(driver string, own func(cmd string) bool) {
	if len(os.Args) < 2 {
		die("usage: " + driver + " <base-url> [subcommand]")
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
		pullMode()
		dests()
	case "tracks":
		login()
		tracks()
	case "stopall":
		login()
		stopAll()
	default:
		if !own(cmd) {
			die("unknown subcommand " + cmd)
		}
	}
}

func waitUp() {
	for i := 0; i < 60; i++ {
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
	// The programme every step below acts on. A fresh install has none since
	// #387; see acceptance_driver.go for the full reason.
	if code, out := do(http.MethodPost, "/sources",
		map[string]any{"name": "Main", "enabled": true}); code != http.StatusOK &&
		code != http.StatusCreated {
		die(fmt.Sprintf("create the first source: %d %s", code, out))
	}
	fmt.Println("SETUP_OK")
}

// pullMode points the ingest at the suite's file, relative to the data
// directory.
//
// Relative on purpose: a file:// pull source is confined to the data directory
// exactly as a file destination is, and using the confined form here is what
// makes the escape check below meaningful rather than theatre.
func pullMode() {
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
	pull["url"] = pullURL

	code, body := do(http.MethodPut, "/settings", s)
	if code != http.StatusOK {
		die(fmt.Sprintf("switch to pull failed: %d %s", code, body))
	}
	fmt.Println(pullModeDone)
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
		// Normalisation off: the suite proves routing by measuring absolute
		// levels, and a limiter in the path would move the numbers under test.
		"normalize": "off", "sampleRate": 48000,
	}
}

func dests() {
	for _, w := range destFixtures {
		code, out := do(http.MethodPost, "/destinations", map[string]any{
			"name": w.name, "kind": "file", "url": w.url,
			"enabled": true, "audioBitrate": 160, "profile": profile(w.track),
		})
		if code != http.StatusOK && code != http.StatusCreated {
			die(fmt.Sprintf("create %s failed: %d %s", w.name, code, out))
		}
	}
	fmt.Println(destsDone)
}

func tracks() {
	_, out := do(http.MethodGet, "/source", nil)
	var src struct {
		Tracks []struct {
			Index int `json:"index"`
		} `json:"tracks"`
	}
	_ = json.Unmarshal(out, &src)
	fmt.Println(len(src.Tracks))
}

func stopAll() {
	_, out := do(http.MethodGet, "/destinations", nil)
	var rows []struct {
		Destination struct {
			ID int64 `json:"id"`
		} `json:"destination"`
	}
	_ = json.Unmarshal(out, &rows)
	for _, r := range rows {
		do(http.MethodPost, fmt.Sprintf("/destinations/%d/stop", r.Destination.ID), nil)
	}
	fmt.Println("STOP_OK")
}

// escape checks the confinement.
//
// A pull source is a path this process opens, so an unconfined one is an
// arbitrary-file-read primitive for anyone who reaches the API. The check is
// here rather than only in a unit test because confinement that holds in the
// validator but not in the running server is worth nothing.
func escape() {
	_, out := do(http.MethodGet, "/settings", nil)
	var s map[string]any
	_ = json.Unmarshal(out, &s)
	ing, _ := s["ingest"].(map[string]any)
	pull, _ := ing["pull"].(map[string]any)
	pull["url"] = "file://../../../../etc/passwd"

	code, body := do(http.MethodPut, "/settings", s)
	if code == http.StatusOK {
		fmt.Println("ESCAPE_ACCEPTED")
		return
	}
	fmt.Printf("ESCAPE_REFUSED %d %s\n", code, strings.TrimSpace(string(body)))
}

func die(msg string) {
	fmt.Fprintln(os.Stderr, "driver: "+msg)
	os.Exit(1)
}
