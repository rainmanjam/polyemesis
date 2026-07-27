package routing

import (
	"fmt"
	"sort"
	"strings"
)

// Preset IDs.
const (
	PresetEverything = "everything"
	PresetNoMusic    = "no-music"
	PresetMicOnly    = "mic-only"
	PresetSurround   = "surround-downmix"

	// The role-aware presets. Each names what it wants ("the commentary", "the
	// clean feed") instead of a track number, and each falls back to the
	// index-based behaviour when the ingest carries no role metadata — an
	// un-annotated stream must still get a sensible mix, never an error and
	// never silence.
	PresetCommentary  = "commentary-language"
	PresetCleanFeed   = "clean-feed"
	PresetExceptMusic = "except-music"
)

// PresetPlatformPrefix marks the presets generated from the music-rights table.
// The platform is in the ID so the catalogue can grow a row without the UI
// learning a new option shape.
const PresetPlatformPrefix = "platform:"

// PlatformPresetID is the preset ID that applies a platform's policy.
func PlatformPresetID(p Platform) string { return PresetPlatformPrefix + string(p) }

// platformPreset splits a platform preset ID back apart.
func platformPreset(id string) (Platform, bool) {
	rest, ok := strings.CutPrefix(id, PresetPlatformPrefix)
	if !ok || rest == "" {
		return "", false
	}
	p := Platform(rest)
	for _, pol := range platformPolicies {
		if pol.Platform == p {
			return p, true
		}
	}
	return "", false
}

// Preset is a named starting point for a routing profile.
type Preset struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// NeedsMusicTrack / NeedsMicTrack / NeedsSurroundTrack / NeedsCleanTrack
	// tell the UI which track picker to show alongside the preset button. On a
	// role-aware preset the picker is the fallback: it is what gets used when
	// the ingest has no annotation to answer the question with.
	NeedsMusicTrack    bool `json:"needsMusicTrack"`
	NeedsMicTrack      bool `json:"needsMicTrack"`
	NeedsSurroundTrack bool `json:"needsSurroundTrack"`
	NeedsCleanTrack    bool `json:"needsCleanTrack"`
	// NeedsLanguage asks the UI for a BCP-47 tag, e.g. "es".
	NeedsLanguage bool `json:"needsLanguage"`

	// Platform is set on the presets generated from the music-rights table, so
	// the UI can badge them with what the policy actually is.
	Platform Platform `json:"platform,omitempty"`
	// Policy is the rights decision this preset will apply, pre-resolved for
	// the badge. Zero value on every preset that has no opinion about music.
	Policy MusicDecision `json:"policy,omitempty"`

	// Loudness and DelayMS are the destination-side defaults this preset
	// implies. Both are nil/zero on the presets that imply nothing, which is
	// what keeps the original four compiling to the exact bytes they always
	// did. A platform preset carries the loudness its platform normalizes to,
	// so picking "YouTube" starts you at -14 LUFS rather than at whatever the
	// ingest happened to be.
	Loudness *Loudness `json:"loudness,omitempty"`
	DelayMS  int       `json:"delayMs,omitempty"`
}

// Presets is the catalogue offered in the routing editor.
func Presets() []Preset {
	out := []Preset{
		{
			ID:          PresetEverything,
			Name:        "Everything",
			Description: "Sum every audio track the ingest is carrying.",
		},
		{
			ID:              PresetNoMusic,
			Name:            "No music",
			Description:     "Sum every track except the one carrying copyrighted music.",
			NeedsMusicTrack: true,
		},
		{
			ID:            PresetMicOnly,
			Name:          "Mic only",
			Description:   "Just the microphone track. Commentary, no game or media audio.",
			NeedsMicTrack: true,
		},
		{
			ID:                 PresetSurround,
			Name:               "5.1 → stereo",
			Description:        "Downmix one surround track to stereo with standard ITU coefficients, as an editable matrix.",
			NeedsSurroundTrack: true,
		},
		{
			ID:          PresetCommentary,
			Name:        "Commentary",
			Description: "The commentary tracks in one language. Falls back to the mic track when the ingest carries no role or language metadata.",
			// The mic picker is the fallback, not the answer.
			NeedsMicTrack: true,
			NeedsLanguage: true,
		},
		{
			ID:              PresetCleanFeed,
			Name:            "Clean feed",
			Description:     "Programme audio with no commentary on it — what a rights holder, an archive or a second-language broadcast wants.",
			NeedsCleanTrack: true,
		},
		{
			ID:              PresetExceptMusic,
			Name:            "Everything except music",
			Description:     "Every track except the ones marked as music, and a standing instruction to keep excluding music if it moves to another track.",
			NeedsMusicTrack: true,
		},
	}

	for _, pol := range platformPolicies {
		out = append(out, platformPresetEntry(pol))
	}
	return out
}

// platformPresetEntry turns one row of the music-rights table into a preset.
// Building these from the table rather than by hand is the point: adding a
// platform to the policy adds its preset, and the two can never disagree.
func platformPresetEntry(pol PlatformPolicy) Preset {
	dec := ResolveMusicPolicy(pol.Platform, MusicPolicyDefault)
	p := Preset{
		ID:              PlatformPresetID(pol.Platform),
		Name:            pol.Name,
		Description:     "Everything the ingest is carrying, with this destination's defaults applied: " + dec.Summary + ".",
		Platform:        pol.Platform,
		Policy:          dec,
		NeedsMusicTrack: pol.ExcludeMusic,
	}
	if pol.TargetLUFS != 0 {
		p.Description += " Loudness target " + trimFloat(pol.TargetLUFS) + " LUFS."
		p.Loudness = &Loudness{TargetLUFS: pol.TargetLUFS}
	}
	return p
}

func trimFloat(f float64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.1f", f), "0"), ".")
}

// PresetOpts carries the track choices a preset needs. Defaults follow the
// common OBS convention: track 1 full mix, track 2 clean mix, track 3 mic.
//
// Every field here is a fallback for the role-aware presets and the answer for
// the index-based ones, which is why they all keep working on an ingest nobody
// has annotated.
type PresetOpts struct {
	MusicTrack    int `json:"musicTrack"`    // 0-based
	MicTrack      int `json:"micTrack"`      // 0-based
	SurroundTrack int `json:"surroundTrack"` // 0-based
	CleanTrack    int `json:"cleanTrack"`    // 0-based
	// Language is the BCP-47 tag the commentary preset selects on, e.g. "es".
	// Empty asks for "whatever commentary there is".
	Language string `json:"language,omitempty"`
	// MusicPolicy overrides the platform table for a platform preset. Zero
	// value follows the table.
	MusicPolicy MusicPolicyChoice `json:"musicPolicy,omitempty"`
}

// DefaultPresetOpts returns the OBS-convention defaults.
func DefaultPresetOpts() PresetOpts {
	return PresetOpts{MusicTrack: 0, MicTrack: 2, SurroundTrack: 0, CleanTrack: 1}
}

// basePreset is the blank simple-mode profile every preset starts from: all six
// rows present at unity, none enabled.
func basePreset() Profile {
	p := Profile{Mode: ModeSimple, Normalize: NormAuto, SampleRate: 48000}
	for i := 0; i < MaxTracks; i++ {
		p.Tracks = append(p.Tracks, TrackSel{Track: i, Gain: 1.0})
	}
	return p
}

func enableTrack(p *Profile, idx int) {
	for i := range p.Tracks {
		if p.Tracks[i].Track == idx {
			p.Tracks[i].Enabled = true
		}
	}
}

// presetCtx is everything a preset builder needs: the layout it is selecting
// against, already substituted for DefaultSource when the ingest has not been
// probed, and the operator's fallback picks.
type presetCtx struct {
	src     Source
	tracks  []Track // the tracks actually present
	present map[int]bool
	opts    PresetOpts
}

// selecting returns a blank profile with the given tracks switched on.
func (c presetCtx) selecting(idx []int) Profile {
	p := basePreset()
	for _, i := range idx {
		enableTrack(&p, i)
	}
	return p
}

// allTracks is every present track index, ascending.
func (c presetCtx) allTracks() []int {
	out := make([]int, 0, len(c.tracks))
	for _, t := range c.tracks {
		out = append(out, t.Index)
	}
	sort.Ints(out)
	return out
}

// ApplyPreset builds a profile from a preset against the live source layout.
func ApplyPreset(id string, src Source, opts PresetOpts) (Profile, error) {
	tracks := src.Tracks
	if len(tracks) == 0 {
		tracks = DefaultSource().Tracks
	}
	c := presetCtx{src: src, tracks: tracks, opts: opts, present: make(map[int]bool, len(tracks))}
	for _, t := range tracks {
		c.present[t.Index] = true
	}

	build, ok := presetBuilder(id)
	if !ok {
		return Profile{}, fmt.Errorf("unknown routing preset %q", id)
	}
	p, err := build(id, c)
	if err != nil {
		return Profile{}, err
	}

	// The catalogue's declared defaults, applied in one place so a preset can
	// imply a loudness target or a delay by saying so in Presets() rather than
	// by growing another branch here. Anything the builder already decided
	// wins; anything the preset does not declare stays at its zero value, which
	// is what keeps the original four byte-identical.
	if pre, found := presetByID(id); found {
		if p.Loudness == nil && pre.Loudness != nil {
			l := *pre.Loudness
			p.Loudness = &l
		}
		if p.DelayMS == 0 {
			p.DelayMS = pre.DelayMS
		}
	}
	return p, nil
}

// presetBuilder maps a preset ID to the function that builds its profile.
func presetBuilder(id string) (func(string, presetCtx) (Profile, error), bool) {
	switch id {
	case PresetEverything:
		return buildEverything, true
	case PresetNoMusic:
		return buildNoMusic, true
	case PresetMicOnly:
		return buildMicOnly, true
	case PresetCommentary:
		return buildCommentary, true
	case PresetCleanFeed:
		return buildCleanFeed, true
	case PresetExceptMusic:
		return buildExceptMusic, true
	case PresetSurround:
		return buildSurround, true
	}
	if _, ok := platformPreset(id); ok {
		return buildPlatform, true
	}
	return nil, false
}

func presetByID(id string) (Preset, bool) {
	for _, p := range Presets() {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}

func buildEverything(_ string, c presetCtx) (Profile, error) {
	return c.selecting(c.allTracks()), nil
}

func buildNoMusic(id string, c presetCtx) (Profile, error) {
	var sel []int
	for _, i := range c.allTracks() {
		if i != c.opts.MusicTrack {
			sel = append(sel, i)
		}
	}
	p := c.selecting(sel)
	if len(p.SelectedTracks()) == 0 {
		return Profile{}, fmt.Errorf("preset %q would leave no audio: the ingest only carries the excluded track", id)
	}
	return p, nil
}

// buildMicOnly puts roles first and the picker second: an ingest that says
// which track is the mic knows better than a convention about OBS defaults.
func buildMicOnly(_ string, c presetCtx) (Profile, error) {
	return c.selecting(fallbackTracks(roleTracks(c.src, c.present, RoleMic), c.opts.MicTrack)), nil
}

func buildCommentary(_ string, c presetCtx) (Profile, error) {
	return c.selecting(commentaryTracks(c.src, c.present, c.opts)), nil
}

func buildCleanFeed(_ string, c presetCtx) (Profile, error) {
	return c.selecting(cleanFeedTracks(c.src, c.present, c.tracks, c.opts)), nil
}

func buildExceptMusic(id string, c presetCtx) (Profile, error) {
	music := fallbackTracks(roleTracks(c.src, c.present, RoleMusic), c.opts.MusicTrack)
	excluded := make(map[int]bool, len(music))
	for _, i := range music {
		excluded[i] = true
	}
	var sel []int
	for _, i := range c.allTracks() {
		if !excluded[i] {
			sel = append(sel, i)
		}
	}

	p := c.selecting(sel)
	if len(p.SelectedTracks()) == 0 {
		return Profile{}, fmt.Errorf("preset %q would leave no audio: every track the ingest carries is music", id)
	}
	// The standing instruction, which is the part that survives the streamer
	// moving music to another track. Inert until something is actually marked
	// as music, so setting it on an un-annotated ingest changes nothing today
	// and starts working the moment it can.
	p.ExcludeRoles = []TrackRole{RoleMusic}
	return p, nil
}

// buildSurround emits an editable matrix rather than a checkbox, because the
// point of this preset is to *show* the coefficients so the user can then do
// things simple mode cannot — e.g. delete the front channels and keep only the
// rears.
func buildSurround(_ string, c presetCtx) (Profile, error) {
	ch := 6
	if t, ok := c.src.TrackByIndex(c.opts.SurroundTrack); ok && t.Channels > 0 {
		ch = t.Channels
	}
	p := Profile{Mode: ModeMatrix, Normalize: NormAuto, SampleRate: 48000}
	p.Matrix = CellsForTrack(c.opts.SurroundTrack, ch, 1.0)
	for i := 0; i < MaxTracks; i++ {
		p.Tracks = append(p.Tracks, TrackSel{Track: i, Gain: 1.0, Enabled: i == c.opts.SurroundTrack})
	}
	return p, nil
}

// buildPlatform builds "everything this platform will accept": every track the
// ingest carries, minus whatever its music policy refuses. The loudness target
// rides in from the catalogue entry, which took it from the same policy row.
func buildPlatform(id string, c presetCtx) (Profile, error) {
	plat, ok := platformPreset(id)
	if !ok {
		return Profile{}, fmt.Errorf("unknown routing preset %q", id)
	}

	p, dec := ApplyMusicPolicy(c.selecting(c.allTracks()), c.src, plat, c.opts.MusicPolicy)
	if len(p.SelectedTracks()) == 0 {
		return Profile{}, fmt.Errorf("preset %q would leave no audio: %s, and every track the ingest carries is marked as music", id, dec.Summary)
	}
	return p, nil
}

// roleTracks returns the tracks the ingest is currently carrying that hold any
// of the given roles, ascending and deduplicated.
//
// An empty result means "role metadata has nothing to say here", which every
// caller turns into the index-based fallback rather than into an empty
// selection. Selecting nothing is how a preset produces a silent destination,
// and no amount of missing metadata justifies that.
func roleTracks(src Source, present map[int]bool, roles ...TrackRole) []int {
	if len(src.Annotations) == 0 {
		return nil
	}
	seen := map[int]bool{}
	var out []int
	for _, r := range roles {
		for _, i := range src.TracksWithRole(r) {
			if present[i] && !seen[i] {
				seen[i] = true
				out = append(out, i)
			}
		}
	}
	sort.Ints(out)
	return out
}

// languageTracks returns the tracks the ingest is carrying whose effective
// language matches tag. Empty tag matches nothing, not everything.
func languageTracks(src Source, present map[int]bool, tag string) []int {
	var out []int
	for _, i := range src.TracksWithLanguage(tag) {
		if present[i] {
			out = append(out, i)
		}
	}
	return out
}

// fallbackTracks returns sel, or the single index the operator picked when sel
// is empty.
func fallbackTracks(sel []int, fallback int) []int {
	if len(sel) > 0 {
		return sel
	}
	return []int{fallback}
}

// commentaryTracks resolves "the commentary, in this language" down a cascade
// that always ends somewhere audible:
//
//  1. tracks that are both roled commentary and tagged with the language;
//  2. tracks tagged with the language, whatever their role — a Spanish track
//     nobody roled is still the Spanish track;
//  3. tracks roled commentary in any language. Asking for Spanish and getting
//     the only commentary there is beats getting silence, and a preset is a
//     starting point the operator sees before saving;
//  4. the mic track, because on an un-annotated ingest the commentary is the
//     microphone and that is what "Mic only" has always meant.
func commentaryTracks(src Source, present map[int]bool, opts PresetOpts) []int {
	byRole := roleTracks(src, present, RoleCommentary)

	if strings.TrimSpace(opts.Language) != "" {
		byLang := languageTracks(src, present, opts.Language)
		if both := intersectTracks(byRole, byLang); len(both) > 0 {
			return both
		}
		if len(byLang) > 0 {
			return byLang
		}
	}
	return fallbackTracks(byRole, opts.MicTrack)
}

// cleanFeedTracks resolves "programme audio with nobody talking over it":
//
//  1. tracks explicitly roled clean;
//  2. otherwise, on an annotated ingest, everything that is not a voice —
//     subtraction gets the right answer without the operator having to mark a
//     track "clean" as well as marking the mic "mic";
//  3. otherwise the clean-track picker, which defaults to the OBS convention.
func cleanFeedTracks(src Source, present map[int]bool, tracks []Track, opts PresetOpts) []int {
	if clean := roleTracks(src, present, RoleClean); len(clean) > 0 {
		return clean
	}

	if voices := roleTracks(src, present, RoleMic, RoleCommentary); len(voices) > 0 {
		isVoice := make(map[int]bool, len(voices))
		for _, i := range voices {
			isVoice[i] = true
		}
		var rest []int
		for _, t := range tracks {
			if !isVoice[t.Index] {
				rest = append(rest, t.Index)
			}
		}
		if len(rest) > 0 {
			sort.Ints(rest)
			return rest
		}
	}

	return []int{opts.CleanTrack}
}

func intersectTracks(a, b []int) []int {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	inB := make(map[int]bool, len(b))
	for _, i := range b {
		inB[i] = true
	}
	var out []int
	for _, i := range a {
		if inB[i] {
			out = append(out, i)
		}
	}
	return out
}
