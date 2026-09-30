package engine

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
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
// Calls reached through a struct field or an interface value (e.sup.Stop(),
// a destination's method, a supervisor callback) ARE followed, but only one
// hop of indirection: collectLockFacts resolves the field's or interface's
// static type with go/types (loading the whole module with go/packages),
// resolves that method through the type's FULL method set -- so a method
// promoted from an embedded field is found the same as one declared
// directly -- and, for every parameter of type *Engine it has (there may be
// more than one), walks its body the same way, under that parameter's name
// instead of "e". A call to another Engine method reached that way
// (eng.SomeMethod()) is then recognised too, because it lands back in the
// same facts map under the plain method name. For an interface-typed field,
// every module type implementing the interface is walked and its results
// unioned: a conservative over-approximation, since the concrete value at
// the call site is usually only one of them.
//
// WHAT IT DOES NOT SEE, stated so nobody mistakes it for a proof:
//   - function literals are checked on their own, as if nothing were held when
//     they run, and are not counted as locks their enclosing method takes --
//     most of them are callbacks that run on another goroutine, and treating
//     them as inline would flag every spawn built under e.mu. A closure called
//     inline under a lock is therefore invisible to it;
//   - a field or interface method that does NOT take *Engine as a parameter
//     is assumed to be unable to touch an Engine lock. That is true unless it
//     reaches one through a *Engine stashed on its own receiver type (a field,
//     not a parameter) or captured by a closure set up elsewhere -- neither is
//     followed, nor is a local alias of a tracked identifier (eng := e) inside
//     the SAME method, nor a plain package-level function call (helper(e));
//   - a field or interface method defined outside this module (stdlib, a
//     third-party import) is not source available to walk and is silently
//     skipped, same as if it took no locks;
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
	fset *token.FileSet
	// recv names the identifier(s) that stand for the *Engine this walk
	// tracks locks on: one for a plain *Engine method (its receiver name),
	// more than one for a field/interface method with several *Engine
	// parameters, or the same *Engine reached under more than one name.
	recv  []string
	locks map[string]int
	facts *methodFacts
	fn    string
	// lits collects the function literals met on the way, checked separately.
	lits []*ast.FuncLit
	// inLit is set while walking a literal: its calls do not count towards the
	// enclosing method's callees.
	inLit bool
	// info is the type info for the package fn's body lives in. Nil when this
	// walker was built without type info (the synthetic-source tests), in
	// which case calls through fields and interfaces are simply not resolved,
	// same as before this check learned to follow them.
	info *types.Info
	// resolver turns a field or interface call into the facts key(s) of the
	// method(s) it can reach, nil along with info.
	resolver *extResolver
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
	if !ok || !slices.Contains(w.recv, id.Name) {
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
				if id, ok := sel.X.(*ast.Ident); ok && slices.Contains(w.recv, id.Name) {
					if !w.inLit {
						w.facts.callees[sel.Sel.Name] = true
					}
					if len(held) > 0 {
						w.facts.calls = append(w.facts.calls, callSite{name: sel.Sel.Name,
							held: slices.Clone(held), at: w.fset.Position(x.Pos()).String()})
					}
				} else if w.info != nil && w.resolver != nil {
					// A call through a struct field or an interface value:
					// e.sup.Stop(), a destination's method, a callback held by
					// an interface field. resolve turns it into zero or more
					// facts keys (more than one for an interface: one per
					// module type that implements it).
					for _, key := range w.resolver.resolve(w.info, sel) {
						if !w.inLit {
							w.facts.callees[key] = true
						}
						if len(held) > 0 {
							w.facts.calls = append(w.facts.calls, callSite{name: key,
								held: slices.Clone(held), at: w.fset.Position(x.Pos()).String()})
						}
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

// collectLockFacts walks every *Engine method in the given files. info and
// resolver carry type information for following calls through struct fields
// and interface values; either may be nil, which simply disables that (the
// synthetic-source tests that have no type-checked package exercise this
// path). facts, if non-nil, is filled in place instead of a fresh map, so a
// resolver sharing it can add the facts of the field/interface methods it
// resolves into the same map collectLockFacts is building.
func collectLockFacts(fset *token.FileSet, files []*ast.File, locks map[string]int,
	info *types.Info, resolver *extResolver, facts map[string]*methodFacts) map[string]*methodFacts {
	if facts == nil {
		facts = map[string]*methodFacts{}
	}
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
			w := &lockWalker{fset: fset, recv: []string{fd.Recv.List[0].Names[0].Name}, locks: locks,
				facts: mf, fn: fd.Name.Name, info: info, resolver: resolver}
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

// extPkg is the slice of a type-checked package that extResolver needs: its
// own (non-test) syntax and the type info for it. Built either from a
// *packages.Package (the real run, loading the whole module) or, in tests,
// straight from a single types.Config.Check of synthetic source.
type extPkg struct {
	path  string
	files []*ast.File
	info  *types.Info
	scope *types.Scope
}

// extResolver turns a call through a struct field or an interface value into
// the facts key(s) of the method(s) it can reach, walking and memoising them
// into facts as it goes -- the same facts map collectLockFacts is filling
// for *Engine's own methods, so lockViolations sees everything in one place.
type extResolver struct {
	fset       *token.FileSet
	pkgs       []extPkg
	modulePath string
	engineT    *types.Named
	locks      map[string]int
	facts      map[string]*methodFacts
}

// namedOf strips one pointer indirection and reports the named type
// underneath, if there is one.
func namedOf(t types.Type) (*types.Named, bool) {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	n, ok := t.(*types.Named)
	return n, ok
}

// resolve resolves one call sel = <expr>.<method>(...), where <expr> is
// anything other than the walker's own receiver identifier, to the facts
// key(s) of the method(s) it can reach. It returns nil when the receiver
// expression's static type cannot be pinned to module source (an interface
// with no module implementor, a type from outside the module, a builtin).
func (r *extResolver) resolve(info *types.Info, sel *ast.SelectorExpr) []string {
	recvType := info.TypeOf(sel.X)
	if recvType == nil {
		return nil
	}
	method := sel.Sel.Name
	if iface, ok := recvType.Underlying().(*types.Interface); ok {
		var keys []string
		for _, named := range r.implementors(iface) {
			if key, ok := r.resolveMethod(named, method); ok {
				keys = append(keys, key)
			}
		}
		return keys
	}
	named, ok := namedOf(recvType)
	if !ok {
		return nil
	}
	if types.Identical(named, r.engineT) {
		// The expression is itself a *Engine value under another name (an
		// alias, a value threaded through as a parameter): the same facts as
		// a direct e.<method>() call apply.
		return []string{method}
	}
	if key, ok := r.resolveMethod(named, method); ok {
		return []string{key}
	}
	return nil
}

// implementors returns every named, non-interface type in the loaded
// packages whose method set (value or pointer) implements iface. A
// conservative over-approximation of "the concrete type actually stored in
// this interface field": it may include types never assigned there.
func (r *extResolver) implementors(iface *types.Interface) []*types.Named {
	var out []*types.Named
	for _, p := range r.pkgs {
		if p.scope == nil {
			continue
		}
		for _, name := range p.scope.Names() {
			tn, ok := p.scope.Lookup(name).(*types.TypeName)
			if !ok {
				continue
			}
			named, ok := tn.Type().(*types.Named)
			if !ok {
				continue
			}
			if _, isIface := named.Underlying().(*types.Interface); isIface {
				continue
			}
			if types.Implements(named, iface) || types.Implements(types.NewPointer(named), iface) {
				out = append(out, named)
			}
		}
	}
	return out
}

func (r *extResolver) inModule(pkgPath string) bool {
	return pkgPath == r.modulePath || strings.HasPrefix(pkgPath, r.modulePath+"/")
}

func (r *extResolver) filesFor(pkgPath string) ([]*ast.File, *types.Info) {
	for _, p := range r.pkgs {
		if p.path == pkgPath {
			return p.files, p.info
		}
	}
	return nil, nil
}

// methodFunc finds named.method through named's full method set -- pointer
// and value, and INCLUDING methods promoted from an embedded field, which a
// receiver-name AST scan would miss. It returns the *types.Func exactly as
// go/types resolved it, whose Recv() names the type that actually declares
// the method (named itself, or whatever it embeds).
func methodFunc(named *types.Named, method string) *types.Func {
	for _, t := range [2]types.Type{types.NewPointer(named), named} {
		ms := types.NewMethodSet(t)
		for i := 0; i < ms.Len(); i++ {
			if obj := ms.At(i).Obj(); obj.Name() == method {
				if fn, ok := obj.(*types.Func); ok {
					return fn
				}
			}
		}
	}
	return nil
}

// resolveMethod finds named.method -- however it reaches named, directly or
// through an embedded field -- locates its declaration in module source,
// walks its body under the name(s) of any *Engine parameter it has, and
// memoises the result into r.facts under a key namespaced by the DECLARING
// package and type (not named itself, so a method promoted into several
// wrapper types is only walked once). It pre-registers an empty entry before
// walking so a call cycle through fields/interfaces resolves to "nothing new
// found" on the second visit rather than recursing forever.
func (r *extResolver) resolveMethod(named *types.Named, method string) (string, bool) {
	fn := methodFunc(named, method)
	if fn == nil {
		return "", false
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return "", false
	}
	declT, ok := namedOf(sig.Recv().Type())
	if !ok {
		return "", false
	}
	obj := declT.Obj()
	if obj.Pkg() == nil || !r.inModule(obj.Pkg().Path()) {
		// Outside the module: no source to walk. Treated as taking nothing,
		// same limitation as the literals this check has always skipped.
		return "", false
	}
	key := "external:" + obj.Pkg().Path() + "." + obj.Name() + "." + method
	if _, ok := r.facts[key]; ok {
		return key, true
	}
	files, info := r.filesFor(obj.Pkg().Path())
	if files == nil {
		return "", false
	}
	fd := findMethod(files, obj.Name(), method)
	if fd == nil || fd.Body == nil {
		return "", false
	}
	mf := &methodFacts{direct: map[string]bool{}, callees: map[string]bool{}}
	r.facts[key] = mf
	if recvs := engineParams(info, fd, r.engineT); len(recvs) > 0 {
		w := &lockWalker{fset: r.fset, recv: recvs, locks: r.locks, facts: mf, fn: key,
			info: info, resolver: r}
		w.stmts(fd.Body.List, nil)
		w.inLit = true
		for i := 0; i < len(w.lits); i++ {
			w.stmts(w.lits[i].Body.List, nil)
		}
	}
	// recvs is empty: the method has no *Engine-typed parameter, so nothing
	// in its body can reach an Engine lock; mf stays empty, correctly.
	return key, true
}

// findMethod finds func (recv [*]typeName) method(...) among files. The
// receiver need not be named: this is reached only with a (pkg, typeName)
// pair go/types already confirmed declares method, so there is nothing left
// to disambiguate by name.
func findMethod(files []*ast.File, typeName, method string) *ast.FuncDecl {
	for _, f := range files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || fd.Name.Name != method {
				continue
			}
			rt := fd.Recv.List[0].Type
			if s, ok := rt.(*ast.StarExpr); ok {
				rt = s.X
			}
			if id, ok := rt.(*ast.Ident); ok && id.Name == typeName {
				return fd
			}
		}
	}
	return nil
}

// engineParams returns the name(s) of every fd parameter of type *Engine (or
// Engine) -- a method commonly needs only one, but nothing stops it from
// taking two (e.g. a source and a destination engine), and missing one would
// make that identifier's lock calls invisible. A parameter naming several
// identifiers of the same type (a, b *Engine) contributes all of them.
func engineParams(info *types.Info, fd *ast.FuncDecl, engineT *types.Named) []string {
	if fd.Type.Params == nil {
		return nil
	}
	var names []string
	for _, field := range fd.Type.Params.List {
		t := info.TypeOf(field.Type)
		if t == nil {
			continue
		}
		named, ok := namedOf(t)
		if !ok || !types.Identical(named, engineT) {
			continue
		}
		for _, n := range field.Names {
			names = append(names, n.Name)
		}
	}
	return names
}

// modulePath reads the module path out of go.mod, two directories up from
// this package (internal/engine -> repo root) -- the same assumption
// lockOrderTable and parsePackageFiles already make about the working
// directory `go test` runs this package's tests from.
func modulePath(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if after, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(after)
		}
	}
	t.Fatal("go.mod has no module line")
	return ""
}

// loadTypedEnginePackage type-checks the whole module with go/packages and
// returns internal/engine's own (fset-consistent) syntax and type info,
// plus a resolver over every loaded package for following calls through
// fields and interfaces. locks is threaded into the resolver so methods it
// walks recognise the same lock fields collectLockFacts does.
func loadTypedEnginePackage(t *testing.T, locks map[string]int) (*token.FileSet, []*ast.File, *types.Info, *extResolver) {
	t.Helper()
	mod := modulePath(t)
	fset := token.NewFileSet()
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedSyntax |
			packages.NeedImports | packages.NeedDeps,
		Fset: fset,
		Dir:  ".",
	}
	pkgs, err := packages.Load(cfg, mod+"/...")
	if err != nil {
		t.Fatalf("load module packages for the lock-order check: %v", err)
	}
	var engine *packages.Package
	extPkgs := make([]extPkg, 0, len(pkgs))
	for _, p := range pkgs {
		if p.Types == nil || p.TypesInfo == nil {
			continue // failed to type-check; the resolver just won't see it
		}
		extPkgs = append(extPkgs, extPkg{path: p.PkgPath, files: p.Syntax, info: p.TypesInfo, scope: p.Types.Scope()})
		if p.PkgPath == mod+"/internal/engine" {
			engine = p
		}
	}
	if engine == nil {
		t.Fatal("could not load internal/engine as a type-checked package")
	}
	for _, e := range engine.Errors {
		t.Fatalf("type error loading internal/engine: %v", e)
	}
	obj := engine.Types.Scope().Lookup("Engine")
	if obj == nil {
		t.Fatal("no Engine type found in the type-checked internal/engine package")
	}
	engineT, ok := obj.Type().(*types.Named)
	if !ok {
		t.Fatal("Engine is not a named type")
	}
	r := &extResolver{fset: fset, pkgs: extPkgs, modulePath: mod, engineT: engineT, locks: locks}
	return fset, engine.Syntax, engine.TypesInfo, r
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

// TestEngineLocksAreTakenInTableOrder is the check.
func TestEngineLocksAreTakenInTableOrder(t *testing.T) {
	ranks := lockOrderTable(t)
	fset, files, info, resolver := loadTypedEnginePackage(t, ranks)
	facts := map[string]*methodFacts{}
	resolver.facts = facts
	collectLockFacts(fset, files, ranks, info, resolver, facts)

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
	got := lockViolations(collectLockFacts(fset, []*ast.File{f}, ranks, nil, nil, nil), ranks)
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

// TestLockOrderCheckerFollowsFieldAndInterfaceCalls is the negative control
// for the field/interface extension: one inversion reached ONLY through a
// struct field call (e.sup.Stop) and one reached ONLY through an interface
// call (e.iface.Stop), neither of which names an Engine method directly at
// the call site. A resolver that had quietly stopped following either path
// would report both viaField and viaIface clean.
//
// The fixture is type-checked directly with go/types rather than loaded
// through go/packages (which needs real files on module-relative import
// paths): a single synthetic package is enough to exercise resolve(),
// implementors() and resolveMethod(), which don't care how many packages the
// module has, only what go/types says about one call's receiver type.
func TestLockOrderCheckerFollowsFieldAndInterfaceCalls(t *testing.T) {
	const src = `package synthetic

import "sync"

type Engine struct {
	mu    sync.Mutex
	selMu sync.Mutex
	sup   *Sup
	wrap  *Wrap
	iface Stopper
}

type Sup struct{}

// Stop is reached only through a struct field (e.sup): nothing at the
// viaField call site names it as an Engine method.
func (s *Sup) Stop(e *Engine) {
	e.selMu.Lock()
	e.selMu.Unlock()
}

// Take is reached the same way, but in the correct order. It takes two
// *Engine parameters to check that neither is missed.
func (s *Sup) Take(a, b *Engine) {
	a.mu.Lock()
	a.mu.Unlock()
	b.mu.Lock()
	b.mu.Unlock()
}

func (e *Engine) viaField() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sup.Stop(e) // inversion: selMu (rank 1) taken while mu (rank 2) is held
}

func (e *Engine) fineViaField() {
	e.selMu.Lock()
	defer e.selMu.Unlock()
	e.sup.Take(e, e) // fine: mu ranks above selMu, from either parameter name
}

// Wrap embeds Sup without redeclaring Stop: calling it through Wrap is a
// promoted method, not one findMethod would find by scanning for a
// declaration named directly on Wrap.
type Wrap struct {
	*Sup
}

func (e *Engine) viaEmbeddedField() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.wrap.Stop(e) // inversion reached only through a promoted method
}

type Stopper interface {
	Stop(e *Engine)
}

type IfaceSup struct{}

// Stop is reached only through an interface value (e.iface): the concrete
// type is never named at the viaIface call site either.
func (s *IfaceSup) Stop(e *Engine) {
	e.selMu.Lock()
	e.selMu.Unlock()
}

func (e *Engine) viaIface() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.iface.Stop(e) // inversion: selMu taken while mu is held, via the interface
}

func (e *Engine) fine() {
	e.mu.Lock()
	e.mu.Unlock()
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "synthetic.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	conf := types.Config{Importer: importer.Default()}
	pkg, err := conf.Check("synthetic", fset, []*ast.File{f}, info)
	if err != nil {
		t.Fatalf("type-check the field/interface fixture: %v", err)
	}
	engineT, ok := pkg.Scope().Lookup("Engine").Type().(*types.Named)
	if !ok {
		t.Fatal("Engine is not a named type in the fixture")
	}

	ranks := map[string]int{"selMu": 1, "mu": 2}
	facts := map[string]*methodFacts{}
	r := &extResolver{
		fset:       fset,
		pkgs:       []extPkg{{path: "synthetic", files: []*ast.File{f}, info: info, scope: pkg.Scope()}},
		modulePath: "synthetic",
		engineT:    engineT,
		locks:      ranks,
		facts:      facts,
	}
	collectLockFacts(fset, []*ast.File{f}, ranks, info, r, facts)
	got := lockViolations(facts, ranks)

	want := map[string]bool{"viaField": false, "viaIface": false, "viaEmbeddedField": false}
	for _, v := range got {
		for fn := range want {
			if strings.Contains(v, " "+fn+" holds ") {
				want[fn] = true
			}
		}
		if strings.Contains(v, " fine holds ") || strings.Contains(v, " fineViaField holds ") {
			t.Errorf("false positive: %s", v)
		}
	}
	for fn, seen := range want {
		if !seen {
			t.Errorf("the checker missed the inversion reached only through %s; it reported %q", fn, got)
		}
	}
}
