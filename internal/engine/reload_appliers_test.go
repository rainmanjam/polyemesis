package engine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// The package directories a qualified applier name can point into.
var applierDirs = map[string]string{
	"api":   filepath.Join("..", "api"),
	"logtz": filepath.Join("..", "logtz"),
	"main":  filepath.Join("..", "..", "cmd", "polyemesis"),
}

// parsePkg parses the non-test files of one package directory.
func parsePkg(t *testing.T, dir string) []*ast.File {
	t.Helper()
	pkgs, err := parser.ParseDir(token.NewFileSet(), dir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", dir, err)
	}
	var files []*ast.File
	for _, p := range pkgs {
		for _, f := range p.Files {
			files = append(files, f)
		}
	}
	return files
}

// recvName is the receiver's type name, without a pointer star.
func recvName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return ""
	}
	t := fd.Recv.List[0].Type
	if st, ok := t.(*ast.StarExpr); ok {
		t = st.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// declared reports whether the package declares Func or Recv.Method exactly.
func declared(files []*ast.File, name string) bool {
	for _, f := range files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			got := fd.Name.Name
			if r := recvName(fd); r != "" {
				got = r + "." + got
			}
			if got == name {
				return true
			}
		}
	}
	return false
}

// calledBy is every "pkg.Func" (or bare "Func", qualified with own) that the
// named method's body calls.
func calledBy(files []*ast.File, recv, method, own string) map[string]bool {
	out := map[string]bool{}
	for _, f := range files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Name.Name != method || recvName(fd) != recv || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fn := call.Fun.(type) {
				case *ast.Ident:
					out[own+"."+fn.Name] = true
				case *ast.SelectorExpr:
					if x, ok := fn.X.(*ast.Ident); ok {
						out[x.Name+"."+fn.Sel.Name] = true
					}
				}
				return true
			})
		}
	}
	return out
}

// A rule that names a package-qualified applier is a claim about exactly one
// function. Every such name must be declared where it says, and the ones api's
// ApplySettings is responsible for (the "api." and "logtz." names) must be
// exactly the ones it calls -- a rename, or an applier added to ApplySettings
// without a rule, fails here.
func TestQualifiedReloadAppliersAreExactAndCalled(t *testing.T) {
	pkgs := map[string][]*ast.File{}
	for name, dir := range applierDirs {
		pkgs[name] = parsePkg(t, dir)
	}

	named := map[string]bool{}
	for path, rule := range settingsReload {
		pkg, rest, ok := strings.Cut(rule.Applies, ".")
		if !ok {
			continue
		}
		files, known := pkgs[pkg]
		if !known {
			t.Errorf("settingsReload[%q] names %q in package %q, which this test does not know", path, rule.Applies, pkg)
			continue
		}
		if !declared(files, rest) {
			t.Errorf("settingsReload[%q] names %q, which package %s does not declare", path, rule.Applies, pkg)
		}
		if pkg != "main" {
			named[rule.Applies] = true
		}
	}

	called := calledBy(pkgs["api"], "Server", "ApplySettings", "api")
	for name := range named {
		if !called[name] {
			t.Errorf("a rule names %s as an applier, but Server.ApplySettings never calls it", name)
		}
	}
	for name := range called {
		if (strings.HasPrefix(name, "api.Apply") || name == "logtz.Set") && !named[name] {
			t.Errorf("Server.ApplySettings calls %s, which no settingsReload rule names", name)
		}
	}
	if len(named) == 0 {
		t.Fatal("no qualified applier found, so this test checked nothing")
	}
}
