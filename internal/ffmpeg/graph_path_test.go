package ffmpeg

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// A path inside a filter option is unescaped TWICE -- as part of the graph,
// then as an option value -- so every metacharacter of either level has to
// survive both. Driven against the real parser with the movie= source, which
// every FFmpeg build has, so the measurement does not depend on libfreetype:
// a directory named with each graph metacharacter must open.
//
// MUTATION: GraphArgPath is filterPath (the single replacer that doubles every
// escape). Observed: FAIL, "No option name near" on the comma.
// MUTATION: the graph level forgets ','. Observed: FAIL, the graph splits.
func TestGraphArgPathSurvivesEveryMetacharacter(t *testing.T) {
	bin := testenv.FFmpegBinary(t, "ffmpeg", "without ffmpeg the filtergraph parser cannot be measured")
	for _, name := range []string{"plain", "a'b", "a,b", "a[b]c", "a;b", "it's,[all];here", "sp ace"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			src := filepath.Join(dir, "src.mkv")
			if out, err := exec.Command(bin, "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi",
				"-i", "color=c=red:s=32x32:d=1", "-c:v", "ffv1", src).CombinedOutput(); err != nil {
				t.Fatalf("fixture: %v %s", err, out)
			}
			graph := "movie=filename=" + GraphArgPath(src) + ",scale=w=16:h=16[v]"
			out, err := exec.Command(bin, "-hide_banner", "-loglevel", "error", "-filter_complex", graph,
				"-map", "[v]", "-frames:v", "1", "-f", "null", "-").CombinedOutput()
			if err != nil {
				t.Errorf("a directory named %q does not survive the graph: %v\n%s\ngraph: %s", name, err, out, graph)
			}
		})
	}
}
