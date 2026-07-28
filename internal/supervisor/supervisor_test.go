// The process-lifecycle tests drive real children and probe whether their pids
// are still alive; both are platform-specific, and both live behind helpers in
// testfake_test.go so these bodies run unchanged on every platform we ship.
// The pure-logic tests (ring, classify, CommandString) live here too rather
// than in a second file, because splitting them buys nothing.

package supervisor

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// ------------------------------------------------------------------ restart

func TestAutoRestartRespawnsAfterNonZeroExit(t *testing.T) {
	rec := newRecorder()
	p := testProcess(t, fakeExit(1), Spec{
		AutoRestart: true,
		MinBackoff:  10 * time.Millisecond,
		MaxBackoff:  10 * time.Millisecond,
		OnState:     rec.onState,
	})
	p.Start()

	waitFor(t, "two restarts", func() bool { return p.Status().Restarts >= 2 })

	// Counting restarts is not enough: a supervisor that increments the
	// counter but never respawns would still pass that check.
	if got := rec.distinctPIDs(); got < 2 {
		t.Errorf("respawn must produce a new child; saw %d distinct pid(s)", got)
	}
	if !rec.saw(StateReconnecting) {
		t.Error("a restarting process must pass through reconnecting")
	}
	if got := p.Status().LastError; got == "" {
		t.Error("a non-zero exit must be reported as LastError")
	}
}

func TestNoAutoRestartIsTerminal(t *testing.T) {
	tests := []struct {
		name      string
		exitCode  int
		wantState State
		wantErr   bool
	}{
		{
			name:      "a failing child ends in failed and is not respawned",
			exitCode:  7,
			wantState: StateFailed,
			wantErr:   true,
		},
		{
			name:      "a child that exits cleanly ends in stopped",
			exitCode:  0,
			wantState: StateStopped,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := newRecorder()
			p := testProcess(t, fakeExit(tt.exitCode), Spec{
				AutoRestart: false,
				OnState:     rec.onState,
			})
			p.Start()

			waitFor(t, "terminal state", func() bool { return rec.saw(tt.wantState) })
			if !rec.saw(StateRunning) {
				t.Fatal("the child never reached running")
			}

			// Long enough for a backoff-free respawn loop to have gone round
			// several times if AutoRestart were being ignored.
			time.Sleep(100 * time.Millisecond)

			st := p.Status()
			if st.State != tt.wantState {
				t.Errorf("state = %q, want %q", st.State, tt.wantState)
			}
			if st.Restarts != 0 {
				t.Errorf("restarts = %d, want 0", st.Restarts)
			}
			if gotErr := st.LastError != ""; gotErr != tt.wantErr {
				t.Errorf("LastError = %q, want error: %v", st.LastError, tt.wantErr)
			}
			if st.PID != 0 {
				t.Errorf("PID = %d, want 0 once the child is gone", st.PID)
			}
		})
	}
}

func TestBackoffDoublesUpToTheCeiling(t *testing.T) {
	const (
		min = 20 * time.Millisecond
		max = 50 * time.Millisecond
	)
	// 20, 40, then clamped: 80 and 160 would both exceed the ceiling.
	want := []time.Duration{min, 2 * min, max, max}

	var mu sync.Mutex
	var got []time.Duration
	enough := make(chan struct{})

	p := testProcess(t, fakeExit(1), Spec{
		AutoRestart: true,
		MinBackoff:  min,
		MaxBackoff:  max,
		OnState: func(st Status) {
			if st.State != StateReconnecting {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if len(got) < len(want) {
				got = append(got, time.Duration(st.NextRetryIn*float64(time.Second)))
				if len(got) == len(want) {
					close(enough)
				}
			}
		},
	})
	p.Start()

	select {
	case <-enough:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out collecting retry delays")
	}

	mu.Lock()
	defer mu.Unlock()
	for i, w := range want {
		// NextRetryIn is computed a hair after the deadline is set, so it can
		// only ever read slightly short of the true backoff.
		if got[i] > w || got[i] < w-10*time.Millisecond {
			t.Errorf("retry %d delayed %v, want ~%v (sequence %v)", i+1, got[i], w, got)
		}
	}
	if got[1] <= got[0] {
		t.Errorf("backoff must grow between retries: %v", got)
	}
}

// --------------------------------------------------------------------- stop

func TestStopKillsTheChildAndReportsStopped(t *testing.T) {
	p := testProcess(t, fakeSleep(30*time.Second), Spec{
		AutoRestart: true,
		MinBackoff:  10 * time.Millisecond,
	})
	p.Start()

	waitFor(t, "child to start", func() bool { return p.Status().State == StateRunning })
	pid := p.Status().PID
	if pid == 0 {
		t.Fatal("a running process must report its pid")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		p.Stop(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("Stop did not return")
	}

	if st := p.Status(); st.State != StateStopped {
		t.Errorf("state = %q, want %q", st.State, StateStopped)
	}
	// Stop must reap the child, not merely stop watching it: a leaked FFmpeg
	// keeps holding the capture device and the destination socket.
	if alive(pid) {
		t.Errorf("pid %d survived Stop", pid)
	}
	// AutoRestart must not resurrect a deliberately stopped process.
	time.Sleep(100 * time.Millisecond)
	if st := p.Status(); st.State != StateStopped {
		t.Errorf("state after Stop = %q, want it to stay %q", st.State, StateStopped)
	}
}

// --------------------------------------------------------------------- logs

func TestStderrCaptureIsBoundedAndOldestFirst(t *testing.T) {
	const emitted = logRingSize + 200

	rec := newRecorder()
	p := testProcess(t, fakeStderr(emitted), Spec{
		AutoRestart: false,
		OnState:     rec.onState,
	})
	p.Start()

	waitFor(t, "child to finish", func() bool { return rec.saw(StateStopped) })

	logs := p.Logs()
	if len(logs) != logRingSize {
		t.Fatalf("captured %d lines, want the ring capacity %d", len(logs), logRingSize)
	}
	if want := fmt.Sprintf("line%d", emitted-logRingSize); logs[0].Text != want {
		t.Errorf("oldest retained line = %q, want %q", logs[0].Text, want)
	}
	if want := fmt.Sprintf("line%d", emitted-1); logs[len(logs)-1].Text != want {
		t.Errorf("newest line = %q, want %q", logs[len(logs)-1].Text, want)
	}
	if logs[0].Process != p.Name() {
		t.Errorf("log line process = %q, want %q", logs[0].Process, p.Name())
	}
}

func TestRingSnapshotOrdersOldestFirst(t *testing.T) {
	tests := []struct {
		name  string
		size  int
		added int
		want  []string
	}{
		{name: "an untouched ring is empty", size: 4, added: 0, want: nil},
		{name: "a partly filled ring yields only what was added", size: 4, added: 2, want: []string{"0", "1"}},
		{name: "a ring filled exactly to capacity does not drop its first line", size: 4, added: 4, want: []string{"0", "1", "2", "3"}},
		{name: "one line past capacity evicts only the oldest", size: 4, added: 5, want: []string{"1", "2", "3", "4"}},
		{name: "a wrapped ring reads oldest to newest across the seam", size: 4, added: 6, want: []string{"2", "3", "4", "5"}},
		{name: "several laps still leave exactly the last capacity lines", size: 4, added: 13, want: []string{"9", "10", "11", "12"}},
		{name: "a single-slot ring keeps only the newest", size: 1, added: 3, want: []string{"2"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRing(tt.size)
			for i := 0; i < tt.added; i++ {
				r.add(LogLine{Text: fmt.Sprint(i)})
			}

			got := r.snapshot()
			if len(got) != len(tt.want) {
				t.Fatalf("snapshot has %d lines, want %d", len(got), len(tt.want))
			}
			for i, w := range tt.want {
				if got[i].Text != w {
					t.Errorf("line %d = %q, want %q (got %v)", i, got[i].Text, w, texts(got))
				}
			}
		})
	}
}

func TestRingSnapshotIsADetachedCopy(t *testing.T) {
	r := newRing(2)
	r.add(LogLine{Text: "first"})

	snap := r.snapshot()
	r.add(LogLine{Text: "second"})
	r.add(LogLine{Text: "third"})

	if len(snap) != 1 || snap[0].Text != "first" {
		t.Errorf("snapshot changed under the caller: %v", texts(snap))
	}
}

func texts(lines []LogLine) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.Text
	}
	return out
}

// ----------------------------------------------------------------- classify

func TestClassifyLevels(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{"fatal outranks the error it also contains", "[out#0] Fatal error initialising muxer", "fatal"},
		{"a panic is fatal", "assertion failed: PANIC in mov muxer", "fatal"},
		{"a refused connection is an error", "rtmp://a.rtmp.example/live: Connection refused", "error"},
		{"a missing file is an error", "srt.sdp: No such file or directory", "error"},
		{"an unwritable output is an error", "Unable to open output file", "error"},
		{"an invalid argument is an error", "Invalid sample rate 0", "error"},
		{"a failed operation is an error", "Encoder initialization failed", "error"},
		{"deprecation is only a warning", "The pixel format yuvj420p is deprecated", "warning"},
		{"past duration is only a warning", "Past duration 0.998253 too large", "warning"},
		{"non-monotonous DTS is only a warning", "Non-monotonous DTS in output stream 0:1", "warning"},
		{"an explicit warning is a warning", "[hls] WARNING: skipping segment", "warning"},
		{"a progress line is plain info", "frame= 1200 fps= 25 q=28.0 size=  4096kB", "info"},
		{"a banner line is plain info", "ffmpeg version 7.1 Copyright (c) 2000-2024", "info"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classify(tt.line); got != tt.want {
				t.Errorf("classify(%q) = %q, want %q", tt.line, got, tt.want)
			}
		})
	}
}

// ------------------------------------------------------------ CommandString

func TestCommandStringQuoting(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "a bare binary renders alone",
			want: "ffmpeg",
		},
		{
			name: "ordinary flags are left unquoted",
			args: []string{"-i", "srt://0.0.0.0:6000", "-c:v", "copy"},
			want: "ffmpeg -i srt://0.0.0.0:6000 -c:v copy",
		},
		{
			name: "an argument containing a space is quoted",
			args: []string{"-i", "/media/My Recordings/take 1.mkv"},
			want: "ffmpeg -i '/media/My Recordings/take 1.mkv'",
		},
		{
			name: "a filter graph with pipes is quoted",
			args: []string{"-filter_complex", "[0:a]pan=stereo|c0=c0|c1=c1[out]"},
			want: "ffmpeg -filter_complex '[0:a]pan=stereo|c0=c0|c1=c1[out]'",
		},
		{
			name: "an embedded single quote is escaped, not swallowed",
			args: []string{"-metadata", "title=Ben's show"},
			want: `ffmpeg -metadata 'title=Ben'\''s show'`,
		},
		{
			name: "shell metacharacters are quoted even without spaces",
			args: []string{"rtmp://x/live;rm", "$HOME"},
			want: "ffmpeg 'rtmp://x/live;rm' '$HOME'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New(slog.New(slog.NewTextHandler(io.Discard, nil)),
				Spec{Name: "ingest", Bin: "ffmpeg", Args: tt.args})
			if got := p.CommandString(); got != tt.want {
				t.Errorf("CommandString() = %s, want %s", got, tt.want)
			}
		})
	}
}
