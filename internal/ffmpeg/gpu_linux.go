//go:build linux

package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func init() { platformDetect = detectLinuxGPUs }

// linuxRoots is every tree the scan reads. They are fields rather than
// constants so the tests can point the whole scan at a fake /dev and /sys in a
// TempDir — none of this is reachable on a developer's macOS box otherwise.
type linuxRoots struct {
	// dev is /dev: the scan reads dev/dri/* and looks for dev/nvidia*.
	dev string
	// sys is /sys/class/drm, where each node's PCI vendor ID lives.
	sys string
	// nvidiaProc is /proc/driver/nvidia/version, which reports the driver
	// version without running anything.
	nvidiaProc string
}

var systemRoots = linuxRoots{
	dev:        "/dev",
	sys:        "/sys/class/drm",
	nvidiaProc: "/proc/driver/nvidia/version",
}

func detectLinuxGPUs(ctx context.Context) GPUInfo {
	return scanLinuxGPUs(ctx, systemRoots)
}

func scanLinuxGPUs(ctx context.Context, r linuxRoots) GPUInfo {
	info := GPUInfo{Platform: "linux"}
	scanDRM(&info, r)
	info.VAAPIDevice = chooseVAAPIDevice(&info)
	scanNVIDIA(ctx, &info, r)
	return info
}

// ------------------------------------------------------------------ drm nodes

// scanDRM enumerates /dev/dri, which is what VAAPI and QSV need and what a
// container started without `--device /dev/dri` silently lacks. Their absence
// is the single most common reason hardware encoding "does not work", so it is
// reported as an instruction rather than left for the encoder to discover.
func scanDRM(info *GPUInfo, r linuxRoots) {
	driDir := filepath.Join(r.dev, "dri")

	entries, err := os.ReadDir(driDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			info.addNote(missingDRIDirNote(driDir))
		} else {
			info.addNote(fmt.Sprintf("cannot list %s (%v); VAAPI and QSV cannot be used", driDir, err))
		}
		return
	}

	// ReadDir sorts by name, so renderD128 is considered before renderD129 and
	// the primary card wins ties in the VAAPI choice below.
	for _, e := range entries {
		name := e.Name()
		render := strings.HasPrefix(name, "renderD")
		if !render && !strings.HasPrefix(name, "card") {
			continue
		}
		d := GPUDevice{
			Path:   filepath.Join(driDir, name),
			Node:   name,
			Render: render,
		}
		d.VendorID = readDRMVendorID(r.sys, name)
		d.Vendor = vendorFromID(d.VendorID)
		d.Usable, d.Problem = probeDRMNode(d.Path)

		info.Devices = append(info.Devices, d)
		info.addVendor(d.Vendor)
		info.addNote(d.Problem)
	}

	// An empty /dev/dri is the passed-through-nothing case, and it deserves the
	// same instruction as a missing one.
	if len(info.Devices) == 0 {
		info.addNote(missingDRIDirNote(driDir))
	}
}

// missingDRIDirNote names both fixes because the two audiences never overlap:
// whoever is running in Docker has no kernel problem, and whoever is on bare
// metal cannot pass a device through.
func missingDRIDirNote(driDir string) string {
	return fmt.Sprintf("no render nodes under %s, so VAAPI and QSV cannot be used. "+
		"In Docker, pass the GPU through with --device /dev/dri; on bare metal, check that the "+
		"kernel driver (i915 for Intel, amdgpu for AMD) is loaded.", driDir)
}

// readDRMVendorID reads the PCI vendor ID sysfs exports for a DRM node, e.g.
// "0x8086". Missing is normal and not an error: virtio-gpu and some ARM SoC
// drivers have no PCI parent at all.
func readDRMVendorID(sysRoot, node string) string {
	b, err := os.ReadFile(filepath.Join(sysRoot, node, "device", "vendor"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// probeDRMNode answers the question existence cannot: can THIS process open the
// device?
//
// O_RDWR is what libva itself uses, so a read-only success would be a false
// positive. The classic failure is a node owned by group 'render' or 'video'
// with the service user in neither, and the fix is one usermod away — provided
// somebody says so.
func probeDRMNode(path string) (bool, string) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err == nil {
		f.Close()
		return true, ""
	}
	if errors.Is(err, fs.ErrPermission) {
		return false, permissionProblem(path, nodeGroupName(path))
	}
	return false, fmt.Sprintf("found %s but cannot open it (%v)", path, err)
}

func permissionProblem(path, group string) string {
	if group == "" {
		return fmt.Sprintf("found %s but cannot open it (permission denied) — "+
			"the user running polyemesis needs read/write access to that device", path)
	}
	return fmt.Sprintf("found %s but cannot open it (permission denied) — "+
		"add the user running polyemesis to the %q group (usermod -aG %s <user>, then restart), "+
		"or start the container with --group-add %s", path, group, group, group)
}

// nodeGroupName is the group that owns the device, which is the group the user
// has to join. A numeric GID is still returned when the name cannot be resolved
// (containers routinely lack the host's /etc/group), because `usermod -aG 104`
// works just as well as `usermod -aG render`.
func nodeGroupName(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	gid := strconv.FormatUint(uint64(st.Gid), 10)
	if g, err := user.LookupGroupId(gid); err == nil && g.Name != "" {
		return g.Name
	}
	return gid
}

// ----------------------------------------------------------------- vaapi pick

// chooseVAAPIDevice picks the node -vaapi_device should name, instead of
// hardcoding renderD128 — which is wrong on any machine whose first card is a
// display-only GPU, and on any container that was passed renderD129.
//
// Only render nodes are eligible: a card node requires DRM master, so opening
// one succeeds for root and fails for the service user, which is the worst of
// both worlds. Vendor order matters too — an NVIDIA render node opens fine but
// has no VAAPI encode entrypoint unless the nvidia-vaapi-driver shim is
// installed, so it is chosen last rather than never.
func chooseVAAPIDevice(info *GPUInfo) string {
	best, bestRank := "", 0
	for _, d := range info.Devices {
		if rank := vaapiRank(d); rank > bestRank {
			best, bestRank = d.Path, rank
		}
	}
	if best == "" && len(info.Devices) > 0 {
		info.addNote(noVAAPIDeviceNote(info.Devices))
	}
	return best
}

func vaapiRank(d GPUDevice) int {
	if !d.Usable || !d.Render {
		return 0
	}
	switch d.Vendor {
	case VendorIntel, VendorAMD:
		return 3
	case VendorNVIDIA:
		return 1
	default:
		// An unnamed node is more likely a working virtio-gpu or ARM SoC
		// encoder than a mislabelled NVIDIA card.
		return 2
	}
}

func noVAAPIDeviceNote(devs []GPUDevice) string {
	for _, d := range devs {
		if d.Render {
			// The per-device Problem already says why and how to fix it.
			return "no usable render node, so VAAPI will not be offered"
		}
	}
	return "found only card nodes under /dev/dri and no renderD* node, so VAAPI will not be offered. " +
		"A render node appears once a GPU driver that supports one is loaded."
}

// ---------------------------------------------------------------- nvidia stack

var nvrmVersionRe = regexp.MustCompile(`(\d+\.\d+(?:\.\d+)?)`)

// scanNVIDIA reports whether the NVIDIA kernel stack NVENC needs is present.
//
// Presence is decided by /dev/nvidia*, not by nvidia-smi: the CUDA container
// images encode perfectly well without that binary installed, and requiring it
// would report "no NVIDIA" on exactly the machines this feature exists for.
// nvidia-smi is used only as a fallback source of the driver version, and only
// when it is already on PATH — on a machine with no NVIDIA driver it is not,
// so this costs nothing.
func scanNVIDIA(ctx context.Context, info *GPUInfo, r linuxRoots) {
	nodes, _ := filepath.Glob(filepath.Join(r.dev, "nvidia[0-9]*"))
	hasNode := len(nodes) > 0
	if !hasNode {
		if _, err := os.Stat(filepath.Join(r.dev, "nvidiactl")); err == nil {
			hasNode = true
		}
	}

	info.NVIDIADriver = nvidiaProcVersion(r.nvidiaProc)
	if info.NVIDIADriver == "" {
		info.NVIDIADriver = nvidiaSMIVersion(ctx)
	}

	info.NVIDIA = hasNode || info.NVIDIADriver != ""
	if info.NVIDIA {
		info.addVendor(VendorNVIDIA)
	}

	// A card on the PCI bus with no character device means nouveau is driving
	// it. Nouveau exposes no NVENC at all, and the fix is a driver install, not
	// anything the user can change in polyemesis.
	if info.HasVendor(VendorNVIDIA) && !hasNode {
		info.addNote("an NVIDIA GPU is present but /dev/nvidia* is missing, so NVENC cannot be used. " +
			"Install the proprietary NVIDIA driver (the nouveau driver has no encoder), " +
			"and in Docker run the container with --gpus all.")
	}
}

// nvidiaProcVersion reads the driver version straight out of procfs. This is
// preferred over nvidia-smi because it is a file read rather than a process
// spawn, and because it is visible inside containers that have no nvidia-smi.
func nvidiaProcVersion(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	// "NVRM version: NVIDIA UNIX x86_64 Kernel Module  550.54.14  Thu Feb ..."
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.Contains(line, "NVRM") {
			continue
		}
		if m := nvrmVersionRe.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	return ""
}

// nvidiaSMIVersion is the fallback, bounded hard: a wedged nvidia-smi against a
// GPU in a bad state can hang for minutes, and no diagnostic is worth delaying
// a launch.
func nvidiaSMIVersion(ctx context.Context) string {
	bin, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, bin, "--query-gpu=driver_version", "--format=csv,noheader").Output()
	if err != nil {
		return ""
	}
	// One line per GPU; the driver version is the same for all of them.
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(line)
}
