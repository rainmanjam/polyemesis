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
	// posts maps each captioned youtube_post destination to its caption
	// ingestion URL. It is a credential and is deliberately NOT part of sig.
	posts map[int64]string
	// policy is the programme's caption text policy. It is NOT part of sig
	// either: the running fanout reads it on every cue, so a change needs no
	// restart, and a save of it must not re-arm a degraded captioner.
	policy captions.TextPolicy
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
	if src != nil {
		in.policy = captions.TextPolicy{
			Vocabulary: src.CaptionPolicy.Vocabulary,
			Blocklist:  src.CaptionPolicy.Blocklist,
		}
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
		if mode == captions.ModeYouTubePOST {
			// Without a URL for the current key there is nowhere to post, so the
			// destination is not a caption target at all. db.SetDestinationEnabled
			// already refuses to start one, so this is a URL lost mid-show.
			u, err := e.store.CaptionIngestURL(d.ID, d.StreamKey)
			if err != nil || u == "" {
				continue
			}
			if in.posts == nil {
				in.posts = map[int64]string{}
			}
			in.posts[d.ID] = u
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
		// chosen to try again, possibly with a smaller model. The restart budget
		// starts again with it.
		e.captWarn = ""
		e.captBudget = captionRestartBudget{}
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

// captionPolicy is the programme's caption text policy as the last reconcile
// read it: its custom vocabulary and its masked words. Before any reconcile it
// is the empty policy, and text passes through unchanged.
func (e *Engine) captionPolicy() captions.TextPolicy {
	if p := e.captPolicy.Load(); p != nil {
		return *p
	}
	return captions.TextPolicy{}
}

// useCaptionPolicy makes p the policy the fanout applies from the next cue on.
func (e *Engine) useCaptionPolicy(p captions.TextPolicy) {
	e.captPolicy.Store(&p)
}

// openCueLog opens this programme's live cue log (captions.CueLogPath), or
// returns nil when there is nowhere to keep it. Not fatal: the log feeds the VOD
// track and the exports, and losing it must not cost the broadcast its captions.
func (e *Engine) openCueLog() *captions.CueLog {
	if e.cfg.DataDir == "" {
		return nil
	}
	path := captions.CueLogPath(e.cfg.DataDir, e.sourceID)
	l, err := captions.OpenCueLog(path, captions.DefaultCueLogBytes)
	if err != nil {
		e.log.Warn("live captions: no cue log", "path", path, "err", err)
		return nil
	}
	return l
}

// openBurnWriter returns the writer for this programme's burn file, the file a
// rendition burning captions in draws, or nil without a data directory.
func (e *Engine) openBurnWriter() *captions.BurnWriter {
	if e.cfg.DataDir == "" {
		return nil
	}
	return captions.NewBurnWriter(captions.BurnPath(e.cfg.DataDir, e.sourceID), nil)
}

// captionSidecar is the fanout's undelayed file path: the WebVTT file beside the
// HLS window (the public player's live track), the programme's cue log (the VOD
// track and the exports) and its burn file (burned-in tiers). Undelayed is right
// for the burn file too: a rendition sits before any destination's delay, so
// the burned caption is delayed with its picture. A write failure is logged and
// reported, and no file can stop another.
func (e *Engine) captionSidecar(vtt *transcribe.LiveVTT, cueLog *captions.CueLog, burn *captions.BurnWriter) func(transcribe.LiveCaption) error {
	return func(c transcribe.LiveCaption) error {
		err := vtt.Append(c)
		if err != nil {
			e.log.Warn("live captions: sidecar write failed", "err", err)
		}
		if lerr := cueLog.Append(c); lerr != nil {
			e.log.Warn("live captions: cue log write failed", "err", lerr)
			if err == nil {
				err = lerr
			}
		}
		if berr := burn.Show(c); berr != nil {
			e.log.Warn("live captions: burn file write failed", "err", berr)
			if err == nil {
				err = berr
			}
		}
		return err
	}
}

// captionFanout builds the emit boundary for one captioner. The policy is read
// per cue through captionPolicy, never captured here, so a save reaches a
// captioner that is already running.
func (e *Engine) captionFanout(publish func(transcribe.LiveCaption), sidecar func(transcribe.LiveCaption) error,
	route *captions.CueRouter) *captions.Fanout {
	return &captions.Fanout{
		PolicyNow: e.captionPolicy,
		Sidecar:   sidecar,
		Publish:   publish,
		Route:     route,
	}
}
