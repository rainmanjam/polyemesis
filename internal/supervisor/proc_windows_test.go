//go:build windows

// These tests cover the parts of the Windows process path that are decidable
// without a live child: the CreateProcess flag arithmetic and the process
// group ID guard. Whether FFmpeg actually finalises its output on CTRL_BREAK,
// and whether the job object actually reaps a crashed supervisor's children,
// can only be established by running the binary on Windows.
package supervisor

import (
	"os"
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCreationFlagsAddsNewProcessGroupWithoutClobberingExistingFlags(t *testing.T) {
	tests := []struct {
		name     string
		existing uint32
		want     uint32
	}{
		{
			name:     "zero value gains the process group flag",
			existing: 0,
			want:     windows.CREATE_NEW_PROCESS_GROUP,
		},
		{
			name:     "unrelated caller flags are preserved",
			existing: windows.CREATE_UNICODE_ENVIRONMENT,
			want:     windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NEW_PROCESS_GROUP,
		},
		{
			name:     "applying twice is a no-op",
			existing: windows.CREATE_NEW_PROCESS_GROUP,
			want:     windows.CREATE_NEW_PROCESS_GROUP,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := creationFlags(tc.existing); got != tc.want {
				t.Fatalf("creationFlags(%#x) = %#x, want %#x", tc.existing, got, tc.want)
			}
		})
	}
}

func TestApplyProcessGroupAllocatesSysProcAttrWhenAbsent(t *testing.T) {
	tests := []struct {
		name string
		attr *syscall.SysProcAttr
	}{
		{name: "nil SysProcAttr is allocated"},
		{name: "existing SysProcAttr is reused", attr: &syscall.SysProcAttr{CreationFlags: windows.CREATE_UNICODE_ENVIRONMENT}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("cmd.exe", "/c", "exit")
			if tc.attr != nil {
				cmd.SysProcAttr = tc.attr
			}

			applyProcessGroup(cmd)

			if cmd.SysProcAttr == nil {
				t.Fatal("SysProcAttr is nil after applyProcessGroup")
			}
			if cmd.SysProcAttr.CreationFlags&windows.CREATE_NEW_PROCESS_GROUP == 0 {
				t.Fatalf("CreationFlags = %#x, want CREATE_NEW_PROCESS_GROUP set", cmd.SysProcAttr.CreationFlags)
			}
			if tc.attr != nil && cmd.SysProcAttr != tc.attr {
				t.Fatal("applyProcessGroup replaced the caller's SysProcAttr instead of amending it")
			}
		})
	}
}

// A group ID of 0 addresses every process on the console, so anything that
// could produce one has to be rejected before it reaches
// GenerateConsoleCtrlEvent — a bug here would kill polyemesis itself and every
// other supervised child, not just fail to stop one.
func TestProcessGroupIDRefusesToAddressTheWholeConsole(t *testing.T) {
	tests := []struct {
		name    string
		cmd     *exec.Cmd
		want    uint32
		wantOK  bool
		comment string
	}{
		{
			name:   "nil command",
			cmd:    nil,
			wantOK: false,
		},
		{
			name:   "command that was never started",
			cmd:    exec.Command("cmd.exe", "/c", "exit"),
			wantOK: false,
		},
		{
			name:   "process with a zero pid",
			cmd:    &exec.Cmd{Process: &os.Process{Pid: 0}},
			wantOK: false,
		},
		{
			name:   "process with a negative pid",
			cmd:    &exec.Cmd{Process: &os.Process{Pid: -1}},
			wantOK: false,
		},
		{
			name:   "started process uses its own pid as the group",
			cmd:    &exec.Cmd{Process: &os.Process{Pid: 4321}},
			want:   4321,
			wantOK: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := processGroupID(tc.cmd)
			if ok != tc.wantOK {
				t.Fatalf("processGroupID ok = %v, want %v", ok, tc.wantOK)
			}
			if got != tc.want {
				t.Fatalf("processGroupID = %d, want %d", got, tc.want)
			}
			if !ok && got != 0 {
				t.Fatalf("rejected command yielded group %d; must be 0", got)
			}
		})
	}
}

// signalGroup and killGroup must be inert on a command that never started,
// because Stop can race a child that exited on its own.
func TestSignalAndKillAreNoOpsWithoutAProcess(t *testing.T) {
	tests := []struct {
		name string
		fn   func(*exec.Cmd)
	}{
		{name: "signalGroup", fn: signalGroup},
		{name: "killGroup", fn: killGroup},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.fn(exec.Command("cmd.exe", "/c", "exit"))
			tc.fn(nil)
		})
	}
}
