package appbridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	gotoken "go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
)

// Settings slice F §2: every guarded method fences what it sends. The scan
// type-checks the package (F1 re-review, RR2), so a context is known by its
// type and every function by its object - an import alias, a dot import, a
// tuple result or an unusual name changes nothing - and it accepts a closed
// set of guarded-shell forms, failing closed on anything else. In each
// function that takes a ticket from guardFor:
//   - the ticket is used, by Write or StartCLI;
//   - every context.Background or context.TODO is called (never taken as a
//     function value) inside the ticket's Write, or its line says
//     "unfenced:" and why;
//   - every expression of context type has a known provenance: the
//     ticket's Write, a context.With*, readfence or fenced wrapper of one,
//     or a variable assigned only those - a variable declared on an
//     "unfenced:" line carries that line's reason. Any other context - a
//     field, a parameter, another call's result, a tuple result other than
//     a wrapper's first - is refused;
//   - every child start (a Start call, launchTerminal) is the statement
//     right after `if err := t.StartCLI(); err != nil { ... return }` in
//     the same block, a goroutine's included; a StartCLI in any other form
//     is refused.
// Across the package, a function returning a context (other than the
// reviewed ones below) and a package-level context or constructor reference
// are refused unless marked "unfenced:".

// reviewedContextFuncs are the package's functions returning a context,
// each reviewed: the ticket's Write, a derivation of its argument, and a
// read's own lifetime.
var reviewedContextFuncs = map[string]string{
	"Ticket.Write":        "the ticket's fence and a write tracker on its argument",
	"fenced":              "its argument with a cancellation the fence drives; a wrapper, never a new root",
	"LogsService.reserve": "a log stream's lifetime: a read, never a guarded write's",
	"withLineages":        "its argument with a lineage recorder value (WithValue), and its fence extended; a wrapper, never a new root (Settings S3a §4.3)",
}

// contextWrappers derive a context from their first argument.
var contextWrappers = map[string][]string{
	"context": {"WithTimeout", "WithCancel", "WithDeadline", "WithValue", "WithoutCancel", "WithCancelCause", "WithTimeoutCause", "WithDeadlineCause"},
}

type ctxStatus int

const (
	ctxFenced   ctxStatus = iota // the ticket's Write, or derived from it
	ctxMarked                    // made on an "unfenced:" line, or derived from one
	ctxUnfenced                  // anything else
)

func worse(a, b ctxStatus) ctxStatus {
	if a > b {
		return a
	}
	return b
}

// typed is a type-checked package: its files and what the checker knows.
type typed struct {
	fset  *gotoken.FileSet
	pkg   *types.Package
	info  *types.Info
	files []*ast.File
}

// goList is the package's own build: each dependency's export data, from
// `go list -export -deps`, and the package's files.
var goList struct {
	once    sync.Once
	exports map[string]string
	files   []string
	err     error
	fset    *gotoken.FileSet
	imp     types.Importer
}

func listed(t *testing.T) {
	t.Helper()
	goList.once.Do(func() {
		bin, err := exec.LookPath("go")
		if err != nil {
			goList.err = err
			return
		}
		cmd := exec.Command(bin, "list", "-export", "-deps", "-json=ImportPath,Export,GoFiles", ".")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			goList.err = fmt.Errorf("go list: %v: %s", err, stderr.String())
			return
		}
		goList.exports = map[string]string{}
		dec := json.NewDecoder(bytes.NewReader(out))
		for {
			var p struct {
				ImportPath, Export string
				GoFiles            []string
			}
			if err := dec.Decode(&p); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				goList.err = err
				return
			}
			if p.Export != "" {
				goList.exports[p.ImportPath] = p.Export
			}
			goList.files = p.GoFiles // -deps lists the package itself last
		}
		goList.fset = gotoken.NewFileSet()
		goList.imp = importer.ForCompiler(goList.fset, "gc", func(path string) (io.ReadCloser, error) {
			f, ok := goList.exports[path]
			if !ok {
				return nil, fmt.Errorf("no export data for %q", path)
			}
			return os.Open(f)
		})
	})
	if goList.err != nil {
		t.Fatal(goList.err)
	}
}

func parseIn(t *testing.T, name string, src []byte) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(goList.fset, name, src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// typecheck checks files as the package path; any type error fails the
// test, so every fixture and mutation is valid Go.
func typecheck(t *testing.T, path string, files []*ast.File) typed {
	t.Helper()
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}}
	var errs []string
	conf := types.Config{Importer: goList.imp, Error: func(err error) { errs = append(errs, err.Error()) }}
	pkg, _ := conf.Check(path, goList.fset, files, info)
	if len(errs) > 0 {
		t.Fatalf("type-check %s: %s", path, strings.Join(errs[:min(5, len(errs))], "; "))
	}
	return typed{fset: goList.fset, pkg: pkg, info: info, files: files}
}

// thisPackage is the package's own files, type-checked, with mutate
// replacing one file's source first ("" for none).
func thisPackage(t *testing.T, file string, mutate func(string) string) (typed, *ast.File) {
	t.Helper()
	listed(t)
	var files []*ast.File
	var target *ast.File
	for _, name := range goList.files {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if name == file && mutate != nil {
			changed := mutate(string(src))
			if changed == string(src) {
				t.Fatalf("the mutation of %s did not land", file)
			}
			src = []byte(changed)
		}
		f := parseIn(t, name, src)
		if name == file {
			target = f
		}
		files = append(files, f)
	}
	return typecheck(t, "github.com/moomora/klyx/internal/appbridge", files), target
}

type guardScan struct {
	typed
	ctxType  types.Type
	unfenced map[int]bool
	vars     map[*types.Var]ctxStatus
	tickets  map[*types.Var]bool
}

func newScan(tp typed, f *ast.File) *guardScan {
	g := &guardScan{typed: tp, unfenced: map[int]bool{}}
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			if strings.Contains(c.Text, "unfenced:") {
				g.unfenced[tp.fset.Position(c.Pos()).Line] = true
			}
		}
	}
	g.ctxType = contextType(tp.pkg, map[*types.Package]bool{})
	return g
}

// contextType is context.Context, found through the package's imports.
func contextType(p *types.Package, seen map[*types.Package]bool) types.Type {
	if p == nil || seen[p] {
		return nil
	}
	seen[p] = true
	if p.Path() == "context" {
		return p.Scope().Lookup("Context").Type()
	}
	for _, imp := range p.Imports() {
		if t := contextType(imp, seen); t != nil {
			return t
		}
	}
	return nil
}

func (g *guardScan) marked(n ast.Node) bool { return g.unfenced[g.fset.Position(n.Pos()).Line] }

func (g *guardScan) isCtxType(t types.Type) bool {
	if g.ctxType == nil || t == nil {
		return false
	}
	if _, tuple := t.(*types.Tuple); tuple {
		return false
	}
	return types.AssignableTo(t, g.ctxType)
}

// isCtx: e is a value of context type.
func (g *guardScan) isCtx(e ast.Expr) bool {
	tv, ok := g.info.Types[e]
	return ok && tv.IsValue() && !tv.IsNil() && g.isCtxType(tv.Type)
}

func (g *guardScan) callee(call *ast.CallExpr) types.Object {
	switch f := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		return g.info.Uses[f]
	case *ast.SelectorExpr:
		return g.info.Uses[f.Sel]
	}
	return nil
}

func pkgFunc(obj types.Object, path string, names ...string) bool {
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != path || fn.Type().(*types.Signature).Recv() != nil {
		return false
	}
	return slices.Contains(names, fn.Name())
}

func (g *guardScan) local(obj types.Object, name string) bool {
	return g.pkg != nil && pkgFunc(obj, g.pkg.Path(), name)
}

func (g *guardScan) ticketMethod(obj types.Object, name string) bool {
	fn, ok := obj.(*types.Func)
	if !ok || fn.Name() != name {
		return false
	}
	recv := fn.Type().(*types.Signature).Recv()
	if recv == nil {
		return false
	}
	t := recv.Type()
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	n, ok := t.(*types.Named)
	return ok && n.Obj().Name() == "Ticket" && n.Obj().Pkg() == g.pkg
}

func isConstructor(obj types.Object) bool {
	return pkgFunc(obj, "context", "Background", "TODO")
}

func (g *guardScan) isWrapper(obj types.Object) bool {
	if pkgFunc(obj, "context", contextWrappers["context"]...) || g.local(obj, "fenced") {
		return true
	}
	fn, ok := obj.(*types.Func)
	return ok && fn.Pkg() != nil && strings.HasSuffix(fn.Pkg().Path(), "/internal/readfence") && pkgFunc(obj, fn.Pkg().Path(), "With", "WithWriteTracker")
}

// status is a context expression's provenance.
func (g *guardScan) status(e ast.Expr) ctxStatus {
	st := g.statusRaw(ast.Unparen(e))
	if st == ctxUnfenced && g.marked(e) {
		return ctxMarked
	}
	return st
}

func (g *guardScan) statusRaw(e ast.Expr) ctxStatus {
	switch e := e.(type) {
	case *ast.CallExpr:
		obj := g.callee(e)
		switch {
		case g.ticketMethod(obj, "Write"):
			return ctxFenced
		case g.isWrapper(obj):
			if len(e.Args) > 0 && g.isCtx(e.Args[0]) {
				return g.status(e.Args[0])
			}
		}
		return ctxUnfenced // a constructor, or another call's context
	case *ast.Ident:
		if v, ok := g.info.Uses[e].(*types.Var); ok {
			if st, ok := g.vars[v]; ok {
				return st
			}
		}
	}
	return ctxUnfenced // a field, a parameter, an index, a conversion
}

func (g *guardScan) varOf(e ast.Expr) *types.Var {
	id, ok := ast.Unparen(e).(*ast.Ident)
	if !ok {
		return nil
	}
	if v, ok := g.info.Defs[id].(*types.Var); ok {
		return v
	}
	v, _ := g.info.Uses[id].(*types.Var)
	return v
}

// binding is one assignment to a context variable: rhs (nil for none - a
// parameter, a zero value, a range), and the result's index in a tuple.
type binding struct {
	rhs   ast.Expr
	tuple int
}

func (g *guardScan) bindingStatus(b binding) ctxStatus {
	switch {
	case b.rhs == nil:
		return ctxUnfenced
	case b.tuple < 0:
		return g.status(b.rhs)
	}
	// a tuple: only a wrapper's first result derives from its argument
	if call, ok := ast.Unparen(b.rhs).(*ast.CallExpr); ok && b.tuple == 0 && g.isWrapper(g.callee(call)) {
		return g.status(call)
	}
	if g.marked(b.rhs) {
		return ctxMarked
	}
	return ctxUnfenced
}

// track computes every context variable's provenance in fd, joined over
// its bindings, to the least fixpoint; a variable with no binding here (a
// package-level one) is unfenced.
func (g *guardScan) track(fd *ast.FuncDecl) {
	binds := map[*types.Var][]binding{}
	bind := func(lhs ast.Expr, b binding) {
		if v := g.varOf(lhs); v != nil && g.isCtxType(v.Type()) {
			binds[v] = append(binds[v], b)
		}
	}
	params := func(ft *ast.FuncType) {
		for _, fl := range ft.Params.List {
			for _, n := range fl.Names {
				bind(n, binding{})
			}
		}
	}
	params(fd.Type)
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			for i, l := range n.Lhs {
				switch {
				case len(n.Rhs) == len(n.Lhs):
					bind(l, binding{rhs: n.Rhs[i], tuple: -1})
				case len(n.Rhs) == 1:
					bind(l, binding{rhs: n.Rhs[0], tuple: i})
				}
			}
		case *ast.ValueSpec:
			for i, id := range n.Names {
				switch {
				case len(n.Values) == len(n.Names):
					bind(id, binding{rhs: n.Values[i], tuple: -1})
				case len(n.Values) == 1:
					bind(id, binding{rhs: n.Values[0], tuple: i})
				default:
					bind(id, binding{})
				}
			}
		case *ast.RangeStmt:
			if n.Key != nil {
				bind(n.Key, binding{})
			}
			if n.Value != nil {
				bind(n.Value, binding{})
			}
		case *ast.FuncLit:
			params(n.Type)
		}
		return true
	})
	g.vars = map[*types.Var]ctxStatus{}
	for v := range binds {
		g.vars[v] = ctxFenced
	}
	for changed := true; changed; {
		changed = false
		for v, bs := range binds {
			st := g.vars[v]
			for _, b := range bs {
				st = worse(st, g.bindingStatus(b))
			}
			if st != g.vars[v] {
				g.vars[v], changed = st, true
			}
		}
	}
}

func guardFindings(tp typed, f *ast.File) []string {
	g := newScan(tp, f)
	out := g.packageFindings(f)
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		g.tickets = map[*types.Var]bool{}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if as, ok := n.(*ast.AssignStmt); ok && len(as.Rhs) == 1 {
				if call, ok := ast.Unparen(as.Rhs[0]).(*ast.CallExpr); ok && g.local(g.callee(call), "guardFor") {
					if v := g.varOf(as.Lhs[0]); v != nil {
						g.tickets[v] = true
					}
				}
			}
			return true
		})
		if len(g.tickets) == 0 {
			continue
		}
		g.track(fd)
		out = append(out, g.functionFindings(fd)...)
	}
	return out
}

func (g *guardScan) isTicket(e ast.Expr) bool {
	v := g.varOf(e)
	return v != nil && g.tickets[v]
}

func (g *guardScan) functionFindings(fd *ast.FuncDecl) []string {
	name := fd.Name.Name
	var out []string
	used := map[*types.Var]bool{}
	var stack []ast.Node
	var visit func(n ast.Node) bool
	visit = func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		switch n := n.(type) {
		case *ast.AssignStmt:
			// a binding's target is a definition or a store, not a use
			for _, r := range n.Rhs {
				ast.Inspect(r, visit)
			}
			stack = stack[:len(stack)-1]
			return false
		case *ast.ValueSpec:
			for _, v := range n.Values {
				ast.Inspect(v, visit)
			}
			stack = stack[:len(stack)-1]
			return false
		case *ast.Ident:
			if obj := g.info.Uses[n]; isConstructor(obj) && !g.marked(n) && !g.calledAs(stack) {
				out = append(out, name+": a context constructor taken as a value")
			}
		case *ast.CallExpr:
			obj := g.callee(n)
			sel, _ := ast.Unparen(n.Fun).(*ast.SelectorExpr)
			switch {
			case g.ticketMethod(obj, "Write"):
				if sel != nil && g.isTicket(sel.X) {
					used[g.varOf(sel.X)] = true
				}
				// the ticket fences what it is given: its argument is its own
				stack = stack[:len(stack)-1]
				return false
			case g.ticketMethod(obj, "StartCLI"):
				if sel != nil && g.isTicket(sel.X) {
					used[g.varOf(sel.X)] = true
				}
				if !g.canonicalCheck(stack) {
					out = append(out, name+": StartCLI's refusal is not checked by `if err := t.StartCLI(); err != nil { return }`")
				}
			case isConstructor(obj) && !g.marked(n):
				out = append(out, name+": a context outside the ticket's Write")
			}
			if sel != nil && sel.Sel.Name == "Start" && !g.checkedRightBefore(stack) {
				out = append(out, name+": a child started without StartCLI checked right before it")
			}
			if g.local(obj, "launchTerminal") && !g.checkedRightBefore(stack) {
				out = append(out, name+": a terminal launched without StartCLI checked right before it")
			}
		}
		if e, ok := n.(ast.Expr); ok && g.isCtx(e) && g.status(e) == ctxUnfenced {
			out = append(out, name+": a context of unfenced or unknown provenance "+g.reaches(stack, e))
		}
		return true
	}
	ast.Inspect(fd.Body, visit)
	for tk := range g.tickets {
		if !used[tk] {
			out = append(out, name+": ticket "+tk.Name()+" is never used by Write or StartCLI")
		}
	}
	return out
}

// calledAs: the identifier at the top of the stack is the function a call
// calls, directly or as a selector's name.
func (g *guardScan) calledAs(stack []ast.Node) bool {
	n := len(stack)
	if n < 2 {
		return false
	}
	fun := stack[n-1].(ast.Expr)
	if sel, ok := stack[n-2].(*ast.SelectorExpr); ok && sel.Sel == fun && n >= 3 {
		fun = sel
		if call, ok := stack[n-3].(*ast.CallExpr); ok {
			return ast.Unparen(call.Fun) == fun
		}
		return false
	}
	call, ok := stack[n-2].(*ast.CallExpr)
	return ok && ast.Unparen(call.Fun) == fun
}

// reaches names the call a context argument reaches, or the expression.
func (g *guardScan) reaches(stack []ast.Node, e ast.Expr) string {
	if len(stack) >= 2 {
		if call, ok := stack[len(stack)-2].(*ast.CallExpr); ok && slices.Contains(call.Args, e) {
			switch f := ast.Unparen(call.Fun).(type) {
			case *ast.Ident:
				return "reaches " + f.Name
			case *ast.SelectorExpr:
				return "reaches " + f.Sel.Name
			}
		}
	}
	return "is used: " + types.ExprString(e)
}

// isCanonicalCheck: `if err := t.StartCLI(); err != nil { ...; return ... }`.
func (g *guardScan) isCanonicalCheck(st ast.Stmt) bool {
	is, ok := st.(*ast.IfStmt)
	if !ok || is.Else != nil {
		return false
	}
	init, ok := is.Init.(*ast.AssignStmt)
	if !ok || len(init.Lhs) != 1 || len(init.Rhs) != 1 {
		return false
	}
	errID, ok := init.Lhs[0].(*ast.Ident)
	if !ok || errID.Name == "_" {
		return false
	}
	call, ok := ast.Unparen(init.Rhs[0]).(*ast.CallExpr)
	if !ok || !g.ticketMethod(g.callee(call), "StartCLI") {
		return false
	}
	if sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr); !ok || !g.isTicket(sel.X) {
		return false
	}
	cond, ok := is.Cond.(*ast.BinaryExpr)
	if !ok || cond.Op != gotoken.NEQ || g.varOf(cond.X) != g.varOf(errID) {
		return false
	}
	if y, ok := cond.Y.(*ast.Ident); !ok || g.info.Uses[y] != types.Universe.Lookup("nil") {
		return false
	}
	n := len(is.Body.List)
	if n == 0 {
		return false
	}
	_, returns := is.Body.List[n-1].(*ast.ReturnStmt)
	return returns
}

// canonicalCheck: the StartCLI call at the top of the stack is the Init of
// a canonical check (the call, its assignment, the if).
func (g *guardScan) canonicalCheck(stack []ast.Node) bool {
	n := len(stack)
	if n < 3 {
		return false
	}
	as, ok := stack[n-2].(*ast.AssignStmt)
	if !ok {
		return false
	}
	is, ok := stack[n-3].(*ast.IfStmt)
	return ok && is.Init == as && g.isCanonicalCheck(is)
}

// checkedRightBefore: the statement of the innermost block holding the
// call follows a canonical StartCLI check directly.
func (g *guardScan) checkedRightBefore(stack []ast.Node) bool {
	for i := len(stack) - 2; i >= 0; i-- {
		block, ok := stack[i].(*ast.BlockStmt)
		if !ok {
			continue
		}
		child := stack[i+1]
		for j, st := range block.List {
			if st == child {
				return j > 0 && g.isCanonicalCheck(block.List[j-1])
			}
		}
		return false
	}
	return false
}

// packageFindings refuses, across a file, a function returning a context
// (other than the reviewed ones) and a package-level context or
// constructor reference, unless marked "unfenced:".
func (g *guardScan) packageFindings(f *ast.File) []string {
	var out []string
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			fn, ok := g.info.Defs[d.Name].(*types.Func)
			if !ok || g.marked(d) {
				continue
			}
			if _, ok := reviewedContextFuncs[funcKey(d)]; ok {
				continue
			}
			res := fn.Type().(*types.Signature).Results()
			for i := range res.Len() {
				if g.isCtxType(res.At(i).Type()) {
					out = append(out, d.Name.Name+": a function returning a context outside the reviewed forms")
					break
				}
			}
		case *ast.GenDecl:
			if d.Tok != gotoken.VAR {
				continue
			}
			for _, sp := range d.Specs {
				vs := sp.(*ast.ValueSpec)
				if g.marked(vs) {
					continue
				}
				for _, id := range vs.Names {
					if v, ok := g.info.Defs[id].(*types.Var); ok && g.isCtxType(v.Type()) {
						out = append(out, id.Name+": a package-level context")
					}
				}
				ast.Inspect(vs, func(n ast.Node) bool {
					if id, ok := n.(*ast.Ident); ok && isConstructor(g.info.Uses[id]) {
						out = append(out, vs.Names[0].Name+": a package-level context constructor reference")
					}
					return true
				})
			}
		}
	}
	return out
}

// funcKey is "Recv.name" for a method, "name" for a function.
func funcKey(d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) != 1 {
		return d.Name.Name
	}
	t := d.Recv.List[0].Type
	if st, ok := t.(*ast.StarExpr); ok {
		t = st.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name + "." + d.Name.Name
	}
	return d.Name.Name
}

func TestEveryGuardedMethodFencesWhatItSends(t *testing.T) {
	tp, _ := thisPackage(t, "", nil)
	guarded := 0
	for _, f := range tp.files {
		name := tp.fset.Position(f.Pos()).Filename
		src, _ := os.ReadFile(name)
		guarded += strings.Count(string(src), "guardFor(cluster)")
		for _, finding := range guardFindings(tp, f) {
			t.Errorf("%s: %s", name, finding)
		}
	}
	if guarded < 21 {
		t.Fatalf("only %d guarded methods found, want the 21", guarded)
	}
}

// fixtureFinding is the finding each refused fixture is refused for.
var fixtureFinding = map[string]string{
	"alias.go":      "a context outside the ticket's Write",
	"async.go":      "a child started without StartCLI checked right before it",
	"closure.go":    "unknown provenance reaches write",
	"dot.go":        "a context outside the ticket's Write",
	"field.go":      "unknown provenance reaches write",
	"funcvalue.go":  "a context constructor taken as a value",
	"gap.go":        "a child started without StartCLI checked right before it",
	"helper.go":     "bg: a function returning a context outside the reviewed forms",
	"ignored.go":    "StartCLI's refusal is not checked",
	"later.go":      "unknown provenance reaches write",
	"param.go":      "unknown provenance reaches write",
	"pkgref.go":     "bg: a package-level context constructor reference",
	"pkgvar.go":     "root: a package-level context\n",
	"reassigned.go": "unknown provenance reaches write",
	"second.go":     "a context outside the ticket's Write",
	"tuple.go":      "unknown provenance reaches write",
	"unfenced.go":   "a context outside the ticket's Write",
	"unknown.go":    "unknown provenance reaches write",
	"unused.go":     "is never used by Write or StartCLI",
	"wrapped.go":    "unknown provenance reaches write",
}

// Each fixture is type-checked with the stub (testdata/guardscan/stub)
// that declares the ticket and the sinks, as package fixture.
func TestTheGuardScanRefusesEveryFixture(t *testing.T) {
	listed(t)
	dir := filepath.Join("testdata", "guardscan")
	stub, err := os.ReadFile(filepath.Join(dir, "stub", "stub.go"))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		names = append(names, e.Name())
		src, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		f := parseIn(t, filepath.Join(dir, e.Name()), src)
		tp := typecheck(t, "fixture", []*ast.File{f, parseIn(t, "stub.go", stub)})
		got := guardFindings(tp, f)
		if e.Name() == "clean.go" {
			if len(got) != 0 {
				t.Errorf("clean.go: %v", got)
			}
			continue
		}
		if want := fixtureFinding[e.Name()]; want == "" || !strings.Contains(strings.Join(got, "\n")+"\n", want) {
			t.Errorf("%s: want a finding %q, got %v", e.Name(), want, got)
		}
	}
	sort.Strings(names)
	want := []string{"clean.go"}
	for k := range fixtureFinding {
		want = append(want, k)
	}
	sort.Strings(want)
	if !slices.Equal(names, want) {
		t.Fatalf("fixtures %v, want %v", names, want)
	}
}

// Codex's F1 review probe and its two re-review probes (RR2), adopted:
// the real ScaleWorkload, mutated to keep its fenced Write and send the
// write on another context - a constructor taken as a value, an import
// alias's constructor, a reviewed helper's tuple result - fails the scan.
// Unmutated, it passes (TestEveryGuardedMethodFencesWhatItSends).
func TestTheGuardScanChecksTheForwardedContext(t *testing.T) {
	const old = "return doScaleWorkload(ctx, conn, kind, namespace, name, uid, replicas)"
	for name, mutate := range map[string]func(string) string{
		"constructor-value": func(s string) string {
			return strings.Replace(s, old, "bare := context.Background\n\t_ = ctx\n\treturn doScaleWorkload(bare(), conn, kind, namespace, name, uid, replicas)", 1)
		},
		"import-alias": func(s string) string {
			s = strings.Replace(s, "\t\"context\"\n", "\t\"context\"\n\tstdctx \"context\"\n", 1)
			return strings.Replace(s, old, "_ = ctx\n\treturn doScaleWorkload(stdctx.Background(), conn, kind, namespace, name, uid, replicas)", 1)
		},
		"reviewed-multi-result": func(s string) string {
			return strings.Replace(s, old, "logs := NewLogsService(nil, nil)\n\t_, lifetime, _ := logs.reserve(\"probe\", PlainLease(\"probe\", nil))\n\t_ = ctx\n\treturn doScaleWorkload(lifetime, conn, kind, namespace, name, uid, replicas)", 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			tp, f := thisPackage(t, "workloads_service.go", mutate)
			if got := guardFindings(tp, f); len(got) == 0 {
				t.Fatal("an unfenced write in ScaleWorkload passed the scan")
			}
		})
	}
}
