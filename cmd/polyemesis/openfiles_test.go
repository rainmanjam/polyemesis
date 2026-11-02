package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// openFilesUnder lists the files under dir that this process holds open.
//
// Linux reads /proc/self/fd; macOS asks lsof, when it is installed. Elsewhere,
// Windows included, it returns nil: there the same fault shows itself directly,
// as t.TempDir's cleanup failing to remove a file in use.
func openFilesUnder(t *testing.T, dir string) []string {
	t.Helper()
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		root = dir
	}
	under := func(p string) bool {
		return p == root || strings.HasPrefix(p, root+string(filepath.Separator)) ||
			p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
	}
	var held []string
	switch runtime.GOOS {
	case "linux":
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatalf("read /proc/self/fd: %v", err)
		}
		for _, e := range entries {
			if p, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name())); err == nil && under(p) {
				held = append(held, p)
			}
		}
	case "darwin":
		lsof, err := exec.LookPath("lsof")
		if err != nil {
			return nil
		}
		// -Fn prints one "n<path>" line per open file.
		out, err := exec.Command(lsof, "-n", "-P", "-Fn", "-p", strconv.Itoa(os.Getpid())).Output()
		if err != nil {
			t.Fatalf("lsof: %v", err)
		}
		for _, line := range strings.Split(string(out), "\n") {
			if p, ok := strings.CutPrefix(line, "n"); ok && under(p) {
				held = append(held, p)
			}
		}
	}
	return held
}
