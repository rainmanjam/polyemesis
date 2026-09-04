package testenv_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/rainmanjam/polyemesis/internal/testenv"
)

// THE SKIP RATCHET. #161's third piece, and the only one with jurisdiction over
// code nobody has looked at.
//
// A per-site registry would have been the obvious design and is the wrong one:
// it needs an entry for each of the ~95 existing sites, most of which are honest
// environmental skips, and a list that large rots into a rubber stamp. What this
// does instead is COUNT, per package, against a committed file. Nothing about
// the existing skips has to be judged for the count to hold, and no NEW bare
// t.Skip can land anywhere without the number going up and the test failing.
//
// It is a syntactic count and it is honest about that: it cannot tell an
// environmental skip from a self-silencing one. C measured about 50% precision
// on a classifier that tried, which is why the classifier is not here -- a gate
// with 50% precision grows an override list, and an override list is the seventh
// free pass this repository has invented.
//
// THE LIMIT, stated rather than papered over: this is a TEST, so it does not run
// under `go test -run X ./some/other/package`. It is a merge gate. Only
// internal/api has run-level jurisdiction, via its preflight.
var updateSkips = flag.Bool("update-skips", false,
	"rewrite testdata/skips.json from a fresh AST walk. It can only be used to record "+
		"a count that has FALLEN; the ratchet below runs first either way.")

const skipsPath = "testdata/skips.json"

type skipCensus struct {
	Note string `json:"note"`
	// Total is the ratchet. Per-package counts are recorded too, so a failure
	// names the package rather than only the number.
	Total    int            `json:"total"`
	ByImport map[string]int `json:"byPackage"`
}

// walkSkips counts calls to t.Skip, t.Skipf, t.SkipNow and testing.Short()
// across the repository, by package directory, excluding internal/testenv --
// which is the one place a skip is allowed to live, and whose own Quarantine
// call would otherwise be counted as the thing it replaces.
func walkSkips(t *testing.T, root string) (map[string]int, map[string]int, []string) {
	t.Helper()
	skips := map[string]int{}
	shorts := map[string]int{}
	var quarantineIDs []string

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			// TOP-LEVEL ui/ and web/ only. Matching on the base NAME excluded
			// internal/web as well, and internal/web is where two of the four
			// quarantined tests live -- so the first version of this counter
			// was not counting the sites it was written for. Caught by
			// comparing the census against origin/main and finding the
			// difference two short of the conversions actually made.
			switch rel {
			case ".git", "node_modules", "ui", "web":
				return filepath.SkipDir
			// .claude/worktrees holds FULL CHECKOUTS OF THIS MODULE, one per
			// agent branch in flight. Counting them multiplies the census by
			// the number of worktrees on the machine and reports every package
			// in each as a brand-new one at "committed count is 0" -- a
			// failure about the developer's working tree that no commit can
			// fix and no reviewer can reproduce. Every guard here that walks
			// the repo has to skip it for the same reason.
			case ".claude":
				return filepath.SkipDir
			}
			if info.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		pkg := filepath.ToSlash(filepath.Dir(rel))
		if pkg == "internal/testenv" {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Errorf("parse %s: %v", rel, err)
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			recv, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			switch {
			case sel.Sel.Name == "Skip" || sel.Sel.Name == "Skipf" || sel.Sel.Name == "SkipNow":
				skips[pkg]++
			case sel.Sel.Name == "Short" && recv.Name == "testing":
				shorts[pkg]++
			case sel.Sel.Name == "Quarantine" && recv.Name == "testenv":
				if len(call.Args) == 2 {
					if lit, ok := call.Args[1].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						id, _ := strconv.Unquote(lit.Value)
						quarantineIDs = append(quarantineIDs, id)
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(quarantineIDs)
	return skips, shorts, quarantineIDs
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return root
}

// TestNoNewBareSkipCanLand is the ratchet.
func TestNoNewBareSkipCanLand(t *testing.T) {
	root := repoRoot(t)
	skips, shorts, _ := walkSkips(t, root)

	total := 0
	for _, n := range skips {
		total += n
	}
	shortTotal := 0
	for _, n := range shorts {
		shortTotal += n
	}
	t.Logf("skip census: %d t.Skip/Skipf/SkipNow sites and %d testing.Short() sites "+
		"outside internal/testenv, across %d packages", total, shortTotal, len(skips))

	if *updateSkips {
		writeSkipCensus(t, skips, total)
		return
	}
	b, err := os.ReadFile(skipsPath)
	if err != nil {
		t.Fatalf("read %s: %v\nRun `go test ./internal/testenv -run TestNoNewBareSkipCanLand "+
			"-update-skips` to create it.", skipsPath, err)
	}
	var want skipCensus
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatalf("parse %s: %v", skipsPath, err)
	}

	if total > want.Total {
		t.Errorf("%d bare skip sites, and the committed count is %d. A skip is the same "+
			"free pass the route coverage ledger spent a whole round removing, in a "+
			"different shape: a test that declines to run prints ok and is counted as "+
			"coverage. If the new site is environmental, say so in the message and "+
			"regenerate with -update-skips AND raise the number here by hand, which is "+
			"the reviewable act. If it fires because the thing under test CHANGED, it "+
			"is not a skip at all -- it is a failure, or a testenv.Quarantine entry.",
			total, want.Total)
	}
	pkgs := make([]string, 0, len(skips))
	for pkg := range skips {
		pkgs = append(pkgs, pkg)
	}
	sort.Strings(pkgs)
	for _, pkg := range pkgs {
		if prev, ok := want.ByImport[pkg]; !ok || skips[pkg] > prev {
			t.Errorf("%s has %d skip sites and the committed count is %d. Per-package so "+
				"the failure names where, rather than only that the total moved.",
				pkg, skips[pkg], prev)
		}
	}
}

// censusKeysOwnedByTheWriter are the only two the regenerator computes. Every
// other key in the file belongs to whoever hand-edited it, and is carried
// through untouched -- see nextCensusFile.
var censusKeysOwnedByTheWriter = map[string]bool{"total": true, "byPackage": true}

// nextCensusFile rewrites only the keys the regenerator owns and preserves
// everything else, in its original order.
//
// #697. This used to unmarshal into skipCensus, build a FRESH one, and marshal
// that -- so every key the struct does not declare was silently dropped. The
// file records its own history in seven of them (raisedBy, loweredBy,
// raisedBy2, raisedBy3, loweredBy2, raisedBy4, raisedBy5 -- the numbering is
// what happens when a hand edit has nowhere obvious to append), and the ratchet
// message tells an author to run -update-skips. Following that instruction
// deleted 142 of the file's 166 lines: every justification for every previous
// raise, on the one file whose entire purpose is to make raises reviewable.
//
// It does NOT know the history schema, deliberately. "Preserve every key I do
// not own" is the rule that stays correct when the eighth history key appears,
// and a fix that carried `raisedBy` by name would have dropped the other six.
//
// Order is preserved because this file is read in diffs. A map would reorder it
// on every regeneration and bury the two lines that actually changed.
func nextCensusFile(prevRaw []byte, note string, total int, byPackage map[string]int) ([]byte, error) {
	fields := map[string]json.RawMessage{}
	var order []string
	if len(prevRaw) > 0 {
		if err := json.Unmarshal(prevRaw, &fields); err != nil {
			return nil, fmt.Errorf("parse the existing census: %w", err)
		}
		// encoding/json gives no key order, so it is read back off the token
		// stream. A file this is asked to rewrite has been hand-edited, and
		// reordering somebody's hand edit is its own small betrayal.
		dec := json.NewDecoder(bytes.NewReader(prevRaw))
		if _, err := dec.Token(); err != nil { // opening brace
			return nil, err
		}
		for dec.More() {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, ok := tok.(string)
			if !ok {
				return nil, fmt.Errorf("census key is %T, not a string", tok)
			}
			order = append(order, key)
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, err
			}
		}
	}

	set := func(k string, v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if _, seen := fields[k]; !seen {
			order = append(order, k)
		}
		fields[k] = b
		return nil
	}
	if _, seen := fields["note"]; !seen {
		if err := set("note", note); err != nil {
			return nil, err
		}
	}
	if err := set("total", total); err != nil {
		return nil, err
	}
	if err := set("byPackage", byPackage); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	buf.WriteString("{\n")
	for i, k := range order {
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, fields[k], "  ", "  "); err != nil {
			return nil, err
		}
		fmt.Fprintf(&buf, "  %s: %s", kb, pretty.String())
		if i < len(order)-1 {
			buf.WriteString(",")
		}
		buf.WriteString("\n")
	}
	buf.WriteString("}")
	return buf.Bytes(), nil
}

func writeSkipCensus(t *testing.T, skips map[string]int, total int) {
	t.Helper()
	prev := skipCensus{Total: 1 << 30, ByImport: map[string]int{}}
	prevRaw, readErr := os.ReadFile(skipsPath)
	if readErr == nil {
		_ = json.Unmarshal(prevRaw, &prev)
	} else {
		prevRaw = nil
	}
	byPkg := map[string]int{}
	for pkg, n := range skips {
		if p, ok := prev.ByImport[pkg]; ok {
			n = min(n, p)
		}
		byPkg[pkg] = n
	}
	b, err := nextCensusFile(prevRaw,
		"t.Skip/Skipf/SkipNow call sites per package, outside internal/testenv. "+
			"A COUNT rather than a per-site registry: a registry needs an entry for "+
			"every one of these, most of which are honest environmental skips, and a "+
			"list that long becomes a rubber stamp. This can only fall on regeneration; "+
			"raising it is a hand edit of this file.",
		min(total, prev.Total), byPkg)
	if err != nil {
		t.Fatalf("build census: %v", err)
	}
	out := skipCensus{Total: min(total, prev.Total)}
	if err := os.MkdirAll(filepath.Dir(skipsPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(skipsPath, append(b, '\n'), 0o644); err != nil {
		t.Fatalf("write %s: %v", skipsPath, err)
	}
	t.Logf("regenerated %s: %d sites", skipsPath, out.Total)
}

// TestEveryQuarantineIsLiveAndEnumerated prints the register by name on every
// run and reconciles it against the call sites.
//
// Both directions matter. A registered id nobody calls is a row that rots; a
// call site whose id is not registered already fails inside Quarantine itself.
func TestEveryQuarantineIsLiveAndEnumerated(t *testing.T) {
	entries, ceiling, err := testenv.Entries()
	if err != nil {
		t.Fatalf("read the quarantine register: %v", err)
	}
	if len(entries) > ceiling {
		t.Errorf("%d quarantined tests and the committed ceiling is %d. The target is "+
			"zero; raising this is a hand edit of %s.",
			len(entries), ceiling, testenv.QuarantinePath)
	}
	_, _, called := walkSkips(t, repoRoot(t))
	live := map[string]bool{}
	for _, id := range called {
		live[id] = true
	}
	for _, e := range entries {
		// BY NAME, on every run. A quarantine nobody sees is the skip it
		// replaced with extra steps.
		t.Logf("QUARANTINED %s (%s)\n    what it would assert: %s\n    to un-silence: %s",
			e.ID, e.Site, e.Why, e.WhatItWouldTake)
		if e.WhatItWouldTake == "" {
			t.Errorf("the quarantine %s says nothing about what it would take to "+
				"un-silence it, so nobody can ever act on it.", e.ID)
		}
		if !live[e.ID] {
			t.Errorf("the quarantine %s is registered and no test calls "+
				"testenv.Quarantine with it. Either the test was fixed -- delete the "+
				"row, the ceiling comes down with it -- or it was deleted, in which "+
				"case say so.", e.ID)
		}
	}
}

// #697. The ratchet's own failure message tells an author to regenerate with
// -update-skips. Following that instruction used to delete 142 of this file's
// 166 lines: every justification for every previous raise, on the one file
// whose entire purpose is to make raises reviewable.
//
// The cause was that writeSkipCensus unmarshalled into skipCensus, built a
// FRESH one and marshalled that, so every key the struct does not declare was
// dropped. The file carries its history in seven of them -- raisedBy,
// loweredBy, raisedBy2, raisedBy3, loweredBy2, raisedBy4, raisedBy5, the
// numbering being what happens when a hand edit has nowhere obvious to append.
//
// So the property under test is deliberately NOT "raisedBy survives". A fix
// that carried that one key by name would have dropped the other six and
// passed a test written about it. The property is that the regenerator
// preserves every key it does not own.
func TestRegeneratingTheCensusKeepsEveryKeyItDoesNotOwn(t *testing.T) {
	before := []byte(`{
  "note": "the note",
  "raisedBy": [
    {"pr": 118, "why": "environmental, and the reason is the whole point"}
  ],
  "loweredBy2": ["something a hand edit invented"],
  "total": 100,
  "byPackage": {
    "internal/api": 4
  },
  "raisedBy5": {"pr": 696, "why": "a later hand edit, after the owned keys"}
}`)

	got, err := nextCensusFile(before, "a fresh note", 97, map[string]int{"internal/api": 3})
	if err != nil {
		t.Fatalf("nextCensusFile: %v", err)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(got, &fields); err != nil {
		t.Fatalf("the regenerated census is not valid JSON: %v\n%s", err, got)
	}

	for _, k := range []string{"raisedBy", "loweredBy2", "raisedBy5"} {
		if _, ok := fields[k]; !ok {
			t.Errorf("%q was dropped. That key is somebody's record of why a raise was "+
				"allowed, and regeneration is advertised in the ratchet's own failure "+
				"message -- so dropping it destroys the audit trail at the exact moment "+
				"an author is being told to run this.\n%s", k, got)
		}
	}
	if s := string(fields["raisedBy"]); !strings.Contains(s, "the whole point") {
		t.Errorf("raisedBy survived as a key but lost its contents: %s", s)
	}

	// The two it DOES own are rewritten.
	var total int
	if err := json.Unmarshal(fields["total"], &total); err != nil || total != 97 {
		t.Errorf("total = %v (err %v), want 97 -- the regenerator must still do its job", total, err)
	}
	var byPkg map[string]int
	if err := json.Unmarshal(fields["byPackage"], &byPkg); err != nil || byPkg["internal/api"] != 3 {
		t.Errorf("byPackage = %v (err %v), want internal/api:3", byPkg, err)
	}
	// An existing note is left alone: it is prose somebody may have edited.
	var note string
	_ = json.Unmarshal(fields["note"], &note)
	if note != "the note" {
		t.Errorf("note = %q, want the existing one preserved", note)
	}

	// ORDER, because this file is read in diffs. Reordering it on every
	// regeneration buries the two lines that actually changed under a rewrite
	// of the whole file -- which is how the original bug stayed invisible.
	want := []string{"note", "raisedBy", "loweredBy2", "total", "byPackage", "raisedBy5"}
	var order []string
	dec := json.NewDecoder(bytes.NewReader(got))
	if _, err := dec.Token(); err != nil {
		t.Fatal(err)
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		order = append(order, tok.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("key order = %v, want %v", order, want)
	}
}

func TestRegeneratingAnAbsentCensusStillWritesOne(t *testing.T) {
	// The bootstrap path the ratchet's message names: "Run ... -update-skips to
	// create it." With no previous file there is nothing to preserve, and the
	// note has to come from the caller rather than from a file that is not there.
	got, err := nextCensusFile(nil, "the fresh note", 12, map[string]int{"internal/db": 2})
	if err != nil {
		t.Fatalf("nextCensusFile on an absent census: %v", err)
	}
	var c skipCensus
	if err := json.Unmarshal(got, &c); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, got)
	}
	if c.Total != 12 || c.ByImport["internal/db"] != 2 || c.Note != "the fresh note" {
		t.Fatalf("bootstrap census is %+v", c)
	}
}
