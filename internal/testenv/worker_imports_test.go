package testenv

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THE BACKGROUND JOB TIER DOES NOT DEPEND ON THE HTTP LAYER. The job workers
// are wired in cmd/polyemesis beside the API server, and used to take their
// transcript source and highlight model connector from internal/api, which made
// "a worker" and "a handler" one dependency. Anything both need lives in a
// package below both.
func TestJobWorkersDoNotImportTheHTTPPackage(t *testing.T) {
	root := repoRootFromTest(t)
	workers := []string{"clipper", "highlights", "media", "transcribe", "publish",
		"playlistmedia", "uploadverify", "jobs"}
	const api = `"github.com/rainmanjam/polyemesis/internal/api"`
	fset := token.NewFileSet()
	for _, w := range workers {
		dir := filepath.Join(root, "internal", w)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("%s: %v", w, err)
		}
		checked := 0
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("%s/%s: %v", w, e.Name(), err)
			}
			checked++
			for _, imp := range f.Imports {
				if imp.Path.Value == api {
					t.Errorf("internal/%s/%s imports internal/api", w, e.Name())
				}
			}
		}
		if checked == 0 {
			t.Errorf("internal/%s has no Go files; the guard would check nothing", w)
		}
	}
}
