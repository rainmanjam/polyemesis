//go:build windows

package recording

import (
	"strings"

	"golang.org/x/sys/windows"
)

// diskFree reports free and total bytes on the volume holding path.
func diskFree(path string) (free, total uint64, err error) {
	// GetDiskFreeSpaceExW rejects a UNC share root that lacks its trailing
	// separator, and reads a bare drive letter ("C:") as "the current directory
	// on C:" rather than the volume root. A trailing separator is harmless on an
	// ordinary directory, so normalise rather than special-case.
	if path != "" && !strings.HasSuffix(path, `\`) && !strings.HasSuffix(path, "/") {
		path += `\`
	}

	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, err
	}

	var availToCaller, totalBytes, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &availToCaller, &totalBytes, &totalFree); err != nil {
		return 0, 0, err
	}
	// availToCaller, not totalFree: the two diverge once a disk quota is in
	// force, and only the caller's share is bytes the recorder can actually
	// write. Same reason the Unix path uses Bavail rather than Bfree.
	return availToCaller, totalBytes, nil
}
