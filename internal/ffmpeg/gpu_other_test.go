package ffmpeg

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestVendorFromIDNamesTheVendorsWeWrapEncodersFor(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want GPUVendor
	}{
		{"intel", "0x8086", VendorIntel},
		{"nvidia", "0x10de", VendorNVIDIA},
		{"amd", "0x1002", VendorAMD},
		// sysfs writes lowercase, but a hand-edited fixture or a different
		// kernel formatting must not silently turn a known card into unknown.
		{"uppercase hex still resolves", "0X8086", VendorIntel},
		{"trailing newline from sysfs is ignored", "0x10de\n", VendorNVIDIA},
		{"unknown vendor is named unknown, not dropped", "0x1af4", VendorUnknown},
		{"missing vendor file yields unknown", "", VendorUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := vendorFromID(tc.id); got != tc.want {
				t.Errorf("vendorFromID(%q) = %q, want %q", tc.id, got, tc.want)
			}
		})
	}
}

func TestAddNoteDropsRepeatsSoOneFixIsReportedOnce(t *testing.T) {
	var info GPUInfo
	info.addNote("add the user to the render group")
	info.addNote("add the user to the render group")
	info.addNote("")
	info.addNote("second problem")

	want := []string{"add the user to the render group", "second problem"}
	if len(info.Notes) != len(want) {
		t.Fatalf("Notes = %q, want %q", info.Notes, want)
	}
	for i := range want {
		if info.Notes[i] != want[i] {
			t.Errorf("Notes[%d] = %q, want %q", i, info.Notes[i], want[i])
		}
	}
}

func TestAddVendorKeepsDiscoveryOrderSoThePrimaryCardIsNamedFirst(t *testing.T) {
	var info GPUInfo
	info.addVendor(VendorAMD)
	info.addVendor(VendorIntel)
	info.addVendor(VendorAMD)
	info.addVendor(VendorUnknown)
	info.addVendor("")

	want := []GPUVendor{VendorAMD, VendorIntel}
	if len(info.Vendors) != len(want) {
		t.Fatalf("Vendors = %v, want %v", info.Vendors, want)
	}
	for i := range want {
		if info.Vendors[i] != want[i] {
			t.Errorf("Vendors[%d] = %q, want %q", i, info.Vendors[i], want[i])
		}
	}
	if !info.HasVendor(VendorIntel) || info.HasVendor(VendorNVIDIA) {
		t.Errorf("HasVendor disagrees with Vendors = %v", info.Vendors)
	}
}

func TestUsableReturnsOnlyDevicesThisProcessCanOpen(t *testing.T) {
	info := GPUInfo{Devices: []GPUDevice{
		{Path: "/dev/dri/card0", Usable: false, Problem: "permission denied"},
		{Path: "/dev/dri/renderD128", Usable: true},
	}}
	usable := info.Usable()
	if len(usable) != 1 || usable[0].Path != "/dev/dri/renderD128" {
		t.Fatalf("Usable() = %+v, want only renderD128", usable)
	}
}

func TestSummaryNamesTheHardwareOrSaysThereIsNone(t *testing.T) {
	tests := []struct {
		name string
		info GPUInfo
		want string
	}{
		{
			name: "nothing found",
			info: GPUInfo{Platform: "linux"},
			want: "linux: no GPU detected",
		},
		{
			name: "vendor and chosen vaapi node",
			info: GPUInfo{Platform: "linux", Vendors: []GPUVendor{VendorIntel}, VAAPIDevice: "/dev/dri/renderD128"},
			want: "linux: intel (vaapi /dev/dri/renderD128)",
		},
		{
			// NVENC needs no DRM node, so an NVIDIA-only machine has a vendor
			// worth naming even with no device in the list.
			name: "nvidia stack with no drm vendor",
			info: GPUInfo{Platform: "linux", NVIDIA: true},
			want: "linux: nvidia",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.info.Summary(); got != tc.want {
				t.Errorf("Summary() = %q, want %q", got, tc.want)
			}
		})
	}
}

// A hung open() on a broken driver must cost a timeout, never a launch.
func TestDetectGPUsGivesUpRatherThanBlockingAStart(t *testing.T) {
	restoreTimeout, restoreDetect := gpuDetectTimeout, platformDetect
	released, finished := make(chan struct{}), make(chan struct{})
	// One cleanup, in this order: the abandoned detector is still reading the
	// globals it was installed in, so it has to be let go and joined before
	// they are put back.
	t.Cleanup(func() {
		close(released)
		<-finished
		gpuDetectTimeout, platformDetect = restoreTimeout, restoreDetect
	})

	gpuDetectTimeout = 20 * time.Millisecond
	platformDetect = func(context.Context) GPUInfo {
		defer close(finished)
		<-released // stands in for an uninterruptible open()
		return GPUInfo{Platform: "wedged"}
	}

	done := make(chan GPUInfo, 1)
	go func() { done <- DetectGPUs(context.Background()) }()

	select {
	case info := <-done:
		if info.Platform != runtime.GOOS {
			t.Errorf("Platform = %q, want %q", info.Platform, runtime.GOOS)
		}
		if len(info.Notes) == 0 || !strings.Contains(info.Notes[0], "timed out") {
			t.Errorf("Notes = %q, want the timeout explained", info.Notes)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("DetectGPUs blocked on a detector that never returns")
	}
}

func TestDetectGPUsHonoursAnAlreadyCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Non-fatal means non-fatal: a cancelled context yields an empty answer,
	// not a panic and not a hang.
	info := DetectGPUs(ctx)
	if info.Platform != runtime.GOOS {
		t.Errorf("Platform = %q, want %q", info.Platform, runtime.GOOS)
	}
}

func TestDetectNoDevicesReportsSilenceRatherThanGuessing(t *testing.T) {
	info := detectNoDevices(context.Background())
	if info.Platform != runtime.GOOS {
		t.Errorf("Platform = %q, want %q", info.Platform, runtime.GOOS)
	}
	if len(info.Devices) != 0 || info.VAAPIDevice != "" || info.NVIDIA {
		t.Errorf("fallback claimed hardware: %+v", info)
	}
	if len(info.Notes) == 0 {
		t.Error("fallback said nothing about why it found nothing")
	}
}

// The real detector must be safe to call on whatever machine runs the suite.
func TestDetectGPUsOnThisMachineIsNeverFatalAndIsFast(t *testing.T) {
	start := time.Now()
	info := DetectGPUs(context.Background())
	if elapsed := time.Since(start); elapsed > gpuDetectTimeout+time.Second {
		t.Errorf("DetectGPUs took %s, which is startup-visible", elapsed)
	}
	if info.Platform != runtime.GOOS {
		t.Errorf("Platform = %q, want %q", info.Platform, runtime.GOOS)
	}
	for _, d := range info.Devices {
		if !d.Usable && d.Problem == "" {
			t.Errorf("device %s is unusable with no reason given", d.Path)
		}
	}
	// A device we cannot open must never be handed to -vaapi_device.
	for _, d := range info.Devices {
		if d.Path == info.VAAPIDevice && !d.Usable {
			t.Errorf("chose unusable %s as the VAAPI device", d.Path)
		}
	}
}
