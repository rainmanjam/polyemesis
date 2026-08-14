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

	"github.com/rainmanjam/polyemesis/internal/alerts"
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
	// StopWarning is set when the LAST stop of this destination ended on Stop's
	// deadline arm: SIGKILL issued, not waited for, the child possibly still
	// running and still publishing.
	//
	// It is a separate field from Error because it is not a failure of this
	// destination -- the row is fine, the stop was issued, the port and the
	// subscription have been released. It is a statement about what was NOT
	// observed, and the reason it has to be said out loud is that Process.State
	// reads "stopped" on both of Stop's arms and so cannot say it (#209).
	StopWarning string `json:"stopWarning,omitempty"`
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

	// MultitrackNote is what the Twitch Enhanced Broadcasting negotiation for
	// this destination's CURRENT run decided, in one sentence. Absent for every
	// destination that did not ask, which is nearly all of them.
	//
	// DELIBERATELY NOT IN Warnings. Warnings is what the card renders in amber
	// behind an alert triangle, and Twitch refuses any client without a
	// supported GPU -- so on most installs a fallback is what happens every
	// time, for ever. Putting it there would train an operator to read a
	// perfectly normal broadcast as broken, which is the failure
	// multitrack.Outcome refuses an error return to avoid.
	MultitrackNote string `json:"multitrackNote,omitempty"`
	// MultitrackVerdict is "negotiated", "advisory" or "refused", so the card
	// can tell "we asked and were turned down" from "we never asked".
	MultitrackVerdict string `json:"multitrackVerdict,omitempty"`
	// MultitrackDivergences are the places Twitch's configuration departs from
	// what this destination asked for. ADVISORY ONLY: they annotate a
	// destination that IS publishing and must never be rendered as faults.
	MultitrackDivergences []DivergenceStatus `json:"multitrackDivergences,omitempty"`
	// VODAudioDropped says why this destination's second (VOD) audio mix is not
	// on the wire. Empty when there is no second mix, and empty when there is
	// one and it is going out.
	//
	// It exists because the alternative was silence: the engine compiled the
	// pair on the profile alone and pushed two audio tracks at Twitch's
	// one-track RTMP ingest with nothing anywhere saying so. Not a warning
	// either -- the destination is publishing correctly, one track short of
	// what was configured, and the operator's fix is a toggle rather than a
	// repair.
	VODAudioDropped string `json:"vodAudioDropped,omitempty"`
}

// DivergenceStatus is one advisory note about the negotiated configuration.
// Mirrors multitrack.Divergence rather than embedding it, so the wire shape of
// the status payload does not move when that package's internals do.
type DivergenceStatus struct {
	Field  string `json:"field"`
	Detail string `json:"detail"`
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
	// ONE acquisition for everything e.mu owns. Status used to take it here and
	// again inside SourceInfo, and a reconcile landing between the two paired
	// the ingest's state with a layout from a different instant -- "running"
	// beside a track list that had just been invalidated, which reads as a
	// fault that is not there.
	e.mu.RLock()
	ingest, recorder, preview, meters := e.ingest, e.recorder, e.preview, e.meters
	dests := make([]*destination, 0, len(e.dests))
	for _, d := range e.dests {
		dests = append(dests, d)
	}
	source := e.sourceInfoLocked()
	e.mu.RUnlock()

	st := Status{
		Source:       source,
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
			// Read for EVERY row, running or not. The rows this matters for are
			// precisely the ones with no live entry left: a destination that was
			// stopped is gone from e.dests, so anything hung off `live` below
			// would be silently absent exactly when the warning is true.
			ds.StopWarning, _ = e.StopUnreaped(row.ID)
			// Looked up ONCE. This was two identical linear scans of the same
			// list with the same argument, per row, on a function that runs per
			// WebSocket push and per telemetry tick.
			live := byID[row.ID]
			if live != nil {
				ds.BackupProcess = procStatus(live.backup)
				ds.BackupError = live.backupErr
				// What THIS run negotiated, which is the only run an operator
				// can ask about. A destination that is not running has nothing
				// to report here rather than a stale answer from last time --
				// the negotiation is per go-live and a minted key expires with
				// the broadcast it was minted for.
				ds.MultitrackNote = live.multitrack.Note
				if live.multitrack.Asked {
					ds.MultitrackVerdict = string(live.multitrack.Verdict)
				}
				for _, d := range live.multitrack.Divergences {
					ds.MultitrackDivergences = append(ds.MultitrackDivergences,
						DivergenceStatus{Field: d.Field, Detail: d.Detail})
				}
				ds.VODAudioDropped = live.vodDropped
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
			// The compiled summary describes the MIX, and for a destination
			// that copies its audio the mix does not happen: its line would end
			// "→ stereo" about tracks that are being forwarded untouched, at
			// whatever width they arrived. Rewritten here rather than inside
			// Compile because copy is a property of the destination and Compile
			// is only shown the profile. FilterComplex is left as compiled and
			// not blanked -- it is what the graph WOULD be, the expert preview
			// already shows the real argv, and an empty box on the card would
			// read as a failure to compile rather than as a mode.
			if row.Audio.Copy && ds.Error == "" {
				ds.Summary = routing.CopySummary(ds.Tracks)
			}
			// A destination whose stored stream key would not decrypt on this
			// machine. It is disabled by the store, so nothing above this line
			// has anything to say about it -- there is no process, no error and
			// no compile failure, and without this the card would be a healthy
			// looking destination that is simply switched off, which is the one
			// reading that would send somebody hunting in the wrong place.
			//
			// A warning rather than Error: the row is not broken and the
			// routing still compiles, the credential is unreadable. Warnings is
			// also the field the card already renders as "needs attention".
			//
			// slices.Clip first, because ds.Warnings above is the COMPILED
			// slice, shared with routing's cache and with every other
			// destination on the same profile: appending in place would write
			// this destination's warning into theirs the moment there is spare
			// capacity. Clip forces the append to allocate.
			if row.KeyUnreadable != "" {
				ds.Warnings = append(slices.Clip(ds.Warnings), row.KeyUnreadable)
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

// ScrubDestinationText masks a destination's own declared credential literals
// in a status string, then applies the residual pass.
//
// IT EXISTS FOR ONE SINK AND THE SINK IS THE REASON (#160). DestStatus.Error
// reaches cmd/polyemesis/mqtt.go, which publishes it to a RETAINED topic. A
// retained message is not a disclosure to whoever happened to be subscribed
// when it was sent; it persists on the broker and is delivered to EVERY FUTURE
// SUBSCRIBER, so a credential that lands there is not fixable after the fact by
// rotating a token -- the old value is still sitting on somebody else's broker.
// That is a different severity from the same string appearing in an API
// response, and it is why this one field gets a pass the API path does not.
//
// The credential risk in the text itself is LOW: the strings that reach
// DestStatus.Error are compile diagnostics and start failures, not URLs. Low is
// not zero, and the coverage here was zero, on a sink that has no expiry. One
// function is the right price.
//
// The two passes are the same shape and the same order as
// supervisor.(*Process).scrub, for the same reasons: the exact set is the
// boundary and cannot be defeated by how the credential was spelled; Redact is
// the residual for what nobody declared. See the doc on alerts.Redact for what
// that second pass can and cannot do.
//
// Applied at the PUBLISH SITE rather than inside Status, deliberately. The same
// DestStatus.Error is served to the admin console, where masking a compile
// diagnostic would cost an operator the message and buy nothing -- that
// response has a principal, an expiry and a session behind it. The retained
// topic has none of the three.
//
// A destination with no RUNNING entry gets the residual only. That is correct
// rather than a gap: without a live row there is no compiled argv, so the text
// came from routing.Compile, which is handed the profile and the source layout
// and never sees a credential.
func (e *Engine) ScrubDestinationText(id int64, text string) string {
	if text == "" {
		return text
	}
	e.mu.RLock()
	var row *db.Destination
	if d := e.dests[id]; d != nil {
		row = d.row
	}
	e.mu.RUnlock()
	if row == nil {
		return alerts.Redact(text)
	}
	return alerts.Redact(alerts.NewSecretSet(nil, destSecrets(row)...).Scrub(text))
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
