package engine

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/srtserver"
)

// TestListenerHealthBadgesOnlyWhatAnOperatorCanFix is #105's badge ruling.
//
// The field this asserts drives an orange "Partly bound" badge on the source
// card. Before this, it was derived from BindReport.Degraded, which asks "did
// every requested address bind" -- the right question for a log line and the
// wrong one for a badge, because a wildcard expands to 0.0.0.0 AND [::] and an
// IPv4-only host therefore answers no forever. That host wore the badge for its
// entire life with nothing to fix, which trains the operator to ignore it and
// so costs them the one time it means something.
//
// Table rather than real listeners on purpose, and the reason is the whole
// point of the change: a test can occupy the IPv6 wildcard on a port and stage
// a genuine EADDRINUSE, which is what the srtserver and api suites already do,
// but there is no way to make a CI runner that HAS IPv6 pretend it does not.
// The absent-family case is the case, and only a report can express it.
func TestListenerHealthBadgesOnlyWhatAnOperatorCanFix(t *testing.T) {
	const (
		v4 = "0.0.0.0:6000"
		v6 = "[::]:6000"
	)

	tests := []struct {
		name string
		rep  srtserver.BindReport
		want string
		// detail, when set, must appear in the reported Detail.
		detail string
	}{
		{
			name: "both families bound",
			rep: srtserver.BindReport{
				Requested: []string{v4, v6}, Bound: []string{v4, v6},
			},
			want: listenerOK,
		},
		{
			name: "an IPv4-only host is NORMAL and must not wear a badge",
			rep: srtserver.BindReport{
				Requested: []string{v4, v6},
				Bound:     []string{v4},
				Failed: []srtserver.BindFailure{{
					Addr:        v6,
					Err:         "listen udp6 [::]:6000: socket: address family not supported by protocol",
					Unavailable: true,
				}},
			},
			want: listenerOK,
		},
		{
			name: "a family this host HAS, refused anyway, is the alarm",
			rep: srtserver.BindReport{
				Requested: []string{v4, v6},
				Bound:     []string{v4},
				Failed: []srtserver.BindFailure{{
					Addr: v6,
					Err:  "listen udp6 [::]:6000: bind: address already in use",
				}},
			},
			want:   listenerDegraded,
			detail: "address already in use",
		},
		{
			name: "a MIXED report names the failure that can be acted on",
			rep: srtserver.BindReport{
				Requested: []string{v6, v4},
				Bound:     []string{"127.0.0.1:6000"},
				Failed: []srtserver.BindFailure{
					{Addr: v6, Err: "address family not supported by protocol", Unavailable: true},
					{Addr: v4, Err: "bind: permission denied"},
				},
			},
			want: listenerDegraded,
			// Not "address family not supported", which is what Failed[0] says
			// and which would send the operator looking for an IPv6 problem
			// while the port they cannot open went unmentioned.
			detail: "permission denied",
		},
		{
			name: "no listener at all is not a degradation, it is an absence",
			rep: srtserver.BindReport{
				Requested: []string{v4, v6},
				Failed: []srtserver.BindFailure{
					{Addr: v4, Err: "bind: address already in use"},
					{Addr: v6, Err: "bind: address already in use"},
				},
			},
			// Nothing bound, so Degraded is false by construction: Start
			// returns an error in this case and the manager has no listener to
			// report on. The badge is not where that gets said.
			want: listenerOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := listenerHealthFor(tt.rep)
			if got.State != tt.want {
				t.Fatalf("state = %q, want %q (detail %q)", got.State, tt.want, got.Detail)
			}
			if tt.want == listenerDegraded && got.Detail == "" {
				t.Fatal("degraded with no detail; a bare \"degraded\" sends the " +
					"operator to the logs, which is what this field exists to end")
			}
			if tt.want == listenerOK && got.Detail != "" {
				t.Errorf("an ok state carried a detail %q; the UI renders the detail "+
					"beside the badge and would show a warning with no badge", got.Detail)
			}
			if tt.detail != "" && !strings.Contains(got.Detail, tt.detail) {
				t.Errorf("detail = %q, want it to mention %q", got.Detail, tt.detail)
			}
		})
	}
}
