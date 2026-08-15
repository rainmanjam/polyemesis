package db

import (
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// Every encoder the UI offers must produce a command line that can actually
// start.
//
// This is the meta-test for #343, and it is driven off KnownEncoders rather
// than off a list of its own so that adding a thirteenth encoder to that slice
// and nothing else is a FAILURE here rather than a rendition an operator can
// save and never run.
//
// The defect it exists to stop coming back: KnownEncoders offered twelve
// encoders and ffmpeg.encoderProfiles configured six. The other six fell
// through to "pass only the flags every encoder understands", which for
// hevc_vaapi meant no -vaapi_device and no format=nv12,hwupload — and VAAPI
// encodes from GPU surfaces, so it could not open on any machine. Nothing
// caught it downstream either: only the H.264 half of each family is
// test-encoded, so hevc_vaapi was never greyed out, and the start gate in
// internal/engine refuses on MEASURED failures only.
//
// The floors are explicit on purpose. An enumerator-driven test whose
// enumerator is accidentally emptied passes by iterating nothing, which is the
// same "test that cannot fail" this repo has been bitten by before.
func TestEveryEncoderTheUIOffersCanActuallyStart(t *testing.T) {
	if len(KnownEncoders) < 12 {
		t.Fatalf("KnownEncoders has %d entries; it had 12 when this test was written. "+
			"An encoder was removed, or the enumerator this test rides on is empty and "+
			"every assertion below would vacuously pass", len(KnownEncoders))
	}

	// A floor per family as well as a total, because the total alone is
	// satisfied by twelve copies of libx264.
	families := map[string]int{}

	for _, e := range KnownEncoders {
		// Counted out here rather than inside the subtest, so that running one
		// case with -run does not read as "the sweep covered one family".
		families[familyOf(string(e))]++

		t.Run(string(e), func(t *testing.T) {
			name := string(e)

			// 1. It is configured at all. Everything below is a consequence of
			//    this, but stated separately because the message is the one a
			//    reader needs: add an entry to ffmpeg.encoderProfiles.
			if !ffmpeg.EncoderIsConfigured(name) {
				t.Fatalf("%s is offered by KnownEncoders and has no entry in ffmpeg.encoderProfiles, "+
					"so it takes the unknown-encoder branch and gets only the flags every encoder "+
					"understands. For a VAAPI encoder that is a stream that cannot start; for the "+
					"rest it is a stream missing the pixel-format and profile pinning that exists "+
					"to keep a platform from refusing it", name)
			}

			args := ffmpeg.RenditionArgs(ffmpeg.RenditionSpec{
				InRelayURL:  "udp://127.0.0.1:5000",
				OutRelayURL: "udp://127.0.0.1:5001",
				Width:       1280,
				Height:      720,
				VideoKbps:   4500,
				Encoder:     name,
			})
			line := strings.Join(args, " ")

			// 2. VAAPI needs a device before -i and an upload at the tail of
			//    the filter chain. This is the half hevc_vaapi was missing.
			if strings.HasSuffix(name, "_vaapi") {
				dev, in := indexOfArg(args, "-vaapi_device"), indexOfArg(args, "-i")
				if dev < 0 {
					t.Errorf("%s got no -vaapi_device: VAAPI encodes from GPU surfaces and "+
						"cannot open without one\n  %s", name, line)
				} else if in >= 0 && dev > in {
					t.Errorf("%s put -vaapi_device after -i; the device has to exist before the "+
						"filter graph that uploads into it is configured\n  %s", name, line)
				}
				if !strings.Contains(line, "format=nv12,hwupload") {
					t.Errorf("%s got no format=nv12,hwupload: the frames stay in system memory "+
						"and the encoder has nothing it can read\n  %s", name, line)
				}
			}

			// 3. THE TRAP. -profile:v high is H.264-only. libx265,
			//    hevc_videotoolbox and every other HEVC encoder REFUSE it —
			//    "unknown profile <high>" — rather than ignoring it, so
			//    copying an H.264 row across to its HEVC sibling produces a
			//    rendition that will not start.
			if e.Codec() == "hevc" {
				if prof := argAfterFlag(args, "-profile:v"); prof != "" && !hevcProfiles[prof] {
					t.Errorf("%s is an HEVC encoder and was handed -profile:v %q. HEVC profiles are "+
						"main/main10/rext; %q is an H.264 profile name and the encoder refuses to "+
						"open with it\n  %s", name, prof, prof, line)
				}
			}

			// 4. The line the whole product rests on, asserted for every
			//    encoder because an encoder profile is exactly the sort of
			//    change that could reach for -c:a.
			if c := argAfterFlag(args, "-c:a"); c != "copy" {
				t.Errorf("%s produced -c:a %q; a rendition re-encodes video ONLY", name, c)
			}
			if v := argAfterFlag(args, "-c:v"); v != name {
				t.Errorf("-c:v = %q, want %q", v, name)
			}
		})
	}

	// Both codecs of all five hardware families plus software, or the sweep
	// above is narrower than the list it claims to cover.
	for _, want := range []string{"nvenc", "qsv", "videotoolbox", "vaapi", "amf", "software"} {
		if families[want] < 2 {
			t.Errorf("only %d encoder(s) of the %s family were exercised, want both the H.264 "+
				"and the HEVC one", families[want], want)
		}
	}
}

// hevcProfiles is every value the HEVC encoders in KnownEncoders accept for
// -profile:v, read out of `ffmpeg -h encoder=<name>` on real binaries:
// libx265, hevc_nvenc, hevc_qsv, hevc_vaapi and hevc_videotoolbox between them
// offer main, main10, rext and a few vendor extensions. What matters is that
// "high" and the other H.264 names are not among them.
var hevcProfiles = map[string]bool{
	"main": true, "main10": true, "rext": true,
	"mainsp": true, "scc": true, "mv": true, "main42210": true,
}

func familyOf(name string) string {
	for _, f := range []string{"nvenc", "qsv", "videotoolbox", "vaapi", "amf"} {
		if strings.HasSuffix(name, "_"+f) {
			return f
		}
	}
	return "software"
}

func indexOfArg(args []string, flag string) int {
	for i, a := range args {
		if a == flag {
			return i
		}
	}
	return -1
}

func argAfterFlag(args []string, flag string) string {
	if i := indexOfArg(args, flag); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}
