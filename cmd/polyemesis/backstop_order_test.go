package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// NOTHING SPAWNS AN OS CHILD BEFORE THE CRASH BACKSTOP EXISTS. #723.
//
// The hazard these two tests exist for is peculiar in that it has no symptom on
// the machines the code is written on. On Windows the backstop is a job object
// with KILL_ON_JOB_CLOSE, membership is INHERITED at CreateProcess and
// polyemesis never enrols a child afterwards, so a child created before the job
// exists is outside it for the rest of its life: it survives a crash of
// polyemesis still holding its ingest port and its RTMP connection, and the
// restarted server cannot rebind. On Unix
// EnsureCrashBackstop is a deliberate no-op, so getting the order wrong there
// compiles, runs, passes and reports nothing at all. Everything below is
// therefore checked in the SOURCE, because the runtime cannot be asked.
//
// Control -- the mistake made impossible -- is what backstop.go's init()
// achieves for the ordinary case: Go runs it before main's first statement, so
// no line inserted anywhere in main or run() can get in front of it. These
// tests cover the two ways that Control can still be lost, and both are edits
// rather than executions, which is why a source walk is the right instrument:
//
//   - somebody moves the call back into run(), where its position becomes a
//     thing to remember again;
//   - somebody spawns from a package init function or a package-level variable
//     initializer, both of which run BEFORE main's init when they live in an
//     imported package.
//
// Neither can be stopped by the compiler, so these are Warning rather than
// Control: they fire when the suite runs, not when the line is typed -- early
// enough to be a build failure on the branch that wrote it, and long before an
// operator finds an encoder on a host. Control would need the language to let a
// package say "no init in my import graph may call exec.Command", which nothing
// in Go can express.

// backstopCall is one call to supervisor.EnsureCrashBackstop, with the function
// that encloses it, so a failure can name both.
type backstopCall struct {
	site      string // file:line
	enclosing string // "init", "run", "main", or "<package-level>"
}

// findBackstopCalls parses this package's non-test sources and returns every
// call to EnsureCrashBackstop paired with the function it sits in.
//
// It reads the FILES rather than reflecting on the built package, because the
// property under test is about where the call is WRITTEN. A reflective check
// would see the backstop happen and be satisfied, which is exactly the mistake
// -- run() calling it late still "happens", just after the first child.
func findBackstopCalls(t *testing.T, dir string) []backstopCall {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	var found []backstopCall
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if perr != nil {
			t.Fatalf("parsing %s: %v", name, perr)
		}
		// The enclosing function is tracked by walking declarations rather than
		// the whole file, so the answer is a name and not a guess from line
		// numbers.
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			enclosing := "<package-level>"
			var body ast.Node = decl
			if ok {
				enclosing = fn.Name.Name
				if fn.Body == nil {
					continue
				}
				body = fn.Body
			}
			ast.Inspect(body, func(n ast.Node) bool {
				if !callsSelector(n, "supervisor", "EnsureCrashBackstop") {
					return true
				}
				found = append(found, backstopCall{
					site:      name + ":" + itoaLine(fset, n.Pos()),
					enclosing: enclosing,
				})
				return true
			})
		}
	}
	return found
}

// callsSelector reports whether n is a call of the form pkg.Fn(...).
func callsSelector(n ast.Node, pkg, fn string) bool {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == pkg && sel.Sel.Name == fn
}

func TestTheCrashBackstopIsEstablishedBeforeMainRuns(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	calls := findBackstopCalls(t, dir)

	// POSITIVE CONTROL, and it is not decoration. Every other assertion here is
	// of the form "each call is in an init", which a package containing NO
	// calls satisfies perfectly -- and a package with no call to
	// EnsureCrashBackstop is the worst version of this bug, not the best. So
	// finding nothing is a failure before anything else is asked.
	if len(calls) == 0 {
		t.Fatal("no call to supervisor.EnsureCrashBackstop anywhere in package main.\n\n" +
			"On Windows that means no job object, which means every FFmpeg, whisper " +
			"and ffprobe this process spawns survives a crash of polyemesis -- still " +
			"holding its ingest port and its RTMP connection, so the restarted server " +
			"cannot rebind. Unix will not tell you: EnsureCrashBackstop is a no-op " +
			"there and its absence has no local symptom at all.\n\n" +
			"It belongs in an init() in backstop.go.")
	}

	var misplaced []string
	inInit := 0
	for _, c := range calls {
		if c.enclosing == "init" {
			inInit++
			continue
		}
		misplaced = append(misplaced, c.site+" (in "+c.enclosing+")")
	}
	sort.Strings(misplaced)

	if len(misplaced) > 0 {
		t.Errorf("supervisor.EnsureCrashBackstop is called from somewhere other than "+
			"an init function:\n  %s\n\n"+
			"That is where it used to be, and the position was held in place by a "+
			"comment reading \"BEFORE THE FIRST CHILD OF ANY KIND\". Windows job "+
			"membership is inherited at CreateProcess and nothing enrols a child "+
			"afterwards: anything spawned above the call -- "+
			"an ffmpeg probe, a codec detection, a version check -- is outside the job "+
			"permanently and survives a crash holding its port. Nothing catches it "+
			"locally, because the Unix implementation is a no-op.\n\n"+
			"Leave the call in backstop.go's init(), which Go runs before main's first "+
			"statement, so the ordering is structural rather than remembered.",
			strings.Join(misplaced, "\n  "))
	}
	if inInit == 0 {
		t.Error("no init() in package main calls supervisor.EnsureCrashBackstop, so " +
			"the crash backstop's position relative to the first spawn is once again " +
			"a matter of where somebody happened to put a line")
	}
}

// THE ONE HOLE init() DOES NOT CLOSE. Go initialises imported packages before
// the importing one, and package-level variable initializers before init
// functions, so a spawn in either of those places in ANY package linked into
// this binary runs before backstop.go's init and lands outside the job.
//
// Nothing in the module does this today, and the point of the test is to keep
// that true, because the failure it prevents is invisible: a probe moved into a
// var initializer "to run it once" costs nothing on Unix and orphans a process
// on Windows.
func TestNothingSpawnsFromAPackageInitializer(t *testing.T) {
	root := moduleRoot(t)

	total, early := spawnSitesByInitTiming(t, root)

	// POSITIVE CONTROL FOR THE WALKER. "No spawn happens in an initializer" is
	// also what a walker that parses nothing reports, and this repository has
	// spawn sites in a dozen packages. If the count collapses, the walk is
	// broken and its green means nothing.
	//
	// THE FLOOR IS FIVE CALL SITES, and that is a far weaker bar than the number
	// suggests: this walk reports 58 of them as it is written. It is deliberately
	// not a tight count, because a tight one would be edited by every new spawn
	// site until somebody edited it down instead; five only has to catch a
	// walker that stopped walking. Note that internal/childcensus's five is five
	// PACKAGES, not five calls -- the same digit guarding a different quantity,
	// so do not read the two floors as equivalent.
	if total < 5 {
		t.Fatalf("the walk found only %d exec.Command call(s) under %s; this "+
			"repository has them in a dozen packages, so the walker is broken and "+
			"every assertion below is vacuous", total, root)
	}

	sort.Strings(early)
	if len(early) > 0 {
		t.Errorf("these OS children are spawned from a package initializer, which runs "+
			"before main's init:\n  %s\n\n"+
			"backstop.go establishes the Windows job object from an init() in package "+
			"main, and Go runs the initializers of imported packages -- and every "+
			"package-level variable initializer -- before it. A child spawned there is "+
			"created before the job exists; job membership is inherited at "+
			"CreateProcess and polyemesis enrols no child afterwards, so it is outside "+
			"the job for the whole of its life: it survives a crash of polyemesis "+
			"holding its ingest port and its RTMP connection open.\n\n"+
			"Move the spawn into a function called from run(), where the backstop is "+
			"already in place.", strings.Join(early, "\n  "))
	}
}

// spawnSitesByInitTiming walks the module and returns how many exec.Command
// calls it saw in total, and the sites of those that run during package
// initialization -- inside an init function, or inside a package-level variable
// initializer.
//
// IT IS A SECOND WALKER, AND THAT IS WORTH SAYING. internal/childcensus has
// walkSpawners, shared between its two guards precisely so they cannot disagree
// about what a spawn site is. This cannot use it: those helpers are unexported
// test code in another package, and Go offers no way to import them. So the
// match is kept deliberately identical -- a selector call on `exec` whose name
// starts with "Command" -- and the two tests ask different questions of the same
// syntax. childcensus asks WHICH packages spawn; this asks WHEN in the
// program's lifetime they do it.
func spawnSitesByInitTiming(t *testing.T, root string) (total int, early []string) {
	t.Helper()
	fset := token.NewFileSet()

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Every dot-directory, not just .git: .claude/worktrees holds
			// checkouts of this same repository, and walking them reports sites
			// under a path nobody can act on. childcensus's walker skips them
			// for the same reason.
			if strings.HasPrefix(info.Name(), ".") && info.Name() != "." {
				return filepath.SkipDir
			}
			switch info.Name() {
			case "node_modules", "web", "ui", "dist", "vendor", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil // the build is what fails on unparseable Go, not this
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)

		// ONE PASS OVER THE TOP-LEVEL DECLARATIONS, so every spawn site is
		// counted exactly once and its timing is decided by which declaration
		// encloses it rather than by a second, differently-shaped walk. Every
		// function body is descended into, because the total is the positive
		// control and a total that only saw init functions would be a floor of
		// nearly zero on a healthy repository.
		for _, decl := range f.Decls {
			runsAtInit := false
			var body ast.Node
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Body == nil {
					continue // an assembly or linkname stub has nothing to read
				}
				body = d.Body
				runsAtInit = d.Recv == nil && d.Name.Name == "init"
			case *ast.GenDecl:
				// Only `var`. A const cannot call anything, and imports and
				// type declarations have no call sites to find.
				if d.Tok != token.VAR {
					continue
				}
				body = d
				runsAtInit = true
			default:
				continue
			}
			ast.Inspect(body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				ident, ok := sel.X.(*ast.Ident)
				if !ok || ident.Name != "exec" || !strings.HasPrefix(sel.Sel.Name, "Command") {
					return true
				}
				total++
				if runsAtInit {
					early = append(early, rel+":"+itoaLine(fset, call.Pos()))
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}
	return total, early
}

func itoaLine(fset *token.FileSet, p token.Pos) string {
	line := fset.Position(p).Line
	if line == 0 {
		return "0"
	}
	var b []byte
	for line > 0 {
		b = append([]byte{byte('0' + line%10)}, b...)
		line /= 10
	}
	return string(b)
}

// moduleRoot walks up from the test's working directory to the directory
// holding go.mod, so this runs the same from `go test ./...` and from an IDE.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not find go.mod above the test's working directory")
	return ""
}
