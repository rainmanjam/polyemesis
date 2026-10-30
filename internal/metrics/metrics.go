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

	"github.com/rainmanjam/polyemesis/internal/failreason"
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

	// Alerts is what the alert notifiers have delivered. Nil when there is no
	// notifier to ask -- a process with no engine manager -- and then the
	// families are omitted rather than published as zeros that would read as
	// "nothing has failed".
	Alerts *AlertDeliveries
}

// AlertDeliveries is the alert-rule delivery outcome, summed over every
// engine's notifier (alert rules are install-wide; see api.alertStats).
//
// On the scrape because a webhook that has stopped accepting deliveries is the
// one failure the alert path cannot report through itself. Only the
// automation page showed it, and only to somebody who opened it.
type AlertDeliveries struct {
	// Sent and Failed count deliveries, not events: one delivery carries every
	// event coalesced into it, and a failure is after the whole retry budget.
	// Each notifier restarts from zero with its engine, which Prometheus reads
	// as a counter reset.
	Sent, Failed int64
	// LastSent is the newest successful delivery, zero when there has been
	// none. Zero omits the sample: see renderAlerts.
	LastSent time.Time
}

// Process is one supervised FFmpeg child.
type Process struct {
	State    string
	Restarts int
	// BitrateKbps is kilobits per second, the unit FFmpeg and the UI use. It is
	// converted to bits per second on the way out.
	BitrateKbps float64
	DropFrames  int64
	// Stalled is the engine's verdict that the destination is running but its
	// output has not moved for supervisor.StallAfter while the source is
	// arriving (engine.DestStatus.Stalled). Not the supervisor's per-process
	// flag, which is also set on every destination while the ingest is lost.
	// See renderDestinations for what it does to _up.
	Stalled bool
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
	// NoEngine says the programme is in the sources table and nothing is
	// running for it: its engine failed to build or to start, and Sync logged
	// and carried on so the other programmes stayed on air. Its Process is
	// then stopped and its relay zero, which _up already says -- this is what
	// says WHY, so "the encoder is not sending" and "there is nothing here to
	// receive it" do not read the same.
	//
	// Negative so the zero value is the ordinary case: every Ingest built from
	// a running engine, which is all of them but one sweep, is right without
	// setting it.
	NoEngine bool
	// Failover is the programme's source-selector tier, nil while it is off.
	Failover *Failover
	// BondedLinks is how many network links the programme's encoder is
	// bonding through the SRTLA receiver right now; 0 when it is not bonding.
	BondedLinks int
}

// Failover is a programme's selector tier and the primary's link health (#6).
type Failover struct {
	// Active is the source on air: primary, backup, playlist or slate.
	Active string
	// Degraded is the primary's link judged degraded right now.
	Degraded bool
	// RTTMs and LossPercent are the primary's SRT link figures, nil when not
	// measured -- an RTMP or pull primary, or degraded-link failover off.
	RTTMs       *float64
	LossPercent *float64
}

// failoverSources is every kind the selector can put on air, emitted for every
// programme with a tier so that `source="slate"` sits at 0 rather than
// appearing only once the slate has been used.
var failoverSources = []string{"primary", "backup", "playlist", "slate"}

// Destination is one output, plus the labels identifying it.
type Destination struct {
	Process
	// Delay is the broadcast delay (#11): configured seconds, and the buffer
	// while a line exists. Zero for a destination with no delay.
	Delay    DestDelay
	ID       int64
	Name     string
	Kind     string
	Platform string
	Enabled  bool
	// SourceID is the programme this destination carries, nil when the row
	// names none. Rendered on _info alone, as source_id, for the one join the
	// destination series cannot otherwise make: to that programme's ingest
	// series, whose id label is the SOURCE's id. See renderDestinations.
	SourceID *int64
	// OutTimeMS and OutputBytes are how far this run's output has got, in
	// media time and in bytes, from FFmpeg's progress report. Both restart
	// from zero with the process, which is a counter reset to Prometheus.
	//
	// They are here because nothing else on the scrape moves when a sink stops
	// reading. The child stays running, so _up stays 1 and _restarts_total
	// stays flat, and the bitrate is FFmpeg's whole-run average from the last
	// report before the write blocked -- frozen, and non-zero. These two stop
	// advancing the moment delivery does.
	OutTimeMS   int64
	OutputBytes int64

	// Cause is why the destination is failing now (engine.DestStatus.Reason),
	// empty while nothing is wrong. Held is the supervisor having stopped
	// retrying it after repeated quick rejections.
	//
	// ONLY THE CODE, never Reason.Line. The line is FFmpeg's stderr, which for
	// a refused publish is the line carrying the publish URL, and a label is
	// copied into every time-series database that scrapes this. The code is a
	// fixed enum, so the label's cardinality is bounded too; a value outside
	// failreason.AllCauses() is rendered as unknown rather than passed through.
	Cause failreason.Cause
	Held  bool
	// Failures counts this destination's failed runs by cause, from the
	// supervisor (supervisor.Status.Failures). It restarts from zero with the
	// process, which is a counter reset to Prometheus.
	Failures map[failreason.Cause]int
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
	renderFailover(&d, s.Ingests)
	renderRecordings(&d, s.Recordings)
	renderHost(&d, s.Host)
	renderAlerts(&d, s.Alerts)

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
		"1 when the programme can receive: its ingest process is running, or for an SRT source the shared SRT listener is bound.")
	for _, in := range sorted {
		d.sample("polyemesis_ingest_up", boolValue(in.State == stateRunning), ingestIdent(in)...)
	}

	d.family("polyemesis_ingest_state", "gauge",
		"Ingest state; 1 for the state it is currently in. An SRT source has no ingest process, and reads its shared listener: running while bound, failed when its port is not bound, stopped when the port is off.")
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

	// Per programme, beside _up rather than folded into it: _up is 0 for a
	// listener waiting on a streamer too, which is normal between shows. This
	// is 0 only when the server itself has nothing running for a configured
	// programme, which is never normal.
	d.family("polyemesis_source_engine_up", "gauge",
		"1 when the programme has a running engine; 0 when its engine failed to build or start.")
	for _, in := range sorted {
		d.sample("polyemesis_source_engine_up", boolValue(!in.NoEngine), ingestIdent(in)...)
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
		// source_id is what lets a destination query ask about ITS programme's
		// ingest: destination and ingest series both carry id, but they are
		// ids of different things, so nothing joined them and a "moving slowly"
		// alert could not leave out a destination whose source had gone. On
		// _info, not on every series, with kind and platform: a descriptive
		// label joined in when wanted, and empty for a row with no programme.
		src := ""
		if dest.SourceID != nil {
			src = strconv.FormatInt(*dest.SourceID, 10)
		}
		d.sample("polyemesis_destination_info", 1, append(ident(dest),
			label{"kind", dest.Kind}, label{"platform", dest.Platform}, label{"source_id", src})...)
	}

	d.family("polyemesis_destination_enabled", "gauge",
		"1 when the destination is configured to run.")
	for _, dest := range sorted {
		d.sample("polyemesis_destination_enabled", boolValue(dest.Enabled), ident(dest)...)
	}

	// UP MEANS DELIVERING. It used to mean "the process is running", and a
	// sink that stops reading leaves the process running -- so a stalled
	// destination read up=1, flat restarts and a frozen non-zero bitrate for as
	// long as the stall lasted, and the dashboard an operator reads said
	// healthy (exploratory row 7). The state series still say running, which
	// is true of the process; _up and _stalled say what happened to the
	// stream. It is the meaning the destination.up hook already has --
	// including its exception: a stall because the ingest is lost is not
	// counted, so a lost source does not read as every platform failing.
	d.family("polyemesis_destination_up", "gauge",
		"1 when the destination's FFmpeg process is running and its output is moving.")
	for _, dest := range sorted {
		d.sample("polyemesis_destination_up", boolValue(delivering(dest)), ident(dest)...)
	}

	d.family("polyemesis_destination_stalled", "gauge",
		"1 when the destination's process is running but its output has not advanced for 5 seconds.")
	for _, dest := range sorted {
		d.sample("polyemesis_destination_stalled", boolValue(dest.State == stateRunning && dest.Stalled), ident(dest)...)
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
		"Average bitrate FFmpeg reports for the destination's current run, and 0 while it is stalled. rate() of polyemesis_destination_output_bytes_total is the bitrate being sent now.")
	for _, dest := range sorted {
		// FFmpeg's figure is a run average and freezes non-zero in a stall.
		// The supervisor already reports 0 then; saying it again here keeps a
		// snapshot built any other way from rendering the frozen number.
		bps := dest.BitrateKbps * 1000
		if dest.Stalled {
			bps = 0
		}
		d.sample("polyemesis_destination_bitrate_bits_per_second", bps, ident(dest)...)
	}

	d.family("polyemesis_destination_output_seconds_total", "counter",
		"Media time the destination's current run has delivered, in seconds. rate() is the delivery speed: about 1 while keeping up, 0 while stalled.")
	for _, dest := range sorted {
		d.sample("polyemesis_destination_output_seconds_total", float64(dest.OutTimeMS)/1000, ident(dest)...)
	}

	d.family("polyemesis_destination_output_bytes_total", "counter",
		"Bytes the destination's current run has written.")
	for _, dest := range sorted {
		d.sample("polyemesis_destination_output_bytes_total", float64(dest.OutputBytes), ident(dest)...)
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

	renderDestinationReasons(d, sorted, ident)
}

// renderDestinationReasons emits why each destination is failing (#20).
//
// EVERY CAUSE FOR EVERY DESTINATION, at 0 when it does not apply, for the same
// reason the state series do -- and for one more that is specific to the
// counter. A refused key is held after three quick rejections, which can all
// land inside one scrape interval: a series first seen at 3 has no earlier
// sample, so increase() over it reads 0 and the failure never shows. Present at
// 0 from the destination's first scrape, the same three failures read as 3.
//
// Bounded: nine causes times the destinations configured, and the cause is a
// code from a fixed list, never text from FFmpeg.
func renderDestinationReasons(d *doc, sorted []Destination, ident func(Destination) []label) {
	causes := failreason.AllCauses()

	d.family("polyemesis_destination_failures_total", "counter",
		"Runs of the destination's process that ended in a failure, by cause, since the process was created.")
	for _, dest := range sorted {
		counts := map[failreason.Cause]int{}
		for c, n := range dest.Failures {
			counts[knownCause(c)] += n
		}
		for _, c := range causes {
			d.sample("polyemesis_destination_failures_total", float64(counts[c]),
				append(ident(dest), label{"cause", string(c)})...)
		}
	}

	d.family("polyemesis_destination_failure_reason", "gauge",
		"Why the destination is failing now; 1 for the current cause, all 0 while nothing is wrong.")
	for _, dest := range sorted {
		current := failreason.Cause("")
		if dest.Cause != "" {
			current = knownCause(dest.Cause)
		}
		for _, c := range causes {
			d.sample("polyemesis_destination_failure_reason", boolValue(current == c),
				append(ident(dest), label{"cause", string(c)})...)
		}
	}

	d.family("polyemesis_destination_held", "gauge",
		"1 when the destination has stopped retrying after repeated quick key or codec rejections; Retry clears it.")
	for _, dest := range sorted {
		d.sample("polyemesis_destination_held", boolValue(dest.Held), ident(dest)...)
	}

	renderDelay(d, sorted, ident)
}

// DestDelay is one destination's broadcast delay on the scrape.
type DestDelay struct {
	Seconds int
	// Fill is the share of the delay the buffer holds, 0 to 1.
	Fill         float64
	MemBytes     int64
	DiskBytes    int64
	DroppedBytes uint64
	// Draining is a stopped destination still airing its delayed tail.
	Draining bool
}

// renderDelay is the broadcast delay's series (#11). The bytes are the bound
// the design promises -- RAM and disk, separately -- so an operator can
// alert on the disk filling before the buffer has to drop.
func renderDelay(d *doc, sorted []Destination, ident func(Destination) []label) {
	d.family("polyemesis_destination_delay_seconds", "gauge",
		"The destination's configured broadcast delay; 0 when it has none.")
	for _, dest := range sorted {
		d.sample("polyemesis_destination_delay_seconds", float64(dest.Delay.Seconds), ident(dest)...)
	}
	d.family("polyemesis_destination_delay_buffer_fill", "gauge",
		"How much of the broadcast delay the buffer holds, 0 to 1; 1 while on air.")
	for _, dest := range sorted {
		d.sample("polyemesis_destination_delay_buffer_fill", dest.Delay.Fill, ident(dest)...)
	}
	d.family("polyemesis_destination_delay_buffer_bytes", "gauge",
		"Bytes the broadcast delay buffer holds, by tier: memory or disk.")
	for _, dest := range sorted {
		d.sample("polyemesis_destination_delay_buffer_bytes", float64(dest.Delay.MemBytes),
			append(ident(dest), label{"tier", "memory"})...)
		d.sample("polyemesis_destination_delay_buffer_bytes", float64(dest.Delay.DiskBytes),
			append(ident(dest), label{"tier", "disk"})...)
	}
	d.family("polyemesis_destination_delay_dropped_bytes_total", "counter",
		"Bytes the broadcast delay buffer dropped for want of room, keeping the delay; resets when the destination restarts.")
	for _, dest := range sorted {
		d.sample("polyemesis_destination_delay_dropped_bytes_total", float64(dest.Delay.DroppedBytes), ident(dest)...)
	}
	d.family("polyemesis_destination_delay_draining", "gauge",
		"1 while a stopped destination is still airing its delayed tail.")
	for _, dest := range sorted {
		d.sample("polyemesis_destination_delay_draining", boolValue(dest.Delay.Draining), ident(dest)...)
	}
}

// knownCause folds anything outside the published enum into unknown, so a
// label value is always one of the nine codes whatever a caller passes in.
func knownCause(c failreason.Cause) failreason.Cause {
	for _, k := range failreason.AllCauses() {
		if c == k {
			return c
		}
	}
	return failreason.Unknown
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

// renderFailover emits the selector tier and the primary's link health.
//
// THE DEGRADED GAUGE IS EVERY PROGRAMME'S, at 0 when the tier is off, for the
// usual reason: an alert on `polyemesis_failover_degraded == 1` must evaluate
// before the first degradation. The link figures are the opposite case and are
// absent until measured, because 0 ms RTT and 0% loss are claims about a link.
func renderFailover(d *doc, ins []Ingest) {
	sorted := sortedIngests(ins)

	d.family("polyemesis_failover_degraded", "gauge",
		"1 while the programme's primary link is judged degraded by degraded-link failover; 0 otherwise, including when failover is off.")
	for _, in := range sorted {
		d.sample("polyemesis_failover_degraded", boolValue(in.Failover != nil && in.Failover.Degraded), ingestIdent(in)...)
	}

	d.family("polyemesis_failover_active", "gauge",
		"Source on air for a programme with failover on; 1 for the active source. Absent while failover is off.")
	for _, in := range sorted {
		if in.Failover == nil {
			continue
		}
		for _, src := range failoverSources {
			d.sample("polyemesis_failover_active", boolValue(in.Failover.Active == src),
				append(ingestIdent(in), label{"source", src})...)
		}
	}

	d.family("polyemesis_ingest_link_rtt_seconds", "gauge",
		"Smoothed SRT round-trip time of the programme's primary encoder. Absent unless the primary is SRT or SRTLA and degraded-link failover is on.")
	for _, in := range sorted {
		if in.Failover != nil && in.Failover.RTTMs != nil {
			d.sample("polyemesis_ingest_link_rtt_seconds", *in.Failover.RTTMs/1000, ingestIdent(in)...)
		}
	}

	d.family("polyemesis_srtla_links", "gauge",
		"Network links the programme's encoder is bonding through the SRTLA receiver; 0 when it is not bonding.")
	for _, in := range sorted {
		d.sample("polyemesis_srtla_links", float64(in.BondedLinks), ingestIdent(in)...)
	}

	d.family("polyemesis_ingest_link_loss_ratio", "gauge",
		"Share of SRT data packets found missing over the last interval, 0-1. Absent under the same conditions as the RTT.")
	for _, in := range sorted {
		if in.Failover != nil && in.Failover.LossPercent != nil {
			d.sample("polyemesis_ingest_link_loss_ratio", *in.Failover.LossPercent/100, ingestIdent(in)...)
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

// renderAlerts emits the delivery counters, and the last success only once
// there has been one. A timestamp of 0 would make `time() - last_success` read
// as fifty-odd years on every install that has simply had nothing to say yet;
// an absent sample is a state a rule can ask about with absent() on purpose.
func renderAlerts(d *doc, a *AlertDeliveries) {
	if a == nil {
		return
	}
	d.family("polyemesis_alert_deliveries_total", "counter",
		"Alert-rule deliveries since the server started, by outcome. A failure is counted after the retry budget.")
	d.sample("polyemesis_alert_deliveries_total", float64(a.Sent), label{"result", "sent"})
	d.sample("polyemesis_alert_deliveries_total", float64(a.Failed), label{"result", "failed"})

	d.family("polyemesis_alert_last_success_timestamp_seconds", "gauge",
		"Unix time of the newest alert-rule delivery that succeeded; absent until one has.")
	if !a.LastSent.IsZero() {
		d.sample("polyemesis_alert_last_success_timestamp_seconds", float64(a.LastSent.Unix()))
	}
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

// delivering is what polyemesis_destination_up reports: running, and not
// stalled.
func delivering(dest Destination) bool {
	return dest.State == stateRunning && !dest.Stalled
}

func boolValue(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
