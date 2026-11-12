package clipper

import "github.com/rainmanjam/polyemesis/internal/jobs"

// Info is the jobs-page entry for the clip export kind.
var Info = jobs.KindInfo{
	Kind:        JobKind,
	Label:       "Clip export",
	Description: "A keyframe-accurate cut out of a finished recording, copied losslessly wherever the cut lands on a keyframe.",
}

// Register wires a clip export worker into a queue, one at a time.
func Register(r jobs.Registerer, w jobs.Worker) error {
	return r.Register(JobKind, 1, w)
}
