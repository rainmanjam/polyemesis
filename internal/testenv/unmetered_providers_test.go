package testenv

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// PRODUCTION CODE NEVER BUILDS AN UNMETERED PROVIDER. oauth.Get, Providers and
// ProvidersWith build providers with no YouTube quota meter, so a call made
// through one is spent from the shared allowance without being counted. The
// server resolves providers through the metered oauth.Set that api.New builds.
func TestProductionCodeDoesNotUseTheUnmeteredPackageProviders(t *testing.T) {
	root := repoRootFromTest(t)
	banned := map[string]bool{"Get": true, "Providers": true, "ProvidersWith": true, "NewYouTube": true}
	fset := token.NewFileSet()
	scanned := 0
	for _, top := range []string{"internal", "cmd"} {
		_ = filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "internal/oauth/") {
				return nil
			}
			f, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				return nil
			}
			scanned++
			ast.Inspect(f, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "oauth" && banned[sel.Sel.Name] {
					t.Errorf("%s uses oauth.%s, which builds providers with no quota meter", rel, sel.Sel.Name)
				}
				return true
			})
			return nil
		})
	}
	if scanned < 50 {
		t.Fatalf("scanned %d files; the guard checked nothing", scanned)
	}
}
