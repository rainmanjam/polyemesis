package routing

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"
)

// The console decides whether a destination's "music excluded" badge is true
// with excludedMusicTracks in ui/src/lib/musicExclusion.ts. This test runs the
// server's real policy (Profile.ExcludedTracks, applyRolePolicy) over the same
// cases as ui/src/lib/musicExclusion.test.ts, so the two rules cannot drift
// unseen. Change a case in testdata/music-exclusion-cases.json and both sides
// must agree with it.
func TestMusicExclusionCasesMatchTheConsole(t *testing.T) {
	raw, err := os.ReadFile("testdata/music-exclusion-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Cases []struct {
			Name          string      `json:"name"`
			ExcludeRoles  []TrackRole `json:"excludeRoles"`
			ServerRefuses bool        `json:"serverRefuses"`
			Tracks        []struct {
				Track  int       `json:"track"`
				Role   TrackRole `json:"role"`
				Routed bool      `json:"routed"`
				// Carried defaults to true: false means the ingest does not
				// carry the track although it is annotated.
				Carried *bool `json:"carried"`
			} `json:"tracks"`
			ExcludedMusicTracks []int `json:"excludedMusicTracks"`
			KeptRouted          []int `json:"keptRouted"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Cases) < 10 {
		t.Fatalf("fixture parsed to %d cases; it has rotted", len(fx.Cases))
	}
	for _, c := range fx.Cases {
		t.Run(c.Name, func(t *testing.T) {
			var src Source
			var cells []Cell
			for _, tr := range c.Tracks {
				if tr.Carried == nil || *tr.Carried {
					src.Tracks = append(src.Tracks, Track{Index: tr.Track, Channels: 2})
				}
				src.Annotations = append(src.Annotations, TrackAnnotation{Track: tr.Track, Role: tr.Role})
				if tr.Routed {
					cells = append(cells, Cell{Track: tr.Track, Channel: 0, Out: OutL, Gain: 1})
				}
			}
			p := Profile{ExcludeRoles: c.ExcludeRoles}

			if c.ServerRefuses {
				if err := ValidateAnnotations(src.Annotations); err == nil {
					t.Error("the server accepted annotations the fixture says it refuses")
				}
				return
			}

			got := []int{}
			for _, tr := range p.ExcludedTracks(src) {
				if src.RoleOf(tr) == RoleMusic {
					got = append(got, tr)
				}
			}
			want := append([]int{}, c.ExcludedMusicTracks...)
			sort.Ints(want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("ExcludedTracks music = %v, want %v", got, want)
			}

			// The compiler agrees, both ways: the routed cells that survive are
			// exactly the routed tracks the fixture says are kept, so a policy
			// that dropped too much fails as surely as one that dropped too little.
			out, _ := applyRolePolicy(p, src, cells)
			kept := []int{}
			seen := map[int]bool{}
			for _, cell := range out {
				if !seen[cell.Track] {
					seen[cell.Track] = true
					kept = append(kept, cell.Track)
				}
			}
			sort.Ints(kept)
			wantKept := append([]int{}, c.KeptRouted...)
			sort.Ints(wantKept)
			if !reflect.DeepEqual(kept, wantKept) {
				t.Errorf("tracks kept after applyRolePolicy = %v, want %v", kept, wantKept)
			}
		})
	}
}
