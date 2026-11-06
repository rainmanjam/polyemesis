package publish

import "github.com/rainmanjam/polyemesis/internal/jobs"

// Info is the jobs-page entry for the clip publish kind.
var Info = jobs.KindInfo{
	Kind:        JobKind,
	Label:       "Clip publish",
	Description: "Sends a finished clip or short to a platform, after somebody clicked Publish. It resumes an interrupted upload instead of starting a second post, and it waits while a broadcast is live so the upload does not compete with the stream.",
}

// Register wires a publish worker into a queue. Concurrency 1: uploads share
// the machine's upstream bandwidth with the stream, and the governor already
// holds them back while an ingest is live.
func Register(r jobs.Registrar, w jobs.Worker) error {
	return r.Register(JobKind, 1, w)
}
