//go:build linux

package ffmpeg

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// driTree is a fake /dev and /sys. None of this exists on a macOS or Windows
// developer machine, and the interesting cases (a render node owned by a group
// the service user is not in) cannot be reproduced on a real box without
// breaking it, so the whole scan is pointed at a TempDir instead.
type driTree struct {
	t     *testing.T
	root  string
	roots linuxRoots
}

func newDRITree(t *testing.T) *driTree {
	t.Helper()
	root := t.TempDir()
	d := &driTree{
		t:    t,
		root: root,
		roots: linuxRoots{
			dev:        filepath.Join(root, "dev"),
			sys:        filepath.Join(root, "sys", "class", "drm"),
			nvidiaProc: filepath.Join(root, "proc", "driver", "nvidia", "version"),
		},
	}
	d.mkdirAll(filepath.Join(d.roots.dev, "dri"))
	d.mkdirAll(d.roots.sys)
	return d
}

func (d *driTree) mkdirAll(path string) {
	d.t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		d.t.Fatalf("MkdirAll(%s): %v", path, err)
	}
}

// node writes a stand-in for a DRM character device. A regular file is enough:
// what is under test is whether this process may open it O_RDWR, and the kernel
// applies the same mode bits to a regular file as to a device node.
func (d *driTree) node(name, vendorID string, mode os.FileMode) *driTree {
	d.t.Helper()
	path := filepath.Join(d.roots.dev, "dri", name)
	if err := os.WriteFile(path, nil, 0o666); err != nil {
		d.t.Fatalf("write %s: %v", path, err)
	}
	// Explicit chmod, because WriteFile's mode is masked by the umask and the
	// permission cases here depend on the exact bits.
	if err := os.Chmod(path, mode); err != nil {
		d.t.Fatalf("chmod %s: %v", path, err)
	}
	if vendorID != "" {
		dir := filepath.Join(d.roots.sys, name, "device")
		d.mkdirAll(dir)
		if err := os.WriteFile(filepath.Join(dir, "vendor"), []byte(vendorID+"\n"), 0o644); err != nil {
			d.t.Fatalf("write vendor for %s: %v", name, err)
		}
	}
	return d
}

func (d *driTree) devFile(name string) *driTree {
	d.t.Helper()
	if err := os.WriteFile(filepath.Join(d.roots.dev, name), nil, 0o666); err != nil {
		d.t.Fatalf("write %s: %v", name, err)
	}
	return d
}

func (d *driTree) nvidiaProcVersion(content string) *driTree {
	d.t.Helper()
	d.mkdirAll(filepath.Dir(d.roots.nvidiaProc))
	if err := os.WriteFile(d.roots.nvidiaProc, []byte(content), 0o644); err != nil {
		d.t.Fatalf("write nvidia proc version: %v", err)
	}
	return d
}

func (d *driTree) rm(rel string) *driTree {
	d.t.Helper()
	if err := os.RemoveAll(filepath.Join(d.root, rel)); err != nil {
		d.t.Fatalf("remove %s: %v", rel, err)
	}
	return d
}

func (d *driTree) scan() GPUInfo {
	d.t.Helper()
	return scanLinuxGPUs(context.Background(), d.roots)
}

// requireUnprivileged skips cases that depend on file modes actually denying
// access. Root bypasses them, so on a root CI runner these would pass for the
// wrong reason.
func requireUnprivileged(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: file modes do not deny access, so permission handling cannot be exercised")
	}
}

// requireNoNvidiaSMI skips cases that assert NVIDIA is absent. On a real NVIDIA
// host the binary is on PATH and reports a driver, which is correct behaviour
// but not what the fixture is describing.
func requireNoNvidiaSMI(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("nvidia-smi"); err == nil {
		t.Skip("nvidia-smi is on PATH: this host has a real NVIDIA stack")
	}
}

func notesContain(notes []string, want string) bool {
	for _, n := range notes {
		if strings.Contains(n, want) {
			return true
		}
	}
	return false
}

func deviceByNode(info GPUInfo, node string) (GPUDevice, bool) {
	for _, d := range info.Devices {
		if d.Node == node {
			return d, true
		}
	}
	return GPUDevice{}, false
}

func TestScanEnumeratesRenderNodesAndNamesTheCard(t *testing.T) {
	info := newDRITree(t).
		node("card0", "0x8086", 0o660).
		node("renderD128", "0x8086", 0o666).
		scan()

	if len(info.Devices) != 2 {
		t.Fatalf("Devices = %+v, want card0 and renderD128", info.Devices)
	}
	render, ok := deviceByNode(info, "renderD128")
	if !ok {
		t.Fatal("renderD128 missing from Devices")
	}
	if !render.Render {
		t.Error("renderD128 not marked as a render node")
	}
	if render.Vendor != VendorIntel || render.VendorID != "0x8086" {
		t.Errorf("renderD128 vendor = %q/%q, want intel/0x8086", render.Vendor, render.VendorID)
	}
	if !render.Usable || render.Problem != "" {
		t.Errorf("renderD128 usable=%v problem=%q, want usable with no problem", render.Usable, render.Problem)
	}
	if card, _ := deviceByNode(info, "card0"); card.Render {
		t.Error("card0 marked as a render node")
	}
	if !info.HasVendor(VendorIntel) {
		t.Errorf("Vendors = %v, want intel", info.Vendors)
	}
}

func TestNonDRIEntriesInDevDriAreIgnored(t *testing.T) {
	tree := newDRITree(t).node("renderD128", "0x1002", 0o666)
	// by-path/ and stray files live alongside the nodes on many distros.
	tree.mkdirAll(filepath.Join(tree.roots.dev, "dri", "by-path"))

	info := tree.scan()
	if len(info.Devices) != 1 || info.Devices[0].Node != "renderD128" {
		t.Fatalf("Devices = %+v, want only renderD128", info.Devices)
	}
}

func TestUnopenableRenderNodeNamesTheGroupToJoin(t *testing.T) {
	requireUnprivileged(t)

	info := newDRITree(t).node("renderD128", "0x8086", 0o000).scan()

	dev, ok := deviceByNode(info, "renderD128")
	if !ok {
		t.Fatal("renderD128 missing from Devices")
	}
	// Existence is not capability: this is the case a plain directory listing
	// gets wrong, and the one that costs users an afternoon.
	if dev.Usable {
		t.Fatal("a node with mode 0000 was reported usable")
	}
	for _, want := range []string{dev.Path, "permission denied", "group"} {
		if !strings.Contains(dev.Problem, want) {
			t.Errorf("Problem = %q, want it to mention %q", dev.Problem, want)
		}
	}
	if !notesContain(info.Notes, dev.Path) {
		t.Errorf("Notes = %q, want the unopenable device reported", info.Notes)
	}
	if info.VAAPIDevice != "" {
		t.Errorf("VAAPIDevice = %q, want empty: the node cannot be opened", info.VAAPIDevice)
	}
	if !notesContain(info.Notes, "no usable render node") {
		t.Errorf("Notes = %q, want VAAPI's absence explained", info.Notes)
	}
}

func TestPermissionProblemFallsBackToAdviceWhenTheGroupIsUnknown(t *testing.T) {
	tests := []struct {
		name  string
		group string
		want  []string
	}{
		{"named group", "render", []string{"/dev/dri/renderD128", "permission denied", `"render"`, "usermod -aG render", "--group-add render"}},
		{"numeric gid still actionable", "104", []string{"usermod -aG 104"}},
		{"unknown group", "", []string{"/dev/dri/renderD128", "permission denied", "needs read/write access"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := permissionProblem("/dev/dri/renderD128", tc.group)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("permissionProblem = %q, want it to mention %q", got, want)
				}
			}
		})
	}
}

func TestMissingRenderNodesAreReportedAsAPassthroughProblem(t *testing.T) {
	tests := []struct {
		name  string
		build func(*driTree) *driTree
	}{
		{"no /dev/dri at all", func(d *driTree) *driTree { return d.rm("dev/dri") }},
		{"/dev/dri exists but is empty", func(d *driTree) *driTree { return d }},
		{"/dev/dri holds nothing that is a node", func(d *driTree) *driTree {
			d.mkdirAll(filepath.Join(d.roots.dev, "dri", "by-path"))
			return d
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info := tc.build(newDRITree(t)).scan()

			if len(info.Devices) != 0 {
				t.Errorf("Devices = %+v, want none", info.Devices)
			}
			if info.VAAPIDevice != "" {
				t.Errorf("VAAPIDevice = %q, want empty", info.VAAPIDevice)
			}
			// The container case is the common one and the fix is one flag.
			if !notesContain(info.Notes, "--device /dev/dri") {
				t.Errorf("Notes = %q, want the Docker passthrough fix named", info.Notes)
			}
			if !notesContain(info.Notes, "i915") {
				t.Errorf("Notes = %q, want the bare-metal driver fix named", info.Notes)
			}
		})
	}
}

func TestVAAPIDeviceIsChosenNotHardcoded(t *testing.T) {
	type node struct {
		name   string
		vendor string
		mode   os.FileMode
	}
	tests := []struct {
		name       string
		nodes      []node
		want       string
		wantNote   string
		privileged bool // needs modes to actually deny
	}{
		{
			name:  "single render node is used whatever its number",
			nodes: []node{{"card1", "0x8086", 0o666}, {"renderD129", "0x8086", 0o666}},
			want:  "renderD129",
		},
		{
			name:  "first usable render node wins ties",
			nodes: []node{{"renderD128", "0x8086", 0o666}, {"renderD129", "0x1002", 0o666}},
			want:  "renderD128",
		},
		{
			// The NVIDIA render node opens fine and then has no VAAPI encode
			// entrypoint, so it must lose to a card that does.
			name:  "intel beats nvidia regardless of node order",
			nodes: []node{{"renderD128", "0x10de", 0o666}, {"renderD129", "0x8086", 0o666}},
			want:  "renderD129",
		},
		{
			name:  "amd beats an unnamed node",
			nodes: []node{{"renderD128", "", 0o666}, {"renderD129", "0x1002", 0o666}},
			want:  "renderD129",
		},
		{
			// virtio-gpu and ARM SoC drivers have no PCI vendor at all, and are
			// still likelier to encode than an NVIDIA node.
			name:  "an unnamed node beats nvidia",
			nodes: []node{{"renderD128", "", 0o666}, {"renderD129", "0x10de", 0o666}},
			want:  "renderD128",
		},
		{
			name:       "an unopenable intel node loses to an openable amd one",
			nodes:      []node{{"renderD128", "0x8086", 0o000}, {"renderD129", "0x1002", 0o666}},
			want:       "renderD129",
			privileged: true,
		},
		{
			// A card node needs DRM master; it opens for root and fails for the
			// service user, so it is never a safe -vaapi_device.
			name:     "card nodes are never chosen",
			nodes:    []node{{"card0", "0x8086", 0o666}},
			want:     "",
			wantNote: "no renderD* node",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.privileged {
				requireUnprivileged(t)
			}
			tree := newDRITree(t)
			for _, n := range tc.nodes {
				tree.node(n.name, n.vendor, n.mode)
			}
			info := tree.scan()

			want := ""
			if tc.want != "" {
				want = filepath.Join(tree.roots.dev, "dri", tc.want)
			}
			if info.VAAPIDevice != want {
				t.Errorf("VAAPIDevice = %q, want %q", info.VAAPIDevice, want)
			}
			if tc.wantNote != "" && !notesContain(info.Notes, tc.wantNote) {
				t.Errorf("Notes = %q, want %q explained", info.Notes, tc.wantNote)
			}
		})
	}
}

func TestScanSurvivesSysfsBeingAbsent(t *testing.T) {
	// Containers are routinely started without /sys/class/drm even when
	// /dev/dri was passed through. An unnamed card is still a usable card.
	info := newDRITree(t).node("renderD128", "", 0o666).rm("sys").scan()

	dev, ok := deviceByNode(info, "renderD128")
	if !ok {
		t.Fatal("renderD128 missing from Devices")
	}
	if dev.Vendor != VendorUnknown || dev.VendorID != "" {
		t.Errorf("vendor = %q/%q, want unknown/empty", dev.Vendor, dev.VendorID)
	}
	if !dev.Usable {
		t.Error("a readable node was reported unusable because sysfs was missing")
	}
	if info.VAAPIDevice == "" {
		t.Error("VAAPIDevice is empty; an unnamed but openable render node is still usable")
	}
}

func TestNVIDIAStackIsDetectedFromDeviceNodesAlone(t *testing.T) {
	// The CUDA container images encode happily with no nvidia-smi installed,
	// so requiring it would report "no NVIDIA" on the machines this exists for.
	info := newDRITree(t).
		node("renderD128", "0x10de", 0o666).
		devFile("nvidia0").
		devFile("nvidiactl").
		scan()

	if !info.NVIDIA {
		t.Error("NVIDIA = false with /dev/nvidia0 present")
	}
	if !info.HasVendor(VendorNVIDIA) {
		t.Errorf("Vendors = %v, want nvidia", info.Vendors)
	}
	if notesContain(info.Notes, "proprietary NVIDIA driver") {
		t.Errorf("Notes = %q, want no driver complaint when the nodes are there", info.Notes)
	}
}

func TestNVIDIACardWithoutTheProprietaryDriverIsCalledOut(t *testing.T) {
	requireNoNvidiaSMI(t)

	// An NVIDIA card on the bus with no /dev/nvidia* means nouveau, which has
	// no encoder at all — a distinct failure from "no GPU".
	info := newDRITree(t).node("renderD128", "0x10de", 0o666).scan()

	if info.NVIDIA {
		t.Error("NVIDIA = true with no /dev/nvidia* and no driver")
	}
	if !notesContain(info.Notes, "proprietary NVIDIA driver") {
		t.Errorf("Notes = %q, want the nouveau case explained", info.Notes)
	}
	if !notesContain(info.Notes, "--gpus all") {
		t.Errorf("Notes = %q, want the container fix named", info.Notes)
	}
}

func TestNoNVIDIAIsReportedOnAMachineWithoutIt(t *testing.T) {
	requireNoNvidiaSMI(t)

	info := newDRITree(t).node("renderD128", "0x8086", 0o666).scan()
	if info.NVIDIA || info.NVIDIADriver != "" {
		t.Errorf("NVIDIA=%v driver=%q on an Intel-only fixture", info.NVIDIA, info.NVIDIADriver)
	}
	if notesContain(info.Notes, "NVIDIA") {
		t.Errorf("Notes = %q, want nothing about NVIDIA", info.Notes)
	}
}

func TestNVIDIADriverVersionComesFromProcfsWithoutSpawningAnything(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "modern driver",
			content: "NVRM version: NVIDIA UNIX x86_64 Kernel Module  550.54.14  Thu Feb 22 01:44:30 UTC 2024\nGCC version:  gcc 13.2.0\n",
			want:    "550.54.14",
		},
		{
			name:    "two-component version",
			content: "NVRM version: NVIDIA UNIX Open Kernel Module for x86_64  535.86\n",
			want:    "535.86",
		},
		{
			name:    "no NVRM line",
			content: "GCC version:  gcc 13.2.0\n",
			want:    "",
		},
		{
			name:    "unparseable",
			content: "NVRM version: something we have never seen\n",
			want:    "",
		},
		{
			name:    "empty file",
			content: "",
			want:    "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tree := newDRITree(t).nvidiaProcVersion(tc.content)
			if got := nvidiaProcVersion(tree.roots.nvidiaProc); got != tc.want {
				t.Errorf("nvidiaProcVersion = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNVIDIADriverVersionIsEmptyWhenProcfsIsAbsent(t *testing.T) {
	tree := newDRITree(t)
	if got := nvidiaProcVersion(tree.roots.nvidiaProc); got != "" {
		t.Errorf("nvidiaProcVersion = %q, want empty", got)
	}
}

func TestProcfsDriverVersionIsReportedOnTheScan(t *testing.T) {
	info := newDRITree(t).
		devFile("nvidia0").
		nvidiaProcVersion("NVRM version: NVIDIA UNIX x86_64 Kernel Module  550.54.14  Thu Feb 22\n").
		scan()

	if info.NVIDIADriver != "550.54.14" {
		t.Errorf("NVIDIADriver = %q, want 550.54.14", info.NVIDIADriver)
	}
	if !info.NVIDIA {
		t.Error("NVIDIA = false with /dev/nvidia0 and a driver version present")
	}
}

// Detection is a diagnostic, never a gate: every broken tree must still return.
func TestScanIsNeverFatal(t *testing.T) {
	tests := []struct {
		name  string
		roots linuxRoots
	}{
		{"nothing exists", linuxRoots{dev: "/nonexistent/dev", sys: "/nonexistent/sys", nvidiaProc: "/nonexistent/proc"}},
		{"empty paths", linuxRoots{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info := scanLinuxGPUs(context.Background(), tc.roots)
			if info.Platform != "linux" {
				t.Errorf("Platform = %q, want linux", info.Platform)
			}
			if info.VAAPIDevice != "" {
				t.Errorf("VAAPIDevice = %q, want empty", info.VAAPIDevice)
			}
			if len(info.Notes) == 0 {
				t.Error("no notes; a scan that finds nothing must say why")
			}
		})
	}
}
