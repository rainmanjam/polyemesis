package engine

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

func statusIn(state string) *supervisor.Status {
	return &supervisor.Status{State: supervisor.State(state)}
}

func TestOnAirBusy(t *testing.T) {
	for _, tc := range []struct {
		name string
		o    OnAir
		want bool
	}{
		{"nothing", OnAir{}, false},
		{"a publisher", OnAir{Publishers: 1}, true},
		{"a destination", OnAir{Destinations: 1}, true},
		{"only a recording", OnAir{Recording: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.o.Busy(); got != tc.want {
				t.Errorf("Busy() = %v, want %v", got, tc.want)
			}
		})
	}
}

// The summary is the whole feature. "Cannot upgrade" tells an operator nothing;
// what they need is what they are being asked to interrupt, so they can decide
// it is fine.
func TestOnAirSummarySaysWhatIsAtStake(t *testing.T) {
	for _, tc := range []struct {
		name string
		o    OnAir
		want []string
	}{
		{"idle is silent", OnAir{}, nil},
		{"one of each", OnAir{Publishers: 1, Destinations: 1, Recording: true},
			[]string{"1 encoder is publishing", "1 destination is live", "a recording is running"}},
		{"plurals agree", OnAir{Publishers: 2, Destinations: 3},
			[]string{"2 encoders are publishing", "3 destinations are live"}},
		{"names are included", OnAir{Destinations: 1, Names: []string{"Main"}},
			[]string{"1 destination is live", "(Main)"}},
		{"recording alone still reports", OnAir{Recording: true},
			[]string{"a recording is running"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.o.Summary()
			if len(tc.want) == 0 {
				if got != "" {
					t.Errorf("Summary() = %q, want empty", got)
				}
				return
			}
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("Summary() = %q, want it to contain %q", got, w)
				}
			}
		})
	}
}

func TestJoinAnd(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{"a"}, "a"},
		{[]string{"a", "b"}, "a and b"},
		{[]string{"a", "b", "c"}, "a, b, and c"},
	} {
		if got := joinAnd(tc.in); got != tc.want {
			t.Errorf("joinAnd(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A destination reconnecting to a platform is mid-broadcast from the operator's
// point of view. Restarting underneath it turns a recoverable blip into a
// dropped show, so it must count as on air.
func TestReconnectingCountsAsOnAir(t *testing.T) {
	if !running(statusIn("reconnecting")) {
		t.Error("a reconnecting destination is not counted as on air; an upgrade " +
			"would be allowed to restart underneath a broadcast that was about to recover")
	}
	if !running(statusIn("running")) {
		t.Error("a running destination is not counted as on air")
	}
	for _, s := range []string{"stopped", "failed", "starting"} {
		if running(statusIn(s)) {
			t.Errorf("state %q counted as on air; an enabled destination that cannot "+
				"start would make the gate impossible to get past", s)
		}
	}
	if running(nil) {
		t.Error("a nil process counted as on air")
	}
}
