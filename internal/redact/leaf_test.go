package redact

import (
	"os/exec"
	"strings"
	"testing"
)

// TestRedactImportsNothingFromThisModule holds the package doc's claim. The
// point of this package is that db, ffmpeg and supervisor can redact without
// importing a feature package; one internal import here would quietly bring
// that edge back.
func TestRedactImportsNothingFromThisModule(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	const self = "github.com/rainmanjam/polyemesis/internal/redact"
	for _, p := range strings.Fields(string(out)) {
		if strings.HasPrefix(p, "github.com/rainmanjam/polyemesis") && p != self {
			t.Errorf("internal/redact depends on %s; it must stay a leaf", p)
		}
	}
}
