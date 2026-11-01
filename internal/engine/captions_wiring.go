package engine

import (
	"fmt"
	"sort"
	"time"

	"github.com/rainmanjam/polyemesis/internal/captions"
	"github.com/rainmanjam/polyemesis/internal/transcribe"
)

// captionIntent is what this programme's destinations ask of the captioner.
//
// A destination asks for captions when it is enabled and its caption mode is one
// its platform accepts. The captioner's track comes from the programme (the
// source row), with nil meaning the automatic choice.
type captionIntent struct {
	on      bool
	track   int
	targets []captions.CueTarget
	// sig identifies the intent. It changes only when the operator saves a
	// caption-relevant setting, which is what re-arms the switch after a degrade.
	sig string
}

// captionIntentNow reads the programme's destinations and track. Without a store
// there is nothing to read and the intent is off, so a partly built Engine in a
// test gets a quiet answer rather than a panic.
func (e *Engine) captionIntentNow() (captionIntent, error) {
	in := captionIntent{track: transcribe.LiveTrackAuto}
	if e.store == nil {
		in.sig = intentSig(in)
		return in, nil
	}
	src, err := e.store.GetSource(e.sourceID)
	if err != nil {
		return in, err
	}
	if src != nil && src.CaptionTrack != nil {
		in.track = *src.CaptionTrack
	}
	dests, err := e.store.ListDestinationsBySource(e.sourceID)
	if err != nil {
		return in, err
	}
	for _, d := range dests {
		if !d.Enabled {
			continue
		}
		mode := captions.Mode(d.CaptionMode)
		if mode == captions.ModeOff || mode == "" {
			continue
		}
		if !captions.Allowed(string(d.Platform), mode, captions.FacebookSEIEnabled()) {
			continue
		}
		in.targets = append(in.targets, captions.CueTarget{
			ID:    d.ID,
			Delay: time.Duration(d.DelaySeconds) * time.Second,
		})
	}
	in.on = len(in.targets) > 0
	sort.Slice(in.targets, func(i, j int) bool { return in.targets[i].ID < in.targets[j].ID })
	in.sig = intentSig(in)
	return in, nil
}

// intentSig is the stable identity of an intent: the switch, the track and each
// captioned destination with its delay. The delay is part of it because a delay
// change is an operator save, which legitimately re-arms the switch.
func intentSig(in captionIntent) string {
	s := fmt.Sprintf("on=%t track=%d", in.on, in.track)
	for _, t := range in.targets {
		s += fmt.Sprintf(" dest=%d delay=%s", t.ID, t.Delay)
	}
	return s
}

// applyCaptionIntent sets the captioner's switch and track from the intent, but
// only when the intent has changed since the last reconcile. Without that latch,
// an every-reconcile re-derivation would switch captions back on right after the
// health guard switched them off to protect the stream, and restart the captioner.
//
// The configuration goes through setCaptionConfig, the same normalize and
// validate step SetLiveCaptions uses, so a zero Window or Step never reaches the
// captioner from this path.
func (e *Engine) applyCaptionIntent(in captionIntent) {
	e.mu.Lock()
	e.captTargets = in.targets
	if in.sig == e.captDesired {
		e.mu.Unlock()
		return
	}
	e.captDesired = in.sig
	cfg := e.captCfg
	e.mu.Unlock()

	cfg.Enabled, cfg.Track = in.on, in.track
	if err := e.setCaptionConfig(cfg); err != nil {
		e.captionsFailed(err.Error())
	}
}

// setCaptionConfig normalizes, validates and stores the captioner's configuration
// and its switch. It does not reconcile: SetLiveCaptions calls Reconcile after it,
// and the reconcile path calls it directly, because Reconcile is not reentrant.
func (e *Engine) setCaptionConfig(cfg transcribe.LiveConfig) error {
	cfg = cfg.Normalized()
	if err := cfg.Validate(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.captCfg, e.captOn = cfg, cfg.Enabled
	if cfg.Enabled {
		// A fresh decision clears the last refusal. The operator has seen it and
		// chosen to try again, possibly with a smaller model.
		e.captWarn = ""
	}
	return nil
}

// syncCaptionRoute gives the live route the current delays. It is cheap and runs
// on every reconcile, so a changed delay takes effect without restarting the
// recogniser.
func (e *Engine) syncCaptionRoute(targets []captions.CueTarget) {
	e.mu.Lock()
	route := e.captRoute
	e.captTargets = targets
	e.mu.Unlock()
	if route != nil {
		route.SetTargets(targets)
	}
}

// captionTargets is the destination set a new route starts with.
func (e *Engine) captionTargets() []captions.CueTarget {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.captTargets
}

// captionPolicy is the operator's text policy for live captions. No policy is
// stored yet, so it is the empty policy and text passes through unchanged. The
// emit boundary is already in place, so storing the policy changes one function.
func (e *Engine) captionPolicy() captions.TextPolicy {
	return captions.TextPolicy{}
}
