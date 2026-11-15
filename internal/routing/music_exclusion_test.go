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
			Name         string      `json:"name"`
			ExcludeRoles []TrackRole `json:"excludeRoles"`
			Tracks       []struct {
				Track  int       `json:"track"`
				Role   TrackRole `json:"role"`
				Routed bool      `json:"routed"`
			} `json:"tracks"`
			ExcludedMusicTracks []int `json:"excludedMusicTracks"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Cases) < 5 {
		t.Fatalf("fixture parsed to %d cases; it has rotted", len(fx.Cases))
	}
	for _, c := range fx.Cases {
		t.Run(c.Name, func(t *testing.T) {
			var src Source
			var cells []Cell
			for _, tr := range c.Tracks {
				src.Tracks = append(src.Tracks, Track{Index: tr.Track, Channels: 2})
				src.Annotations = append(src.Annotations, TrackAnnotation{Track: tr.Track, Role: tr.Role})
				if tr.Routed {
					cells = append(cells, Cell{Track: tr.Track, Channel: 0, Out: OutL, Gain: 1})
				}
			}
			p := Profile{ExcludeRoles: c.ExcludeRoles}

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

			// The compiler agrees: no routed music track survives applyRolePolicy
			// when the music role is excluded.
			out, _ := applyRolePolicy(p, src, cells)
			for _, cell := range out {
				for _, m := range want {
					if cell.Track == m {
						t.Errorf("track %d is excluded but its cell was kept", m)
					}
				}
			}
		})
	}
}
