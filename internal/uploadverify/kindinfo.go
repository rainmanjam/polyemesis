package uploadverify

import "github.com/rainmanjam/polyemesis/internal/jobs"

// Info is the jobs-page entry for the media re-check kind.
var Info = jobs.KindInfo{
	Kind:        Kind,
	Label:       "Media re-check",
	Description: "Reads a stored upload again and records what it is. This is how a file the server never managed to inspect stops being \"Not checked\" without uploading it a second time. It records only what it establishes: an inspection that cannot run leaves the existing record exactly as it was.",
}
