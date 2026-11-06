package highlights

import "github.com/rainmanjam/polyemesis/internal/jobs"

// Info is the jobs-page entry for the highlight scan kind.
var Info = jobs.KindInfo{
	Kind:        JobKind,
	Label:       "Highlight scan",
	Description: "Finds moments worth a short: chat bursts, loudness peaks and, if asked, the Automod model's opinion of what was said. It only suggests; nothing is clipped until somebody accepts a suggestion.",
}

// Register wires a highlight scan worker into a queue, one at a time.
func Register(r jobs.Registrar, w jobs.Worker) error {
	return r.Register(JobKind, 1, w)
}
