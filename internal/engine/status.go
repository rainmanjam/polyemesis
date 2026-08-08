// The status snapshot: everything the dashboard, the WebSocket push and the
// telemetry tick read out of a running engine.
//
// Split out of engine.go because it is the read side and nothing here changes
// what the engine is doing. Worth having in one place for a reason the split
// makes visible rather than fixes: Status, Renditions and SourceInfo each take
// e.mu separately and each go to the database on their own, so the snapshot is
// assembled from several instants rather than one. Nothing observed has needed
// it to be atomic yet; when something does, this file is where that gets fixed.
package engine

import (
	"maps"
	"slices"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/meters"
	"github.com/rainmanjam/polyemesis/internal/playout"
	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// ------------------------------------------------------------------- status

// DestStatus is one destination's live state, as the dashboard renders it.
type DestStatus struct {
	ID            int64              `json:"id"`
	Name          string             `json:"name"`
	Kind          db.DestKind        `json:"kind"`
	Platform      db.Platform        `json:"platform"`
	Enabled       bool               `json:"enabled"`
	Summary       string             `json:"summary"`
	Tracks        []int              `json:"tracks"`
	FilterComplex string             `json:"filterComplex"`
	Normalization routing.NormMode   `json:"normalization"`
	Warnings      []string           `json:"warnings"`
	Error         string             `json:"error,omitempty"`
	Process       *supervisor.Status `json:"process,omitempty"`
	// RenditionID is the shared encode this destination reads, nil for
	// passthrough. RenditionName is its label, empty for passthrough, so the
	// dashboard can group destinations under the encode they share.
	RenditionID   *int64 `json:"renditionId,omitempty"`
	RenditionName string `json:"renditionName,omitempty"`
	// BackupProcess is the redundant output's live state, absent when this
	// destination has none.
	//
	// Reported separately rather than folded into Process, because a backup
	// that has been dead for an hour beside a healthy primary is the single
	// state this feature must never hide: the operator believes they have
	// redundancy, which is worse than knowing they do not.
	BackupProcess *supervisor.Status `json:"backupProcess,omitempty"`
	// BackupError says why there is no backup when one was asked for.
	BackupError string `json:"backupError,omitempty"`
	// FacebookBroadcastID is the pre-announced scheduled broadcast, when one
	// exists. Carried on the live status rather than left on the stored row
	// because the card is where an operator looks, and a public event page
	// created on their behalf that they cannot reach is half a feature.
	FacebookBroadcastID string `json:"facebookBroadcastId,omitempty"`
}

// RenditionStatus is one shared video encode's live state.
//
// Consumers is the ref count the engine acted on: a rendition with none has no
// process, by design, and the dashboard should say so rather than show it as
// failed.
type RenditionStatus struct {
	ID           int64              `json:"id"`
	Name         string             `json:"name"`
	Width        int                `json:"width"`
	Height       int                `json:"height"`
	FPS          int                `json:"fps"`
	VideoBitrate int                `json:"videoBitrate"`
	Encoder      db.VideoEncoder    `json:"encoder"`
	Codec        string             `json:"codec"`
	Consumers    int                `json:"consumers"`
	RelayPort    int                `json:"relayPort,omitempty"`
	Error        string             `json:"error,omitempty"`
	Process      *supervisor.Status `json:"process,omitempty"`
}

// Status is the whole-system snapshot pushed over the WebSocket.
type Status struct {
	Ingest   *supervisor.Status `json:"ingest,omitempty"`
	Recorder *supervisor.Status `json:"recorder,omitempty"`
	Preview  *supervisor.Status `json:"preview,omitempty"`
	Meters   *supervisor.Status `json:"meters,omitempty"`
	// Silence is the synthetic-audio tier, absent unless it is running. Nothing
	// in the stream can say why a video-only ingest suddenly has audio — the
	// MPEG-TS muxer discards a track title — so this is the only place it can
	// be explained.
	Silence *SilenceStatus `json:"silence,omitempty"`
	// Failover is the source-selector tier, absent unless it is running. Which
	// source is on air has to be visible somewhere: a failover nobody notices is
	// how an operator discovers at the end of a broadcast that they streamed the
	// backup all night.
	Failover     *FailoverStatus   `json:"failover,omitempty"`
	Renditions   []RenditionStatus `json:"renditions"`
	Destinations []DestStatus      `json:"destinations"`
	Source       SourceInfo        `json:"source"`
	Relay        relay.Stats       `json:"relay"`
	// Loudness is the post-routing EBU R128 report for each monitored
	// destination — what the platform on the other end actually receives, which
	// is the only loudness figure it will judge the stream on.
	Loudness []meters.Report `json:"loudness"`
	// Clips is the rolling capture buffer's state.
	Clips ClipStatus `json:"clips"`
}

// procStatus is nil for a process that is not running, which the JSON omits.
func procStatus(p *supervisor.Process) *supervisor.Status {
	if p == nil {
		return nil
	}
	s := p.Status()
	return &s
}

// Renditions returns the live state of every shared encode.
//
// Every rendition row appears, running or not: one with no enabled destination
// is idle on purpose and must not read as broken.
func (e *Engine) Renditions() []RenditionStatus {
	rows, err := e.store.ListRenditions()
	if err != nil {
		return []RenditionStatus{}
	}
	counts, cerr := e.store.CountEnabledDestinationsByRendition()
	if cerr != nil {
		counts = map[int64]int{}
	}
	// The same fold reconcileOutputs does. Without it a tier kept alive purely
	// by a playout variant reads as "0 consumers" on a card that is showing a
	// running process, which is the dashboard calling its own decision a bug.
	for id, n := range playout.RenditionRefs(e.Settings().Playout) {
		counts[id] += n
	}

	e.mu.RLock()
	live := make(map[int64]*rendition, len(e.rends))
	for id, r := range e.rends {
		live[id] = r
	}
	e.mu.RUnlock()

	out := make([]RenditionStatus, 0, len(rows))
	for _, row := range rows {
		rs := RenditionStatus{
			ID: row.ID, Name: row.Name, Width: row.Width, Height: row.Height,
			FPS: row.FPS, VideoBitrate: row.VideoBitrate, Encoder: row.Encoder,
			Codec: row.Codec(), Consumers: counts[row.ID],
		}
		if r := live[row.ID]; r != nil {
			rs.Error = r.err
			rs.Process = procStatus(r.proc)
			if r.hub != nil {
				rs.RelayPort = r.hub.Port()
			}
		}
		out = append(out, rs)
	}
	return out
}

// Status assembles the current snapshot.
func (e *Engine) Status() Status {
	e.mu.RLock()
	ingest, recorder, preview, meters := e.ingest, e.recorder, e.preview, e.meters
	dests := make([]*destination, 0, len(e.dests))
	for _, d := range e.dests {
		dests = append(dests, d)
	}
	e.mu.RUnlock()

	st := Status{
		Source:       e.SourceInfo(),
		Relay:        e.hub.Stats(),
		Renditions:   e.Renditions(),
		Destinations: []DestStatus{},
		Loudness:     e.Loudness(),
		Clips:        e.ClipBuffer(),
	}
	st.Ingest = procStatus(ingest)
	st.Recorder = procStatus(recorder)
	st.Preview = procStatus(preview)
	st.Meters = procStatus(meters)
	st.Silence = e.Silence()
	st.Failover = e.Failover()

	names := make(map[int64]string, len(st.Renditions))
	for _, r := range st.Renditions {
		names[r.ID] = r.Name
	}

	// Indexed once instead of scanned twice per row. destByID is linear, and it
	// was called with the same argument twice for every destination on a
	// function that runs per WebSocket push and per telemetry tick.
	byID := make(map[int64]*destination, len(dests))
	for _, d := range dests {
		if d.row != nil {
			byID[d.row.ID] = d
		}
	}

	// Every destination row appears, running or not, so the dashboard shows a
	// disabled destination rather than silently omitting it.
	rows, err := e.store.ListDestinations()
	if err == nil {
		for _, row := range rows {
			ds := DestStatus{
				ID: row.ID, Name: row.Name, Kind: row.Kind,
				Platform: row.Platform, Enabled: row.Enabled,
				RenditionID: row.RenditionID,
			}
			if row.RenditionID != nil {
				ds.RenditionName = names[*row.RenditionID]
			}
			ds.FacebookBroadcastID = row.Facebook.BroadcastID
			// Looked up ONCE. This was two identical linear scans of the same
			// list with the same argument, per row, on a function that runs per
			// WebSocket push and per telemetry tick.
			live := byID[row.ID]
			if live != nil {
				ds.BackupProcess = procStatus(live.backup)
				ds.BackupError = live.backupErr
			}
			if live != nil {
				ds.Summary = live.compiled.Summary
				ds.Tracks = live.compiled.Tracks
				ds.FilterComplex = live.compiled.FilterComplex
				ds.Normalization = live.compiled.Normalization
				ds.Warnings = live.compiled.Warnings
				ds.Error = live.err
				ds.Process = procStatus(live.proc)
			} else if c, cerr := routing.Compile(row.Profile, e.Source()); cerr == nil {
				// Not running: still show what it *would* send, so the card is
				// informative before the stream is ever started.
				ds.Summary = c.Summary
				ds.Tracks = c.Tracks
				ds.FilterComplex = c.FilterComplex
				ds.Normalization = c.Normalization
				ds.Warnings = c.Warnings
			} else {
				ds.Error = cerr.Error()
			}
			st.Destinations = append(st.Destinations, ds)
		}
	}
	return st
}

func (e *Engine) destByID(list []*destination, id int64) *destination {
	for _, d := range list {
		if d.row != nil && d.row.ID == id {
			return d
		}
	}
	return nil
}

// Processes returns every supervised process, for the monitoring page.
func (e *Engine) Processes() []*supervisor.Process {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var out []*supervisor.Process
	procs := []*supervisor.Process{e.ingest, e.recorder, e.preview, e.meters}
	if e.silence != nil {
		procs = append(procs, e.silence.proc)
	}
	if e.backup != nil {
		procs = append(procs, e.backup.proc)
	}
	if e.playlist != nil {
		procs = append(procs, e.playlist.proc)
	}
	if e.sel != nil && e.sel.feed != nil {
		procs = append(procs, e.sel.feed.proc)
	}
	for _, p := range procs {
		if p != nil {
			out = append(out, p)
		}
	}
	for _, r := range e.rends {
		if r.proc != nil {
			out = append(out, r.proc)
		}
	}
	for _, d := range e.dests {
		if d.proc != nil {
			out = append(out, d.proc)
		}
		// The REDUNDANT output, which is not e.backup above -- that is the
		// source-side backup-ingest tier, a different thing entirely.
		//
		// Both API consumers go through this function, so without it
		// GET /processes/dest:<id>:backup/logs answered "no such process". The
		// card shows the backup's state, so an operator could see that
		// redundancy was broken and had no way to find out why, at the one
		// moment those logs exist for. destArgs' own justification for existing
		// is that a drifted backup argv "would be invisible until somebody
		// compared two argv strings on the monitoring page" -- and the backup's
		// argv was never on that page.
		if d.backup != nil {
			out = append(out, d.backup)
		}
	}
	// Sorted, because a map of analysers would otherwise reshuffle the
	// monitoring page on every poll.
	for _, id := range slices.Sorted(maps.Keys(e.loud)) {
		if m := e.loud[id]; m != nil && m.proc != nil {
			out = append(out, m.proc)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(e.playProcs)) {
		out = append(out, e.playProcs[name])
	}
	return out
}
