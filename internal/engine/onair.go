package engine

import (
	"fmt"

	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// OnAir is what is currently at stake if this process restarts.
//
// It exists for the upgrade path. polyemesis is not a tool an operator runs and
// closes: it is the thing carrying a broadcast, and restarting it drops every
// destination mid-stream. An upgrade button that does not ask this question is
// an upgrade button that ends someone's show, and it will do it on the one
// afternoon they were not watching.
//
// Deliberately a REPORT, not a boolean. "Cannot upgrade" is useless on its own;
// "3 destinations are live and a recording is running" tells an operator what
// they are being asked to interrupt, and lets them decide it is fine.
type OnAir struct {
	// Publishers is the number of sources with a live encoder pushing into
	// them. This is the one that means a broadcast is genuinely happening.
	Publishers int `json:"publishers"`
	// Destinations is the number of destination processes in a running state,
	// which is what a platform sees as the connection.
	Destinations int `json:"destinations"`
	// Recording is set when anything is being written to disk. It matters
	// separately from a destination: interrupting a recording loses footage
	// that no downstream has a copy of.
	Recording bool `json:"recording"`
	// Names are the source names involved, so the warning can say WHICH
	// programme rather than a count. Bounded, because a warning listing forty
	// programmes is one nobody reads.
	Names []string `json:"names,omitempty"`
}

// Busy reports whether anything would be interrupted by a restart.
func (o OnAir) Busy() bool {
	return o.Publishers > 0 || o.Destinations > 0 || o.Recording
}

// Summary is the sentence an operator is shown. Empty when nothing is at stake.
//
// Written here rather than in the UI because the same words have to reach a
// terminal: an upgrade run over SSH must refuse for a reason the operator can
// read, and two phrasings of the same refusal is how they come to disagree.
func (o OnAir) Summary() string {
	if !o.Busy() {
		return ""
	}
	parts := make([]string, 0, 3)
	if o.Publishers > 0 {
		parts = append(parts, plural(o.Publishers, "encoder is publishing", "encoders are publishing"))
	}
	if o.Destinations > 0 {
		parts = append(parts, plural(o.Destinations, "destination is live", "destinations are live"))
	}
	if o.Recording {
		parts = append(parts, "a recording is running")
	}
	out := joinAnd(parts)
	if len(o.Names) > 0 {
		out += " (" + joinAnd(o.Names) + ")"
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func joinAnd(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	}
	out := ""
	for i, p := range parts[:len(parts)-1] {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out + ", and " + parts[len(parts)-1]
}

// OnAir surveys every engine and reports what a restart would interrupt.
//
// Counted from the SUPERVISOR's view of each process rather than from the
// database's view of what is enabled. A destination that is enabled and failing
// to start is not on air, and refusing an upgrade because of one would make the
// gate impossible to get past exactly when an operator most needs to.
func (m *Manager) OnAir() OnAir {
	var o OnAir
	seen := map[string]bool{}

	for _, eng := range m.Engines() {
		st := eng.Status()

		live := false
		if m.SharedIngestPublishing(eng.SourceID()) {
			o.Publishers++
			live = true
		}
		for _, d := range st.Destinations {
			if running(d.Process) {
				o.Destinations++
				live = true
			}
			// The redundant output counts too: it is a second connection to a
			// platform, and dropping it is the same event to whoever is watching.
			if running(d.BackupProcess) {
				o.Destinations++
				live = true
			}
		}
		if running(st.Recorder) {
			o.Recording = true
			live = true
		}

		if live && st.Source.Name != "" && !seen[st.Source.Name] {
			seen[st.Source.Name] = true
			// Four names, then stop. A list long enough to scroll is a list
			// nobody reads, and the counts above already carry the scale.
			if len(o.Names) < 4 {
				o.Names = append(o.Names, st.Source.Name)
			}
		}
	}
	return o
}

// running reports whether a supervised process is actually carrying traffic.
//
// Reconnecting counts. A destination reconnecting to a platform is mid-broadcast
// from the operator's point of view, and restarting underneath it turns a
// recoverable blip into a dropped show.
func running(s *supervisor.Status) bool {
	if s == nil {
		return false
	}
	return s.State == supervisor.StateRunning || s.State == supervisor.StateReconnecting
}
