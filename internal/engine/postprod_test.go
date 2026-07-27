package engine

import (
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/stats"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// These two predicates are the only thing standing between a live broadcast and
// a transcode, so both are pinned as decisions rather than left to be inferred
// from a running server.

func TestIngestLiveFollowsBytesArrivingNotProcessState(t *testing.T) {
	now := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	sample := func(ago time.Duration, kbps float64) stats.Sample {
		return stats.Sample{Time: now.Add(-ago), Kbps: kbps}
	}

	tests := []struct {
		name    string
		samples []stats.Sample
		want    bool
	}{
		{
			name: "no samples yet is not live, so a fresh server does not block its own queue forever",
			want: false,
		},
		{
			name:    "a stream delivering right now is live",
			samples: []stats.Sample{sample(4*time.Second, 4000), sample(0, 4200)},
			want:    true,
		},
		{
			name:    "a listener waiting for a publisher delivers nothing and is not live",
			samples: []stats.Sample{sample(2*time.Second, 0), sample(0, 0)},
			want:    false,
		},
		{
			name:    "a stale sample is not live even though it carried bytes",
			samples: []stats.Sample{sample(30*time.Second, 5000)},
			want:    false,
		},
		{
			name:    "one late tick inside the grace window does not flap to not-live",
			samples: []stats.Sample{sample(ingestLiveGrace-time.Millisecond, 5000)},
			want:    true,
		},
		{
			name:    "the newest sample decides, not the busiest one",
			samples: []stats.Sample{sample(2*time.Second, 9000), sample(0, 0)},
			want:    false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ingestLive(tc.samples, now); got != tc.want {
				t.Errorf("ingestLive() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGPUBusyOnlyCountsAHardwareEncoderThatIsActuallyRunning(t *testing.T) {
	running := &supervisor.Status{State: supervisor.StateRunning}
	stopped := &supervisor.Status{State: supervisor.StateStopped}

	tests := []struct {
		name string
		in   []RenditionStatus
		want bool
	}{
		{name: "no renditions at all", want: false},
		{
			name: "software encoding is not GPU contention",
			in:   []RenditionStatus{{Encoder: db.EncoderX264, Process: running}},
			want: false,
		},
		{
			name: "libx265 is software too, however slow it is",
			in:   []RenditionStatus{{Encoder: db.EncoderX265, Process: running}},
			want: false,
		},
		{
			name: "a running hardware encoder is the case this exists for",
			in:   []RenditionStatus{{Encoder: db.EncoderNVENCH264, Process: running}},
			want: true,
		},
		{
			name: "a configured but not running hardware rendition holds nothing",
			in:   []RenditionStatus{{Encoder: db.EncoderNVENCH264, Process: stopped}},
			want: false,
		},
		{
			name: "a rendition with no process at all cannot be busy",
			in:   []RenditionStatus{{Encoder: db.EncoderVAAPIHEVC}},
			want: false,
		},
		{
			name: "one hardware rendition among software ones is enough",
			in: []RenditionStatus{
				{Encoder: db.EncoderX264, Process: running},
				{Encoder: db.EncoderQSVH264, Process: running},
			},
			want: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := gpuBusy(tc.in); got != tc.want {
				t.Errorf("gpuBusy() = %v, want %v", got, tc.want)
			}
		})
	}
}
