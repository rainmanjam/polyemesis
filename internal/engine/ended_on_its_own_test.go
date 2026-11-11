package engine

import (
	"testing"

	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// endedOnItsOwn decides whether a live captioner stopped without being asked to
// (so it is restarted) or is still the one the operator wants. Each guard below
// is a way to restart something that should be left alone, or to leave dead
// captions on screen.
func TestEndedOnItsOwn(t *testing.T) {
	// A zero LiveCaptioner has never been started, so it reads as not running.
	stopped := &transcribe.LiveCaptioner{}
	tests := []struct {
		name string
		cur  *transcribe.LiveCaptioner
		sig  string
		want string
		ok   bool
	}{
		{name: "nothing installed is not an ended captioner", cur: nil, sig: "a", want: "a", ok: false},
		{name: "no wanted signature means nothing to restart", cur: stopped, sig: "a", want: "", ok: false},
		{name: "a captioner for a different signature is not ours to restart", cur: stopped, sig: "a", want: "b", ok: false},
		{name: "a stopped captioner under the wanted signature has ended on its own", cur: stopped, sig: "a", want: "a", ok: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := endedOnItsOwn(tc.cur, tc.sig, tc.want); got != tc.ok {
				t.Errorf("endedOnItsOwn = %v, want %v", got, tc.ok)
			}
		})
	}
}
