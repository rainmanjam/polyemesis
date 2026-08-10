package db

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/routing"
)

// copyDest is the shape the feature exists for: an SRT contribution feed that
// forwards the encoder's own tracks.
func copyDest() *Destination {
	d := validDest()
	d.Kind = DestSRT
	d.URL = "srt://relay.example:9000"
	d.StreamKey = ""
	d.Audio = AudioEncoding{Copy: true}
	return d
}

// The positive cases, first and deliberately. Every refusal below would be
// satisfied by a validator that refused everything, and a copy toggle nobody
// can turn on is the same bug as one that ignores its settings.
func TestACopyDestinationIsAcceptedOnTheKindsThatCanCarryIt(t *testing.T) {
	if err := copyDest().Validate(); err != nil {
		t.Fatalf("copy on an SRT destination was refused: %v", err)
	}

	f := copyDest()
	f.Kind = DestFile
	f.URL = "archive.mkv"
	if err := f.Validate(); err != nil {
		t.Fatalf("copy on a file destination was refused: %v", err)
	}

	// And a destination that has NOT opted in must stay exactly as valid as it
	// was, or every row written before this column existed becomes unsavable.
	off := validDest()
	if err := off.Validate(); err != nil {
		t.Fatalf("a destination with no audio settings at all was refused: %v", err)
	}
}

// Track selection and role exclusion are the two things copy DOES honour, and
// the two most likely to be swept up by a validator written as "copy means the
// profile must be empty". ExcludeRoles in particular is the DMCA switch; a copy
// destination that could not drop the music track would be useless to the
// archive it exists for.
func TestCopyDoesNotRefuseTrackSelectionOrRoleExclusion(t *testing.T) {
	d := copyDest()
	d.Profile.Tracks = []routing.TrackSel{
		{Track: 0, Enabled: true, Gain: 1},
		{Track: 1, Enabled: false, Gain: 1},
		{Track: 2, Enabled: true, Gain: 1},
	}
	d.Profile.ExcludeRoles = []routing.TrackRole{routing.RoleMusic}
	if err := d.Validate(); err != nil {
		t.Fatalf("selecting tracks and excluding a role on a copy destination "+
			"was refused, which takes the DMCA switch away from the destinations "+
			"most likely to want it: %v", err)
	}

	// A gain left on a DISABLED row is a setting nothing will ever read, so it
	// must not be refused either -- the UI keeps whatever the operator last
	// dragged there.
	d.Profile.Tracks[1].Gain = 0.5
	if err := d.Validate(); err != nil {
		t.Fatalf("a gain on a track that is switched off was refused: %v", err)
	}
}

// Every setting copy cannot honour, refused, and refused by NAME. A message
// that says only "incompatible with copy" sends the operator hunting through a
// form for which of eight controls it meant.
func TestCopyRefusesEverySettingItCannotHonourAndSaysWhich(t *testing.T) {
	cases := []struct {
		name string
		bend func(*Destination)
		want string
	}{{
		// Same failure class as Opus on RTMP: it muxes and the platform
		// rejects it, so the operator sees a stream that uploads cleanly and
		// never appears.
		name: "rtmp kind",
		bend: func(d *Destination) {
			d.Kind = DestRTMP
			d.URL = "rtmp://ingest.example/live"
		},
		want: "not available on an RTMP destination",
	}, {
		name: "audio-only kind",
		bend: func(d *Destination) {
			d.Kind = DestAudio
			d.URL = "podcast.mp3"
		},
		want: "not available on an audio-only destination",
	}, {
		name: "codec",
		bend: func(d *Destination) { d.Audio.Codec = DestAudioOpus },
		want: "cannot be set on a destination that copies its audio",
	}, {
		name: "mono",
		bend: func(d *Destination) { d.Audio.Mono = true },
		want: "mono cannot be set",
	}, {
		name: "limiter",
		bend: func(d *Destination) { d.Profile.Normalize = routing.NormLimiter },
		want: "the limiter cannot run",
	}, {
		name: "loudnorm",
		bend: func(d *Destination) { d.Profile.Normalize = routing.NormLoudnorm },
		want: "loudness normalization cannot run",
	}, {
		name: "loudness target",
		bend: func(d *Destination) {
			d.Profile.Loudness = &routing.Loudness{TargetLUFS: -14}
		},
		want: "a loudness target cannot be applied",
	}, {
		name: "ducking",
		bend: func(d *Destination) {
			d.Profile.Ducking = &routing.Ducking{
				Trigger: []int{0}, Target: []int{1},
			}
		},
		want: "ducking cannot run",
	}, {
		name: "delay",
		bend: func(d *Destination) { d.Profile.DelayMS = 250 },
		want: "an audio delay of 250 ms cannot be applied",
	}, {
		name: "negative delay",
		bend: func(d *Destination) { d.Profile.DelayMS = -300 },
		want: "an audio delay of -300 ms cannot be applied",
	}, {
		name: "simple-mode gain",
		bend: func(d *Destination) {
			d.Profile.Tracks = []routing.TrackSel{{Track: 1, Enabled: true, Gain: 0.5}}
		},
		// Named by track number, because "a gain is set" does not say which
		// slider to move.
		want: "track 2 is set to a gain other than unity",
	}, {
		name: "matrix gain",
		bend: func(d *Destination) {
			d.Profile.Mode = routing.ModeMatrix
			d.Profile.Matrix = []routing.Cell{
				{Track: 0, Channel: 0, Out: routing.OutL, Gain: 1},
				{Track: 0, Channel: 1, Out: routing.OutR, Gain: 0.5},
			}
		},
		want: "the mix matrix sets a gain other than unity",
	}, {
		// The one a numbers-only UI hides completely: same gains, different
		// wiring.
		name: "matrix channel routing",
		bend: func(d *Destination) {
			d.Profile.Mode = routing.ModeMatrix
			d.Profile.Matrix = []routing.Cell{
				{Track: 0, Channel: 0, Out: routing.OutR, Gain: 1},
				{Track: 0, Channel: 1, Out: routing.OutL, Gain: 1},
			}
		},
		want: "the mix matrix routes channels between outputs",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := copyDest()
			tc.bend(d)
			err := d.Validate()
			if err == nil {
				t.Fatalf("%s was accepted alongside copy; the setting would be "+
					"stored and silently never applied", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error was %q, want one naming the setting (%q)", err, tc.want)
			}

			// The same bend without copy must still be fine, or the case above
			// proves nothing about copy.
			ok := d
			ok.Audio.Copy = false
			if err := ok.Validate(); err != nil {
				t.Fatalf("%s was refused even without copy, so the refusal above "+
					"is not about copy at all: %v", tc.name, err)
			}
		})
	}
}

// NormAuto is the one normalization value copy leaves alone. It means "no
// opinion" -- resolveNorm turns it into a filter only when there is a sum to
// protect from clipping, and a copy destination has no sum. Refusing it would
// mean every destination created from DefaultProfile, which is every new one,
// could not be switched to copy without first changing a control that was going
// to do nothing.
func TestCopyAcceptsAutoNormalizationBecauseItMeansNoOpinion(t *testing.T) {
	d := copyDest()
	d.Profile.Normalize = routing.NormAuto
	if err := d.Validate(); err != nil {
		t.Fatalf("auto normalization was refused on a copy destination, so a "+
			"newly created destination cannot be switched to copy: %v", err)
	}
	d.Profile.Normalize = routing.NormOff
	if err := d.Validate(); err != nil {
		t.Fatalf("normalization off was refused on a copy destination: %v", err)
	}
}

// A setting reachable in the UI and lost on save is the same class of bug as
// one that is unreachable. The transport block has this test; so does this.
func TestCopySurvivesTheDatabaseRoundTrip(t *testing.T) {
	d := testDB(t)
	src, err := d.DefaultSourceID()
	if err != nil {
		t.Fatalf("default source: %v", err)
	}
	in := copyDest()
	in.SourceID = &src

	created, err := d.CreateDestination(in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !created.Audio.Copy {
		t.Fatalf("copy came back as %+v; it was dropped between the INSERT and "+
			"the SELECT, so the destination would silently keep re-encoding",
			created.Audio)
	}

	// And turning it off must actually turn it off, which is the half a
	// migration with a hardcoded default gets wrong.
	created.Audio.Copy = false
	updated, err := d.UpdateDestination(created)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Audio.Copy {
		t.Error("copy survived being switched off")
	}
}
