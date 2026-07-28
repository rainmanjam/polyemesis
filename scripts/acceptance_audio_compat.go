//go:build ignore

// Backwards-compatibility half of scripts/acceptance-audio.sh.
//
// Fifteen features landed on the routing compiler at once. Every one of them
// was supposed to be inert until somebody opts in, and the cost of being wrong
// about that is not a failed test — it is that every existing user's audio
// changes on upgrade, silently, on a filter they never touched.
//
// So this compiles a set of profiles that use NONE of the new fields through
// the running server's own /routing/compile endpoint, against the very source
// the rest of the suite has been streaming, and demands the exact filter string
// those profiles produced before any of this existed. The endpoint is the same
// one the routing editor calls, and it compiles against the engine's live
// annotated source — so if roles, denoise, exclusions or anything else leaked
// into the default path, it is caught here rather than by a listener.
//
// Prints PASS/FAIL lines for the shell script to count.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"time"
)

var (
	client *http.Client
	base   string
	csrf   string
)

// golden is a profile that predates every feature in this workstream, paired
// with the filter string it has always compiled to.
type golden struct {
	name    string
	profile map[string]any
	want    string
}

func main() {
	if len(os.Args) < 2 {
		die("usage: acceptance_audio_compat.go <http-port>")
	}
	base = "http://127.0.0.1:" + os.Args[1] + "/api/v1"

	jar, _ := cookiejar.New(nil)
	client = &http.Client{Jar: jar, Timeout: 30 * time.Second}

	// The server is already set up by the main driver; this only needs a
	// session and the CSRF token that goes with it.
	login()

	cases := []golden{
		{
			// The single most common profile in existence: one track, straight
			// through. Nothing may touch it.
			name:    "one track, no normalisation",
			profile: simple([]int{0}, "off", 48000),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[a_t0]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			// Two tracks under auto, which is the default a destination is
			// created with. The limiter, not loudnorm: auto must not be
			// promoted to loudnorm just because a loudness target now exists
			// as a concept.
			name:    "two tracks, auto normalisation",
			profile: simple([]int{0, 1}, "auto", 48000),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];" +
				"[a_t0][a_t1]amix=inputs=2:duration=longest:normalize=0[a_mix];" +
				"[a_mix]alimiter=limit=0.95:level=disabled[a_norm];" +
				"[a_norm]aresample=48000:async=1:first_pts=0[aout]",
		},
		{
			// All three, with the non-unity gains that exercise the pan
			// coefficients the new per-track stages sit next to.
			name:    "three tracks with gains",
			profile: gains(map[int]float64{0: 0.5, 1: 1.0, 2: 1.25}, "auto", 44100),
			want: "[0:a:0]pan=stereo|c0=0.5*c0|c1=0.5*c1[a_t0];" +
				"[0:a:1]pan=stereo|c0=1*c0|c1=1*c1[a_t1];" +
				"[0:a:2]pan=stereo|c0=1.25*c0|c1=1.25*c1[a_t2];" +
				"[a_t0][a_t1][a_t2]amix=inputs=3:duration=longest:normalize=0[a_mix];" +
				"[a_mix]alimiter=limit=0.95:level=disabled[a_norm];" +
				"[a_norm]aresample=44100:async=1:first_pts=0[aout]",
		},
		{
			// The old fixed loudnorm. A destination that asked for loudnorm
			// without naming a target must still get the numbers it always got,
			// not the new default target.
			name:    "loudnorm with no target",
			profile: simple([]int{0}, "loudnorm", 48000),
			want: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1[a_t0];" +
				"[a_t0]loudnorm=I=-16:TP=-1.5:LRA=11[a_norm];" +
				"[a_norm]aresample=48000:async=1:first_pts=0[aout]",
		},
	}

	bad := false
	for _, c := range cases {
		got, err := compile(c.profile)
		if err != nil {
			fmt.Printf("FAIL %s: %v\n", c.name, err)
			bad = true
			continue
		}
		if got != c.want {
			fmt.Printf("FAIL %s: the compiled filter changed\n", c.name)
			fmt.Printf("  got:  %s\n", got)
			fmt.Printf("  want: %s\n", c.want)
			bad = true
			continue
		}
		fmt.Printf("PASS %s compiles byte-for-byte as before\n", c.name)
	}

	// A stored profile must also still round-trip without gaining keys: a new
	// field that marshals when unset rewrites every row in the database on the
	// next save.
	if err := checkNoNewKeys(); err != nil {
		fmt.Printf("FAIL %v\n", err)
		bad = true
	} else {
		fmt.Println("PASS an untouched profile gains no new JSON keys on the wire")
	}

	if bad {
		os.Exit(1)
	}
}

// checkNoNewKeys compiles a legacy profile and inspects the profile the server
// echoes back, which is what a save would store.
func checkNoNewKeys() error {
	body, err := post("/routing/compile", map[string]any{"profile": simple([]int{0}, "off", 48000)})
	if err != nil {
		return err
	}
	prof, ok := body["profile"].(map[string]any)
	if !ok {
		return fmt.Errorf("no profile echoed back")
	}
	for _, k := range []string{"loudness", "delayMs", "ducking", "excludeRoles"} {
		if _, present := prof[k]; present {
			return fmt.Errorf("an untouched profile now serializes %q", k)
		}
	}
	return nil
}

func compile(profile map[string]any) (string, error) {
	body, err := post("/routing/compile", map[string]any{"profile": profile})
	if err != nil {
		return "", err
	}
	r, ok := body["routing"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("no routing in response: %v", body)
	}
	s, _ := r["filterComplex"].(string)
	return s, nil
}

func simple(tracks []int, norm string, rate int) map[string]any {
	g := map[int]float64{}
	for _, t := range tracks {
		g[t] = 1.0
	}
	return gains(g, norm, rate)
}

func gains(g map[int]float64, norm string, rate int) map[string]any {
	rows := []map[string]any{}
	for i := 0; i < 6; i++ {
		gain, on := g[i]
		if !on {
			gain = 1.0
		}
		rows = append(rows, map[string]any{"track": i, "enabled": on, "gain": gain})
	}
	return map[string]any{
		"mode": "simple", "tracks": rows, "normalize": norm, "sampleRate": rate,
	}
}

func login() {
	body := map[string]any{"username": "admin", "password": "acceptance-pw"}
	b, _ := json.Marshal(body)
	resp, err := client.Post(base+"/auth/login", "application/json", bytes.NewReader(b))
	if err != nil {
		die("login: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		die("login -> %d: %s", resp.StatusCode, raw)
	}

	req, _ := http.NewRequest("GET", base+"/health", nil)
	if r, err := client.Get(base + "/health"); err == nil {
		r.Body.Close()
	}
	for _, c := range client.Jar.Cookies(req.URL) {
		if c.Name == "polyemesis_csrf" {
			csrf = c.Value
		}
	}
	if csrf == "" {
		die("no CSRF cookie issued")
	}
}

func post(path string, body any) (map[string]any, error) {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", base+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", csrf)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("POST %s -> %d: %s", path, resp.StatusCode, raw)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out, nil
}

func die(f string, a ...any) {
	fmt.Printf("FAIL "+f+"\n", a...)
	os.Exit(1)
}
