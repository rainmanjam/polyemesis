//go:build ignore

// Driver for scripts/acceptance-synth.sh.
//
// Pull inverts the ingest: polyemesis dials a source rather than waiting for an
// encoder. Everything downstream -- probing, the relay, per-destination routing
// -- is supposed to be identical once the bytes arrive, and this driver exists
// to prove that rather than assume it.
//
//	(no subcommand)  set up, switch to pull, create two destinations
//	tracks           print how many audio tracks were probed
//	stopall          stop every destination so its file finalises
//	escape           try a file:// source outside the data directory
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
	pass = "SynthAcceptance!9x"
)

var (
	client *http.Client
	base   string
	csrf   string
)

func main() {
	if len(os.Args) < 2 {
		die("usage: acceptance_pull_driver.go <base-url> [subcommand]")
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
	case "slate-escape":
		login()
		slateEscape()
	case "proclog":
		if len(os.Args) < 4 {
			die("usage: proclog <process-name>")
		}
		login()
		procLog(os.Args[3])
	default:
		die("unknown subcommand " + cmd)
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

// pullMode points the ingest at the loop file, relative to the data directory.
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
	pull["url"] = "file://recordings/videoonly.ts"

	code, body := do(http.MethodPut, "/settings", s)
	if code != http.StatusOK {
		die(fmt.Sprintf("switch to pull failed: %d %s", code, body))
	}
	fmt.Println("PULL_OK")
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
	for _, w := range []struct {
		name, url string
		track     int
	}{
		// Track 1 on a source that has NO audio: without the silence tier this
		// destination has nothing to select and crash-loops.
		{"synth", "synth.mkv", 0},
	} {
		code, out := do(http.MethodPost, "/destinations", map[string]any{
			"name": w.name, "kind": "file", "url": w.url,
			"enabled": true, "audioBitrate": 160, "profile": profile(w.track),
		})
		if code != http.StatusOK && code != http.StatusCreated {
			die(fmt.Sprintf("create %s failed: %d %s", w.name, code, out))
		}
	}
	fmt.Println("DEST_OK")
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

// procLog prints a process's own stderr, which the server keeps in a ring and
// publishes over the event bus rather than writing to its log file.
//
// Without this a crash-looping process is diagnosed entirely from the outside:
// the server log says it exited and with what status, and the one thing that
// would explain why -- what FFmpeg itself said -- is only visible in a browser.
func procLog(name string) {
	_, out := do(http.MethodGet, "/processes/"+name+"/logs", nil)
	var got struct {
		Lines []struct {
			Text string `json:"text"`
		} `json:"lines"`
	}
	_ = json.Unmarshal(out, &got)
	for _, l := range got.Lines {
		fmt.Println(l.Text)
	}
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

// slateEscape checks the slate image confinement.
//
// The slate path is a file this process opens to paint a holding frame, so an
// unconfined one reads anything the server can. Same reasoning as a pull
// source, and worth an end-to-end check for the same reason: confinement that
// holds in the validator but not in the running server is worth nothing.
func slateEscape() {
	_, out := do(http.MethodGet, "/settings", nil)
	var s map[string]any
	_ = json.Unmarshal(out, &s)
	fo, _ := s["failover"].(map[string]any)
	if fo == nil {
		fo = map[string]any{}
		s["failover"] = fo
	}
	slate, _ := fo["slate"].(map[string]any)
	if slate == nil {
		slate = map[string]any{}
		fo["slate"] = slate
	}
	slate["enabled"] = true
	slate["imagePath"] = "../../../../etc/passwd"

	code, body := do(http.MethodPut, "/settings", s)
	if code == http.StatusOK {
		fmt.Println("SLATE_ACCEPTED")
		return
	}
	fmt.Printf("SLATE_REFUSED %d %s\n", code, strings.TrimSpace(string(body)))
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
