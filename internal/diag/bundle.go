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
// EVERY FIELD HERE IS A DISCLOSURE DECISION. The rule applied to the fields
// BELOW is: include what an engineer cannot work without, and nothing that only
// might be useful. So no configuration, no destination list, no hostname.
//
// THAT RULE DOES NOT AND CANNOT EXTEND TO Records, AND AN EARLIER VERSION OF
// THIS COMMENT CLAIMED OTHERWISE. It said an operator's own naming was
// "deliberately absent". That is true of the struct fields here and FALSE of
// what they contain: polyemesis logs `"destination", d.Name` at more than
// twenty call sites -- internal/api/lifecycle.go:764 and :844 among them -- so
// destination, source and host names reach this bundle inside the log lines,
// and no scrubber can remove them without destroying the diagnostic.
//
// The consent dialog says so now, in the operator's own words, because the
// person deciding whether to send this is the only one who can judge whether a
// client's name in a log line matters. A dialog that told them otherwise was
// worse than no dialog: it bought consent with a false statement.
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
	// Bytes is the measured payload size of the records, so the operator sending
	// this can be told how large it is BEFORE they send it. Approximate: it
	// excludes the JSON syntax and indentation wrapped around it at export.
	Bytes int `json:"bytes"`
	// RecordsTruncated is how many captured records exceeded MaxRecordBytes and
	// were cut. A DIFFERENT CLAIM FROM Truncated above, which is about whole
	// records the ring dropped; this is about surviving records that are short.
	// An engineer needs both: one explains a missing line, the other explains a
	// line that stops mid-sentence.
	RecordsTruncated uint64 `json:"recordsTruncated"`
	// RecordCap is MaxRecordBytes, so "a line was cut at 8 KB" is a fact the
	// bundle carries rather than one the reader has to know.
	RecordCap int `json:"recordCap"`
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
// ONE SNAPSHOT, NOT SIX READS. An earlier version called Records, Seen, Bytes,
// TruncatedCount and Recording in turn, each taking the recorder's lock
// separately, so a concurrent Observe or Reset produced a bundle whose stated
// size described a ring state its records had not come from. The size an
// operator is shown before sending a file has to be that file's size.
func Build(version string, platform string, rec *Recorder, sw *Switch, now time.Time) Bundle {
	snap := rec.Snapshot()
	level := ""
	if sw != nil {
		level = sw.Level().String()
	}
	return Bundle{
		GeneratedAt: now,
		Version:     version,
		Platform:    platform,
		Capture: CaptureInfo{
			Held:             len(snap.Records),
			Seen:             snap.Seen,
			Capacity:         snap.Capacity,
			Truncated:        snap.Seen > uint64(len(snap.Records)),
			Bytes:            snap.Bytes,
			RecordsTruncated: snap.Truncated,
			RecordCap:        MaxRecordBytes,
			Recording:        snap.Recording,
			Level:            level,
		},
		Records: snap.Records,
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
