package api

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/jobs"
	"github.com/rainmanjam/polyemesis/internal/media"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// The reason string is the whole point of the jobs page: a queued job with no
// explanation reads as a broken job. These pin the sentences, not just the
// booleans, because the sentence is what the operator acts on.

func TestBlockReasonExplainsWhyAJobIsNotRunning(t *testing.T) {
	now := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)

	governed := func(kind jobs.Kind, reason string, gates jobs.Gates) jobs.Snapshot {
		return jobs.Snapshot{
			At:       now,
			Enabled:  true,
			Gates:    gates,
			Verdicts: []jobs.Verdict{{Kind: kind, Start: false, Reason: reason}},
		}
	}

	tests := []struct {
		name       string
		job        jobs.Job
		snap       jobs.Snapshot
		paused     bool
		slotsFree  bool
		wantBlock  bool
		wantReason string
	}{
		{
			name:      "a running job is never explained away",
			job:       jobs.Job{Kind: media.KindProxy, State: jobs.StateRunning},
			slotsFree: true,
		},
		{
			name: "a finished job is not explained either",
			job:  jobs.Job{Kind: media.KindProxy, State: jobs.StateDone},
		},
		{
			name:       "the queue-wide pause beats every other explanation",
			job:        jobs.Job{Kind: media.KindProxy, State: jobs.StateQueued},
			snap:       governed(media.KindProxy, jobs.ReasonIngestLive, jobs.Gates{IngestLive: true}),
			paused:     true,
			slotsFree:  true,
			wantBlock:  true,
			wantReason: "the queue is paused",
		},
		{
			name:       "a live ingest is named as the cause, not the deferral it produced",
			job:        jobs.Job{Kind: media.KindProxy, State: jobs.StateDeferred, AvailableAt: now.Add(24 * time.Second)},
			snap:       governed(media.KindProxy, jobs.ReasonIngestLive, jobs.Gates{IngestLive: true, CPUPercent: -1}),
			slotsFree:  true,
			wantBlock:  true,
			wantReason: jobs.ReasonIngestLive,
		},
		{
			name: "a cpu gate carries the measurement that justifies it",
			job:  jobs.Job{Kind: transcribe.KindTranscribe, State: jobs.StateDeferred},
			snap: governed(transcribe.KindTranscribe, jobs.ReasonCPUBusy,
				jobs.Gates{CPUPercent: 94.4, CPUOverCeiling: true}),
			slotsFree:  true,
			wantBlock:  true,
			wantReason: "host cpu is above the ceiling (94%)",
		},
		{
			name: "an unreadable cpu is not reported as a load of nothing",
			job:  jobs.Job{Kind: transcribe.KindTranscribe, State: jobs.StateDeferred},
			snap: governed(transcribe.KindTranscribe, jobs.ReasonCPUBusy,
				jobs.Gates{CPUPercent: -1, CPUOverCeiling: true}),
			slotsFree:  true,
			wantBlock:  true,
			wantReason: jobs.ReasonCPUBusy,
		},
		{
			name: "battery level is quoted when the platform knew it",
			job:  jobs.Job{Kind: media.KindArchive, State: jobs.StateDeferred},
			snap: governed(media.KindArchive, jobs.ReasonOnBattery, jobs.Gates{
				CPUPercent: -1,
				OnBattery:  true,
				Power:      jobs.PowerState{Known: true, OnBattery: true, Percent: 31, TempC: -1},
			}),
			slotsFree:  true,
			wantBlock:  true,
			wantReason: "the machine is on battery (31%)",
		},
		{
			name:       "a verdict that allows the kind leaves retry backoff to explain itself",
			job:        jobs.Job{Kind: media.KindProxy, State: jobs.StateQueued, Attempts: 2, MaxAttempts: 3, AvailableAt: now.Add(30 * time.Second)},
			slotsFree:  true,
			wantBlock:  true,
			wantReason: "retrying in 30s after attempt 2 of 3",
		},
		{
			name:       "work that has never started and is parked is not called a retry",
			job:        jobs.Job{Kind: media.KindProxy, State: jobs.StateDeferred, AvailableAt: now.Add(45 * time.Second)},
			slotsFree:  true,
			wantBlock:  true,
			wantReason: "held back for another 45s",
		},
		{
			name:       "a full queue says so rather than saying nothing",
			job:        jobs.Job{Kind: media.KindProxy, State: jobs.StateQueued},
			slotsFree:  false,
			wantBlock:  true,
			wantReason: "waiting for a free slot",
		},
		{
			name:      "next in line is not explained, because that is noise",
			job:       jobs.Job{Kind: media.KindProxy, State: jobs.StateQueued},
			slotsFree: true,
		},
		{
			name: "a disabled governor's stale verdicts are not quoted at the operator",
			job:  jobs.Job{Kind: media.KindProxy, State: jobs.StateQueued},
			snap: jobs.Snapshot{
				At:       now,
				Enabled:  false,
				Verdicts: []jobs.Verdict{{Kind: media.KindProxy, Start: false, Reason: jobs.ReasonIngestLive}},
			},
			slotsFree: true,
		},
		{
			name:      "a verdict about a different kind does not block this one",
			job:       jobs.Job{Kind: media.KindProxy, State: jobs.StateQueued},
			snap:      governed(transcribe.KindTranscribe, jobs.ReasonCPUBusy, jobs.Gates{CPUPercent: -1}),
			slotsFree: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocked, reason := blockReason(tt.job, tt.snap, tt.paused, tt.slotsFree, now)
			if blocked != tt.wantBlock {
				t.Errorf("blocked = %v, want %v (reason %q)", blocked, tt.wantBlock, reason)
			}
			if reason != tt.wantReason {
				t.Errorf("reason = %q, want %q", reason, tt.wantReason)
			}
		})
	}
}

func TestETAIsWithheldUntilTheExtrapolationIsWorthAnything(t *testing.T) {
	now := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)
	started := now.Add(-60 * time.Second)

	tests := []struct {
		name string
		job  jobs.Job
		want float64
	}{
		{
			name: "half done after a minute has a minute left",
			job:  jobs.Job{State: jobs.StateRunning, StartedAt: started, Progress: 0.5},
			want: 60,
		},
		{
			name: "a job that has barely begun reports nothing rather than nonsense",
			job:  jobs.Job{State: jobs.StateRunning, StartedAt: started, Progress: 0.01},
		},
		{
			name: "a queued job has no elapsed time to extrapolate from",
			job:  jobs.Job{State: jobs.StateQueued, Progress: 0.5},
		},
		{
			name: "a job with no start time is not guessed at",
			job:  jobs.Job{State: jobs.StateRunning, Progress: 0.5},
		},
		{
			name: "complete work has nothing remaining",
			job:  jobs.Job{State: jobs.StateRunning, StartedAt: started, Progress: 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := etaSeconds(tt.job, now); got != tt.want {
				t.Errorf("etaSeconds = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestJobFilterReadsTheQueryString(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		wantErr bool
		check   func(*testing.T, jobs.Filter)
	}{
		{
			name:  "an empty query means everything",
			query: "",
			check: func(t *testing.T, f jobs.Filter) {
				if len(f.States) != 0 || len(f.Kinds) != 0 || f.Target != "" || f.Limit != 0 {
					t.Errorf("expected a zero filter, got %+v", f)
				}
			},
		},
		{
			name:  "active expands to the queue's own definition",
			query: "state=active",
			check: func(t *testing.T, f jobs.Filter) {
				if len(f.States) != len(jobs.Active().States) {
					t.Errorf("states = %v, want %v", f.States, jobs.Active().States)
				}
			},
		},
		{
			name:  "states may be comma separated",
			query: "state=done,failed",
			check: func(t *testing.T, f jobs.Filter) {
				if len(f.States) != 2 || f.States[0] != jobs.StateDone || f.States[1] != jobs.StateFailed {
					t.Errorf("states = %v", f.States)
				}
			},
		},
		{
			name:  "a recording id is translated to the one canonical target spelling",
			query: "recordingId=42",
			check: func(t *testing.T, f jobs.Filter) {
				if f.Target != jobs.RecordingTarget(42) {
					t.Errorf("target = %q, want %q", f.Target, jobs.RecordingTarget(42))
				}
			},
		},
		{
			// A misspelt filter and an empty result look identical in a table,
			// so this must be an error rather than a silent nothing.
			name:    "an unknown state is refused rather than silently ignored",
			query:   "state=finished",
			wantErr: true,
		},
		{
			name:    "a negative limit is refused",
			query:   "limit=-1",
			wantErr: true,
		},
		{
			name:    "a non-numeric recording id is refused",
			query:   "recordingId=abc",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/jobs?"+tt.query, nil)
			f, err := jobFilter(r)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got filter %+v", f)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tt.check(t, f)
		})
	}
}

func TestKindInfoReportsTheEffectivePolicy(t *testing.T) {
	base := db.DefaultSettings().PostProd

	tests := []struct {
		name             string
		policy           db.PostProdSettings
		kind             jobs.Kind
		wantMode         string
		wantOverridden   bool
		wantAvailable    bool
		requireUnwhisper bool
	}{
		{
			name:          "a kind with no row of its own inherits the default",
			policy:        base,
			kind:          media.KindProxy,
			wantMode:      base.DefaultMode,
			wantAvailable: true,
		},
		{
			name: "a kind with a row reports its own mode and says it is overridden",
			policy: func() db.PostProdSettings {
				p := base
				p.Kinds = []db.PostProdKindSettings{{Kind: string(media.KindArchive), Mode: "scheduled"}}
				return p
			}(),
			kind:           media.KindArchive,
			wantMode:       "scheduled",
			wantOverridden: true,
			wantAvailable:  true,
		},
		{
			// Fails open everywhere except the one tool we can actually
			// detect: transcription without whisper.cpp.
			name:             "transcription is the only kind a missing tool can mark unavailable",
			policy:           base,
			kind:             transcribe.KindTranscribe,
			wantMode:         base.DefaultMode,
			wantAvailable:    false,
			requireUnwhisper: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A nil *transcribe.Tools is a machine without whisper.cpp, and
			// every method on it is nil-receiver safe for exactly this case.
			var whisper *transcribe.Tools
			if !tt.requireUnwhisper {
				whisper = &transcribe.Tools{Binary: "/usr/bin/whisper-cli"}
			}

			infos := kindInfo(tt.policy, whisper)
			var got *jobKindInfo
			for i := range infos {
				if infos[i].Kind == string(tt.kind) {
					got = &infos[i]
					break
				}
			}
			if got == nil {
				t.Fatalf("kind %q is missing from the catalogue", tt.kind)
			}
			if got.Mode != tt.wantMode {
				t.Errorf("mode = %q, want %q", got.Mode, tt.wantMode)
			}
			if got.Overridden != tt.wantOverridden {
				t.Errorf("overridden = %v, want %v", got.Overridden, tt.wantOverridden)
			}
			if got.Available != tt.wantAvailable {
				t.Errorf("available = %v, want %v (%q)", got.Available, tt.wantAvailable, got.Unavailable)
			}
			if !tt.wantAvailable && got.Unavailable == "" {
				t.Error("an unavailable kind must say why")
			}
			if got.Label == "" || got.Description == "" {
				t.Error("every catalogued kind needs a label and a description")
			}
		})
	}
}

// Every kind this API can submit must be catalogued, or the jobs page cannot
// offer a mode control for it and the governor's verdicts arrive unlabelled.
func TestEveryCataloguedKindHasAUniqueLabel(t *testing.T) {
	seen := map[jobs.Kind]bool{}
	for _, c := range kindCatalogue {
		if seen[c.Kind] {
			t.Errorf("kind %q is catalogued twice", c.Kind)
		}
		seen[c.Kind] = true
		if kindLabel(c.Kind) != c.Label {
			t.Errorf("kindLabel(%q) = %q, want %q", c.Kind, kindLabel(c.Kind), c.Label)
		}
	}
	// An uncatalogued kind falls back to its own name rather than to empty.
	if got := kindLabel("something.else"); got != "something.else" {
		t.Errorf("kindLabel of an unknown kind = %q, want the kind itself", got)
	}
}
