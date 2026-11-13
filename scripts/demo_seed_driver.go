//go:build ignore

// Applies scripts/demo-seed.fixture.json to a running polyemesis, for
// scripts/demo-seed.sh.
//
// WHY THIS IS NOT scripts/seed_demo.go. That one seeds the smallest
// arrangement that demonstrates one claim — a three-track source and three
// destinations whose mixes differ — and scripts/capture-media.sh depends on its
// exact two lines of stdout. This one seeds an INSTALLATION: several
// programmes, a rendition ladder, destinations across every platform the
// product supports, and a recording library. Widening the old seeder would have
// changed what the existing capture script photographs, silently, on a run
// nobody was watching.
//
// WHY GO RATHER THAN THE SHELL. Session cookie, CSRF header, JSON out of every
// response. curl plus jq can do it, and every acceptance suite in this tree
// reached the same conclusion the other way: net/http/cookiejar and
// encoding/json are already a dependency, and a shell pipeline that loses a
// cookie fails as a 403 several steps from the cause.
//
//	go run scripts/demo_seed_driver.go <base> setup      <fixture>
//	go run scripts/demo_seed_driver.go <base> wait       <fixture>
//	go run scripts/demo_seed_driver.go <base> finish     <fixture>
//	go run scripts/demo_seed_driver.go <base> recordings <fixture>
//	go run scripts/demo_seed_driver.go <base> census     <fixture>
//
// The sports scene adds (see scripts/lib-demo-sports.sh); <base> is "-" for the
// one that needs no server:
//
//	go run scripts/demo_seed_driver.go -      guardfixture <fixture> <host>
//	go run scripts/demo_seed_driver.go <base> guard        <fixture> <host>
//	go run scripts/demo_seed_driver.go <base> routing      <fixture> <initial|final>
//	go run scripts/demo_seed_driver.go <base> start        <fixture> <host>
//	go run scripts/demo_seed_driver.go <base> stop         <fixture>
//	go run scripts/demo_seed_driver.go <base> status       <fixture>
//	go run scripts/demo_seed_driver.go <base> waitlive     <fixture> [seconds]
//	go run scripts/demo_seed_driver.go <base> secrets      <fixture>   (stdout must be a file)
//
// Stdout is machine-readable, one record per line, tab-separated. Everything a
// human reads goes to stderr, so the caller can consume the first without
// filtering the second.
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

	"github.com/rainmanjam/polyemesis/internal/demorig"
	"github.com/rainmanjam/polyemesis/internal/recording/segname"
)

// Shared with Playwright's auth.setup.ts, which signs into the install this
// creates. REQUIRED rather than defaulted for the reason scripts/seed_demo.go
// gives: a literal here is a password in a public repository, however
// short-lived the account it protects.
//
// Read in authenticate, not at package level: the offline commands (recordings,
// guardfixture) must run with no credential in the environment at all.
var password string

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		fmt.Fprintf(os.Stderr, "%s is not set; scripts/demo-seed.sh generates one per run\n", key)
		os.Exit(2)
	}
	return v
}

// ------------------------------------------------------------------- fixture

type fixture struct {
	Settings map[string]any `json:"settings"`
	Sources  []fixSource    `json:"sources"`
	// Recordings describe files this driver does NOT write. The shell renders
	// them with FFmpeg into the recordings directory and the server's own
	// scanner indexes them, which is the only way the durations, sizes and
	// track counts on the library page are measurements rather than assertions.
	Recordings []fixRecording `json:"recordings"`
}

type fixSource struct {
	Name  string `json:"name"`
	Video struct {
		Lavfi string `json:"lavfi"`
		Kbps  int    `json:"kbps"`
	} `json:"video"`
	Audio        []fixTrack       `json:"audio"`
	Renditions   []map[string]any `json:"renditions"`
	Destinations []fixDest        `json:"destinations"`
}

type fixTrack struct {
	Label    string `json:"label"`
	Role     string `json:"role"`
	Language string `json:"language"`
	Lavfi    string `json:"lavfi"`
}

// fixDest is the destination body plus `rendition`, which names a tier by NAME
// rather than by id because ids do not exist until this run creates them.
type fixDest struct {
	Name      string `json:"name"`
	Rendition string `json:"rendition"`
	Rest      map[string]any
}

func (d *fixDest) UnmarshalJSON(b []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	d.Name, _ = raw["name"].(string)
	d.Rendition, _ = raw["rendition"].(string)
	// Deleted rather than left in place: the API decodes destinations with
	// DisallowUnknownFields, so one stray key is a 400 for the whole row.
	delete(raw, "rendition")
	d.Rest = raw
	return nil
}

type fixRecording struct {
	StartedAgoMinutes int `json:"startedAgoMinutes"`
	Minutes           int `json:"minutes"`
}

// ---------------------------------------------------------------------- main

var (
	base   string
	client *http.Client
	csrf   string
)

func main() {
	if len(os.Args) < 4 {
		die("usage: demo_seed_driver.go <base> <setup|wait|finish|recordings|census|guardfixture|guard|routing|start|stop|status|waitlive|secrets> <fixture> [args...]")
	}
	base = strings.TrimSuffix(os.Args[1], "/") + "/api/v1"
	cmd := os.Args[2]
	fx := readFixture(os.Args[3])

	// `recordings` answers from the fixture alone, so it runs before anything
	// needs a server. The shell calls it while the container is still starting.
	if cmd == "recordings" {
		printRecordingPlan(fx)
		return
	}
	// Same for the allow-list check on the fixture itself: it runs before any
	// container exists, which is the point of it.
	if cmd == "guardfixture" {
		guardFixture(fx, argOrDie(4, "guardfixture <fixture> <host>"))
		return
	}

	jar, _ := cookiejar.New(nil)
	client = &http.Client{Jar: jar, Timeout: 30 * time.Second}
	waitUp()
	authenticate()

	switch cmd {
	case "setup":
		setup(fx)
	case "wait":
		waitPublishing(fx)
	case "finish":
		finish(fx)
	case "census":
		census(fx)
	case "guard":
		guardLive(argOrDie(4, "guard <fixture> <host>"))
	case "routing":
		routeAll(fx, argOrDie(4, "routing <fixture> <initial|final>"))
	case "start":
		startAll(argOrDie(4, "start <fixture> <host>"))
	case "stop":
		stopAll()
	case "status":
		statusAll()
	case "waitlive":
		secs := 120
		if len(os.Args) > 4 {
			n, err := strconv.Atoi(os.Args[4])
			if err != nil || n <= 0 {
				die("waitlive: %q is not a positive number of seconds", os.Args[4])
			}
			secs = n
		}
		waitLive(fx, time.Duration(secs)*time.Second)
	case "secrets":
		printSecrets(fx)
	default:
		die("unknown command %q", cmd)
	}
}

func readFixture(path string) *fixture {
	b, err := os.ReadFile(path)
	if err != nil {
		die("read fixture: %v", err)
	}
	var fx fixture
	if err := json.Unmarshal(b, &fx); err != nil {
		die("parse %s: %v", path, err)
	}
	if len(fx.Sources) == 0 {
		die("%s describes no sources", path)
	}
	return &fx
}

// ------------------------------------------------------------------- 1. setup

func setup(fx *fixture) {
	refuseIfOccupied(fx)
	applySettings(fx)

	// The first-run tour banner spans the full width of every page, so it is in
	// every one of the seventeen shots. Marked complete rather than dismissed
	// in the browser: dismissal is React state, and Playwright gives each test
	// a fresh page, so it would have to be clicked away seventeen times.
	send("POST", "/tour/complete", map[string]any{})

	existing := sourcesByName()
	for _, s := range fx.Sources {
		id, ok := existing[s.Name]
		if !ok {
			id = createSource(s)
			fmt.Fprintf(os.Stderr, "  created source %q (id %d)\n", s.Name, id)
		} else {
			fmt.Fprintf(os.Stderr, "  source %q already present (id %d)\n", s.Name, id)
		}
		// Re-read rather than trusting the create response: the token is what
		// the publisher dials with, and a plan line carrying an empty one
		// produces an SRT connect that is refused for a reason no log explains.
		row := getSource(id)
		token, _ := row["token"].(string)
		if token == "" {
			die("source %d has no publish token; nothing can address it", id)
		}
		lavfi := make([]string, 0, len(s.Audio))
		for _, a := range s.Audio {
			lavfi = append(lavfi, a.Lavfi)
		}
		fmt.Printf("%d\t%s\t%s\t%d\t%s\n",
			id, token, s.Video.Lavfi, s.Video.Kbps, strings.Join(lavfi, "|"))
	}
}

// refuseIfOccupied stops this from running against somebody's install.
//
// scripts/demo-seed.sh already answers the question structurally, by starting a
// server on a data directory it created itself. This is the second half of that
// answer, for the caller who points --base at a server they already had: the
// seed writes destinations with unroutable URLs and disabled platform rows, and
// merging that into a real installation is a bad afternoon nobody asked for.
//
// The test is "does this install hold anything this fixture did not put here",
// not "is it empty" -- because re-running the seed against its OWN install has
// to keep working, and that is what makes the whole thing idempotent.
func refuseIfOccupied(fx *fixture) {
	if os.Getenv("DEMO_SEED_ALLOW_EXISTING") == "1" {
		return
	}
	known := map[string]bool{}
	for _, s := range fx.Sources {
		known[s.Name] = true
		for _, d := range s.Destinations {
			known[d.Name] = true
		}
	}
	var strangers []string
	for name := range sourcesByName() {
		if !known[name] {
			strangers = append(strangers, name)
		}
	}
	// Destinations too, and not for symmetry: an install can hold a real
	// destination on a programme this fixture happens to name the same way,
	// and that row is the one that publishes somewhere real.
	for name := range destinationsByName() {
		if !known[name] {
			strangers = append(strangers, name)
		}
	}
	if len(strangers) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr,
		"REFUSING TO SEED: this install already holds %d programme(s) or destination(s)\n"+
			"the demo fixture does not describe (%s). Seeding would add destinations with\n"+
			"unroutable URLs beside real ones. Point --base at a throwaway install, or set\n"+
			"DEMO_SEED_ALLOW_EXISTING=1 if you are certain.\n",
		len(strangers), strings.Join(strangers, ", "))
	os.Exit(3)
}

// applySettings merges the fixture's settings over whatever the server already
// has, then writes the whole document back.
//
// A merge rather than a literal document: settings gains fields every release,
// and a seeder that PUT a hand-written blob would silently reset every one it
// had not heard of.
func applySettings(fx *fixture) {
	cur := get("/settings")
	mergeInto(cur, fx.Settings)
	put("/settings", cur)
}

func mergeInto(dst map[string]any, src map[string]any) {
	for k, v := range src {
		if sub, ok := v.(map[string]any); ok {
			if existing, ok := dst[k].(map[string]any); ok {
				mergeInto(existing, sub)
				continue
			}
		}
		dst[k] = v
	}
}

func createSource(s fixSource) int64 {
	// The ingest block is READ FROM SETTINGS and only its mode overridden, for
	// the reason auth.setup.ts states: spelling out a literal SRT block means
	// carrying a latency, a passphrase policy and whatever it gains next.
	ingest, _ := get("/settings")["ingest"].(map[string]any)
	if ingest == nil {
		ingest = map[string]any{}
	}
	ingest["mode"] = "srt"
	ingest["annotations"] = annotationsFor(s)

	code, body := send("POST", "/sources", map[string]any{
		"name": s.Name, "enabled": true, "ingest": ingest,
	})
	if code >= 300 {
		die("create source %q: HTTP %d (%d-byte body not shown)", s.Name, code, len(body))
	}
	var row struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(body, &row); err != nil || row.ID == 0 {
		die("create source %q returned no id (%d-byte body not shown)", s.Name, len(body))
	}
	return row.ID
}

func annotationsFor(s fixSource) []map[string]any {
	out := make([]map[string]any, 0, len(s.Audio))
	for i, a := range s.Audio {
		ann := map[string]any{"track": i}
		if a.Role != "" {
			ann["role"] = a.Role
		}
		if a.Label != "" {
			ann["label"] = a.Label
		}
		if a.Language != "" {
			ann["language"] = a.Language
		}
		out = append(out, ann)
	}
	return out
}

// -------------------------------------------------------------------- 2. wait

// waitPublishing blocks until every seeded programme has an encoder on the
// shared listener.
//
// sourceView.publishing is the honest signal and is READ PER SOURCE, which
// /api/v1/status cannot give: that route answers for the DEFAULT engine only,
// so on a three-programme install polling it would declare the whole thing live
// the moment one publisher connected — and the two still-dark programmes would
// be photographed reading Offline.
func waitPublishing(fx *fixture) {
	want := map[string]bool{}
	for _, s := range fx.Sources {
		want[s.Name] = true
	}
	deadline := time.Now().Add(150 * time.Second)
	for time.Now().Before(deadline) {
		live, dark := publishingSplit(want)
		if len(dark) == 0 && live == len(want) {
			fmt.Fprintf(os.Stderr, "  %d/%d programmes publishing\n", live, len(want))
			// Bytes on the relay AND a probed layout, for every seeded
			// programme. Publishing means an encoder connected; the routing
			// editor still has no tracks to draw until the probe lands, and a
			// screenshot taken between the two shows a live source with an
			// empty mixer.
			if unprobed := waitProbed(fx); len(unprobed) > 0 {
				die("these programmes published but their layout never probed: %s",
					strings.Join(unprobed, ", "))
			}
			return
		}
		time.Sleep(2 * time.Second)
	}
	_, dark := publishingSplit(want)
	die("these programmes never went live: %s", strings.Join(dark, ", "))
}

func publishingSplit(want map[string]bool) (int, []string) {
	var rows []map[string]any
	decode(getRaw("/sources"), &rows)
	live := 0
	var dark []string
	for _, r := range rows {
		name, _ := r["name"].(string)
		if !want[name] {
			continue
		}
		if pub, _ := r["publishing"].(bool); pub {
			live++
		} else {
			dark = append(dark, name)
		}
	}
	return live, dark
}

// waitProbed returns the names of the seeded programmes whose layout has not
// probed by the deadline; empty means every one has.
//
// EACH PROGRAMME BY ID. This read a bare GET /source, which answers for "the"
// programme -- and an install running several refuses that with 400
// source_required rather than guess. The fixture always seeds several, so the
// harness died here on every run, before a single destination existed and
// before anything was photographed. Asking per id is also the stronger wait:
// the old one only ever looked at the default programme, and the other lanes
// could still be drawn with an empty mixer.
func waitProbed(fx *fixture) []string {
	ids := sourcesByName()
	deadline := time.Now().Add(90 * time.Second)
	for {
		var unprobed []string
		for _, s := range fx.Sources {
			si := get(fmt.Sprintf("/source?source=%d", ids[s.Name]))
			tracks, _ := si["tracks"].([]any)
			if probed, _ := si["probed"].(bool); !probed || len(tracks) == 0 {
				unprobed = append(unprobed, s.Name)
			}
		}
		if len(unprobed) == 0 {
			fmt.Fprintf(os.Stderr, "  %d/%d programmes probed\n", len(fx.Sources), len(fx.Sources))
			return nil
		}
		if time.Now().After(deadline) {
			return unprobed
		}
		time.Sleep(2 * time.Second)
	}
}

// ------------------------------------------------------------------ 3. finish

// finish creates the renditions and destinations, and re-writes the track
// annotations.
//
// DELIBERATELY AFTER THE STREAM IS LIVE. Annotations are indexed against
// probed tracks, and a destination profile is validated against the layout that
// actually arrived — so both are written once there is a layout to write them
// against, rather than hopefully before there is one.
func finish(fx *fixture) {
	sources := sourcesByName()
	rends := renditionsByName()
	dests := destinationsByName()

	for _, s := range fx.Sources {
		sid, ok := sources[s.Name]
		if !ok {
			die("source %q vanished between setup and finish", s.Name)
		}
		reannotate(sid, s)

		for _, r := range s.Renditions {
			name, _ := r["name"].(string)
			if _, exists := rends[name]; exists {
				continue
			}
			body := cloneMap(r)
			body["sourceId"] = sid
			code, out := send("POST", "/renditions", body)
			if code >= 300 {
				die("create rendition %q: HTTP %d (%d-byte body not shown)", name, code, len(out))
			}
			var made struct {
				Rendition struct {
					ID int64 `json:"id"`
				} `json:"rendition"`
			}
			decode(out, &made)
			rends[name] = made.Rendition.ID
			fmt.Fprintf(os.Stderr, "  rendition %q (id %d)\n", name, made.Rendition.ID)
		}

		for _, d := range s.Destinations {
			if dests[d.Name] {
				continue
			}
			body := cloneMap(d.Rest)
			body["sourceId"] = sid
			if d.Rendition != "" {
				rid, ok := rends[d.Rendition]
				if !ok {
					die("destination %q wants rendition %q, which does not exist", d.Name, d.Rendition)
				}
				body["renditionId"] = rid
			}
			code, out := send("POST", "/destinations", body)
			if code >= 300 {
				die("create destination %q: HTTP %d (%d-byte body not shown)", d.Name, code, len(out))
			}
			dests[d.Name] = true
			fmt.Fprintf(os.Stderr, "  destination %q\n", d.Name)
		}
	}
}

// reannotate rewrites the source's track annotations now that the layout is
// probed. Idempotent: the same list, written twice, is the same row.
func reannotate(id int64, s fixSource) {
	row := getSource(id)
	ingest, _ := row["ingest"].(map[string]any)
	if ingest == nil {
		return
	}
	ingest["annotations"] = annotationsFor(s)
	code, out := send("PUT", fmt.Sprintf("/sources/%d", id), map[string]any{
		"name": s.Name, "enabled": true, "ingest": ingest,
	})
	if code >= 300 {
		die("annotate source %d: HTTP %d (%d-byte body not shown)", id, code, len(out))
	}
}

// ------------------------------------------------------------- 4. recordings

// printRecordingPlan turns "twenty-one and a half hours ago, ten minutes long"
// into the filename the recorder itself would have written.
//
// The name is not cosmetic: internal/recording parses rec-YYYYMMDD-HHMMSS out
// of it and prefers it to the file's mtime, because mtime moves while a segment
// is being written. A file named anything else is indexed at whatever moment
// FFmpeg finished rendering it, and five recordings that all start within the
// same minute do not group into sessions.
//
// It writes the untagged name an earlier release's recorder wrote, on purpose:
// a tagged rec-s<id>- name would attribute the demo footage to a programme ID
// this script cannot know. The format itself is package segname's.
func printRecordingPlan(fx *fixture) {
	now := time.Now()
	for _, r := range fx.Recordings {
		started := now.Add(-time.Duration(r.StartedAgoMinutes) * time.Minute)
		fmt.Printf("%s%s%s\t%d\n", segname.Prefix, segname.FormatStamp(started), segname.MasterExt, r.Minutes*60)
	}
}

// ---------------------------------------------------------------- 5. census

// census refuses to let the caller photograph an under-seeded install.
//
// Every count here was supposed to be non-zero, and each has its own way of
// silently being zero: a destination refused for a profile the probe did not
// support, a rendition rejected because the ladder's source was not named, a
// recordings directory the container could not write. The capture that follows
// would still pass — it would just photograph an emptier product than the one
// this script exists to build, which is the exact failure mode
// docs/media/README.md says the capture harness must not produce.
func census(fx *fixture) {
	var srcs []map[string]any
	decode(getRaw("/sources"), &srcs)
	var rends []map[string]any
	decode(getRaw("/renditions"), &rends)
	var dsts []map[string]any
	decode(getRaw("/destinations"), &dsts)
	var recs []map[string]any
	decode(getRaw("/recordings"), &recs)

	wantSrc, wantRend, wantDest := 0, 0, 0
	for _, s := range fx.Sources {
		wantSrc++
		wantRend += len(s.Renditions)
		wantDest += len(s.Destinations)
	}

	fmt.Printf("sources\t%d\t%d\n", len(srcs), wantSrc)
	fmt.Printf("renditions\t%d\t%d\n", len(rends), wantRend)
	fmt.Printf("destinations\t%d\t%d\n", len(dsts), wantDest)
	fmt.Printf("recordings\t%d\t%d\n", len(recs), len(fx.Recordings))
}

// --------------------------------------------------------- 6. the sports rig

// The commands below drive the sports scene (one programme, five destinations
// that publish only to a local receiver). They are small on purpose; the one
// part that must be right, the host allow-list, is package demorig and is
// unit-tested.

func argOrDie(i int, usage string) string {
	if len(os.Args) <= i || os.Args[i] == "" {
		die("usage: demo_seed_driver.go <base> %s", usage)
	}
	return os.Args[i]
}

// guardFixture refuses a fixture whose destinations point anywhere but host.
// Offline: runs before any container is started.
func guardFixture(fx *fixture, host string) {
	var rows []demorig.DestRow
	for _, s := range fx.Sources {
		for _, d := range s.Destinations {
			url, _ := d.Rest["url"].(string)
			backup, _ := d.Rest["backupUrl"].(string)
			rows = append(rows, demorig.DestRow{Name: d.Name, URL: url, BackupURL: backup})
		}
	}
	if len(rows) == 0 {
		die("the fixture has no destinations to check")
	}
	if err := demorig.CheckHosts(rows, []string{host}); err != nil {
		die("%v", err)
	}
	fmt.Fprintf(os.Stderr, "  fixture guard ok: %d destinations, every host is %s\n", len(rows), host)
}

// storedDest is one destination as the server holds it (the "destination" half
// of a list or get response).
type storedDest struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	URL             string `json:"url"`
	BackupURL       string `json:"backupUrl"`
	ExtraInputArgs  string `json:"extraInputArgs"`
	ExtraOutputArgs string `json:"extraOutputArgs"`
	AccountID       *int64 `json:"accountId"`
	Multitrack      bool   `json:"multitrack"`
}

// listStored reads GET /destinations, whose rows wrap each destination in
// {"destination": ...}. Order is the server's list order.
func listStored() []storedDest {
	var rows []struct {
		Destination storedDest `json:"destination"`
	}
	decode(getRaw("/destinations"), &rows)
	out := make([]storedDest, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Destination)
	}
	return out
}

// guardLive is the second safety layer: it checks what the SERVER stored, not
// what the fixture said, because those can differ (an edit in the console, a
// reused data directory). It reads the list ONCE and returns exactly the rows
// it checked, so a caller that starts them starts only what was guarded.
//
// Beyond the host it refuses what could reach a real platform without the URL:
// a platform account (its API is called at go-live), Enhanced Broadcasting
// negotiation, and raw FFmpeg arguments (which can name any output). The
// fixture sets none of them.
func guardLive(host string) []storedDest {
	stored := listStored()
	if len(stored) == 0 {
		die("the server holds no destinations to check")
	}
	rows := make([]demorig.DestRow, 0, len(stored))
	for _, d := range stored {
		rows = append(rows, demorig.DestRow{Name: d.Name, URL: d.URL, BackupURL: d.BackupURL})
	}
	if err := demorig.CheckHosts(rows, []string{host}); err != nil {
		die("%v", err)
	}
	for _, d := range stored {
		switch {
		case d.ExtraOutputArgs != "":
			die("destination %q has extraOutputArgs set; the rig refuses raw FFmpeg arguments", d.Name)
		case d.ExtraInputArgs != "":
			die("destination %q has extraInputArgs set; the rig refuses raw FFmpeg arguments", d.Name)
		case d.AccountID != nil:
			die("destination %q has accountId set; a platform account would be called at go-live", d.Name)
		case d.Multitrack:
			die("destination %q has multitrack on; Enhanced Broadcasting negotiates with a real platform", d.Name)
		}
	}
	fmt.Fprintf(os.Stderr, "  guard ok: %d stored destinations, every host is %s\n", len(rows), host)
	return stored
}

// routeAll sets every fixture destination's track selection. "initial": all
// tracks at gain 1.0. "final": for the destination at position i, the i-th
// commentary track plus every track that is not commentary (the crowd bed).
// Only profile.tracks changes; the rest of what GET returned is sent back as it was.
func routeAll(fx *fixture, state string) {
	if state != "initial" && state != "final" {
		die("routing: unknown state %q (want initial or final)", state)
	}
	ids := map[string]int64{}
	for _, d := range listStored() {
		ids[d.Name] = d.ID
	}
	for _, s := range fx.Sources {
		var commentary []int
		for i, a := range s.Audio {
			if a.Role == "commentary" {
				commentary = append(commentary, i)
			}
		}
		for pos, d := range s.Destinations {
			id, ok := ids[d.Name]
			if !ok {
				die("routing: destination %q is not on the server", d.Name)
			}
			on := map[int]bool{}
			for i := range s.Audio {
				on[i] = state == "initial"
			}
			if state == "final" {
				if pos >= len(commentary) {
					die("routing: destination %q is number %d but %q has only %d commentary tracks", d.Name, pos+1, s.Name, len(commentary))
				}
				for i, a := range s.Audio {
					on[i] = a.Role != "commentary" || i == commentary[pos]
				}
			}
			tracks := make([]map[string]any, 0, len(s.Audio))
			for i := range s.Audio {
				tracks = append(tracks, map[string]any{"track": i, "enabled": on[i], "gain": 1.0})
			}
			body, _ := get(fmt.Sprintf("/destinations/%d", id))["destination"].(map[string]any)
			if body == nil {
				die("routing: GET /destinations/%d returned no destination", id)
			}
			profile, _ := body["profile"].(map[string]any)
			if profile == nil {
				profile = map[string]any{}
				body["profile"] = profile
			}
			profile["tracks"] = tracks
			put(fmt.Sprintf("/destinations/%d", id), body)
			fmt.Fprintf(os.Stderr, "  %s: routing %s\n", d.Name, state)
		}
	}
}

// startAll starts every destination, one at a time, after re-checking what the
// server stored. Never the bulk route, which would skip the guard
// between rows.
//
// The rows come from guardLive's single read. The destinations are left
// enabled: a server restart would start them again WITHOUT this guard, and only
// the internal Docker network (no route off the machine) covers that case. The
// rig's --down removes everything.
func startAll(host string) {
	for _, d := range guardLive(host) {
		path := fmt.Sprintf("/destinations/%d", d.ID) + "/start"
		if code, out := send("POST", path, map[string]any{}); code >= 300 {
			die("start %q: HTTP %d (%d-byte body not shown)", d.Name, code, len(out))
		}
		fmt.Fprintf(os.Stderr, "  started %s\n", d.Name)
	}
}

// stopAll stops every destination. The confirmation is sent although the
// per-destination route does not ask for it: stop-all does, and a caller that
// always says it means it never trips either.
func stopAll() {
	for _, d := range listStored() {
		path := fmt.Sprintf("/destinations/%d", d.ID) + "/stop"
		if code, out := send("POST", path, map[string]any{"confirm": true}); code >= 300 {
			die("stop %q: HTTP %d (%d-byte body not shown)", d.Name, code, len(out))
		}
		fmt.Fprintf(os.Stderr, "  stopped %s\n", d.Name)
	}
}

// liveStatus is the part of GET /status the rig reads.
type liveStatus struct {
	Destinations []struct {
		ID      int64  `json:"id"`
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
		Process *struct {
			State    string `json:"state"`
			Progress struct {
				Frame int64   `json:"frame"`
				FPS   float64 `json:"fps"`
			} `json:"progress"`
		} `json:"process"`
	} `json:"destinations"`
	Loudness []struct {
		DestinationID int64   `json:"destinationId"`
		Mix           string  `json:"mix"`
		Seconds       float64 `json:"seconds"`
		Momentary     float64 `json:"momentaryLufs"`
		Verdict       string  `json:"verdict"`
	} `json:"loudness"`
}

// readStatus reads GET /status, which answers for the DEFAULT engine only. The
// sports scene has one programme, so that is the whole install; do not use it
// for the multi-programme fixture.
func readStatus() liveStatus {
	var st liveStatus
	decode(getRaw("/status"), &st)
	return st
}

// liveLoudness picks each destination's live-mix report (a report with no mix is the live one).
func (st liveStatus) liveLoudness(id int64) (seconds, momentary float64, verdict string, ok bool) {
	for _, l := range st.Loudness {
		if l.DestinationID == id && (l.Mix == "" || l.Mix == "live") {
			return l.Seconds, l.Momentary, l.Verdict, true
		}
	}
	return 0, 0, "", false
}

// statusAll prints one line per destination: name, enabled, state, fps,
// momentary LUFS and the loudness verdict.
func statusAll() {
	st := readStatus()
	for _, d := range st.Destinations {
		state, fps := "off", 0.0
		if d.Process != nil {
			state, fps = d.Process.State, d.Process.Progress.FPS
		}
		lufs, verdict := "-", "-"
		if sec, m, v, ok := st.liveLoudness(d.ID); ok && sec > 0 {
			lufs, verdict = strconv.FormatFloat(m, 'f', 1, 64), v
		}
		fmt.Printf("%s\tenabled=%t\t%s\tfps=%.1f\tmomentary=%s\tverdict=%s\n", d.Name, d.Enabled, state, fps, lufs, verdict)
	}
}

// waitLive blocks until every fixture destination is running, has output
// frames and has a measured momentary loudness; it dies naming the laggards.
// GET /status is the default engine's, which is every destination here.
func waitLive(fx *fixture, timeout time.Duration) {
	want := map[string]bool{}
	for _, s := range fx.Sources {
		for _, d := range s.Destinations {
			want[d.Name] = true
		}
	}
	deadline := time.Now().Add(timeout)
	for {
		st := readStatus()
		ready := map[string]bool{}
		for _, d := range st.Destinations {
			sec, _, _, measured := st.liveLoudness(d.ID)
			if d.Process != nil && d.Process.State == "running" && d.Process.Progress.Frame > 0 && measured && sec > 0 {
				ready[d.Name] = true
			}
		}
		var lag []string
		for _, s := range fx.Sources {
			for _, d := range s.Destinations {
				if !ready[d.Name] {
					lag = append(lag, d.Name)
				}
			}
		}
		if len(lag) == 0 {
			fmt.Fprintf(os.Stderr, "  %d/%d destinations live with measured loudness\n", len(want), len(want))
			return
		}
		if time.Now().After(deadline) {
			die("not live with measured loudness after %s: %s", timeout, strings.Join(lag, ", "))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// printSecrets writes "<kind>\t<value>" lines for the credentials the capture
// must keep out of every screenshot: each programme's publish token (read from
// the server) and each destination's stream key (from the fixture). It refuses
// a terminal, so the values can only go to a file the caller chose.
func printSecrets(fx *fixture) {
	// A regular file only: not a terminal, and not a pipe either, because
	// `| less` would show them.
	if fi, err := os.Stdout.Stat(); err != nil || fi.Mode()&os.ModeCharDevice != 0 || !fi.Mode().IsRegular() {
		die("secrets writes credentials; redirect stdout to a file (mode 600), not a terminal or a pipe")
	}
	ids := sourcesByName()
	for _, s := range fx.Sources {
		row := getSource(ids[s.Name])
		if token, _ := row["token"].(string); token != "" {
			fmt.Printf("token\t%s\n", token)
		}
		for _, d := range s.Destinations {
			if key, _ := d.Rest["streamKey"].(string); key != "" {
				fmt.Printf("streamkey\t%s\n", key)
			}
		}
	}
}

// ----------------------------------------------------------------- inventory

func sourcesByName() map[string]int64 {
	var rows []map[string]any
	decode(getRaw("/sources"), &rows)
	out := map[string]int64{}
	for _, r := range rows {
		name, _ := r["name"].(string)
		id, _ := r["id"].(float64)
		if name != "" {
			out[name] = int64(id)
		}
	}
	return out
}

func renditionsByName() map[string]int64 {
	var rows []map[string]any
	decode(getRaw("/renditions"), &rows)
	out := map[string]int64{}
	for _, r := range rows {
		name, _ := r["name"].(string)
		id, _ := r["id"].(float64)
		if name != "" {
			out[name] = int64(id)
		}
	}
	return out
}

// destinationsByName reads the LIST ENVELOPE, which wraps each destination
// rather than being one: parsing {id,name} at the top level yields zero-valued
// rows and an inventory that thinks nothing exists, so every re-run duplicates
// every destination.
func destinationsByName() map[string]bool {
	var rows []struct {
		Destination struct {
			Name string `json:"name"`
		} `json:"destination"`
	}
	decode(getRaw("/destinations"), &rows)
	out := map[string]bool{}
	for _, r := range rows {
		if r.Destination.Name != "" {
			out[r.Destination.Name] = true
		}
	}
	return out
}

func getSource(id int64) map[string]any {
	return get(fmt.Sprintf("/sources/%d", id))
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+2)
	for k, v := range in {
		out[k] = v
	}
	return out
}

// ------------------------------------------------------------------ plumbing

func waitUp() {
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if r, err := client.Get(base + "/health"); err == nil {
			r.Body.Close()
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	die("server never answered /health at %s", base)
}

// authenticate handles first run and returning user alike, because re-seeding a
// still-running demo has to work: adjusting a shot is something you do twice.
func authenticate() {
	password = mustEnv("E2E_PASSWORD")
	if setupNeeded() {
		if code, out := send("POST", "/setup", map[string]any{
			"username": "admin", "password": password,
			"setupCode": os.Getenv("POLYEMESIS_SETUP_CODE"),
		}); code >= 300 {
			die("first-run setup: HTTP %d (%d-byte body not shown)", code, len(out))
		}
	} else if code, out := send("POST", "/auth/login", map[string]any{
		"username": "admin", "password": password,
	}); code >= 300 {
		die("sign in: HTTP %d, %d-byte body not shown (a stale data directory from an earlier run has a "+
			"different password; remove it, or pass --reset)", code, len(out))
	}
	grabCSRF()
}

func setupNeeded() bool {
	r, err := client.Get(base + "/setup")
	if err != nil {
		die("GET /setup: %v", err)
	}
	defer r.Body.Close()
	var m map[string]any
	json.NewDecoder(r.Body).Decode(&m)
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

// lastRequest names the most recent request for failure messages that have
// nothing else to say about where a response came from.
var lastRequest string

func getRaw(path string) []byte {
	lastRequest = "GET " + path
	r, err := client.Get(base + path)
	if err != nil {
		die("GET %s: %v", path, err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode >= 300 {
		die("GET %s: HTTP %d (%d-byte body not shown)", path, r.StatusCode, len(b))
	}
	return b
}

func get(path string) map[string]any {
	var m map[string]any
	decode(getRaw(path), &m)
	return m
}

func decode(b []byte, into any) {
	if json.Unmarshal(b, into) != nil {
		// Neither the body nor the parser's message: either can quote content,
		// and a destination list begins with a stream key.
		die("decode response to %s: unexpected shape (%d bytes)", lastRequest, len(b))
	}
}

func put(path string, body any) {
	if code, out := send("PUT", path, body); code >= 300 {
		die("PUT %s: HTTP %d (%d-byte body not shown)", path, code, len(out))
	}
}

// send returns the status and body rather than dying on its own, because the
// callers differ: a duplicate name on a re-run is expected, a rejected profile
// is fatal, and only the caller knows which it asked for.
func send(method, path string, body any) (int, []byte) {
	lastRequest = method + " " + path
	b, err := json.Marshal(body)
	if err != nil {
		die("encode %s %s: %v", method, path, err)
	}
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
	out, _ := io.ReadAll(r.Body)
	return r.StatusCode, out
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "demo-seed: "+format+"\n", a...)
	os.Exit(1)
}
