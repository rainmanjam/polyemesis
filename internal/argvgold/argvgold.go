// Package argvgold pins FFmpeg command lines byte for byte, one element per
// line, in testdata files that are reviewed like code.
//
// A refactor of how arguments are assembled is only safe if every command line
// an existing configuration produces is unchanged except where the change is
// the point. The golden files make that visible: a diff of testdata/argv is the
// complete list of behaviour changes, argument boundaries included
// (["-map", "0:a"] and ["-map\n0:a"] are different commands).
//
// Regenerate with POLY_UPDATE_ARGV=1 go test ./...
package argvgold

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// LogLevel runs the test under POLYEMESIS_FFMPEG_LOGLEVEL=lvl ("" means unset)
// and restores the default when it ends.
func LogLevel(t testing.TB, lvl string) {
	t.Helper()
	ffmpeg.SetLogLevel(lvl)
	t.Cleanup(func() { ffmpeg.SetLogLevel("") })
}

// Check compares argv with testdata/argv/<name>.txt in the calling package.
//
// A name ending in "_loglevel_debug" is a case built under LogLevel(t, "debug"),
// and must carry that level: docs/CONFIGURATION.md says POLYEMESIS_FFMPEG_LOGLEVEL
// is the -loglevel EVERY FFmpeg child gets, and eight children once ignored it.
//
// roots are directories the test itself passed in as slash paths. A builder that
// joins onto one (filepath.Join) returns the host's separator, so an argument
// under a root is compared with its separators as slashes. Nothing else is
// rewritten: a filter escape or URL carries backslashes or slashes on purpose.
func Check(t testing.TB, name string, argv []string, roots ...string) {
	t.Helper()
	argv = slashUnder(argv, roots, string(filepath.Separator))
	if strings.HasSuffix(name, "_loglevel_debug") && !hasPair(argv, "-loglevel", "debug") {
		t.Errorf("%s ignores POLYEMESIS_FFMPEG_LOGLEVEL=debug: %q", name, argv)
	}
	var b strings.Builder
	for _, a := range argv {
		b.WriteString(strconv.Quote(a))
		b.WriteByte('\n')
	}
	got := b.String()
	path := filepath.Join("testdata", "argv", name+".txt")
	if os.Getenv("POLY_UPDATE_ARGV") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no golden argv for %s (POLY_UPDATE_ARGV=1 writes it): %v", name, err)
	}
	if string(want) != got {
		t.Errorf("the argv for %s changed.\n--- got\n%s--- want\n%s", name, got, string(want))
	}
}

// slashUnder returns argv with every element that starts with one of roots
// (spelled with sep) rewritten back to slashes. It is the identity where sep is
// already a slash.
func slashUnder(argv, roots []string, sep string) []string {
	if sep == "/" || len(roots) == 0 {
		return argv
	}
	out := slices.Clone(argv)
	for i, a := range out {
		for _, r := range roots {
			if r != "" && strings.HasPrefix(a, strings.ReplaceAll(r, "/", sep)) {
				out[i] = strings.ReplaceAll(a, sep, "/")
				break
			}
		}
	}
	return out
}

func hasPair(argv []string, flag, val string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == val {
			return true
		}
	}
	return false
}

// RequireRelayConsumer fails unless argv reads the relay hub with the input
// options every consumer needs (ffmpeg.RelayInputArgs), before its first -i.
// Five consumers once wrote their own prologue without them.
func RequireRelayConsumer(t testing.TB, name string, argv []string) {
	t.Helper()
	first := -1
	for i, a := range argv {
		if a == "-i" {
			first = i
			break
		}
	}
	if first < 0 {
		t.Errorf("%s has no -i: %q", name, argv)
		return
	}
	in := argv[:first]
	for _, want := range [][2]string{{"-scan_all_pmts", "0"}, {"-dts_delta_threshold", "31536000"}} {
		if !hasPair(in, want[0], want[1]) {
			t.Errorf("%s reads the relay without %s %s before -i: %q", name, want[0], want[1], argv)
		}
	}
	for _, flag := range []string{"-analyzeduration", "-probesize"} {
		if !slices.Contains(in, flag) {
			t.Errorf("%s reads the relay without %s before -i: %q", name, flag, argv)
		}
	}
}

// RequireRelayProducer fails unless argv ends with the relay output epilogue
// (ffmpeg.RelayProducerOutput), including the interleave-stall fix.
func RequireRelayProducer(t testing.TB, name string, argv []string) {
	t.Helper()
	for _, want := range [][2]string{{"-f", "mpegts"}, {"-max_interleave_delta", "100000"}, {"-muxdelay", "0"}, {"-muxpreload", "0"}, {"-flush_packets", "1"}} {
		if !hasPair(argv, want[0], want[1]) {
			t.Errorf("%s writes the relay without %s %s: %q", name, want[0], want[1], argv)
		}
	}
	if n := len(argv); n == 0 || !strings.Contains(argv[n-1], "pkt_size=1316") {
		t.Errorf("%s does not end at the relay's sized URL: %q", name, argv)
	}
}
