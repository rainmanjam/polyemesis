package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Encoder capability detection, by encoding.
//
// `ffmpeg -encoders` answers "what was this binary compiled with", which is a
// different question from "what can this machine do". A stock Linux build lists
// h264_nvenc, h264_qsv, h264_vaapi and h264_amf on a box with no GPU at all, and
// the user only discovers the difference when the encoder dies mid-stream with
// "Cannot load libcuda.so.1" — after they have gone live.
//
// The only authoritative test is to encode a frame and look at the exit status.

// VendorSoftware completes the GPUVendor set for encoders that drive no
// silicon at all. It lives here rather than with the GPU vendors because it is
// not something GPU enumeration can ever return — libx264 is the absence of a
// GPU, not a discovery about one.
const VendorSoftware GPUVendor = "software"

// EncoderCapability is one encoder's measured answer: what happened when this
// machine, with these drivers, was asked to encode a frame just now.
type EncoderCapability struct {
	Name string `json:"name"`
	// Vendor is the silicon this encoder drives, which is what makes a reason
	// readable: "no NVENC capable devices found" is expected on an Intel box
	// and a real problem on an NVIDIA one.
	Vendor GPUVendor `json:"vendor"`
	// Works is the exit status of the probe encode, and nothing else.
	Works bool `json:"works"`
	// Reason is what FFmpeg said when it failed, kept verbatim minus the log
	// prefix. "Cannot load libcuda.so.1", "No CUDA capable devices found",
	// "Failed to initialise VAAPI connection" and "Permission denied" are four
	// different problems with four different fixes, and only the message tells
	// them apart. Empty when Works.
	//
	// Read out of libavcodec, NVIDIA's two are "No CUDA capable devices found"
	// (nothing enumerated) and "No capable devices found" (GPUs present, none
	// with an NVENC block). FFmpeg never prints "No NVENC capable devices
	// found"; searching for that phrase finds nothing.
	Reason string `json:"reason,omitempty"`
	// Duration is how long the probe took. A hardware encoder that takes two
	// seconds to open one frame is usually a driver falling back to software.
	// It is carried twice because time.Duration marshals as nanoseconds, which
	// no UI wants to divide by a million to render.
	Duration   time.Duration `json:"-"`
	DurationMS int64         `json:"durationMs"`

	// ran records whether FFmpeg started at all, which separates "this encoder
	// failed" from "nothing could have succeeded". It is unexported because it
	// is an input to judging the run as a whole, not something a caller should
	// have to reason about; see discardVerdictsIfHarnessIsBroken.
	ran bool
}

// probeSource is the smallest input that still exercises a real encode: one
// frame of a generated pattern, no file, no network, no ingest required.
const probeSource = "testsrc2=size=320x240:rate=1"

// probeTimeout bounds a single probe. Generous enough for a cold CUDA context
// (first NVENC open on an idle GPU is routinely over a second), short enough
// that a wedged VAAPI node — which blocks forever rather than failing — cannot
// hold up startup.
const probeTimeout = 6 * time.Second

// probeBudget bounds all probes together. The probes run concurrently, so the
// budget is a backstop for the case where every candidate hangs at once, not
// the sum of the individual timeouts.
const probeBudget = 10 * time.Second

// probeCandidates is every encoder worth asking about, hardware first. The
// order is preserved in the result so the output is stable across runs.
var probeCandidates = append(append([]string{}, hwEncoders...), EncoderX264)

// ProbeEncoders runs a one-frame encode per name and reports what happened.
//
// It never returns an error. A machine where every probe fails is a machine
// that software-encodes, not a machine that refuses to boot — the SRT check in
// detect.go learned that the hard way.
func ProbeEncoders(ctx context.Context, ffmpegBin string, names []string) []EncoderCapability {
	return probeEncodersWith(ctx, ffmpegBin, names, probeTimeout, probeBudget)
}

// probeEncodersWith is ProbeEncoders with the bounds injected, so tests can
// pin the timeout and budget behaviour without waiting real seconds for it.
func probeEncodersWith(ctx context.Context, ffmpegBin string, names []string, perProbe, budget time.Duration) []EncoderCapability {
	if ffmpegBin == "" || len(names) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	// Resolved once for the whole run, BEFORE the probes fan out: every VAAPI
	// candidate wants the same node, and detection reads the same /dev/dri for
	// each of them. Inside the goroutine it would be one detection per encoder,
	// racing, against a budget the probes themselves need.
	vaapiDevice := probeVAAPIDevice(ctx, names)

	// Sequentially, five probes at a few hundred milliseconds each is a second
	// added to every startup; concurrently it is one probe's latency. They are
	// independent processes touching different devices, so there is nothing to
	// serialise them for.
	out := make([]EncoderCapability, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = probeEncoder(ctx, ffmpegBin, name, vaapiDevice, perProbe)
		}()
	}
	wg.Wait()
	return discardVerdictsIfHarnessIsBroken(out)
}

// notProbedPrefix marks a verdict that demonstrates nothing. Both the engine
// and the API test for it before withholding anything, so a result carrying it
// can never be the reason a rendition is refused or a choice is hidden.
const notProbedPrefix = "not probed: "

// discardVerdictsIfHarnessIsBroken throws away every failure in a run where the
// software encoder also failed.
//
// libx264 needs no driver, no device and no permissions: if FFmpeg ran and
// still could not encode one 320x240 frame with it, what failed was the probe
// and not the hardware — a build with no lavfi testsrc2 to generate from, a
// wrapper that is not really FFmpeg, a sandbox that blocks the filter graph.
// Every other verdict in that run came out of the same broken apparatus and
// none of them is evidence about anything. Reported literally they would take
// away every encoder on the box, including the software one, and refuse
// renditions that encode perfectly well — which is the SRT check that used to
// stop the server booting, wearing a different hat.
//
// An FFmpeg that never started is deliberately not covered. That is not a
// measurement that failed, it is a machine where nothing can encode, and
// "nothing works" is the honest answer rather than a false negative.
//
// Successes are kept. A build with no libx264 at all is a real thing, and an
// encoder that demonstrably encoded a frame demonstrated that whoever else
// failed.
func discardVerdictsIfHarnessIsBroken(caps []EncoderCapability) []EncoderCapability {
	baseline, ok := capabilityIn(caps, EncoderX264)
	if !ok || baseline.Works || !baseline.ran {
		return caps
	}
	why := baseline.Reason
	if strings.HasPrefix(why, notProbedPrefix) {
		// The run was cancelled or ran out of budget, which every entry
		// already says for itself.
		return caps
	}
	for i, c := range caps {
		if c.Works || strings.HasPrefix(c.Reason, notProbedPrefix) {
			continue
		}
		if c.Name == EncoderX264 {
			caps[i].Reason = fmt.Sprintf("%sthe test encode itself could not run here (%s), which says nothing about %s",
				notProbedPrefix, why, c.Name)
			continue
		}
		caps[i].Reason = fmt.Sprintf("%s%s failed the same test encode (%s), so nothing measured here is evidence about %s",
			notProbedPrefix, EncoderX264, why, c.Name)
	}
	return caps
}

func capabilityIn(caps []EncoderCapability, name string) (EncoderCapability, bool) {
	for _, c := range caps {
		if c.Name == name {
			return c, true
		}
	}
	return EncoderCapability{}, false
}

// probeEncoder encodes one frame and reads the outcome off the exit status.
func probeEncoder(ctx context.Context, ffmpegBin, name, vaapiDevice string, perProbe time.Duration) EncoderCapability {
	c := EncoderCapability{Name: name, Vendor: EncoderVendorOf(name)}

	probeCtx, cancel := context.WithTimeout(ctx, perProbe)
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(probeCtx, ffmpegBin, probeArgs(name, vaapiDevice)...)
	// Killing the process is not the same as getting the output pipe back: a
	// child it spawned inherits that pipe and CombinedOutput blocks reading it
	// until every holder is gone. Without a WaitDelay the deadline above is a
	// suggestion.
	cmd.WaitDelay = time.Second
	output, err := cmd.CombinedOutput()

	c.Duration = time.Since(start)
	c.DurationMS = c.Duration.Milliseconds()
	// A non-zero exit means FFmpeg ran and disagreed; an *exec.Error means it
	// never started, and no verdict from that run says anything about anything.
	var exited *exec.ExitError
	c.ran = err == nil || errors.As(err, &exited)

	switch {
	case err == nil:
		c.Works = true
	case errors.Is(ctx.Err(), context.Canceled):
		c.Reason = notProbedPrefix + "detection was cancelled"
	case ctx.Err() != nil:
		// The run as a whole ran out of time, so this probe never got a fair
		// chance. Say that, rather than blaming the encoder for it.
		c.Reason = notProbedPrefix + "overall detection budget expired"
	case errors.Is(probeCtx.Err(), context.DeadlineExceeded):
		c.Reason = fmt.Sprintf("probe timed out after %s (driver did not respond)", perProbe)
	default:
		c.Reason = probeFailureReason(string(output), err)
	}
	return c
}

// anyVAAPI reports whether any candidate needs a render node named, so a host
// with no VAAPI encoder in the list does not pay for GPU detection to answer a
// question nobody asked.
func anyVAAPI(names []string) bool {
	for _, n := range names {
		if encoderProfiles[n].vaapi {
			return true
		}
	}
	return false
}

// probeVAAPIDevice resolves the render node the VAAPI probe should name.
//
// Detection already chooses this node correctly for the real encode
// (chooseVAAPIDevice ranks render nodes by vendor and usability), and the probe
// ignored it, naming renderD128 unconditionally. On a multi-GPU host whose
// first render node is display-only, or on a container passed renderD129, that
// probed a device the encode would never use, failed, and VAAPI was withheld on
// the strength of a test of the wrong hardware.
//
// NOT cached, deliberately. RefreshEncoderCapabilities exists because this
// answer changes under a running server -- a driver package upgrades, a GPU is
// passed through -- so a sync.Once here would pin the pre-GPU answer forever,
// which is this same bug wearing a different hat.
//
// Falls back to the constant rather than returning empty when detection finds
// nothing, because probeArgs must still name SOMETHING; see the note there.
func probeVAAPIDevice(ctx context.Context, names []string) string {
	if !anyVAAPI(names) {
		return ""
	}
	if dev := DetectGPUs(ctx).VAAPIDevice; dev != "" {
		return dev
	}
	return defaultVAAPIDevice
}

// probeArgs builds the one-frame encode for name.
//
// The per-encoder flags mirror rendition.go's profiles, because the flags are
// part of what is being tested: VAAPI without -vaapi_device and the hwupload
// tail fails on every machine including the ones where it works fine, and a
// probe that always says no is worse than no probe at all.
//
// That is also why an empty vaapiDevice falls back to the constant instead of
// dropping the flag: "detection found no render node" would otherwise be
// reported as "VAAPI is broken on this machine", which is a different and
// wrong answer.
func probeArgs(name, vaapiDevice string) []string {
	prof := encoderProfiles[name]

	args := []string{"-hide_banner", "-nostdin", "-loglevel", "warning"}
	if prof.vaapi {
		if vaapiDevice == "" {
			vaapiDevice = defaultVAAPIDevice
		}
		// Must precede -i, same as the real encode.
		args = append(args, "-vaapi_device", vaapiDevice)
	}
	args = append(args,
		"-f", "lavfi",
		"-i", probeSource,
		"-frames:v", "1",
		"-c:v", name,
	)
	if prof.vaapi {
		args = append(args, "-vf", "format=nv12,hwupload")
	}
	// -f null discards the output: this asks whether the encoder opens and
	// produces a frame, and nothing should be written anywhere to find out.
	return append(args, "-f", "null", "-")
}

// EncoderVendorOf maps an encoder name to the silicon it drives.
//
// VAAPI is Intel's API and the Intel path on Linux, but Mesa implements it for
// AMD too, so the label is the likely vendor rather than a claim about what is
// in the machine — DetectGPUs is what actually knows. AMF is unambiguously
// AMD's own.
func EncoderVendorOf(name string) GPUVendor {
	switch {
	case strings.HasSuffix(name, "_nvenc"):
		return VendorNVIDIA
	case strings.HasSuffix(name, "_qsv"), strings.HasSuffix(name, "_vaapi"):
		return VendorIntel
	case strings.HasSuffix(name, "_amf"):
		return VendorAMD
	case strings.HasSuffix(name, "_videotoolbox"):
		return VendorApple
	default:
		return VendorSoftware
	}
}

// logPrefixRe strips FFmpeg's "[h264_nvenc @ 0x55d1c0]" context prefix. The
// pointer changes every run, which would make otherwise identical reasons
// compare unequal and churn in the UI.
var logPrefixRe = regexp.MustCompile(`\[[^\]]*@\s*0x[0-9a-fA-F]+\]\s*`)

// genericFailureLines are the lines FFmpeg prints on the way out of any failed
// encode. They are true and they explain nothing; the useful line is the one
// the encoder printed before them.
var genericFailureLines = []string{
	"conversion failed",
	"error while filtering",
	"error initializing output stream",
	"error opening output file",
	"error opening output files",
	"task finished with error code",
	"terminating thread with return code",
}

// probeFailureReason extracts the most specific thing FFmpeg said.
//
// The first non-generic line wins: encoders report their own failure at the
// point of failure and FFmpeg appends its boilerplate afterwards, so scanning
// forwards finds "Cannot load libcuda.so.1" rather than "Conversion failed!".
func probeFailureReason(output string, err error) string {
	var generic string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(logPrefixRe.ReplaceAllString(line, ""))
		if line == "" {
			continue
		}
		if isGenericFailureLine(line) {
			if generic == "" {
				generic = line
			}
			continue
		}
		return truncate(line, 300)
	}
	if generic != "" {
		return truncate(generic, 300)
	}
	// Some failures are silent — a binary that is not FFmpeg, a killed
	// process, a missing shared library the loader complained about on a
	// stream we did not capture. The exit status is all there is.
	if err != nil {
		return err.Error()
	}
	return "encoder failed for an unknown reason"
}

func isGenericFailureLine(line string) bool {
	lower := strings.ToLower(line)
	for _, g := range genericFailureLines {
		if strings.HasPrefix(lower, g) {
			return true
		}
	}
	return false
}
