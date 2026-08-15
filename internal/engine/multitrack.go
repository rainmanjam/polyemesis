// Twitch Enhanced Broadcasting, wired into the go-live path.
//
// EXPERIMENTAL, AND THIS FILE IS THE PART THE LABEL IS ACTUALLY ABOUT. The
// negotiation itself is not experimental any more: internal/multitrack's
// live_test.go runs it against ingest.twitch.tv on every run and Twitch
// answers — a supported-GPU inventory is accepted, a VOD audio track is
// granted, and a key is minted. What has never been observed is anything AFTER
// that: no broadcast has been published through a minted key. THIS FILE is
// where that gap lives. negotiateDestination has only ever reached an httptest
// server replaying recorded fixtures, so of the two paths it chooses between,
// the fallback is the one with evidence behind it. That is also the path this
// file guarantees: every function here returns a usable decision and none of
// them can fail a broadcast, so an operator switching the toggle on is risking
// the feature not working rather than the stream not going out.
//
// internal/multitrack does the negotiation and says nothing about polyemesis;
// this file is the whole of the translation between the two. It answers three
// questions and nothing else:
//
//   - WHAT DO WE ASK FOR. multitrackAsk, from the destination row, the shared
//     encode it reads and the operator's declared hardware.
//   - WHERE DO WE PUBLISH. negotiateDestination, which returns a decision that
//     is always usable: either the minted target or the caller's own.
//   - WHAT DOES THE OPERATOR SEE. mtDecision.Note, surfaced once per start by
//     startDest and carried on DestStatus for the card.
//
// NOTHING HERE MAY FAIL A BROADCAST. Every path returns a decision; there is no
// error return, because there is no error a caller could usefully handle -- the
// ordinary Twitch ingest is right whenever the negotiated one is not. See
// multitrack.Outcome, which is shaped the same way for the same reason.
package engine

import (
	"context"
	"runtime/debug"
	"strings"

	"github.com/rainmanjam/polyemesis/internal/db"
	"github.com/rainmanjam/polyemesis/internal/multitrack"
)

// mtDecision is what one destination's go-live decided about Enhanced
// Broadcasting.
//
// It is deliberately NOT multitrack.Outcome. Outcome speaks Twitch's
// vocabulary -- a Target split into server and key, because that is how the
// endpoint returns it -- and a destination publishes to a single composed URL.
// Composing it here, once, is what keeps the split from leaking into startDest
// and from being reassembled a second, slightly different way by the backup.
type mtDecision struct {
	// Asked reports that a negotiation was attempted or deliberately skipped
	// for this destination, i.e. that the operator switched the feature on.
	// False for every destination that did not, and a false here means every
	// other field is zero and nothing is to be said.
	Asked bool
	// Use reports that Target and MintedKey are the ones to publish with.
	//
	// WHEN TRUE THE MINTED KEY IS MANDATORY. Twitch signs the agreed ladder
	// into it; publishing with the operator's own key would connect and send a
	// ladder the ingest never agreed to. See multitrack.Outcome.Use.
	Use bool
	// Target is the composed publish URL, valid only when Use. It carries the
	// minted key as its last path segment, so it is as secret as the key is.
	Target string
	// MintedKey is the credential on its own, for registration as a secret.
	// It must be declared in its own right and cannot inherit the original's
	// registration -- see destSecrets.
	MintedKey string
	// Note is one sentence for the operator, always set when Asked. It is a
	// note and not a warning: a refusal on a GPU-less server is the normal
	// path, not a misconfiguration.
	Note string
	// Verdict distinguishes "we never asked" from "we asked and were refused",
	// which is the difference between a machine with no declared GPU and a
	// machine Twitch turned down.
	Verdict multitrack.Verdict
	// Divergences are advisory only. They annotate a destination that IS
	// publishing; they never block one and must never be rendered as faults.
	Divergences []multitrack.Divergence
}

// mtNotAsked is the decision for a destination that did not opt in. Nothing is
// said about it anywhere, which is the whole point: an ordinary RTMP
// destination must look exactly as it did before this feature existed.
var mtNotAsked = mtDecision{}

// wantsMultitrack reports whether this destination asks Twitch anything at
// go-live.
//
// RTMP ONLY, AND THE ROW'S OWN FLAG. The negotiated ingest is an RTMP/RTMPS
// endpoint -- multitrack.Config.Resolve refuses anything else -- so an SRT or a
// file destination with the flag set has nothing to negotiate. The flag is not
// gated on db.PlatformTwitch here, because an operator who pastes Twitch's
// ingest URL into a "custom" destination and switches this on has asked for
// exactly what it does, and Twitch's own refusal is a better answer than a
// silent no from us.
func wantsMultitrack(row *db.Destination) bool {
	return row != nil && row.Multitrack && row.Kind == db.DestRTMP
}

// twitchOneAudioTrack reports a destination whose SECOND audio mix must not be
// emitted unless Enhanced Broadcasting was negotiated for it.
//
// #141's other half, and the defect this gate exists for: the engine compiled
// the pair on row.VODProfile != nil alone and never read row.Multitrack, so a
// Twitch destination with a VOD mix pushed TWO audio tracks at an ingest that
// takes ONE -- silently, with nothing in the status, the log or the card saying
// so. db.AudioEncoding.copyProblems already states the ingest's limit in as
// many words; nothing acted on it.
//
// PLATFORM, NOT URL. It is db.PlatformTwitch because that is what the operator
// chose and what the dialog gates the toggle on. A destination pointed at
// Twitch's ingest under the "custom" platform is the operator taking the
// controls off, and the generic two-mix egress is genuinely correct for every
// non-Twitch target -- see routing.CompilePair and ffmpeg.secondAudioMap, which
// this must not break.
func twitchOneAudioTrack(row *db.Destination) bool {
	return row != nil && row.Kind == db.DestRTMP && row.Platform == db.PlatformTwitch
}

// vodNeedsNegotiation reports that this destination's second audio mix is
// conditional on a negotiation that has not happened yet.
//
// True only where all three hold: a VOD mix is configured, the destination is a
// Twitch RTMP one, and it opted into Enhanced Broadcasting. Where the opt-in is
// ABSENT the pair is refused at plan time instead -- see planDestinations --
// because that answer does not depend on anything a network call could say.
func vodNeedsNegotiation(row *db.Destination) bool {
	return row != nil && row.VODProfile != nil && twitchOneAudioTrack(row) && wantsMultitrack(row)
}

// noteVODWithoutMultitrack is what an operator is told when they configured a
// second audio mix on a Twitch destination and left Enhanced Broadcasting off.
// It names the fix rather than the rule, because the rule is Twitch's.
const noteVODWithoutMultitrack = "This destination has a second (VOD) audio mix and the ordinary Twitch " +
	"RTMP ingest carries one audio track, so the second mix is not being sent. Switch on Enhanced " +
	"Broadcasting for this destination to negotiate an ingest that takes it."

// noteVODProvisional is what an operator is told when the second mix is dropped
// because the ingest could not be probed. It is the only one of these three that
// is not about a destination's configuration and not about Twitch, so it says
// there is nothing to do: the mix returns on its own once a probe lands.
const noteVODProvisional = "The second (VOD) audio mix is not being sent yet: this ingest could " +
	"not be probed, so the live mix is running on a guessed channel layout and a second guessed " +
	"mix is not added on top of it. It returns by itself once a probe succeeds."

// noteVODNotNegotiated is the same fact after the negotiation was tried and did
// not succeed. Separate wording because the operator's next step is different:
// there is nothing for them to switch on.
const noteVODNotNegotiated = "The second (VOD) audio mix is not being sent: the ordinary Twitch RTMP " +
	"ingest carries one audio track."

// ---------------------------------------------------------------- the ask

// multitrackGPUs converts the operator's declared inventory into the wire
// shape. An empty result is the ordinary case and makes multitrack.Negotiate
// short-circuit to Refused without a network call.
//
// NOTHING IS INVENTED HERE. A field the operator left blank is sent blank; see
// db.MultitrackSettings for why the alternative -- deriving what internal/ffmpeg
// can measure and zeroing the rest -- is a request that describes a machine
// that does not exist.
func multitrackGPUs(s db.MultitrackSettings) []multitrack.GPU {
	if len(s.GPUs) == 0 {
		return nil
	}
	out := make([]multitrack.GPU, 0, len(s.GPUs))
	for _, g := range s.GPUs {
		out = append(out, multitrack.GPU{
			Model:                strings.TrimSpace(g.Model),
			VendorID:             g.VendorID,
			DeviceID:             g.DeviceID,
			DedicatedVideoMemory: g.DedicatedVideoMemory,
			SharedSystemMemory:   g.SharedSystemMemory,
			DriverVersion:        strings.TrimSpace(g.DriverVersion),
		})
	}
	return out
}

// multitrackAsk builds the request from what polyemesis already knows.
//
// THE CANVAS IS THE OPERATOR'S RENDITION, which is the whole of how a
// negotiated ladder reconciles with their own settings: multitrack.Ask's doc
// records that Twitch DERIVES the renditions from the canvas the client says it
// is producing, so an operator who picked 720p gets a 720p negotiation by
// virtue of being asked. Nothing here overrides anything they chose.
//
// A ZERO CANVAS IS NOT SENT. Twitch was measured refusing a request with no
// canvases, and a 0x0 canvas is a statement about the composition that is not
// true; negotiateDestination declines to ask at all rather than send one.
func multitrackAsk(row *db.Destination, canvas multitrack.Canvas, gpus []multitrack.GPU) multitrack.Ask {
	channels := uint32(multitrack.DefaultAudioChannels)
	if row.Audio.Mono {
		channels = 1
	}
	rate := uint32(0)
	if r := row.Profile.SampleRate; r > 0 {
		rate = uint32(r)
	}
	return multitrack.Ask{
		Version: polyemesisVersion(),
		Canvas:  canvas,
		// The switch the whole feature exists for. Asked for exactly when this
		// destination has a second mix to feed the track with: a VOD track
		// negotiated and left unfed is a divergence Reconcile reports and a
		// track Twitch is waiting on.
		VODAudio: row.VODProfile != nil,
		// One, because one is what a destination publishes. Left at the
		// package default rather than restated, so the reasoning stays in the
		// one place that owns it (multitrack.DefaultMaxVideoTracks).
		MaxVideoTracks:  0,
		AudioSampleRate: rate,
		AudioChannels:   channels,
		Hardware:        multitrack.Capabilities{GPU: gpus},
	}
}

// polyemesisVersion is what we tell Twitch we are, and Twitch quotes it back
// inside status.html_en_us. Read from the build info rather than plumbed from
// main: cmd/polyemesis stamps `version` through -ldflags into a package
// variable this package cannot see, and threading it through engine.New for one
// string would be a constructor change for every caller and every test.
//
// "(devel)" is what a `go build` from a working tree reports, and it is left as
// it stands rather than prettied up -- an operator reading "your broadcast
// software (polyemesis) ..." about a development build should be told it was
// one.
func polyemesisVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "unknown"
}

// ---------------------------------------------------------- the negotiation

// negotiateDestination asks Twitch and returns what to publish with.
//
// IT CANNOT FAIL AND IT CANNOT HANG PAST THE CLIENT'S TIMEOUT. Both are
// requirements of the caller rather than of this function: it is called between
// the operator pressing the button and anything reaching a viewer, so a network
// hang at go-live must cost the timeout and not the broadcast. multitrack.Client
// carries that timeout (10s, obs-studio allows five for the same call), and
// multitrack.Negotiate turns every error into a Refused outcome with a sentence
// -- so there is nothing here to recover from, only something to report.
//
// A ZERO CANVAS SKIPS THE CALL. See multitrackAsk: a request that describes a
// 0x0 composition is one Twitch has been measured refusing, and spending a
// round trip at go-live to be refused for a reason we could see beforehand
// delays the broadcast to learn nothing. Same shape as multitrack.Negotiate's
// own no-GPU short circuit, and same justification.
func negotiateDestination(ctx context.Context, c *multitrack.Client, row *db.Destination,
	canvas multitrack.Canvas, gpus []multitrack.GPU) mtDecision {

	if !wantsMultitrack(row) {
		return mtNotAsked
	}
	if canvas.Width == 0 || canvas.Height == 0 {
		return mtDecision{
			Asked:   true,
			Verdict: multitrack.Refused,
			Note: "Enhanced Broadcasting was not requested: polyemesis does not yet know what size " +
				"picture this destination is sending, so this destination is publishing to the " +
				"ordinary Twitch ingest.",
		}
	}
	if c == nil {
		c = &multitrack.Client{}
	}

	out := multitrack.Negotiate(ctx, c, row.StreamKey, multitrackAsk(row, canvas, gpus))
	d := mtDecision{
		Asked:       true,
		Use:         out.Use,
		Note:        out.Note,
		Verdict:     out.Verdict,
		Divergences: out.Divergences,
	}
	if out.Use {
		// Composed the way db.Destination.Target composes one, and from
		// out.Target ALONE. Nothing of the row's URL or key may reach this
		// string: the negotiated ingest is a different host, and the key is the
		// minted credential rather than the operator's.
		d.MintedKey = out.Target.Key
		d.Target = strings.TrimRight(out.Target.URL, "/") + "/" + out.Target.Key
	}
	return d
}

// ---------------------------------------------------------------- the engine

// multitrackClient is the negotiator this engine uses. The zero value talks to
// the real endpoint, which is what production wants; a test replaces it with
// one whose BaseURL points at an httptest server, in the shape
// multitrack.Client.BaseURL exists for. Nothing at runtime writes it.
func (e *Engine) multitrackClient() *multitrack.Client {
	if e.mtClient != nil {
		return e.mtClient
	}
	return &multitrack.Client{}
}

// multitrackCanvas is the composition this destination is producing, in
// multitrack's terms.
//
// THE RENDITION FIRST, THE INGEST SECOND, and that order is the point: a
// destination on a shared encode publishes the RENDITION's picture, so
// negotiating against the ingest's size would ask Twitch for a ladder derived
// from a picture this destination does not send. A rendition axis of 0 means
// "keep the source's" (see db.Rendition.Width), so each axis falls back on its
// own rather than the whole rendition being discarded.
//
// A zero return means "not known", which negotiateDestination declines to ask
// on. That is the honest answer before the ingest has been probed.
func (e *Engine) multitrackCanvas(row *db.Destination) multitrack.Canvas {
	var w, h, fps int
	if v := e.SourceInfo().Video; v != nil {
		w, h = v.Width, v.Height
		fps = int(v.FrameRate + 0.5)
	}
	if row.RenditionID != nil {
		if r, err := e.store.GetRendition(*row.RenditionID); err == nil && r != nil {
			if r.Width > 0 {
				w = r.Width
			}
			if r.Height > 0 {
				h = r.Height
			}
			if r.FPS > 0 {
				fps = r.FPS
			}
		}
	}
	if w <= 0 || h <= 0 {
		return multitrack.Canvas{}
	}
	if fps <= 0 {
		// Not guessed at 30: a framerate polyemesis does not know is one it
		// must not assert, and Reconcile would then report a divergence against
		// a number nobody chose. Denominator 0 is what multitrack.Reconcile
		// reads as "no opinion" and skips.
		return multitrack.Canvas{
			Width: uint32(w), Height: uint32(h),
			CanvasWidth: uint32(w), CanvasHeight: uint32(h),
		}
	}
	return multitrack.Canvas{
		Width: uint32(w), Height: uint32(h),
		CanvasWidth: uint32(w), CanvasHeight: uint32(h),
		Framerate: multitrack.Framerate{Numerator: uint32(fps), Denominator: 1},
	}
}

// negotiateFor is the engine's one call into internal/multitrack.
//
// Called from startDest, which runs ONCE PER START and not per respawn -- a
// reconnect reuses the argv the supervisor already holds. That is what makes
// "says so once" true without any bookkeeping: the note is produced once
// because the negotiation happens once.
func (e *Engine) negotiateFor(ctx context.Context, row *db.Destination) mtDecision {
	if !wantsMultitrack(row) {
		return mtNotAsked
	}
	return negotiateDestination(ctx, e.multitrackClient(), row,
		e.multitrackCanvas(row), multitrackGPUs(e.Settings().Multitrack))
}
