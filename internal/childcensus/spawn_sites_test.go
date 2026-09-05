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

// ---------------------------------------------------------------- #723

// spawnPolicy records, per package, WHETHER ITS CHILDREN GET A PROCESS GROUP OF
// THEIR OWN -- and why. It is the second decision every spawn site makes and
// the one nothing stated.
//
// THE TRADE, IN BOTH DIRECTIONS. internal/supervisor calls setProcessGroup, and
// proc_unix.go is unusually careful about what that buys and costs:
//
//   - A new group is what lets a stop signal reach FFmpeg's whole tree, and
//     what keeps a terminal's Ctrl-C from killing a child mid-write. That is
//     the difference between a finalised recording and a truncated one.
//   - A new group is ALSO what detaches the child from an abrupt death of
//     polyemesis. #448 found thirteen orphaned encoders on a shared host, the
//     oldest running five and a half days, each still holding a relay port
//     whose owner no longer existed.
//
// So it is genuinely a decision, and the two families land on opposite sides:
//
//   - SUPERVISED children get their own group. They are long-lived, they are
//     stopped in order by code that holds their handle, and losing that
//     ordering costs a truncated recording every time.
//   - EVERYTHING ELSE stays in this process's group. A transcode, a whisper
//     worker or a probe has no such teardown protocol, so the property worth
//     having is the opposite one: an abrupt death of polyemesis -- Ctrl-C, the
//     OOM killer, a cancelled CI job -- takes them with it, and there is no
//     #448 orphan to find three weeks later.
//
// Windows reaches the same place by a different mechanism and it is NOT the
// process group: the job object with KILL_ON_JOB_CLOSE is inherited by every
// child of this process, so it covers both families unconditionally. See
// supervisor.EnsureCrashBackstop, which is called from main precisely so that
// coverage does not depend on a supervised child having spawned first.
//
// Every package that spawns says which side it is on. A new spawner fails until
// somebody decides -- which is the point, because the answer is not obvious and
// the wrong one is invisible until a host is found with encoders on it.
type groupPolicy string

const (
	ownGroup    groupPolicy = "own process group"
	parentGroup groupPolicy = "stays in polyemesis's group"
)

var spawnGroupPolicy = map[string]struct {
	policy groupPolicy
	why    string
}{
	"internal/supervisor": {ownGroup,
		"long-lived children stopped in order by code that holds their handle: the new " +
			"group is what makes a stop signal reach FFmpeg's whole tree and what keeps a " +
			"terminal Ctrl-C from truncating a recording mid-write. The crash-orphan cost " +
			"is real and is covered by systemd KillMode=mixed on Unix and by the job " +
			"object on Windows"},
	"internal/media": {parentGroup,
		"a transcode has no teardown protocol worth protecting, so the property worth " +
			"having is that an abrupt death of polyemesis takes it too rather than leaving " +
			"a #448 orphan holding CPU for days"},
	"internal/transcribe": {parentGroup,
		"same as media: the whisper and extract workers are killed by their context and " +
			"have nothing to finalise, so staying in this group is what makes a Ctrl-C or " +
			"an OOM kill reach them"},
	"internal/clipper": {parentGroup,
		"an output-collected ffprobe that cannot outlive the call; a group of its own " +
			"would buy nothing and cost the abrupt-death guarantee"},
	"internal/playlistmedia": {parentGroup,
		"the probe helper only; every encode in this package goes through media.Exec, " +
			"which is listed above"},
	"internal/recording": {parentGroup,
		"a single ffprobe for a finished file's duration, output-collected"},
	"internal/ffmpeg": {parentGroup,
		"capability probes and duration counts, all bounded by a context deadline with " +
			"WaitDelay set; none can outlive the call that made it"},
	"internal/api": {parentGroup,
		"the playout poster frame and the expert dry-run, both short and output-collected, " +
			"and both bounded by the request they serve"},
	"internal/testenv": {parentGroup,
		"test scaffolding: netstat and lsof, which exit on their own within milliseconds"},
	"scripts": {parentGroup,
		"acceptance drivers, which are their own process tree and are not linked into the " +
			"server binary at all"},
	"scripts/cmd/gotest": {parentGroup,
		"a build-time wrapper around `go test`; its child is the test run itself and it " +
			"must die with the wrapper"},
}

// walkSpawners is the one walk both guards share: it returns every package that
// calls exec.Command, with its call sites, and every package that enrols.
//
// SHARED SO THE TWO GUARDS CANNOT DISAGREE ABOUT WHAT A SPAWNER IS. #723 asks a
// second question of the same set #717 asks the first of, and two walkers would
// be two definitions of "spawn site" that drift apart silently.
func walkSpawners(t *testing.T, root string) (spawners map[string][]string, enrollers map[string]bool) {
	t.Helper()
	fset := token.NewFileSet()
	spawners = map[string][]string{}
	enrollers = map[string]bool{}

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
	return spawners, enrollers
}

// packagesThatSpawn is walkSpawners' first half, sorted, for a caller that does
// not care where in the file the call is.
func packagesThatSpawn(t *testing.T, root string) []string {
	t.Helper()
	spawners, _ := walkSpawners(t, root)
	out := make([]string, 0, len(spawners))
	for pkg := range spawners {
		out = append(out, pkg)
	}
	sort.Strings(out)
	return out
}

func TestEverySpawnSiteIsAccountedFor(t *testing.T) {
	root := repoRoot(t)
	spawners, enrollers := walkSpawners(t, root)

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

// EVERY SPAWNER STATES WHETHER ITS CHILDREN GET THEIR OWN PROCESS GROUP. #723.
//
// This is the same device as the census above, applied to the second decision a
// spawn site makes and the one nothing recorded. The asymmetry was invisible:
// internal/supervisor calls setProcessGroup and nothing else does, and no
// comment anywhere said whether that was deliberate.
//
// It is deliberate, and the reasoning runs in both directions -- see
// spawnGroupPolicy. What this refuses is a spawner that has not been thought
// about.
func TestEverySpawnerStatesItsProcessGroupPolicy(t *testing.T) {
	root := repoRoot(t)
	pkgs := packagesThatSpawn(t, root)

	if len(pkgs) < 5 {
		t.Fatalf("only %d spawning package(s) found; the walker is broken and this "+
			"test is asserting nothing", len(pkgs))
	}

	var undecided []string
	for _, pkg := range pkgs {
		if pkg == "internal/childcensus" {
			continue
		}
		if _, ok := spawnGroupPolicy[pkg]; !ok {
			undecided = append(undecided, pkg)
		}
	}
	sort.Strings(undecided)
	if len(undecided) > 0 {
		t.Errorf("these packages spawn OS children and do not say whether those children "+
			"get a process group of their own:\n  %s\n\n"+
			"It is not a formality. A new group lets a stop signal reach FFmpeg's whole "+
			"tree and keeps a terminal Ctrl-C from truncating a recording -- and it is "+
			"ALSO what detached thirteen encoders from a dying polyemesis in #448, the "+
			"oldest still running five and a half days later. Add an entry to "+
			"spawnGroupPolicy saying which side this spawner is on and why.",
			strings.Join(undecided, "\n  "))
	}

	// AND THE LIST MUST NOT ROT, for the same reason the census excuses must not.
	for pkg := range spawnGroupPolicy {
		found := false
		for _, p := range pkgs {
			if p == pkg {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("spawnGroupPolicy lists %q, which no longer spawns anything. Remove "+
				"the entry rather than leaving a decision standing for whatever is "+
				"written there next.", pkg)
		}
	}
}

// The stated policies have to be arguable, and they have to match the code.
func TestTheProcessGroupPoliciesAreArguableAndTrue(t *testing.T) {
	root := repoRoot(t)

	for pkg, p := range spawnGroupPolicy {
		if len(p.why) < 60 {
			t.Errorf("%s's process-group reason is too short to argue with: %q", pkg, p.why)
		}
		if strings.Contains(strings.ToLower(p.why), "tbd") || strings.Contains(p.why, "TODO") {
			t.Errorf("%s: %q is a placeholder, not a reason", pkg, p.why)
		}
	}

	// THE CLAIM IS CHECKED AGAINST THE CODE, not just spelled. setProcessGroup
	// is the thing that creates a group, so "own group" must mean the package
	// calls it and "stays in polyemesis's group" must mean it does not.
	// Otherwise this table is a second place to be wrong.
	for pkg, p := range spawnGroupPolicy {
		calls := packageCalls(t, filepath.Join(root, pkg), "setProcessGroup")
		switch p.policy {
		case ownGroup:
			if !calls {
				t.Errorf("%s claims %q but never calls setProcessGroup", pkg, p.policy)
			}
		case parentGroup:
			if calls {
				t.Errorf("%s claims %q and calls setProcessGroup, so one of the two is "+
					"wrong. A child in its own group survives an abrupt death of "+
					"polyemesis -- which is the outcome this entry says it does not want.",
					pkg, p.policy)
			}
		}
	}
}

// packageCalls reports whether any non-test file directly in dir calls fn.
func packageCalls(t *testing.T, dir, fn string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		// The DECLARATION is not a call, and both live in this package on the
		// two platform files, so a bare name match would report every build.
		for _, line := range strings.Split(string(b), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "func ") || strings.HasPrefix(trimmed, "//") {
				continue
			}
			if strings.Contains(trimmed, fn+"(") {
				return true
			}
		}
	}
	return false
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
