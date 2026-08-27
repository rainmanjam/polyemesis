package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/rainmanjam/polyemesis/internal/services"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/rainmanjam/polyemesis/internal/routing"
)

// DestKind is the transport a destination publishes over.
type DestKind string

const (
	DestRTMP DestKind = "rtmp" // rtmp:// or rtmps://
	DestSRT  DestKind = "srt"  // srt://
	DestFile DestKind = "file" // local recording of this specific mix
	// DestAudio carries no video at all — an Icecast mount for a radio or
	// podcast feed, or an audio file. The routing profile is the whole output,
	// which makes this the one kind where the per-destination mix is not a
	// feature of the stream but the entire stream.
	DestAudio DestKind = "audio" // icecast://user:pass@host:port/mount, or a filename
)

// IcecastScheme is the URL prefix an audio-only destination uses for a live
// mount. Credentials and mount point ride in the URL, the way FFmpeg's icecast
// protocol expects them, so no new column is needed to hold them.
const IcecastScheme = "icecast://"

// Platform identifies an integration, for branding and for stream-key fetch.
type Platform string

const (
	PlatformCustom  Platform = "custom"
	PlatformYouTube Platform = "youtube"
	PlatformTwitch  Platform = "twitch"
	PlatformKick    Platform = "kick"
	// PlatformFacebook is a real platform rather than a preset because Facebook
	// issues its ingest per broadcast over the Graph API: there is nothing for
	// an operator to paste, so the integration has to exist for the destination
	// to work at all. The string matches routing.PlatformFacebook, which is what
	// makes the Rights Manager music policy apply to these destinations.
	PlatformFacebook Platform = "facebook"
	// PlatformRumble exists for CHAT and for nothing else, which makes it the
	// first entry here that is not about a destination.
	//
	// The other five are named because polyemesis either fetches the stream key
	// or, for Facebook, creates the broadcast. Rumble does neither: its ingest
	// URL and key are still pasted by hand from Rumble Studio, and the
	// destination preset deliberately does NOT carry this platform, so nothing
	// about streaming to Rumble changes. What exists is a read-only live-stream
	// API keyed from the operator's own account settings, and internal/chat's
	// adapter needs a Platform value to stamp on every message it normalises.
	//
	// Recorded because the asymmetry looks like an oversight and is not: a
	// platform can be integrated for one capability and manual for another, and
	// pretending otherwise is how a destination starts claiming a key fetch that
	// does not exist.
	PlatformRumble Platform = "rumble"
	// PlatformTrovo is the sixth, and it is the first whose integration fetches
	// the stream KEY without being able to fetch the ingest URL beside it.
	//
	// Trovo publishes the key on its channel resource behind
	// channel_details_self and publishes the ingest hostname nowhere at all --
	// it varies by region and lives only in the creator dashboard. So the
	// preset below still carries an empty URL and still explains itself, while
	// the connected account fills in the secret half. Recorded here because
	// "integrated" has meant "both fields arrive" for every previous entry.
	PlatformTrovo Platform = "trovo"
	// PlatformVimeo exists for SIGN-IN, and sign-in is the only thing it can
	// promise every operator.
	//
	// Vimeo's OAuth is open to any registered app, so connecting an account
	// works on any plan. Its LIVE API is not: "Please note that our live API is
	// available only to Vimeo Enterprise customers"
	// (https://developer.vimeo.com/api/reference/live, read 2026-08-26). So the
	// ingest URL and stream key stay pasted by hand from a Vimeo live event,
	// exactly as before this platform existed, and the destination preset keeps
	// SeparateKey.
	//
	// The asymmetry is the reason the constant is here at all. A connected
	// account is what lets polyemesis ASK Vimeo whether this operator can reach
	// the live API, and say so at connect time rather than letting a refusal
	// arrive mid-broadcast. See internal/oauth/vimeo.go.
	PlatformVimeo Platform = "vimeo"
)

// ErrNotFound is returned by the typed getters.
var ErrNotFound = errors.New("not found")

// Destination is one output: where it goes, and which audio it gets.
type Destination struct {
	ID       int64    `json:"id"`
	Name     string   `json:"name"`
	Kind     DestKind `json:"kind"`
	Platform Platform `json:"platform"`
	// AccountID links to a connected OAuth account. When set, URL/StreamKey
	// are refreshed from the platform API rather than typed by the user.
	AccountID *int64 `json:"accountId,omitempty"`
	// URL is the ingest endpoint (rtmp/srt) or the output path template (file).
	URL string `json:"url"`
	// StreamKey is appended to URL for RTMP. Kept separate so the UI can mask
	// it and so a key rotation does not require retyping the endpoint.
	StreamKey string `json:"streamKey"`
	// BackupURL and BackupStreamKey are the platform's secondary ingest,
	// stored when the broadcast was created. Empty when the platform offered
	// none, which is the normal state for every destination without backup
	// ingest enabled.
	//
	// On Destination rather than in FacebookSettings because the ENGINE
	// consumes them, and the engine should not have to know which platform a
	// destination is. Nothing but Facebook populates them today.
	BackupURL       string `json:"backupUrl,omitempty"`
	BackupStreamKey string `json:"backupStreamKey,omitempty"`
	// KeyUnreadable is the reason this destination's stored stream key could
	// not be decrypted on this machine, empty for every destination whose key
	// was read normally -- which is all of them on a healthy install.
	//
	// It is set by scanDestination and NEVER by a column: it is a fact about
	// this process's key file, not about the row, so it must be recomputed on
	// every read rather than remembered. Restore the right key file and it
	// goes away by itself, with no repair step and nothing to un-set.
	//
	// WHEN IT IS SET, StreamKey AND BackupStreamKey ARE EMPTY AND Enabled IS
	// FALSE. That is the whole point: an unopenable ciphertext means the key
	// this destination would publish with is not knowable, and the alternative
	// to refusing is FFmpeg connecting to somebody's ingest with an empty key.
	// A destination that silently stops going out is a bad afternoon; a
	// destination that goes out wrong is a broadcast on the wrong channel or a
	// rejected connection nobody can explain.
	//
	// The row itself is not touched. enabled is still 1 in the database, so
	// this is reversible by restoring the key file rather than by re-enabling
	// twenty destinations by hand.
	KeyUnreadable string `json:"keyUnreadable,omitempty"`
	// BackupIngestWanted is the operator's intent: "publish a redundant feed
	// for this destination". It sits here, beside the endpoint it gates, for
	// the reason stated directly above -- it used to live in FacebookSettings,
	// which made the engine's gate on two platform-neutral fields read a
	// platform-named struct, and a second platform could not reach redundancy
	// at all no matter what it stored.
	//
	// Named ...Wanted rather than BackupIngest because the pair is intent plus
	// endpoint and wantsBackup needs BOTH: the name has to say which half this
	// is. Intent alone is the normal state between enabling the setting and
	// the next broadcast being created, and it is reported, not started.
	//
	// Facebook additionally passes it to its create call as
	// enable_backup_ingest -- see oauth.IngestOptions.BackupIngest, which stays
	// where it is because that one is a platform fact rather than the intent.
	BackupIngestWanted bool `json:"backupIngestWanted,omitempty"`
	// Enabled is user intent, not live state: "this should be running".
	Enabled      bool            `json:"enabled"`
	AudioBitrate int             `json:"audioBitrate"` // kbps
	Profile      routing.Profile `json:"profile"`
	// Multitrack opts this destination into Twitch Enhanced Broadcasting, which
	// Amazon's IVS calls Multitrack Video: a negotiation at go-live that answers
	// with an ingest endpoint, a minted stream key, and the audio tracks Twitch
	// will accept. See internal/multitrack.
	//
	// FALSE IS THE RIGHT DEFAULT AND WILL STAY THE COMMON CASE. Twitch refuses
	// any client without a supported GPU, by name, and polyemesis is built to be
	// installed on the operator's own server -- a rented VPS has no GPU. Turning
	// this on where negotiation cannot succeed is not a fault and is not
	// punished: the destination falls back to the ordinary ingest and says so
	// once. It is opt-in only because a network round trip at go-live should be
	// something the operator asked for.
	Multitrack bool `json:"multitrack,omitempty"`
	// VODProfile is the SECOND audio mix -- the VOD track, separate from the
	// live one, which is the whole ask of #141.
	//
	// Nil for every destination that has not opted in, which is nearly all of
	// them, and nil produces byte for byte the filter graph and the argv the
	// destination produced before this field existed. See routing.CompilePair,
	// which compiles the pair, and ffmpeg.DestSpec.SecondAudioOutLabel, which
	// maps and encodes it.
	//
	// A POINTER, NOT A VALUE, because "no second mix" and "a second mix that
	// happens to be the zero profile" are different things and the zero profile
	// is not valid anyway (Validate refuses it: no track enabled, no normalize
	// mode, no sample rate). A value here would make every existing row look
	// like it had asked for a broken second track.
	//
	// ON TWITCH THIS NEEDS Multitrack. The ordinary Twitch RTMP ingest takes one
	// audio track; Enhanced Broadcasting is the only published path that takes
	// two and says what the second is for.
	//
	// NOTHING HERE ENFORCES THAT PAIRING AND THE ENGINE NOW DOES, which is a
	// change from what this comment used to promise and never delivered. The
	// engine compiled the pair on this field alone and never read Multitrack,
	// so a Twitch destination with a VOD mix pushed two audio tracks at a
	// one-track ingest silently. Both halves are now real:
	//
	//   - engine.planDestinations refuses the pair outright on a Twitch RTMP
	//     destination that did not opt in, and says so on the card.
	//   - engine.startDest drops it when the opt-in was made and the
	//     negotiation did not succeed, which is the ordinary outcome on a
	//     server with no GPU.
	//
	// Validation stays out of it deliberately. A VOD mix configured before the
	// toggle is turned on is a half-finished setup, not an invalid row, and
	// refusing to SAVE it would make the two settings impossible to enter in
	// the order an operator naturally enters them.
	VODProfile *routing.Profile `json:"vodProfile,omitempty"`
	// RenditionID selects the shared video encode this destination subscribes
	// to. nil is passthrough: no encode, no process, straight off the ingest
	// relay. Whatever the rendition, the destination still does -c:v copy plus
	// its own audio routing graph.
	RenditionID *int64 `json:"renditionId,omitempty"`
	// SourceID is the programme this destination carries.
	//
	// It is a pointer only because SQLite would not accept a NOT NULL REFERENCES
	// column through ALTER TABLE while foreign keys are on -- the nullability is
	// a migration artefact, not part of the model. In practice it is never nil:
	// CreateDestination fills it with the default source when a caller omits it,
	// the foreign key CASCADEs, and scanDestination REFUSES a NULL rather than
	// propagating one. A destination with no source belongs to no programme, so
	// no reconciler lists it as work and it would sit there created but never
	// started.
	SourceID *int64 `json:"sourceId,omitempty"`
	// Expert mode: arguments an operator hand-wrote for this destination,
	// stored as the raw strings they typed so the editor shows them back
	// unchanged. Parsing and the guard acknowledgement live in the API, which
	// is the only place allowed to set these — see handleUpdateDestination.
	//
	// Empty for every destination that has not opted in, which is why they are
	// omitempty: a payload for an ordinary destination looks exactly as it did
	// before expert mode existed.
	ExtraInputArgs  string `json:"extraInputArgs,omitempty"`
	ExtraOutputArgs string `json:"extraOutputArgs,omitempty"`
	// ExpertAckReencode records the operator agreeing, in as many words, that
	// an argument here overrides something the product otherwise guarantees.
	// Stored rather than treated as a one-shot confirmation, so a later edit
	// that keeps the same override does not lose the record of who agreed.
	ExpertAckReencode bool `json:"expertAckReencode,omitempty"`
	// Transport is the optional muxer and socket tuning. Its zero value emits
	// no FFmpeg arguments at all, so a destination that has not opted in
	// produces exactly the command it always did.
	Transport DestTransport `json:"transport"`
	// Resilience is how hard this destination is retried, and when to stop.
	// Its zero value is the behaviour every destination had before it existed:
	// retry forever, 1s to 30s.
	Resilience DestResilience `json:"resilience"`
	// Audio is the output encoding choice. Its zero value is AAC stereo, which
	// is what every destination emitted before it existed.
	Audio AudioEncoding `json:"audio"`
	// Compliance is the obligation metadata: who the programme is for, who may
	// see it, what a viewer is about to be shown. Its zero value touches
	// nothing -- see oauth.Compliance.
	Compliance Compliance `json:"compliance"`
	// Facebook is create-time configuration for a Facebook destination. Empty
	// for every other platform, and for a Facebook destination that has not set
	// any of it.
	Facebook FacebookSettings `json:"facebook"`
	// Lifecycle is what the broadcast-lifecycle coordinator knows about this
	// destination's current broadcast. Zero for every destination on a platform
	// whose broadcast is a side effect of bytes arriving rather than an object
	// with a state machine -- see internal/oauth/lifecycle.go.
	//
	// READ-ONLY THROUGH THE API, and structurally so rather than by a guard in a
	// handler: neither CreateDestination nor UpdateDestination mentions the
	// `lifecycle` column, so a request body carrying one is decoded, ignored and
	// never written. UpdateLifecycle is the only writer in the process. That is
	// what stops an operator's rename -- which decodes the body OVER the stored
	// row and writes the whole thing back -- from reverting a phase the
	// coordinator recorded a moment earlier, and it is why there is no
	// dropUnsendableSettings clause for this field.
	Lifecycle BroadcastControl `json:"lifecycle"`
	Position  int              `json:"position"`
	CreatedAt time.Time        `json:"createdAt"`
	UpdatedAt time.Time        `json:"updatedAt"`
}

// ExpertArgsSet reports whether this destination has any hand-written
// arguments. Two empty strings and no row at all must both read as "expert
// mode off".
// Transport bounds. Wide on purpose: these catch a unit mix-up or a typo, not
// an opinion about how somebody runs their box.
const (
	// MaxMuxQueuePackets is FFmpeg's own practical ceiling; beyond this the
	// queue is a memory leak with a limit rather than a buffer.
	MaxMuxQueuePackets = 1 << 20
	// MaxMuxQueueBytes is 1 GiB. A threshold larger than the machine's RAM is
	// a typo, not a policy.
	MaxMuxQueueBytes = 1 << 30
	// MaxRWTimeoutSeconds is an hour. Anything longer is indistinguishable
	// from the hang the timeout exists to break.
	MaxRWTimeoutSeconds = 3600
	// MinRWTimeoutSeconds is 1. A sub-second timeout on a live socket fires on
	// ordinary jitter and turns a healthy stream into a restart loop.
	MinRWTimeoutSeconds = 1
)

// Resilience bounds.
const (
	MinDestBackoffSeconds = 1
	MaxDestBackoffSeconds = 300
	// MaxDestGiveUpAfter is generous: a platform that has refused a thousand
	// times is not coming back, but the number exists to catch a typo rather
	// than to express an opinion.
	MaxDestGiveUpAfter = 1000
)

// DestResilience is the per-destination reconnect policy.
//
// The one global knob that existed before this was
// settings.ingest.pull.reconnectDelayMaxSeconds, and that governs PULL INGEST
// -- the dial-out source -- not destinations. Destinations had no policy at
// all: every one retried forever on the same 1s-to-30s curve, whatever it was
// and however hopeless.
type DestResilience struct {
	// MinBackoffSeconds and MaxBackoffSeconds bracket the retry curve. 0 takes
	// the supervisor's defaults, which are 1 and 30.
	MinBackoffSeconds int `json:"minBackoffSeconds,omitempty"`
	MaxBackoffSeconds int `json:"maxBackoffSeconds,omitempty"`
	// GiveUpAfter stops retrying after this many CONSECUTIVE failed restarts.
	// 0 is forever, which is the historical behaviour and still the right
	// answer for a platform that is merely slow to come back.
	//
	// Consecutive, not cumulative: a destination that reconnects cleanly once
	// an hour for a week must never accumulate its way to the limit. A run
	// that lasts past the supervisor's stability window resets the count.
	//
	// The point is not to save CPU. It is that a destination retrying forever
	// is INDISTINGUISHABLE from one that works -- the card says
	// "reconnecting", and nothing ever says this endpoint is not coming back.
	// Giving up moves it to failed, which the alert rules already treat as an
	// incident, so the operator is told once rather than never.
	GiveUpAfter int `json:"giveUpAfter,omitempty"`
}

// Active reports whether any resilience policy is set.
func (r DestResilience) Active() bool {
	return r.MinBackoffSeconds > 0 || r.MaxBackoffSeconds > 0 || r.GiveUpAfter > 0
}

func (r DestResilience) problems() []string {
	var probs []string
	add := func(f string, a ...any) { probs = append(probs, fmt.Sprintf(f, a...)) }

	for _, b := range []struct {
		name string
		v    int
	}{{"minimum", r.MinBackoffSeconds}, {"maximum", r.MaxBackoffSeconds}} {
		if b.v != 0 && (b.v < MinDestBackoffSeconds || b.v > MaxDestBackoffSeconds) {
			add("%s reconnect delay %ds out of range (%d-%d, 0 for the default)",
				b.name, b.v, MinDestBackoffSeconds, MaxDestBackoffSeconds)
		}
	}
	// Refused rather than silently swapped. An inverted pair is a typo, and
	// quietly reordering it would hide the typo AND produce a retry curve the
	// operator did not ask for.
	if r.MinBackoffSeconds > 0 && r.MaxBackoffSeconds > 0 &&
		r.MinBackoffSeconds > r.MaxBackoffSeconds {
		add("minimum reconnect delay %ds is greater than the maximum %ds",
			r.MinBackoffSeconds, r.MaxBackoffSeconds)
	}
	if r.GiveUpAfter < 0 || r.GiveUpAfter > MaxDestGiveUpAfter {
		add("give up after %d retries out of range (0-%d, 0 to retry forever)",
			r.GiveUpAfter, MaxDestGiveUpAfter)
	}
	return probs
}

// Audio codec choices for a destination.
const (
	// DestAudioAAC is the default and the only thing every platform takes.
	DestAudioAAC = ""
	// DestAudioOpus is meaningfully better below ~64 kbps. SRT and file
	// destinations only -- see DestAudio.Codec.
	DestAudioOpus = "opus"
)

// DestAudioCodecs is every codec a destination may name, in the order to offer
// them: the universal one first.
var DestAudioCodecs = []string{DestAudioAAC, DestAudioOpus}

// AudioEncoding is the per-destination output encoding.
//
// Smaller than the roadmap asked for, because one of its three items does not
// exist. "AAC profile (LC / HE-AAC v1 / v2)" is NOT BUILDABLE: FFmpeg's native
// aac encoder exposes no -profile option, and `-profile:a aac_he` makes it
// refuse to open outright ("Profile not supported!"). HE-AAC needs the nonfree
// libfdk_aac, which cannot ship in a redistributable build.
//
// The goal behind that item -- good audio well below 64 kbps -- is met by Opus,
// which is free, already in the pinned build, and better than HE-AAC at those
// rates. Answered by a different means rather than abandoned.
type AudioEncoding struct {
	// Codec is empty for AAC. Opus is refused on RTMP: FFmpeg will write it
	// into FLV, because Enhanced RTMP defines a mapping, and no mainstream
	// ingest accepts it. A stream that muxes cleanly and is rejected by the
	// platform looks correct everywhere the operator can see.
	Codec string `json:"codec,omitempty"`
	// Mono folds the routing graph's stereo output to one channel. A DOWNMIX
	// of the operator's mix, not a re-route: the matrix still produces OutL and
	// OutR and this sums them. Halves the bitrate on talk content for no
	// perceptual loss.
	Mono bool `json:"mono,omitempty"`
	// Copy forwards the SELECTED ingest audio tracks to this destination
	// untouched -- `-c:a copy`, no decode, no mix, no encoder -- so the archive
	// or contribution feed carries the same bits the encoder sent us.
	//
	// It is called Copy and not "passthrough" deliberately. Passthrough already
	// means something else everywhere else in this repo: a NULL rendition_id,
	// i.e. video at the ingest's own resolution. Reusing the word for audio
	// would make "a passthrough destination" ambiguous in exactly the
	// conversations where it matters.
	//
	// Copy still SELECTS. The compiled routing profile decides which tracks go
	// out and the role policy still removes the excluded ones, so the DMCA
	// switch keeps working; what is given up is everything the mix stage does
	// to the samples. That is why the validation below refuses a profile whose
	// mix stages would be silently discarded rather than accepting it and
	// producing audio the operator did not ask for.
	Copy bool `json:"copy,omitempty"`
}

// problems reports everything wrong with the encoding block, judged against the
// destination kind AND its routing profile.
//
// The profile is here because Copy is the first setting on this struct whose
// validity depends on it: `-c:a copy` cannot honour a single thing the mix stage
// does, so a profile that asks for loudness while the destination asks for copy
// is two settings that contradict each other. Taking the profile is cheaper than
// the alternative, which is a second validation hook that would drift from this
// one.
func (a AudioEncoding) problems(kind DestKind, p routing.Profile) []string {
	var probs []string
	add := func(f string, v ...any) { probs = append(probs, fmt.Sprintf(f, v...)) }

	known := false
	for _, c := range DestAudioCodecs {
		if c == a.Codec {
			known = true
			break
		}
	}
	if !known {
		add("unknown audio codec %q (aac, opus)", a.Codec)
	}
	// Refused at save time rather than silently downgraded at start time. A
	// downgrade would leave the operator looking at a destination whose
	// settings say Opus and whose stream is AAC, with nothing anywhere saying
	// which is running -- the exact failure the deinterlace validation exists
	// to prevent.
	if a.Codec == DestAudioOpus && kind == DestRTMP {
		add("opus cannot be used on an RTMP destination: FFmpeg will mux it, " +
			"but no mainstream RTMP ingest accepts it, so the stream would " +
			"upload cleanly and be rejected")
	}
	if a.Copy {
		probs = append(probs, a.copyProblems(kind, p)...)
	}
	return probs
}

// copyProblems lists every reason this destination cannot copy its audio.
//
// The doctrine is the one the Opus-on-RTMP refusal above set: a setting that is
// silently ignored is worse than a setting that is refused, because the operator
// is then looking at a form that says one thing and a stream that does another
// with nothing anywhere reconciling them. Copy removes the entire mix stage, so
// EVERY mix-stage setting the profile carries becomes such a lie, and each one
// is named individually rather than rolled into "profile incompatible with
// copy" -- the operator has to know which control to turn off.
//
// NOT REFUSED, deliberately: track selection and ExcludeRoles. Both survive
// copy intact, because the compiled Result still decides which tracks are
// mapped. Refusing them would take the DMCA switch away from the destinations
// most likely to want it, which are the archive and contribution feeds.
//
// ALSO NOT REFUSED: a container that cannot carry the ingest's codec. That is a
// real failure and it is left to fail loudly at start instead, because the
// ingest codec is simply not known at save time -- nothing has connected yet.
// Guessing at save time would mean either refusing combinations that work (I
// measured FFmpeg 8.1.2 muxing two copied AAC tracks into flv, mpegts, matroska
// and mp4 without complaint) or blessing ones that do not.
func (a AudioEncoding) copyProblems(kind DestKind, p routing.Profile) []string {
	var probs []string
	add := func(f string, v ...any) { probs = append(probs, fmt.Sprintf(f, v...)) }

	switch kind {
	case DestRTMP:
		// Same failure class as Opus on RTMP: it muxes and the platform
		// rejects it. #141 exists to measure whether that is still true for a
		// second track on E-RTMP; until it has a measured answer, the honest
		// position is that one AAC stereo track is what RTMP ingests take.
		add("copying audio is not available on an RTMP destination: platform " +
			"ingests expect one encoded stereo track, so a copied multitrack " +
			"stream would upload cleanly and be rejected")
	case DestAudio:
		// An audio-only destination has no video stream to hang a copied track
		// beside, and its codec comes from the target extension or the Icecast
		// mount rather than from the ingest -- so "copy" has no meaning that
		// could be honoured here.
		add("copying audio is not available on an audio-only destination: its " +
			"codec is chosen by the output container, not by the ingest")
	}

	if a.Codec != DestAudioAAC {
		add("audio codec %q cannot be set on a destination that copies its audio: "+
			"nothing is encoded, so the codec is whatever the ingest sent", a.Codec)
	}
	if a.Mono {
		add("mono cannot be set on a destination that copies its audio: " +
			"folding to one channel requires decoding and re-encoding")
	}

	// Every mix stage, named. resolveNorm turns NormAuto into a real filter
	// only when it has something to protect against, and for copy there is no
	// sum to clip -- so "auto" is the one normalization value that means "no
	// opinion" and is left alone. The other two are explicit requests.
	switch p.Normalize {
	case routing.NormLimiter:
		add("the limiter cannot run on a destination that copies its audio: " +
			"set normalization to off or auto")
	case routing.NormLoudnorm:
		add("loudness normalization cannot run on a destination that copies its " +
			"audio: set normalization to off or auto")
	}
	if p.Loudness != nil {
		add("a loudness target cannot be applied on a destination that copies " +
			"its audio: the samples are forwarded untouched")
	}
	if p.Ducking != nil {
		add("ducking cannot run on a destination that copies its audio: it " +
			"needs the mix stage this destination does not have")
	}
	if p.DelayMS != 0 {
		add("an audio delay of %d ms cannot be applied on a destination that "+
			"copies its audio: shifting audio requires a filter on the decoded "+
			"samples", p.DelayMS)
	}
	// The level and channel-routing instructions, named by the package that
	// owns the coefficients rather than re-derived here.
	probs = append(probs, routing.CopyMixProblems(p)...)
	return probs
}

// DestTransport is the per-destination muxer and socket tuning.
//
// Everything here was probed against the pinned FFmpeg before it was designed
// around. See ffmpeg.TransportSpec for the probe results, including the one
// that corrected the roadmap: max_muxing_queue_size and
// muxing_queue_data_threshold are a PAIR, not alternatives.
type DestTransport struct {
	// NoDurationFilesize drops FLV's zero duration and filesize metadata.
	// RTMP only; ignored elsewhere rather than refused, because a destination
	// switched from RTMP to SRT should not become unsavable.
	NoDurationFilesize bool `json:"noDurationFilesize,omitempty"`
	// MuxQueuePackets and MuxQueueBytes bound the interleave buffer. The
	// packet cap applies only once the queue passes the byte threshold, so
	// setting the threshold alone does nothing -- which is why the UI offers
	// them together.
	MuxQueuePackets int `json:"muxQueuePackets,omitempty"`
	MuxQueueBytes   int `json:"muxQueueBytes,omitempty"`
	// RWTimeoutSeconds breaks a half-open socket. Without it a far end that
	// vanished without a FIN blocks the muxer indefinitely: FFmpeg keeps
	// running, the supervisor sees a live process, and the stream is off air
	// with nothing reporting it.
	RWTimeoutSeconds int `json:"rwTimeoutSeconds,omitempty"`
}

// Active reports whether any transport tuning is set.
func (t DestTransport) Active() bool {
	return t.NoDurationFilesize || t.MuxQueuePackets > 0 || t.MuxQueueBytes > 0 ||
		t.RWTimeoutSeconds > 0
}

// problems reports everything wrong with the transport block.
func (t DestTransport) problems() []string {
	var probs []string
	add := func(f string, a ...any) { probs = append(probs, fmt.Sprintf(f, a...)) }

	if t.MuxQueuePackets < 0 || t.MuxQueuePackets > MaxMuxQueuePackets {
		add("muxing queue %d packets out of range (0-%d, 0 for the FFmpeg default)",
			t.MuxQueuePackets, MaxMuxQueuePackets)
	}
	if t.MuxQueueBytes < 0 || t.MuxQueueBytes > MaxMuxQueueBytes {
		add("muxing queue %d bytes out of range (0-%d, 0 for the FFmpeg default)",
			t.MuxQueueBytes, MaxMuxQueueBytes)
	}
	// Refused rather than quietly accepted, because a byte threshold with no
	// packet cap is a setting that appears to do something and does nothing:
	// FFmpeg documents the threshold as "the threshold after which
	// max_muxing_queue_size is taken into account".
	if t.MuxQueueBytes > 0 && t.MuxQueuePackets == 0 {
		add("a muxing queue byte threshold does nothing without a packet limit: " +
			"FFmpeg only applies the packet cap once the queue passes the threshold")
	}
	if t.RWTimeoutSeconds != 0 &&
		(t.RWTimeoutSeconds < MinRWTimeoutSeconds || t.RWTimeoutSeconds > MaxRWTimeoutSeconds) {
		add("socket timeout %ds out of range (%d-%d, 0 to disable)",
			t.RWTimeoutSeconds, MinRWTimeoutSeconds, MaxRWTimeoutSeconds)
	}
	return probs
}

func (d Destination) ExpertArgsSet() bool {
	return strings.TrimSpace(d.ExtraInputArgs) != "" ||
		strings.TrimSpace(d.ExtraOutputArgs) != ""
}

// CarriesStreamKey reports whether this kind's publish URL has a stream key
// joined onto it.
//
// ONE FUNCTION BECAUSE TWO COPIES DRIFTED, AND THE DRIFT WAS THE BUG (#610).
// Target() knew that only RTMP appends a key; Validate() did not, so a key on
// an SRT, file or audio destination was accepted, stored, and then silently
// dropped on the way to the wire. Both callers now ask the same question, so
// "which kinds carry a key" cannot be answered two different ways again.
func (k DestKind) CarriesStreamKey() bool {
	return k == DestRTMP
}

// Target returns the full URL FFmpeg should publish to, i.e. URL with the
// stream key joined on for RTMP.
func (d Destination) Target() string {
	if !d.Kind.CarriesStreamKey() || d.StreamKey == "" {
		return d.URL
	}
	return strings.TrimRight(d.URL, "/") + "/" + d.StreamKey
}

// Validate checks a destination is startable.
func (d Destination) Validate() error {
	var probs []string
	add := func(f string, a ...any) { probs = append(probs, fmt.Sprintf(f, a...)) }

	if strings.TrimSpace(d.Name) == "" {
		add("name is required")
	}
	switch d.Kind {
	case DestRTMP, DestSRT, DestFile, DestAudio:
	default:
		add("unknown destination kind %q", d.Kind)
	}
	switch d.Platform {
	case PlatformCustom, PlatformYouTube, PlatformTwitch, PlatformKick, PlatformFacebook, PlatformRumble, PlatformTrovo, PlatformVimeo, "":
	default:
		add("unknown platform %q", d.Platform)
	}
	if d.AudioBitrate < 32 || d.AudioBitrate > 512 {
		add("audio bitrate %d kbps out of range (32-512)", d.AudioBitrate)
	}

	target := strings.TrimSpace(d.URL)
	switch d.Kind {
	case DestRTMP:
		if target == "" {
			add("an RTMP URL is required")
		} else if u, err := url.Parse(target); err != nil {
			add("malformed RTMP URL: %v", err)
		} else if u.Scheme != "rtmp" && u.Scheme != "rtmps" {
			add("RTMP destination URL must start with rtmp:// or rtmps:// (got %q)", u.Scheme)
		}
	case DestSRT:
		if target == "" {
			add("an SRT URL is required")
		} else if u, err := url.Parse(target); err != nil {
			add("malformed SRT URL: %v", err)
		} else if u.Scheme != "srt" {
			add("SRT destination URL must start with srt:// (got %q)", u.Scheme)
		}
	case DestFile:
		if target == "" {
			add("a filename is required")
		}
		// Keep file destinations inside the data directory. Without this a
		// destination is an arbitrary-file-write primitive for anyone who
		// reaches the API.
		if strings.Contains(target, "..") || strings.HasPrefix(target, "/") {
			add("file destination must be a relative name inside the recordings directory")
		}
	case DestAudio:
		// Two shapes, one kind: a live Icecast mount, or an audio file. The
		// file form gets the same confinement DestFile gets, because "audio
		// only" changes what is written, never where it may be written.
		switch {
		case target == "":
			add("an Icecast URL or an output filename is required")
		case strings.Contains(target, "://"):
			if u, err := url.Parse(target); err != nil {
				add("malformed audio destination URL: %v", err)
			} else if u.Scheme != strings.TrimSuffix(IcecastScheme, "://") {
				add("audio destination URL must start with %s (got %q)", IcecastScheme, u.Scheme)
			} else if strings.Trim(u.Path, "/") == "" {
				add("Icecast destination needs a mount point, e.g. %sHOST:8000/live.mp3", IcecastScheme)
			}
		case strings.Contains(target, ".."), strings.HasPrefix(target, "/"):
			add("audio file destination must be a relative name inside the recordings directory")
		}
	}

	// A STREAM KEY ON A KIND THAT CANNOT CARRY ONE IS REFUSED (#610).
	//
	// Retyping an existing RTMP destination to srt, file or audio left the key
	// on the row. Target() joins a key on for RTMP and only for RTMP, so the
	// destination went on publishing to the bare URL WITH NO CREDENTIAL, and
	// nothing said so -- not Validate, not Warnings, not the dialog, which
	// renders the key field only for RTMP and so had no screen left to show it
	// on.
	//
	// The second half is worse than the misconfiguration. The key is still a
	// live credential, still returned in full by GET /destinations, and now
	// unreachable from every screen that edits the destination -- so it cannot
	// be rotated, because it cannot be seen. GHSA-7jqx-76vq-hvfc was five paths
	// a key could escape by; this is a sixth that also nobody could close.
	//
	// Refused rather than dropped, for the reason spelled out below about
	// control characters: this function refuses, it does not repair. A silent
	// clear here would destroy a credential the operator may still need and say
	// nothing, and it would leave the divergence -- stored value, sent value --
	// that every downstream defence assumes cannot happen.
	//
	// The rows that ALREADY carry a stranded key are not left unsaveable by
	// this: MigrateStrandedStreamKeys clears them at Open, before anything can
	// read one back and hand it to this function. Without that sweep an
	// operator could not so much as rename such a destination, because the API
	// decodes an update body over the row it just read and the key would come
	// back round with it.
	//
	// The message never echoes the key, for the same reason the control-
	// character one does not: a validation error is rendered into a 400 body
	// and into the server log.
	if !d.Kind.CarriesStreamKey() && d.StreamKey != "" {
		add("a %s destination cannot carry a stream key, because only RTMP joins one onto "+
			"the publish URL. Stored here the key would never be sent and no screen would "+
			"show it again, so it could not be rotated either. Clear the stream key, or set "+
			"the transport back to RTMP", d.Kind)
	}

	// A STREAM KEY CARRYING A CONTROL CHARACTER IS REFUSED, NOT REPAIRED.
	//
	// #306. A key was configured with a bracketed-paste artefact glued onto it
	// -- the real key followed by ESC [ 2 7 ; 2 ; 1 3 -- so the stored value was
	// 65 bytes while the value FFmpeg opened, and printed back on stderr when the
	// connect failed, was the 56-byte PREFIX ending at the ESC. The credential
	// scrubber is an exact substring replacement over the literals it was handed,
	// and it was handed the stored 65 bytes; a 65-byte needle does not occur
	// inside a 56-byte haystack, so nothing matched and the key was written to
	// data/logs/process.log in the clear.
	//
	// The root cause is not the escape sequence. It is that the stored spelling
	// and the spelling that reaches the wire were allowed to be different
	// strings, and every downstream defence -- scrub, the API leak scan, the
	// acceptance harness -- is built on them being the same one.
	//
	// The OTHER half of Target() is already protected and that is why only this
	// half leaked: url.Parse above returns "net/url: invalid control character in
	// URL" for exactly these bytes, so a pasted URL with an ESC in it has never
	// been storable. The key half is joined on by Target() with no parser
	// anywhere in its path, so nothing looked.
	//
	// REFUSED rather than sanitised, and the choice is deliberate:
	//
	//   - Sanitising mints a credential the operator never typed. If the cut
	//     point is ever wrong -- a control character in the middle rather than at
	//     the end -- the destination is stored with a truncated key, fails to
	//     publish, and says only that the platform refused it. A refusal at the
	//     boundary is wrong in the direction the operator can see and fix.
	//   - This function refuses; it does not repair. Every other problem it finds
	//     is reported, and the only defaults applied to a destination are applied
	//     by CreateDestination before it is called. A single silent rewrite here
	//     would be the one place a stored value differs from the submitted one,
	//     which is the very property that produced this bug.
	//   - A refusal is a CHECKABLE invariant: no row can hold a divergent key. A
	//     sanitiser leaves "does the sanitiser cover every transformation" open
	//     for ever, and that open question is what engine.wireSpellings exists
	//     for as defence in depth rather than as the fix.
	//
	// An existing row carrying such a key can no longer be saved without the key
	// being re-entered. That is intended: it is a live credential leak, and the
	// message says what to do about it.
	//
	// The message names the offending BYTE and its OFFSET and never the value. A
	// validation error is rendered into a 400 body and into the server log, and a
	// diagnostic that echoes the credential to say it is malformed would be the
	// same disclosure by a shorter route.
	for _, k := range []struct{ field, value string }{
		{"stream key", d.StreamKey},
		{"backup stream key", d.BackupStreamKey},
	} {
		if i := strings.IndexFunc(k.value, unicode.IsControl); i >= 0 {
			add("%s contains a control character (0x%02x at byte %d). It is refused rather "+
				"than trimmed because FFmpeg stops reading the publish URL there, so the "+
				"stored key and the key that reaches the platform would be different "+
				"strings and the credential scrubber only knows the stored one. This is "+
				"almost always a bracketed-paste artefact from a terminal; re-enter the key",
				k.field, k.value[i], i)
		}
	}

	if err := d.Profile.Validate(); err != nil {
		add("%v", err)
	}

	for _, p := range d.Transport.problems() {
		add("%s", p)
	}
	for _, p := range d.Resilience.problems() {
		add("%s", p)
	}
	for _, p := range d.Audio.problems(d.Kind, d.Profile) {
		add("%s", p)
	}
	// Refused at save time rather than at go-live. A compliance field that
	// fails when the operator presses "go live" fails at the one moment they
	// cannot stop to fix it.
	for _, p := range d.Compliance.Problems() {
		add("%s", p)
	}

	if len(probs) > 0 {
		return fmt.Errorf("invalid destination: %s", strings.Join(probs, "; "))
	}
	return nil
}

// ErrKeyUnreadable is the sentinel behind Destination.KeyUnreadable.
//
// Deliberately never returned to a caller of ListDestinations or
// GetDestination: a destination whose key will not open must still appear, with
// its name and platform and everything else intact, or an operator whose key
// file went missing opens the dashboard to an empty page and a 500. It is an
// error type only so openStreamKey has one thing to return and scanDestination
// has one thing to match on.
var ErrKeyUnreadable = errors.New("the stream key could not be read on this machine")

// keyUnreadableReason is what the operator is shown. It names the fix, because
// "decryption failed" tells somebody staring at a dead destination nothing they
// can act on, and the fix really is this small: type the key in again.
const keyUnreadableReason = "the stream key could not be read on this machine — " +
	"re-enter it to enable this destination"

// sealStreamKey splits a stream key into the pair of columns that store it:
// Warning is one advisory finding about a destination: something that will
// probably not work, reported without refusing it. Separate from Validate's
// errors on purpose -- Validate refuses, and refusal has to stay narrow enough
// that it is never wrong.
type Warning struct {
	Field  string `json:"field"`
	Detail string `json:"detail"`
	// Fix is a corrected value when one can be derived, offered rather than
	// applied. Silently rewriting what somebody typed produces a bug report
	// that says "it changed my URL".
	Fix string `json:"fix,omitempty"`
}

// Warnings reports what the platform registry knows that this destination
// appears to contradict. Empty for a well-formed destination, and empty for a
// custom one, since the registry has no opinion about an endpoint it has never
// heard of.
//
// WHAT IT DOES NOT CHECK, stated so the gap is not mistaken for a pass: video
// bitrate, resolution and frame rate belong to the rendition, not to the
// destination, so the ceilings services.CheckEncoder can compare against them
// are not reachable from here. Only the audio bitrate and the URL are.
func (d Destination) Warnings() []Warning {
	var out []Warning
	for _, p := range services.AnalyseURL(d.URL) {
		out = append(out, Warning{Field: p.Field, Detail: p.Detail, Fix: p.Fix})
	}
	// A destination with no registry entry -- "custom", or a platform we do
	// not carry -- gets no encoder opinion rather than a default one.
	if svc, ok := services.Lookup(string(d.Platform)); ok {
		for _, p := range services.CheckEncoder(svc, d.AudioBitrate, 0, 0) {
			out = append(out, Warning{Field: p.Field, Detail: p.Detail})
		}
		if svc.PerChannelIngest && svc.Note != "" && len(out) > 0 {
			// Only alongside another finding: the note explains how to fix a
			// URL, and on its own it is noise on every correct Kick setup.
			out = append(out, Warning{Field: "url", Detail: svc.Note})
		}
	}
	return out
}

// the ciphertext and the plaintext, of which exactly one is ever populated.
//
// With a box: ciphertext, and the plaintext column written EMPTY. Writing both
// would make the plaintext column the answer to "where is the stream key",
// which is the thing this whole change exists to stop -- a leaked database
// would still be a leaked set of live credentials, and the encryption would be
// decoration.
//
// Without a box: no ciphertext and the plaintext, which is what every install
// did before this existed.
func (d *DB) sealStreamKey(key string) (enc []byte, plain string, err error) {
	if d.box == nil {
		return nil, key, nil
	}
	enc, err = d.box.Seal(key)
	if err != nil {
		return nil, "", err
	}
	return enc, "", nil
}

// openStreamKey is sealStreamKey's inverse, and the fallback in the middle of
// it is what lets an install upgrade without a flag day.
//
// PREFER THE CIPHERTEXT, FALL BACK TO THE PLAINTEXT. A row written by the
// previous release has bytes in the plaintext column and nothing in the sealed
// one, and it has to keep working from the first read after the upgrade --
// before the backfill has run, in fact, because Open backfills and then
// something reads, and a crash in between must not lose a destination.
//
// The empty ciphertext is not a special case that needs a flag beside it:
// Seal("") returns no bytes, so a destination with no stream key at all (every
// file destination, every SRT one) reads back through the same fallback as a
// pre-upgrade row and gets the same empty string either way.
func (d *DB) openStreamKey(enc []byte, plain string) (string, error) {
	if len(enc) == 0 {
		return plain, nil
	}
	if d.box == nil {
		// Sealed bytes and no key to open them with. This is the same failure
		// as a wrong key, reached by a different route -- an install that had
		// encryption configured and no longer does -- and it fails the same
		// way rather than falling back to the plaintext column, which is blank
		// for exactly these rows and would publish an empty key.
		return "", ErrKeyUnreadable
	}
	out, err := d.box.Open(enc)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrKeyUnreadable, err)
	}
	return out, nil
}

// marshalVODProfile renders the second (VOD) audio mix for storage.
//
// NIL BECOMES THE EMPTY STRING, NOT "null" AND NOT "{}". The read side treats
// empty as "no second track", so this is the half of that contract that has to
// agree: json.Marshal of a nil pointer produces the four bytes `null`, which is
// not empty, would take the decode branch, and would decode to a nil profile by
// a route the reader cannot distinguish from a corrupt value. One spelling of
// absence, checked at both ends. See TestADestinationWithNoVODMixStoresNoVODMix.
func marshalVODProfile(p *routing.Profile) (string, error) {
	if p == nil {
		return "", nil
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("encode second (VOD) audio profile: %w", err)
	}
	return string(b), nil
}

func (d *DB) scanDestination(s interface{ Scan(...any) error }) (*Destination, error) {
	var (
		dst        Destination
		acct       sql.NullInt64
		rendition  sql.NullInt64
		source     sql.NullInt64
		streamEnc  []byte
		backupEnc  []byte
		profileRaw string
		// Defaulted to "{}" so a row written before the column existed decodes
		// to a zero Compliance -- touch nothing -- rather than failing the scan.
		complianceJSON = "{}"
		// Same reasoning as complianceJSON: a row written before this column
		// existed must decode to a zero FacebookSettings, not fail the scan.
		facebookJSON = "{}"
		// Same reasoning again, and it is the one that matters most here: every
		// row in every existing install predates this column, so a scan that
		// failed on the default would take the whole destinations list down on
		// the first start after an upgrade.
		lifecycleJSON = "{}"
		// The second (VOD) mix. Empty is "no second track" -- see the vod_profile
		// migration for why empty and not "{}" -- so a row written before the
		// column existed decodes to a nil VODProfile rather than to a profile
		// that fails Validate.
		vodProfileRaw = ""
		created       int64
		updated       int64
	)
	err := s.Scan(&dst.ID, &dst.Name, &dst.Kind, &dst.Platform, &acct, &dst.URL,
		&dst.StreamKey, &streamEnc,
		&dst.BackupURL, &dst.BackupStreamKey, &backupEnc, &dst.BackupIngestWanted,
		&dst.Enabled, &dst.AudioBitrate, &profileRaw, &rendition, &source,
		&dst.ExtraInputArgs, &dst.ExtraOutputArgs, &dst.ExpertAckReencode,
		&dst.Transport.NoDurationFilesize, &dst.Transport.MuxQueuePackets,
		&dst.Transport.MuxQueueBytes, &dst.Transport.RWTimeoutSeconds,
		&dst.Resilience.MinBackoffSeconds, &dst.Resilience.MaxBackoffSeconds,
		&dst.Resilience.GiveUpAfter,
		&dst.Audio.Codec, &dst.Audio.Mono, &dst.Audio.Copy, &complianceJSON, &facebookJSON,
		&lifecycleJSON,
		&dst.Multitrack, &vodProfileRaw,
		&dst.Position, &created, &updated)
	if err != nil {
		return nil, err
	}
	// THE FAIL-CLOSED POINT, and it is here rather than in the four methods
	// above it because here is the only place every read passes through.
	//
	// Both keys are decided together and one failure condemns both: they are
	// two halves of one destination's credentials, sealed with the same key at
	// the same moment, so a box that cannot open one cannot be trusted with the
	// other. Refusing the pair also means the operator is told once, about the
	// destination, instead of twice about columns they never think of
	// separately.
	primary, perr := d.openStreamKey(streamEnc, dst.StreamKey)
	backup, berr := d.openStreamKey(backupEnc, dst.BackupStreamKey)
	switch {
	case perr != nil || berr != nil:
		// NOT returned as an error. See ErrKeyUnreadable: the row still has to
		// be listed, or a lost key file is a dashboard that will not load
		// rather than a destination that says what is wrong with it.
		dst.StreamKey, dst.BackupStreamKey = "", ""
		dst.Enabled = false
		dst.KeyUnreadable = keyUnreadableReason
	default:
		dst.StreamKey, dst.BackupStreamKey = primary, backup
	}
	if acct.Valid {
		v := acct.Int64
		dst.AccountID = &v
	}
	// NULL stays nil: that is passthrough, which is what every destination
	// created before renditions existed reads back as.
	if rendition.Valid {
		v := rendition.Int64
		dst.RenditionID = &v
	}
	if source.Valid {
		v := source.Int64
		dst.SourceID = &v
	} else {
		// A NULL source_id is a row that no reconciler will ever start: it
		// belongs to no programme, so nothing lists it as work to do. It is
		// created successfully, appears in the API, and silently never runs.
		//
		// Refused at the boundary rather than propagated, so the impossible
		// state stops here instead of every caller downstream having to know
		// about it. CreateDestination fills the field and the FK CASCADEs, so
		// reaching this means the database was edited by hand or a migration
		// half-finished -- both worth failing loudly over.
		return nil, fmt.Errorf("destination %d has no source: it belongs to no "+
			"programme and would never be started", dst.ID)
	}
	if complianceJSON != "" {
		if err := json.Unmarshal([]byte(complianceJSON), &dst.Compliance); err != nil {
			return nil, fmt.Errorf("destination %d has unreadable compliance metadata: %w", dst.ID, err)
		}
	}
	if facebookJSON != "" {
		if err := json.Unmarshal([]byte(facebookJSON), &dst.Facebook); err != nil {
			return nil, fmt.Errorf("destination %d has unreadable Facebook settings: %w", dst.ID, err)
		}
	}
	if lifecycleJSON != "" {
		if err := json.Unmarshal([]byte(lifecycleJSON), &dst.Lifecycle); err != nil {
			return nil, fmt.Errorf("destination %d has unreadable broadcast lifecycle state: %w", dst.ID, err)
		}
	}
	if err := json.Unmarshal([]byte(profileRaw), &dst.Profile); err != nil {
		return nil, fmt.Errorf("destination %d: decode routing profile: %w", dst.ID, err)
	}
	if vodProfileRaw != "" {
		// Named as the operator's setting, not as a column, because that is what
		// the sentence has to mean to whoever reads it: "the VOD track on this
		// destination is unreadable", not "column vod_profile failed to decode".
		var vod routing.Profile
		if err := json.Unmarshal([]byte(vodProfileRaw), &vod); err != nil {
			return nil, fmt.Errorf("destination %d: decode second (VOD) audio profile: %w", dst.ID, err)
		}
		dst.VODProfile = &vod
	}
	dst.CreatedAt = time.Unix(created, 0)
	dst.UpdatedAt = time.Unix(updated, 0)
	return &dst, nil
}

const destColumns = `id, name, kind, platform, account_id, url,
	stream_key, stream_key_enc,
	backup_url, backup_stream_key, backup_stream_key_enc, backup_ingest_wanted,
	enabled, audio_bitrate, profile, rendition_id, source_id,
	extra_input_args, extra_output_args, expert_ack_reencode,
	tr_no_duration_filesize, tr_mux_queue_packets, tr_mux_queue_bytes, tr_rw_timeout_seconds,
	rs_min_backoff_seconds, rs_max_backoff_seconds, rs_give_up_after,
	au_codec, au_mono, au_copy, compliance, facebook,
	lifecycle,
	multitrack, vod_profile,
	position, created_at, updated_at`

// The reads below, as whole compile-time constants.
//
// Go folds `"a" + constB + "c"` at compile time when every operand is a const,
// so these cost nothing at runtime and cannot vary. A query assembled at the
// call site is indistinguishable, to a reader and to a static analyser, from
// one that interpolates a variable; a constant is safe BY CONSTRUCTION,
// because there is no expression left for a value to reach. Fuller argument in
// chat.go.
const (
	destBySourceQuery = `SELECT ` + destColumns + ` FROM destinations WHERE source_id = ? ORDER BY position, id`
	destListQuery     = `SELECT ` + destColumns + ` FROM destinations ORDER BY position, id`
	destByIDQuery     = `SELECT ` + destColumns + ` FROM destinations WHERE id = ?`
)

// The two shapes of UpdateDestination's write, split at the key columns and
// assembled the same way the reads above are: whole constants, folded at
// compile time, with nothing left for a value to reach.
//
// The key columns lead, so the two differ by a prefix and the argument list is
// the same list with four values on the front or not.
const (
	destUpdateKeyCols = `stream_key=?, stream_key_enc=?,
		backup_stream_key=?, backup_stream_key_enc=?, `
	// The two halves are re-sealed independently, because an operator who
	// retypes one is not saying anything about the other. See keepsSealedKey.
	destUpdatePrimaryKeyCols = `stream_key=?, stream_key_enc=?, `
	destUpdateBackupKeyCols  = `backup_stream_key=?, backup_stream_key_enc=?, `
	destUpdateCols           = `name=?, kind=?, platform=?, account_id=?, url=?,
		backup_url=?, backup_ingest_wanted=?,
		enabled=?, audio_bitrate=?, profile=?, rendition_id=?, source_id=?,
		extra_input_args=?, extra_output_args=?, expert_ack_reencode=?,
		tr_no_duration_filesize=?, tr_mux_queue_packets=?, tr_mux_queue_bytes=?,
		tr_rw_timeout_seconds=?,
		rs_min_backoff_seconds=?, rs_max_backoff_seconds=?, rs_give_up_after=?,
		au_codec=?, au_mono=?, au_copy=?, compliance=?, facebook=?,
		multitrack=?, vod_profile=?,
		updated_at=? WHERE id=?`
	destUpdateQuery        = `UPDATE destinations SET ` + destUpdateKeyCols + destUpdateCols
	destUpdateKeepKeyQuery = `UPDATE destinations SET ` + destUpdateCols
	// Keep one sealed column and re-seal the other.
	destUpdateKeepBackupQuery  = `UPDATE destinations SET ` + destUpdatePrimaryKeyCols + destUpdateCols
	destUpdateKeepPrimaryQuery = `UPDATE destinations SET ` + destUpdateBackupKeyCols + destUpdateCols
)

// keepsSealedKey reports whether a write must leave the stored key columns
// exactly as they are.
//
// THIS IS A DATA-LOSS GUARD, and the loss it stops is not hypothetical. A
// destination whose key would not open reads back with an empty StreamKey --
// that is the fail-closed rule -- and the API's update handler decodes the
// request body OVER the row it just read. So an operator who does nothing more
// than rename such a destination sends a body with no key in it, the merged row
// carries the empty string, and without this the write would seal "" over the
// ciphertext and destroy it.
//
// The ciphertext is worth keeping precisely because the failure it belongs to
// is usually recoverable: a key file that was not copied with the database, a
// restore onto the wrong machine, a data directory mounted late. Put the right
// key file back and every destination returns. Overwrite the sealed bytes and
// nothing brings them back, and the operator was never asked.
//
// Typing a new key still works, and that is the whole exit: a non-empty
// StreamKey means the operator supplied one, so the guard does not apply and
// the new value is sealed over the old. KeyUnreadable is what limits this to
// destinations that are actually in that state -- a client cannot use it to
// pin a key it does not know, because with no key in the body there is no
// value for the write to have carried anyway.
// PER HALF, because the halves fail independently and an operator who retypes
// one has said nothing about the other.
//
// Requiring BOTH to be empty meant that supplying the primary key on a
// destination that also had a backup took the re-sealing branch for both
// columns -- and sealStreamKey("") returns nil bytes, so backup_stream_key_enc
// became NULL. The ciphertext that putting the right secret.key back would have
// recovered was destroyed by the act of recovering the other half, and nothing
// was said. The read path condemns both together, which is right: neither can
// be shown to be readable. The write path inherited that coupling, where it is
// wrong.
func keepsSealedPrimaryKey(dst *Destination) bool {
	return dst.KeyUnreadable != "" && dst.StreamKey == ""
}

func keepsSealedBackupKey(dst *Destination) bool {
	return dst.KeyUnreadable != "" && dst.BackupStreamKey == ""
}

// keepsSealedKey is both halves: the ordinary case, where a rename carried no
// key at all.
func keepsSealedKey(dst *Destination) bool {
	return keepsSealedPrimaryKey(dst) && keepsSealedBackupKey(dst)
}

// checkRendition rejects a rendition_id that names no rendition, and one that
// names a rendition belonging to a different programme.
//
// The foreign key would catch the first anyway, but only as "FOREIGN KEY
// constraint failed", which tells the user nothing about which field is wrong.
// A nil id is passthrough and always valid.
//
// THE CROSS-PROGRAMME HALF IS THE ONE THAT COST SOMETHING. PUT /destinations/4
// on source 2 with renditionId 1 (source 1's) was accepted with a 200 and no
// warning, and nothing downstream could recover: reconcileOutputs lists only
// its own programme's renditions, so source 2's engine found no rendition 1,
// gave that destination no process at all, and the card explained itself with
// "rendition 1 is no longer available". That sentence is false -- rendition 1
// exists and is encoding, under the other programme -- so an operator watching
// a live output stop publishing was sent looking for a deleted rendition that
// was never deleted. Refusing the write is the only point at which the truth
// is still known.
//
// sourceID nil means the destination names no programme, so there is no
// pairing to check; that is the pre-sources row shape CreateDestination fills
// in before it gets here.
func (d *DB) checkRendition(id *int64, sourceID *int64) error {
	if id == nil {
		return nil
	}
	rend, err := d.GetRendition(*id)
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("invalid destination: rendition %d does not exist", *id)
	}
	if err != nil {
		return err
	}
	if sourceID == nil || rend.SourceID == nil || *rend.SourceID == *sourceID {
		return nil
	}
	return fmt.Errorf("invalid destination: rendition %d (%q) belongs to source %d, "+
		"but this destination belongs to source %d. A destination can only select a "+
		"rendition from its own programme, because only that programme's engine runs "+
		"it. Pick one of source %d's renditions, or leave this destination on "+
		"passthrough.", *id, rend.Name, *rend.SourceID, *sourceID, *sourceID)
}

// destinationKeepsItsRendition reports whether a pending update leaves both
// halves of the pairing exactly as the stored row has them.
//
// WHY AN UPDATE IS NOT SIMPLY HELD TO THE SAME RULE AS A CREATE. Rows wired
// across programmes before checkRendition looked at source_id are already in
// the field, and the API's update handler decodes the request body OVER the
// row it just read -- so a client renaming such a destination sends the
// foreign renditionId straight back. Enforcing the rule unconditionally would
// turn every one of those rows unsaveable: the operator could not rename it,
// could not disable it, could not correct its URL, and the refusal would name
// a field they had not touched. That is #607's trap, refused at the dial
// rather than at load, and it is why the check is on the CHANGE and not on the
// state. Anything that touches either half is held to the full rule, so a bad
// pairing can be cleared but never made or moved, and no migration has to
// silently drop a live destination to passthrough behind the operator's back.
func (d *DB) destinationKeepsItsRendition(dst *Destination) (bool, error) {
	var rend, src sql.NullInt64
	err := d.sql.QueryRow(
		`SELECT rendition_id, source_id FROM destinations WHERE id = ?`, dst.ID,
	).Scan(&rend, &src)
	if errors.Is(err, sql.ErrNoRows) {
		// No stored row to have inherited anything from, so there is nothing to
		// grandfather. The UPDATE below reports the missing row as ErrNotFound.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return sameOptionalID(rend, dst.RenditionID) && sameOptionalID(src, dst.SourceID), nil
}

// sameOptionalID compares a stored nullable id with the one a write carries.
func sameOptionalID(stored sql.NullInt64, want *int64) bool {
	if !stored.Valid || want == nil {
		return !stored.Valid && want == nil
	}
	return stored.Int64 == *want
}

// ListDestinations returns every destination in display order.
// ListDestinationsBySource returns the destinations belonging to one source.
//
// This is what a per-source engine reconciles against: it must never see, and
// so can never start, a destination that belongs to another programme. Getting
// that wrong would fan the wrong video out to somebody's platform, which is the
// worst failure this feature can have.
func (d *DB) ListDestinationsBySource(sourceID int64) ([]*Destination, error) {
	rows, err := d.sql.Query(
		destBySourceQuery, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*Destination{}
	for rows.Next() {
		dst, err := d.scanDestination(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, dst)
	}
	return out, rows.Err()
}

func (d *DB) ListDestinations() ([]*Destination, error) {
	rows, err := d.sql.Query(destListQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*Destination{}
	for rows.Next() {
		dst, err := d.scanDestination(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, dst)
	}
	return out, rows.Err()
}

// GetDestination loads one destination.
func (d *DB) GetDestination(id int64) (*Destination, error) {
	row := d.sql.QueryRow(destByIDQuery, id)
	dst, err := d.scanDestination(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return dst, err
}

// CreateDestination inserts a destination, defaulting anything unset.
func (d *DB) CreateDestination(dst *Destination) (*Destination, error) {
	if dst.AudioBitrate == 0 {
		dst.AudioBitrate = 160
	}
	if dst.Platform == "" {
		dst.Platform = PlatformCustom
	}
	// A create request normally carries no routing profile — the user sets one
	// afterwards in the routing editor. ApplyDefaults alone would produce six
	// rows with nothing enabled, which fails validation ("no track is enabled")
	// and makes creating a destination impossible. Seed a real default instead:
	// track 1 at unity, which is what a single-track ingest wants anyway.
	if dst.Profile.IsUnset() {
		dst.Profile = routing.DefaultProfile()
	}
	dst.Profile.ApplyDefaults()
	if err := dst.Validate(); err != nil {
		return nil, err
	}
	// A caller that names no source gets the default one.
	//
	// THIS IS NO LONGER A PUBLIC BEHAVIOUR. It used to be justified by API
	// clients written before sources existed, which is void -- polyemesis has
	// not shipped, and handleCreateDestination now refuses a body with no
	// sourceId outright (requireNamedSource). Nothing a client can send reaches
	// this branch.
	//
	// It stays for the callers that are our own: ~100 test fixtures that mean
	// "a destination on the default source" and would otherwise each have to
	// create a source and thread its id through, for no assertion any of them
	// makes. A guard test pins that no non-test caller relies on it.
	//
	// The other half of the original reason still stands and is why this fills
	// rather than refuses: a destination with a NULL source_id belongs to no
	// programme, so no reconciler lists it as work -- created successfully,
	// never started, with nothing on screen to explain why. Between a silent
	// default and a silent zombie, the default is recoverable. Refusing is
	// better than both, and that is what the handler now does.
	if dst.SourceID == nil {
		id, err := d.DefaultSourceID()
		if err != nil {
			return nil, fmt.Errorf("resolve default source: %w", err)
		}
		dst.SourceID = &id
	}
	// AFTER the source is settled, never before. checkRendition holds the
	// rendition against the destination's programme, and a check that ran while
	// SourceID was still nil would have nothing to hold it against and would
	// wave through exactly the cross-programme pairing it exists to refuse.
	if err := d.checkRendition(dst.RenditionID, dst.SourceID); err != nil {
		return nil, err
	}

	profile, err := json.Marshal(dst.Profile)
	if err != nil {
		return nil, err
	}
	compliance, err := json.Marshal(dst.Compliance)
	if err != nil {
		return nil, err
	}
	facebook, err := json.Marshal(dst.Facebook)
	if err != nil {
		return nil, err
	}
	vodProfile, err := marshalVODProfile(dst.VODProfile)
	if err != nil {
		return nil, err
	}
	keyEnc, keyPlain, err := d.sealStreamKey(dst.StreamKey)
	if err != nil {
		return nil, fmt.Errorf("seal stream key: %w", err)
	}
	backupEnc, backupPlain, err := d.sealStreamKey(dst.BackupStreamKey)
	if err != nil {
		return nil, fmt.Errorf("seal backup stream key: %w", err)
	}
	now := time.Now().Unix()

	var maxPos sql.NullInt64
	if err := d.sql.QueryRow(`SELECT MAX(position) FROM destinations`).Scan(&maxPos); err != nil {
		return nil, err
	}
	dst.Position = int(maxPos.Int64) + 1

	res, err := d.sql.Exec(`INSERT INTO destinations
		(name, kind, platform, account_id, url,
		 stream_key, stream_key_enc,
		 backup_url, backup_stream_key, backup_stream_key_enc,
		 backup_ingest_wanted,
		 enabled, audio_bitrate, profile, rendition_id, source_id,
		 extra_input_args, extra_output_args, expert_ack_reencode,
		 tr_no_duration_filesize, tr_mux_queue_packets, tr_mux_queue_bytes, tr_rw_timeout_seconds,
		 rs_min_backoff_seconds, rs_max_backoff_seconds, rs_give_up_after,
		 au_codec, au_mono, au_copy, compliance, facebook,
		 multitrack, vod_profile,
		 position, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		dst.Name, dst.Kind, dst.Platform, dst.AccountID, dst.URL,
		keyPlain, keyEnc,
		dst.BackupURL, backupPlain, backupEnc, dst.BackupIngestWanted,
		dst.Enabled, dst.AudioBitrate, string(profile), dst.RenditionID, dst.SourceID,
		dst.ExtraInputArgs, dst.ExtraOutputArgs, dst.ExpertAckReencode,
		dst.Transport.NoDurationFilesize, dst.Transport.MuxQueuePackets,
		dst.Transport.MuxQueueBytes, dst.Transport.RWTimeoutSeconds,
		dst.Resilience.MinBackoffSeconds, dst.Resilience.MaxBackoffSeconds,
		dst.Resilience.GiveUpAfter,
		dst.Audio.Codec, dst.Audio.Mono, dst.Audio.Copy, string(compliance), string(facebook),
		dst.Multitrack, vodProfile,
		dst.Position, now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return d.GetDestination(id)
}

// UpdateDestination replaces a destination's mutable fields.
func (d *DB) UpdateDestination(dst *Destination) (*Destination, error) {
	if dst.AudioBitrate == 0 {
		dst.AudioBitrate = 160
	}
	if dst.Platform == "" {
		dst.Platform = PlatformCustom
	}
	dst.Profile.ApplyDefaults()
	if err := dst.Validate(); err != nil {
		return nil, err
	}
	// The programme the rendition is held against, or nil for a pairing this
	// write is not touching. See destinationKeepsItsRendition for why an
	// inherited mismatch must not make the whole row unsaveable.
	scope := dst.SourceID
	kept, err := d.destinationKeepsItsRendition(dst)
	if err != nil {
		return nil, err
	}
	if kept {
		scope = nil
	}
	if err := d.checkRendition(dst.RenditionID, scope); err != nil {
		return nil, err
	}
	profile, err := json.Marshal(dst.Profile)
	if err != nil {
		return nil, err
	}
	compliance, err := json.Marshal(dst.Compliance)
	if err != nil {
		return nil, err
	}
	facebook, err := json.Marshal(dst.Facebook)
	if err != nil {
		return nil, err
	}
	vodProfile, err := marshalVODProfile(dst.VODProfile)
	if err != nil {
		return nil, err
	}
	// The key columns first, so that the two statements below differ only by a
	// prefix and the arguments line up with whichever one is used.
	args := []any{
		dst.Name, dst.Kind, dst.Platform, dst.AccountID, dst.URL,
		dst.BackupURL, dst.BackupIngestWanted,
		dst.Enabled, dst.AudioBitrate, string(profile), dst.RenditionID, dst.SourceID,
		dst.ExtraInputArgs, dst.ExtraOutputArgs, dst.ExpertAckReencode,
		dst.Transport.NoDurationFilesize, dst.Transport.MuxQueuePackets,
		dst.Transport.MuxQueueBytes, dst.Transport.RWTimeoutSeconds,
		dst.Resilience.MinBackoffSeconds, dst.Resilience.MaxBackoffSeconds,
		dst.Resilience.GiveUpAfter,
		dst.Audio.Codec, dst.Audio.Mono, dst.Audio.Copy, string(compliance), string(facebook),
		dst.Multitrack, vodProfile,
		time.Now().Unix(), dst.ID,
	}
	// Four cases, because the two halves are decided separately.
	keepPrimary, keepBackup := keepsSealedPrimaryKey(dst), keepsSealedBackupKey(dst)
	var query string
	switch {
	case keepPrimary && keepBackup:
		query = destUpdateKeepKeyQuery
	case keepPrimary:
		query = destUpdateKeepPrimaryQuery
		backupEnc, backupPlain, err := d.sealStreamKey(dst.BackupStreamKey)
		if err != nil {
			return nil, fmt.Errorf("seal backup stream key: %w", err)
		}
		args = append([]any{backupPlain, backupEnc}, args...)
	case keepBackup:
		query = destUpdateKeepBackupQuery
		keyEnc, keyPlain, err := d.sealStreamKey(dst.StreamKey)
		if err != nil {
			return nil, fmt.Errorf("seal stream key: %w", err)
		}
		args = append([]any{keyPlain, keyEnc}, args...)
	default:
		query = destUpdateQuery
		keyEnc, keyPlain, err := d.sealStreamKey(dst.StreamKey)
		if err != nil {
			return nil, fmt.Errorf("seal stream key: %w", err)
		}
		backupEnc, backupPlain, err := d.sealStreamKey(dst.BackupStreamKey)
		if err != nil {
			return nil, fmt.Errorf("seal backup stream key: %w", err)
		}
		args = append([]any{keyPlain, keyEnc, backupPlain, backupEnc}, args...)
	}
	res, err := d.sql.Exec(query, args...)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return d.GetDestination(dst.ID)
}

// ErrAnnouncementSkipped is what UpdateAnnouncement returns when the callback
// declined the row it was shown. Not an error in the operational sense -- it is
// the answer "the destination is no longer one this write may touch" -- so it
// is a sentinel rather than a string a caller has to match on.
var ErrAnnouncementSkipped = errors.New("announcement skipped")

// UpdateAnnouncement writes ONLY the columns the pre-announce sweep owns: the
// primary stream key, the backup endpoint, and the Facebook block that carries
// the announcement markers.
//
// WHY NOT UpdateDestination. That one is a full-row unconditional
// `UPDATE ... WHERE id=?` with no version column behind it, and the sweep holds
// a destination across a Graph call that takes up to thirty seconds. Writing
// the whole row back reverts every operator edit that landed in that window --
// a rename, a routing change, the enable switch, a key refresh -- silently, and
// for destinations late in a sweep the window is the whole sweep.
//
// So the row is READ AGAIN HERE, inside the transaction that writes it, and apply
// is handed the row as it stands now rather than the caller's snapshot. That is
// what makes the Facebook blob safe to rewrite: it also holds crossposting, the
// donate charity and the backup toggle, none of which belong to this sweep.
// Anything apply sets outside the four columns below is DISCARDED -- deliberately,
// because a caller that could write any column would be UpdateDestination again.
//
// apply reports whether to go ahead. Returning false rolls back and returns
// ErrAnnouncementSkipped, which is how the sweep refuses to rewrite the stream
// key of a destination that went live while Facebook was being asked for one:
// StreamKey is inside Target(), which is the first element of the engine's
// restart hash, so changing it under a running FFmpeg cycles the live process at
// a moment nobody chose.
func (d *DB) UpdateAnnouncement(id int64, apply func(*Destination) bool) (*Destination, error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	cur, err := d.scanDestination(tx.QueryRow(destByIDQuery, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !apply(cur) {
		return nil, ErrAnnouncementSkipped
	}
	facebook, err := json.Marshal(cur.Facebook)
	if err != nil {
		return nil, err
	}
	// The same guard as UpdateDestination's, for the same reason and one more.
	// This sweep runs unattended, every few minutes, over every Facebook
	// destination -- so if it wrote empty keys over unopenable ciphertext it
	// would do it to all of them, at three in the morning, with nobody to
	// notice. apply is what would normally put a fresh key here; when it has
	// not, there is nothing to write and the stored bytes stay.
	if keepsSealedKey(cur) {
		res, err := tx.Exec(`UPDATE destinations SET
			backup_url=?, facebook=?, updated_at=? WHERE id=?`,
			cur.BackupURL, string(facebook), time.Now().Unix(), id)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil, ErrNotFound
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return d.GetDestination(id)
	}
	keyEnc, keyPlain, err := d.sealStreamKey(cur.StreamKey)
	if err != nil {
		return nil, fmt.Errorf("seal stream key: %w", err)
	}
	backupEnc, backupPlain, err := d.sealStreamKey(cur.BackupStreamKey)
	if err != nil {
		return nil, fmt.Errorf("seal backup stream key: %w", err)
	}
	res, err := tx.Exec(`UPDATE destinations SET
		stream_key=?, stream_key_enc=?, backup_url=?,
		backup_stream_key=?, backup_stream_key_enc=?, facebook=?, updated_at=?
		WHERE id=?`,
		keyPlain, keyEnc, cur.BackupURL, backupPlain, backupEnc, string(facebook),
		time.Now().Unix(), id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return d.GetDestination(id)
}

// ErrLifecycleSkipped is what UpdateLifecycle returns when the callback declined
// the row it was shown. The sibling of ErrAnnouncementSkipped and a sentinel for
// the same reason: it is not an operational failure, it is the answer "the
// destination is no longer one this write may claim".
var ErrLifecycleSkipped = errors.New("lifecycle update skipped")

// UpdateLifecycle writes ONE column: the broadcast-lifecycle block.
//
// THE NARROWEST WRITER IN THIS FILE, AND THAT IS THE POINT. It is the only way
// the lifecycle coordinator can touch the database at all, so whatever it can
// persist is exactly what this statement lists -- and this statement lists
// nothing that changes what a destination DOES. Set Enabled inside apply and it
// is discarded. Set StreamKey and it is discarded. Set URL and it is discarded.
// The coordinator therefore cannot start, stop, or reconfigure an output by any
// route, however wrong its logic becomes, and that is a property of this
// function rather than a promise made in a comment somewhere else.
//
// Contrast UpdateAnnouncement, which writes three columns including the stream
// key and is therefore forbidden on an enabled destination. This one writes a
// column that reaches no FFmpeg argument, so it is safe on a LIVE destination --
// which is the whole reason a lifecycle coordinator can exist while the
// pre-announce sweep has to stand back. See internal/engine/lifecycle_spec_test.go
// for the pin.
//
// The row is READ AGAIN INSIDE THE TRANSACTION, for UpdateAnnouncement's reason:
// the caller has been away making an HTTP call to a platform, an operator may
// have disabled or re-enabled the destination in that window, and apply must
// decide against the row as it stands rather than against a snapshot taken
// before the call. apply returning false rolls back with ErrLifecycleSkipped,
// which is what makes "this broadcast is being ended" a compare-and-set: two
// daemons on one database serialise here, and the loser sends nothing.
func (d *DB) UpdateLifecycle(id int64, apply func(*Destination) bool) (*Destination, error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	cur, err := d.scanDestination(tx.QueryRow(destByIDQuery, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !apply(cur) {
		return nil, ErrLifecycleSkipped
	}
	lifecycle, err := json.Marshal(cur.Lifecycle)
	if err != nil {
		return nil, err
	}
	// updated_at is deliberately NOT touched. This sweep runs every fifteen
	// seconds over every lifecycle destination, and bumping the timestamp would
	// make "when did somebody last change this destination" mean "when did the
	// coordinator last confirm the platform agreed with us" -- which is the
	// answer to a different question, on the field an operator uses to work out
	// what changed before a show went wrong.
	res, err := tx.Exec(`UPDATE destinations SET lifecycle=? WHERE id=?`, string(lifecycle), id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return d.GetDestination(id)
}

// SetDestinationEnabled flips the run/stop intent without touching anything
// else, so start/stop never risks rewriting a routing profile.
func (d *DB) SetDestinationEnabled(id int64, enabled bool) error {
	res, err := d.sql.Exec(`UPDATE destinations SET enabled=?, updated_at=? WHERE id=?`,
		enabled, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func destinationIDs(tx *sql.Tx) (map[int64]bool, error) {
	rows, err := tx.Query(`SELECT id FROM destinations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = true
	}
	return ids, rows.Err()
}

// checkPermutation reports whether ids names every member of known exactly
// once. Anything less is rejected rather than partially honoured: a subset
// would leave the unnamed rows sharing positions with the named ones, and the
// resulting order is not one any client asked for.
func checkPermutation(ids []int64, known map[int64]bool) error {
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if !known[id] {
			return fmt.Errorf("cannot reorder: destination %d does not exist", id)
		}
		if seen[id] {
			return fmt.Errorf("cannot reorder: destination %d listed twice", id)
		}
		seen[id] = true
	}
	if len(ids) != len(known) {
		return fmt.Errorf("cannot reorder: got %d ids for %d destinations", len(ids), len(known))
	}
	return nil
}

// ReorderDestinations rewrites display order so it matches ids, which must
// name every destination exactly once. Position is presentation only, so
// updated_at is deliberately left alone: moving a card up the dashboard is not
// an edit to the destination.
func (d *DB) ReorderDestinations(ids []int64) error {
	// One transaction, because a half-applied order leaves rows sharing a
	// position and the dashboard in an arrangement nobody asked for.
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	known, err := destinationIDs(tx)
	if err != nil {
		return err
	}
	if err := checkPermutation(ids, known); err != nil {
		return err
	}

	stmt, err := tx.Prepare(`UPDATE destinations SET position=? WHERE id=?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for pos, id := range ids {
		if _, err := stmt.Exec(pos, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteDestination removes a destination.
func (d *DB) DeleteDestination(id int64) error {
	res, err := d.sql.Exec(`DELETE FROM destinations WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MigrateDestinationExpertArgs adds the expert-mode columns to a destinations
// table created before expert mode existed, and folds in anything the earlier
// sidecar table held.
//
// Same reasoning as MigrateRenditions, and the same constraint: schema.sql only
// runs CREATE TABLE IF NOT EXISTS, which is a no-op against a table that is
// already there, so a new column can only arrive by ALTER. Idempotent, safe on
// every open, and every existing row reads back as two empty strings — which is
// precisely "expert mode off".
//
// The destination_expert_args table is the shape expert mode shipped in while
// internal/db was owned by another workstream. It is drained and dropped here
// rather than left in place, so there is exactly one answer to "what arguments
// does this destination run with".
// backupIntentBackfill carries the intent out of the facebook blob and into the
// column promoted from it.
//
// A var rather than a literal for one reason: a test replaces it with a
// statement that fails, which is the only way to prove the ALTER that precedes
// it rolls back with it. Nothing at runtime rewrites it. The alternative was to
// assert atomicity by reading the source, which asserts nothing.
// See TestAFailedBackfillTakesTheColumnWithIt.
var backupIntentBackfill = `UPDATE destinations SET backup_ingest_wanted = 1
	WHERE json_valid(facebook) AND json_extract(facebook, '$.backupIngest') = 1`

func (d *DB) MigrateDestinationExpertArgs() error {
	columns := []struct{ name, ddl string }{
		{"extra_input_args", `ALTER TABLE destinations ADD COLUMN extra_input_args TEXT NOT NULL DEFAULT ''`},
		{"extra_output_args", `ALTER TABLE destinations ADD COLUMN extra_output_args TEXT NOT NULL DEFAULT ''`},
		{"expert_ack_reencode", `ALTER TABLE destinations ADD COLUMN expert_ack_reencode INTEGER NOT NULL DEFAULT 0`},
		// Transport tuning. Every default is the no-op value, so an upgraded
		// install emits exactly the FFmpeg command it did yesterday.
		{"tr_no_duration_filesize", `ALTER TABLE destinations ADD COLUMN tr_no_duration_filesize INTEGER NOT NULL DEFAULT 0`},
		{"tr_mux_queue_packets", `ALTER TABLE destinations ADD COLUMN tr_mux_queue_packets INTEGER NOT NULL DEFAULT 0`},
		{"tr_mux_queue_bytes", `ALTER TABLE destinations ADD COLUMN tr_mux_queue_bytes INTEGER NOT NULL DEFAULT 0`},
		{"tr_rw_timeout_seconds", `ALTER TABLE destinations ADD COLUMN tr_rw_timeout_seconds INTEGER NOT NULL DEFAULT 0`},
		// Reconnect policy. 0 everywhere is "the behaviour you already had".
		{"rs_min_backoff_seconds", `ALTER TABLE destinations ADD COLUMN rs_min_backoff_seconds INTEGER NOT NULL DEFAULT 0`},
		{"rs_max_backoff_seconds", `ALTER TABLE destinations ADD COLUMN rs_max_backoff_seconds INTEGER NOT NULL DEFAULT 0`},
		{"rs_give_up_after", `ALTER TABLE destinations ADD COLUMN rs_give_up_after INTEGER NOT NULL DEFAULT 0`},
		// Audio encoding. '' is AAC and 0 is stereo, which is what every
		// destination emitted before these existed.
		{"au_codec", `ALTER TABLE destinations ADD COLUMN au_codec TEXT NOT NULL DEFAULT ''`},
		{"au_mono", `ALTER TABLE destinations ADD COLUMN au_mono INTEGER NOT NULL DEFAULT 0`},
		// Bit-exact audio copy. 0 is the mix path, which is what every existing
		// row ran on, so an upgraded install emits the same command it did
		// yesterday for every destination that has not opted in.
		{"au_copy", `ALTER TABLE destinations ADD COLUMN au_copy INTEGER NOT NULL DEFAULT 0`},
		// Twitch Enhanced Broadcasting. 0 is "do not negotiate", which is what
		// every existing row did, and it stays the common case: Twitch refuses a
		// host with no supported GPU and most polyemesis installs are exactly
		// that. See Destination.Multitrack.
		{"multitrack", `ALTER TABLE destinations ADD COLUMN multitrack INTEGER NOT NULL DEFAULT 0`},
		// The second (VOD) audio mix, as one JSON blob for the reason compliance
		// is one: it is the same shape as the `profile` column beside it and is
		// edited as a unit.
		//
		// '' rather than '{}' is the no-op, and the difference matters. '{}'
		// would decode to the zero routing.Profile -- no track enabled, no
		// normalize mode, no sample rate -- which is a profile that fails
		// Validate, so every upgraded row would carry a second mix that is
		// broken rather than absent. '' decodes to nil, which is "no second
		// track", which is what every existing row means.
		{"vod_profile", `ALTER TABLE destinations ADD COLUMN vod_profile TEXT NOT NULL DEFAULT ''`},
		// Compliance rides as one JSON blob rather than four columns: it is a
		// map plus two scalars, edited as a unit, and '{}' is "touch nothing".
		{"compliance", `ALTER TABLE destinations ADD COLUMN compliance TEXT NOT NULL DEFAULT '{}'`},
		// Facebook's create-time block, one JSON blob for the same reason
		// compliance is one: a slice plus a scalar, edited as a unit, and '{}'
		// is "send nothing".
		{"facebook", `ALTER TABLE destinations ADD COLUMN facebook TEXT NOT NULL DEFAULT '{}'`},
		// The broadcast-lifecycle coordinator's bookkeeping, one JSON blob for
		// the same reason compliance and facebook are, and '{}' for the same
		// reason: it decodes to a zero BroadcastControl, which means "this
		// destination has never been through the coordinator".
		//
		// THAT DEFAULT IS THE UPGRADE STORY IN FULL, and it is deliberately the
		// quiet one. A row that predates this column has no recorded phase, so
		// the disabled-row branch of the sweep declines to end anything for it
		// -- see the table in internal/api/lifecycle.go. An install upgrading
		// mid-show therefore does nothing to the broadcast already on air; the
		// coordinator adopts it the moment the platform is next asked and says
		// it is live.
		{"lifecycle", `ALTER TABLE destinations ADD COLUMN lifecycle TEXT NOT NULL DEFAULT '{}'`},
		{"backup_url", `ALTER TABLE destinations ADD COLUMN backup_url TEXT NOT NULL DEFAULT ''`},
		{"backup_stream_key", `ALTER TABLE destinations ADD COLUMN backup_stream_key TEXT NOT NULL DEFAULT ''`},
		// The operator's intent, promoted out of the facebook JSON blob to sit
		// beside the endpoint it gates. 0 is "no redundancy", which is the
		// right default for a NEW row and the WRONG one for an existing row
		// that had it on -- see the backfill below.
		{"backup_ingest_wanted", `ALTER TABLE destinations ADD COLUMN backup_ingest_wanted INTEGER NOT NULL DEFAULT 0`},
		// The sealed halves of the two stream-key columns. NULL is "this row's
		// key is still in the plaintext column beside it", which is true of
		// every row at the instant the ALTER lands and stops being true when
		// backfillDestinationStreamKeys runs a few lines later in Open.
		//
		// No NOT NULL and no default, unlike everything above: a BLOB column
		// defaulting to '' would make "no ciphertext" and "the ciphertext of
		// the empty string" the same value, and the read path distinguishes
		// them to decide whether to fall back to the plaintext.
		//
		// No backfill keyed off `added` here either, deliberately. Sealing
		// needs the box, which the guard above knows nothing about, and a
		// once-only guard is the wrong shape for a step that must also catch
		// rows written by a release that had no box configured.
		{"stream_key_enc", `ALTER TABLE destinations ADD COLUMN stream_key_enc BLOB`},
		{"backup_stream_key_enc", `ALTER TABLE destinations ADD COLUMN backup_stream_key_enc BLOB`},
	}
	// added records the columns this pass created. columnExists is the only
	// guard this function has and it is the right one: on every later open the
	// column is already there, the ALTER is skipped, and so is anything keyed
	// off `added`. That is what makes the data migration below one-shot rather
	// than something that reasserts an old value over an operator's edit on
	// every start.
	// Every existence check happens BEFORE the transaction opens, and that is
	// not stylistic. columnExists queries d.sql, and db.go sets
	// SetMaxOpenConns(1) -- so a read issued while a transaction holds the one
	// connection waits for a connection the transaction will not release until
	// it commits. It would not fail; it would hang on startup, for ever.
	added := make(map[string]bool, len(columns))
	missing := make([]struct{ name, ddl string }, 0, len(columns))
	for _, c := range columns {
		has, err := columnExists(d.sql, "destinations", c.name)
		if err != nil {
			return fmt.Errorf("inspect destinations columns: %w", err)
		}
		if has {
			continue
		}
		missing = append(missing, c)
		added[c.name] = true
	}

	// ONE TRANSACTION over the ALTERs and the data migration below them.
	//
	// SQLite's DDL is transactional, and this needs it. The backfill is guarded
	// by "did THIS pass create the column", so a crash between the ALTER
	// committing and the UPDATE running would leave a database where the column
	// exists, the guard is false for ever after, and every operator who had
	// backup ingest on has silently lost it -- invisible until the broadcast
	// that was supposed to survive a dropped connection does not.
	//
	// Either the column and its data arrive together or neither does, and the
	// next open tries again from a state it recognises.
	tx, err := d.sql.Begin()
	if err != nil {
		return fmt.Errorf("begin destinations migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, c := range missing {
		if _, err := tx.Exec(c.ddl); err != nil {
			return fmt.Errorf("add destinations.%s: %w", c.name, err)
		}
	}

	// THE ALTER ALONE WOULD TURN REDUNDANCY OFF for every operator who had it
	// on. Existing rows carry the intent inside the facebook blob, as
	// `{"backupIngest":true}`, because that is where it lived before it was
	// promoted; a column defaulting to 0 silently answers "no" for all of them,
	// wantsBackup goes false, and nothing says so until a broadcast drops and
	// the second feed that was supposed to catch it is not running.
	//
	// So the ALTER is only half the migration. json_valid guards the extract
	// against a row whose blob was written before the column had its '{}'
	// default; json_extract returns 1 for a JSON true, which is what the Go
	// bool marshalled to.
	if added["backup_ingest_wanted"] {
		if _, err := tx.Exec(backupIntentBackfill); err != nil {
			return fmt.Errorf("backfill destinations.backup_ingest_wanted: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit destinations migration: %w", err)
	}

	sidecar, err := tableExists(d.sql, "destination_expert_args")
	if err != nil {
		return fmt.Errorf("inspect destination_expert_args: %w", err)
	}
	if !sidecar {
		return nil
	}
	// One UPDATE, then the drop. A destination whose sidecar row was deleted
	// between the two statements simply keeps its empty columns, which is the
	// same answer either order would have given.
	if _, err := d.sql.Exec(`UPDATE destinations SET
		extra_input_args    = COALESCE((SELECT input_args   FROM destination_expert_args e WHERE e.destination_id = destinations.id), extra_input_args),
		extra_output_args   = COALESCE((SELECT output_args  FROM destination_expert_args e WHERE e.destination_id = destinations.id), extra_output_args),
		expert_ack_reencode = COALESCE((SELECT ack_reencode FROM destination_expert_args e WHERE e.destination_id = destinations.id), expert_ack_reencode)`); err != nil {
		return fmt.Errorf("fold destination_expert_args into destinations: %w", err)
	}
	if _, err := d.sql.Exec(`DROP TABLE destination_expert_args`); err != nil {
		return fmt.Errorf("drop destination_expert_args: %w", err)
	}
	return nil
}

// MigrateStrandedStreamKeys clears the stream key of every destination whose
// kind cannot carry one, in either column it may be sitting in.
//
// WHY THIS RUNS AT ALL, given Validate now refuses the combination (#610).
// Because Validate refusing it is exactly what would strand the operator. An
// install upgrading into this release can already have such rows -- retyping an
// RTMP destination to srt, file or audio never cleared the key -- and
// UpdateDestination validates before it writes, over a struct the API built by
// decoding the request body ON TOP OF the row it just read. So the stranded key
// comes back round with every save, and the dialog renders no key field for
// those kinds, so there is nothing on screen to clear. Without this sweep the
// new refusal would make those destinations unrenameable, undisableable and
// unfixable, and the error would name a field the operator cannot see.
//
// CLEARED RATHER THAN REPORTED, and that is the one silent rewrite in this
// area. It is defensible only because of what the value is: a key this kind
// never sent, that no screen has shown since the kind changed, and that GET
// /destinations still returns in full. There is nothing to preserve and a live
// credential to remove. Everything the operator can act on -- a key they meant
// to keep -- is still reachable the moment they set the transport back to RTMP
// and type it in, which is the only route that ever worked for these rows.
//
// IDEMPOTENT BY ITS GUARD, like backfillDestinationStreamKeys beside it: the
// WHERE clause asks about the data in front of it rather than about a marker,
// so it is correct on the second open, correct after a crash halfway through
// the first, and correct against a row stranded later by some path nobody has
// thought of yet.
//
// THE BACKUP KEY IS DELIBERATELY NOT TOUCHED. engine.backupTarget joins the
// backup key onto the backup URL for every kind, so a backup key is SENT
// whatever the transport is. It is not stranded, and clearing it would be
// destroying a credential that works.
func (d *DB) MigrateStrandedStreamKeys() error {
	res, err := d.sql.Exec(`UPDATE destinations
		SET stream_key = '', stream_key_enc = NULL
		WHERE kind <> ? AND (stream_key <> '' OR stream_key_enc IS NOT NULL)`, string(DestRTMP))
	if err != nil {
		return fmt.Errorf("clear stranded stream keys: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("clear stranded stream keys: %w", err)
	}
	if n == 0 {
		return nil
	}
	// THE COMMIT IS NOT THE END OF THE PLAINTEXT -- the full argument is at the
	// bottom of backfillDestinationStreamKeys. The short version: the UPDATE
	// wrote new pages into the -wal and the old pages still hold the key until a
	// checkpoint copies over them, so a function whose entire purpose is to
	// remove a credential from the database has not done it until this runs.
	return checkpointTruncate(d)
}

// backfillDestinationStreamKeys seals every stream key still sitting in a
// plaintext column and blanks the column it came out of.
//
// WHY IT EXISTS AT ALL. The read path falls back to the plaintext column, so an
// upgraded install works perfectly without this -- and would go on storing
// every stream key in the clear for ever, which is the entire thing this change
// is for. The fallback keeps the upgrade safe; this is what makes it finish.
//
// IDEMPOTENT BY ITS GUARD, not by a marker. The WHERE clause is "is there still
// plaintext here", so the second open matches no rows, and so does the second
// open after a crash halfway through the first. Nothing records that it ran,
// because nothing needs to: the question it asks is about the data in front of
// it. That also means it picks up rows written later by a release running
// WITHOUT a box, which a once-only migration flag would miss for ever.
//
// NO BOX, NO WORK. An install with no key file keeps its keys where they are;
// see WithSecretBox.
func (d *DB) backfillDestinationStreamKeys() error {
	if d.box == nil {
		return nil
	}
	type pending struct {
		id                   int64
		key, backup          string
		keyEnc, backupEnc    []byte
		newKeyEnc, newBakEnc []byte
	}
	// READ EVERYTHING FIRST, then write, and that is not a style choice.
	// db.go sets SetMaxOpenConns(1), so a query issued while the transaction
	// below holds the one connection waits for a connection that transaction
	// will not release until it commits -- a startup that hangs rather than
	// fails. MigrateDestinationExpertArgs opens with the same warning.
	rows, err := d.sql.Query(`SELECT id, stream_key, stream_key_enc,
		backup_stream_key, backup_stream_key_enc FROM destinations
		WHERE stream_key <> '' OR backup_stream_key <> ''`)
	if err != nil {
		return fmt.Errorf("scan destinations for plaintext stream keys: %w", err)
	}
	var todo []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.key, &p.keyEnc, &p.backup, &p.backupEnc); err != nil {
			rows.Close()
			return fmt.Errorf("scan destinations for plaintext stream keys: %w", err)
		}
		todo = append(todo, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("scan destinations for plaintext stream keys: %w", err)
	}
	if len(todo) == 0 {
		// NOTHING TO SEAL IS NOT THE SAME AS NOTHING TO CLEAN UP, and reading it
		// that way left plaintext in the log permanently. The previous version
		// returned here, and the TRUNCATE below is the only thing that clears the
		// write-ahead log. An upgrade that sealed every row, COMMITTED, and then
		// died before the checkpoint -- power loss, an OOM kill, a restart landing
		// in the wrong second -- comes back to exactly this state: every row
		// already sealed, so no work to do, so an early return, so the log is
		// never truncated. Not on that boot and not on any later one.
		//
		// So the checkpoint runs anyway. It is cheap on an already-clean log, and
		// it is the difference between a transient exposure and a permanent one.
		return checkpointTruncate(d)
	}

	for i := range todo {
		p := &todo[i]
		// Each column decided on its own. A row can have a sealed primary and a
		// plaintext backup -- that is exactly what a row created after the
		// upgrade and then pre-announced by an older binary looks like -- and
		// sealing "" over the primary's ciphertext because the backup needed
		// work would destroy a live credential.
		p.newKeyEnc, p.newBakEnc = p.keyEnc, p.backupEnc
		if p.key != "" {
			if p.newKeyEnc, err = d.box.Seal(p.key); err != nil {
				return fmt.Errorf("seal stream key of destination %d: %w", p.id, err)
			}
		}
		if p.backup != "" {
			if p.newBakEnc, err = d.box.Seal(p.backup); err != nil {
				return fmt.Errorf("seal backup stream key of destination %d: %w", p.id, err)
			}
		}
	}

	// ONE TRANSACTION over the whole set. Half a backfill is not a state worth
	// having: it would leave some rows sealed and some not, and an interruption
	// during it must land back on "some plaintext remains", which the guard
	// above already knows how to finish.
	tx, err := d.sql.Begin()
	if err != nil {
		return fmt.Errorf("begin stream key backfill: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`UPDATE destinations SET
		stream_key='', stream_key_enc=?,
		backup_stream_key='', backup_stream_key_enc=? WHERE id=?`)
	if err != nil {
		return fmt.Errorf("prepare stream key backfill: %w", err)
	}
	defer stmt.Close()
	for _, p := range todo {
		if _, err := stmt.Exec(p.newKeyEnc, p.newBakEnc, p.id); err != nil {
			return fmt.Errorf("seal stream key of destination %d: %w", p.id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit stream key backfill: %w", err)
	}

	// THE COMMIT IS NOT THE END OF THE PLAINTEXT. Committing wrote the sealed
	// rows into the -wal; the pages the plaintext used to live in are still in
	// the main database file, untouched, until a checkpoint copies the new
	// pages over them. Between the two, `grep` on either file finds the keys
	// this function exists to remove -- and the -wal is not even mode-protected
	// on an install that predates the fsperm pass in Open.
	//
	// TRUNCATE rather than PASSIVE or FULL: PASSIVE may copy nothing at all if
	// a reader is active, and FULL leaves the -wal at full size with the old
	// frames still in it. TRUNCATE copies everything back and then shortens the
	// -wal to zero bytes, which is the only variant that removes both copies.
	//
	// IT IS NOT SUFFICIENT ON ITS OWN, and neither is secure_delete on its own:
	// measured against 60 pre-0.7.0 destinations, this checkpoint alone left 2
	// plaintext copies in freed pages, and secure_delete alone left every copy
	// that had not yet been checkpointed. Open's DSN supplies the other half.
	//
	// A FAILURE HERE IS FATAL, and deliberately so. Open has one connection
	// (SetMaxOpenConns(1)), so there is no concurrent reader to block the
	// checkpoint and no expected way for this to fail. If it does, the rows
	// read back correctly and every automated check passes while the plaintext
	// is still on disk -- exactly the silent half-fix this change is about. An
	// operator who sees the startup error can run the remediation in
	// docs/UPGRADING.md; one who sees nothing cannot.
	return checkpointTruncate(d)
}

// checkpointTruncate empties the write-ahead log, and CHECKS THAT IT DID.
//
// PRAGMA wal_checkpoint(TRUNCATE) DOES NOT RETURN AN ERROR WHEN IT FAILS. It
// returns a row -- (busy, log, checkpointed) -- and signals refusal by setting
// busy=1 with the log left exactly where it was. The previous code used Exec,
// which discards result rows, so the "a failure here is fatal, and deliberately
// so" guarantee stated below was never armed for the one failure mode that
// actually happens. It caught a SQL error that cannot occur and missed the
// refusal that can.
//
// A FAILURE HERE IS FATAL, and deliberately so. Open holds one connection
// (SetMaxOpenConns(1)), so nothing inside this process can hold the lock -- but
// another process can: a second polyemesis pointed at the same file, a backup
// tool, an operator with the sqlite3 CLI open. If that happens the rows still
// read back correctly and every automated check passes while the plaintext is
// still on disk, which is exactly the silent half-fix this whole change is
// about. An operator who sees the startup error can run the remediation in
// docs/UPGRADING.md; one who sees nothing cannot.
func checkpointTruncate(d *DB) error {
	var busy, logFrames, checkpointed int
	if err := d.sql.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).
		Scan(&busy, &logFrames, &checkpointed); err != nil {
		return fmt.Errorf("checkpoint after stream key backfill: %w", err)
	}
	if busy != 0 {
		return fmt.Errorf("the write-ahead log could not be truncated: SQLite reported "+
			"the checkpoint busy (%d frames left in the log). Another process is holding "+
			"a read transaction on this database. Plaintext stream keys from before the "+
			"seal-at-rest upgrade may remain readable in polyemesis.db-wal until it is "+
			"truncated -- see docs/UPGRADING.md", logFrames)
	}
	return nil
}

func tableExists(sqldb *sql.DB, table string) (bool, error) {
	var name string
	err := sqldb.QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
