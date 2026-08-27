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
	mode := ""
	if len(os.Args) > 2 {
		mode = os.Args[2]
	}

	if mode == "waitlive" {
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

	// `add <key>` MODE: bolt one more programme onto an install that is already
	// seeded, and print its publish token so the caller can start a stream for
	// it. This is what lets the dashboard be photographed at one, then two,
	// then three programmes WITHOUT tearing the install down between shots --
	// so the three images differ only in the thing being demonstrated, rather
	// than in every timestamp, port and generated id on the page.
	if mode == "add" {
		if len(os.Args) < 4 {
			die("add needs a programme key; one of the keys in extraProgrammes")
		}
		key := os.Args[3]
		p, ok := extraProgrammes[key]
		if !ok {
			die("no demo programme keyed %q", key)
		}
		id := seedProgramme(p)
		// The token on stdout and nothing else, because the shell captures it.
		fmt.Println(tokenForSource(id))
		fmt.Fprintf(os.Stderr, "seeded %q with %d destination(s)\n",
			p.name, len(p.destinations))
		return
	}

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
	// FAILOVER ON, because this seeds a WELL-CONFIGURED install and that is the
	// install the screenshots are of.
	//
	// It is off by default in the product and should stay that way -- the tier
	// costs a remux hop and not every box needs it (#512). But the dashboard
	// now says so where the destinations are listed, so a demo left at the
	// default puts a warning box across the middle of the hero image: a reader
	// meets "if the encoder disconnects, this broadcast ends" as the first
	// thing the product has to say about itself, which describes the demo's
	// configuration rather than the product.
	//
	// Turning it on is not hiding the notice. The notice is tested in
	// lib/failoverNotice.test.ts, where its behaviour belongs; a screenshot is
	// not the place to prove a warning renders, and an install that has taken
	// the advice is the honest thing to photograph.
	if fo, ok := settings["failover"].(map[string]any); ok {
		fo["enabled"] = true
	} else {
		settings["failover"] = map[string]any{"enabled": true}
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
	// `solo` STOPS HERE, at one programme.
	//
	// Not a convenience: it is the only way to photograph the single-programme
	// dashboard, which is a genuinely different page -- no lanes, no per-card
	// programme badge, one full-width preview instead of a captured grid. The
	// alternative, seeding all three and deleting two, leaves their
	// destinations behind as orphans, and Dashboard.tsx draws those in a
	// flagged group by design. The shot would then show a one-programme
	// install carrying a paragraph about destinations whose programme is
	// missing, which is a fault state, not the shape being demonstrated.
	var secondID int64
	if mode != "solo" {
		secondID = seedProgramme(secondProgramme)
	}

	// THE AUTOMATION PAGE, which photographs as three empty panels otherwise.
	//
	// Last, and after both programmes: a schedule names the destinations it
	// acts on, so seeding it before they exist would store an empty target list
	// -- which does not fail, it means "every destination", the opposite of the
	// narrow targeting the shot is there to show.
	//
	// AND NOT AT ALL UNDER `solo`, for that same reason read the other way.
	// demoSchedules target "Archive — hosts only", which belongs to the second
	// programme, so on a one-programme install the target list would resolve
	// empty. missingAll() catches it and refuses -- correctly, and it caught
	// this the first time capture-lanes.sh ran -- but the honest fix is not to
	// ask: a one-programme install genuinely has no such destination, and the
	// dashboard shots `solo` exists for do not show the automation page.
	if mode != "solo" {
		seedAutomation()
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
	// Empty third line under `solo`: capture-media.sh already treats a missing
	// second token as "one live programme", so the contract holds.
	if secondID == 0 {
		fmt.Println()
	} else {
		fmt.Println(tokenForSource(secondID))
	}
	programmes := 1
	extra := 0
	if secondID != 0 {
		programmes = 2
		extra = len(secondProgramme.destinations)
	}
	// COUNTS FOR WHAT ACTUALLY RAN. These are constants, so printing them
	// unconditionally told a `solo` run it had seeded "5 alert rules, 3 hooks,
	// 4 schedules" immediately after the branch above had skipped every one --
	// a summary line asserting the opposite of what happened, which is the
	// failure this repo names as an absence reading like an answer.
	automation := "automation skipped (solo)"
	if mode != "solo" {
		automation = fmt.Sprintf("%d alert rules, %d hooks, %d schedules",
			len(demoAlertRules), len(demoHooks), len(demoSchedules))
	}
	fmt.Fprintf(os.Stderr, "seeded: %d + %d destinations over %d programme(s), "+
		"%s, relay on udp/%d\n",
		len(demoDestinations), extra, programmes, automation, relay)
}

// The arrangement itself. Three destinations over three tracks, chosen so no
// two share a selection and every track is used by something — which is what
// makes the mix matrix in a screenshot read as a matrix rather than a list.
var demoDestinations = []demoDest{
	{"YouTube — full mix", "youtube.mkv", []int{0, 1, 2}},
	{"Twitch — no music", "twitch.mkv", []int{0, 2}},
	{"Podcast — mic only", "podcast.mkv", []int{0}},
}

// THE PROGRAMMES AFTER THE FIRST. Each a different shape on purpose: a
// different destination count and a track selection its neighbours do not use,
// so the lanes read as separate shows rather than as one list cut into pieces.
//
// SHAPE IS THE SUBJECT of the dashboard shots these feed. Dashboard.tsx draws
// nothing per-programme with one source, and everything per-programme with two
// or more -- so three lanes of identical size would photograph the feature
// while arguing that every programme must look alike, which is the opposite of
// what per-source scoping is for.
type demoProgramme struct {
	name         string
	destinations []demoDest
}

var secondProgramme = demoProgramme{
	name: "Studio B — panel show",
	destinations: []demoDest{
		{"YouTube — panel", "panel-youtube.mkv", []int{0, 1}},
		{"Archive — hosts only", "panel-archive.mkv", []int{1}},
	},
}

// THE THIRD, one destination rather than two or three. Its stream carries two
// audio tracks (see capture-lanes.sh), so a selection naming track 2 would be
// a mix of a track that does not arrive -- which renders as a destination
// running with a silent channel and photographs as a fault.
var thirdProgramme = demoProgramme{
	name: "Studio C — outside broadcast",
	destinations: []demoDest{
		{"Facebook — match feed", "obc-facebook.mkv", []int{0, 1}},
	},
}

// The programmes `add` can seed, by the short key the shell passes.
var extraProgrammes = map[string]demoProgramme{
	"b": secondProgramme,
	"c": thirdProgramme,
}

// seedProgramme creates one programme and its destinations, and returns its id.
// Idempotent on the name, because capturing is something you do repeatedly.
func seedProgramme(p demoProgramme) int64 {
	if !hasSourceNamed(p.name) {
		post("/sources", map[string]any{"name": p.name})
	}
	id := sourceIDNamed(p.name)
	if id == 0 {
		die("could not create the programme %q, so a shot meant to show its "+
			"lane would photograph a dashboard one programme short", p.name)
	}
	for _, d := range p.destinations {
		post("/destinations", d.body(id))
	}
	return id
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

// -------------------------------------------------------------- automation

// The three panels of /automation, seeded together because they are one shot.
//
// VARIETY IS THE SUBJECT, not decoration. 13-automation.png is a single frame
// of three lists, and five rows that differ only in their name argue that the
// feature has one knob. So no two alert rules here share a format, a severity
// floor and a subscription; the hooks subscribe to different halves of the
// lifecycle; the schedules span two recurrence kinds and three actions. One row
// in each panel is switched OFF, because disabled is a state the page draws
// differently and a screenshot of nothing but enabled rows never shows it.
//
// EVERY ENDPOINT IS example.com AND HAS TO BE. A seeder carrying a
// plausible-looking discord.com/api/webhooks/... is a string somebody pastes
// into their own install, and the very first thing this software does with an
// alert rule is POST to it -- a "demo" that fires real traffic at a stranger's
// channel. An obviously fake host is the only honest choice, and it is also
// what keeps the screenshot from advertising a URL that will 404 by the time
// anybody reads it.
func seedAutomation() {
	// Nothing below is refused for being a duplicate. POST /alerts/rules and
	// POST /hooks both accept a name that already exists and mint a second row,
	// so a second capture run -- which is the normal way you work, adjusting a
	// shot and re-running -- would photograph ten alert rules, five of them the
	// same five. Skipping by name is what makes re-seeding a no-op.
	//
	// By NAME rather than by id: send() does not hand back a response body, and
	// the id exists only inside the create it discards.
	for _, r := range demoAlertRules {
		if hasNamed("/alerts/rules", r.name) {
			continue
		}
		post("/alerts/rules", r.body())
	}
	for _, h := range demoHooks {
		if hasNamed("/hooks", h.name) {
			continue
		}
		post("/hooks", h.body())
	}
	for _, sc := range demoSchedules {
		if hasNamed("/schedules", sc.name) {
			continue
		}
		post("/schedules", sc.body(destinationIDsNamed(sc.targets)))
	}

	// READ BACK, because send() only PRINTS a refusal and carries on -- which it
	// has to, since a re-run re-POSTs everything the install already has. The
	// cost is that a whole panel can fail and the run still exits 0 with a
	// summary counting what it MEANT to create. All four schedules were once
	// rejected for a time zone the image could not resolve, and the only trace
	// was four lines of stderr scrolling past a capture that then photographed
	// the empty panel it was written to fill.
	//
	// Named individually rather than counted: a count matches when a stale
	// install carries four schedules that are not these four.
	var ruleNames, hookNames, schedNames []string
	for _, r := range demoAlertRules {
		ruleNames = append(ruleNames, r.name)
	}
	for _, h := range demoHooks {
		hookNames = append(hookNames, h.name)
	}
	for _, sc := range demoSchedules {
		schedNames = append(schedNames, sc.name)
	}
	missingAll("/alerts/rules", ruleNames)
	missingAll("/hooks", hookNames)
	missingAll("/schedules", schedNames)
}

// missingAll dies naming whatever did not land. Dying rather than warning: the
// caller's next step is a screenshot, and a shot of a panel that is empty for a
// reason nobody read is worse than no shot at all.
func missingAll(path string, want []string) {
	var missing []string
	for _, name := range want {
		if !hasNamed(path, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		die("%s is missing %d of %d seeded rows (%s) -- the create was refused "+
			"and the panel above it would photograph empty; the refusal is in "+
			"the POST lines above", path, len(missing), len(want),
			strings.Join(missing, ", "))
	}
}

type demoRule struct {
	name     string
	url      string
	format   string
	severity string
	events   []string
	debounce int
	interval int
	disabled bool
}

func (r demoRule) body() map[string]any {
	// events is sent even when empty, and the empty case means EVERY type --
	// which is a real subscription an operator chooses, not an omission. Sent
	// explicitly so the row that means "everything" says so rather than
	// arriving as a missing field the server fills in.
	ev := r.events
	if ev == nil {
		ev = []string{}
	}
	return map[string]any{
		"name": r.name, "url": r.url, "format": r.format,
		"minSeverity": r.severity, "events": ev,
		"debounceSeconds": r.debounce, "minIntervalSeconds": r.interval,
		"enabled": !r.disabled,
	}
}

// The alert set. Read down the format, severity and events columns rather than
// down the names: that is the axis the shot exists to show.
var demoAlertRules = []demoRule{
	// The one an operator makes first: a channel, everything that matters to a
	// human, nothing quieter than a warning.
	{
		name: "Ops — #broadcast-alerts", url: "https://example.com/webhooks/slack/broadcast-alerts",
		format: "slack", severity: "warning",
		events: []string{"destination.down", "destination.recovered",
			"destination.falling_behind", "ingest.lost", "ingest.recovered"},
		debounce: 30, interval: 60,
	},
	// The pager. An EMPTY events list on purpose -- it subscribes to every
	// type and lets the severity floor do the filtering, which is the other way
	// round from the rule above and the reason both are here.
	{
		name: "On-call pager — critical only", url: "https://example.com/webhooks/discord/oncall",
		format: "discord", severity: "critical",
		debounce: 15, interval: 30,
	},
	// The one nobody would guess the product had. Long debounce because a mix
	// that drifts out of compliance drifts back, and a channel that says so
	// every ten seconds is a channel that gets muted before the first real one.
	{
		name: "Audio desk — clipping and loudness", url: "https://example.com/hooks/audio-desk",
		format: "json", severity: "info",
		events: []string{"audio.clipping", "loudness.out_of_compliance",
			"loudness.recovered"},
		debounce: 120, interval: 300,
	},
	// The security set, which is a different argument from the streaming ones
	// and the reason it gets its own row: an install has exactly one account,
	// and this is the only signal that somebody else is holding the password.
	{
		name: "Security log — sign-ins and tokens", url: "https://example.com/collector/polyemesis/security",
		format: "json", severity: "info",
		events: []string{"auth.login.failed", "auth.login.succeeded",
			"auth.password.changed", "auth.token.created", "auth.token.revoked",
			"settings.changed"},
		debounce: 5, interval: 15,
	},
	// OFF, and the panel has to contain one. A rule an operator has muted for
	// the season is the commonest thing in a real install and the page renders
	// it differently; a shot of five live rows claims the switch does nothing.
	{
		name: "Recording volume — disk watch", url: "https://example.com/webhooks/slack/facilities",
		format: "slack", severity: "warning",
		events:   []string{"disk.low", "disk.recovered"},
		debounce: 60, interval: 600, disabled: true,
	},
}

type demoHook struct {
	name     string
	url      string
	triggers []string
	timeout  int
	attempts int
	disabled bool
}

func (h demoHook) body() map[string]any {
	tr := h.triggers
	if tr == nil {
		tr = []string{}
	}
	// No "secret": the server mints one with crypto/rand and returns the
	// plaintext exactly once. Supplying a literal here would put a signing key
	// in a public repository AND make every install that ran the seeder share
	// it, which is the failure mode of a shipped default key.
	return map[string]any{
		"name": h.name, "url": h.url, "triggers": tr,
		"timeoutSeconds": h.timeout, "maxAttempts": h.attempts,
		"enabled": !h.disabled,
	}
}

// The hook set. Split by what a SCRIPT would do with it -- ingest edges,
// destination edges, faults -- because that is the distinction between this
// panel and the alert panel above, and a screenshot where both lists say
// "destination.down" in the same words argues they are the same feature.
var demoHooks = []demoHook{
	{
		name: "Show automation — go-live mirror", url: "https://example.com/hooks/showrunner/live",
		triggers: []string{"ingest.published", "ingest.disconnected"},
		timeout:  10, attempts: 3,
	},
	{
		name: "Status page — destination edges", url: "https://example.com/hooks/statuspage/destinations",
		triggers: []string{"destination.up", "destination.down"},
		timeout:  5, attempts: 5,
	},
	// OFF, for the same reason the disk rule is.
	{
		name: "Incident bot — faults and rollovers", url: "https://example.com/hooks/incident-bot",
		triggers: []string{"broadcast.fault", "destination.rolledover"},
		timeout:  15, attempts: 2, disabled: true,
	},
}

type demoSchedule struct {
	name     string
	action   string
	kind     string
	tz       string
	at       int      // minutes past local midnight
	days     []int    // weekdays, Sunday = 0
	targets  []string // destination names; empty means every destination
	grace    int
	disabled bool
}

func (s demoSchedule) body(destIDs []int64) map[string]any {
	days := s.days
	if days == nil {
		days = []int{}
	}
	if destIDs == nil {
		destIDs = []int64{}
	}
	return map[string]any{
		"name": s.name, "enabled": !s.disabled,
		"action": s.action, "kind": s.kind,
		"tz": s.tz, "atMinutes": s.at, "days": days,
		"destinationIds": destIDs, "graceSeconds": s.grace,
	}
}

// The timetable. Two kinds and three actions across four rows, so the panel
// reads as a scheduler rather than as a list of alarms.
//
// AN ENABLED SCHEDULE ON A DEMO INSTALL REALLY FIRES, which is the one way this
// seeder can wreck the shots it exists to take: a `stop` that comes round
// mid-capture disables destinations and the dashboard photographs as an outage.
// So the only enabled stop here names ONE destination, and the rest either
// enable things or feed the failover playlist, which ranks below a live ingest
// and cannot pre-empt it. The times are evening on purpose too -- inside the
// grace window is the only moment any of this matters.
//
// EVERY ROW SAYS "UTC" AND IT IS NOT A STYLE CHOICE. Schedule.Location() resolves
// an IANA name through time.LoadLocation, which reads the operating system's
// zoneinfo database, and the capture image has none -- nothing in this repo
// imports time/tzdata, so the zone table is whatever the base image shipped.
// "America/New_York" here made all four POSTs 400 with `unknown time zone` and
// the automation page kept its empty third panel, which is the exact failure
// the whole file exists to prevent, arriving through a route nobody would look
// down. UTC is the one zone the standard library carries in the binary.
var demoSchedules = []demoSchedule{
	{
		name: "Weeknight show — on air 19:00", action: "start", kind: "weekly",
		tz: "UTC", at: 19 * 60,
		days:  []int{1, 2, 3, 4, 5},
		grace: 300,
	},
	// The narrow one. Naming its destination is the whole point of the column:
	// an empty target list means EVERY destination, so a schedule that looks
	// specific because its name says "archive" and is not would stop the show.
	{
		name: "Archive — stop recording 21:30", action: "stop", kind: "weekly",
		tz: "UTC", at: 21*60 + 30,
		days:    []int{1, 2, 3, 4, 5},
		targets: []string{"Archive — hosts only"},
		grace:   300,
	},
	{
		name: "Overnight filler — playlist from 02:00", action: "playlist.start", kind: "daily",
		tz: "UTC", at: 2 * 60,
		grace: 900,
	},
	// OFF, third panel, same reason as the other two.
	{
		name: "Saturday rehearsal — on air 11:00", action: "start", kind: "weekly",
		tz: "UTC", at: 11 * 60,
		days:  []int{6},
		grace: 1800, disabled: true,
	},
}

// hasNamed reports whether path already lists a row called name. Every
// automation collection answers a flat array whose rows carry "name" at the top
// level, schedules included -- their view type embeds the schedule rather than
// nesting it.
func hasNamed(path, name string) bool {
	for _, row := range getList(path) {
		if n, _ := row["name"].(string); n == name {
			return true
		}
	}
	return false
}

// destinationIDsNamed resolves destination names to ids, skipping any it cannot
// find. GET /destinations wraps each row as {"destination":…,"routing":…} and is
// install-wide -- it carries every programme's destinations and ignores a
// ?source= filter -- which is what lets one schedule name a destination
// belonging to either show.
//
// A name that resolves to nothing is DROPPED rather than defaulted, and the
// difference matters: the empty target list means "every destination", so a
// renamed destination would silently widen a schedule that says "archive" into
// one that stops the broadcast. Dropping it leaves the schedule aimed at
// whatever it could still find, and an all-misses list is refused below.
func destinationIDsNamed(names []string) []int64 {
	if len(names) == 0 {
		return nil
	}
	byName := map[string]int64{}
	for _, row := range getList("/destinations") {
		d, ok := row["destination"].(map[string]any)
		if !ok {
			continue
		}
		n, _ := d["name"].(string)
		if id, ok := d["id"].(float64); ok && n != "" {
			byName[n] = int64(id)
		}
	}
	out := make([]int64, 0, len(names))
	for _, n := range names {
		if id, ok := byName[n]; ok {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		die("no destination named %v, and a schedule with an empty target list "+
			"acts on EVERY destination -- refusing rather than seeding a stop "+
			"that would take the whole demo off air", names)
	}
	return out
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

func listSources() []map[string]any { return getList("/sources") }

// getList is get() for the routes that answer a JSON ARRAY. get() decodes into
// a map and hands back an empty one for every one of them, which reads as "the
// install has none" -- the answer that makes a seeder create a second copy of
// everything it already made.
func getList(path string) []map[string]any {
	r, err := client.Get(base + path)
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
