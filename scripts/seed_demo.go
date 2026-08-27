//go:build ignore

// Seeds a running polyemesis with data worth photographing, for
// scripts/capture-media.sh.
//
// Marketing screenshots of an empty install are worse than no screenshots: the
// product's entire argument is that each destination gets a DIFFERENT mix, and
// an empty dashboard shows none of that. So this creates the smallest
// arrangement that demonstrates the thesis — one three-track source feeding
// three destinations whose track selections differ — and leaves it running.
//
// Deliberately NOT a test asset. scripts/smoketest.go verifies behaviour and is
// wired into CI; this exists to make a screenshot true, and the two should not
// share a file where a change for one silently alters the other.
//
//	go run scripts/seed_demo.go <port>
//
// Prints the relay hub's UDP port on stdout so the caller can push a stream
// into it. Everything else goes to stderr.
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

// Must match what Playwright's auth.setup.ts will sign in with. Both read
// E2E_PASSWORD, and the calling script generates one per run for the pair --
// because when the two disagreed, the seeder created the account and the
// browser then failed on a missing <nav>, which points nowhere near the cause.
//
// REQUIRED rather than defaulted. A literal here was a password committed to a
// public repository: harmless in that it protects an account that lives for one
// test run, and still the kind of thing that gets copied into somewhere it is
// not harmless. Failing loudly costs one line in each calling script.
var password = mustEnv("E2E_PASSWORD")

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		fmt.Fprintf(os.Stderr,
			"%s is not set. The calling script generates one per run; "+
				"set it yourself to run this directly.\n", key)
		os.Exit(2)
	}
	return v
}

var (
	base   string
	client *http.Client
	csrf   string
)

func main() {
	port := "8099"
	if len(os.Args) > 1 {
		port = os.Args[1]
	}
	base = "http://127.0.0.1:" + port + "/api/v1"

	jar, _ := cookiejar.New(nil)
	client = &http.Client{Jar: jar, Timeout: 30 * time.Second}

	waitUp()

	// `waitlive` mode: sign in, then block until the ingest is actually
	// carrying bytes. It lives here rather than in the shell because
	// /api/v1/status requires a session -- an unauthenticated curl gets 401,
	// which a caller polling for '"live":true' cannot distinguish from a dead
	// stream. That mistake reported a working ingest as dead.
	if len(os.Args) > 2 && os.Args[2] == "waitlive" {
		login()
		if waitLive() {
			fmt.Fprintln(os.Stderr, "ingest is live")
			return
		}
		fmt.Fprintln(os.Stderr, "ingest never went live")
		os.Exit(1)
	}

	// First run or a re-run against the same volume. Both have to work, because
	// capturing is something you do repeatedly while adjusting a shot.
	if setupNeeded() {
		post("/setup", map[string]any{"username": "admin", "password": password})
	} else {
		post("/auth/login", map[string]any{"username": "admin", "password": password})
	}
	grabCSRF()

	// A FRESH INSTALL HAS NO PROGRAMME, AND /setup DOES NOT MAKE ONE.
	//
	// Zero-source installs boot on purpose -- an install that refused to start
	// without a source was unrecoverable -- and every programme-scoped write is
	// refused with `no_source` until one exists. The PUT below is one of those,
	// and so is annotate(), so the whole seed died on its first request with
	// "this install has no source yet" and then again on onlySourceID().
	//
	// This seeder predates that: it was written when a source arrived with the
	// install and it could simply read the one that was there. It now makes its
	// own. Named rather than defaulted, because the name is on screen in every
	// shot this script exists to take.
	if !hasSource() {
		post("/sources", map[string]any{"name": "Studio A"})
	}
	if !hasSource() {
		die("could not create the demo programme, so every scoped write below " +
			"would be refused and the shots would be of an empty install")
	}

	// Recording off, preview ON. The recorder only writes to disk for the whole
	// run and appears nowhere; the preview player is the LARGEST element on the
	// dashboard, and with it disabled the hero shot is most of a black
	// rectangle reading "Preview is disabled in Settings".
	settings := get("/settings")
	if rec, ok := settings["recording"].(map[string]any); ok {
		rec["enabled"] = false
	}
	if prev, ok := settings["preview"].(map[string]any); ok {
		prev["enabled"] = true
	}
	put("/settings", settings)

	// Label the incoming tracks. This is the step that makes every later screen
	// legible — without it the routing editor reads "Track 1 / Track 2 /
	// Track 3" and the screenshot argues nothing.
	annotate()

	// Every destination has to name the programme it belongs to; the server no
	// longer picks one. Read back rather than assumed to be 1.
	sid := onlySourceID()
	for _, d := range demoDestinations {
		post("/destinations", d.body(sid))
	}

	// THE LOUDNESS MONITOR, ON, because the front page's whole proof section
	// rests on it.
	//
	// web/src/pages/index.astro quotes three per-destination LUFS figures in
	// body copy and in the alt text, and says they differ because each
	// destination was sent a different set of tracks. None of that renders
	// unless the analyser is running: the meters page otherwise shows
	// "Loudness compliance -- NOT UPDATING" and three near-identical ingest
	// tracks, which argues nothing at all. The committed screenshot had the
	// readings and no step here produced them, so it was a one-off nobody
	// could regenerate.
	//
	// Scoped, and it has to be: PUT /loudness is one of the three routes that
	// were refused outright on a multi-programme install until #606.
	put("/loudness"+fmt.Sprintf("?source=%d", sid), map[string]any{"enabled": true})

	// A SECOND PROGRAMME, because the dashboard is a different page with two.
	//
	// Dashboard.tsx divides the destination area into per-programme lanes and
	// badges each card with the programme it carries -- and does neither with
	// one source, which its own comment calls "the shape this page had before
	// per-source anything". Every screenshot this harness had ever taken was
	// therefore of the degenerate case, and the multi-programme behaviour that
	// most of the scoping work exists for appeared nowhere.
	//
	// It gets its OWN destinations rather than sharing: a lane with nothing in
	// it photographs as a programme that is not working, which is the opposite
	// of the claim. It also gets its own stream -- see capture-media.sh, which
	// reads the third line below -- because a second lane reading "Offline"
	// beside a live one argues that multi-source is broken.
	second := &secondProgramme
	if !hasSourceNamed(second.name) {
		post("/sources", map[string]any{"name": second.name})
	}
	secondID := sourceIDNamed(second.name)
	if secondID == 0 {
		die("could not create the second programme %q, so the lanes shot would "+
			"photograph a single-programme dashboard under a multi-programme name",
			second.name)
	}
	for _, d := range second.destinations {
		post("/destinations", d.body(secondID))
	}

	// Three lines on stdout: the relay port, the first programme's publish
	// token, then the second's.
	//
	// The token is what lets the caller push through the REAL SRT ingest rather
	// than injecting into the relay hub. That distinction is invisible in a
	// screenshot of the routing page and glaring on the dashboard, which reads
	// "Ingest Offline" with every track "no signal" when the ingest itself
	// never saw a publisher.
	relay := int(get("/stats")["relay"].(map[string]any)["port"].(float64))
	fmt.Println(relay)
	fmt.Println(sourceToken())
	fmt.Println(tokenForSource(secondID))
	fmt.Fprintf(os.Stderr, "seeded: %d + %d destinations over 2 programmes, relay on udp/%d\n",
		len(demoDestinations), len(second.destinations), relay)
}

// The arrangement itself. Three destinations over three tracks, chosen so no
// two share a selection and every track is used by something — which is what
// makes the mix matrix in a screenshot read as a matrix rather than a list.
var demoDestinations = []demoDest{
	{"YouTube — full mix", "youtube.mkv", []int{0, 1, 2}},
	{"Twitch — no music", "twitch.mkv", []int{0, 2}},
	{"Podcast — mic only", "podcast.mkv", []int{0}},
}

// THE SECOND PROGRAMME. A different shape from the first on purpose: two
// destinations rather than three, and a track selection the first does not
// use, so the lanes read as two different shows rather than as one list that
// happened to be cut in half.
var secondProgramme = struct {
	name         string
	destinations []demoDest
}{
	name: "Studio B — panel show",
	destinations: []demoDest{
		{"YouTube — panel", "panel-youtube.mkv", []int{0, 1}},
		{"Archive — hosts only", "panel-archive.mkv", []int{1}},
	},
}

type demoDest struct {
	name   string
	file   string
	tracks []int
}

func (d demoDest) body(sourceID int64) map[string]any {
	on := map[int]bool{}
	for _, t := range d.tracks {
		on[t] = true
	}
	rows := make([]map[string]any, 0, 6)
	for i := 0; i < 6; i++ {
		rows = append(rows, map[string]any{"track": i, "enabled": on[i], "gain": 1.0})
	}
	return map[string]any{
		"name": d.name, "kind": "file", "platform": "custom",
		"sourceId": sourceID,
		"url":      d.file, "enabled": true, "audioBitrate": 160,
		"profile": map[string]any{
			"mode": "simple", "tracks": rows, "normalize": "auto", "sampleRate": 48000,
		},
	}
}

// annotate names the incoming tracks on the source, retrying while the engine
// probes the layout: annotations are indexed against probed tracks, so writing
// them before the probe lands is a silent no-op.
func annotate() {
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		si := get("/source")
		if tr, _ := si["tracks"].([]any); si["probed"] == true && len(tr) >= 3 {
			break
		}
		time.Sleep(1500 * time.Millisecond)
	}
	settings := get("/settings")
	ing, ok := settings["ingest"].(map[string]any)
	if !ok {
		return
	}
	ing["annotations"] = []map[string]any{
		{"track": 0, "role": "mic", "label": "Host mic"},
		{"track": 1, "role": "music", "label": "Music bed"},
		{"track": 2, "role": "commentary", "label": "Co-host"},
	}
	put("/settings", settings)
}

// login authenticates an already-set-up install.
func login() {
	post("/auth/login", map[string]any{"username": "admin", "password": password})
	grabCSRF()
}

// waitLive blocks until bytes are actually arriving AND the layout is probed.
//
// There is no `source.live` field on /api/v1/status. An earlier version of this
// checked one, having taken the name from the MQTT payload documented in
// docs/MQTT.md -- which does publish `live`, computed elsewhere. The API and the
// MQTT state are different shapes, and polling a field that does not exist
// reports every healthy stream as dead.
//
// relay.rxBytes is the honest signal, and is what MQTT's `live` means: bytes on
// the relay rather than process state. An SRT listener sits in "running" for as
// long as it waits for a publisher, which is a different question.
//
// source.probed as well, because bytes alone are not enough for a screenshot:
// until the layout is probed the routing editor has no tracks to draw.
// EVERY PROGRAMME, EACH ONE NAMED.
//
// This polled `/status` with no programme, which was fine while an install had
// exactly one. The moment a second exists the server refuses the unscoped route
// with 400 `source_required` -- correctly, since "the status" is not a question
// with one answer any more -- and this loop read the refusal as "not live yet",
// waited out its ninety seconds and reported a perfectly healthy pair of
// streams as dead. The capture then refused to photograph them, which is the
// guard doing its job on a lie.
//
// Every programme rather than the first: the second one has its own publisher,
// and a lanes screenshot whose second lane is still black is the exact image
// that argues multi-source does not work.
func waitLive() bool {
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		sources := listSources()
		if len(sources) == 0 {
			time.Sleep(2 * time.Second)
			continue
		}
		live := 0
		for _, row := range sources {
			id, ok := row["id"].(float64)
			if !ok {
				continue
			}
			st := get(fmt.Sprintf("/status?source=%d", int64(id)))
			var bytesIn float64
			if rl, ok := st["relay"].(map[string]any); ok {
				bytesIn, _ = rl["rxBytes"].(float64)
			}
			probed := false
			if src, ok := st["source"].(map[string]any); ok {
				probed, _ = src["probed"].(bool)
			}
			if bytesIn > 0 && probed {
				live++
			}
		}
		if live == len(sources) {
			fmt.Fprintf(os.Stderr, "  %d of %d programmes live\n", live, len(sources))
			return true
		}
		fmt.Fprintf(os.Stderr, "  %d of %d programmes live, waiting\n", live, len(sources))
		time.Sleep(2 * time.Second)
	}
	return false
}

// sourceToken returns the default source's publish token, which is its address
// on the shared SRT port -- `streamid=<token>`. Empty if it cannot be read, and
// onlySourceID is the programme these demo destinations belong to.
//
// Unlike sourceToken this cannot degrade to "" and carry on: a create with no
// source is refused outright, so a seed that could not find one has nothing to
// seed and should say so rather than emit a wall of 400s.
// Whether the install has a programme at all. Distinct from onlySourceID,
// which DIES when there is none -- that is the right behaviour at the point
// destinations are attached and the wrong one here, where the answer "none"
// is actionable.
func hasSource() bool {
	r, err := client.Get(base + "/sources")
	if err != nil {
		return false
	}
	defer r.Body.Close()
	var list []struct {
		ID int64 `json:"id"`
	}
	if json.NewDecoder(r.Body).Decode(&list) != nil {
		return false
	}
	return len(list) > 0
}

// A source by NAME. onlySourceID takes the first row, which stops being the
// right answer the moment there are two.
func sourceIDNamed(name string) int64 {
	for _, row := range listSources() {
		if n, _ := row["name"].(string); n == name {
			if id, ok := row["id"].(float64); ok {
				return int64(id)
			}
		}
	}
	return 0
}

func hasSourceNamed(name string) bool { return sourceIDNamed(name) != 0 }

func tokenForSource(id int64) string {
	for _, row := range listSources() {
		if rid, ok := row["id"].(float64); ok && int64(rid) == id {
			tok, _ := row["token"].(string)
			return tok
		}
	}
	return ""
}

func listSources() []map[string]any {
	r, err := client.Get(base + "/sources")
	if err != nil {
		return nil
	}
	defer r.Body.Close()
	var list []map[string]any
	if json.NewDecoder(r.Body).Decode(&list) != nil {
		return nil
	}
	return list
}

func onlySourceID() int64 {
	r, err := client.Get(base + "/sources")
	if err != nil {
		die("list sources: " + err.Error())
	}
	defer r.Body.Close()
	var list []struct {
		ID int64 `json:"id"`
	}
	if json.NewDecoder(r.Body).Decode(&list) != nil || len(list) == 0 {
		die("no source to attach the demo destinations to")
	}
	return list[0].ID
}

// the caller falls back to relay injection rather than failing the capture.
func sourceToken() string {
	r, err := client.Get(base + "/sources")
	if err != nil {
		return ""
	}
	defer r.Body.Close()
	var list []map[string]any
	if json.NewDecoder(r.Body).Decode(&list) != nil || len(list) == 0 {
		return ""
	}
	tok, _ := list[0]["token"].(string)
	return tok
}

// ------------------------------------------------------------------ plumbing

func waitUp() {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if r, err := client.Get(base + "/health"); err == nil {
			r.Body.Close()
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	die("server never answered /health at %s", base)
}

func setupNeeded() bool {
	r, err := client.Get(base + "/setup")
	if err != nil {
		die("GET /setup: %v", err)
	}
	defer r.Body.Close()
	var m map[string]any
	json.NewDecoder(r.Body).Decode(&m)
	// A field named either way across versions; absent means "already set up".
	if v, ok := m["needsSetup"].(bool); ok {
		return v
	}
	if v, ok := m["required"].(bool); ok {
		return v
	}
	return false
}

func grabCSRF() {
	u, _ := http.NewRequest("GET", base+"/health", nil)
	for _, c := range client.Jar.Cookies(u.URL) {
		if strings.Contains(strings.ToLower(c.Name), "csrf") {
			csrf = c.Value
		}
	}
}

func get(path string) map[string]any {
	r, err := client.Get(base + path)
	if err != nil {
		die("GET %s: %v", path, err)
	}
	defer r.Body.Close()
	var m map[string]any
	json.NewDecoder(r.Body).Decode(&m)
	return m
}

func post(path string, body any) { send("POST", path, body) }
func put(path string, body any)  { send("PUT", path, body) }

func send(method, path string, body any) {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, base+path, bytes.NewReader(b))
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
		msg, _ := io.ReadAll(r.Body)
		// Not fatal: re-seeding an already-seeded install duplicates names and
		// is refused, which is fine and expected on a second capture run.
		fmt.Fprintf(os.Stderr, "  %s %s -> %d: %s\n", method, path, r.StatusCode, strings.TrimSpace(string(msg)))
	}
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "seed: "+format+"\n", a...)
	os.Exit(1)
}
