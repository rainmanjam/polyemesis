package ffmpeg

// This file carries no build constraint, unlike its gpu_linux/darwin/windows
// siblings, because it holds the shape they all return. Duplicating GPUInfo
// once per GOOS would make the four copies free to drift, and a JSON field
// that exists only on Linux is a UI bug waiting to happen. The per-platform
// files register their detector here in an init(); the fallback below is what
// answers on any GOOS that registers nothing, so an unusual target still
// builds and still starts.

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"
)

// GPUVendor names the silicon behind an encoder so the UI can say "Intel" or
// "NVIDIA" instead of "hardware", and so a support reply can be specific.
type GPUVendor string

const (
	VendorUnknown GPUVendor = "unknown"
	VendorIntel   GPUVendor = "intel"
	VendorNVIDIA  GPUVendor = "nvidia"
	VendorAMD     GPUVendor = "amd"
	VendorApple   GPUVendor = "apple"
)

// PCI vendor IDs, spelled the way /sys/class/drm/*/device/vendor spells them.
// These are the only three that matter to us: they are the vendors whose
// encoders FFmpeg wraps.
const (
	pciVendorIntel  = "0x8086"
	pciVendorNVIDIA = "0x10de"
	// 0x1002 is ATI's original ID; every AMD GPU, Radeon or integrated, still
	// reports it.
	pciVendorAMD = "0x1002"
)

// vendorFromID maps a PCI vendor ID to a name. An ID we do not recognise is
// reported as unknown rather than dropped: a device we cannot name is still a
// device the user can be told about.
func vendorFromID(id string) GPUVendor {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case pciVendorIntel:
		return VendorIntel
	case pciVendorNVIDIA:
		return VendorNVIDIA
	case pciVendorAMD:
		return VendorAMD
	default:
		return VendorUnknown
	}
}

// GPUDevice is one node the encoders could use — on Linux a /dev/dri entry.
//
// Usable is the field that matters. A node that exists but cannot be opened is
// the single most common hardware-encoding failure there is, and it is
// indistinguishable from a working one until something tries to open it.
type GPUDevice struct {
	// Path is what would be passed to -vaapi_device or -qsv_device.
	Path string `json:"path"`
	// Node is the bare name, e.g. "renderD128", which is also its key in sysfs.
	Node string `json:"node"`
	// Render distinguishes a render node from a card (modesetting) node. Only
	// render nodes are usable by an unprivileged encoder.
	Render bool `json:"render"`

	Vendor   GPUVendor `json:"vendor"`
	VendorID string    `json:"vendorId,omitempty"`

	// Usable records whether this process could actually open the node.
	Usable bool `json:"usable"`
	// Problem is the operator-facing reason Usable is false, phrased as the fix.
	Problem string `json:"problem,omitempty"`
}

// GPUInfo is what the machine has, as opposed to what the FFmpeg build lists.
//
// It is deliberately advisory. Nothing here may prevent a start: the encode
// probe decides what works, and software x264 always works. What this adds is
// the WHY behind a probe failure — "the render node is there but your user
// cannot open it" turns a support thread into a one-line fix.
type GPUInfo struct {
	Platform string      `json:"platform"`
	Devices  []GPUDevice `json:"devices,omitempty"`
	// Vendors is every vendor seen, deduped, in discovery order.
	Vendors []GPUVendor `json:"vendors,omitempty"`

	// VAAPIDevice is the node RenditionSpec.VAAPIDevice should be set to.
	// Empty means VAAPI has no device to use and should not be offered.
	VAAPIDevice string `json:"vaapiDevice,omitempty"`

	// NVIDIA is true when the NVIDIA kernel stack is present, which is what
	// NVENC needs. It is deliberately not derived from nvidia-smi alone:
	// plenty of containers encode happily without that binary installed.
	NVIDIA bool `json:"nvidia"`
	// NVIDIADriver is the driver version when we could learn it cheaply.
	NVIDIADriver string `json:"nvidiaDriver,omitempty"`

	// AppleSilicon distinguishes the VideoToolbox generations on macOS.
	AppleSilicon bool `json:"appleSilicon,omitempty"`

	// Notes are operator-facing diagnostics, already phrased as instructions.
	Notes []string `json:"notes,omitempty"`
}

// HasVendor reports whether any discovered device came from this vendor.
func (g GPUInfo) HasVendor(v GPUVendor) bool {
	for _, got := range g.Vendors {
		if got == v {
			return true
		}
	}
	return false
}

// Usable returns the devices this process could actually open.
func (g GPUInfo) Usable() []GPUDevice {
	var out []GPUDevice
	for _, d := range g.Devices {
		if d.Usable {
			out = append(out, d)
		}
	}
	return out
}

// Summary is the one line worth writing to the log at startup.
func (g GPUInfo) Summary() string {
	var parts []string
	for _, v := range g.Vendors {
		parts = append(parts, string(v))
	}
	if g.NVIDIA && !g.HasVendor(VendorNVIDIA) {
		parts = append(parts, string(VendorNVIDIA))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%s: no GPU detected", g.Platform)
	}
	s := fmt.Sprintf("%s: %s", g.Platform, strings.Join(parts, ", "))
	if g.VAAPIDevice != "" {
		s += fmt.Sprintf(" (vaapi %s)", g.VAAPIDevice)
	}
	return s
}

// addNote appends a diagnostic, ignoring repeats so a machine with four render
// nodes does not report the same missing group four times.
func (g *GPUInfo) addNote(note string) {
	if note == "" {
		return
	}
	for _, have := range g.Notes {
		if have == note {
			return
		}
	}
	g.Notes = append(g.Notes, note)
}

// addVendor records a vendor once, preserving discovery order so the primary
// card (renderD128 before renderD129) is named first.
func (g *GPUInfo) addVendor(v GPUVendor) {
	if v == "" || v == VendorUnknown {
		return
	}
	if g.HasVendor(v) {
		return
	}
	g.Vendors = append(g.Vendors, v)
}

// gpuDetectTimeout bounds the entire scan. It is a var so the tests can shrink
// it, and it exists because open() on a render node backed by a wedged driver
// blocks in the kernel and is not interruptible — detection must give up and
// let the product start on software encoding rather than hang a launch.
var gpuDetectTimeout = 3 * time.Second

// platformDetect is installed by whichever gpu_<goos>.go was compiled in.
// The default answers for every other target.
var platformDetect = detectNoDevices

// DetectGPUs reports what hardware is present, without deciding what works —
// that is the encode probe's job.
//
// It never returns an error and never blocks a start. The scan runs in a
// goroutine so an uninterruptible open() on a broken driver costs a timeout
// instead of the whole launch; in that (pathological) case the goroutine is
// left to finish on its own, which is the cheaper of the two leaks.
func DetectGPUs(ctx context.Context) GPUInfo {
	ctx, cancel := context.WithTimeout(ctx, gpuDetectTimeout)
	defer cancel()

	// Buffered: nothing must block on a receiver that has already given up.
	ch := make(chan GPUInfo, 1)
	go func() { ch <- platformDetect(ctx) }()

	select {
	case info := <-ch:
		return info
	case <-ctx.Done():
		info := GPUInfo{Platform: runtime.GOOS}
		info.addNote("GPU detection timed out; continuing without it. " +
			"A device node that will not open usually means a hung or half-installed GPU driver.")
		return info
	}
}

// detectNoDevices is the answer on a platform we have no enumeration for. It
// reports nothing rather than guessing, because a wrong "you have a GPU" is
// worse than an honest silence: the probe still finds any encoder that works.
func detectNoDevices(context.Context) GPUInfo {
	info := GPUInfo{Platform: runtime.GOOS}
	info.addNote(fmt.Sprintf("no GPU enumeration on %s; hardware encoders are offered only if a test encode succeeds", runtime.GOOS))
	return info
}
