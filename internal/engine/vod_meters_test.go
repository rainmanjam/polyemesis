package engine

import (
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/meters"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// A destination with a VOD track publishes two mixes, and the Meters page shows
// a reading for each. These tests pin which analysers exist, what each one
// measures, and when it is cycled. The analyser is a separate process reading
// the destination's relay, so none of this touches what the destination
// publishes, and the test that pins that is in the ffmpeg package's argv tests.

// pairedDest is a running destination whose graph carries both mixes, the way
// startDest sees it when the VOD mix is going out.
func pairedDest(t *testing.T, row *db.Destination) *destination {
	t.Helper()
	vod := mixProfile(1)
	row.VODProfile = &vod
	paired, err := routing.CompilePair(row.Profile, row.VODProfile, pairSource())
	if err != nil {
		t.Fatalf("CompilePair: %v", err)
	}
	d := runningDest(t, row)
	d.compiled = paired.Result
	d.spec = "specPaired"
	return d
}

// A destination whose running graph carries the VOD mix earns a second
// analyser keyed to that mix, and the live analyser sinks the VOD half so that
// its own graph can be handed to FFmpeg whole.
//
// MUTATION: in loudnessWanted, drop the vodKey lookup. Observed: FAIL, "no VOD
// analyser for a destination whose graph carries a second mix".
func TestAPairedDestinationEarnsAnAnalyserForEachMix(t *testing.T) {
	on := db.Settings{Meters: db.MeterSettings{Enabled: true}}
	d := pairedDest(t, &db.Destination{ID: 1, Name: "yt", Kind: db.DestRTMP, Platform: db.PlatformYouTube, Profile: mixProfile(0)})
	e := &Engine{dests: map[int64]*destination{1: d}}

	got := e.loudnessWanted(on)
	live, okLive := got[liveKey(1)]
	vod, okVOD := got[vodKey(1)]
	if !okLive || !okVOD {
		t.Fatalf("live analyser wanted=%v, VOD analyser wanted=%v; want both", okLive, okVOD)
	}
	if len(live.sink) != 1 || live.sink[0] != d.compiled.SecondOutLabel {
		t.Errorf("the live analyser sinks %v, want the VOD label %q", live.sink, d.compiled.SecondOutLabel)
	}
	if len(vod.sink) != 1 || vod.sink[0] != d.compiled.OutLabel {
		t.Errorf("the VOD analyser sinks %v, want the live label %q", vod.sink, d.compiled.OutLabel)
	}
}

// A destination whose running graph is the single live mix (the VOD mix was
// negotiated away, or never asked for) earns only the live analyser. Metering a
// mix that is not published would show a reading for audio nobody receives.
//
// MUTATION: in loudnessPlanFor, skip the SecondOutLabel check on the VOD arm.
// Observed: FAIL, "an analyser for a mix that is not on the wire".
func TestASingleMixDestinationEarnsNoVODAnalyser(t *testing.T) {
	on := db.Settings{Meters: db.MeterSettings{Enabled: true}}
	row := &db.Destination{ID: 1, Name: "yt", Kind: db.DestRTMP, Platform: db.PlatformYouTube, Profile: mixProfile(0)}
	vod := mixProfile(1)
	row.VODProfile = &vod
	d := runningDest(t, row) // compiled: the live mix alone, no SecondOutLabel
	e := &Engine{dests: map[int64]*destination{1: d}}

	got := e.loudnessWanted(on)
	if _, ok := got[vodKey(1)]; ok {
		t.Errorf("a VOD analyser was planned for a graph with no second mix")
	}
	if _, ok := got[liveKey(1)]; !ok {
		t.Errorf("the live analyser was not planned")
	}
}

// The VOD mix is judged against its own target. Inheriting the live mix's
// target would judge an archive against the programme it was cut from.
//
// MUTATION: in loudnessPlanFor, take loud from row.Profile for the VOD arm.
// Observed: FAIL, "the VOD mix was judged against the live target".
func TestTheVODAnalyserIsJudgedAgainstItsOwnTarget(t *testing.T) {
	on := db.Settings{Meters: db.MeterSettings{Enabled: true}}
	live := mixProfile(0)
	live.Loudness = &routing.Loudness{TargetLUFS: -14}
	row := &db.Destination{ID: 1, Name: "yt", Kind: db.DestRTMP, Platform: db.PlatformYouTube, Profile: live}
	d := pairedDest(t, row)
	vodProfile := mixProfile(1)
	vodProfile.Loudness = &routing.Loudness{TargetLUFS: -20}
	d.row.VODProfile = &vodProfile

	e := &Engine{dests: map[int64]*destination{1: d}}
	got := e.loudnessWanted(on)
	if p := got[vodKey(1)]; p.target.LUFS != -20 {
		t.Errorf("the VOD analyser targets %v LUFS, want its own -20", p.target.LUFS)
	}
	if p := got[liveKey(1)]; p.target.LUFS != -14 {
		t.Errorf("the live analyser targets %v LUFS, want its own -14", p.target.LUFS)
	}
}

// A live fader moves a level in the destination's graph without restarting the
// publisher, and the destination's spec masks that level on purpose. The analyser
// is not the publisher, so it must be cycled when a level moves: otherwise a
// muted track keeps reading as loud on the Meters page.
//
// MUTATION: in loudnessPlanFor, drop FilterComplex from the signature. Observed:
// FAIL, "a level change did not cycle the analyser".
func TestALiveLevelChangeCyclesTheAnalyserEvenWhenTheSpecIsUnchanged(t *testing.T) {
	on := db.Settings{Meters: db.MeterSettings{Enabled: true}}
	sigAt := func(level string) string {
		d := runningDest(t, &db.Destination{ID: 1, Name: "yt", Kind: db.DestRTMP, Platform: db.PlatformYouTube})
		d.compiled.FilterComplex = "[0:a:0]volume@trk0=volume=" + level + "[aout]"
		d.spec = "specSameBecauseLevelsAreMasked"
		e := &Engine{dests: map[int64]*destination{1: d}}
		return e.loudnessWanted(on)[liveKey(1)].sig
	}
	if sigAt("1") == sigAt("0") {
		t.Fatal("muting a track left the analyser's signature unchanged, so the analyser would keep measuring the old level")
	}
}

// The analysers are keyed by mix, so the VOD one's report is never confused with
// the live one's, and the store is told which analysers are wanted by mix too.
func TestTheAnalyserKeysNameTheirMix(t *testing.T) {
	if liveKey(9).mix != meters.MixLive || vodKey(9).mix != meters.MixVOD {
		t.Fatalf("liveKey=%q vodKey=%q", liveKey(9).mix, vodKey(9).mix)
	}
	if liveKey(9) == vodKey(9) {
		t.Fatal("the live and VOD analysers of one destination share a key")
	}
}

// The alert watcher keys its loudness state by destination, so a VOD mix's
// failing verdict must not become an alert on that destination: it would fire
// for the live mix's id and clear when the live mix recovered. The VOD verdict
// stays on the Meters page.
//
// MUTATION: in alertSnapshot, drop the MixVOD skip. Observed: FAIL, "a VOD mix
// verdict became a loudness alert".
func TestAVODMixVerdictIsNotAnAlertOnItsDestination(t *testing.T) {
	e, _ := storeEngine(t)
	target := meters.TargetFor(&routing.Loudness{TargetLUFS: -14}, routing.PlatformYouTube)
	now := time.Now()
	e.loudStore.Put(meters.Observe(9, "yt", target, meters.Frame{IntegratedLUFS: -30, Seconds: 60, Integrated: true}, now).ForMix(meters.MixVOD))

	for _, l := range e.alertSnapshot(now, true).Loudness {
		if l.ID == 9 {
			t.Fatalf("a VOD mix's failing verdict was raised as a loudness alert on destination 9: %+v", l)
		}
	}
}
