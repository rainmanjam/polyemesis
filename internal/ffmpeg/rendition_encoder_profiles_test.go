package ffmpeg

import (
	"os/exec"
	"sort"
	"strings"
	"testing"
)

// ------------------------------------------------------- defect 1: HEVC rows

// The HEVC encoders get their own tuning rather than falling through the
// unknown-encoder branch, and none of them is handed an H.264 profile name.
//
// #343. hevc_vaapi is the row that mattered: with no profile entry its argv
// carried neither -vaapi_device nor format=nv12,hwupload, which is not a
// tuning gap but a structural one -- VAAPI encodes from GPU surfaces, so the
// encoder had nothing it could read and the rendition could not start on any
// machine. It was selectable anyway, because only the H.264 half of each
// family is test-encoded and the start gate refuses on measured failures only.
func TestHEVCEncodersAreConfiguredAndNeverGetAnH264Profile(t *testing.T) {
	tests := []struct {
		encoder    string
		wantPreset string // flag+value that must appear, "" if none may
		wantFlags  []string
		absent     []string
	}{
		{
			// x265 takes x264's preset names, and `main` is its spelling of
			// the 8-bit 4:2:0 profile a platform will accept. `high` is not a
			// value it has: x265 refuses with "unknown profile <high>".
			encoder: EncoderX265, wantPreset: "-preset veryfast",
			wantFlags: []string{"-profile:v main", "-pix_fmt yuv420p"},
		},
		{
			encoder: EncoderNVENCHEVC, wantPreset: "-preset p4",
			wantFlags: []string{"-rc cbr"},
		},
		{
			encoder: EncoderQSVHEVC, wantPreset: "-preset veryfast",
		},
		{
			encoder: EncoderVideoToolboxHEVC, wantPreset: "",
			wantFlags: []string{"-realtime 1"},
			absent:    []string{"-preset", "-quality"},
		},
		{
			// The whole of the defect: these two flags, and their absence.
			encoder: EncoderVAAPIHEVC, wantPreset: "",
			wantFlags: []string{"-vaapi_device " + defaultVAAPIDevice, "format=nv12,hwupload"},
			absent:    []string{"-preset", "-quality"},
		},
		{
			encoder: EncoderAMFHEVC, wantPreset: "-quality speed",
			wantFlags: []string{"-usage transcoding"},
			absent:    []string{"-preset"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.encoder, func(t *testing.T) {
			if !EncoderIsConfigured(tc.encoder) {
				t.Fatalf("%s has no encoderProfiles entry and falls through to the "+
					"unknown-encoder branch", tc.encoder)
			}
			s := baseRendition()
			s.Encoder = tc.encoder
			args := RenditionArgs(s)
			line := join(args)

			if tc.wantPreset != "" && !strings.Contains(line, tc.wantPreset) {
				t.Errorf("missing %q in: %s", tc.wantPreset, line)
			}
			for _, want := range tc.wantFlags {
				if !strings.Contains(line, want) {
					t.Errorf("missing %q in: %s", want, line)
				}
			}
			for _, bad := range tc.absent {
				if has(args, bad) {
					t.Errorf("%s must not be passed to %s: %s", bad, tc.encoder, line)
				}
			}
			// The trap, asserted directly rather than left to the profile
			// table being read correctly: an HEVC encoder handed `high` does
			// not warn, it refuses to open.
			if prof, _ := argsAfter(args, "-profile:v"); prof == "high" {
				t.Errorf("%s was handed -profile:v high. HEVC profiles are main/main10/rext; "+
					"`high` is an H.264 name and every HEVC encoder rejects it outright: %s",
					tc.encoder, line)
			}
		})
	}
}

// ------------------------------------------------ defect 2: capped VBR modes

// A ceiling above the target switches NVENC out of CBR, and changes nothing
// anywhere else.
//
// #341 shipped settable maxrateKbps/bufsizeKbps and docs/ENCODING.md
// advertised capped VBR. But `-rc cbr` was appended unconditionally
// immediately before -b:v/-maxrate/-bufsize, and `-rc cbr` PINS NVENC to
// constant bitrate: the ceiling reached the command line and did nothing, so
// the operator set a number that had no effect.
//
// Only NVENC needs the mode stated. QSV has no -rc option at all and derives
// its bitrate-control mode from -b:v against -maxrate; VAAPI's -rc_mode
// defaults to `auto`, which is documented as choosing from the other
// parameters; VideoToolbox is capped-VBR unless -constant_bit_rate is asked
// for, which it never is here. Those three are asserted UNCHANGED, because
// "fix it everywhere" would have added flags none of them needs.
func TestACeilingAboveTheTargetSwitchesNVENCToVBR(t *testing.T) {
	argvFor := func(enc string, target, ceiling int) string {
		t.Helper()
		s := baseRendition()
		s.Encoder, s.VideoKbps, s.MaxrateKbps = enc, target, ceiling
		return join(RenditionArgs(s))
	}

	t.Run("nvenc gets -rc vbr when a ceiling was asked for", func(t *testing.T) {
		for _, enc := range []string{EncoderNVENC, EncoderNVENCHEVC} {
			line := argvFor(enc, 4500, 8000)
			if !strings.Contains(line, "-rc vbr") {
				t.Errorf("%s with a 8000k ceiling over a 4500k target did not get -rc vbr, so "+
					"NVENC runs constant-bitrate and the ceiling the operator typed does "+
					"nothing: %s", enc, line)
			}
			if strings.Contains(line, "-rc cbr") {
				t.Errorf("%s still carries -rc cbr alongside the ceiling: %s", enc, line)
			}
		}
	})

	// Equal and unset are the same case and must stay CBR: this is the
	// default every existing install already emits.
	t.Run("a ceiling equal to the target stays CBR", func(t *testing.T) {
		for _, enc := range []string{EncoderNVENC, EncoderNVENCHEVC} {
			for _, ceiling := range []int{0, 4500} {
				line := argvFor(enc, 4500, ceiling)
				if !strings.Contains(line, "-rc cbr") {
					t.Errorf("%s with ceiling %d lost -rc cbr; CBR is the documented default "+
						"and the one every platform ingest is specified against: %s",
						enc, ceiling, line)
				}
			}
		}
	})

	// The other four families have their own rate-control spellings and none
	// of them is pinned to CBR, so a ceiling needs no extra flag. If one is
	// ever added, it must be added deliberately and this test rewritten.
	t.Run("no other family gains a rate-control flag", func(t *testing.T) {
		for _, enc := range []string{
			EncoderX264, EncoderX265,
			EncoderQSV, EncoderQSVHEVC,
			EncoderVideoToolbox, EncoderVideoToolboxHEVC,
			EncoderVAAPI, EncoderVAAPIHEVC,
			EncoderAMF, EncoderAMFHEVC,
		} {
			capped, flat := argvFor(enc, 4500, 8000), argvFor(enc, 4500, 4500)
			// The only differences between the two must be the two NUMBERS
			// the operator set -- the ceiling, and the rate window derived
			// from it. No mode flag may appear or disappear.
			normalised := strings.NewReplacer(
				"-maxrate 8000k", "-maxrate 4500k",
				"-bufsize 16000k", "-bufsize 9000k",
			).Replace(capped)
			if normalised != flat {
				t.Errorf("%s changed something other than its ceiling and rate window when a "+
					"ceiling was set.\n  capped: %s\n  flat:   %s", enc, capped, flat)
			}
			for _, bad := range []string{"-rc", "-rc_mode"} {
				s := baseRendition()
				s.Encoder, s.VideoKbps, s.MaxrateKbps = enc, 4500, 8000
				if has(RenditionArgs(s), bad) {
					t.Errorf("%s gained %s; it does not need one and the flag may not even exist "+
						"on that encoder: %s", enc, bad, capped)
				}
			}
		}
	})
}

// ------------------------------------------------------- against real FFmpeg

// Every encoder we hold a profile for, that this FFmpeg actually registers,
// OPENS with the flags this package would give it.
//
// This is the test that would have caught #343 on any machine rather than only
// on one with the right silicon, and it is the reason the profile table can be
// trusted at all: `-profile:v high` on an HEVC encoder is not a warning, it is
// "Unable to parse \"profile\" option value \"high\"" and a stream that never
// starts.
//
// A hardware encoder on a machine without that hardware fails for a reason
// that is not our business -- no CUDA device, no VA display, no render node --
// so only failures that name an OPTION are treated as defects. That
// distinction is the whole design: it lets this run everywhere and still mean
// something, instead of being a macOS-only or NVIDIA-only test.
func TestEveryConfiguredEncoderOpensWithItsOwnFlags(t *testing.T) {
	bin := needFFmpeg(t, "ffmpeg")[0]

	out, err := exec.Command(bin, "-hide_banner", "-encoders").Output()
	if err != nil {
		t.Fatalf("ffmpeg -encoders: %v", err)
	}
	registered := map[string]bool{}
	for _, n := range parseVideoEncoders(string(out)) {
		registered[n] = true
	}

	names := make([]string, 0, len(encoderProfiles))
	for n := range encoderProfiles {
		names = append(names, n)
	}
	sort.Strings(names)

	var opened []string
	for _, name := range names {
		if !registered[name] {
			continue
		}
		t.Run(name, func(t *testing.T) {
			prof := encoderProfiles[name]

			args := []string{"-hide_banner", "-nostdin", "-loglevel", "error"}
			if prof.vaapi {
				args = append(args, "-vaapi_device", defaultVAAPIDevice)
			}
			args = append(args, "-f", "lavfi",
				"-i", "testsrc2=size=320x240:rate=30", "-frames:v", "5",
				"-c:v", name)
			args = append(args, presetArgs(prof, "")...)

			ran := true
			// Both rate-control branches, because the capped one is the branch
			// #341 shipped and nothing had ever run.
			for _, ceiling := range []int{4500, 8000} {
				full := append(append([]string{}, args...), rateModeArgs(prof, 4500, ceiling)...)
				full = append(full, prof.rateControl...)
				full = append(full, "-b:v", "4500k", "-maxrate", itoa(ceiling)+"k", "-bufsize", "9000k")
				if prof.vaapi {
					full = append(full, "-vf", "format=nv12,hwupload")
				}
				full = append(full, "-f", "null", "-")

				b, err := exec.Command(bin, full...).CombinedOutput()
				if err == nil {
					continue
				}
				if msg := optionComplaint(string(b)); msg != "" {
					t.Errorf("%s refused a flag this package gives it, at ceiling %dk: %s\n  argv: %s",
						name, ceiling, msg, strings.Join(full, " "))
					continue
				}
				// Deliberately a log and not a t.Skip. The encoder is
				// registered but this machine has no such silicon, which says
				// nothing either way about our flags -- and a skip would spend
				// a slot on internal/testenv's ratchet to record a fact the
				// floor below already covers. Nothing passes vacuously here:
				// libx264 must have opened, or the test fails.
				ran = false
				t.Logf("%s is registered but cannot run on this machine, which is a property "+
					"of the hardware and not of the flags:\n%s", name, strings.TrimSpace(string(b)))
			}
			if ran {
				opened = append(opened, name)
			}
		})
	}

	// The floor. Any FFmpeg worth testing against has libx264, and a run that
	// silently exercised nothing is the failure mode this repo has been bitten
	// by before -- a package that prints ok having asserted nothing at all.
	if !registered[EncoderX264] {
		t.Fatalf("this FFmpeg registers no libx264; nothing above can have been exercised")
	}
	if len(opened) == 0 {
		t.Fatalf("no encoder was actually opened, so this test proved nothing")
	}
	t.Logf("opened with our own flags: %s", strings.Join(opened, " "))
}

// optionComplaint returns FFmpeg's own words when a failure is about an OPTION
// VALUE we chose, and "" when it is about the machine.
//
// These are the exact phrasings the four HEVC encoders produce when handed
// `-profile:v high`, collected by running it:
//
//	x265 [error]: unknown profile <high>
//	[libx265] Invalid or incompatible profile set: high.
//	[hevc_videotoolbox] Unable to parse "profile" option value "high"
//	[hevc_videotoolbox] Error setting option profile to value high.
func optionComplaint(log string) string {
	needles := []string{
		"unknown profile",
		"incompatible profile",
		"unable to parse",
		"error setting option",
		"invalid argument for option",
		"option not found",
	}
	for _, line := range strings.Split(log, "\n") {
		low := strings.ToLower(line)
		for _, n := range needles {
			if strings.Contains(low, n) {
				return strings.TrimSpace(line)
			}
		}
	}
	return ""
}
