package meters

import (
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/routing"
)

func TestTargetForPrefersTheProfileOverThePlatformTable(t *testing.T) {
	tests := []struct {
		name       string
		loudness   *routing.Loudness
		platform   routing.Platform
		wantSource TargetSource
		wantLUFS   float64
		wantPeak   float64
	}{
		{
			name:       "an explicit target wins over what the platform assumes",
			loudness:   &routing.Loudness{TargetLUFS: -16, TruePeakDB: -2},
			platform:   routing.PlatformYouTube,
			wantSource: TargetProfile, wantLUFS: -16, wantPeak: -2,
		},
		{
			name:       "a profile target with no ceiling takes routing's default",
			loudness:   &routing.Loudness{TargetLUFS: -23},
			platform:   routing.PlatformCustom,
			wantSource: TargetProfile, wantLUFS: -23, wantPeak: routing.DefaultTruePeakDB,
		},
		{
			name:       "no profile target falls back to what the platform normalizes to",
			platform:   routing.PlatformTwitch,
			wantSource: TargetPlatform, wantLUFS: routing.LUFSStreaming, wantPeak: routing.DefaultTruePeakDB,
		},
		{
			name:       "a platform with no opinion gets no target invented for it",
			platform:   routing.PlatformCustom,
			wantSource: TargetNone,
		},
		{
			name:       "a local recording is not a delivery and is not judged",
			platform:   routing.PlatformFile,
			wantSource: TargetNone,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := TargetFor(tc.loudness, tc.platform)
			if got.Source != tc.wantSource {
				t.Fatalf("source = %q, want %q", got.Source, tc.wantSource)
			}
			if got.LUFS != tc.wantLUFS || got.TruePeakDBTP != tc.wantPeak {
				t.Fatalf("target = %+v, want %v LUFS / %v dBTP", got, tc.wantLUFS, tc.wantPeak)
			}
			if got.Reason == "" {
				t.Fatal("every target must be able to explain where it came from")
			}
		})
	}
}

func TestTargetSigMovesOnlyWhenTheTargetDoes(t *testing.T) {
	a := TargetFor(&routing.Loudness{TargetLUFS: -14}, routing.PlatformYouTube)
	b := TargetFor(&routing.Loudness{TargetLUFS: -14}, routing.PlatformTwitch)
	c := TargetFor(&routing.Loudness{TargetLUFS: -16}, routing.PlatformYouTube)

	if a.Sig() != b.Sig() {
		t.Fatalf("the same explicit target must hash the same on any platform: %q vs %q", a.Sig(), b.Sig())
	}
	if a.Sig() == c.Sig() {
		t.Fatal("changing the target must change the signature, or the analyser never restarts")
	}
}

func TestEvaluateVerdicts(t *testing.T) {
	target := Target{LUFS: -14, TruePeakDBTP: -1, ToleranceLU: ToleranceLU, Source: TargetProfile}
	settled := func(f Frame) Frame {
		f.Seconds = MinIntegrationSeconds + 10
		f.Integrated = true
		if f.TruePeakDBTP == 0 {
			f.TruePeakDBTP = -6
		}
		return f
	}

	tests := []struct {
		name  string
		frame Frame
		want  Verdict
	}{
		{
			name:  "on target is a pass",
			frame: settled(Frame{IntegratedLUFS: -14.2}),
			want:  VerdictPass,
		},
		{
			name:  "exactly at the tolerance edge is still a pass",
			frame: settled(Frame{IntegratedLUFS: -15}),
			want:  VerdictPass,
		},
		{
			name:  "past tolerance but inside the warn band is a warning",
			frame: settled(Frame{IntegratedLUFS: -15.6}),
			want:  VerdictWarn,
		},
		{
			name:  "well over target is a failure",
			frame: settled(Frame{IntegratedLUFS: -10.5}),
			want:  VerdictFail,
		},
		{
			name:  "a true peak just over the ceiling warns even when the loudness passes",
			frame: settled(Frame{IntegratedLUFS: -14, TruePeakDBTP: -0.5}),
			want:  VerdictWarn,
		},
		{
			name:  "a true peak a decibel over the ceiling fails",
			frame: settled(Frame{IntegratedLUFS: -14, TruePeakDBTP: 0.5}),
			want:  VerdictFail,
		},
		{
			name:  "the first seconds of a stream are not a verdict",
			frame: Frame{Seconds: 3, Integrated: true, IntegratedLUFS: -30, TruePeakDBTP: -20},
			want:  VerdictUnknown,
		},
		{
			name:  "a programme still under the gate is not a verdict",
			frame: Frame{Seconds: 120, Integrated: false, IntegratedLUFS: LUFSFloor, TruePeakDBTP: -60},
			want:  VerdictUnknown,
		},
		{
			name:  "a clipping mix is reported before the integration window fills",
			frame: Frame{Seconds: 2, Integrated: false, IntegratedLUFS: LUFSFloor, TruePeakDBTP: 2},
			want:  VerdictFail,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _, reason := target.Evaluate(tc.frame)
			if got != tc.want {
				t.Fatalf("verdict = %q, want %q (%s)", got, tc.want, reason)
			}
			if reason == "" {
				t.Fatal("a verdict with no reason is a badge nobody can act on")
			}
		})
	}
}

func TestEvaluateWithNoTargetJudgesNothing(t *testing.T) {
	v, dev, reason := Target{Source: TargetNone}.Evaluate(Frame{
		Seconds: 600, Integrated: true, IntegratedLUFS: -3, TruePeakDBTP: 5,
	})
	if v != VerdictUnknown || dev != 0 {
		t.Fatalf("verdict = %q dev = %v; an unconfigured destination must not be failed", v, dev)
	}
	if reason == "" {
		t.Fatal("the absence of a target has to be stated, not left blank")
	}
}

func TestEvaluateReportsDeviationSigned(t *testing.T) {
	target := Target{LUFS: -14, TruePeakDBTP: -1, Source: TargetProfile}
	_, over, _ := target.Evaluate(Frame{Seconds: 60, Integrated: true, IntegratedLUFS: -11, TruePeakDBTP: -6})
	_, under, _ := target.Evaluate(Frame{Seconds: 60, Integrated: true, IntegratedLUFS: -18, TruePeakDBTP: -6})
	if over <= 0 || under >= 0 {
		t.Fatalf("deviation must keep its sign: over=%v under=%v", over, under)
	}
}

func TestStoreKeepsTheLatestReportPerDestination(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.Put(Observe(2, "twitch", Target{Source: TargetNone}, Frame{}, now))
	s.Put(Observe(1, "youtube", Target{Source: TargetNone}, Frame{IntegratedLUFS: -14}, now))
	s.Put(Observe(1, "youtube", Target{Source: TargetNone}, Frame{IntegratedLUFS: -13}, now))

	all := s.All()
	if len(all) != 2 {
		t.Fatalf("want 2 reports, got %d", len(all))
	}
	t.Run("ordered by destination so the dashboard does not reshuffle", func(t *testing.T) {
		if all[0].DestinationID != 1 || all[1].DestinationID != 2 {
			t.Fatalf("order = %d,%d", all[0].DestinationID, all[1].DestinationID)
		}
	})
	t.Run("the newer report replaces the older", func(t *testing.T) {
		if all[0].IntegratedLUFS != -13 {
			t.Fatalf("integrated = %v, want the latest -13", all[0].IntegratedLUFS)
		}
	})
	t.Run("a destination that is gone stops being reported", func(t *testing.T) {
		s.Keep(map[int64]bool{2: true})
		if got := s.All(); len(got) != 1 || got[0].DestinationID != 2 {
			t.Fatalf("after Keep: %+v", got)
		}
	})
}

func TestFailedReportNeverReadsAsCompliant(t *testing.T) {
	r := Failed(3, "kick", Target{LUFS: -14, Source: TargetPlatform}, "no relay port", time.Now())
	if r.Verdict == VerdictPass {
		t.Fatal("a meter that could not start must not report a pass")
	}
	if r.Error == "" {
		t.Fatal("the reason the meter is missing has to reach the UI")
	}
}
