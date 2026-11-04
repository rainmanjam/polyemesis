package ffmpeg

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// The option table and the filter allowlist are claims about FFmpeg. These
// tests check them against the FFmpeg on this machine, so a table that drifts
// from the binary fails here rather than becoming a bypass. Skipped where no
// FFmpeg is installed; the classification itself is fail-closed either way.

// A table boolean that actually takes a value would swallow the next token,
// and a path after it would no longer read as a second output.
func TestEveryTableBooleanTakesNoValueInTheRealFFmpeg(t *testing.T) {
	bin := requireFFmpeg(t)
	for opt := range ffmpegBoolOptions {
		cmd := exec.Command(bin, "-hide_banner", "-nostdin", opt)
		cmd.Dir = t.TempDir() // -report and -vstats write into the working directory
		out, _ := cmd.CombinedOutput()
		if strings.Contains(string(out), "Missing argument") {
			t.Errorf("%s is listed as a boolean, but this FFmpeg says it takes a value", opt)
		}
	}
}

// Every allowed filter is re-read: none of its options may describe a file or
// a path. A filter that gains one in a later FFmpeg fails here.
func TestNoAllowedFilterHasAFileOption(t *testing.T) {
	bin := requireFFmpeg(t)
	fileish := regexp.MustCompile(`(?i)\b(file|filename|filepath|path)\b`)
	// Read and judged benign: "fast path using libc memcpy" is about copying.
	benign := map[string]string{"scale": "memcpy", "scale2ref": "memcpy"}
	optLine := regexp.MustCompile(`^ +([a-z_0-9]+) `)
	for name := range allowedFilters {
		out, _ := exec.Command(bin, "-hide_banner", "-h", "filter="+name).CombinedOutput()
		if strings.Contains(string(out), "Unknown filter") {
			continue // not in this build; nothing to read
		}
		for _, line := range strings.Split(string(out), "\n") {
			m := optLine.FindStringSubmatch(line)
			if m == nil || benign[name] == m[1] {
				continue
			}
			if fileish.MatchString(line) {
				t.Errorf("allowed filter %s has an option that names a file or path: %s",
					name, strings.TrimSpace(line))
			}
		}
	}
}
