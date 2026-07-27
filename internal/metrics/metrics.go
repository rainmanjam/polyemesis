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

	Ingest       Process
	Destinations []Destination
	Relay        Relay
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

	renderIngest(&d, s.Ingest)
	renderDestinations(&d, s.Destinations)
	renderRelay(&d, s.Relay)
	renderRecordings(&d, s.Recordings)
	renderHost(&d, s.Host)

	return d.b.String()
}

func renderIngest(d *doc, p Process) {
	d.scalar("polyemesis_ingest_up", "gauge",
		"1 when the ingest process is running.", boolValue(p.State == stateRunning))

	d.family("polyemesis_ingest_state", "gauge",
		"Ingest process state; 1 for the state it is currently in.")
	for _, st := range processStates {
		d.sample("polyemesis_ingest_state", boolValue(p.State == st), label{"state", st})
	}

	d.scalar("polyemesis_ingest_bitrate_bits_per_second", "gauge",
		"Bitrate currently arriving from the streamer.", p.BitrateKbps*1000)
	d.scalar("polyemesis_ingest_restarts_total", "counter",
		"Restarts of the ingest process since the server started.", float64(p.Restarts))
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

func renderRelay(d *doc, r Relay) {
	d.scalar("polyemesis_relay_subscribers", "gauge",
		"Consumers currently attached to the relay hub.", float64(r.Subscribers))
	d.scalar("polyemesis_relay_received_packets_total", "counter",
		"Datagrams received from the ingest.", float64(r.RxPackets))
	d.scalar("polyemesis_relay_received_bytes_total", "counter",
		"Bytes received from the ingest.", float64(r.RxBytes))
	d.scalar("polyemesis_relay_transmitted_packets_total", "counter",
		"Datagrams replicated to subscribers.", float64(r.TxPackets))
	d.scalar("polyemesis_relay_dropped_packets_total", "counter",
		"Datagrams the relay failed to deliver to a subscriber.", float64(r.Dropped))
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
