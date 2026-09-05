package childcensus

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

// EVERY SPAWN SITE IS EITHER ENROLLED OR EXPLAINED. #717.
//
// The census was added with unexported enrol/discharge inside internal/
// supervisor, so exactly one of the roughly twenty spawn sites in this
// repository could use it. Its own comment framed it as "WHAT HAVE WE ACTUALLY
// SPAWNED?" and the shutdown report said "it would have said it on the first
// occurrence of #631" -- both true only for supervisor children. A transcode or
// a whisper child surviving shutdown produced exactly the silence #631 produced,
// while the shutdown log actively reported that nothing was wrong.
//
// A DETECTION DEVICE THAT UNDER-REPORTS IS WORSE THAN NONE, because its green is
// read as an all-clear. So the scope is no longer a thing somebody remembers:
// every package that calls exec.Command or exec.CommandContext is either a
// package that enrols, or a package on the list below WITH A REASON.
//
// Control is not available -- nothing in Go stops a package calling
// exec.Command -- so this is Warning, at the earliest moment it can be raised:
// when the code is written, not when a child is found on a host.
//
// A new spawner fails this test until somebody decides which it is. That is the
// whole device: the decision is forced, and it is recorded where the next reader
// will find it.
var spawnersThatNeedNoCensus = map[string]string{
	"internal/ffmpeg": "capability probes and duration counts: every one collects its " +
		"output and returns, is bounded by a context deadline with WaitDelay set, and " +
		"cannot outlive the call that made it, let alone the process",
	"internal/clipper": "one ffprobe keyframe scan, output-collected, killed with its " +
		"context; the clip ENCODE is a supervisor child and is enrolled there",
	"internal/playlistmedia": "the probe helper only; every encode in this package goes " +
		"through media.Exec, which enrols",
	"internal/recording": "a single ffprobe for a finished file's duration, output-collected",
	"internal/api": "two short output-collected runs -- the playout poster frame and the " +
		"expert dry-run, the latter bounded by both a context deadline and WaitDelay. " +
		"Neither can survive the handler, so neither can survive the shutdown",
	"internal/testenv": "test scaffolding: netstat and lsof, to find a free UDP port",
	"scripts/cmd/gotest": "a build-time wrapper that shells out to `go test`; it is not " +
		"part of the server binary and its child is the test run itself",
	"scripts": "the acceptance drivers, which are built and run by the suites in " +
		"scripts/ and never linked into the server binary; their children die with " +
		"the driver process and there is no shutdown report for them to be missing from",
	"internal/supervisor": "enrols through childcensus at spawn and discharges at reap; " +
		"listed here because its exec.Command call is in the same function as the Enrol " +
		"and the walker below matches on package, not on line",
}

// A REASON THAT SAYS NOTHING IS NOT A REASON. The failure mode this guards
// against is somebody silencing the test with "n/a" and moving on, which puts
// the scope back where it was: a thing nobody can see.
const minReasonLen = 40

func TestEverySpawnSiteIsAccountedFor(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()

	spawners := map[string][]string{} // package dir -> call sites
	enrollers := map[string]bool{}    // package dir -> calls childcensus.Enrol

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// EVERY DOT-DIRECTORY, not just .git. .claude/worktrees holds
			// checkouts of this same repository, and walking them made the
			// report list every spawn site three times under a path nobody can
			// act on -- and would have kept a stale copy failing this test long
			// after the branch it came from was merged.
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
			return nil // not our business to fail on unparseable Go; the build does that
		}
		rel, _ := filepath.Rel(root, path)
		pkg := filepath.ToSlash(filepath.Dir(rel))

		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			switch {
			case ident.Name == "exec" && strings.HasPrefix(sel.Sel.Name, "Command"):
				spawners[pkg] = append(spawners[pkg],
					filepath.ToSlash(rel)+":"+fsetLine(fset, call.Pos()))
			case ident.Name == "childcensus" && sel.Sel.Name == "Enrol":
				enrollers[pkg] = true
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}

	// THE WALKER MUST ACTUALLY FIND THINGS. A test that walks nothing passes
	// every assertion below and reports a scope it never checked, which is the
	// same failure as the census it is here to bound.
	if len(spawners) < 5 {
		t.Fatalf("only found spawn sites in %d package(s) under %s; the walker is "+
			"broken and this test is asserting nothing", len(spawners), root)
	}
	if !enrollers["internal/supervisor"] && !enrollers["internal/media"] {
		t.Fatal("found no package calling childcensus.Enrol; the walker cannot tell " +
			"an enrolling package from a silent one, so its verdict is worthless")
	}

	var unaccounted []string
	for pkg, sites := range spawners {
		if pkg == "internal/childcensus" || enrollers[pkg] {
			continue
		}
		if _, excused := spawnersThatNeedNoCensus[pkg]; excused {
			continue
		}
		unaccounted = append(unaccounted, pkg+" ("+strings.Join(sites, ", ")+")")
	}
	sort.Strings(unaccounted)
	if len(unaccounted) > 0 {
		t.Errorf("these packages spawn OS children, do not enrol them in the census, "+
			"and give no reason:\n  %s\n\n"+
			"A child nobody enrolled is invisible from inside the program: absent from "+
			"every map, every status page and every log line, and present only in the "+
			"process table. That is the exact shape of #631, which took three weeks and "+
			"53 escalations to notice.\n\n"+
			"Either call childcensus.Enrol after Start and Discharge after Wait, or add "+
			"the package to spawnersThatNeedNoCensus with a reason saying why its child "+
			"cannot outlive the call.", strings.Join(unaccounted, "\n  "))
	}

	// AND THE LIST MUST NOT ROT. An entry for a package that no longer spawns
	// anything is a standing excuse nobody re-earns.
	for pkg := range spawnersThatNeedNoCensus {
		if len(spawners[pkg]) == 0 {
			t.Errorf("spawnersThatNeedNoCensus lists %q, which no longer calls "+
				"exec.Command. Remove the entry rather than leaving an excuse in "+
				"place for whatever is written there next.", pkg)
		}
	}
}

// The reasons have to be reasons, or the list becomes a way to silence the test.
func TestTheCensusExcusesExplainThemselves(t *testing.T) {
	for pkg, why := range spawnersThatNeedNoCensus {
		if len(why) < minReasonLen {
			t.Errorf("%s is excused from the census with %q (%d chars). Say what "+
				"bounds the child's life -- an output-collected run, a context "+
				"deadline, a WaitDelay -- because the next reader has to decide "+
				"whether it is still true.", pkg, why, len(why))
		}
		if strings.Contains(strings.ToLower(why), "tbd") || strings.Contains(why, "TODO") {
			t.Errorf("%s: %q is a placeholder, not a reason", pkg, why)
		}
	}
}

func fsetLine(fset *token.FileSet, p token.Pos) string {
	return itoa(fset.Position(p).Line)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// repoRoot walks up from the test's working directory to the module root, so
// this runs the same from `go test ./...` and from an IDE.
func repoRoot(t *testing.T) string {
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
