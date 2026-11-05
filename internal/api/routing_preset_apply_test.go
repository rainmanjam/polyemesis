package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// POST /routing/presets/{preset}/apply: the Stream Deck's Routing preset key
// and the MCP tool's apply, which until now compiled a preset and stored
// nothing. See routing_preset_apply.go.

func presetApplyPath(preset string, q url.Values) string {
	p := "/api/v1/routing/presets/" + url.PathEscape(preset) + "/apply"
	if len(q) > 0 {
		p += "?" + q.Encode()
	}
	return p
}

type presetApplyAnswer struct {
	Preset          string           `json:"preset"`
	Mix             string           `json:"mix"`
	DryRun          bool             `json:"dryRun"`
	Results         []presetApplyRow `json:"results"`
	Warnings        []string         `json:"warnings"`
	ReconcileFailed bool             `json:"reconcileFailed"`
	Error           string           `json:"error"`
}

func (a presetApplyAnswer) row(t *testing.T, id int64) presetApplyRow {
	t.Helper()
	for _, r := range a.Results {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("destination %d has no row in the answer: %+v", id, a.Results)
	return presetApplyRow{}
}

func applyPreset(t *testing.T, h http.Handler, sign func(*http.Request), preset string, q url.Values, body any, want int) presetApplyAnswer {
	t.Helper()
	raw := send(t, h, sign, http.MethodPost, presetApplyPath(preset, q), body, want)
	var out presetApplyAnswer
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("the apply answered non-JSON: %v: %s", err, raw)
	}
	return out
}

// loudnessPreset is a catalogue preset that adds a loudness stage, so it moves
// the filter graph of a destination that has none: a change that needs a
// restart, against the one stereo track faketool's probe reports.
func loudnessPreset(t *testing.T) string {
	t.Helper()
	for _, p := range routing.Presets() {
		if p.Loudness != nil && p.Platform != "" {
			return p.ID
		}
	}
	t.Fatal("the catalogue has no platform preset with a loudness target")
	return ""
}

// destChild is dest:1's command line and the pid of the FFmpeg child running
// it, from GET /processes. The pid is what tells a restart of the child (the
// supervisor's own Restart keeps the same process record) from a kept one.
func destChild(t *testing.T, h http.Handler, sign func(*http.Request)) (command string, pid int) {
	t.Helper()
	r := jsonRequest(t, http.MethodGet, "/api/v1/processes", nil)
	sign(r)
	w := do(t, h, r)
	var rows []struct {
		Status struct {
			Name string `json:"name"`
			PID  int    `json:"pid"`
		} `json:"status"`
		Command string `json:"command"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode /processes: %v (%s)", err, w.Body.String())
	}
	for _, p := range rows {
		if p.Status.Name == "dest:1" {
			return p.Command, p.Status.PID
		}
	}
	t.Fatalf("no dest:1 process: %s", w.Body.String())
	return "", 0
}

// The bug, end to end on a destination that is really running: a preset applied
// through the route is STORED and reaches the RUNNING output, and the restart
// happens only when the graph moved. Applying the same preset again changes
// nothing the destination sends, so it keeps its process and says "live".
//
// MUTATION: roll back instead of committing in db.UpdateDestinationMixes.
// Observed: FAIL, applied="live" ("nothing ... changed") where a restart onto
// the stored loudness stage was due.
// MUTATION: drop the reconcile (reconcileWarn = ""). Observed: FAIL, the same:
// the running destination kept its old graph.
// MUTATION: restart every saved row after the reconcile (Engine.RestartDestination,
// which restarts the child inside the same process record). Observed: FAIL, the
// second apply's child pid changed.
func TestAPresetAppliedThroughTheRouteIsStoredAndReachesTheRunningMix(t *testing.T) {
	h, store, sign := runningDestServer(t)
	s := serverUnderTest(t, h)
	row, err := store.GetDestination(1)
	if err != nil {
		t.Fatal(err)
	}
	eng := s.engineForSource(row.SourceID)
	first := eng.DestinationRunOf(1)
	if !first.Running() {
		t.Fatal("the fixture's destination is not running; nothing here would be measured")
	}
	if row.Profile.Loudness != nil {
		t.Fatal("the fixture's destination already has a loudness target; the preset would change nothing")
	}
	cmdBefore, pidBefore := destChild(t, h, sign)
	if pidBefore == 0 {
		t.Fatal("the fixture's destination has no child pid")
	}
	preset := loudnessPreset(t)

	out := applyPreset(t, h, sign, preset, nil, nil, http.StatusOK)
	got := out.row(t, 1)
	if got.Outcome != presetSaved {
		t.Fatalf("outcome = %q (%s), want saved", got.Outcome, got.Message)
	}
	if got.Applied != "restart" {
		t.Errorf("a new loudness stage answered applied=%q reason=%q, want restart", got.Applied, got.Reason)
	}
	stored, err := store.GetDestination(1)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Profile.Loudness == nil {
		t.Fatalf("the preset was answered saved and not stored: %+v", stored.Profile)
	}
	second := eng.DestinationRunOf(1)
	if !second.Running() || second == first {
		t.Fatalf("the destination did not restart onto the new mix (running=%v, same process=%v)",
			second.Running(), second == first)
	}
	// The new process spawns its child a moment after the reconcile publishes it.
	cmdAfter, pidAfter := destChild(t, h, sign)
	for deadline := time.Now().Add(10 * time.Second); pidAfter == 0 && time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
		cmdAfter, pidAfter = destChild(t, h, sign)
	}
	if pidAfter == 0 {
		t.Fatal("the restarted destination never spawned a child")
	}
	if pidAfter == pidBefore {
		t.Errorf("the child kept pid %d through a restart onto a new graph", pidAfter)
	}
	if cmdAfter == cmdBefore || !strings.Contains(cmdAfter, "loudnorm") {
		t.Fatalf("the running command line does not carry the preset's loudness stage:\nbefore: %s\nafter:  %s",
			cmdBefore, cmdAfter)
	}

	again := applyPreset(t, h, sign, preset, nil, nil, http.StatusOK).row(t, 1)
	if again.Outcome != presetSaved || again.Applied != "live" {
		t.Fatalf("the same preset again answered %q/%q (%s), want saved and live", again.Outcome, again.Applied, again.Reason)
	}
	if !strings.Contains(again.Reason, "kept running") {
		t.Errorf("reason = %q, want the engine's nothing-changed note", again.Reason)
	}
	if eng.DestinationRunOf(1) != second {
		t.Fatal("a preset that changed nothing the destination sends replaced its process")
	}
	if _, pid := destChild(t, h, sign); pid != pidAfter {
		t.Fatalf("a preset that changed nothing the destination sends restarted its child: pid %d -> %d", pidAfter, pid)
	}
}

// presetFixture is a two-programme install whose destinations never run
// (FFmpeg cannot exec): what the route stores, skips and refuses, and where.
type presetFixture struct {
	s     *Server
	h     http.Handler
	store *db.DB
	sign  func(*http.Request)
	// one and two are the programmes; the destinations are named for what
	// they hold.
	one, two                           int64
	guarded, plain, withVOD, copyAudio int64
	other                              int64
}

func newPresetFixture(t *testing.T) presetFixture {
	t.Helper()
	s, h, store, sign := engineServer(t, defaultTools(), Options{})
	f := presetFixture{s: s, h: h, store: store, sign: sign}
	f.one = s.engOrNil().SourceID()
	second := &db.Source{Name: "second programme", Enabled: true, Ingest: db.DefaultSettings().Ingest}
	if err := store.CreateSource(second); err != nil {
		t.Fatalf("create second source: %v", err)
	}
	if err := s.mgr.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}
	f.two = second.ID

	mk := func(name string, src int64, edit func(*db.Destination)) int64 {
		t.Helper()
		d := &db.Destination{
			Name: name, Kind: db.DestRTMP, Platform: db.PlatformCustom,
			URL: "rtmp://example.invalid/live", StreamKey: "k-" + name, SourceID: &src,
			AudioBitrate: 160,
			Profile: routing.Profile{
				Mode: routing.ModeSimple, Normalize: routing.NormOff, SampleRate: 48000,
				Tracks: []routing.TrackSel{{Track: 0, Enabled: true, Gain: 1}, {Track: 1, Enabled: true, Gain: 0.7}},
			},
		}
		if edit != nil {
			edit(d)
		}
		created, err := store.CreateDestination(d)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return created.ID
	}
	f.guarded = mk("guarded", f.one, func(d *db.Destination) {
		d.Profile.Tracks[1].Muted = true
		d.Profile.ExcludeRoles = []routing.TrackRole{routing.RoleMusic}
	})
	f.plain = mk("plain", f.one, nil)
	f.withVOD = mk("with-vod", f.one, func(d *db.Destination) {
		v := routing.Profile{
			Mode: routing.ModeSimple, Normalize: routing.NormOff, SampleRate: 48000,
			Tracks:       []routing.TrackSel{{Track: 0, Enabled: true, Gain: 1, Muted: true}},
			ExcludeRoles: []routing.TrackRole{routing.RoleMusic},
		}
		d.VODProfile = &v
	})
	f.copyAudio = mk("copy-audio", f.one, func(d *db.Destination) {
		// A local file: the one kind that may copy a multitrack ingest's audio.
		d.Kind, d.URL, d.StreamKey = db.DestFile, "copy-audio.mkv", ""
		d.Profile.Tracks = d.Profile.Tracks[:1]
		d.Audio.Copy = true
	})
	f.other = mk("other-programme", f.two, nil)
	return f
}

func (f presetFixture) q(kv ...string) url.Values {
	v := url.Values{"source": {strconv.FormatInt(f.one, 10)}}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return v
}

func (f presetFixture) get(t *testing.T, id int64) *db.Destination {
	t.Helper()
	d, err := f.store.GetDestination(id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func enabledTracks(p routing.Profile) []int {
	var out []int
	for _, s := range p.Tracks {
		if s.Enabled {
			out = append(out, s.Track)
		}
	}
	return out
}

func mutedTracks(p routing.Profile) []int {
	var out []int
	for _, s := range p.Tracks {
		if s.Muted {
			out = append(out, s.Track)
		}
	}
	return out
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The programme's destinations, every one of them, and only them; a muted
// track stays muted and the music guard stays on; audio copy is skipped; the
// second mix is replaced only where there is one.
//
// MUTATION: list every destination on the install (ListDestinations) even when
// ?source= names one. Observed: FAIL, the other programme's destination changed.
// MUTATION: drop KeepOperatorDecisions (store the preset as built). Observed:
// FAIL, track 1 was unmuted and the music guard dropped.
// MUTATION: drop the audio-copy skip. Observed: FAIL, copy-audio answered saved.
// MUTATION: drop the no-second-mix skip. Observed: FAIL, plain gained a VOD mix.
func TestAPresetAppliesToTheProgrammeAndKeepsTheOperatorsDecisions(t *testing.T) {
	f := newPresetFixture(t)
	otherBefore := f.get(t, f.other).Profile

	out := applyPreset(t, f.h, f.sign, routing.PresetEverything, f.q(), nil, http.StatusOK)
	if out.Preset != routing.PresetEverything || out.Mix != "live" {
		t.Errorf("answered preset %q mix %q", out.Preset, out.Mix)
	}
	if len(out.Results) != 4 {
		t.Fatalf("%d rows, want the programme's four: %+v", len(out.Results), out.Results)
	}
	for _, id := range []int64{f.guarded, f.plain, f.withVOD} {
		r := out.row(t, id)
		if r.Outcome != presetSaved {
			t.Errorf("%s: outcome %q (%s), want saved", r.Name, r.Outcome, r.Message)
		}
		if r.Applied != "" {
			t.Errorf("%s: nothing is running, and the answer says applied=%q", r.Name, r.Applied)
		}
		if r.Profile == nil {
			t.Errorf("%s: a saved row does not carry its new mix", r.Name)
		}
	}
	if r := out.row(t, f.copyAudio); r.Outcome != presetSkipped || !strings.Contains(r.Message, "audio copy") {
		t.Errorf("copy-audio: %q %q, want skipped for audio copy", r.Outcome, r.Message)
	}

	// Every track the placeholder layout carries, so more than the two it had.
	plain := f.get(t, f.plain).Profile
	if got := enabledTracks(plain); len(got) <= 2 {
		t.Errorf("plain was not given the preset: enabled %v", got)
	}
	guarded := f.get(t, f.guarded).Profile
	if got := mutedTracks(guarded); !sameInts(got, []int{1}) {
		t.Errorf("guarded: muted %v after the preset, want [1] kept", got)
	}
	if len(guarded.ExcludeRoles) != 1 || guarded.ExcludeRoles[0] != routing.RoleMusic {
		t.Errorf("guarded: the music guard was dropped: %v", guarded.ExcludeRoles)
	}
	if k := out.row(t, f.guarded).Kept; k == nil || !sameInts(k.Muted, []int{1}) || len(k.Excluded) != 1 {
		t.Errorf("guarded: the answer does not say what it kept: %+v", k)
	}
	if got := f.get(t, f.other).Profile; !sameInts(enabledTracks(got), enabledTracks(otherBefore)) {
		t.Errorf("the other programme's destination changed: %v -> %v", enabledTracks(otherBefore), enabledTracks(got))
	}
	if f.get(t, f.plain).VODProfile != nil {
		t.Error("a live-mix preset gave plain a second mix")
	}

	// The second mix: only where one exists, with its own mute and guard kept.
	vodBefore := f.get(t, f.withVOD).Profile
	out = applyPreset(t, f.h, f.sign, routing.PresetEverything, f.q("mix", "vod"), nil, http.StatusOK)
	if r := out.row(t, f.withVOD); r.Outcome != presetSaved {
		t.Fatalf("with-vod: %q (%s), want saved", r.Outcome, r.Message)
	}
	for _, id := range []int64{f.plain, f.guarded} {
		if r := out.row(t, id); r.Outcome != presetSkipped || !strings.Contains(r.Message, "second (VOD) mix") {
			t.Errorf("%s: %q %q, want skipped for having no second mix", r.Name, r.Outcome, r.Message)
		}
		if f.get(t, id).VODProfile != nil {
			t.Errorf("destination %d was given a second mix it never had", id)
		}
	}
	vod := f.get(t, f.withVOD)
	if vod.VODProfile == nil || len(enabledTracks(*vod.VODProfile)) <= 1 {
		t.Fatalf("the second mix was not replaced: %+v", vod.VODProfile)
	}
	if !sameInts(mutedTracks(*vod.VODProfile), []int{0}) || len(vod.VODProfile.ExcludeRoles) != 1 {
		t.Errorf("the second mix lost its mute or guard: muted %v, roles %v",
			mutedTracks(*vod.VODProfile), vod.VODProfile.ExcludeRoles)
	}
	if !sameInts(enabledTracks(vod.Profile), enabledTracks(vodBefore)) {
		t.Error("a VOD preset changed the live mix")
	}
}

// ?destination= narrows the apply to one row of the programme, as the MCP tool
// uses it; a row that is not this preset's to change is refused rather than
// answered 200 with nothing done; a row on another programme is refused.
//
// MUTATION: ignore ?destination=. Observed: FAIL, plain changed too.
// MUTATION: answer a named skipped row 200. Observed: FAIL, 200 for copy-audio.
func TestNarrowingAPresetToOneDestination(t *testing.T) {
	f := newPresetFixture(t)
	plainBefore := f.get(t, f.plain).Profile

	out := applyPreset(t, f.h, f.sign, routing.PresetEverything,
		f.q("destination", strconv.FormatInt(f.guarded, 10)), nil, http.StatusOK)
	if len(out.Results) != 1 || out.Results[0].ID != f.guarded || out.Results[0].Outcome != presetSaved {
		t.Fatalf("a narrowed apply answered %+v", out.Results)
	}
	if !sameInts(enabledTracks(f.get(t, f.plain).Profile), enabledTracks(plainBefore)) {
		t.Fatal("a narrowed apply changed another destination")
	}

	msg := mustJSONError(t, f.h, f.sign, http.MethodPost, presetApplyPath(routing.PresetEverything,
		f.q("destination", strconv.FormatInt(f.copyAudio, 10))), nil, http.StatusConflict)
	if !strings.Contains(msg, "audio copy") {
		t.Errorf("a named copy-audio destination was refused with %q", msg)
	}
	mustJSONError(t, f.h, f.sign, http.MethodPost, presetApplyPath(routing.PresetEverything,
		f.q("destination", strconv.FormatInt(f.plain, 10), "mix", "vod")), nil, http.StatusConflict)
	mustJSONError(t, f.h, f.sign, http.MethodPost, presetApplyPath(routing.PresetEverything,
		f.q("destination", strconv.FormatInt(f.other, 10))), nil, http.StatusBadRequest)
	mustJSONError(t, f.h, f.sign, http.MethodPost, presetApplyPath(routing.PresetEverything,
		f.q("destination", "999999")), nil, http.StatusNotFound)
	mustJSONError(t, f.h, f.sign, http.MethodPost, presetApplyPath(routing.PresetEverything,
		f.q("mix", "archive")), nil, http.StatusBadRequest)
	mustJSONError(t, f.h, f.sign, http.MethodPost, presetApplyPath("no-such-preset", f.q()), nil, http.StatusBadRequest)
	// Two programmes: an apply that does not say which is refused, as every
	// programme-scoped route refuses it.
	mustJSONError(t, f.h, f.sign, http.MethodPost, presetApplyPath(routing.PresetEverything, nil), nil, http.StatusBadRequest)
}

// ATOMIC: every destination is checked before any is written, and one that
// cannot take the preset refuses the whole request. Here the guarded
// destination keeps music out, the ingest's only selected track is music, so the
// preset would leave it silent: refused, and plain -- which could take it -- is
// not changed either.
//
// MUTATION: in db.UpdateDestinationMixes, write and commit the rows that passed
// even when another was refused. Observed: FAIL, plain was changed by a refused
// request.
func TestOneDestinationThatCannotTakeAPresetRefusesTheWholeRequest(t *testing.T) {
	f := newPresetFixture(t)
	send(t, f.h, f.sign, http.MethodPut, "/api/v1/source/annotations?source="+strconv.FormatInt(f.one, 10),
		map[string]any{"annotations": []map[string]any{{"track": 2, "role": "music"}}}, http.StatusOK)
	plainBefore := f.get(t, f.plain).Profile
	guardedBefore := f.get(t, f.guarded).Profile

	// Mic only, on track 2: the music track, which guarded excludes.
	raw := send(t, f.h, f.sign, http.MethodPost, presetApplyPath(routing.PresetMicOnly, f.q()),
		map[string]any{"micTrack": 2, "musicTrack": 2, "cleanTrack": 1}, http.StatusBadRequest)
	var out presetApplyAnswer
	decodeInto(t, raw, &out)
	if !strings.Contains(out.Error, "nothing was changed") || !strings.Contains(out.Error, `"guarded"`) {
		t.Errorf("the refusal does not name the destination or say nothing changed: %q", out.Error)
	}
	if len(out.Results) != 1 || out.Results[0].ID != f.guarded || out.Results[0].Outcome != presetRefused || out.Results[0].Message == "" {
		t.Fatalf("the refusal's rows: %+v", out.Results)
	}
	if !sameInts(enabledTracks(f.get(t, f.plain).Profile), enabledTracks(plainBefore)) {
		t.Fatal("a refused request changed a destination that could have taken the preset")
	}
	if !sameInts(enabledTracks(f.get(t, f.guarded).Profile), enabledTracks(guardedBefore)) {
		t.Fatal("a refused request changed the destination it refused")
	}
}

// ONLY THE MIX IS WRITTEN, from the row as it stands when the transaction reads
// it. A stop, a scheduled start or a key refresh does not take the PUT's lock,
// so one that lands between the listing and the write must survive it: written
// back whole, the destination just stopped would come back enabled and the
// reconcile would start it. Here a start and a rename land in between.
//
// MUTATION: write the listed rows back whole with UpdateDestination in place of
// db.UpdateDestinationMixes (the first version of this route). Observed: FAIL,
// "switched off a destination started meanwhile".
func TestAPresetDoesNotUndoAWriteThatLandedWhileItWasApplied(t *testing.T) {
	f := newPresetFixture(t)
	presetApplyBetweenPhases = func() {
		if err := f.store.SetDestinationEnabled(f.plain, true); err != nil {
			t.Errorf("start between the phases: %v", err)
		}
		d := f.get(t, f.guarded)
		d.Name = "renamed meanwhile"
		if _, err := f.store.UpdateDestination(d); err != nil {
			t.Errorf("rename between the phases: %v", err)
		}
	}
	t.Cleanup(func() { presetApplyBetweenPhases = func() {} })

	out := applyPreset(t, f.h, f.sign, routing.PresetEverything, f.q(), nil, http.StatusOK)
	for _, id := range []int64{f.plain, f.guarded} {
		if r := out.row(t, id); r.Outcome != presetSaved {
			t.Errorf("destination %d: %q (%s), want saved", id, r.Outcome, r.Message)
		}
	}
	plain := f.get(t, f.plain)
	if !plain.Enabled {
		t.Error("the preset wrote back the listed row and switched off a destination started meanwhile")
	}
	if len(enabledTracks(plain.Profile)) <= 2 {
		t.Errorf("plain did not take the preset: %v", enabledTracks(plain.Profile))
	}
	guarded := f.get(t, f.guarded)
	if guarded.Name != "renamed meanwhile" {
		t.Errorf("the preset wrote back the listed row and lost a rename: %q", guarded.Name)
	}
	if !sameInts(mutedTracks(guarded.Profile), []int{1}) {
		t.Errorf("the mutes were not carried from the row as it stood: %v", mutedTracks(guarded.Profile))
	}
}

// A destination deleted between the listing and the transaction (DELETE does not
// take the PUT's lock) is answered skipped, and the others are saved. Named, it
// is a 404.
//
// MUTATION: in db.UpdateDestinationMixes, return ErrNotFound for a missing row
// instead of listing it in missing. Observed: FAIL, 500 and nothing saved.
func TestADestinationDeletedWhileAPresetIsAppliedIsSkipped(t *testing.T) {
	f := newPresetFixture(t)
	presetApplyBetweenPhases = func() {
		if err := f.store.DeleteDestination(f.plain); err != nil {
			t.Errorf("delete between the phases: %v", err)
		}
	}
	t.Cleanup(func() { presetApplyBetweenPhases = func() {} })

	out := applyPreset(t, f.h, f.sign, routing.PresetEverything, f.q(), nil, http.StatusOK)
	lost := out.row(t, f.plain)
	if lost.Outcome != presetSkipped || !strings.Contains(lost.Message, "deleted") || lost.Name != "plain" {
		t.Errorf("the deleted destination answered %+v, want skipped, named, saying it was deleted", lost)
	}
	if r := out.row(t, f.guarded); r.Outcome != presetSaved {
		t.Errorf("guarded: %q, want saved", r.Outcome)
	}
	if got := enabledTracks(f.get(t, f.guarded).Profile); len(got) <= 2 {
		t.Errorf("guarded was answered saved and not stored: %v", got)
	}

	presetApplyBetweenPhases = func() {
		if err := f.store.DeleteDestination(f.guarded); err != nil {
			t.Errorf("delete between the phases: %v", err)
		}
	}
	mustJSONError(t, f.h, f.sign, http.MethodPost, presetApplyPath(routing.PresetEverything,
		f.q("destination", strconv.FormatInt(f.guarded, 10))), nil, http.StatusNotFound)
}

// PARTIAL FAILURE, the one an all-or-nothing write leaves: the mixes are stored
// and the reconcile that should apply them fails. The answer is 200, every row
// saved and none claiming how it applied, with the reconcile's sentence in
// warnings and reconcileFailed set -- what the Stream Deck key alerts on.
//
// MUTATION: hand writeMutation "" instead of the reconcile's sentence. Observed:
// FAIL, reconcileFailed=false and no warning.
func TestAReconcileThatFailsAfterThePresetIsStoredIsReported(t *testing.T) {
	f := newPresetFixture(t)
	const broken = "the routing preset was saved, but the running pipeline could not be updated to match it: test"
	presetReconcile = func(*Server) string { return broken }
	t.Cleanup(func() { presetReconcile = func(s *Server) string { return s.reconcileNow("the routing preset") } })

	out := applyPreset(t, f.h, f.sign, routing.PresetEverything, f.q(), nil, http.StatusOK)
	if !out.ReconcileFailed || len(out.Warnings) != 1 || out.Warnings[0] != broken {
		t.Fatalf("a failed reconcile was not reported: reconcileFailed=%v warnings=%v", out.ReconcileFailed, out.Warnings)
	}
	for _, id := range []int64{f.plain, f.guarded, f.withVOD} {
		r := out.row(t, id)
		if r.Outcome != presetSaved || r.Applied != "" {
			t.Errorf("destination %d answered %q applied=%q; want saved, and no claim of how it applied", id, r.Outcome, r.Applied)
		}
	}
	if got := enabledTracks(f.get(t, f.plain).Profile); len(got) <= 2 {
		t.Errorf("the preset was not stored: %v", got)
	}
}

// A destination whose stream key this machine cannot read reads back switched
// off and keyless (scanDestination). Written back whole, it would be stored
// switched off for good, and restoring the key file would no longer bring it
// back. The mix-only write leaves both columns as they are.
//
// MUTATION: write the whole row (UpdateDestination) for each saved row after
// the transaction. Observed: FAIL, the enabled column went from 1 to 0.
func TestAPresetLeavesAnUnreadableKeysDestinationSwitchedOn(t *testing.T) {
	f := newPresetFixture(t)
	if err := f.store.SetDestinationEnabled(f.plain, true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.SQL().Exec(`UPDATE destinations SET stream_key='', stream_key_enc=? WHERE id=?`,
		[]byte("not a ciphertext this box can open"), f.plain); err != nil {
		t.Fatal(err)
	}
	if d := f.get(t, f.plain); d.KeyUnreadable == "" || d.Enabled {
		t.Fatalf("the fixture's key is readable (%q) or the row reads enabled (%v); this test measures nothing",
			d.KeyUnreadable, d.Enabled)
	}

	out := applyPreset(t, f.h, f.sign, routing.PresetEverything, f.q(), nil, http.StatusOK)
	if r := out.row(t, f.plain); r.Outcome != presetSaved {
		t.Fatalf("plain: %q (%s), want saved", r.Outcome, r.Message)
	}
	var enabled bool
	var enc []byte
	if err := f.store.SQL().QueryRow(`SELECT enabled, stream_key_enc FROM destinations WHERE id=?`, f.plain).
		Scan(&enabled, &enc); err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Error("the preset stored a destination with an unreadable key switched off")
	}
	if string(enc) != "not a ciphertext this box can open" {
		t.Errorf("the preset rewrote the sealed key: %q", enc)
	}
	if got := enabledTracks(f.get(t, f.plain).Profile); len(got) <= 2 {
		t.Errorf("the preset was not stored: %v", got)
	}
}

// dryRun answers what the route would store, through the same check, and
// stores nothing: the MCP preview.
//
// MUTATION: ignore dryRun. Observed: FAIL, plain changed.
func TestADryRunChecksAndStoresNothing(t *testing.T) {
	f := newPresetFixture(t)
	plainBefore := f.get(t, f.plain).Profile
	out := applyPreset(t, f.h, f.sign, routing.PresetEverything, f.q("dryRun", "true"), nil, http.StatusOK)
	if !out.DryRun {
		t.Error("the answer does not say it was a dry run")
	}
	r := out.row(t, f.guarded)
	if r.Outcome != presetReady || r.Profile == nil || r.Kept == nil {
		t.Errorf("guarded: %+v, want ready with its new mix and what it kept", r)
	}
	if !sameInts(enabledTracks(f.get(t, f.plain).Profile), enabledTracks(plainBefore)) {
		t.Fatal("a dry run stored the preset")
	}
}

// With no body the pickers follow the programme's track roles, as the console's
// do: "No music" drops the track annotated music, not track 0. A body still
// replaces the options outright.
//
// MUTATION: start from routing.DefaultPresetOpts() alone. Observed: FAIL, track
// 0 was dropped and the music track kept.
func TestABodylessPresetFollowsTheProgrammesTrackRoles(t *testing.T) {
	f := newPresetFixture(t)
	send(t, f.h, f.sign, http.MethodPut, "/api/v1/source/annotations?source="+strconv.FormatInt(f.one, 10),
		map[string]any{"annotations": []map[string]any{{"track": 2, "role": "music"}}}, http.StatusOK)
	q := f.q("destination", strconv.FormatInt(f.plain, 10))

	applyPreset(t, f.h, f.sign, routing.PresetNoMusic, q, nil, http.StatusOK)
	got := enabledTracks(f.get(t, f.plain).Profile)
	has := func(n int) bool {
		for _, x := range got {
			if x == n {
				return true
			}
		}
		return false
	}
	if has(2) || !has(0) {
		t.Fatalf("a bodyless No music kept %v; want track 2 (music) out and track 0 in", got)
	}

	applyPreset(t, f.h, f.sign, routing.PresetNoMusic, q, map[string]any{"musicTrack": 0}, http.StatusOK)
	got = enabledTracks(f.get(t, f.plain).Profile)
	if has(0) || !has(2) {
		t.Fatalf("a body naming musicTrack 0 kept %v; want track 0 out", got)
	}
}

// A platform preset's id has a colon, and the Stream Deck plugin and the console
// escape the segment (encodeURIComponent): "platform%3Ayoutube". chi hands the
// route the segment as sent, so both preset routes refused every platform preset
// from those clients as unknown. Found by the Stream Deck e2e.
//
// MUTATION: return chi.URLParam(r, "preset") unescaped from presetParam.
// Observed: FAIL, 400 unknown routing preset "platform%3Ayoutube" on both.
func TestAnEscapedPlatformPresetIdIsUnderstoodByBothPresetRoutes(t *testing.T) {
	f := newPresetFixture(t)
	id := loudnessPreset(t)
	escaped := strings.ReplaceAll(url.PathEscape(id), ":", "%3A")
	if escaped == id {
		t.Fatalf("preset %q has nothing to escape; this test would assert nothing", id)
	}
	q := f.q("destination", strconv.FormatInt(f.plain, 10))
	send(t, f.h, f.sign, http.MethodPost, "/api/v1/routing/presets/"+escaped+"/apply?"+q.Encode(), nil, http.StatusOK)
	if f.get(t, f.plain).Profile.Loudness == nil {
		t.Fatal("the escaped platform preset was answered and not applied")
	}
	send(t, f.h, f.sign, http.MethodPost, "/api/v1/routing/presets/"+escaped+"?"+f.q().Encode(), nil, http.StatusOK)
}

// Reachable with an API token, and only an admin one: the plugin and the MCP
// server send bearer tokens and never hold a session. A read token is refused
// before anything is stored.
//
// MUTATION: register the route in the session-only group. Observed: FAIL, the
// admin token was refused.
func TestThePresetApplyRouteTakesAnAdminTokenAndRefusesARead(t *testing.T) {
	f := newPresetFixture(t)
	admin := createScopedToken(t, f.h, f.sign, "deck-admin", db.ScopeAdmin)
	read := createScopedToken(t, f.h, f.sign, "deck-read", db.ScopeRead)
	plainBefore := f.get(t, f.plain).Profile
	q := f.q("destination", strconv.FormatInt(f.plain, 10))

	mustJSONError(t, f.h, bearer(read), http.MethodPost, presetApplyPath(routing.PresetEverything, q), nil, http.StatusForbidden)
	if !sameInts(enabledTracks(f.get(t, f.plain).Profile), enabledTracks(plainBefore)) {
		t.Fatal("a read token's refused apply stored the preset")
	}
	out := applyPreset(t, f.h, bearer(admin), routing.PresetEverything, q, nil, http.StatusOK)
	if out.row(t, f.plain).Outcome != presetSaved {
		t.Fatalf("an admin token's apply answered %+v", out.Results)
	}
	if got := enabledTracks(f.get(t, f.plain).Profile); len(got) <= 2 {
		t.Fatalf("an admin token's apply was answered and not stored: %v", got)
	}
}
