package engine

import (
	"github.com/rainmanjam/polyemesis/internal/alerts"
	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// The engine's one reading of "is this destination up", and the one projection
// of a destination into what the alert watcher, the webhook watcher and MQTT
// judge. Every surface that asked "is it running" used to answer for itself
// and the answers differed (error ignored, uptime required, reconnecting
// counted); they read these instead.

// processUp is a supervised process in the running state, nothing more.
func processUp(p *supervisor.Status) bool {
	return p != nil && p.State == supervisor.StateRunning
}

// processCarrying is processUp plus reconnecting. A process reconnecting to a
// platform is mid-broadcast from the operator's point of view, and restarting
// underneath it turns a recoverable blip into a dropped show: the reading
// OnAir uses for the upgrade gate.
func processCarrying(p *supervisor.Status) bool {
	return p != nil && (p.State == supervisor.StateRunning || p.State == supervisor.StateReconnecting)
}

// processDelivering is processUp once FFmpeg has reported uptime, i.e. media is
// actually flowing. A process that is merely running has nothing for a
// platform's health API to judge.
func processDelivering(p *supervisor.Status) bool {
	return processUp(p) && p.UptimeSec > 0
}

// IsUp reports whether the destination is up. A destination whose graph would
// not compile has no process and is as down as a failed one; one carrying an
// error is down whatever its process says.
func (d DestStatus) IsUp() bool {
	return d.Error == "" && processUp(d.Process)
}

// AlertState projects the destination into the shape the alert watcher, the
// webhook watcher and the MQTT publisher all judge. Nothing secret crosses it:
// a destination contributes its name, platform and error, never its URL or
// stream key.
func (d DestStatus) AlertState() alerts.DestState {
	ds := alerts.DestState{
		// A stopped destination playing its delayed tail out is still on the
		// air, so it is still ENABLED to every watcher until the tail ends:
		// the down edge -- and the YouTube end it triggers -- waits for the
		// last delayed second. See delayStillPublishing.
		ID: d.ID, Name: d.Name, Enabled: d.Enabled || delayStillPublishing(d),
		// Filling its delay: enabled, not running, and not down either.
		Buffering:    delayHoldsBack(d),
		DelaySeconds: d.DelaySeconds,
		Running:      d.IsUp(),
		Platform:     string(d.Platform),
		Error:        d.Error,
		// The classified failure, for the down alert's text (and so the
		// timeline) and the webhook's cause.
		Reason: d.Reason,
	}
	// FFmpeg has been reporting all three of these once a second per
	// destination since -progress was wired up. Left at their zero values the
	// watcher treats the rate as unknown and says nothing, which is what
	// happens for a destination with no process.
	if d.Process != nil {
		ds.OutTimeMS = d.Process.Progress.OutTimeMS
		ds.DropFrames = d.Process.Progress.DropFrames
		ds.DupFrames = d.Process.Progress.DupFrames
		ds.Restarts = d.Process.Restarts
		ds.RestartCause = string(d.Process.RestartCause)
	}
	return ds
}

// IngestState is the state of the engine's programme ingest: its child's state,
// or for a programme with no child the state of the listener that carries it.
// The metrics scrape reads this rather than deriving it. `ingest` is the
// caller's own Status().Ingest, so state and restarts come from one snapshot.
func (m *Manager) IngestState(e *Engine, ingest *supervisor.Status) supervisor.State {
	if in := ingest; in != nil {
		return in.State
	}
	set := e.Settings()
	return childlessIngestState(set.Ingest.Mode, e.SourceEnabled(),
		set.Listeners.SRTPort, m.ListenerBound(db.IngestSRT))
}

// childlessIngestState is the ingest state of a programme whose engine has no
// ingest process.
//
// AN SRT PROGRAMME NEVER HAS ONE. The manager's shared SRT listener hands
// datagrams straight to the programme's relay hub, so reading the state off
// the child alone reported every SRT programme stopped for its whole life:
// polyemesis_ingest_up 0 while its encoder was pushing, and the "failed" half
// of PolyemesisIngestDown unable to fire whatever happened to the port.
//
// The listener is that programme's ingest, so it carries the state, with the
// same meaning an RTMP or pull child's state has: running while it is able to
// receive -- waiting for the encoder or receiving it, which only the bitrate
// tells apart -- failed when a port is configured and nothing is bound to it,
// and stopped when the operator has turned the port off (port 0) or disabled
// the source (the listener refuses a disabled source's encoder). Those two are
// decisions rather than faults and must not page.
//
// Every other mode's ingest IS its child, so with none it is stopped, as
// before.
func childlessIngestState(mode db.IngestMode, enabled bool, srtPort int, srtBound bool) supervisor.State {
	if mode != db.IngestSRT || !enabled {
		return supervisor.StateStopped
	}
	switch {
	case srtBound:
		return supervisor.StateRunning
	case srtPort == 0:
		return supervisor.StateStopped
	default:
		return supervisor.StateFailed
	}
}
