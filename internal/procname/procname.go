// Package procname owns the names of the processes polyemesis supervises: the
// kind each one is, and the name a destination's output goes by.
//
// A leaf, because three packages that cannot import one another need the same
// spelling. The supervisor keys its kill grace and its teardown tally on the
// kind; the engine builds both; the API rebuilds a destination's process name
// to find its command line and to link a timeline row to its log; and the UI
// parses the same names (ProcessTable). A kind or a name that is spelled
// differently in one of them fails quietly: a renamed kind costs latency on its
// teardown and nothing else, and a rebuilt name matches no process.
package procname

import "strconv"

// Kind groups supervised processes: in the UI's process table, in the census
// and the teardown tallies, and in the kill grace a child gets
// (supervisor.graceFor). The strings are wire values.
type Kind string

const (
	// KindIngest is an ingest listener or puller, primary or backup.
	KindIngest Kind = "ingest"
	// KindSource is a copy hop that carries one source into the selector's hub:
	// the primary, the backup or the playlist feed.
	KindSource Kind = "source"
	// KindDestination is one output to a platform, file or Icecast mount,
	// primary or backup.
	KindDestination Kind = "destination"
	// KindRecorder is the master recording.
	KindRecorder Kind = "recorder"
	// KindPreview is the HLS preview encoder.
	KindPreview Kind = "preview"
	// KindMeters is the ingest's audio meter sidecar.
	KindMeters Kind = "meters"
	// KindLoudness is one destination's loudness analyser.
	KindLoudness Kind = "loudness"
	// KindRendition is a re-encoded variant of the ingest.
	KindRendition Kind = "rendition"
	// KindSilence is the silent-audio synthesiser for an ingest with no audio.
	KindSilence Kind = "silence"
	// KindPlayout is an HLS/DASH variant muxer for the viewer-facing player.
	KindPlayout Kind = "playout"
)

// The roles a destination's relay subscription can have beyond the primary
// output. The role is part of the name, always: relay.Hub.Subscribe is a map
// assignment, so two outputs under one name REPLACE one another, and the
// replaced process keeps running with a correct command line and receives no
// packets at all.
const (
	RolePrimary = ""
	RoleBackup  = "backup"
	RoleDelay   = "delay"
	RoleCaption = "caption"
)

// DestProcessName is the supervised-process name and the relay subscription
// name of one of destination id's outputs: `dest:<id>` for the primary and
// `dest:<id>:<role>` for the others. The primary's name never changed, so no
// existing subscription moves; only new roles get a suffix.
func DestProcessName(id int64, role string) string {
	name := "dest:" + strconv.FormatInt(id, 10)
	if role == RolePrimary {
		return name
	}
	return name + ":" + role
}
