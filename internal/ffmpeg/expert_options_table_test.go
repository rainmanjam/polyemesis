package ffmpeg

import (
	"os/exec"
	"regexp"
	"slices"
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
	fileish := regexp.MustCompile(`(?i)(^|[^A-Za-z_])(file|files|filename|filenames|filepath|path|paths|dir|directory|url|urls|uri|log|logs|dump|dumps|stats|statistics)([^A-Za-z_]|$)`)
	// Read and judged benign: "fast path using libc memcpy" is about copying.
	benign := map[string][]string{
		"scale": {"memcpy"}, "scale2ref": {"memcpy"}, // "fast path using libc memcpy"
		"transpose":  {"dir"},         // the transpose direction
		"palettegen": {"stats_mode"},  // which frames the palette statistics cover
		"acrossfade": {"inputs", "n"}, // the number of input STREAMS to cross-fade
	}
	// Option lines only: "  name   <type>  flags  description". The enum
	// constants listed under an option ("log  1  ...  logarithmic") are
	// values, not options, and name nothing.
	optLine := regexp.MustCompile(`^ +([A-Za-z0-9_-]+) +<`)
	for name := range allowedFilters {
		out, _ := exec.Command(bin, "-hide_banner", "-h", "filter="+name).CombinedOutput()
		if strings.Contains(string(out), "Unknown filter") {
			continue // not in this build; nothing to read
		}
		for _, line := range strings.Split(string(out), "\n") {
			m := optLine.FindStringSubmatch(line)
			if m == nil || slices.Contains(benign[name], m[1]) {
				continue
			}
			if fileish.MatchString(line) {
				t.Errorf("allowed filter %s has an option that names a file or path: %s",
					name, strings.TrimSpace(line))
			}
		}
	}
}

// The option allowlist is vetted by the generator against FFmpeg's own help.
// This re-reads that help, so a table regenerated wrongly or edited by hand to
// admit an option described as taking a file, path, URL, log, dump or stats
// fails here. (The same expression as scripts/gen-expert-ffmpeg-options.sh.)
func TestNoAllowedOptionIsDescribedAsAFile(t *testing.T) {
	bin := requireFFmpeg(t)
	out, err := exec.Command(bin, "-hide_banner", "-h", "full").Output()
	if err != nil {
		t.Fatalf("ffmpeg -h full: %v", err)
	}
	fileish := regexp.MustCompile(`(?i)(^|[^A-Za-z_])(file|files|filename|filenames|filepath|path|paths|dir|directory|url|urls|uri|log|logs|dump|dumps|stats|statistics)([^A-Za-z_]|$)`)
	name := regexp.MustCompile(`^ {0,2}(-[A-Za-z0-9_-]+)`)
	for _, line := range strings.Split(string(out), "\n") {
		m := name.FindStringSubmatch(line)
		if m == nil || !fileish.MatchString(line) {
			continue
		}
		if ffmpegValueOptions[m[1]] || ffmpegBoolOptions[m[1]] {
			t.Errorf("%s is allowed, but FFmpeg describes it as touching a file: %s", m[1], strings.TrimSpace(line))
		}
	}
}

// The options the security reviews named are refused, whatever this build lists.
// No option naming a device is allowed either: each opens a path.
func TestTheReviewedFileOptionsAreNotAllowed(t *testing.T) {
	for _, o := range []string{"-x265-stats", "-graph_file", "-dumpgraph", "-passlogfile", "-progress",
		"-vstats_file", "-attach", "-sdp_file", "-key_file", "-report", "-fpre", "-vpre",
		"-vaapi_device", "-init_hw_device", "-filter_hw_device", "-hwaccel_device"} {
		if ffmpegValueOptions[o] || ffmpegBoolOptions[o] {
			t.Errorf("%s is on the allowlist", o)
		}
		if err := CheckExpertFileAccess("output args", []string{o, "x"}); err == nil {
			t.Errorf("%s x was accepted", o)
		}
	}
}

func TestNoAllowedOptionNamesADevice(t *testing.T) {
	for o := range ffmpegValueOptions {
		if strings.Contains(strings.ToLower(o), "device") {
			t.Errorf("%s is on the allowlist, and a device option opens a path", o)
		}
	}
	for o := range ffmpegBoolOptions {
		if strings.Contains(strings.ToLower(o), "device") {
			t.Errorf("%s is on the allowlist, and a device option opens a path", o)
		}
	}
}

// THE DANGEROUS DIRECTION. A table VALUE option that FFmpeg treats as a
// boolean makes the walk swallow the next token as its value while FFmpeg
// takes that token as a second output file. Every value option, run alone,
// must therefore be refused for its missing argument -- or not be one of
// ffmpeg's own options at all (AVOptions report "Unrecognized" when given no
// value and no stream to attach to).
func TestEveryTableValueOptionTakesAValueInTheRealFFmpeg(t *testing.T) {
	bin := requireFFmpeg(t)
	type res struct {
		opt, out string
	}
	opts := make(chan string)
	results := make(chan res)
	dir := t.TempDir()
	for w := 0; w < 8; w++ {
		go func() {
			for o := range opts {
				cmd := exec.Command(bin, "-hide_banner", "-nostdin", o)
				cmd.Dir = dir
				out, _ := cmd.CombinedOutput()
				results <- res{o, string(out)}
			}
		}()
	}
	go func() {
		for o := range ffmpegValueOptions {
			opts <- o
		}
		close(opts)
	}()
	for range ffmpegValueOptions {
		r := <-results
		if !strings.Contains(r.out, "Missing argument") && !strings.Contains(r.out, "Unrecognized option") {
			t.Errorf("%s is listed as taking a value, but FFmpeg run with it alone says: %.120q", r.opt, r.out)
		}
	}
}

// Nested option dictionaries hand a key=value list to another component,
// past the walk. Only the encoder-param lists the walk checks may be allowed.
func TestNoAllowedOptionIsAnUncheckedDictionary(t *testing.T) {
	bin := requireFFmpeg(t)
	out, err := exec.Command(bin, "-hide_banner", "-h", "full").Output()
	if err != nil {
		t.Fatalf("ffmpeg -h full: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "<dictionary>") {
			continue
		}
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		o := f[0]
		if (ffmpegValueOptions[o] || ffmpegBoolOptions[o]) && !codecParamFlags[o] {
			t.Errorf("%s is a dictionary option the walk does not check, and it is allowed", o)
		}
	}
}
