package engine

import (
	"io"
	"log/slog"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// DestinationInUse is what stops a new start (and the Facebook live-video
// renewal that rides on it) from disturbing a destination the engine is
// carrying. A delay buffer still filling has NO process, a drain has left
// e.dests, and a stopped or held process is exactly what a start is for.
//
// Mutation: make the d.proc == nil branch return false. Red on "filling".
func TestDestinationInUse(t *testing.T) {
	stopped := supervisor.New(slog.New(slog.NewTextHandler(io.Discard, nil)), supervisor.Spec{Name: "held"})
	e := &Engine{
		dests: map[int64]*destination{
			1: {},              // delay buffer filling: no process yet
			4: {proc: stopped}, // process present but stopped, failed or held
		},
		draining: map[int64]*destination{2: {}},
		retiring: map[int64]*destination{3: {}},
	}
	cases := []struct {
		name string
		id   int64
		want bool
	}{
		{"filling a delay buffer (no process)", 1, true},
		{"draining its tail (left e.dests)", 2, true},
		{"retiring", 3, true},
		{"process stopped or held: a start is wanted", 4, false},
		{"absent", 9, false},
	}
	for _, c := range cases {
		if got := e.DestinationInUse(c.id); got != c.want {
			t.Errorf("%s: DestinationInUse = %v, want %v", c.name, got, c.want)
		}
	}
}
