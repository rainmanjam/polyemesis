//go:build ignore

// Driver for scripts/acceptance-obs-multitrack.sh.
//
// Everything else that exercises the E-RTMP multitrack path uses FFmpeg as the
// publisher. This one uses OBS, which is the encoder the feature exists for and
// the one thing docs/evidence/enhanced-rtmp-multitrack.md still lists as
// unconfirmed: OBS's RTMP connect/handshake, the onMetaData it sends, and the
// trackIds it assigns are its own code, not FFmpeg's.
//
// Subcommands:
//
//	(none)     first-run setup, put the source on RTMP ingest, print the token
//	tracks     what polyemesis says arrived: <count> <layout,layout,...>
//	waitlive   block until bytes are on the relay and the layout is probed
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"
	"time"
)

// ingestPort must match INGEST in acceptance-obs-multitrack.sh.
const ingestPort = 1935

var (
	base     string
	client   *http.Client
	csrf     string
	password = mustEnv("E2E_PASSWORD")
)

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		die("%s must be set; the calling script generates one per run", k)
	}
	return v
}

func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "driver: "+f+"\n", a...)
	os.Exit(1)
}

func main() {
	if len(os.Args) < 2 {
		die("usage: acceptance_obs_driver.go <base-url> [subcommand]")
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
	case "tracks":
		login()
		tracks()
	case "waitlive":
		login()
		if !waitLive() {
			die("no bytes ever reached the relay")
		}
		fmt.Println("LIVE")
	default:
		die("unknown subcommand %s", cmd)
	}
}

// setup creates the admin, switches the source to RTMP ingest, and prints the
// publish token — which is the stream key OBS must use, since the shared
// listener addresses sources by key.
func setup() {
	if setupNeeded() {
		post("/setup", map[string]any{"username": "admin", "password": password})
		grabCSRF()
		// The programme sourceToken() below reads the publish key off. A fresh
		// install has none since #387; see acceptance_driver.go for the full
		// reason. Inside the setupNeeded branch on purpose: the else branch is
		// a re-run against a server that already has one, and a second source
		// would change which token "the first" resolves to.
		post("/sources", map[string]any{"name": "Main", "enabled": true})
	} else {
		login()
	}

	s := getRaw("/settings")
	ing, _ := s["ingest"].(map[string]any)
	if ing == nil {
		die("settings carried no ingest block")
	}
	// RTMP, because that is what OBS speaks and what multitrack rides on.
	ing["mode"] = "rtmp"
	rtmp, _ := ing["rtmp"].(map[string]any)
	if rtmp == nil {
		rtmp = map[string]any{}
		ing["rtmp"] = rtmp
	}
	rtmp["app"] = "live"
	// Empty: the token below is the address, and a legacy key alongside it would
	// only add a second way in that this test does not use.
	rtmp["streamKey"] = ""
	s["listeners"] = map[string]any{"srtPort": 6000, "rtmpPort": ingestPort}
	put("/settings", s)

	fmt.Println(sourceToken())
}

func sourceToken() string {
	r, err := client.Get(base + "/sources")
	if err != nil {
		die("GET /sources: %v", err)
	}
	defer r.Body.Close()
	var list []map[string]any
	if json.NewDecoder(r.Body).Decode(&list) != nil || len(list) == 0 {
		die("no sources")
	}
	if tok, _ := list[0]["token"].(string); tok != "" {
		return tok
	}
	if src, ok := list[0]["source"].(map[string]any); ok {
		if tok, _ := src["token"].(string); tok != "" {
			return tok
		}
	}
	die("the source carries no publish token")
	return ""
}

// tracks reports what polyemesis PROBED, which is the whole question: OBS said
// it sent N tracks, and this is how many arrived and what shape they are.
func tracks() {
	src := getRaw("/source")
	list, _ := src["tracks"].([]any)
	layouts := make([]string, 0, len(list))
	for _, t := range list {
		m, _ := t.(map[string]any)
		if m == nil {
			continue
		}
		ch, _ := m["channels"].(float64)
		codec, _ := m["codec"].(string)
		layouts = append(layouts, fmt.Sprintf("%s/%dch", codec, int(ch)))
	}
	fmt.Printf("%d %s\n", len(layouts), strings.Join(layouts, ","))
}

func waitLive() bool {
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		st := getRaw("/status")
		var rx float64
		if rl, ok := st["relay"].(map[string]any); ok {
			rx, _ = rl["rxBytes"].(float64)
		}
		probed := false
		if src, ok := st["source"].(map[string]any); ok {
			probed, _ = src["probed"].(bool)
		}
		if rx > 0 && probed {
			fmt.Fprintf(os.Stderr, "  relay rxBytes=%.0f probed=true\n", rx)
			return true
		}
		time.Sleep(2 * time.Second)
	}
	return false
}

// ------------------------------------------------------------------ plumbing

func login() {
	post("/auth/login", map[string]any{"username": "admin", "password": password})
	grabCSRF()
}

func setupNeeded() bool {
	r, err := client.Get(base + "/setup")
	if err != nil {
		die("GET /setup: %v", err)
	}
	defer r.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(r.Body).Decode(&out)
	need, _ := out["needsSetup"].(bool)
	return need
}

func grabCSRF() {
	u := client.Jar
	if u == nil {
		return
	}
	req, _ := http.NewRequest(http.MethodGet, base+"/setup", nil)
	for _, c := range client.Jar.Cookies(req.URL) {
		if c.Name == "polyemesis_csrf" {
			csrf = c.Value
		}
	}
}

func getRaw(path string) map[string]any {
	r, err := client.Get(base + path)
	if err != nil {
		die("GET %s: %v", path, err)
	}
	defer r.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(r.Body).Decode(&out); err != nil {
		die("GET %s: %v", path, err)
	}
	return out
}

func post(path string, body any) { send(http.MethodPost, path, body) }
func put(path string, body any)  { send(http.MethodPut, path, body) }

func send(method, path string, body any) {
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, base+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	r, err := client.Do(req)
	if err != nil {
		die("%s %s: %v", method, path, err)
	}
	defer r.Body.Close()
	if r.StatusCode >= 300 {
		var b bytes.Buffer
		_, _ = b.ReadFrom(r.Body)
		die("%s %s failed: %d %s", method, path, r.StatusCode, b.String())
	}
	grabCSRF()
}

func waitUp() {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if r, err := client.Get(base + "/setup"); err == nil {
			r.Body.Close()
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	die("server never became healthy at %s", base)
}
