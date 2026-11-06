package meters

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/platform"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// A destination with a VOD track has two analysers, one per mix, and the store
// has to hold both. Keyed by destination alone, the VOD report would overwrite
// the live one on every frame, and the dashboard would show whichever arrived
// last as though it were the destination's only reading.

// MUTATION: in Store.Put, key by DestinationID alone. Observed: FAIL, "the VOD
// report replaced the live one for the same destination".
func TestTheVODReportIsKeptBesideTheLiveReportForTheSameDestination(t *testing.T) {
	s := NewStore()
	now := time.Now()
	target := TargetFor(&routing.Loudness{TargetLUFS: -16}, routing.Platform(platform.Twitch))

	s.Put(Observe(7, "twitch", target, Frame{IntegratedLUFS: -15, Seconds: 30, Integrated: true}, now))
	s.Put(Observe(7, "twitch", target, Frame{IntegratedLUFS: -22, Seconds: 30, Integrated: true}, now).ForMix(MixVOD))

	all := s.All()
	if len(all) != 2 {
		t.Fatalf("the store holds %d reports for one destination with two mixes, want 2", len(all))
	}
	live, ok := s.Get(7)
	if !ok || live.Mix != MixLive || live.IntegratedLUFS != -15 {
		t.Errorf("Get(7) = %+v, want the live mix's report", live)
	}
	vod, ok := s.GetMix(7, MixVOD)
	if !ok || vod.Mix != MixVOD || vod.IntegratedLUFS != -22 {
		t.Errorf("GetMix(7, vod) = %+v, want the VOD mix's report", vod)
	}
}

// A report with no mix is the live mix. Every constructor predates the field,
// and the JSON the UI reads must always say which mix it is.
//
// MUTATION: in Store.Put, skip the normalisation of an empty Mix. Observed:
// FAIL, a report with no mix was stored as "" and not "live".
func TestAReportWithNoMixIsStoredAsTheLiveMix(t *testing.T) {
	s := NewStore()
	s.Put(Observe(3, "kick", Target{Source: TargetNone}, Frame{}, time.Now()))
	got, ok := s.Get(3)
	if !ok {
		t.Fatal("the report was not stored")
	}
	if got.Mix != MixLive {
		t.Errorf("mix = %q, want %q", got.Mix, MixLive)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"mix":"live"`) {
		t.Errorf("the wire form does not name its mix: %s", raw)
	}
}

// When a destination stops having a VOD mix, its VOD analyser goes, and its
// report must go with it. KeepFor is how the engine says which analysers are
// still wanted, so a VOD report nobody is measuring cannot outlive it.
//
// MUTATION: in KeepFor, ignore the mix and keep by destination. Observed: FAIL,
// "the VOD report survived after its analyser was removed".
func TestKeepForRemovesTheVODReportOnceItsAnalyserIsNoLongerWanted(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.Put(Observe(7, "twitch", Target{Source: TargetNone}, Frame{}, now))
	s.Put(Observe(7, "twitch", Target{Source: TargetNone}, Frame{}, now).ForMix(MixVOD))

	s.KeepFor(map[Key]bool{{ID: 7, Mix: MixLive}: true})
	all := s.All()
	if len(all) != 1 || all[0].Mix != MixLive {
		t.Fatalf("after dropping the VOD analyser the store holds %+v, want only the live report", all)
	}
}
