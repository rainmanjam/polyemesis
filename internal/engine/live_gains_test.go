package engine

import (
	"errors"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/routing"
)

// A two-track graph in the shape routing now emits: a named volume stage per
// track, after its pan. Only the levels vary between the variants below.
func liveGraph(trk0, trk1 string) routing.Result {
	return routing.Result{
		OutLabel: "[aout]",
		FilterComplex: "[0:a:0]pan=stereo|c0=1*c0|c1=1*c1,volume@trk0=volume=" + trk0 +
			",aresample=async=1:first_pts=0[a_t0];" +
			"[0:a:1]pan=stereo|c0=1*c0|c1=1*c1,volume@trk1=volume=" + trk1 +
			",aresample=async=1:first_pts=0[a_t1];" +
			"[a_t0][a_t1]amix=inputs=2:duration=longest:normalize=0[a_mix];" +
			"[a_mix]aresample=48000:async=1[aout]",
	}
}

// THE property that makes a gain edit live: it must not move the restart
// signature. If it did, the reconcile would restart the destination rather
// than send the level.
func TestGainAndMuteDoNotMoveTheRestartSignature(t *testing.T) {
	row := backupRow()
	before := destSpec(row, liveGraph("0.4", "0.5"), "")
	after := destSpec(row, liveGraph("0.9", "0"), "")
	if before != after {
		t.Errorf("a level-only change moved the restart signature; the destination would restart instead of taking the level live")
	}
}

// The other side of the same rule: a change that is not a level must restart.
func TestAStructuralChangeStillMovesTheSignature(t *testing.T) {
	row := backupRow()
	a := liveGraph("0.5", "0.5")
	b := liveGraph("0.5", "0.5")
	b.FilterComplex = strings.Replace(b.FilterComplex, "amix=inputs=2", "amix=inputs=2:weights=1 0.5", 1)
	if destSpec(row, a, "") == destSpec(row, b, "") {
		t.Error("a change to the mix itself did not move the signature; it would be applied live when it must restart")
	}
}

func TestLevelsDiffer(t *testing.T) {
	base := liveGraph("0.4", "0.5").FilterComplex
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"identical", base, base, false},
		{"one level moved", base, liveGraph("0.4", "0").FilterComplex, true},
		{"a level moved back", liveGraph("0.4", "0").FilterComplex, base, true},
		{"fewer named stages", base, "[0:a:0]anull[aout]", true},
	}
	for _, tc := range cases {
		if got := levelsDiffer(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: levelsDiffer = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// fakeSetter records commands in order and can refuse one of them.
type fakeSetter struct {
	calls []string
	fail  string
}

func (f *fakeSetter) SetVolume(instance string, level float64) error {
	f.calls = append(f.calls, instance)
	if instance == f.fail {
		return errors.New("ret -78")
	}
	return nil
}

// Only the levels that moved are sent, and the refusal of one stops the run
// with an error so the caller restarts.
func TestApplyLiveLevelsSendsOnlyWhatMoved(t *testing.T) {
	f := &fakeSetter{}
	if err := applyLiveLevels(f, liveGraph("0.4", "0.5").FilterComplex, liveGraph("0.4", "0").FilterComplex); err != nil {
		t.Fatalf("applyLiveLevels: %v", err)
	}
	if len(f.calls) != 1 || f.calls[0] != "volume@trk1" {
		t.Errorf("sent %v; only volume@trk1 moved", f.calls)
	}

	f = &fakeSetter{fail: "volume@trk0"}
	err := applyLiveLevels(f, liveGraph("0.4", "0.5").FilterComplex, liveGraph("0.9", "0").FilterComplex)
	if err == nil {
		t.Fatal("a refused command was reported as applied")
	}
	if len(f.calls) != 1 {
		t.Errorf("kept sending after a refusal: %v", f.calls)
	}
}

// THE respawn guarantee: after a live mute the holder carries the new graph,
// so the next spawn of the process is built with the mute in force.
func TestRespawnBuildsTheLevelLastApplied(t *testing.T) {
	row := backupRow()
	g := newGraphHolder(liveGraph("0.4", "0.5"))
	g.set(liveGraph("0.4", "0"))
	e := &Engine{log: testLogger()}
	argv := strings.Join(e.destArgs(row, g.get(), "udp://127.0.0.1:1", row.Target()), " ")
	if !strings.Contains(argv, "volume@trk1=volume=0") {
		t.Errorf("a respawn after a live mute would bring the old level back; argv: %s", argv)
	}
	if strings.Contains(argv, "volume@trk1=volume=0.5") {
		t.Errorf("the pre-mute level is still in the respawn argv: %s", argv)
	}
}
