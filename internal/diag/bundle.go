package diag

import (
	"encoding/json"
	"io"
	"time"
)

// Bundle is what leaves the machine.
//
// A SINGLE JSON DOCUMENT RATHER THAN AN ARCHIVE OF FILES, deliberately. An
// operator is about to attach this to a support thread, and the first thing
// anybody sensible does with a stranger's diagnostic is look inside it. One
// readable document can be skimmed before sending; a zip has to be trusted.
// That matters more here than compactness, because the person deciding whether
// to send it is the person whose credentials are in it if the scrubbing failed.
//
// EVERY FIELD HERE IS A DISCLOSURE DECISION. The rule applied throughout is:
// include what an engineer cannot work without, and nothing that only MIGHT be
// useful. Anything carrying an operator's own naming -- destination names, which
// are frequently a client's name; source names; hostnames -- is deliberately
// absent, because it identifies the business rather than the fault, and it is
// the sort of thing whose absence nobody notices while debugging.
type Bundle struct {
	// GeneratedAt is when this was produced, so a stale bundle in a thread can
	// be recognised as stale.
	GeneratedAt time.Time `json:"generatedAt"`
	Version     string    `json:"version"`
	// Platform is GOOS/GOARCH. Enough to explain a path separator or an
	// address-family difference; not enough to identify a machine.
	Platform string `json:"platform"`

	// Capture describes the recording itself, so a thin bundle is visibly thin.
	Capture CaptureInfo `json:"capture"`

	// Records is the scrubbed ring, oldest first. Already redacted -- see
	// Recorder.scrub. Nothing is scrubbed at this point, because anything that
	// needed scrubbing and reached here was never scrubbed at all.
	Records []Record `json:"records"`
}

// CaptureInfo is the honest description of what the ring holds.
//
// TRUNCATION IS REPORTED RATHER THAN IMPLIED. A ring that dropped nine thousand
// lines and shows five thousand looks exactly like a quiet system unless it
// says so, and an engineer reading it would conclude the problem left no trace.
type CaptureInfo struct {
	// Held is how many records are in this bundle.
	Held int `json:"held"`
	// Seen is how many were captured in total. Greater than Held means the
	// oldest were dropped.
	Seen uint64 `json:"seen"`
	// Capacity is the ring's size, so "raise it and reproduce again" is an
	// actionable suggestion rather than a guess.
	Capacity int `json:"capacity"`
	// Truncated is Seen > Held, stated rather than left to arithmetic.
	Truncated bool `json:"truncated"`
	// Recording is whether capture was still running when this was taken.
	Recording bool `json:"recording"`
	// Level is the log level in force, because "debug mode was on" and "the
	// level was actually debug" are different claims and only one of them
	// explains a sparse capture.
	Level string `json:"level"`
}

// Build assembles a bundle from the recorder and the switch.
//
// It takes no store, no config and no destination list, and that is the point of
// the current scope: everything here is either already scrubbed or is a constant
// of the build. Adding an operator's configuration means adding a disclosure
// decision per field, and each one needs its own test -- see the note in
// docs/notes on why step 4 is security work rather than plumbing.
func Build(version string, platform string, rec *Recorder, sw *Switch, now time.Time) Bundle {
	records := rec.Records()
	seen := rec.Seen()
	capacity := 0
	if rec != nil {
		rec.mu.Lock()
		capacity = len(rec.buf)
		rec.mu.Unlock()
	}
	level := ""
	if sw != nil {
		level = sw.Level().String()
	}
	return Bundle{
		GeneratedAt: now,
		Version:     version,
		Platform:    platform,
		Capture: CaptureInfo{
			Held:      len(records),
			Seen:      seen,
			Capacity:  capacity,
			Truncated: seen > uint64(len(records)),
			Recording: rec.Recording(),
			Level:     level,
		},
		Records: records,
	}
}

// Encode renders the bundle as indented JSON.
//
// NOT WriteTo: go vet objects, and it is right to. io.WriterTo's WriteTo
// returns (int64, error), and a method with that name and a different signature
// reads as an implementation of an interface it does not implement -- so a
// caller passing a Bundle where an io.WriterTo is wanted gets a compile error
// that says nothing about why.
//
// INDENTED ON PURPOSE, for the same reason it is one document: this is meant to
// be READ by the operator sending it, not only parsed by whoever receives it. A
// diagnostic somebody cannot skim is a diagnostic they send without looking.
func (b Bundle) Encode(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(b)
}
