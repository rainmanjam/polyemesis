//go:build darwin

package ffmpeg

import (
	"context"
	"runtime"

	"golang.org/x/sys/unix"
)

func init() { platformDetect = detectDarwinGPUs }

// detectDarwinGPUs reports the one thing macOS has to say here: VideoToolbox
// needs no device node, so there is nothing to enumerate and nothing that can
// be missing. Every Mac that runs FFmpeg can encode H.264 in hardware.
//
// The Apple Silicon / Intel split is worth reporting because the two are
// different encoders behind one name — the Apple Silicon media engine sustains
// 4K60 comfortably, the older Intel Quick Sync path on a 2017 Mac does not —
// and it costs nothing to establish.
func detectDarwinGPUs(context.Context) GPUInfo {
	info := GPUInfo{Platform: "darwin", AppleSilicon: isAppleSilicon()}
	if info.AppleSilicon {
		info.addVendor(VendorApple)
		info.addNote("Apple Silicon: h264_videotoolbox uses the media engine and needs no device or driver setup.")
	} else {
		info.addVendor(VendorIntel)
		info.addNote("Intel Mac: h264_videotoolbox uses Quick Sync and needs no device or driver setup. " +
			"4K60 may exceed what the older media engines sustain in realtime.")
	}
	return info
}

// isAppleSilicon asks the kernel rather than trusting GOARCH, because a
// darwin/amd64 build running under Rosetta 2 reports amd64 on an M-series Mac
// and would name the wrong encoder generation. The sysctl is only consulted in
// that ambiguous case; a native arm64 build already knows.
func isAppleSilicon() bool {
	if runtime.GOARCH == "arm64" {
		return true
	}
	translated, err := unix.SysctlUint32("sysctl.proc_translated")
	return err == nil && translated == 1
}
