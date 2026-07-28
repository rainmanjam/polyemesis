package engine

import (
	"context"
	"fmt"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
	"github.com/rainmanjam/polyemesis/internal/relay"
	"github.com/rainmanjam/polyemesis/internal/routing"
	"github.com/rainmanjam/polyemesis/internal/supervisor"
)

// The silence tier gives a video-only ingest an audio track.
//
// It is shaped exactly like a rendition, and for the same reason: one process,
// its own relay hub, and everything downstream reads that hub instead of the
// ingest's. From routing's point of view the source simply has one stereo
// track, so nothing below this tier — renditions, destinations, playout, the
// routing editor — has to know the track is synthetic.
//
//	ingest -> [silence] -> [rendition] -> destination
//
// The order matters. RenditionArgs uses `-map 0:a`, which on a zero-audio input
// maps nothing and produces a video-only rendition hub, so the silence tier has
// to sit UPSTREAM of the rendition rather than beside it.
//
// It only ever exists when the probe positively reported zero audio tracks. An
// unprobed ingest, a probe that failed, or a probe that found even one track
// all leave it absent — the tier can therefore never be the reason an operator's
// real audio is dropped, which is the one failure this product cannot have.

// silenceTier is the running synthetic-audio process.
type silenceTier struct {
	proc *supervisor.Process
	// hub is this tier's own relay, the one everything downstream reads.
	hub *relay.Hub
	// port and subName are its subscription on the INGEST hub, i.e. its input.
	port    int
	subName string
	// spec hashes what the command line depends on, so an unrelated settings
	// change never cycles it.
	spec string
	err  string
}

// silenceSubName is fixed: there is at most one of these.
const silenceSubName = "silence"

// synthTrack is the layout the tier publishes, and the layout every routing
// graph below it is compiled against.
//
// One stereo track at index 0. The title is engine state rather than stream
// metadata on purpose: the MPEG-TS muxer has no descriptor for a track title
// and discards it without a word, so the only place this can be said is here.
func synthTrack() routing.Source {
	return routing.Source{Tracks: []routing.Track{{
		Index: 0, Channels: 2, Codec: "aac", Layout: "stereo",
		Title: "Silence (synthesised)",
	}}}
}

// wantSilence reports whether the tier should be running, and its signature.
//
// An empty signature means "not wanted". The probe must have SUCCEEDED and
// reported zero tracks: `probed` false covers an ingest nobody has streamed to
// yet and a probe that could not run, and neither is evidence of anything. That
// asymmetry is the whole safety argument for this tier.
func (e *Engine) wantSilence(s db.Settings) string {
	if !s.Synth.SilenceOnVideoOnly {
		return ""
	}
	e.mu.RLock()
	probed, tracks := e.probed, len(e.source.Tracks)
	e.mu.RUnlock()
	if !probed || tracks != 0 {
		return ""
	}
	// Nothing configurable feeds the command line yet, so the signature is a
	// constant: the tier is started once and left alone until the ingest gains
	// a track or the operator switches it off.
	return hashStrings([]string{"silence", "stereo", "48000"})
}

// silenceHub is the tier's relay, or nil when it is not running.
func (e *Engine) silenceHub() *relay.Hub {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.silence == nil || e.silence.hub == nil {
		return nil
	}
	return e.silence.hub
}

// sourceHub is the relay every downstream consumer reads: the silence tier's
// when it is running, the ingest's otherwise.
//
// Every consumer that would otherwise name e.hub goes through here — renditions,
// passthrough destinations, playout's passthrough rung and the meters sidecar —
// so there is exactly one place that decides where "the source" is.
func (e *Engine) sourceHub() *relay.Hub {
	if h := e.silenceHub(); h != nil {
		return h
	}
	return e.hub
}

// effectiveSource is the track layout downstream graphs are compiled against.
//
// This is the line the author of the silence tier flagged as the most likely
// place to get it wrong. routing.Compile against the raw probe of a video-only
// ingest returns ErrNoAudio, which is why destinations cannot start on one
// today; once the tier is between them it must be compiled against the
// SYNTHETIC layout, not the probe.
func (e *Engine) effectiveSource() routing.Source {
	e.mu.RLock()
	src, live := e.source, e.silence != nil && e.silence.hub != nil
	e.mu.RUnlock()
	if live {
		src = synthTrack()
	}
	// The probe rebuilds e.source from scratch on every reconnect, so the
	// annotations are re-attached here rather than stored on it. The synthetic
	// layout gets them too: a single tier track can still be labelled, and
	// dropping them would make a role exclusion silently stop applying the
	// moment the ingest lost its video.
	return e.annotate(src)
}

// reconcileSilence starts, stops or leaves the tier alone.
//
// It must be called with nothing downstream reading the tier's hub: a consumer
// left on a hub that has just closed spins on a dead relay. reconcileOutputs
// therefore calls it after the destinations and renditions have come down and
// before they go back up, which is the same window rendition teardown uses.
func (e *Engine) reconcileSilence(want string) {
	e.mu.Lock()
	cur := e.silence
	e.mu.Unlock()

	if cur != nil && cur.spec == want && cur.err == "" {
		return
	}
	if cur != nil {
		e.mu.Lock()
		e.silence = nil
		e.mu.Unlock()
		e.teardownSilence(cur)
	}
	if want == "" {
		return
	}
	e.startSilence(want)
}

func (e *Engine) startSilence(spec string) {
	fail := func(err error) {
		// Recorded rather than returned, exactly as a rendition does: the
		// destinations downstream have to be told why they are not starting,
		// and the next reconcile retries.
		e.mu.Lock()
		e.silence = &silenceTier{err: err.Error()}
		e.mu.Unlock()
		e.log.Error("start silence tier", "err", err)
	}

	port, err := e.alloc.Allocate()
	if err != nil {
		fail(err)
		return
	}
	hub, err := relay.New(e.log, 0)
	if err != nil {
		e.alloc.Release(port)
		fail(err)
		return
	}

	in := e.hub.Subscribe(silenceSubName, port)
	args := ffmpeg.SilenceArgs(ffmpeg.SilenceSpec{
		InRelayURL:  in,
		OutRelayURL: hub.InputURL(),
	})

	proc := supervisor.New(e.log, supervisor.Spec{
		Name: silenceSubName, Kind: "silence", Bin: e.tools.FFmpeg, Args: args,
		// SilenceArgs passes -shortest, so this process exits with the ingest
		// and the supervisor's normal respawn is the reconnect path.
		AutoRestart: true, OnLog: e.onLog, OnState: e.onState, LogSink: logSink{e},
	})

	e.mu.Lock()
	// Shutdown may have run since this reconcile started; publishing under the
	// same lock Stop collects processes with is what keeps a late start from
	// becoming an orphan holding a UDP socket.
	if e.stopped {
		e.mu.Unlock()
		e.hub.Unsubscribe(silenceSubName)
		e.alloc.Release(port)
		_ = hub.Close()
		return
	}
	e.silence = &silenceTier{
		proc: proc, hub: hub, port: port, subName: silenceSubName, spec: spec,
	}
	e.mu.Unlock()

	proc.Start()
	e.log.Info("silence tier started",
		"reason", "the ingest probed with no audio tracks", "relayPort", hub.Port())
}

func (e *Engine) teardownSilence(t *silenceTier) {
	if t == nil {
		return
	}
	if t.proc != nil {
		ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		t.proc.Stop(ctx)
		cancel()
	}
	if t.subName != "" {
		e.hub.Unsubscribe(t.subName)
	}
	if t.port != 0 {
		e.alloc.Release(t.port)
	}
	// After the process, so it is never writing into a closed socket.
	if t.hub != nil {
		_ = t.hub.Close()
	}
}

// SilenceStatus is the tier as the dashboard reports it. Absent entirely when
// the tier is not running, which is the overwhelmingly common case.
type SilenceStatus struct {
	Reason  string             `json:"reason"`
	Error   string             `json:"error,omitempty"`
	Process *supervisor.Status `json:"process,omitempty"`
}

// Silence returns the tier's live state, or nil when there is none.
func (e *Engine) Silence() *SilenceStatus {
	e.mu.RLock()
	t := e.silence
	e.mu.RUnlock()
	if t == nil {
		return nil
	}
	return &SilenceStatus{
		Reason:  "the ingest carries no audio, so a silent stereo track is being synthesised",
		Error:   t.err,
		Process: procStatus(t.proc),
	}
}

// silenceLabel distinguishes the ingest hub from the silence tier's in an
// upstream label, so a playout variant on the source rung restarts onto the
// right relay when synthesis is switched on or off.
func (e *Engine) silenceLabel() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.silence == nil || e.silence.hub == nil {
		return "ingest"
	}
	return "silence:" + e.silence.spec
}

// silenceProblem is the reason a destination cannot run that the silence tier
// is responsible for, or nil when it is not in the way.
func (e *Engine) silenceProblem() error {
	e.mu.RLock()
	t := e.silence
	e.mu.RUnlock()
	if t == nil || t.hub != nil {
		return nil
	}
	if t.err != "" {
		return fmt.Errorf("the silence tier failed to start: %s", t.err)
	}
	return fmt.Errorf("the silence tier is not running")
}
