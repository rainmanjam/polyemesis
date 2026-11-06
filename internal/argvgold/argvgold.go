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
	"strconv"
	"strings"
	"testing"
)

// Check compares argv with testdata/argv/<name>.txt in the calling package.
func Check(t testing.TB, name string, argv []string) {
	t.Helper()
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
