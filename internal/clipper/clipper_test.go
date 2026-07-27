package clipper

import (
	"errors"
	"testing"
	"time"
)

func TestNewTimelineOrdersAndLaysOutSegments(t *testing.T) {
	tests := []struct {
		name      string
		in        []Segment
		wantPaths []string
		wantStart []time.Duration
		wantTotal time.Duration
		wantErr   error
	}{
		{
			name:    "no segments is an error rather than an empty timeline",
			in:      nil,
			wantErr: ErrNoSegments,
		},
		{
			name:    "a segment with no duration cannot contribute frames and is dropped",
			in:      []Segment{{Path: "/a.mkv", Duration: 0}},
			wantErr: ErrNoSegments,
		},
		{
			name:      "explicit starts are honoured",
			in:        []Segment{{Path: "/b.mkv", Start: time.Hour, Duration: time.Hour}, {Path: "/a.mkv", Duration: time.Hour}},
			wantPaths: []string{"/a.mkv", "/b.mkv"},
			wantStart: []time.Duration{0, time.Hour},
			wantTotal: 2 * time.Hour,
		},
		{
			name: "several segments all claiming to start at zero are laid end to end in the order given",
			in: []Segment{
				{Path: "/a.mkv", Duration: 10 * time.Second},
				{Path: "/b.mkv", Duration: 5 * time.Second},
				{Path: "/c.mkv", Duration: time.Second},
			},
			wantPaths: []string{"/a.mkv", "/b.mkv", "/c.mkv"},
			wantStart: []time.Duration{0, 10 * time.Second, 15 * time.Second},
			wantTotal: 16 * time.Second,
		},
		{
			name:      "a lone segment at zero is left where it is",
			in:        []Segment{{Path: "/a.mkv", Duration: time.Minute}},
			wantPaths: []string{"/a.mkv"},
			wantStart: []time.Duration{0},
			wantTotal: time.Minute,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tl, err := NewTimeline(tc.in)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("NewTimeline: got %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewTimeline: %v", err)
			}
			segs := tl.Segments()
			if len(segs) != len(tc.wantPaths) {
				t.Fatalf("got %d segments, want %d", len(segs), len(tc.wantPaths))
			}
			for i, s := range segs {
				if s.Path != tc.wantPaths[i] {
					t.Errorf("segment %d is %s, want %s", i, s.Path, tc.wantPaths[i])
				}
				if s.Start != tc.wantStart[i] {
					t.Errorf("segment %d starts at %s, want %s", i, s.Start, tc.wantStart[i])
				}
			}
			if tl.Duration() != tc.wantTotal {
				t.Errorf("timeline runs %s, want %s", tl.Duration(), tc.wantTotal)
			}
		})
	}
}

func TestTimelineSpanIsHalfOpen(t *testing.T) {
	tl := hourlyTimeline(t, 3, 10*time.Second)

	tests := []struct {
		name      string
		in, out   time.Duration
		wantPaths []string
		wantErr   error
	}{
		{
			name: "a range inside one segment touches only that segment",
			in:   11 * time.Second, out: 12 * time.Second,
			wantPaths: []string{"/seg1.mkv"},
		},
		{
			name: "a range crossing a boundary touches both segments",
			in:   9 * time.Second, out: 11 * time.Second,
			wantPaths: []string{"/seg0.mkv", "/seg1.mkv"},
		},
		{
			name: "a range ending exactly on a boundary does not pull in the next segment",
			in:   5 * time.Second, out: 10 * time.Second,
			wantPaths: []string{"/seg0.mkv"},
		},
		{
			name: "a range starting exactly on a boundary does not pull in the previous segment",
			in:   10 * time.Second, out: 15 * time.Second,
			wantPaths: []string{"/seg1.mkv"},
		},
		{
			name: "a range spanning everything touches everything",
			in:   0, out: 30 * time.Second,
			wantPaths: []string{"/seg0.mkv", "/seg1.mkv", "/seg2.mkv"},
		},
		{
			name: "an empty range is refused",
			in:   5 * time.Second, out: 5 * time.Second,
			wantErr: ErrEmptyRange,
		},
		{
			name: "a range past the end touches nothing",
			in:   40 * time.Second, out: 50 * time.Second,
			wantErr: ErrOutOfRange,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tl.Span(tc.in, tc.out)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Span: got %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Span: %v", err)
			}
			if len(got) != len(tc.wantPaths) {
				t.Fatalf("got %d segments %v, want %d", len(got), paths(got), len(tc.wantPaths))
			}
			for i, s := range got {
				if s.Path != tc.wantPaths[i] {
					t.Errorf("segment %d is %s, want %s", i, s.Path, tc.wantPaths[i])
				}
			}
		})
	}
}

func TestRequestValidateRejectsWhatCanNeverWork(t *testing.T) {
	ok := Request{In: time.Second, Out: 2 * time.Second, OutPath: "/clips/a.mkv"}

	tests := []struct {
		name    string
		mut     func(*Request)
		wantErr error
	}{
		{name: "a well formed request passes", mut: func(*Request) {}},
		{name: "no output path", mut: func(r *Request) { r.OutPath = "" }, wantErr: ErrInvalidRequest},
		{name: "a relative output path", mut: func(r *Request) { r.OutPath = "clips/a.mkv" }, wantErr: ErrInvalidRequest},
		{name: "a negative in-point", mut: func(r *Request) { r.In = -time.Second }, wantErr: ErrInvalidRequest},
		{name: "an out-point before the in-point", mut: func(r *Request) { r.Out = 0 }, wantErr: ErrEmptyRange},
		{name: "an out-point equal to the in-point", mut: func(r *Request) { r.Out = r.In }, wantErr: ErrEmptyRange},
		{
			name:    "a clip longer than the ceiling",
			mut:     func(r *Request) { r.Out = r.In + MaxClipDuration + time.Second },
			wantErr: ErrInvalidRequest,
		},
		{name: "an unknown mode", mut: func(r *Request) { r.Mode = "nearly" }, wantErr: ErrInvalidRequest},
		{name: "an empty mode means the default", mut: func(r *Request) { r.Mode = "" }},
		{
			name:    "an unknown audio mode",
			mut:     func(r *Request) { r.Audio.Mode = "some" },
			wantErr: ErrInvalidRequest,
		},
		{
			name:    "a track selection that names no tracks",
			mut:     func(r *Request) { r.Audio.Mode = AudioTracks },
			wantErr: ErrInvalidRequest,
		},
		{
			name: "a track selection that names tracks",
			mut:  func(r *Request) { r.Audio.Mode = AudioTracks; r.Audio.Tracks = []int{0, 2} },
		},
		{
			name:    "a negative track index",
			mut:     func(r *Request) { r.Audio.Mode = AudioTracks; r.Audio.Tracks = []int{-1} },
			wantErr: ErrInvalidRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := ok
			tc.mut(&req)
			err := req.Validate()
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate: got %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestContainerForFallsOpenOnAnUnknownExtension(t *testing.T) {
	tests := []struct {
		path string
		want Container
	}{
		{"/clips/a.mkv", ContainerMatroska},
		{"/clips/a.MKV", ContainerMatroska},
		{"/clips/a.mp4", ContainerMP4},
		{"/clips/a.mov", ContainerMP4},
		{"/clips/a.ts", ContainerMPEGTS},
		{"/clips/a.wat", DefaultContainer},
		{"/clips/a", DefaultContainer},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			if got := containerFor(tc.path); got != tc.want {
				t.Fatalf("containerFor(%q) = %s, want %s", tc.path, got, tc.want)
			}
		})
	}
}

func TestSecsIsAlwaysAFixedPointNumberFFmpegAccepts(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{0, "0.000000"},
		{time.Second, "1.000000"},
		{1500 * time.Millisecond, "1.500000"},
		{time.Microsecond, "0.000001"},
		{time.Hour, "3600.000000"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			if got := secs(tc.in); got != tc.want {
				t.Fatalf("secs(%s) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// hourlyTimeline builds n contiguous segments of the given length, named
// /seg0.mkv upwards, the way the recorder lays out an hour at a time.
func hourlyTimeline(t *testing.T, n int, each time.Duration) Timeline {
	t.Helper()
	segs := make([]Segment, 0, n)
	for i := 0; i < n; i++ {
		segs = append(segs, Segment{
			Path:     "/seg" + string(rune('0'+i)) + ".mkv",
			Start:    time.Duration(i) * each,
			Duration: each,
		})
	}
	tl, err := NewTimeline(segs)
	if err != nil {
		t.Fatalf("NewTimeline: %v", err)
	}
	return tl
}

func paths(segs []Segment) []string {
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		out = append(out, s.Path)
	}
	return out
}
