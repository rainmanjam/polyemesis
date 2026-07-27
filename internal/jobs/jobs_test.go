package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestNormalizedFillsDefaultsAndClamps(t *testing.T) {
	tests := []struct {
		name string
		in   Job
		want func(Job) error
	}{
		{
			name: "a bare job becomes a queued job with an attempt budget",
			in:   Job{Kind: "transcribe"},
			want: func(j Job) error {
				if j.State != StateQueued {
					return fmt.Errorf("state = %q, want queued", j.State)
				}
				if j.MaxAttempts != DefaultMaxAttempts {
					return fmt.Errorf("maxAttempts = %d, want %d", j.MaxAttempts, DefaultMaxAttempts)
				}
				if string(j.Params) != "{}" {
					return fmt.Errorf("params = %q, want an empty object", j.Params)
				}
				return nil
			},
		},
		{
			name: "priority is clamped rather than rejected",
			in:   Job{Kind: "k", Priority: 9999},
			want: func(j Job) error {
				if j.Priority != MaxPriority {
					return fmt.Errorf("priority = %d, want %d", j.Priority, MaxPriority)
				}
				return nil
			},
		},
		{
			name: "an attempt ceiling above the cap is clamped down",
			in:   Job{Kind: "k", MaxAttempts: 1000},
			want: func(j Job) error {
				if j.MaxAttempts != MaxMaxAttempts {
					return fmt.Errorf("maxAttempts = %d, want %d", j.MaxAttempts, MaxMaxAttempts)
				}
				return nil
			},
		},
		{
			name: "progress above one is clamped",
			in:   Job{Kind: "k", Progress: 12},
			want: func(j Job) error {
				if j.Progress != 1 {
					return fmt.Errorf("progress = %v, want 1", j.Progress)
				}
				return nil
			},
		},
		{
			name: "negative progress is clamped to zero",
			in:   Job{Kind: "k", Progress: -4},
			want: func(j Job) error {
				if j.Progress != 0 {
					return fmt.Errorf("progress = %v, want 0", j.Progress)
				}
				return nil
			},
		},
		{
			name: "kind and target are trimmed",
			in:   Job{Kind: "  proxy  ", Target: "  recording:7 "},
			want: func(j Job) error {
				if j.Kind != "proxy" || j.Target != "recording:7" {
					return fmt.Errorf("kind/target = %q/%q, want them trimmed", j.Kind, j.Target)
				}
				return nil
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.want(tc.in.Normalized()); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestValidateRejectsWhatMustNotReachTheDatabase(t *testing.T) {
	tests := []struct {
		name    string
		mut     func(*Job)
		wantErr bool
	}{
		{name: "a normal job is accepted", mut: func(*Job) {}},
		{name: "no kind", mut: func(j *Job) { j.Kind = "" }, wantErr: true},
		{name: "kind too long", mut: func(j *Job) { j.Kind = Kind(strings.Repeat("x", MaxKindLen+1)) }, wantErr: true},
		{name: "target too long", mut: func(j *Job) { j.Target = strings.Repeat("x", MaxTargetLen+1) }, wantErr: true},
		{name: "unknown state", mut: func(j *Job) { j.State = "wandering" }, wantErr: true},
		{name: "params are not JSON", mut: func(j *Job) { j.Params = json.RawMessage("not json") }, wantErr: true},
		{name: "result is not JSON", mut: func(j *Job) { j.Result = json.RawMessage("{oops") }, wantErr: true},
		{
			name:    "params larger than the cap",
			mut:     func(j *Job) { j.Params = json.RawMessage(`"` + strings.Repeat("x", MaxParamsBytes) + `"`) },
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			j := Job{Kind: "transcribe", Target: "recording:1"}.Normalized()
			tc.mut(&j)
			err := j.Validate()
			if tc.wantErr && err == nil {
				t.Fatal("Validate accepted a job it must reject")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

func TestUnclassifiedFailuresAreRetryable(t *testing.T) {
	// The direction of this default is the whole point: a check that is wrong
	// in the restrictive direction throws away work that would have succeeded,
	// and the attempt ceiling already bounds the generous direction.
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "a bare error is retryable", err: errors.New("ffmpeg exited 1")},
		{name: "a wrapped bare error is retryable", err: fmt.Errorf("run: %w", errors.New("busy"))},
		{name: "nil is not permanent", err: nil},
		{name: "an error marked permanent", err: Permanent(errors.New("input file is gone")), want: true},
		{
			name: "a permanent error stays permanent through wrapping",
			err:  fmt.Errorf("transcribe: %w", Permanent(errors.New("unsupported codec"))),
			want: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsPermanent(tc.err); got != tc.want {
				t.Errorf("IsPermanent = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPermanentPreservesTheUnderlyingError(t *testing.T) {
	base := errors.New("file is gone")
	err := Permanent(base)
	if !errors.Is(err, base) {
		t.Error("Permanent lost the error it wrapped; a caller cannot match on it")
	}
	if err.Error() != base.Error() {
		t.Errorf("Error() = %q, want the underlying message", err.Error())
	}
	if Permanent(nil) != nil {
		t.Error("Permanent(nil) must stay nil")
	}
}

func TestTrimLogKeepsTheNewestLinesBounded(t *testing.T) {
	lines := make([]string, MaxLogLines+50)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i)
	}
	got := TrimLog(lines)
	if len(got) != MaxLogLines {
		t.Fatalf("kept %d lines, want %d", len(got), MaxLogLines)
	}
	if got[len(got)-1] != lines[len(lines)-1] {
		t.Errorf("last line = %q, want the newest one kept", got[len(got)-1])
	}

	long := TrimLog([]string{strings.Repeat("x", MaxLogLineLen*2)})
	if len([]rune(long[0])) > MaxLogLineLen+1 {
		t.Errorf("a long line was kept at %d characters, want it truncated", len([]rune(long[0])))
	}
	if TrimLog(nil) != nil {
		t.Error("TrimLog(nil) must stay nil")
	}
}

func TestRecordingTargetRoundTrips(t *testing.T) {
	tests := []struct {
		name   string
		target string
		want   int64
		ok     bool
	}{
		{name: "a recording target", target: RecordingTarget(42), want: 42, ok: true},
		{name: "something else entirely", target: "clip:9"},
		{name: "no id", target: "recording:"},
		{name: "not a number", target: "recording:abc"},
		{name: "zero is not a recording", target: "recording:0"},
		{name: "empty", target: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseRecordingTarget(tc.target)
			if ok != tc.ok || got != tc.want {
				t.Errorf("ParseRecordingTarget(%q) = %d,%v want %d,%v", tc.target, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestStateTerminalMarksOnlyTheFinishedStates(t *testing.T) {
	tests := []struct {
		state State
		want  bool
	}{
		{state: StateQueued},
		{state: StateRunning},
		{state: StateDeferred},
		{state: StateDone, want: true},
		{state: StateFailed, want: true},
		{state: StateCancelled, want: true},
	}
	for _, tc := range tests {
		t.Run(string(tc.state), func(t *testing.T) {
			if got := tc.state.Terminal(); got != tc.want {
				t.Errorf("%q.Terminal() = %v, want %v", tc.state, got, tc.want)
			}
			if !tc.state.Valid() {
				t.Errorf("%q must be a valid state", tc.state)
			}
		})
	}
	if State("wandering").Valid() {
		t.Error("an unknown state must not validate")
	}
}

func TestExhaustedComparesStartsAgainstTheCeiling(t *testing.T) {
	tests := []struct {
		name     string
		attempts int
		max      int
		want     bool
	}{
		{name: "not started", attempts: 0, max: 3},
		{name: "part way", attempts: 2, max: 3},
		{name: "at the ceiling", attempts: 3, max: 3, want: true},
		{name: "past it, as a crash recovery can leave it", attempts: 4, max: 3, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			j := Job{Attempts: tc.attempts, MaxAttempts: tc.max}
			if got := j.Exhausted(); got != tc.want {
				t.Errorf("Exhausted() = %v, want %v", got, tc.want)
			}
		})
	}
}
