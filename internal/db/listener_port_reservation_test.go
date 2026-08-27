package db

import (
	"strings"
	"testing"
)

// AN INGEST LISTENER MAY NOT ASK FOR A PORT THIS PROCESS ALREADY HOLDS.
//
// Saving rtmpPort as the server's own HTTP port returned 200. The listener then
// could not bind on the next reconcile, which logged one ERROR and returned --
// and its own comment concedes the log is the only way anybody finds out.
// Ingest was dead while the settings page showed the port saved and green, so
// the operator spent the outage debugging their encoder.
//
// Mutation: delete the `s.Listeners.RTMPPort == r.Port` branch from
// TCPPortConflicts. Observed to fail with
//
//	rtmp on the reserved port: no conflict reported, so a listener that cannot
//	bind saves clean
func TestTCPPortConflictsRefusesAnRTMPListenerOnAReservedPort(t *testing.T) {
	reserved := []ReservedTCPPort{{Port: 8099, Why: "the web UI and API are served on it"}}

	cases := []struct {
		name      string
		listeners ListenerSettings
		reserved  []ReservedTCPPort
		want      bool
	}{{
		name:      "rtmp on the reserved port",
		listeners: ListenerSettings{SRTPort: 6000, RTMPPort: 8099},
		reserved:  reserved,
		want:      true,
	}, {
		// THE CONTROL. A rule that reported a conflict for every document would
		// satisfy the case above and make the listener settings unusable.
		name:      "neither listener near the reserved port",
		listeners: ListenerSettings{SRTPort: 6000, RTMPPort: 1935},
		reserved:  reserved,
		want:      false,
	}, {
		// SRT IS UDP. Sharing a number with a TCP HTTP listener is not a
		// collision at the kernel, and refusing it would refuse a
		// configuration that works -- SRT on 443 beside HTTPS on 443 is how an
		// install gets through a firewall that allows nothing else.
		name:      "srt on the reserved port",
		listeners: ListenerSettings{SRTPort: 8099, RTMPPort: 1935},
		reserved:  reserved,
		want:      false,
	}, {
		// A zero means the addr named no readable port, not port zero.
		// Reserving it would refuse every save on an install whose addr the
		// parser merely could not read.
		name:      "an unreadable http port reserves nothing",
		listeners: ListenerSettings{SRTPort: 6000, RTMPPort: 1935},
		reserved:  []ReservedTCPPort{{Port: 0, Why: "unreadable"}},
		want:      false,
	}, {
		name:      "nothing reserved at all",
		listeners: ListenerSettings{SRTPort: 6000, RTMPPort: 1935},
		reserved:  nil,
		want:      false,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := DefaultSettings()
			s.Listeners = tc.listeners
			probs := s.TCPPortConflicts(tc.reserved)
			if got := len(probs) > 0; got != tc.want {
				t.Fatalf("%s: %d conflicts %v, want a conflict: %v -- so a "+
					"listener that cannot bind saves clean", tc.name, len(probs), probs, tc.want)
			}
		})
	}
}

// The refusal has to name what is holding the port. "port 8099 is unavailable"
// sends an operator to netstat; naming the web UI sends them to the one line of
// config.yaml they can change.
//
// Mutation: drop `r.Why` from the message in TCPPortConflicts. Observed to fail
// with "the refusal does not say what holds port 8099".
func TestTCPPortConflictSaysWhatIsHoldingThePort(t *testing.T) {
	s := DefaultSettings()
	s.Listeners = ListenerSettings{SRTPort: 6000, RTMPPort: 8099}

	probs := s.TCPPortConflicts([]ReservedTCPPort{{
		Port: 8099, Why: "this server already serves the web UI and API on it",
	}})
	if len(probs) != 1 {
		t.Fatalf("got %d conflicts %v, want exactly one", len(probs), probs)
	}
	if !strings.Contains(probs[0], "web UI") {
		t.Fatalf("the refusal does not say what holds port 8099, only that it "+
			"cannot be used: %q", probs[0])
	}
}
