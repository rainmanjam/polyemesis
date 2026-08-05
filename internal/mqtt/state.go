package mqtt

import "time"

// The payloads published on the retained topics.
//
// Every one of these is built field by field from a whitelist. Nothing here
// marshals a database row, an engine struct or a settings block, and that is a
// security property rather than a style choice: polyemesis holds stream keys,
// OAuth tokens, SRT passphrases and webhook URLs with secrets in their paths,
// and an MQTT topic has no equivalent of the URL masking already applied in the
// alerts payloads. A struct that grew a field later would leak it to every
// subscriber on the broker, retained, with no way to know it had happened.
//
// A whitelist cannot grow a field by accident. TestPayloadsCarryOnlyApprovedFields
// holds that line by construction rather than by review: a field added to any
// struct below fails it until someone lists the field deliberately, in
// state_test.go, next to the reasoning for why it is safe to publish.
// TestNoPayloadFieldIsNamedLikeACredential is the other half, and catches a
// field that was listed deliberately and should not have been.

// Availability is the value published on the status topic. It is a bare string
// rather than JSON so a Home Assistant availability template needs no
// value_template, which is the single most common source of an entity that
// shows "unavailable" for reasons nobody can find.
const (
	Online  = "online"
	Offline = "offline"
)

// HostState is the install-wide snapshot.
type HostState struct {
	Version   string    `json:"version"`
	StartedAt time.Time `json:"startedAt"`
	UptimeSec float64   `json:"uptimeSec"`
	// Sources counts configured programmes; SourcesLive counts those actually
	// receiving. The pair is what distinguishes "nothing configured" from
	// "everything is off air", which look identical from a single number and
	// mean opposite things.
	Sources     int       `json:"sources"`
	SourcesLive int       `json:"sourcesLive"`
	Dests       int       `json:"destinations"`
	DestsUp     int       `json:"destinationsUp"`
	At          time.Time `json:"at"`
}

// SourceState is one programme.
type SourceState struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Slug is the topic segment this state was published under. Carried in the
	// payload as well so a consumer holding only the message body can address
	// the thing it describes -- and so a slug that was altered is visible
	// beside the name it came from.
	Slug string `json:"slug"`
	// Live is bytes actually arriving on the relay, not process state: an SRT
	// or RTMP listener sits in "running" for as long as it waits for a
	// publisher, which is a different question from whether the source is on.
	Live bool `json:"live"`
	// IngestMode is srt / rtmp / pull. Never the URL, which for a pull ingest
	// can carry credentials.
	IngestMode  string  `json:"ingestMode"`
	IngestError string  `json:"ingestError,omitempty"`
	BitrateKbps float64 `json:"bitrateKbps"`
	UptimeSec   float64 `json:"uptimeSec"`
	Restarts    int     `json:"restarts"`
	// LossPercent is MPEG-TS continuity-counter loss on the relay -- the same
	// instrumentation the playlist acceptance suite measures against.
	LossPercent float64 `json:"lossPercent"`
	Recording   bool    `json:"recording"`
	Dests       int     `json:"destinations"`
	DestsUp     int     `json:"destinationsUp"`
	// Failover names which input is on air, empty when the tier is off. A
	// failover nobody notices is how an operator finds out at the end of a
	// broadcast that they streamed the backup all night.
	Failover string    `json:"failover,omitempty"`
	At       time.Time `json:"at"`
}

// DestState is one output.
//
// Note what is absent: URL, stream key, passphrase. A destination contributes
// its name, platform and error, exactly as it does across the alerts boundary.
type DestState struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	Platform string `json:"platform"`
	Kind     string `json:"kind"`
	Enabled  bool   `json:"enabled"`
	// Running is the engine's verdict, not the process's: a destination whose
	// routing graph would not compile has no process at all, and that is as
	// down as a crashed one.
	Running     bool      `json:"running"`
	Error       string    `json:"error,omitempty"`
	BitrateKbps float64   `json:"bitrateKbps"`
	UptimeSec   float64   `json:"uptimeSec"`
	Restarts    int       `json:"restarts"`
	Rendition   string    `json:"rendition,omitempty"`
	At          time.Time `json:"at"`
}

// RenditionState is one shared encode.
type RenditionState struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
	// Consumers is the ref count the engine acted on. A rendition with none has
	// no process by design and must not read as failed.
	Consumers   int       `json:"consumers"`
	Running     bool      `json:"running"`
	Width       int       `json:"width"`
	Height      int       `json:"height"`
	FPS         int       `json:"fps"`
	Codec       string    `json:"codec"`
	Encoder     string    `json:"encoder"`
	BitrateKbps float64   `json:"bitrateKbps"`
	Error       string    `json:"error,omitempty"`
	At          time.Time `json:"at"`
}
