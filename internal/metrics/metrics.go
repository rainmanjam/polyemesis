// Package metrics renders a snapshot of the server as Prometheus text
// exposition.
//
// The format is written by hand rather than imported from the official client
// library. polyemesis ships as one cgo-free static binary with a deliberately
// small dependency tree, and every number here is already collected elsewhere:
// there are no histograms to bucket and no registry to coordinate, so the
// library would buy nothing that fmt does not.
//
// Rendering is kept apart from collecting. A Snapshot is plain data, so the
// exposition can be pinned against hand-written input without an engine, a
// relay or a clock.
package metrics

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ContentType is what a scrape expects back. Prometheus sniffs the version
// parameter, so it is not merely text/plain.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

// processStates mirrors supervisor.State. Every state is emitted for every
// process, not just the current one, so that `state="failed"` is a series that
// sits at 0 rather than one that blinks in and out of existence — an alert on
// a series that does not exist yet never fires.
var processStates = []string{"stopped", "starting", "running", "reconnecting", "failed"}

const stateRunning = "running"

// Snapshot is every number one scrape needs, already gathered.
type Snapshot struct {
	Version string
	Uptime  time.Duration

	// Sources is how many programmes this install has. It is what separates a
	// server that is idle from one that has nothing to run: every other series
	// below reads zero in both cases, so an alert written on ingest_up alone
	// cannot tell a broadcast that ended from an install nobody has configured
	// yet.
	//
	// A POINTER, because for this one number "unknown" and 0 are opposite
	// statements and the type has to be able to say so. Nil OMITS the series
	// rather than publishing 0, and 0 is the value that means "nobody has
	// configured this install": a collector that could not read the count and
	// reported 0 anyway would silence `ingest_up == 0 and on() sources > 0`
	// during a real outage and fire "nobody configured this server" at a box
	// that is on air. Prometheus already has a word for the other case --
	// absent() -- and one missing family is not a lost exposition.
	Sources *int

	// Ingests is ONE ENTRY PER PROGRAMME, and it is a slice for the same reason
	// Destinations is (#528).
	//
	// It used to be a single unlabelled Process and a single unlabelled Relay,
	// collected from the default engine. On a multi-source install that meant
	// programme 2's encoder could stop for the whole show while
	// polyemesis_ingest_up stayed at 1, ingest_state{state="running"} stayed at
	// 1, and the relay counters went on climbing on programme 1's traffic. The
	// destination series were fixed for exactly this in #523; the ingest, which
	// is the UPSTREAM CAUSE of every one of those destinations going down, was
	// not. An alert cannot fire on a series that does not distinguish the
	// programmes, and there is nothing for an operator to notice: it is not a
	// wrong number, it is an alert that never evaluates.
	Ingests      []Ingest
	Destinations []Destination
	Recordings   Recordings
	Host         Host
}

// Process is one supervised FFmpeg child.
type Process struct {
	State    string
	Restarts int
	// BitrateKbps is kilobits per second, the unit FFmpeg and the UI use. It is
	// converted to bits per second on the way out.
	BitrateKbps float64
	DropFrames  int64
}

// Ingest is one programme's inbound feed: the ingest child, plus the relay hub
// that fans that same feed out. They travel together because they measure the
// same stream at two points, and separating them would make it possible to
// label one by programme and forget the other -- which is how this defect got
// in.
type Ingest struct {
	Process
	Relay Relay
	// ID and Name are the SOURCE's, not a process's. Named the same way a
	// Destination is, so a scrape can be read without a second convention.
	ID   int64
	Name string
}

// Destination is one output, plus the labels identifying it.
type Destination struct {
	Process
	ID       int64
	Name     string
	Kind     string
	Platform string
	Enabled  bool
}

// Relay is the fan-out hub's throughput.
type Relay struct {
	Subscribers int
	RxPackets   uint64
	RxBytes     uint64
	TxPackets   uint64
	Dropped     uint64
}

// Recordings is disk usage for the recordings directory.
type Recordings struct {
	Files      int
	UsedBytes  int64
	FreeBytes  uint64
	TotalBytes uint64
}

// Host is process and machine resource usage.
type Host struct {
	CPUPercent     float64
	MemUsedBytes   uint64
	MemTotalBytes  uint64
	ProcCPUPercent float64
	ProcMemBytes   uint64
	NumCPU         int
}

// Render returns the exposition for s.
func Render(s Snapshot) string {
	var d doc

	d.family("polyemesis_build_info", "gauge", "Build information; the value is always 1.")
	d.sample("polyemesis_build_info", 1, label{"version", s.Version})

	d.scalar("polyemesis_uptime_seconds", "gauge",
		"Time since the server process started.", s.Uptime.Seconds())

	if s.Sources != nil {
		d.scalar("polyemesis_sources", "gauge",
			"Programmes configured on this install.", float64(*s.Sources))
	}

	renderIngests(&d, s.Ingests)
	renderDestinations(&d, s.Destinations)
	renderRelay(&d, s.Ingests)
	renderRecordings(&d, s.Recordings)
	renderHost(&d, s.Host)

	return d.b.String()
}

// ingestIdent is the label pair every per-programme series carries. One
// function so ingest and relay cannot come to disagree about how a programme is
// named, which is the drift that would put half the exposition back where it
// started.
func ingestIdent(in Ingest) []label {
	return []label{
		{"id", strconv.FormatInt(in.ID, 10)},
		{"name", in.Name},
	}
}

// sortedIngests orders by source id so consecutive scrapes of an unchanged
// system are byte-identical, exactly as renderDestinations does.
func sortedIngests(ins []Ingest) []Ingest {
	out := make([]Ingest, len(ins))
	copy(out, ins)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// renderIngests emits one series per programme.
//
// THE FAMILY HEADERS ARE UNCONDITIONAL, including on an install with no
// programme at all. A family that appears only once something exists is a
// family an alert cannot be written against before the first outage, which is
// the same "a missing series is indistinguishable from nothing configured"
// failure the destination fix names. With no programmes the headers stand alone
// and polyemesis_sources says why.
func renderIngests(d *doc, ins []Ingest) {
	sorted := sortedIngests(ins)

	d.family("polyemesis_ingest_up", "gauge",
		"1 when the programme's ingest process is running.")
	for _, in := range sorted {
		d.sample("polyemesis_ingest_up", boolValue(in.State == stateRunning), ingestIdent(in)...)
	}

	d.family("polyemesis_ingest_state", "gauge",
		"Ingest process state; 1 for the state it is currently in.")
	for _, in := range sorted {
		for _, st := range processStates {
			d.sample("polyemesis_ingest_state", boolValue(in.State == st),
				append(ingestIdent(in), label{"state", st})...)
		}
	}

	d.family("polyemesis_ingest_bitrate_bits_per_second", "gauge",
		"Bitrate currently arriving from the streamer.")
	for _, in := range sorted {
		d.sample("polyemesis_ingest_bitrate_bits_per_second", in.BitrateKbps*1000, ingestIdent(in)...)
	}

	d.family("polyemesis_ingest_restarts_total", "counter",
		"Restarts of the ingest process since the server started.")
	for _, in := range sorted {
		d.sample("polyemesis_ingest_restarts_total", float64(in.Restarts), ingestIdent(in)...)
	}
}

func renderDestinations(d *doc, dests []Destination) {
	// Sorted by id so consecutive scrapes of an unchanged system are
	// byte-identical, which makes a diff of two scrapes readable.
	sorted := make([]Destination, len(dests))
	copy(sorted, dests)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	// id and name identify the series everywhere; the descriptive labels live
	// on _info alone so that renaming a platform does not orphan a counter.
	ident := func(dest Destination) []label {
		return []label{
			{"id", strconv.FormatInt(dest.ID, 10)},
			{"name", dest.Name},
		}
	}

	d.family("polyemesis_destination_info", "gauge",
		"Destination labels; the value is always 1.")
	for _, dest := range sorted {
		d.sample("polyemesis_destination_info", 1, append(ident(dest),
			label{"kind", dest.Kind}, label{"platform", dest.Platform})...)
	}

	d.family("polyemesis_destination_enabled", "gauge",
		"1 when the destination is configured to run.")
	for _, dest := range sorted {
		d.sample("polyemesis_destination_enabled", boolValue(dest.Enabled), ident(dest)...)
	}

	d.family("polyemesis_destination_up", "gauge",
		"1 when the destination's FFmpeg process is running.")
	for _, dest := range sorted {
		d.sample("polyemesis_destination_up", boolValue(dest.State == stateRunning), ident(dest)...)
	}

	d.family("polyemesis_destination_state", "gauge",
		"Destination process state; 1 for the state it is currently in.")
	for _, dest := range sorted {
		for _, st := range processStates {
			d.sample("polyemesis_destination_state", boolValue(dest.State == st),
				append(ident(dest), label{"state", st})...)
		}
	}

	d.family("polyemesis_destination_bitrate_bits_per_second", "gauge",
		"Bitrate the destination is currently publishing.")
	for _, dest := range sorted {
		d.sample("polyemesis_destination_bitrate_bits_per_second",
			dest.BitrateKbps*1000, ident(dest)...)
	}

	d.family("polyemesis_destination_restarts_total", "counter",
		"Restarts of the destination process since the server started.")
	for _, dest := range sorted {
		d.sample("polyemesis_destination_restarts_total", float64(dest.Restarts), ident(dest)...)
	}

	d.family("polyemesis_destination_dropped_frames_total", "counter",
		"Frames FFmpeg dropped for this destination since it last started.")
	for _, dest := range sorted {
		d.sample("polyemesis_destination_dropped_frames_total", float64(dest.DropFrames), ident(dest)...)
	}
}

// renderRelay emits one relay series set per programme, because there is one
// hub per engine and the counters of two programmes are not addable.
func renderRelay(d *doc, ins []Ingest) {
	sorted := sortedIngests(ins)

	for _, fam := range []struct {
		name, typ, help string
		value           func(Relay) float64
	}{
		{"polyemesis_relay_subscribers", "gauge",
			"Consumers currently attached to the relay hub.",
			func(r Relay) float64 { return float64(r.Subscribers) }},
		{"polyemesis_relay_received_packets_total", "counter",
			"Datagrams received from the ingest.",
			func(r Relay) float64 { return float64(r.RxPackets) }},
		{"polyemesis_relay_received_bytes_total", "counter",
			"Bytes received from the ingest.",
			func(r Relay) float64 { return float64(r.RxBytes) }},
		{"polyemesis_relay_transmitted_packets_total", "counter",
			"Datagrams replicated to subscribers.",
			func(r Relay) float64 { return float64(r.TxPackets) }},
		{"polyemesis_relay_dropped_packets_total", "counter",
			"Datagrams the relay failed to deliver to a subscriber.",
			func(r Relay) float64 { return float64(r.Dropped) }},
	} {
		d.family(fam.name, fam.typ, fam.help)
		for _, in := range sorted {
			d.sample(fam.name, fam.value(in.Relay), ingestIdent(in)...)
		}
	}
}

func renderRecordings(d *doc, r Recordings) {
	d.scalar("polyemesis_recording_files", "gauge",
		"Recording segments on disk.", float64(r.Files))
	d.scalar("polyemesis_recording_used_bytes", "gauge",
		"Bytes occupied by recording segments.", float64(r.UsedBytes))
	d.scalar("polyemesis_recording_free_bytes", "gauge",
		"Free space on the volume holding the recordings directory.", float64(r.FreeBytes))
	d.scalar("polyemesis_recording_total_bytes", "gauge",
		"Size of the volume holding the recordings directory.", float64(r.TotalBytes))
}

func renderHost(d *doc, h Host) {
	d.scalar("polyemesis_process_cpu_percent", "gauge",
		"CPU used by the polyemesis process, as a percentage of one core.", h.ProcCPUPercent)
	d.scalar("polyemesis_process_resident_memory_bytes", "gauge",
		"Resident set size of the polyemesis process.", float64(h.ProcMemBytes))
	d.scalar("polyemesis_host_cpu_percent", "gauge",
		"CPU used across the whole host, as a percentage.", h.CPUPercent)
	d.scalar("polyemesis_host_cpus", "gauge",
		"Logical CPUs on the host.", float64(h.NumCPU))
	d.scalar("polyemesis_host_memory_used_bytes", "gauge",
		"Memory in use across the whole host.", float64(h.MemUsedBytes))
	d.scalar("polyemesis_host_memory_total_bytes", "gauge",
		"Physical memory on the host.", float64(h.MemTotalBytes))
}

// ---------------------------------------------------------------- exposition

type label struct{ name, value string }

type doc struct{ b strings.Builder }

// family writes the HELP and TYPE header. Every sample of a family must follow
// its own header with no other family in between, or a strict parser rejects
// the whole scrape.
func (d *doc) family(name, typ, help string) {
	fmt.Fprintf(&d.b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
}

func (d *doc) sample(name string, value float64, labels ...label) {
	d.b.WriteString(name)
	if len(labels) > 0 {
		d.b.WriteByte('{')
		for i, l := range labels {
			if i > 0 {
				d.b.WriteByte(',')
			}
			d.b.WriteString(l.name)
			d.b.WriteString(`="`)
			d.b.WriteString(escapeLabel(l.value))
			d.b.WriteByte('"')
		}
		d.b.WriteByte('}')
	}
	d.b.WriteByte(' ')
	d.b.WriteString(formatValue(value))
	d.b.WriteByte('\n')
}

// scalar is the common case: a family holding one unlabelled sample.
func (d *doc) scalar(name, typ, help string, value float64) {
	d.family(name, typ, help)
	d.sample(name, value)
}

// escapeLabel quotes the three characters that would otherwise end the label
// value early. Destination names are free text typed by the user, so this is
// the difference between a stray quote and an unparseable scrape.
func escapeLabel(v string) string {
	if !strings.ContainsAny(v, "\\\"\n") {
		return v
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(v)
}

func formatValue(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

func boolValue(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
