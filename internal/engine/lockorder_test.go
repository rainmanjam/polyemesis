package engine

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// This file enforces the LOCK ORDER table in engine.go's Engine doc comment.
//
// -race does not see a lock-order inversion: it reports unsynchronised access,
// and two goroutines taking the same two mutexes in opposite orders are
// perfectly synchronised right up to the moment they deadlock. A deadlock in
// the engine is every destination, the ingest and the status page frozen at
// once, and the only prior record of which order was right was prose spread
// over seven field comments. So the table is the rule, and this is the check.
//
// WHAT IT CHECKS. Every method on *Engine in this package's non-test files is
// walked statement by statement, tracking which engine mutexes are held.
// Taking a lock whose rank is not strictly below-in-the-table every lock
// already held is a violation, and so is calling an Engine method that can
// take such a lock (through any chain of Engine method calls).
//
// WHAT IT DOES NOT SEE, stated so nobody mistakes it for a proof:
//   - function literals are checked on their own, as if nothing were held when
//     they run, and are not counted as locks their enclosing method takes --
//     most of them are callbacks that run on another goroutine, and treating
//     them as inline would flag every spawn built under e.mu. A closure called
//     inline under a lock is therefore invisible to it;
//   - calls through a field or an interface (a destination's method that takes
//     e.mu, a supervisor callback) are not followed;
//   - loops are walked once and switch arms are assumed to leave the lock set
//     as they found it.
// Within those limits it is sound for what it walks: an unlock inside a branch
// that returns does not end the lock for the code after the branch.

// lockOrderTable reads the rank of every lock from the LOCK ORDER table in the
// doc comment on Engine. Lines of the form "<tab>N  name[, name...]" are table
// rows; a row lists locks of equal rank, which may never be held together.
func lockOrderTable(t *testing.T) map[string]int {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "engine.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse engine.go: %v", err)
	}
	var doc *ast.CommentGroup
	ast.Inspect(f, func(n ast.Node) bool {
		if gd, ok := n.(*ast.GenDecl); ok && gd.Tok == token.TYPE {
			for _, s := range gd.Specs {
				if ts, ok := s.(*ast.TypeSpec); ok && ts.Name.Name == "Engine" {
					doc = gd.Doc
				}
			}
		}
		return doc == nil
	})
	if doc == nil {
		t.Fatal("no doc comment on type Engine: the LOCK ORDER table lives there")
	}
	row := regexp.MustCompile(`^\t(\d+)\s+([A-Za-z, ]+?)\s*(?:--.*)?$`)
	ranks := map[string]int{}
	inTable := false
	for _, line := range strings.Split(doc.Text(), "\n") {
		if strings.HasPrefix(line, "LOCK ORDER") {
			inTable = true
			continue
		}
		if !inTable {
			continue
		}
		m := row.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		rank, _ := strconv.Atoi(m[1])
		for _, name := range strings.Split(m[2], ",") {
			ranks[strings.TrimSpace(name)] = rank
		}
	}
	if len(ranks) == 0 {
		t.Fatal("the LOCK ORDER table on type Engine has no rows this test can read")
	}
	return ranks
}

// lockPair is one observed nesting: lock taken while held was held.
type lockPair struct {
	held, taken string
	// via is the Engine method whose call takes `taken`, empty for a direct
	// Lock in the method itself.
	via string
	at  string
	fn  string
}

type callSite struct {
	name string
	held []string
	at   string
}

type methodFacts struct {
	direct  map[string]bool
	pairs   []lockPair
	calls   []callSite
	callees map[string]bool
}

// lockWalker walks one function body.
type lockWalker struct {
	fset  *token.FileSet
	recv  string
	locks map[string]int
	facts *methodFacts
	fn    string
	// lits collects the function literals met on the way, checked separately.
	lits []*ast.FuncLit
	// inLit is set while walking a literal: its calls do not count towards the
	// enclosing method's callees.
	inLit bool
}

// lockCall recognises recv.<lock>.Lock() and friends.
func (w *lockWalker) lockCall(c *ast.CallExpr) (lock, op string, ok bool) {
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", "", false
	}
	inner, ok := sel.X.(*ast.SelectorExpr)
	if !ok {
		return "", "", false
	}
	id, ok := inner.X.(*ast.Ident)
	if !ok || id.Name != w.recv {
		return "", "", false
	}
	if _, known := w.locks[inner.Sel.Name]; !known {
		return "", "", false
	}
	switch sel.Sel.Name {
	case "Lock", "RLock", "TryLock", "TryRLock", "Unlock", "RUnlock":
		return inner.Sel.Name, sel.Sel.Name, true
	}
	return "", "", false
}

// expr scans an expression (or any non-statement node) in source order.
func (w *lockWalker) expr(n ast.Node, held []string) []string {
	if n == nil {
		return held
	}
	ast.Inspect(n, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncLit:
			w.lits = append(w.lits, x)
			return false
		case *ast.CallExpr:
			if lock, op, ok := w.lockCall(x); ok {
				switch op {
				case "Unlock", "RUnlock":
					if i := slices.Index(held, lock); i >= 0 {
						held = slices.Delete(slices.Clone(held), i, i+1)
					}
				default:
					w.facts.direct[lock] = true
					for _, h := range held {
						w.facts.pairs = append(w.facts.pairs, lockPair{held: h, taken: lock,
							at: w.fset.Position(x.Pos()).String(), fn: w.fn})
					}
					held = append(slices.Clone(held), lock)
				}
				return true
			}
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == w.recv {
					if !w.inLit {
						w.facts.callees[sel.Sel.Name] = true
					}
					if len(held) > 0 {
						w.facts.calls = append(w.facts.calls, callSite{name: sel.Sel.Name,
							held: slices.Clone(held), at: w.fset.Position(x.Pos()).String()})
					}
				}
			}
		}
		return true
	})
	return held
}

func terminates(s ast.Stmt) bool {
	switch x := s.(type) {
	case *ast.ReturnStmt, *ast.BranchStmt:
		return true
	case *ast.ExprStmt:
		if c, ok := x.X.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "panic" {
				return true
			}
		}
	}
	return false
}

// stmts walks a statement list and returns the locks held at its end, and
// whether control cannot fall out of the bottom of it.
func (w *lockWalker) stmts(list []ast.Stmt, held []string) ([]string, bool) {
	for _, s := range list {
		var term bool
		held, term = w.stmt(s, held)
		if term {
			return held, true
		}
	}
	return held, false
}

func (w *lockWalker) stmt(s ast.Stmt, held []string) ([]string, bool) {
	switch x := s.(type) {
	case *ast.BlockStmt:
		return w.stmts(x.List, held)
	case *ast.LabeledStmt:
		return w.stmt(x.Stmt, held)
	case *ast.IfStmt:
		if x.Init != nil {
			held, _ = w.stmt(x.Init, held)
		}
		held = w.expr(x.Cond, held)
		thenHeld, thenTerm := w.stmts(x.Body.List, held)
		elseHeld, elseTerm := held, false
		if x.Else != nil {
			elseHeld, elseTerm = w.stmt(x.Else, held)
		}
		switch {
		case thenTerm && elseTerm:
			return held, true
		case thenTerm:
			return elseHeld, false
		default:
			return thenHeld, false
		}
	case *ast.ForStmt:
		if x.Init != nil {
			held, _ = w.stmt(x.Init, held)
		}
		held = w.expr(x.Cond, held)
		w.stmts(x.Body.List, held)
		return held, false
	case *ast.RangeStmt:
		held = w.expr(x.X, held)
		w.stmts(x.Body.List, held)
		return held, false
	case *ast.SwitchStmt:
		if x.Init != nil {
			held, _ = w.stmt(x.Init, held)
		}
		held = w.expr(x.Tag, held)
		for _, c := range x.Body.List {
			cc := c.(*ast.CaseClause)
			for _, e := range cc.List {
				held = w.expr(e, held)
			}
			w.stmts(cc.Body, held)
		}
		return held, false
	case *ast.TypeSwitchStmt:
		if x.Init != nil {
			held, _ = w.stmt(x.Init, held)
		}
		held, _ = w.stmt(x.Assign, held)
		for _, c := range x.Body.List {
			w.stmts(c.(*ast.CaseClause).Body, held)
		}
		return held, false
	case *ast.SelectStmt:
		for _, c := range x.Body.List {
			cc := c.(*ast.CommClause)
			h := held
			if cc.Comm != nil {
				h, _ = w.stmt(cc.Comm, h)
			}
			w.stmts(cc.Body, h)
		}
		return held, false
	case *ast.DeferStmt:
		// A deferred Unlock keeps the lock held to the end of the function,
		// which is what not touching `held` models. Any other deferred call runs
		// at return, under whatever is held then; not modelled.
		if _, _, ok := w.lockCall(x.Call); ok {
			return held, false
		}
		for _, a := range x.Call.Args {
			w.expr(a, nil)
		}
		if lit, ok := x.Call.Fun.(*ast.FuncLit); ok {
			w.lits = append(w.lits, lit)
		}
		return held, false
	case *ast.GoStmt:
		// A new goroutine holds nothing. Its arguments are evaluated here.
		for _, a := range x.Call.Args {
			held = w.expr(a, held)
		}
		if lit, ok := x.Call.Fun.(*ast.FuncLit); ok {
			w.lits = append(w.lits, lit)
		}
		return held, false
	default:
		held = w.expr(s, held)
		return held, terminates(s)
	}
}

// collectLockFacts walks every *Engine method in the given files.
func collectLockFacts(fset *token.FileSet, files []*ast.File, locks map[string]int) map[string]*methodFacts {
	facts := map[string]*methodFacts{}
	for _, f := range files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || fd.Body == nil || len(fd.Recv.List[0].Names) == 0 {
				continue
			}
			rt := fd.Recv.List[0].Type
			if s, ok := rt.(*ast.StarExpr); ok {
				rt = s.X
			}
			if id, ok := rt.(*ast.Ident); !ok || id.Name != "Engine" {
				continue
			}
			mf := &methodFacts{direct: map[string]bool{}, callees: map[string]bool{}}
			facts[fd.Name.Name] = mf
			w := &lockWalker{fset: fset, recv: fd.Recv.List[0].Names[0].Name, locks: locks,
				facts: mf, fn: fd.Name.Name}
			w.stmts(fd.Body.List, nil)
			// Literals: their own nesting is checked, as if they began with
			// nothing held. New literals found inside them join the queue.
			w.inLit = true
			for i := 0; i < len(w.lits); i++ {
				w.stmts(w.lits[i].Body.List, nil)
			}
		}
	}
	return facts
}

// lockViolations applies the ranks to what collectLockFacts saw.
func lockViolations(facts map[string]*methodFacts, ranks map[string]int) []string {
	memo := map[string]map[string]bool{}
	var takes func(fn string, seen map[string]bool) map[string]bool
	takes = func(fn string, seen map[string]bool) map[string]bool {
		if m, ok := memo[fn]; ok {
			return m
		}
		out := map[string]bool{}
		mf := facts[fn]
		if mf == nil || seen[fn] {
			return out
		}
		seen[fn] = true
		for l := range mf.direct {
			out[l] = true
		}
		for c := range mf.callees {
			for l := range takes(c, seen) {
				out[l] = true
			}
		}
		delete(seen, fn)
		memo[fn] = out
		return out
	}

	var bad []string
	check := func(p lockPair) {
		if ranks[p.taken] > ranks[p.held] {
			return
		}
		how := "takes " + p.taken
		if p.via != "" {
			how = "calls " + p.via + ", which can take " + p.taken
		}
		bad = append(bad, fmt.Sprintf("%s: %s holds %s (rank %d) and %s (rank %d)",
			p.at, p.fn, p.held, ranks[p.held], how, ranks[p.taken]))
	}
	for fn, mf := range facts {
		for _, p := range mf.pairs {
			check(p)
		}
		for _, c := range mf.calls {
			for l := range takes(c.name, map[string]bool{}) {
				for _, h := range c.held {
					check(lockPair{held: h, taken: l, via: c.name, at: c.at, fn: fn})
				}
			}
		}
	}
	sort.Strings(bad)
	return slices.Compact(bad)
}

func parsePackageFiles(t *testing.T, dir string) (*token.FileSet, []*ast.File) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		files = append(files, f)
	}
	return fset, files
}

// TestEngineLocksAreTakenInTableOrder is the check.
func TestEngineLocksAreTakenInTableOrder(t *testing.T) {
	ranks := lockOrderTable(t)
	fset, files := parsePackageFiles(t, ".")
	facts := collectLockFacts(fset, files, ranks)

	// NON-VACUITY. StopWithin takes three of the ranked locks in a row; a
	// walker that had stopped seeing acquisitions would pass everything below.
	var sawChain bool
	for _, p := range facts["StopWithin"].pairs {
		if p.held == "previewMu" && p.taken == "selMu" {
			sawChain = true
		}
	}
	if !sawChain {
		t.Fatal("the walker did not see StopWithin take selMu under previewMu; it is not " +
			"seeing lock acquisitions, and a clean result below would mean nothing")
	}

	for _, v := range lockViolations(facts, ranks) {
		t.Errorf("lock order violation: %s", v)
	}
}

// TestEveryEngineMutexIsInTheLockOrderTable keeps the table complete. A new
// mutex on Engine that is not in it is unranked, and the check above would
// silently ignore every acquisition of it.
func TestEveryEngineMutexIsInTheLockOrderTable(t *testing.T) {
	ranks := lockOrderTable(t)
	src, err := os.ReadFile("engine.go")
	if err != nil {
		t.Fatal(err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), "engine.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fields []string
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "Engine" {
			return true
		}
		for _, fl := range ts.Type.(*ast.StructType).Fields.List {
			sel, ok := fl.Type.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "sync" &&
				(sel.Sel.Name == "Mutex" || sel.Sel.Name == "RWMutex") {
				for _, n := range fl.Names {
					fields = append(fields, n.Name)
				}
			}
		}
		return false
	})
	if len(fields) == 0 {
		t.Fatal("found no sync.Mutex fields on Engine; this test is not reading the struct")
	}
	for _, name := range fields {
		if _, ok := ranks[name]; !ok {
			t.Errorf("Engine.%s is a mutex with no rank in the LOCK ORDER table on type Engine; "+
				"add it, or the order check cannot see it", name)
		}
	}
	for name := range ranks {
		if !slices.Contains(fields, name) {
			t.Errorf("the LOCK ORDER table ranks %q, which is not a mutex field on Engine", name)
		}
	}
}

// The checker itself, on code written to break the rule. Without this, a
// checker that had quietly stopped matching anything would read as a clean
// bill of health.
func TestLockOrderCheckerCatchesInversions(t *testing.T) {
	const src = `package engine

type Engine struct{}

func (e *Engine) direct() {
	e.mu.Lock()
	e.selMu.Lock() // inversion: selMu ranks above mu
	e.selMu.Unlock()
	e.mu.Unlock()
}

func (e *Engine) viaCall() {
	e.mu.RLock()
	defer e.mu.RUnlock()
	e.takesPreview() // inversion through a call
}

func (e *Engine) takesPreview() {
	e.previewMu.Lock()
	e.previewMu.Unlock()
}

func (e *Engine) relock() {
	e.mu.Lock()
	e.mu.Lock() // self-deadlock
}

func (e *Engine) earlyReturnStillHeld(x bool) {
	e.mu.Lock()
	if x {
		e.mu.Unlock()
		return
	}
	e.selMu.Lock() // still under mu: the unlock above returned
	e.selMu.Unlock()
	e.mu.Unlock()
}

func (e *Engine) fine() {
	e.previewMu.Lock()
	e.selMu.Lock()
	e.mu.Lock()
	e.heldMu.Lock()
	e.heldMu.Unlock()
	e.mu.Unlock()
	e.selMu.Unlock()
	e.previewMu.Unlock()
	e.mu.Lock()
	e.mu.Unlock()
	e.selMu.Lock() // fine: mu was released
	e.selMu.Unlock()
	go func() { e.mu.Lock(); e.mu.Unlock() }() // a new goroutine holds nothing
}
`
	ranks := map[string]int{"reconcileMu": 1, "previewMu": 2, "selMu": 3, "mu": 4,
		"stopMu": 5, "heldMu": 5, "sinkMu": 5}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "synthetic.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := lockViolations(collectLockFacts(fset, []*ast.File{f}, ranks), ranks)
	want := map[string]bool{"direct": false, "viaCall": false, "relock": false, "earlyReturnStillHeld": false}
	for _, v := range got {
		for fn := range want {
			if strings.Contains(v, " "+fn+" holds ") {
				want[fn] = true
			}
		}
		if strings.Contains(v, " fine holds ") {
			t.Errorf("false positive: %s", v)
		}
	}
	for fn, seen := range want {
		if !seen {
			t.Errorf("the checker missed the inversion in %s; it reported %q", fn, got)
		}
	}
}
