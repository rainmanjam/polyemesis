package testenv

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// THE PINNED FFMPEG BUILD IS WRITTEN DOWN ONCE: .github/ffmpeg-pin.env.
//
// It used to be pasted into ten workflow steps, Dockerfile.cuda and
// scripts/install.sh, and only install.sh had a guard, so the 2026-10-29
// deleted-tag incident meant editing every copy by hand and hoping. Now the
// workflows reach the pin through .github/actions/install-ffmpeg, which reads
// the file at run time; install.sh is a curl-piped standalone script and
// Dockerfile.cuda an image recipe, so neither can read it, and these tests hold
// both to it instead.

func ffmpegRepoFile(t *testing.T, parts ...string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(append([]string{repoRootFromTest(t)}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		t.Fatalf("%s is empty; every assertion on it would pass by examining nothing", filepath.Join(parts...))
	}
	return string(b)
}

var ffmpegPinKeyRe = regexp.MustCompile(`^[A-Z0-9_]+$`)

// ffmpegPin parses .github/ffmpeg-pin.env: KEY=value lines, # comments, blanks.
// A duplicate key fails, because the action takes the first and a reader
// would take the last.
func ffmpegPin(t *testing.T) map[string]string {
	t.Helper()
	pin := map[string]string{}
	for i, line := range strings.Split(ffmpegRepoFile(t, ".github", "ffmpeg-pin.env"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || !ffmpegPinKeyRe.MatchString(k) || v == "" || strings.ContainsAny(v, " \t\"'$") {
			t.Fatalf("ffmpeg-pin.env:%d is not a plain KEY=value line: %q", i+1, line)
		}
		if _, dup := pin[k]; dup {
			t.Fatalf("ffmpeg-pin.env:%d repeats %s", i+1, k)
		}
		pin[k] = v
	}
	return pin
}

// The build every CI job and the images install: the 8.1 line.
var ffmpegPinned = []string{
	"FFMPEG_8_1_LINUX_AMD64", "FFMPEG_8_1_LINUX_ARM64", "FFMPEG_8_1_WINDOWS_AMD64",
	"FFMPEG_9_0_LINUX_AMD64",
}

func TestFFmpegPinFileIsWellFormed(t *testing.T) {
	pin := ffmpegPin(t)
	if !regexp.MustCompile(`^autobuild-\d{4}-\d{2}-\d{2}-\d{2}-\d{2}$`).MatchString(pin["FFMPEG_BTBN_TAG"]) {
		t.Errorf("FFMPEG_BTBN_TAG=%q is not a dated BtbN autobuild tag", pin["FFMPEG_BTBN_TAG"])
	}
	seen := map[string]string{}
	for _, p := range ffmpegPinned {
		asset, sum := pin[p+"_ASSET"], pin[p+"_SHA256"]
		if asset == "" || !strings.HasPrefix(asset, "ffmpeg-n") || strings.Contains(asset, "latest") {
			t.Errorf("%s_ASSET=%q is not a dated BtbN asset name", p, asset)
		}
		if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(sum) {
			t.Errorf("%s_SHA256=%q is not a lowercase sha256", p, sum)
		}
		if other, dup := seen[sum]; dup {
			t.Errorf("%s_SHA256 repeats %s's hash", p, other)
		}
		seen[sum] = p
	}
	known := map[string]bool{"FFMPEG_BTBN_TAG": true}
	for _, p := range ffmpegPinned {
		known[p+"_ASSET"], known[p+"_SHA256"] = true, true
	}
	for k := range pin {
		if !known[k] {
			t.Errorf("ffmpeg-pin.env has %s, which nothing reads; add it to ffmpegPinned or delete it", k)
		}
	}
}

func TestInstallerAgreesWithTheFFmpegPinFile(t *testing.T) {
	pin, src := ffmpegPin(t), installerSource(t)
	if m := regexp.MustCompile(`(?m)^FFMPEG_BTBN_TAG=(\S+)$`).FindStringSubmatch(src); m == nil || m[1] != pin["FFMPEG_BTBN_TAG"] {
		t.Errorf("install.sh FFMPEG_BTBN_TAG=%v, ffmpeg-pin.env says %s", m, pin["FFMPEG_BTBN_TAG"])
	}
	assets, sums := functionBody(t, src, "ffmpeg_static_asset"), functionBody(t, src, "ffmpeg_static_sha256")
	for arch, p := range map[string]string{"amd64": "FFMPEG_8_1_LINUX_AMD64", "arm64": "FFMPEG_8_1_LINUX_ARM64"} {
		if want := arch + `) echo "` + pin[p+"_ASSET"] + `"`; !strings.Contains(assets, want) {
			t.Errorf("ffmpeg_static_asset has no `%s`; ffmpeg-pin.env pins %s_ASSET", want, p)
		}
		if want := arch + `) echo "` + pin[p+"_SHA256"] + `"`; !strings.Contains(sums, want) {
			t.Errorf("ffmpeg_static_sha256 has no `%s`; ffmpeg-pin.env pins %s_SHA256", want, p)
		}
	}
}

func TestDockerfileCudaAgreesWithTheFFmpegPinFile(t *testing.T) {
	pin, src := ffmpegPin(t), ffmpegRepoFile(t, "Dockerfile.cuda")
	for arg, want := range map[string]string{
		"FFMPEG_BTBN_TAG": pin["FFMPEG_BTBN_TAG"],
		"FFMPEG_ASSET":    pin["FFMPEG_8_1_LINUX_AMD64_ASSET"],
		"FFMPEG_SHA256":   pin["FFMPEG_8_1_LINUX_AMD64_SHA256"],
	} {
		m := regexp.MustCompile(`(?m)^ARG ` + arg + `=(\S+)$`).FindStringSubmatch(src)
		if m == nil || m[1] != want {
			t.Errorf("Dockerfile.cuda ARG %s=%v, ffmpeg-pin.env says %s", arg, m, want)
		}
	}
}

// No workflow may carry its own copy. The action is the only place that turns
// the pin into a download, and the only file besides the pin that may name it.
func TestNoWorkflowCarriesItsOwnFFmpegPin(t *testing.T) {
	pin := ffmpegPin(t)
	root := repoRootFromTest(t)
	var files []string
	for _, glob := range []string{"workflows/*.yml", "workflows/*.yaml", "actions/*/action.yml"} {
		m, err := filepath.Glob(filepath.Join(root, ".github", glob))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	if len(files) < 10 {
		t.Fatalf("found %d workflow files; the glob is wrong and this test examines nothing", len(files))
	}
	action := filepath.Join(root, ".github", "actions", "install-ffmpeg", "action.yml")
	users := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		body := string(b)
		rel, _ := filepath.Rel(root, f)
		if strings.Contains(body, "./.github/actions/install-ffmpeg") {
			users++
		}
		if f == action {
			if !strings.Contains(body, "ffmpeg-pin.env") {
				t.Errorf("%s does not read ffmpeg-pin.env", rel)
			}
			continue
		}
		if strings.Contains(body, "FFmpeg-Builds/releases/download") {
			t.Errorf("%s downloads from BtbN itself; use ./.github/actions/install-ffmpeg", rel)
		}
		for k, v := range pin {
			if strings.Contains(body, v) {
				t.Errorf("%s restates %s=%s; read it through the action", rel, k, v)
			}
		}
	}
	if users < 7 {
		t.Errorf("only %d files use ./.github/actions/install-ffmpeg; the pin is not single-sourced", users)
	}
}
