// The second (VOD) audio mix, over the wire.
//
// WHY THIS EXISTS SEPARATELY FROM internal/db/multitrack_test.go. That file
// proves the STORE round-trips a *routing.Profile: create, update, re-read.
// This one proves the two things only the HTTP layer can get wrong, and both of
// them are the difference between an editor that works and one that silently
// discards what the operator typed:
//
//  1. A PUT carrying a vodProfile has to STORE it. handleUpdateDestination
//     decodes over the existing row, so a field that decodes and is then
//     overwritten by the handler -- the way ExtraOutputArgs deliberately is --
//     would look identical to a caller until the reload.
//
//  2. A PUT carrying `"vodProfile": null` has to CLEAR it. This is the one the
//     editor depends on and the one that cannot be inferred from the store
//     test: decoding OVER an existing row means an ABSENT key leaves the stored
//     pointer alone, so "off" has to travel as an explicit null or the operator
//     watches their delete undo itself on the next load. RoutingPage's save
//     sends the null for exactly this reason.
//
// It runs against renditionServer rather than testServer because every
// destination handler here reaches through s.eng(), and a nil manager makes
// the create 500 before any of this is reached.
package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/routing"
)

// vodBody is a second mix that is meaningfully DIFFERENT from any default, so
// a handler that dropped it and returned the primary profile instead would not
// accidentally pass.
func vodBody() map[string]any {
	return map[string]any{
		"mode":       "simple",
		"normalize":  "loudnorm",
		"sampleRate": 44100,
		"delayMs":    250,
		"tracks": []map[string]any{
			{"track": 0, "enabled": true, "gain": -3},
			{"track": 2, "enabled": true, "gain": 0},
		},
		"excludeRoles": []string{"music"},
	}
}

func vodDest(t *testing.T, store *db.DB) int64 {
	t.Helper()
	created, err := store.CreateDestination(&db.Destination{
		Name: "archive", Kind: db.DestRTMP, Platform: db.PlatformTwitch,
		// Short and obviously fake, like every other stream key in these tests
		// ("original-key", "sk-live-Zq7", "key"). A realistic-LOOKING one --
		// this was `live_1_abcdefghijklmnop` -- trips gitleaks' generic-api-key
		// rule, which fires on a credential-shaped literal next to a `...Key:`
		// identifier. That failed the allowlist self-test in security.yml before
		// the scan itself even ran, because that guard needs a clean baseline to
		// prove anything. Allowlisting the file would be the wrong fix and
		// .gitleaks.toml says so: a blanket path rule "would hide a real key".
		URL: "rtmp://live.twitch.tv/app", StreamKey: "sk-live-vod",
		AudioBitrate: 160,
	})
	if err != nil {
		t.Fatalf("create destination: %v", err)
	}
	return created.ID
}

// readVOD re-reads the destination THROUGH THE API -- not through the store --
// because the response is what the editor reloads from, and a field the store
// holds but the handler never marshals would round-trip in the database and
// still arrive empty in the browser.
func readVOD(t *testing.T, h http.Handler, sign func(*http.Request), id int64) *routing.Profile {
	t.Helper()
	r := jsonRequest(t, http.MethodGet, "/api/v1/destinations/"+strconv.FormatInt(id, 10), nil)
	sign(r)
	w := do(t, h, r)
	if w.Code != http.StatusOK {
		t.Fatalf("get destination: status %d, body %s", w.Code, w.Body.String())
	}
	var resp struct {
		Destination struct {
			VODProfile *routing.Profile `json:"vodProfile"`
		} `json:"destination"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode get response: %v (body %s)", err, w.Body.String())
	}
	return resp.Destination.VODProfile
}

func putVOD(t *testing.T, h http.Handler, sign func(*http.Request), id int64, vod any) {
	t.Helper()
	r := jsonRequest(t, http.MethodPut, "/api/v1/destinations/"+strconv.FormatInt(id, 10),
		map[string]any{"vodProfile": vod})
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("update destination: status %d, body %s", w.Code, w.Body.String())
	}
}

// TestASecondMixSurvivesTheRoundTripThroughTheAPI is the editor's contract:
// set it, save, reload, get the same thing back.
//
// MUTATION: in handleUpdateDestination, add `existing.VODProfile = nil`
// immediately after the decode. Observed: FAIL -- "the second mix did not
// survive the save: nothing came back".
func TestASecondMixSurvivesTheRoundTripThroughTheAPI(t *testing.T) {
	h, store, sign := renditionServer(t, defaultTools())
	id := vodDest(t, store)

	// A fresh destination has none, and that is the normal state.
	if got := readVOD(t, h, sign, id); got != nil {
		t.Fatalf("a new destination arrived with a second mix it never asked for: %+v", *got)
	}

	putVOD(t, h, sign, id, vodBody())

	got := readVOD(t, h, sign, id)
	if got == nil {
		t.Fatal("the second mix did not survive the save: nothing came back")
	}
	if got.Normalize != routing.NormLoudnorm {
		t.Errorf("normalize came back %q, want %q", got.Normalize, routing.NormLoudnorm)
	}
	if got.SampleRate != 44100 {
		t.Errorf("sampleRate came back %d, want 44100", got.SampleRate)
	}
	if got.DelayMS != 250 {
		t.Errorf("delayMs came back %d, want 250", got.DelayMS)
	}
	if len(got.Tracks) != 2 {
		t.Fatalf("track selection came back with %d entries, want 2: %+v", len(got.Tracks), got.Tracks)
	}
	if got.Tracks[0].Gain != -3 {
		t.Errorf("the first track's gain came back %v, want -3", got.Tracks[0].Gain)
	}
	// The DMCA switch specifically: a second mix whose whole purpose is to drop
	// the music is worthless if the exclusion is the field that gets lost.
	if len(got.ExcludeRoles) != 1 || got.ExcludeRoles[0] != routing.RoleMusic {
		t.Errorf("excludeRoles came back %+v, want [music]", got.ExcludeRoles)
	}
}

// TestAnExplicitNullClearsTheSecondMix is the OFF half, and it is the one the
// decode-over-existing design makes easy to get wrong.
//
// MUTATION: in handleUpdateDestination, restore the stored pointer after the
// decode -- `existing.VODProfile = saved.VOD` -- the way the expert args are
// restored. Observed: FAIL -- "switching the second mix off left it in place".
func TestAnExplicitNullClearsTheSecondMix(t *testing.T) {
	h, store, sign := renditionServer(t, defaultTools())
	id := vodDest(t, store)

	putVOD(t, h, sign, id, vodBody())
	if readVOD(t, h, sign, id) == nil {
		t.Fatal("setup failed: the second mix was not stored, so the clear proves nothing")
	}

	putVOD(t, h, sign, id, nil)

	if got := readVOD(t, h, sign, id); got != nil {
		t.Errorf("switching the second mix off left it in place: %+v", *got)
	}
}

// TestAPutThatSaysNothingAboutTheSecondMixLeavesItAlone pins the other half of
// the same rule, and it is why RoutingPage sends the field on every save rather
// than only when it changed.
//
// Any other caller that PUTs a destination -- renaming it, toggling enabled,
// the destination dialog saving a stream key -- omits vodProfile entirely, and
// those saves must not destroy a second mix configured elsewhere.
func TestAPutThatSaysNothingAboutTheSecondMixLeavesItAlone(t *testing.T) {
	h, store, sign := renditionServer(t, defaultTools())
	id := vodDest(t, store)

	putVOD(t, h, sign, id, vodBody())

	r := jsonRequest(t, http.MethodPut, "/api/v1/destinations/"+strconv.FormatInt(id, 10),
		map[string]any{"name": "archive renamed"})
	sign(r)
	if w := do(t, h, r); w.Code != http.StatusOK {
		t.Fatalf("rename: status %d, body %s", w.Code, w.Body.String())
	}

	if readVOD(t, h, sign, id) == nil {
		t.Error("renaming the destination destroyed its second audio mix; every PUT that does " +
			"not mention vodProfile would silently do the same")
	}
}
