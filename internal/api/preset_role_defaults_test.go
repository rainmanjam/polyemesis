package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/routing"
)

func annotateMusicOnTrack2(t *testing.T, h http.Handler, sign func(*http.Request), src int64) {
	t.Helper()
	send(t, h, sign, http.MethodPut, "/api/v1/source/annotations?source="+itoa(src),
		map[string]any{"annotations": []map[string]any{{"track": 2, "role": "music"}}}, http.StatusOK)
}

func enabledIn(p routing.Profile) map[int]bool {
	out := map[int]bool{}
	for _, tr := range p.Tracks {
		if tr.Enabled {
			out[tr.Track] = true
		}
	}
	return out
}

// POST /routing/presets/{preset} with no body used to start from the raw
// OBS-convention defaults while /apply started from the programme's track
// roles, so the preview and the apply of the same preset picked different
// tracks on a role-annotated ingest.
//
// Mutation: start handleApplyPreset from routing.DefaultPresetOpts() again.
func TestBodylessCompileFollowsTheProgrammesTrackRolesLikeApply(t *testing.T) {
	s, h, _, sign := managerServer(t, defaultTools())
	annotateMusicOnTrack2(t, h, sign, s.mgr.Default().SourceID())

	raw := send(t, h, sign, http.MethodPost, "/api/v1/routing/presets/"+routing.PresetNoMusic, nil, http.StatusOK)
	var out struct {
		Profile routing.Profile `json:"profile"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	got := enabledIn(out.Profile)
	if got[2] || !got[0] {
		t.Fatalf("a bodyless No music kept tracks %v; the music role is on track 2, so 2 must be out and 0 in", got)
	}
}

// GET /routing/presets?source= additionally serves the defaults derived from
// that programme's roles, so a client need not carry its own role table. The
// existing "defaults" field is unchanged.
func TestPresetCatalogueServesRoleDerivedDefaults(t *testing.T) {
	s, h, _, sign := managerServer(t, defaultTools())
	src := s.mgr.Default().SourceID()
	annotateMusicOnTrack2(t, h, sign, src)

	var out struct {
		Defaults     routing.PresetOpts  `json:"defaults"`
		RoleDefaults *routing.PresetOpts `json:"roleDefaults"`
	}
	raw := send(t, h, sign, http.MethodGet, "/api/v1/routing/presets?source="+itoa(src), nil, http.StatusOK)
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Defaults != routing.DefaultPresetOpts() {
		t.Errorf("defaults changed: %+v", out.Defaults)
	}
	if out.RoleDefaults == nil || out.RoleDefaults.MusicTrack != 2 {
		t.Fatalf("roleDefaults = %+v, want musicTrack 2", out.RoleDefaults)
	}
	// Unnamed on a single-source install is the one programme.
	raw = send(t, h, sign, http.MethodGet, "/api/v1/routing/presets", nil, http.StatusOK)
	if err := json.Unmarshal(raw, &out); err != nil || out.RoleDefaults == nil {
		t.Fatalf("unnamed single-source GET lost roleDefaults: %v %s", err, raw)
	}
}
