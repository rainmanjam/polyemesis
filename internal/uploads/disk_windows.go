//go:build windows

package uploads

import (
	"strings"

	"golang.org/x/sys/windows"
)

// freeBytes reports free bytes on the volume holding path. See disk_unix.go for
// why this is not shared with internal/recording.
func freeBytes(path string) (uint64, error) {
	// GetDiskFreeSpaceExW reads a bare drive letter ("C:") as "the current
	// directory on C:" rather than the volume root, and rejects a UNC share
	// root without its trailing separator. Normalising is harmless on an
	// ordinary directory.
	if path != "" && !strings.HasSuffix(path, `\`) && !strings.HasSuffix(path, "/") {
		path += `\`
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	// The FIRST out-parameter is free-bytes-available-to-the-caller, which
	// respects quotas; total free on the volume is the third and is not what a
	// quota-limited service user can actually use.
	var availToCaller, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &availToCaller, &total, &totalFree); err != nil {
		return 0, err
	}
	return availToCaller, nil
}
