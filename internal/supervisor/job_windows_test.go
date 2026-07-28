//go:build windows

package supervisor

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestJobLimitsRequestKillOnCloseAndForbidBreakaway(t *testing.T) {
	got := jobLimits().BasicLimitInformation.LimitFlags

	tests := []struct {
		name string
		flag uint32
		want bool
		why  string
	}{
		{
			name: "kill on job close",
			flag: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
			want: true,
			why:  "without it a crashed polyemesis leaves FFmpeg holding ports and live RTMP connections",
		},
		{
			name: "breakaway ok",
			flag: windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK,
			want: false,
			why:  "a child that can leave the job defeats the guarantee",
		},
		{
			name: "silent breakaway ok",
			flag: windows.JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK,
			want: false,
			why:  "a child that can leave the job defeats the guarantee",
		},
		{
			name: "process time limit",
			flag: windows.JOB_OBJECT_LIMIT_PROCESS_TIME,
			want: false,
			why:  "a resource cap here would kill a healthy encoder mid-stream",
		},
		{
			name: "job memory limit",
			flag: windows.JOB_OBJECT_LIMIT_JOB_MEMORY,
			want: false,
			why:  "a resource cap here would kill a healthy encoder mid-stream",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if set := got&tc.flag != 0; set != tc.want {
				t.Fatalf("LimitFlags=%#x has %s set=%v, want %v: %s", got, tc.name, set, tc.want, tc.why)
			}
		})
	}
}

// The struct handed to SetInformationJobObject is passed as a raw pointer plus
// a length, so a wrong size or a misaligned field would be accepted silently
// by the compiler and rejected (or worse, misread) by the kernel. Round-trip a
// throwaway job to pin that the marshalling is right.
//
// This job is never assigned any process, so closing it terminates nothing.
func TestJobLimitsSurviveARoundTripThroughTheKernel(t *testing.T) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatalf("CreateJobObject: %v", err)
	}
	t.Cleanup(func() { _ = windows.CloseHandle(h) })

	want := jobLimits()
	if _, err := windows.SetInformationJobObject(
		h,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&want)),
		uint32(unsafe.Sizeof(want)),
	); err != nil {
		t.Fatalf("SetInformationJobObject: %v", err)
	}

	var got windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	var retlen uint32
	if err := windows.QueryInformationJobObject(
		h,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&got)),
		uint32(unsafe.Sizeof(got)),
		&retlen,
	); err != nil {
		t.Fatalf("QueryInformationJobObject: %v", err)
	}

	if got.BasicLimitInformation.LimitFlags != want.BasicLimitInformation.LimitFlags {
		t.Fatalf("LimitFlags round-tripped as %#x, want %#x",
			got.BasicLimitInformation.LimitFlags, want.BasicLimitInformation.LimitFlags)
	}
}

// Deliberately not tested here: ensureJob itself. Calling it would enrol the
// test binary in a KILL_ON_JOB_CLOSE job for the rest of the run, and the
// blast radius of getting that wrong on a machine nobody is watching is worse
// than the coverage is worth. Verify it by running polyemesis on Windows and
// killing it from Task Manager while a stream is live: every ffmpeg.exe should
// disappear with it.
