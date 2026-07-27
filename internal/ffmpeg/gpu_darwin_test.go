package ffmpeg

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

func TestDarwinReportsVideoToolboxWithNoDeviceToConfigure(t *testing.T) {
	info := detectDarwinGPUs(context.Background())

	if info.Platform != "darwin" {
		t.Errorf("Platform = %q, want darwin", info.Platform)
	}
	// VideoToolbox is driver-level: enumerating anything here would only
	// invent a failure mode macOS does not have.
	if len(info.Devices) != 0 {
		t.Errorf("Devices = %+v, want none on macOS", info.Devices)
	}
	if info.VAAPIDevice != "" {
		t.Errorf("VAAPIDevice = %q, want empty: VAAPI does not exist on macOS", info.VAAPIDevice)
	}
	if info.NVIDIA {
		t.Error("NVIDIA = true; the NVIDIA stack has not shipped on macOS since 10.13")
	}
	if len(info.Notes) == 0 || !strings.Contains(strings.ToLower(info.Notes[0]), "videotoolbox") {
		t.Errorf("Notes = %q, want VideoToolbox named as the path", info.Notes)
	}
}

func TestDarwinNamesTheEncoderGeneration(t *testing.T) {
	info := detectDarwinGPUs(context.Background())

	wantVendor, wantSilicon := VendorIntel, false
	if runtime.GOARCH == "arm64" {
		wantVendor, wantSilicon = VendorApple, true
	}
	if info.AppleSilicon != wantSilicon {
		t.Errorf("AppleSilicon = %v on GOARCH=%s, want %v", info.AppleSilicon, runtime.GOARCH, wantSilicon)
	}
	if !info.HasVendor(wantVendor) {
		t.Errorf("Vendors = %v, want %q named", info.Vendors, wantVendor)
	}
}

// isAppleSilicon must answer without a process spawn or a sysctl that can fail
// the caller; anything it cannot determine is reported as Intel, which is the
// conservative half of the split.
func TestIsAppleSiliconAgreesWithTheRunningArchitecture(t *testing.T) {
	got := isAppleSilicon()
	if runtime.GOARCH == "arm64" && !got {
		t.Error("isAppleSilicon() = false on an arm64 build")
	}
	if runtime.GOARCH != "arm64" && runtime.GOARCH != "amd64" && got {
		t.Errorf("isAppleSilicon() = true on GOARCH=%s", runtime.GOARCH)
	}
}
