package argvgold

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/ffmpeg"
)

// recorder stands in for *testing.T so the failure paths can be exercised
// without failing the test that exercises them.
type recorder struct {
	testing.TB
	errs  []string
	fatal bool
}

func (r *recorder) Helper() {}
func (r *recorder) Errorf(f string, a ...any) {
	r.errs = append(r.errs, fmt.Sprintf(f, a...))
}
func (r *recorder) Fatalf(f string, a ...any) {
	r.errs = append(r.errs, fmt.Sprintf(f, a...))
	r.fatal = true
}
func (r *recorder) Fatal(a ...any) {
	r.errs = append(r.errs, fmt.Sprint(a...))
	r.fatal = true
}

func inTempDir(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
}

func TestCheckPassesOnAGoldenThatMatchesAndFlagsOneThatDoesNot(t *testing.T) {
	inTempDir(t)
	if err := os.MkdirAll(filepath.Join("testdata", "argv"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("testdata", "argv", "x.txt"), []byte("\"-a\"\n\"b c\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ok := &recorder{}
	Check(ok, "x", []string{"-a", "b c"})
	if len(ok.errs) != 0 {
		t.Errorf("a matching argv was flagged: %v", ok.errs)
	}

	// Argument boundaries are part of the golden: one element "-a b c" is not two.
	bad := &recorder{}
	Check(bad, "x", []string{"-a b c"})
	if len(bad.errs) != 1 || !strings.Contains(bad.errs[0], "changed") {
		t.Errorf("a changed argv was not flagged: %v", bad.errs)
	}
}

func TestCheckFailsWithoutAGoldenAndWritesOneWhenAsked(t *testing.T) {
	inTempDir(t)
	missing := &recorder{}
	Check(missing, "new", []string{"-x"})
	if !missing.fatal {
		t.Error("an argv with no golden passed")
	}

	t.Setenv("POLY_UPDATE_ARGV", "1")
	write := &recorder{}
	Check(write, "new", []string{"-x", "y"})
	if len(write.errs) != 0 {
		t.Fatalf("update mode failed: %v", write.errs)
	}
	b, err := os.ReadFile(filepath.Join("testdata", "argv", "new.txt"))
	if err != nil || string(b) != "\"-x\"\n\"y\"\n" {
		t.Errorf("the golden written is %q (%v)", b, err)
	}
}

func TestADebugLevelCaseMustCarryTheDebugLevel(t *testing.T) {
	inTempDir(t)
	t.Setenv("POLY_UPDATE_ARGV", "1")
	r := &recorder{}
	Check(r, "child_loglevel_debug", []string{"-loglevel", "warning"})
	if len(r.errs) != 1 || !strings.Contains(r.errs[0], "ignores POLYEMESIS_FFMPEG_LOGLEVEL") {
		t.Errorf("a child that ignores the override was not flagged: %v", r.errs)
	}
	ok := &recorder{}
	Check(ok, "child_loglevel_debug", []string{"-loglevel", "debug"})
	if len(ok.errs) != 0 {
		t.Errorf("a child that honours the override was flagged: %v", ok.errs)
	}
}

func TestLogLevelInstallsTheOverrideAndRestoresTheDefault(t *testing.T) {
	t.Run("inner", func(t *testing.T) {
		LogLevel(t, "trace")
		if got := ffmpeg.LogLevel(ffmpeg.LogWarning); got != "trace" {
			t.Errorf("LogLevel(trace) left %q", got)
		}
	})
	if got := ffmpeg.LogLevel(ffmpeg.LogWarning); got != ffmpeg.LogWarning {
		t.Errorf("the override outlived its test: %q", got)
	}
}

func TestRequireRelayConsumerAcceptsTheSharedOptionsAndNamesWhatIsMissing(t *testing.T) {
	good := append(ffmpeg.RelayConsumerInput("udp://127.0.0.1:1", ffmpeg.RelayQueue), "-map", "0")
	r := &recorder{}
	RequireRelayConsumer(r, "good", good)
	if len(r.errs) != 0 {
		t.Errorf("the shared prologue was refused: %v", r.errs)
	}

	for name, argv := range map[string][]string{
		"no input":         {"-map", "0"},
		"hand-written":     {"-fflags", "+genpts", "-thread_queue_size", "1024", "-i", "udp://127.0.0.1:1"},
		"options after -i": {"-i", "udp://127.0.0.1:1", "-analyzeduration", "1", "-probesize", "1", "-scan_all_pmts", "0", "-dts_delta_threshold", "31536000"},
	} {
		bad := &recorder{}
		RequireRelayConsumer(bad, name, argv)
		if len(bad.errs) == 0 {
			t.Errorf("%s: a consumer without the shared options was accepted", name)
		}
	}
}

func TestRequireRelayProducerAcceptsTheSharedEpilogueAndNamesWhatIsMissing(t *testing.T) {
	good := append([]string{"-map", "0"}, ffmpeg.RelayProducerOutput("udp://127.0.0.1:2")...)
	r := &recorder{}
	RequireRelayProducer(r, "good", good)
	if len(r.errs) != 0 {
		t.Errorf("the shared epilogue was refused: %v", r.errs)
	}

	bare := &recorder{}
	RequireRelayProducer(bare, "bare", []string{"-f", "mpegts", "-flush_packets", "1", ffmpeg.RelayOutputURL("udp://127.0.0.1:2")})
	if len(bare.errs) < 3 {
		t.Errorf("an epilogue without the interleave fix was accepted: %v", bare.errs)
	}
	empty := &recorder{}
	RequireRelayProducer(empty, "empty", nil)
	if len(empty.errs) == 0 {
		t.Error("an empty argv was accepted")
	}
}
