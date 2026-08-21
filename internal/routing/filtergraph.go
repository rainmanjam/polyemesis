package routing

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// OutLabel is the filter_complex label carrying the finished stereo mix. The
// destination command builder maps it with -map "[aout]".
const OutLabel = "aout"

// ns is a label namespace. Every intermediate label a compile emits goes
// through it, so that two graphs can share one filter_complex without colliding.
//
// The empty namespace reproduces the labels this package emitted before it
// existed, BYTE FOR BYTE -- that is asserted, not assumed, because a destination
// that gains a second mix must not have its first mix quietly rewritten. See
// TestTheEmptyNamespaceIsByteIdenticalToTheSingleMixGraph.
type ns struct{ prefix string }

// of namespaces a fixed label.
func (n ns) of(name string) string { return n.prefix + name }

// track names the per-track pan output for ingest track t.
func (n ns) track(t int) string { return fmt.Sprintf("%sa_t%d", n.prefix, t) }

// Result is a compiled routing profile.
type Result struct {
	// FilterComplex is the full -filter_complex argument, ready to hand to
	// FFmpeg. Surfaced verbatim in the UI so routing is never a black box.
	FilterComplex string `json:"filterComplex"`
	// OutLabel is the label to -map for the destination's audio.
	OutLabel string `json:"outLabel"`
	// SecondOutLabel is the label to -map for a SECOND finished mix -- the VOD
	// track -- or "" when this destination has only one, which is nearly all of
	// them. Set only by CompilePair.
	//
	// It lives on Result rather than only on Pair so that the engine's one
	// description of an output does not have to change type to carry it: every
	// signature between the compiler and ffmpeg.DestSpec already passes a
	// Result, and "" is exactly what DestSpec.SecondAudioOutLabel means by "not
	// opted in".
	SecondOutLabel string `json:"secondOutLabel,omitempty"`
	// Summary is the human sentence shown on a destination card,
	// e.g. "Tracks 1, 2, 4 → stereo".
	Summary string `json:"summary"`
	// Tracks are the 0-based ingest tracks that actually contribute.
	Tracks []int `json:"tracks"`
	// Normalization records which clip-protection stage was actually applied,
	// after NormAuto has been resolved.
	Normalization NormMode `json:"normalization"`
	// Warnings are non-fatal mismatches between the profile and the live
	// source (e.g. the profile wants track 4 but the ingest sends three).
	Warnings []string `json:"warnings"`
	// Provisional reports that this graph was built WITHOUT a measured layout,
	// so each track is folded to stereo by FFmpeg at runtime rather than by the
	// matrix the operator drew. See CompileProvisional.
	Provisional bool `json:"provisional,omitempty"`
	// VideoDelayMS is how long the *video* must be held back, in milliseconds,
	// to satisfy a negative Profile.DelayMS. Audio cannot be moved earlier than
	// it arrives, so pulling audio ahead of picture is only expressible as
	// delaying the picture — which is an output-args concern, not something a
	// filter graph on the audio side can do. Zero for every other profile.
	VideoDelayMS int `json:"videoDelayMs,omitempty"`
}

// Compile turns a routing profile plus the live ingest layout into an FFmpeg
// filter graph.
//
// Shape of the generated graph, for N contributing tracks:
//
//	[0:a:0]pan=stereo|c0=...|c1=...[a_t0];
//	[0:a:1]pan=stereo|c0=...|c1=...[a_t1];
//	[a_t0][a_t1]amix=inputs=2:duration=longest:normalize=0[a_mix];
//	[a_mix]alimiter=limit=0.95:level=disabled[a_norm];
//	[a_norm]aresample=48000:async=1:first_pts=0[aout]
//
// Simple mode and matrix mode both funnel through this one path: simple mode
// is expanded to matrix cells using the standard downmix table first.
//
// The optional stages are spliced into that skeleton in signal-chain order —
// denoise per track, then the duck, then the sum, then the loudness target,
// then the delay, then the resample. Each one is present only when the profile
// (or, for denoise, the source annotation) asked for it, so a profile that uses
// none of them produces the string above byte for byte.
func Compile(p Profile, src Source) (Result, error) {
	return compile(p, src, false, ns{})
}

// CompileProvisional builds the same graph for a layout that has NOT been
// measured, replacing every guessed pan matrix with a runtime downmix.
//
// It exists so that "we cannot probe this ingest" stops being a permanent
// outage. The layout guard refuses to plan destinations against the placeholder
// because a guessed matrix is silently wrong, not because it is wrong -- see
// ProvisionalFilter. Once the wrongness is audible instead of silent, running is
// better than not running.
//
// Result.Provisional is set, and every caller that surfaces warnings will show
// one, because an operator has to know the mix is being decided by FFmpeg at
// runtime rather than by the matrix they drew.
func CompileProvisional(p Profile, src Source) (Result, error) {
	return compile(p, src, true, ns{})
}

func compile(p Profile, src Source, provisional bool, n ns) (Result, error) {
	if err := p.Validate(); err != nil {
		return Result{}, err
	}

	res := Result{OutLabel: n.of(OutLabel)}

	cells, warns := resolveCells(p, src)
	if provisional {
		// Those warnings describe a matrix that is NOT being applied. Against
		// the placeholder every track claims two channels, so a saved 5.1
		// profile produces four "channel N is ignored" lines and a level drop
		// in dB -- all of it about coefficients this compile is replacing with a
		// runtime downmix. Reporting them would bury the one warning that is
		// true under four that are not.
		//
		// Which TRACKS contribute is still decided by the cells above, and that
		// part survives: the placeholder carries every track, so a track is
		// dropped here only if the profile never selected it.
		warns = nil
	}
	selected := len(cells)
	cells, exWarns := applyRolePolicy(p, src, cells)
	warns = dedupe(append(warns, exWarns...))
	res.Warnings = warns
	if len(cells) == 0 {
		if selected > 0 {
			// Say *why* there is no audio. "selects no audio" would send an
			// operator hunting through checkboxes that are all still ticked.
			return Result{}, fmt.Errorf("%w: every selected track is excluded by this destination's role policy", ErrNoAudio)
		}
		return Result{}, ErrNoAudio
	}

	// Group by track, preserving ascending track order so the generated string
	// is deterministic (and therefore diffable and testable).
	byTrack := map[int][]Cell{}
	for _, c := range cells {
		byTrack[c.Track] = append(byTrack[c.Track], c)
	}
	tracks := make([]int, 0, len(byTrack))
	for t := range byTrack {
		tracks = append(tracks, t)
	}
	sort.Ints(tracks)
	res.Tracks = tracks

	var chains []string
	label := make(map[int]string, len(tracks))
	for _, t := range tracks {
		label[t] = n.track(t)
		chain := trackChain(src, t, byTrack[t])
		if provisional {
			chain = provisionalChain(src, t, trackGain(p, t))
		}
		chains = append(chains, fmt.Sprintf("[0:a:%d]%s[%s]", t, chain, label[t]))
	}

	// Duck before summing: a duck applied to the finished mix would pull the
	// trigger down along with everything else.
	legs := make([]string, 0, len(tracks))
	if d, ok := p.EffectiveDucking(); ok {
		duckChains, duckLegs, duckWarns := duckGraph(d, src, tracks, label, provisional, n)
		chains = append(chains, duckChains...)
		legs = duckLegs
		if len(duckWarns) > 0 {
			res.Warnings = dedupe(append(res.Warnings, duckWarns...))
		}
	}
	if len(legs) == 0 {
		for _, t := range tracks {
			legs = append(legs, label[t])
		}
	}

	// Sum. amix's normalize=1 default divides by the input count, which would
	// silently drop a 3-track mix by ~9.5 dB. We want the true sum and handle
	// the resulting clip risk explicitly, below.
	cur := legs[0]
	if len(legs) > 1 {
		chains = append(chains, fmt.Sprintf("%samix=inputs=%d:duration=longest:normalize=0[%s]",
			bracket(legs), len(legs), n.of("a_mix")))
		cur = n.of("a_mix")
	}

	norm := resolveNorm(p.Normalize, len(tracks), peakGain(cells))
	loud, loudOK := p.EffectiveLoudness()
	if loudOK {
		// A destination that names a loudness target has asked for loudness
		// normalization, whatever NormAuto would have picked on its own.
		// EffectiveLoudness has already refused to override an explicit
		// NormOff or NormLimiter, so reaching here means it is ours to set.
		norm = NormLoudnorm
	}
	res.Normalization = norm
	if f := normFilterFor(norm, loud, loudOK); f != "" {
		chains = append(chains, fmt.Sprintf("[%s]%s[%s]", cur, f, n.of("a_norm")))
		cur = n.of("a_norm")
	}

	// Delay last but one: holding the finished mix is what "this destination is
	// 250 ms behind its video" means, and doing it after loudnorm keeps the
	// loudness measurement looking at the same samples it always did.
	switch {
	case p.DelayMS > 0:
		chains = append(chains, fmt.Sprintf("[%s]adelay=delays=%d:all=1[%s]", cur, p.DelayMS, n.of("a_delay")))
		cur = n.of("a_delay")
	case p.DelayMS < 0:
		res.VideoDelayMS = -p.DelayMS
	}

	// Final resample pins the rate the AAC encoder sees and lets FFmpeg absorb
	// the small timestamp drift that a UDP relay inevitably introduces.
	rate := p.SampleRate
	if rate == 0 {
		rate = 48000
	}
	chains = append(chains, fmt.Sprintf("[%s]aresample=%d:async=1:first_pts=0[%s]", cur, rate, n.of(OutLabel)))

	res.FilterComplex = strings.Join(chains, ";")
	res.Summary = summarize(tracks)
	if provisional {
		res.Provisional = true
		note := "the ingest layout could not be measured, so each track is being downmixed by FFmpeg at runtime; levels are approximate until a probe succeeds"
		if p.Mode == ModeMatrix {
			// Sharper, because more is being given up. A matrix says which
			// CHANNEL goes where, and with the layout unknown none of that can
			// be honoured -- a track carrying two languages is summed rather
			// than separated. An operator has to be told that specifically.
			note = "the ingest layout could not be measured, so this destination's mix matrix is NOT being applied: each track is downmixed to stereo by FFmpeg at runtime, per-channel routing and per-cell gains are ignored, and a track carrying different content on left and right is summed"
		}
		res.Warnings = dedupe(append(res.Warnings, note))
	}
	return res, nil
}

// trackGain is the per-track gain a provisional chain applies by hand, because
// it is not folding one into a matrix.
//
// Simple mode HAS a per-track gain and it means the same thing whatever the
// layout, so it carries through unchanged.
//
// Matrix mode does not, and no scalar can stand in for one. The first version
// used the largest cell gain on the reasoning that overstating is safer, and
// both reviewers rejected it for the same reason: a matrix of `c0 to L at 0.25`
// and `c1 to R at 1.5` is not "the track at 1.5". Collapsing it boosts the
// intended left contribution six-fold AND leaks each channel into the opposite
// leg, which is a different mix rather than an approximate one.
//
// So matrix mode gets unity, and the warning says the per-cell routing is not
// being applied. Channel isolation genuinely cannot survive here -- a dual-mono
// track carrying English left and Spanish right WILL be summed while the layout
// is unknown -- and the honest thing is to say so rather than to pick a number
// that makes it look considered.
func trackGain(p Profile, track int) float64 {
	if p.Mode == ModeMatrix {
		return 1
	}
	for _, sel := range p.Tracks {
		if sel.Track == track {
			return sel.Gain
		}
	}
	return 1
}

// trackChain renders the filter chain for one contributing track: the pan that
// selects and downmixes its channels, plus noise suppression when the source
// says the track needs it.
func trackChain(src Source, track int, cells []Cell) string {
	return denoised(src, track, seamPin(src, track)+PanFilter(cells))
}

// seamPin fixes a track's channel layout at the head of its chain, so that the
// pan behind it always receives the layout it was compiled against.
//
// WHY, MEASURED. A pan matrix is compiled once, from a probe, and then names
// input channels by number: `pan=stereo|c0=1*c0|c1=1*c1`. That is a promise
// about what will arrive. The selector can break it mid-stream -- switching to
// the playlist filler, to the slate, back to the primary -- and a destination
// copying video does not restart across a switch, by design, so the same
// long-lived FFmpeg meets a layout its graph was not built for.
//
// On FFmpeg 6.1 that was survivable, because a failed filter task was logged and
// ignored unless -xerror was set. From 7.0 the filtergraph runs as a scheduler
// task and its failure ends the process. Captured on a real switch, 8.1.2:
//
//	WARN source switched to=playlist reason="the primary ingest stopped delivering"
//	WARN process exited process=dest:2 ranFor=17s
//	     err="exit status 183: [fc#0] Error sending frames to consumers:
//	          Invalid data found when processing input"
//
// fc#0 is the filtergraph. See #398.
//
// aformat converts rather than refuses: whatever arrives is negotiated to the
// pinned layout by libswresample. This is the same instrument, for the same
// reason, as ProvisionalFilter below -- "it negotiates against the layout that
// ACTUALLY ARRIVES rather than the one we assumed" -- which until now was
// reached for only when the layout had never been measured. A measured layout
// can also stop being true; it just takes a source switch rather than a missing
// probe.
//
// EMPTY WHEN THE LAYOUT IS UNKNOWN, which is the fail-open direction this
// package takes everywhere: a pin invented from a guess would convert every
// track to a layout nobody measured, and that is worse than the failure it
// prevents. An unmeasured layout takes provisionalChain instead, which already
// negotiates at runtime and needs no pin.
func seamPin(src Source, track int) string {
	t, ok := src.TrackByIndex(track)
	if !ok || t.Layout == "" {
		return ""
	}
	return "aformat=channel_layouts=" + t.Layout + ","
}

// ProvisionalFilter is the per-track chain used when the ingest layout has NOT
// been measured, so the channel count the cells were built from is a guess.
//
// The hazard the whole layout guard exists for is that a guess is not an error.
// A profile compiled against the six-stereo placeholder emits
// `pan=stereo|c0=c0|c1=c1`, which is a perfectly valid graph against a real 5.1
// track -- it publishes front left and right and discards centre, where dialogue
// lives, and nothing anywhere reports a fault.
//
// aformat asks libswresample to do the downmix instead, and it negotiates
// against the layout that ACTUALLY ARRIVES rather than the one we assumed. A
// 5.1 track folds with the ITU-R BS.775 coefficients and the same normalization
// downmix.go reproduces by hand; a stereo track passes through; a mono track is
// centred. The guess stops being silent, which is the only property that made
// it dangerous.
//
// What this deliberately does NOT do is guess how many TRACKS there are. A
// profile selecting a track the ingest does not carry still emits [0:a:N] and
// FFmpeg still refuses to start. That failure is loud and diagnosable, and it
// was never the one worth protecting against.
const ProvisionalFilter = "aformat=channel_layouts=stereo"

// provisionalChain is trackChain for an unmeasured layout: the same optional
// stages, with the guessed pan matrix replaced by a runtime downmix.
//
// Gain is still applied, because a per-track gain is the operator's decision and
// does not depend on the layout. It goes after the fold so it scales the stereo
// result rather than one arbitrary channel.
func provisionalChain(src Source, track int, gain float64) string {
	chain := ProvisionalFilter
	if gain != 1 {
		chain += "," + fmt.Sprintf("volume=%s", fmtCoeff(gain))
	}
	return denoised(src, track, chain)
}

func denoised(src Source, track int, chain string) string {
	if src.DenoiseTrack(track) {
		// After the fold, not before: denoising the two channels that survive a
		// 5.1 downmix costs a third of what denoising all six would, and neither
		// the pan nor aformat can hide noise from the denoiser or create any.
		chain += "," + DenoiseFilter
	}
	return chain
}

// DenoiseFilter is the noise-suppression stage applied to a track annotated
// Denoise.
//
// arnndn sounds better on speech, but it is inert without an .rnnn model file
// and FFmpeg refuses to build the graph at all when the file is missing — a
// destination that will not start is a far worse outcome than a slightly noisier
// one, and this repo has been bitten three times by checks that failed closed.
// afftdn is always compiled in and needs nothing. track_noise lets it follow a
// room whose noise floor moves during the stream, which is the live case; the
// -25 dB starting floor is where it converges from rather than a hard
// assumption.
const DenoiseFilter = "afftdn=nr=12:nf=-25:tn=1"

// duckGraph splices a sidechain compressor into the per-track chains: the
// target tracks are summed into one bus, ducked by a key built from the trigger
// tracks, and re-enter the mix in place of the first target.
//
// The subtlety is that the trigger has to reach the output as well as the
// detector — an asplit, not a second reference to the same label, because a
// filter output pad feeds exactly one input.
//
// It mutates label for any trigger track it splits, and returns the ordered mix
// legs. Returning no legs means nothing was ducked and the caller should mix as
// usual; that is the deliberate response to a duck that cannot be built, since
// an un-ducked mix is still the operator's audio and a broken graph is silence.
func duckGraph(d Ducking, src Source, tracks []int, label map[int]string, provisional bool, n ns) (chains, legs, warns []string) {
	inMix := map[int]bool{}
	for _, t := range tracks {
		inMix[t] = true
	}

	isTarget := map[int]bool{}
	var targets []int
	for _, t := range sortedSet(d.Target) {
		if inMix[t] {
			isTarget[t] = true
			targets = append(targets, t)
		}
	}
	var triggers []int
	for _, t := range sortedSet(d.Trigger) {
		// A trigger that is not in this destination's mix is still usable: tap
		// the ingest track straight into the detector. That is how a feed which
		// excludes the mic can still duck its music when the host speaks.
		if _, present := src.TrackByIndex(t); inMix[t] || present {
			triggers = append(triggers, t)
		}
	}

	if len(targets) == 0 {
		return nil, nil, []string{"ducking is configured but none of its target tracks are in this destination's mix; no ducking is applied"}
	}
	if len(triggers) == 0 {
		return nil, nil, []string{"ducking is configured but none of its trigger tracks are present on the ingest; no ducking is applied"}
	}

	var keys []string
	for _, t := range triggers {
		if inMix[t] {
			mixLbl := fmt.Sprintf("%sa_t%d_mix", n.prefix, t)
			keyLbl := fmt.Sprintf("%sa_t%d_key", n.prefix, t)
			chains = append(chains, fmt.Sprintf("[%s]asplit=2[%s][%s]", label[t], mixLbl, keyLbl))
			label[t] = mixLbl
			keys = append(keys, keyLbl)
			continue
		}
		tr, _ := src.TrackByIndex(t)
		keyLbl := fmt.Sprintf("%sa_k%d", n.prefix, t)
		// Downmix the tap the same way a contributing track would, so the two
		// sidechaincompress inputs always agree on channel layout, and denoise
		// it if it is annotated: room noise opening the duck is precisely the
		// failure the annotation exists to prevent.
		// The same fold the mix legs get. In provisional mode that is the
		// runtime downmix, not a guessed matrix: building the key from
		// placeholder channel counts would take channels 0 and 1 of a wide
		// track where the mix takes a proper fold, so the detector would be
		// listening to a different signal than the one being ducked.
		tap := trackChain(src, t, CellsForTrack(t, tr, 1.0))
		if provisional {
			tap = provisionalChain(src, t, 1)
		}
		chains = append(chains, fmt.Sprintf("[0:a:%d]%s[%s]", t, tap, keyLbl))
		keys = append(keys, keyLbl)
	}

	key := keys[0]
	if len(keys) > 1 {
		chains = append(chains, fmt.Sprintf("%samix=inputs=%d:duration=longest:normalize=0[%s]",
			bracket(keys), len(keys), n.of("a_duckkey")))
		key = n.of("a_duckkey")
	}

	bus := label[targets[0]]
	if len(targets) > 1 {
		in := make([]string, 0, len(targets))
		for _, t := range targets {
			in = append(in, label[t])
		}
		// Sum the targets before ducking rather than compressing each of them.
		// amix=normalize=0 is a plain sum, so summing early is arithmetically
		// identical, and one compressor means one gain-reduction envelope
		// instead of several that could drift apart.
		chains = append(chains, fmt.Sprintf("%samix=inputs=%d:duration=longest:normalize=0[%s]",
			bracket(in), len(in), n.of("a_duckin")))
		bus = n.of("a_duckin")
	}
	chains = append(chains, fmt.Sprintf("[%s][%s]sidechaincompress=%s[%s]", bus, key, duckParams(d), n.of("a_duck")))

	placed := false
	for _, t := range tracks {
		if isTarget[t] {
			if !placed {
				legs = append(legs, n.of("a_duck"))
				placed = true
			}
			continue
		}
		legs = append(legs, label[t])
	}
	return chains, legs, nil
}

// duckParams renders sidechaincompress' parameters.
//
// threshold is linear here, not dB: sidechaincompress takes 0.000976563..1 and
// silently means something else entirely if handed "-24".
func duckParams(d Ducking) string {
	return fmt.Sprintf("threshold=%s:ratio=%s:attack=%s:release=%s:detection=rms:link=maximum",
		fmtCoeff(dbToLinear(d.ThresholdDB)),
		fmtCoeff(d.Ratio),
		fmtCoeff(d.AttackMS),
		fmtCoeff(d.ReleaseMS))
}

// dbToLinear converts dBFS to the linear amplitude ratio FFmpeg's dynamics
// filters expect.
func dbToLinear(db float64) float64 {
	return math.Pow(10, db/20)
}

// resolveCells expands a profile into matrix cells validated against the live
// source, dropping (with a warning) anything the ingest cannot supply.
func resolveCells(p Profile, src Source) ([]Cell, []string) {
	var warns []string
	var out []Cell

	switch p.Mode {
	case ModeMatrix:
		// Track the level a cell would have contributed even when it is dropped,
		// so a narrowed ingest can be reported as the volume change it is rather
		// than only as a list of channel numbers.
		var wanted, kept [OutChannels]float64
		for _, c := range p.Matrix {
			if c.Gain <= 0 {
				continue
			}
			if c.Out >= 0 && c.Out < OutChannels {
				wanted[c.Out] += c.Gain
			}
			t, ok := src.TrackByIndex(c.Track)
			if !ok {
				warns = append(warns, fmt.Sprintf("track %d is not present on the ingest; its matrix cells are ignored", c.Track+1))
				continue
			}
			if c.Channel >= t.Channels {
				warns = append(warns, fmt.Sprintf("track %d has %d channel(s); channel %d is ignored", c.Track+1, t.Channels, c.Channel+1))
				continue
			}
			if c.Out >= 0 && c.Out < OutChannels {
				kept[c.Out] += c.Gain
			}
			out = append(out, c)
		}
		if w := levelWarning(wanted, kept); w != "" {
			warns = append(warns, w)
		}

	default: // ModeSimple
		for _, sel := range p.Tracks {
			if !sel.Enabled || sel.Gain <= 0 {
				continue
			}
			t, ok := src.TrackByIndex(sel.Track)
			if !ok {
				warns = append(warns, fmt.Sprintf("track %d is selected but not present on the ingest; it is ignored", sel.Track+1))
				continue
			}
			// Simple mode recomputes the downmix against the live layout every
			// time, so it cannot be left holding coefficients scaled for a width
			// the ingest no longer has.
			out = append(out, CellsForTrack(sel.Track, t, sel.Gain)...)
		}
	}

	warns = dedupe(warns)
	return out, warns
}

// levelWarning reports how much quieter dropping cells made the mix.
//
// A saved matrix outlives the ingest it was drawn against. When a track
// narrows, the cells addressing the missing channels are dropped and the
// survivors keep coefficients that were scaled for the old width — so a 5.1
// matrix meeting a stereo ingest still compiles, still runs, and sits 7.7 dB
// down with nothing anywhere saying the level moved. The coefficients are the
// operator's to change, not ours to rescale behind their back; what was
// missing was anyone saying it happened.
func levelWarning(wanted, kept [OutChannels]float64) string {
	// The quietest surviving leg, in dB relative to what the profile asked for.
	worst := 0.0
	silent := false
	for out := range wanted {
		if wanted[out] <= 0 {
			continue
		}
		if kept[out] <= 0 {
			silent = true
			continue
		}
		if d := 20 * math.Log10(kept[out]/wanted[out]); d < worst {
			worst = d
		}
	}
	switch {
	case silent:
		return "the ingest no longer carries the channels one side of this matrix was routing; that side is silent"
	case worst < -0.5:
		// Below half a dB is not worth a line in the UI; it is the rounding on
		// a single trimmed cell, not something anyone can hear.
		return fmt.Sprintf("the ingest no longer carries every channel this matrix was routing, so the mix is %.1f dB quieter than the profile intends", -worst)
	}
	return ""
}

// applyRolePolicy drops every cell belonging to a track whose role this
// destination refuses. It is the only place in the compiler where a role
// changes what is sent, and it does nothing at all unless the destination set
// ExcludeRoles — an unannotated source, or one nobody has written a policy for,
// takes the same path it always did.
func applyRolePolicy(p Profile, src Source, cells []Cell) ([]Cell, []string) {
	excluded := p.ExcludedTracks(src)
	if len(excluded) == 0 {
		return cells, nil
	}
	drop := map[int]bool{}
	for _, t := range excluded {
		drop[t] = true
	}

	var warns []string
	out := make([]Cell, 0, len(cells))
	for _, c := range cells {
		if drop[c.Track] {
			// Loud on purpose. A track vanishing from a mix because of a policy
			// set weeks ago on a different screen is exactly the kind of silence
			// nobody can explain at 3am.
			warns = append(warns, fmt.Sprintf("track %d carries the %q role, which this destination excludes; it is not being sent",
				c.Track+1, src.RoleOf(c.Track)))
			continue
		}
		out = append(out, c)
	}
	return out, dedupe(warns)
}

// PanFilter renders the pan filter for a single track's cells. Exported
// because it is the smallest independently meaningful unit of the engine and
// is what the tests pin down hardest.
func PanFilter(cells []Cell) string {
	// Deterministic ordering: by output, then by source channel.
	sorted := append([]Cell(nil), cells...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Out != sorted[j].Out {
			return sorted[i].Out < sorted[j].Out
		}
		return sorted[i].Channel < sorted[j].Channel
	})

	var exprs []string
	for out := 0; out < OutChannels; out++ {
		var terms []string
		for _, c := range sorted {
			if c.Out != out || c.Gain == 0 {
				continue
			}
			terms = append(terms, fmt.Sprintf("%s*c%d", fmtCoeff(c.Gain), c.Channel))
		}
		if len(terms) == 0 {
			// pan requires an expression for every output channel of the
			// declared layout. An explicitly silent leg is legal and is the
			// honest rendering of "nothing routed here".
			terms = []string{"0*c0"}
		}
		exprs = append(exprs, fmt.Sprintf("c%d=%s", out, strings.Join(terms, "+")))
	}
	return "pan=stereo|" + strings.Join(exprs, "|")
}

// resolveNorm turns NormAuto into a concrete stage.
//
// The original rule was "summing across tracks is the only thing that creates
// clipping, so a single-track profile stays untouched". Half true: pan sums
// too, per output channel, and Validate caps only the per-cell gain. A
// one-track matrix with three cells at MaxGain on one leg compiles to
// c0=2*c0+2*c2+2*c4 — six times full scale, with NormAuto having decided no
// protection was needed.
//
// So the track count keeps its say, and peak — the largest total gain any one
// output channel applies — gets a say as well. Widening rather than replacing
// is deliberate: no profile that has a limiter today loses it, and every
// profile that peaks at or below unity compiles to the string it always did.
func resolveNorm(m NormMode, trackCount int, peak float64) NormMode {
	if m != NormAuto {
		return m
	}
	if trackCount >= 2 || peak > 1 {
		return NormLimiter
	}
	return NormOff
}

// normFilterFor picks the clip-protection stage, preferring a configured
// loudness target over the fixed one.
func normFilterFor(m NormMode, l Loudness, haveTarget bool) string {
	if m == NormLoudnorm && haveTarget {
		return loudnormFilter(l)
	}
	return normFilter(m)
}

func normFilter(m NormMode) string {
	switch m {
	case NormLimiter:
		// level=disabled turns off alimiter's automatic makeup gain; we want
		// a ceiling, not an AGC that would undo the user's gain staging.
		return "alimiter=limit=0.95:level=disabled"
	case NormLoudnorm:
		// EBU R128 at the -16 LUFS target the streaming platforms expect.
		return "loudnorm=I=-16:TP=-1.5:LRA=11"
	default:
		return ""
	}
}

// loudnormFilter renders a destination's own programme-loudness target.
//
// This is single-pass loudnorm, so the delivered integrated loudness is
// approximate: it adapts as it goes instead of measuring the programme first,
// converges over roughly the first minute, and drifts with the material. That
// is not a shortcoming to fix — two-pass loudnorm needs the whole programme up
// front, which a live stream by definition does not have, and single-pass is
// what every live loudness stage in the industry runs.
func loudnormFilter(l Loudness) string {
	return fmt.Sprintf("loudnorm=I=%s:TP=%s:LRA=%s",
		fmtCoeff(l.TargetLUFS), fmtCoeff(l.TruePeakDB), fmtCoeff(l.RangeLU))
}

// fmtCoeff renders a gain coefficient at 4 decimal places (~0.0009 resolution,
// far below audibility) with trailing zeros trimmed, so filter strings stay
// readable and byte-stable across runs.
func fmtCoeff(g float64) string {
	s := strconv.FormatFloat(g, 'f', 4, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimSuffix(s, ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

// bracket wraps filter labels for use as filter inputs: a, b -> "[a][b]".
func bracket(labels []string) string {
	var b strings.Builder
	for _, l := range labels {
		b.WriteByte('[')
		b.WriteString(l)
		b.WriteByte(']')
	}
	return b.String()
}

// sortedSet returns the ascending distinct members of a track list, so the
// generated graph does not depend on the order the UI happened to send.
func sortedSet(in []int) []int {
	seen := map[int]bool{}
	out := make([]int, 0, len(in))
	for _, v := range in {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Ints(out)
	return out
}

func summarize(tracks []int) string {
	if len(tracks) == 0 {
		return "No audio"
	}
	parts := make([]string, len(tracks))
	for i, t := range tracks {
		parts[i] = strconv.Itoa(t + 1) // 1-based for humans
	}
	if len(tracks) == 1 {
		return "Track " + parts[0] + " → stereo"
	}
	return "Tracks " + strings.Join(parts, ", ") + " → stereo"
}

func dedupe(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
