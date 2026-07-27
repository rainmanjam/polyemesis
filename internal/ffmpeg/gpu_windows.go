//go:build windows

package ffmpeg

import "context"

func init() { platformDetect = detectWindowsGPUs }

// detectWindowsGPUs returns an honest empty result.
//
// NVENC, Quick Sync and AMF are reached through the vendor's user-mode driver
// on Windows; there is no device node to pass through and no permission to get
// wrong, so there is nothing here that would help a user and nothing worth the
// cost of a WMI query at startup. Whether an encoder works is decided by the
// test encode, which is authoritative anyway — enumerating adapters could only
// disagree with it.
func detectWindowsGPUs(context.Context) GPUInfo {
	info := GPUInfo{Platform: "windows"}
	info.addNote("Windows selects NVENC, Quick Sync and AMF through the graphics driver, " +
		"so there is no device to configure. If a hardware encoder is missing, update the GPU driver.")
	return info
}
