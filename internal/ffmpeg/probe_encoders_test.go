package ffmpeg

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The probe's whole job is to disagree with `ffmpeg -encoders`, so the tests
// cannot use this machine's FFmpeg to check it — the result would change with
// the hardware under CI. A fake binary lets every outcome be pinned exactly:
// success, a named driver failure, a silent failure, and a hang.

// fakeFFmpeg writes an executable stand-in for ffmpeg and returns its path.
// The script dispatches on the value of -c:v, which is the only argument the
// probe varies.
func fakeFFmpeg(t *testing.T, cases string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake ffmpeg is a shell script; the probe logic itself is platform-independent")
	}
	path := filepath.Join(t.TempDir(), "ffmpeg")
	writeFakeFFmpeg(t, path, cases)
	return path
}

// writeFakeFFmpeg (re)writes the script at path, so a test can change the
// machine's answer between two probe runs.
func writeFakeFFmpeg(t *testing.T, path, cases string) {
	t.Helper()
	script := "#!/bin/sh\nenc=\"\"\nprev=\"\"\nfor a in \"$@\"; do\n" +
		"  if [ \"$prev\" = \"-c:v\" ]; then enc=\"$a\"; fi\n  prev=\"$a\"\ndone\n" +
		"case \"$enc\" in\n" + cases + "\n*) exit 0 ;;\nesac\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
}

// realisticCases mirrors the four failures that matter, in the words FFmpeg
// actually uses for them.
const realisticCases = `
h264_nvenc)
  echo "[h264_nvenc @ 0x55d1c0a4b900] Cannot load libcuda.so.1" >&2
  echo "[h264_nvenc @ 0x55d1c0a4b900] The minimum required Nvidia driver for nvenc is 471.41 or newer" >&2
  echo "Error initializing output stream 0:0 -- Error while opening encoder for output stream #0:0" >&2
  exit 1 ;;
h264_qsv)
  echo "[h264_qsv @ 0x7f9] Error initializing an internal MFX session: unsupported (-3)" >&2
  echo "Conversion failed!" >&2
  exit 1 ;;
h264_vaapi)
  echo "[AVHWDeviceContext @ 0x60d] Failed to initialise VAAPI connection: -1 (unknown libva error)." >&2
  echo "Device creation failed: -5." >&2
  exit 1 ;;
h264_amf)
  exit 1 ;;
h264_videotoolbox)
  exit 0 ;;
libx264)
  exit 0 ;;`

func capByName(caps []EncoderCapability, name string) (EncoderCapability, bool) {
	for _, c := range caps {
		if c.Name == name {
			return c, true
		}
	}
	return EncoderCapability{}, false
}

func TestProbeEncodersKeepsTheReasonEachEncoderFailedFor(t *testing.T) {
	bin := fakeFFmpeg(t, realisticCases)
	caps := ProbeEncoders(context.Background(), bin, probeCandidates)

	if len(caps) != len(probeCandidates) {
		t.Fatalf("probed %d encoders, want %d", len(caps), len(probeCandidates))
	}
	// Order is the caller's order, so the result is stable run to run.
	for i, name := range probeCandidates {
		if caps[i].Name != name {
			t.Fatalf("result %d is %q, want %q", i, caps[i].Name, name)
		}
	}

	tests := []struct {
		name       string
		encoder    string
		wantWorks  bool
		wantReason string
		wantVendor GPUVendor
	}{
		{
			name:       "a missing CUDA runtime is reported as a missing CUDA runtime",
			encoder:    EncoderNVENC,
			wantReason: "Cannot load libcuda.so.1",
			wantVendor: VendorNVIDIA,
		},
		{
			name:       "a QSV session failure keeps the MFX error code",
			encoder:    EncoderQSV,
			wantReason: "Error initializing an internal MFX session",
			wantVendor: VendorIntel,
		},
		{
			name:       "a VAAPI connection failure is not confused with a missing device",
			encoder:    EncoderVAAPI,
			wantReason: "Failed to initialise VAAPI connection",
			wantVendor: VendorIntel,
		},
		{
			// Nothing on stderr at all: the exit status is the only thing
			// left, and reporting no reason would be worse than reporting it.
			name:       "a silent failure still carries the exit status",
			encoder:    EncoderAMF,
			wantReason: "exit status 1",
			wantVendor: VendorAMD,
		},
		{
			name:       "an encoder that encodes a frame works and has no reason",
			encoder:    EncoderVideoToolbox,
			wantWorks:  true,
			wantVendor: VendorApple,
		},
		{
			name:       "software always works when the binary can run it",
			encoder:    EncoderX264,
			wantWorks:  true,
			wantVendor: VendorSoftware,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := capByName(caps, tc.encoder)
			if !ok {
				t.Fatalf("%s was not probed", tc.encoder)
			}
			if got.Works != tc.wantWorks {
				t.Errorf("Works = %v, want %v (reason %q)", got.Works, tc.wantWorks, got.Reason)
			}
			if got.Vendor != tc.wantVendor {
				t.Errorf("Vendor = %q, want %q", got.Vendor, tc.wantVendor)
			}
			if tc.wantWorks && got.Reason != "" {
				t.Errorf("a working encoder carries reason %q", got.Reason)
			}
			if !tc.wantWorks && !strings.Contains(got.Reason, tc.wantReason) {
				t.Errorf("reason = %q, want it to contain %q", got.Reason, tc.wantReason)
			}
			if got.Duration <= 0 {
				t.Error("probe duration was not recorded")
			}
			if got.DurationMS != got.Duration.Milliseconds() {
				t.Errorf("DurationMS = %d, does not match Duration %s", got.DurationMS, got.Duration)
			}
		})
	}
}

// Every failure above must be distinguishable from every other one. Four
// encoders that all report "encoder failed" would tell the operator nothing.
func TestProbeEncodersReasonsAreDistinctPerFailure(t *testing.T) {
	bin := fakeFFmpeg(t, realisticCases)
	caps := ProbeEncoders(context.Background(), bin, probeCandidates)

	seen := map[string]string{}
	for _, c := range caps {
		if c.Works {
			continue
		}
		if prev, dup := seen[c.Reason]; dup {
			t.Errorf("%s and %s share the reason %q", prev, c.Name, c.Reason)
		}
		seen[c.Reason] = c.Name
	}
	if len(seen) != 4 {
		t.Fatalf("got %d distinct failure reasons, want 4: %v", len(seen), seen)
	}
}

func TestProbeEncodersRunsProbesConcurrently(t *testing.T) {
	const each = 400 * time.Millisecond
	bin := fakeFFmpeg(t, "*) sleep 0.4; exit 0 ;;")

	start := time.Now()
	caps := ProbeEncoders(context.Background(), bin, probeCandidates)
	elapsed := time.Since(start)

	for _, c := range caps {
		if !c.Works {
			t.Fatalf("%s: %s", c.Name, c.Reason)
		}
	}
	// Sequentially this is six probes at 400ms. The ceiling is deliberately
	// loose — the point is the shape of the cost, not a benchmark.
	if ceiling := each * 3; elapsed > ceiling {
		t.Errorf("%d probes took %s, want under %s: they are running sequentially",
			len(probeCandidates), elapsed, ceiling)
	}
}

func TestProbeEncodersTimeoutsAndBudget(t *testing.T) {
	t.Run("one wedged driver does not stop the others being probed", func(t *testing.T) {
		bin := fakeFFmpeg(t, "h264_vaapi) sleep 30 ;;\nlibx264) exit 0 ;;")

		start := time.Now()
		caps := probeEncodersWith(context.Background(), bin,
			[]string{EncoderVAAPI, EncoderX264}, 250*time.Millisecond, 10*time.Second)
		elapsed := time.Since(start)

		hung, _ := capByName(caps, EncoderVAAPI)
		if hung.Works {
			t.Error("a probe that never returned was reported as working")
		}
		if !strings.Contains(hung.Reason, "timed out") {
			t.Errorf("reason = %q, want it to name the timeout", hung.Reason)
		}
		good, _ := capByName(caps, EncoderX264)
		if !good.Works {
			t.Errorf("libx264 = %q, want it unaffected by the hung probe", good.Reason)
		}
		// The wedged probe is killed at the deadline; WaitDelay allows one
		// further second for the pipe, and nothing may wait out the sleep.
		if elapsed > 3*time.Second {
			t.Errorf("probing took %s; the per-probe timeout is not bounding it", elapsed)
		}
	})

	t.Run("the total budget bounds a machine where everything hangs", func(t *testing.T) {
		bin := fakeFFmpeg(t, "*) sleep 30 ;;")

		start := time.Now()
		caps := probeEncodersWith(context.Background(), bin, probeCandidates,
			30*time.Second, 200*time.Millisecond)
		elapsed := time.Since(start)

		if elapsed > 5*time.Second {
			t.Fatalf("probing took %s; the total budget is not bounding it", elapsed)
		}
		for _, c := range caps {
			if c.Works {
				t.Errorf("%s reported working after the budget expired", c.Name)
			}
			// The budget expiring is not the encoder's fault and must not
			// read as one.
			if !strings.Contains(c.Reason, "budget") {
				t.Errorf("%s: reason = %q, want it to blame the budget", c.Name, c.Reason)
			}
		}
	})

	t.Run("an already cancelled context returns without running anything", func(t *testing.T) {
		bin := fakeFFmpeg(t, "*) sleep 30 ;;")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		start := time.Now()
		caps := ProbeEncoders(ctx, bin, probeCandidates)
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Fatalf("cancelled probe took %s", elapsed)
		}
		for _, c := range caps {
			if c.Works {
				t.Errorf("%s reported working under a cancelled context", c.Name)
			}
		}
	})
}

// Detection must never be fatal, so every degenerate input has to produce a
// result rather than a panic or an error.
func TestProbeEncodersIsNeverFatal(t *testing.T) {
	tests := []struct {
		name  string
		bin   string
		names []string
	}{
		{name: "no binary", bin: "", names: probeCandidates},
		{name: "no candidates", bin: "ffmpeg", names: nil},
		{name: "a binary that does not exist", bin: "/nonexistent/ffmpeg", names: []string{EncoderX264}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			caps := ProbeEncoders(context.Background(), tc.bin, tc.names)
			for _, c := range caps {
				if c.Works {
					t.Errorf("%s reported working with binary %q", c.Name, tc.bin)
				}
				if c.Reason == "" {
					t.Errorf("%s failed without a reason", c.Name)
				}
			}
		})
	}
}

func TestProbeFailureReasonPrefersTheSpecificLine(t *testing.T) {
	tests := []struct {
		name   string
		output string
		err    error
		want   string
	}{
		{
			name: "the driver's own line beats FFmpeg's boilerplate",
			output: "[h264_nvenc @ 0x55d1c0a4b900] Cannot load libcuda.so.1\n" +
				"Error initializing output stream 0:0 -- Error while opening encoder\n" +
				"Conversion failed!\n",
			want: "Cannot load libcuda.so.1",
		},
		{
			// The generic lines come first here; the useful one must still win.
			name: "a specific line later in the output still wins",
			output: "Conversion failed!\n" +
				"[h264_nvenc @ 0x7f] No CUDA capable devices found\n",
			want: "No CUDA capable devices found",
		},
		{
			name:   "a permissions problem is reported as a permissions problem",
			output: "[AVHWDeviceContext @ 0x60d] Failed to open /dev/dri/renderD128: Permission denied\n",
			want:   "Failed to open /dev/dri/renderD128: Permission denied",
		},
		{
			// The pointer changes every run; identical failures must compare
			// equal so the UI does not churn.
			name:   "the log context prefix and its address are stripped",
			output: "[h264_vaapi @ 0x123abc] Failed to initialise VAAPI connection: -1\n",
			want:   "Failed to initialise VAAPI connection: -1",
		},
		{
			name:   "boilerplate is used when it is all there is",
			output: "Conversion failed!\n",
			want:   "Conversion failed!",
		},
		{
			name:   "an empty output falls back to the exit status",
			output: "",
			err:    exec.ErrNotFound,
			want:   exec.ErrNotFound.Error(),
		},
		{
			name:   "a failure with neither output nor error still says something",
			output: "   \n\n",
			want:   "encoder failed for an unknown reason",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := probeFailureReason(tc.output, tc.err); got != tc.want {
				t.Errorf("probeFailureReason() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEncoderVendorOfNamesTheSilicon(t *testing.T) {
	tests := []struct {
		encoder string
		want    GPUVendor
	}{
		{EncoderNVENC, VendorNVIDIA},
		{"hevc_nvenc", VendorNVIDIA},
		{EncoderQSV, VendorIntel},
		{EncoderVAAPI, VendorIntel},
		{EncoderAMF, VendorAMD},
		{EncoderVideoToolbox, VendorApple},
		{EncoderX264, VendorSoftware},
		{"libx265", VendorSoftware},
		{"mpeg4", VendorSoftware},
	}
	for _, tc := range tests {
		t.Run(tc.encoder, func(t *testing.T) {
			if got := EncoderVendorOf(tc.encoder); got != tc.want {
				t.Errorf("EncoderVendorOf(%q) = %q, want %q", tc.encoder, got, tc.want)
			}
		})
	}
}

func TestProbeArgsAreAOneFrameEncodeThatWritesNothing(t *testing.T) {
	for _, name := range probeCandidates {
		t.Run(name, func(t *testing.T) {
			args := probeArgs(name)
			line := strings.Join(args, " ")

			for _, want := range []string{
				"-f lavfi", "-i " + probeSource, "-frames:v 1", "-c:v " + name,
			} {
				if !strings.Contains(line, want) {
					t.Errorf("probe args %q are missing %q", line, want)
				}
			}
			// -f null discards the encode: asking whether an encoder opens
			// must never leave a file behind.
			if !strings.HasSuffix(line, "-f null -") {
				t.Errorf("probe args %q do not end in the null muxer", line)
			}
		})
	}
}

// The probe has to carry the same per-encoder flags the real encode does, or
// it measures the flags rather than the hardware.
func TestProbeArgsCarryVAAPIsDeviceAndUpload(t *testing.T) {
	args := probeArgs(EncoderVAAPI)
	line := strings.Join(args, " ")

	if !strings.Contains(line, "-vaapi_device "+defaultVAAPIDevice) {
		t.Errorf("vaapi probe %q has no device", line)
	}
	// The device has to exist before the filter graph that uploads into it.
	devIdx, inIdx := indexOfArg(args, "-vaapi_device"), indexOfArg(args, "-i")
	if devIdx < 0 || devIdx > inIdx {
		t.Errorf("-vaapi_device is at %d and -i at %d; the device must come first", devIdx, inIdx)
	}
	if !strings.Contains(line, "format=nv12,hwupload") {
		t.Errorf("vaapi probe %q has no hwupload filter, so it would fail even where vaapi works", line)
	}

	// And no other encoder may pay for any of it.
	for _, name := range []string{EncoderX264, EncoderNVENC, EncoderQSV, EncoderAMF, EncoderVideoToolbox} {
		other := strings.Join(probeArgs(name), " ")
		for _, bad := range []string{"-vaapi_device", "hwupload", "format=nv12"} {
			if strings.Contains(other, bad) {
				t.Errorf("%s probe carries %q", name, bad)
			}
		}
	}
}

func indexOfArg(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}

// ------------------------------------------------------- caching on Tools

func TestRefreshEncoderCapabilitiesReprobesTheMachine(t *testing.T) {
	bin := fakeFFmpeg(t, realisticCases)
	tools := &Tools{FFmpeg: bin, VideoEncoders: probeCandidates}

	caps := tools.RefreshEncoderCapabilities(context.Background())
	if len(caps) != len(probeCandidates) {
		t.Fatalf("got %d capabilities, want %d", len(caps), len(probeCandidates))
	}
	// Only the encoder that encoded a frame counts as hardware we have.
	if got := tools.HWEncoders; len(got) != 1 || got[0] != EncoderVideoToolbox {
		t.Fatalf("HWEncoders = %v, want only the encoder that passed", got)
	}
	if got := tools.DefaultVideoEncoder(); got != EncoderVideoToolbox {
		t.Errorf("DefaultVideoEncoder() = %q, want the working hardware encoder", got)
	}

	// A driver install, a GPU passed into the container, a reboot: the same
	// machine answers differently and the cached result has to be replaceable.
	writeFakeFFmpeg(t, bin, "h264_nvenc) exit 0 ;;\nlibx264) exit 0 ;;\n*) exit 1 ;;")
	tools.RefreshEncoderCapabilities(context.Background())

	if got := tools.DefaultVideoEncoder(); got != EncoderNVENC {
		t.Errorf("after refresh DefaultVideoEncoder() = %q, want %q", got, EncoderNVENC)
	}
	if works, reason := tools.EncoderWorks(EncoderVideoToolbox); works {
		t.Errorf("videotoolbox still reported working after it stopped (reason %q)", reason)
	}
}

func TestRefreshEncoderCapabilitiesOrdersHardwareByPreference(t *testing.T) {
	// Probe order is nvenc, qsv, videotoolbox, vaapi, amf; the cached list has
	// to come back in the order a caller should try them, not that one.
	bin := fakeFFmpeg(t, "h264_amf) exit 0 ;;\nh264_qsv) exit 0 ;;\nh264_nvenc) exit 0 ;;\n*) exit 1 ;;")
	tools := &Tools{FFmpeg: bin, VideoEncoders: probeCandidates}
	tools.RefreshEncoderCapabilities(context.Background())

	want := []string{EncoderNVENC, EncoderQSV, EncoderAMF}
	got := tools.HWEncoders
	if len(got) != len(want) {
		t.Fatalf("HWEncoders = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("HWEncoders = %v, want %v", got, want)
		}
	}
}

func TestRefreshEncoderCapabilitiesProbesOnlyWhatTheBuildHas(t *testing.T) {
	bin := fakeFFmpeg(t, "*) exit 0 ;;")

	t.Run("candidates are narrowed to the build's encoder list", func(t *testing.T) {
		tools := &Tools{FFmpeg: bin, VideoEncoders: []string{EncoderX264, "libx265", "mpeg4"}}
		caps := tools.RefreshEncoderCapabilities(context.Background())
		if len(caps) != 1 || caps[0].Name != EncoderX264 {
			t.Fatalf("probed %v, want only the candidates this build registers", caps)
		}
	})

	t.Run("an unavailable encoder list probes everything rather than nothing", func(t *testing.T) {
		tools := &Tools{FFmpeg: bin}
		caps := tools.RefreshEncoderCapabilities(context.Background())
		if len(caps) != len(probeCandidates) {
			t.Fatalf("probed %d encoders, want all %d", len(caps), len(probeCandidates))
		}
	})

	t.Run("a machine with no ffmpeg keeps what it had", func(t *testing.T) {
		tools := &Tools{HWEncoders: []string{EncoderNVENC}, VideoEncoders: []string{EncoderNVENC}}
		if caps := tools.RefreshEncoderCapabilities(context.Background()); len(caps) != 0 {
			t.Fatalf("got %v, want no capabilities", caps)
		}
		if len(tools.HWEncoders) != 1 {
			t.Errorf("HWEncoders = %v; a probe that could not run must not erase the build list", tools.HWEncoders)
		}
	})
}

func TestCapabilitiesAccessors(t *testing.T) {
	caps := []EncoderCapability{
		{Name: EncoderNVENC, Vendor: VendorNVIDIA, Reason: "Cannot load libcuda.so.1"},
		{Name: EncoderX264, Vendor: VendorSoftware, Works: true},
	}
	tools := &Tools{EncoderCaps: caps}

	t.Run("Capabilities hands back a copy", func(t *testing.T) {
		got := tools.Capabilities()
		got[0].Works = true
		if c, _ := tools.Capability(EncoderNVENC); c.Works {
			t.Error("mutating the returned slice changed the cached result")
		}
	})

	t.Run("Capability reports whether the encoder was probed at all", func(t *testing.T) {
		if _, ok := tools.Capability(EncoderQSV); ok {
			t.Error("an unprobed encoder was reported as probed")
		}
		c, ok := tools.Capability(EncoderNVENC)
		if !ok || c.Reason == "" {
			t.Errorf("Capability(nvenc) = %+v, %v", c, ok)
		}
	})

	t.Run("EncoderWorks carries the reason through", func(t *testing.T) {
		works, reason := tools.EncoderWorks(EncoderNVENC)
		if works || !strings.Contains(reason, "libcuda") {
			t.Errorf("EncoderWorks(nvenc) = %v, %q", works, reason)
		}
	})

	t.Run("an encoder nobody probed is assumed to work", func(t *testing.T) {
		// Detection that could not run must never be the reason a rendition
		// refuses to start.
		if works, _ := (&Tools{}).EncoderWorks(EncoderQSV); !works {
			t.Error("an unprobed encoder was reported as broken")
		}
	})
}

func TestDefaultVideoEncoderPrefersWorkingHardwareOverX264(t *testing.T) {
	works := func(names ...string) []EncoderCapability {
		var out []EncoderCapability
		for _, n := range probeCandidates {
			c := EncoderCapability{Name: n, Vendor: EncoderVendorOf(n), Reason: "no such device"}
			for _, w := range names {
				if w == n {
					c.Works, c.Reason = true, ""
				}
			}
			out = append(out, c)
		}
		return out
	}

	tests := []struct {
		name  string
		tools *Tools
		want  string
	}{
		{
			// The bug this replaces: a machine with a perfectly good GPU
			// silently software-encoded, and a 4K60 rendition cannot.
			name:  "a working GPU beats x264",
			tools: &Tools{EncoderCaps: works(EncoderNVENC, EncoderX264)},
			want:  EncoderNVENC,
		},
		{
			// The live bug: the build lists nvenc, the machine cannot run it.
			name:  "a listed but dead encoder never wins",
			tools: &Tools{VideoEncoders: probeCandidates, EncoderCaps: works(EncoderX264)},
			want:  EncoderX264,
		},
		{
			name:  "videotoolbox is preferred where it works",
			tools: &Tools{EncoderCaps: works(EncoderVideoToolbox, EncoderNVENC, EncoderX264)},
			want:  EncoderVideoToolbox,
		},
		{
			name:  "nvenc is preferred over qsv, vaapi and amf",
			tools: &Tools{EncoderCaps: works(EncoderAMF, EncoderVAAPI, EncoderQSV, EncoderNVENC)},
			want:  EncoderNVENC,
		},
		{
			name:  "qsv is preferred over vaapi and amf",
			tools: &Tools{EncoderCaps: works(EncoderAMF, EncoderVAAPI, EncoderQSV)},
			want:  EncoderQSV,
		},
		{
			name:  "vaapi is preferred over amf",
			tools: &Tools{EncoderCaps: works(EncoderAMF, EncoderVAAPI)},
			want:  EncoderVAAPI,
		},
		{
			name:  "amf is used when it is the only hardware that works",
			tools: &Tools{EncoderCaps: works(EncoderAMF, EncoderX264)},
			want:  EncoderAMF,
		},
		{
			// Nothing here may be fatal: x264 is the answer and the product
			// works, even when x264 itself failed to probe.
			name:  "every probe failing still names x264",
			tools: &Tools{VideoEncoders: probeCandidates, EncoderCaps: works()},
			want:  EncoderX264,
		},
		{
			// Nothing was demonstrated, so nothing is claimed.
			name:  "an unprobed install keeps the conservative answer",
			tools: &Tools{VideoEncoders: []string{EncoderX264, EncoderNVENC}, HWEncoders: []string{EncoderNVENC}},
			want:  EncoderX264,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.tools.DefaultVideoEncoder(); got != tc.want {
				t.Errorf("DefaultVideoEncoder() = %q, want %q", got, tc.want)
			}
		})
	}
}

// ------------------------------------------------------- the real binary

// requireFFmpeg keeps the suite machine-independent: these tests assert that
// the result is COHERENT, never that a particular encoder works here. Pinning
// nvenc or videotoolbox would make the suite pass or fail on hardware.
func requireFFmpeg(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed; the probe is covered by the fake binary tests")
	}
	return bin
}

func TestProbeEncodersAgainstRealFFmpegIsCoherent(t *testing.T) {
	bin := requireFFmpeg(t)

	start := time.Now()
	caps := ProbeEncoders(context.Background(), bin, probeCandidates)
	t.Logf("%s probed %d encoders in %s", bin, len(caps), time.Since(start))

	if len(caps) != len(probeCandidates) {
		t.Fatalf("got %d capabilities, want %d", len(caps), len(probeCandidates))
	}
	for i, c := range caps {
		// Logged, never asserted: when this test fails on a machine nobody
		// can reach, the table is the first thing worth seeing.
		t.Logf("%-18s vendor=%-8s works=%-5v %s (%s)", c.Name, c.Vendor, c.Works, c.Reason, c.Duration)

		if c.Name != probeCandidates[i] {
			t.Errorf("result %d is %q, want %q", i, c.Name, probeCandidates[i])
		}
		if c.Vendor == "" {
			t.Errorf("%s has no vendor", c.Name)
		}
		if c.Works && c.Reason != "" {
			t.Errorf("%s works but carries reason %q", c.Name, c.Reason)
		}
		if !c.Works && c.Reason == "" {
			t.Errorf("%s failed without saying why, which is the whole point", c.Name)
		}
		if c.Duration <= 0 {
			t.Errorf("%s has no probe duration", c.Name)
		}
	}
}

func TestDetectWithRealFFmpegIsNeverFatalAndPicksACandidate(t *testing.T) {
	requireFFmpeg(t)

	tools, err := Detect(context.Background(), "", "")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(tools.Capabilities()) == 0 {
		t.Fatal("detection ran but cached no capabilities")
	}
	def := tools.DefaultVideoEncoder()
	if !containsString(probeCandidates, def) {
		t.Errorf("DefaultVideoEncoder() = %q, which is not a candidate", def)
	}
	// Whatever it picked has to be something we watched encode a frame, or
	// x264 as the floor.
	if c, ok := tools.Capability(def); ok && !c.Works && def != EncoderX264 {
		t.Errorf("defaulted to %q which failed to probe: %s", def, c.Reason)
	}
	for _, name := range tools.HWEncoders {
		if works, reason := tools.EncoderWorks(name); !works {
			t.Errorf("HWEncoders lists %s, which does not work: %s", name, reason)
		}
	}
}

// A probe that cannot run is not a machine that cannot encode. These pin the
// one failure mode that would turn this whole workstream into a regression:
// reporting "nothing works" when what actually broke was the measurement.
func TestABrokenProbeHarnessWithholdsNothing(t *testing.T) {
	// libx264 drives no silicon, so its failure can only mean the apparatus
	// failed — a binary that is not FFmpeg, a build with no lavfi testsrc2, an
	// environment that will not let it spawn.
	const harnessBroken = `
*)
  echo "Unknown filter 'testsrc2'" >&2
  exit 1 ;;`

	// The same run, except the software floor holds. This is the stock Linux
	// box the workstream exists for, and none of it may be softened.
	const noGPUButFFmpegIsFine = `
libx264)
  exit 0 ;;
h264_nvenc)
  echo "[h264_nvenc @ 0x55d] Cannot load libcuda.so.1" >&2
  exit 1 ;;
*)
  echo "Device creation failed: -22." >&2
  exit 1 ;;`

	tests := []struct {
		name       string
		cases      string
		encoder    string
		wantWorks  bool
		wantNotYet bool   // the reason must read as "we never found out"
		wantReason string // substring the reason must keep
	}{
		{
			name:       "the software encoder failing takes every hardware verdict with it",
			cases:      harnessBroken,
			encoder:    EncoderNVENC,
			wantNotYet: true,
			wantReason: "Unknown filter 'testsrc2'",
		},
		{
			name:       "the software encoder's own failure stops meaning x264 is broken",
			cases:      harnessBroken,
			encoder:    EncoderX264,
			wantNotYet: true,
			wantReason: "Unknown filter 'testsrc2'",
		},
		{
			name:       "a working x264 leaves NVENC's real failure exactly as FFmpeg reported it",
			cases:      noGPUButFFmpegIsFine,
			encoder:    EncoderNVENC,
			wantReason: "Cannot load libcuda.so.1",
		},
		{
			name:      "a working x264 stays working",
			cases:     noGPUButFFmpegIsFine,
			encoder:   EncoderX264,
			wantWorks: true,
		},
		{
			name:       "the other hardware encoders keep their own reasons too",
			cases:      noGPUButFFmpegIsFine,
			encoder:    EncoderVAAPI,
			wantReason: "Device creation failed: -22.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bin := fakeFFmpeg(t, tc.cases)
			caps := ProbeEncoders(context.Background(), bin, probeCandidates)
			c, ok := capByName(caps, tc.encoder)
			if !ok {
				t.Fatalf("%s was not probed", tc.encoder)
			}
			if c.Works != tc.wantWorks {
				t.Errorf("Works = %v, want %v (reason %q)", c.Works, tc.wantWorks, c.Reason)
			}
			if got := strings.HasPrefix(c.Reason, notProbedPrefix); got != tc.wantNotYet {
				t.Errorf("reason %q reads as unprobed=%v, want %v", c.Reason, got, tc.wantNotYet)
			}
			if tc.wantReason != "" && !strings.Contains(c.Reason, tc.wantReason) {
				t.Errorf("reason %q does not carry %q", c.Reason, tc.wantReason)
			}
		})
	}

	t.Run("a broken harness leaves every rendition startable", func(t *testing.T) {
		bin := fakeFFmpeg(t, harnessBroken)
		tools := &Tools{FFmpeg: bin}
		tools.RefreshEncoderCapabilities(context.Background())

		for _, name := range probeCandidates {
			// EncoderWorks is what the engine gates a start on. Every answer
			// here has to be "as far as we know, yes".
			if works, reason := tools.EncoderWorks(name); !works && !strings.HasPrefix(reason, notProbedPrefix) {
				t.Errorf("%s would be refused on a measurement that never happened: %s", name, reason)
			}
		}
		if got := tools.DefaultVideoEncoder(); got != EncoderX264 {
			t.Errorf("DefaultVideoEncoder() = %q, want the software floor when nothing was measured", got)
		}
		if len(tools.HWEncoders) != 0 {
			t.Errorf("HWEncoders = %v, want none offered off an unusable probe", tools.HWEncoders)
		}
	})
}

// The engine and the API both key off this exact prefix. If it is ever renamed
// in one place, the other silently starts refusing renditions on verdicts that
// were never measured — so pin the literal, not the constant.
func TestNotProbedPrefixIsTheStringTheCallersMatch(t *testing.T) {
	if notProbedPrefix != "not probed: " {
		t.Errorf("notProbedPrefix = %q; engine.notProbed and api.notProbed both match %q",
			notProbedPrefix, "not probed:")
	}
}

// The distinction the fail-open rule turns on: FFmpeg ran and disagreed, or
// FFmpeg never started. Only the first is a measurement that can be wrong.
func TestAnFFmpegThatNeverStartsIsNotTreatedAsABrokenProbe(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-ffmpeg")
	caps := ProbeEncoders(context.Background(), missing, probeCandidates)

	for _, c := range caps {
		if c.Works {
			t.Errorf("%s reported working from a binary that does not exist", c.Name)
		}
		// Nothing can encode here, so nothing may be excused. Softening this
		// into "not probed" would have the editor offer every encoder on a
		// machine with no FFmpeg at all.
		if strings.HasPrefix(c.Reason, notProbedPrefix) {
			t.Errorf("%s was excused as unprobed: %q", c.Name, c.Reason)
		}
		if c.Reason == "" {
			t.Errorf("%s failed with no reason at all", c.Name)
		}
	}
}
