// Package driverlib is the plumbing the acceptance drivers under scripts/
// share: one authenticated HTTP session against a server's /api/v1, and the
// handful of calls every suite has to make before it can measure anything.
//
// WHY THIS EXISTS, AND IT IS NOT TIDINESS.
//
// scripts/acceptance_failover_driver.go and
// scripts/acceptance_multistream_driver.go carried ELEVEN functions with the
// same names, and most of them the same bodies. The cost of that is on record
// in this repository. The failover driver's stopAll decoded GET /destinations
// as a bare array of destinations when the endpoint actually returns
// [{"destination": {...}, "routing": {...}}]; it therefore read id 0 off every
// row, POSTed /destinations/0/stop, took the 404 without looking and printed
// STOPPED. No destination was ever stopped, every recording the timeline checks
// read was an unfinalised Matroska, and the checks were then written around
// that damage rather than against it -- the duration check reads the last
// decode timestamp to this day because "format=duration" came back N/A.
//
// That fault was fixed in one copy. A second copy is a second home for the same
// class of fault and a fix that does not travel: a correction to Do's CSRF
// handling or Login's error path lands in one driver and silently not the
// other. One copy is the point.
//
// WHAT IS HERE IS ONLY WHAT IS GENUINELY SHARED. Behaviour that differs between
// the two suites -- how each dispatches subcommands, what shape of destination
// each creates, what each decodes out of /status -- stays in the driver that
// owns it. A shared function that needs a flag to tell its two callers apart is
// two functions wearing one name, and that is worse than the duplication it
// replaces.
//
// A NOTE ON HOW THE DRIVERS ARE BUILT. Both are `//go:build ignore` standalone
// files compiled with `go build scripts/<file>.go`. That works with this import
// -- verified, not assumed -- but only when the build runs with a working
// directory inside the module, because `go build` resolves a module import
// against the current directory's go.mod and not against the source file's
// location. Both suites cd into their work directory before building, so both
// build lines now run from $ROOT in a subshell. A driver with none but standard
// library imports did not care; one with this import does.
package driverlib

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
	// SettingsPath is the settings endpoint, exported because the multistream
	// suite's credential sweep names it as one of the renderings a key could
	// have escaped to, and a suite that swept a mistyped path would report SAFE
	// for the wrong reason.
	SettingsPath = "/settings"

	// ProfileTracks is how many track rows a destination profile declares.
	//
	// Six because that is what OBS sends and what routing.PlaceholderTracks
	// guesses, so a profile of that width is the ordinary shape rather than one
	// tailored to a fixture. Rows past the running ingest's width compile to
	// nothing, which is why a profile can safely be wider than the source.
	ProfileTracks = 6

	// srtPort is the install-wide SRT listener. Every suite here publishes over
	// RTMP -- the Homebrew FFmpeg on macOS is built without libsrt and can
	// neither listen nor publish on SRT -- but the listeners block has to carry
	// a value for both, and moving SRT off its default would be a change no
	// suite asked for.
	srtPort = 6000

	// httpTimeout bounds every call. Generous rather than tight: these drivers
	// talk to a server that is starting encoders, and a timeout that fired
	// during a legitimate spawn would be reported as a product fault.
	httpTimeout = 30 * time.Second
)

var (
	client *http.Client
	base   string
	csrf   string
)

// Init points this process at one server and prepares the cookie jar the whole
// session runs on.
//
// The jar is the reason this is stateful package-level rather than a value
// handed around: login sets a session cookie AND a CSRF cookie, and every
// mutating call afterwards has to present both. A driver that built a fresh
// client per call would be unauthenticated on every call after the first.
func Init(baseURL string) {
	base = strings.TrimSuffix(baseURL, "/") + "/api/v1"
	jar, _ := cookiejar.New(nil)
	client = &http.Client{Jar: jar, Timeout: httpTimeout}
}

// WaitUp blocks until the server answers /health, and gives up after a minute.
//
// A minute rather than a few seconds because the server builds its database and
// opens its listeners on the way up, and a driver that gave up early would
// report "server never became healthy" for a server that was merely still
// starting -- a false accusation the suite would then spend a run chasing.
func WaitUp() {
	for i := 0; i < 60; i++ {
		resp, err := client.Get(base + "/health")
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(time.Second)
	}
	Die("server never became healthy at " + base)
}

// Do issues one API call and returns the status and the raw body.
//
// The status is RETURNED rather than checked here, because "what counts as a
// good answer" is the caller's to decide: /setup answers 200 or 201, a stop on
// an already-stopped destination is a finding rather than a fault, and a suite
// that measures a refusal needs the refusal.
//
// THE CSRF TOKEN IS RE-READ FROM THE JAR ON EVERY CALL, not cached at login.
// The server rotates it, and a driver holding the token it was issued at login
// starts failing mutating calls partway through a long run -- which reads from
// the outside exactly like the product rejecting a legitimate request.
func Do(method, path string, body any) (int, []byte) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, base+path, rdr)
	if err != nil {
		Die(err.Error())
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
		Die(err.Error())
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// GetJSON fetches path and decodes it into v, dying with what on either
// failure.
//
// IT CHECKS THE STATUS CODE, and that is the half worth spelling out. An error
// body is still valid JSON: {"error":"..."} unmarshals into a status document
// happily and leaves every field zero, so a driver that decoded without looking
// at the code would print a confident "none -1 false -1" for a call that was
// refused. One of the two drivers checked and one did not; the checking one is
// what survives here.
func GetJSON(path, what string, v any) {
	code, out := Do(http.MethodGet, path, nil)
	if code != http.StatusOK {
		Die(fmt.Sprintf("cannot read %s: %d %s", what, code, out))
	}
	if err := json.Unmarshal(out, v); err != nil {
		Die(what + " unreadable: " + err.Error())
	}
}

// Login authenticates the session. Every subcommand that reads or writes
// anything calls it first.
func Login(user, pass string) {
	if code, out := Do(http.MethodPost, "/auth/login",
		map[string]string{"username": user, "password": pass}); code != http.StatusOK {
		Die(fmt.Sprintf("login failed: %d %s", code, out))
	}
}

// Setup performs first-run setup, creating the admin account the rest of the
// suite logs in as.
//
// 200 AND 201 are both accepted. The endpoint has answered both over its life
// and the distinction is not something a suite should have an opinion about --
// what matters is that an account now exists.
func Setup(user, pass string) {
	code, out := Do(http.MethodPost, "/setup", map[string]string{"username": user, "password": pass})
	if code != http.StatusOK && code != http.StatusCreated {
		Die(fmt.Sprintf("setup failed: %d %s", code, out))
	}
	fmt.Println("SETUP_OK")
}

// LoadSettings reads the whole settings document.
//
// THE WHOLE DOCUMENT, ALWAYS, because PUT /settings REPLACES the settings.
// Posting a lone block resets everything else to defaults -- which for these
// suites means the ingest listener moves off the port the publisher is dialling
// and the run strands, a failure that looks from the outside exactly like the
// thing under measurement. Read, modify the one key, save the whole thing back.
func LoadSettings() map[string]any {
	var s map[string]any
	GetJSON(SettingsPath, "settings", &s)
	return s
}

// SaveSettings writes the document back. what names the operation for the
// failure message, so a refused save says which change was refused rather than
// just "settings".
func SaveSettings(s map[string]any, what string) {
	if code, body := Do(http.MethodPut, SettingsPath, s); code != http.StatusOK {
		Die(fmt.Sprintf("%s failed: %d %s", what, code, body))
	}
}

// UseRTMPIngest rewrites the settings document's ingest to an RTMP listener on
// rtmpPort. It modifies s in place; the caller saves it.
//
// RTMP RATHER THAN SRT, and the reason is the host toolchain rather than a
// preference. The Homebrew FFmpeg on macOS is built without libsrt, so it can
// neither listen nor publish on SRT, and both of these suites have to run on a
// developer's machine. What either one measures -- the timeline across a feed
// swap, or which mix each destination receives -- is independent of how the
// bytes arrived. SRT ingest is covered by the container suites, which ship an
// FFmpeg that has it.
//
// THE STREAM KEY IS SET EMPTY on purpose. There is one shared RTMP listener for
// the whole install now and it addresses sources BY PER-SOURCE PUBLISH TOKEN,
// which PublishKey below reads back. A publisher with no token, or the wrong
// one, is refused at the handshake and the suite sees an encoder that connected
// and then died with a broken pipe.
func UseRTMPIngest(s map[string]any, rtmpPort int) {
	ing, _ := s["ingest"].(map[string]any)
	if ing == nil {
		Die("settings carried no ingest block")
	}
	ing["mode"] = "rtmp"
	rtmp, _ := ing["rtmp"].(map[string]any)
	if rtmp == nil {
		rtmp = map[string]any{}
		ing["rtmp"] = rtmp
	}
	rtmp["app"] = "live"
	rtmp["streamKey"] = ""
	// The port is install-wide now, not a property of the source.
	s["listeners"] = map[string]any{"srtPort": srtPort, "rtmpPort": rtmpPort}
}

// PublishKey prints the stream key an encoder must use to reach the source.
//
// Read from the API rather than pinned as a constant here, which keeps the
// suites honest about rotation: if the token changes shape, this follows it.
// The token is also what the UI puts in the publish URL, so this is the address
// a real operator would be given.
func PublishKey() {
	code, out := Do(http.MethodGet, "/sources", nil)
	if code != http.StatusOK {
		Die(fmt.Sprintf("cannot read sources: %d %s", code, out))
	}
	var rows []struct {
		Source struct {
			Token string `json:"token"`
		} `json:"source"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		Die("sources unreadable: " + err.Error())
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
	Die("no source carried a publish token")
}

// Sel builds a profile's track rows with exactly the named tracks enabled and
// every other row present but off.
//
// Every row is emitted rather than only the enabled ones because a profile is a
// full declaration of the width it was authored against, and a short list would
// leave the server guessing what the unnamed tracks should do.
func Sel(on ...int) []map[string]any {
	want := map[int]bool{}
	for _, t := range on {
		want[t] = true
	}
	rows := make([]map[string]any, 0, ProfileTracks)
	for i := 0; i < ProfileTracks; i++ {
		rows = append(rows, map[string]any{"track": i, "enabled": want[i], "gain": 1.0})
	}
	return rows
}

// CreateDest posts one destination and prints DEST_OK.
//
// The BODY is the caller's, because that is the part that genuinely differs:
// the failover suite creates a file destination it can measure a recording on,
// the multistream suite creates RTMP destinations carrying a platform and a
// credential. What is shared is only the POST, the two acceptable status codes
// and the failure report -- which is exactly the part that was duplicated.
//
// THE SERVER'S BODY IS PRINTED ON FAILURE and that is safe deliberately: the
// API scrubs its own credentials before it renders anything, so echoing a
// validation failure is how it becomes readable without disclosing the key the
// caller just posted. Nothing here reads the request body back.
func CreateDest(name string, body map[string]any) {
	code, out := Do(http.MethodPost, "/destinations", body)
	if code != http.StatusOK && code != http.StatusCreated {
		Die(fmt.Sprintf("create destination %s failed: %d %s", name, code, out))
	}
	fmt.Println("DEST_OK")
}

// StopAll stops every destination so its far end finalises.
//
// THE LIST BODY IS [{"destination": {...}, "routing": {...}}], NOT a bare array
// of destinations. Decoding it as the latter was silently reading id 0 off every
// row, POSTing /destinations/0/stop, taking the 404 without looking and printing
// STOPPED -- so no destination was ever stopped and the recording the failover
// suite's timeline checks read was always an unfinalised Matroska. The checks
// were then written around that damage rather than against it: the duration one
// reads the last decode timestamp because "format=duration" came back N/A,
// which is what an unfinalised file reports.
//
// The id-0 guard below is the recurrence check for exactly that. A row that
// decodes to no id means the list shape moved again, and saying so loudly is
// the whole lesson of the original fault -- which was not that the shape was
// wrong, but that being wrong about it looked identical to success.
func StopAll() {
	_, out := Do(http.MethodGet, "/destinations", nil)
	var rows []struct {
		Destination struct {
			ID int64 `json:"id"`
		} `json:"destination"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		Die("destinations unreadable: " + err.Error())
	}
	for _, r := range rows {
		if r.Destination.ID == 0 {
			Die("a destination came back with no id; the list shape has changed")
		}
		if code, body := Do(http.MethodPost,
			fmt.Sprintf("/destinations/%d/stop", r.Destination.ID), nil); code != http.StatusOK {
			Die(fmt.Sprintf("stop %d failed: %d %s", r.Destination.ID, code, body))
		}
	}
	fmt.Println("STOPPED")
}

// Die reports a driver-level failure and exits non-zero.
//
// The "driver: " prefix is load-bearing. The suites match on it -- see the
// output guard in acceptance-multistream.sh -- so that a driver that fell over
// is reported as a broken harness rather than counted as a product failure.
func Die(msg string) {
	fmt.Fprintln(os.Stderr, "driver: "+msg)
	os.Exit(1)
}
