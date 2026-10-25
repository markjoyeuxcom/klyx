package appbridge

import (
	"go/ast"
	"go/parser"
	gotoken "go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Settings slice F §2: every guarded method fences what it sends. The scan
// accepts a closed set of guarded-shell forms and fails on anything else
// (F1 implementation review, finding F5). In each function that takes a
// ticket from guardFor:
//   - the ticket is used, by Write or StartCLI;
//   - every context.Background or context.TODO is called (never taken as a
//     function value) inside the ticket's Write, or its line says
//     "unfenced:" and why;
//   - every context a call receives has a known provenance: the ticket's
//     Write, a context.With* or readfence wrapper of a context with known
//     provenance, or a variable assigned only those - a variable declared
//     on an "unfenced:" line carries that line's reason. A field or method
//     named for a context, a context.Background taken as a value, or a
//     context-named variable of unknown origin is refused;
//   - every child start (a Start call, launchTerminal) is the statement
//     right after `if err := t.StartCLI(); err != nil { ... return }` in
//     the same block, a goroutine's included; a StartCLI in any other form
//     is refused.
// Across the package, a function returning a context (other than the
// reviewed ones below) and a package-level variable made from
// context.Background or context.TODO are refused unless marked "unfenced:".

type ctxStatus int

const (
	ctxNone     ctxStatus = iota // not a context, as far as the syntax says
	ctxFenced                    // the ticket's Write, or derived from it
	ctxMarked                    // made on an "unfenced:" line, or derived from one
	ctxUnfenced                  // anything else that is a context
)

func worse(a, b ctxStatus) ctxStatus {
	if a > b {
		return a
	}
	return b
}

// contextWrappers derive a context from their first argument.
var contextWrappers = map[string]map[string]bool{
	"context":   {"WithTimeout": true, "WithCancel": true, "WithDeadline": true, "WithValue": true, "WithoutCancel": true, "WithCancelCause": true, "WithTimeoutCause": true, "WithDeadlineCause": true},
	"readfence": {"With": true, "WithWriteTracker": true},
}

func pkgSel(e ast.Expr) (pkg, name string, ok bool) {
	sel, isSel := e.(*ast.SelectorExpr)
	if !isSel {
		return "", "", false
	}
	x, isID := sel.X.(*ast.Ident)
	if !isID {
		return "", "", false
	}
	return x.Name, sel.Sel.Name, true
}

func isConstructor(e ast.Expr) bool {
	pkg, name, ok := pkgSel(e)
	return ok && pkg == "context" && (name == "Background" || name == "TODO")
}

// reviewedContextFuncs are the package's functions returning a context,
// each reviewed: the ticket's Write, a derivation of its argument, and a
// read's own lifetime.
var reviewedContextFuncs = map[string]string{
	"Ticket.Write":        "the ticket's fence and a write tracker on its argument",
	"fenced":              "its argument with a cancellation the fence drives; a wrapper, never a new root",
	"LogsService.reserve": "a log stream's lifetime: a read, never a guarded write's",
}

// localWrappers are the package's own functions deriving a context from
// their first argument.
var localWrappers = map[string]bool{"fenced": true}

func isWrapper(e ast.Expr) bool {
	if id, ok := e.(*ast.Ident); ok {
		return localWrappers[id.Name]
	}
	pkg, name, ok := pkgSel(e)
	return ok && contextWrappers[pkg][name]
}

// contextNamed: a name that says it holds a context.
func contextNamed(name string) bool {
	l := strings.ToLower(name)
	return l == "ctx" || strings.HasSuffix(l, "ctx") || name == "Context" || l == "base"
}

type guardScan struct {
	fset     *gotoken.FileSet
	unfenced map[int]bool
	tickets  map[string]bool
	vars     map[string]ctxStatus
	// unsettled: provenance did not reach a fixpoint; a finding, never a pass
	unsettled bool
}

func (g *guardScan) marked(n ast.Node) bool { return g.unfenced[g.fset.Position(n.Pos()).Line] }

func (g *guardScan) isTicketWrite(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Write" && isTicket(sel.X, g.tickets)
}

// classify is an expression's provenance as a context.
func (g *guardScan) classify(e ast.Expr) ctxStatus {
	st := g.classifyRaw(e)
	if st == ctxUnfenced && g.marked(e) {
		return ctxMarked
	}
	return st
}

func (g *guardScan) classifyRaw(e ast.Expr) ctxStatus {
	switch e := e.(type) {
	case *ast.ParenExpr:
		return g.classify(e.X)
	case *ast.CallExpr:
		switch {
		case g.isTicketWrite(e):
			return ctxFenced
		case isConstructor(e.Fun):
			return ctxUnfenced
		case isWrapper(e.Fun):
			if len(e.Args) == 0 {
				return ctxUnfenced
			}
			if st := g.classify(e.Args[0]); st != ctxNone {
				return st
			}
			return ctxUnfenced // a wrapper of a context of unknown origin
		}
		if sel, ok := e.Fun.(*ast.SelectorExpr); ok && contextNamed(sel.Sel.Name) {
			return ctxUnfenced // r.Context(), s.baseCtx()
		}
		return ctxNone
	case *ast.Ident:
		if st, ok := g.vars[e.Name]; ok {
			return st
		}
		if contextNamed(e.Name) {
			return ctxUnfenced // a context-named variable of unknown origin
		}
		return ctxNone
	case *ast.SelectorExpr:
		if isConstructor(e) || contextNamed(e.Sel.Name) {
			return ctxUnfenced // context.Background as a value, s.ctx
		}
	}
	return ctxNone
}

// track records each variable's provenance, joined over every assignment
// it has, to a fixpoint: a variable any assignment makes a context is a
// context variable, and its other assignments count too - one the syntax
// cannot place (a field, a call) as unfenced. A parameter or a declared
// variable of type context.Context starts unfenced: its origin is unknown.
func (g *guardScan) track(fd *ast.FuncDecl) {
	rhs := map[string][]ast.Expr{}
	add := func(lhs []ast.Expr, vals []ast.Expr) {
		for i, l := range lhs {
			id, ok := l.(*ast.Ident)
			if !ok || id.Name == "_" {
				continue
			}
			switch {
			case len(vals) == len(lhs):
				rhs[id.Name] = append(rhs[id.Name], vals[i])
			case len(vals) == 1 && i == 0:
				rhs[id.Name] = append(rhs[id.Name], vals[0]) // ctx, cancel := context.WithTimeout(...)
			case len(vals) == 1:
				rhs[id.Name] = append(rhs[id.Name], nil) // a later result: not placed
			}
		}
	}
	isContextType := func(e ast.Expr) bool {
		pkg, name, ok := pkgSel(e)
		return ok && pkg == "context" && name == "Context"
	}
	seed := map[string]bool{}
	for _, fl := range fd.Type.Params.List {
		if isContextType(fl.Type) {
			for _, n := range fl.Names {
				seed[n.Name] = true
			}
		}
	}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			add(n.Lhs, n.Rhs)
		case *ast.ValueSpec:
			lhs := make([]ast.Expr, len(n.Names))
			for i, id := range n.Names {
				lhs[i] = id
				if n.Type != nil && isContextType(n.Type) {
					seed[id.Name] = true
				}
			}
			add(lhs, n.Values)
		case *ast.FuncLit:
			for _, fl := range n.Type.Params.List {
				if isContextType(fl.Type) {
					for _, id := range fl.Names {
						seed[id.Name] = true
					}
				}
			}
		}
		return true
	})
	for name := range seed {
		g.vars[name] = ctxUnfenced
	}
	names := make([]string, 0, len(rhs))
	for name := range rhs {
		names = append(names, name)
	}
	sort.Strings(names) // a fixed order, so the scan is deterministic
	changed := true
	for round := 0; changed && round < 4*len(names)+4; round++ {
		changed = false
		for _, name := range names {
			vals := rhs[name]
			st, isCtx := ctxNone, seed[name]
			unplaced := false
			for _, v := range vals {
				if v == nil {
					unplaced = true
					continue
				}
				c := g.classify(v)
				if c == ctxNone {
					unplaced = true
					continue
				}
				isCtx = true
				st = worse(st, c)
			}
			if !isCtx {
				continue
			}
			if unplaced || seed[name] {
				st = ctxUnfenced
			}
			if prev, ok := g.vars[name]; !ok || prev != st {
				g.vars[name] = st
				changed = true
			}
		}
	}
	g.unsettled = changed
}

func guardFindings(f *ast.File, fset *gotoken.FileSet) []string {
	unfencedLines := map[int]bool{}
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			if strings.Contains(c.Text, "unfenced:") {
				unfencedLines[fset.Position(c.Pos()).Line] = true
			}
		}
	}
	out := contextSourceFindings(f, fset, unfencedLines)
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		g := &guardScan{fset: fset, unfenced: unfencedLines, tickets: map[string]bool{}, vars: map[string]ctxStatus{}}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if as, ok := n.(*ast.AssignStmt); ok && len(as.Rhs) == 1 {
				if call, ok := as.Rhs[0].(*ast.CallExpr); ok && isIdent(call.Fun, "guardFor") {
					if id, ok := as.Lhs[0].(*ast.Ident); ok {
						g.tickets[id.Name] = true
					}
				}
			}
			return true
		})
		if len(g.tickets) == 0 {
			continue
		}
		g.track(fd)
		name := fd.Name.Name
		if g.unsettled {
			out = append(out, name+": context provenance did not settle")
		}
		used := map[string]bool{}
		var stack []ast.Node
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			stack = append(stack, n)
			if isConstructor(asExpr(n)) && !g.marked(n) {
				if call, ok := parentOf(stack).(*ast.CallExpr); !ok || call.Fun != n {
					out = append(out, name+": a context constructor taken as a value")
				}
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if isTicket(sel.X, g.tickets) && (sel.Sel.Name == "Write" || sel.Sel.Name == "StartCLI") {
					used[sel.X.(*ast.Ident).Name] = true
				}
				if isTicket(sel.X, g.tickets) && sel.Sel.Name == "StartCLI" && !canonicalCheck(stack, g.tickets) {
					out = append(out, name+": StartCLI's refusal is not checked by `if err := t.StartCLI(); err != nil { return }`")
				}
				if isConstructor(sel) && !insideWrite(stack, g.tickets) && !g.marked(call) {
					out = append(out, name+": a context outside the ticket's Write")
				}
				if sel.Sel.Name == "Start" && !isTicket(sel.X, g.tickets) && !checkedRightBefore(stack, g.tickets) {
					out = append(out, name+": a child started without StartCLI checked right before it")
				}
			}
			if isIdent(call.Fun, "launchTerminal") && !checkedRightBefore(stack, g.tickets) {
				out = append(out, name+": a terminal launched without StartCLI checked right before it")
			}
			// every argument a call receives: a context of known provenance;
			// a wrapper's first argument and Write's are its own
			if g.isTicketWrite(call) {
				return true
			}
			for i, a := range call.Args {
				if i == 0 && isWrapper(call.Fun) {
					continue
				}
				if g.classify(a) == ctxUnfenced {
					out = append(out, name+": a context of unfenced or unknown provenance reaches "+calleeName(call))
				}
			}
			return true
		})
		for tk := range g.tickets {
			if !used[tk] {
				out = append(out, name+": ticket "+tk+" is never used by Write or StartCLI")
			}
		}
	}
	return out
}

// contextSourceFindings refuses, across a file, a function returning a
// context (other than the ticket's Write) and a package-level variable made
// from context.Background or context.TODO, unless marked "unfenced:".
func contextSourceFindings(f *ast.File, fset *gotoken.FileSet, unfenced map[int]bool) []string {
	var out []string
	marked := func(n ast.Node) bool { return unfenced[fset.Position(n.Pos()).Line] }
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Type.Results == nil || marked(d) {
				continue
			}
			if _, ok := reviewedContextFuncs[funcKey(d)]; ok {
				continue
			}
			for _, r := range d.Type.Results.List {
				if pkg, name, ok := pkgSel(r.Type); ok && pkg == "context" && name == "Context" {
					out = append(out, d.Name.Name+": a function returning a context outside the reviewed forms")
				}
			}
		case *ast.GenDecl:
			if d.Tok != gotoken.VAR {
				continue
			}
			for _, sp := range d.Specs {
				vs := sp.(*ast.ValueSpec)
				if marked(vs) {
					continue
				}
				ast.Inspect(vs, func(n ast.Node) bool {
					if isConstructor(asExpr(n)) {
						out = append(out, vs.Names[0].Name+": a package-level context from context.Background or TODO")
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

func asExpr(n ast.Node) ast.Expr {
	e, _ := n.(ast.Expr)
	return e
}

func parentOf(stack []ast.Node) ast.Node {
	if len(stack) < 2 {
		return nil
	}
	return stack[len(stack)-2]
}

func calleeName(call *ast.CallExpr) string {
	switch f := call.Fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return "a call"
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

func isTicket(e ast.Expr, tickets map[string]bool) bool {
	id, ok := e.(*ast.Ident)
	return ok && tickets[id.Name]
}

// insideWrite reports whether the node at the top of the stack is in the
// arguments of a ticket's Write.
func insideWrite(stack []ast.Node, tickets map[string]bool) bool {
	for i := len(stack) - 2; i >= 0; i-- {
		if call, ok := stack[i].(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Write" && isTicket(sel.X, tickets) {
				return true
			}
		}
	}
	return false
}

// isCanonicalCheck: `if err := t.StartCLI(); err != nil { ...; return ... }`.
func isCanonicalCheck(st ast.Stmt, tickets map[string]bool) bool {
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
	call, ok := init.Rhs[0].(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "StartCLI" || !isTicket(sel.X, tickets) {
		return false
	}
	cond, ok := is.Cond.(*ast.BinaryExpr)
	if !ok || cond.Op != gotoken.NEQ || !isIdent(cond.X, errID.Name) || !isIdent(cond.Y, "nil") {
		return false
	}
	n := len(is.Body.List)
	if n == 0 {
		return false
	}
	_, returns := is.Body.List[n-1].(*ast.ReturnStmt)
	return returns
}

// canonicalCheck reports whether the StartCLI call at the top of the stack
// is the Init of a canonical check: call, its assignment, the if.
func canonicalCheck(stack []ast.Node, tickets map[string]bool) bool {
	n := len(stack)
	if n < 3 {
		return false
	}
	as, ok := stack[n-2].(*ast.AssignStmt)
	if !ok {
		return false
	}
	is, ok := stack[n-3].(*ast.IfStmt)
	return ok && is.Init == as && isCanonicalCheck(is, tickets)
}

// checkedRightBefore reports whether the statement of the innermost block
// holding the call follows a canonical StartCLI check directly.
func checkedRightBefore(stack []ast.Node, tickets map[string]bool) bool {
	for i := len(stack) - 2; i >= 0; i-- {
		block, ok := stack[i].(*ast.BlockStmt)
		if !ok {
			continue
		}
		child := stack[i+1]
		for j, st := range block.List {
			if st == child {
				return j > 0 && isCanonicalCheck(block.List[j-1], tickets)
			}
		}
		return false
	}
	return false
}

func parseGo(t *testing.T, path string) (*ast.File, *gotoken.FileSet) {
	t.Helper()
	fset := gotoken.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	return f, fset
}

func TestEveryGuardedMethodFencesWhatItSends(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	guarded := 0
	for _, p := range files {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		src, _ := os.ReadFile(p)
		guarded += strings.Count(string(src), "guardFor(cluster)")
		f, fset := parseGo(t, p)
		for _, finding := range guardFindings(f, fset) {
			t.Errorf("%s: %s", p, finding)
		}
	}
	if guarded < 21 {
		t.Fatalf("only %d guarded methods found, want the 21", guarded)
	}
}

// fixtureFinding is the finding each refused fixture is refused for.
var fixtureFinding = map[string]string{
	"async.go":      "a child started without StartCLI checked right before it",
	"field.go":      "unknown provenance reaches write",
	"funcvalue.go":  "a context constructor taken as a value",
	"gap.go":        "a child started without StartCLI checked right before it",
	"helper.go":     "bg: a function returning a context outside the reviewed forms",
	"ignored.go":    "StartCLI's refusal is not checked",
	"later.go":      "unknown provenance reaches write",
	"param.go":      "unknown provenance reaches write",
	"pkgvar.go":     "root: a package-level context",
	"reassigned.go": "unknown provenance reaches write",
	"second.go":     "a context outside the ticket's Write",
	"unfenced.go":   "a context outside the ticket's Write",
	"unknown.go":    "unknown provenance reaches write",
	"unused.go":     "is never used by Write or StartCLI",
	"wrapped.go":    "unknown provenance reaches write",
}

func TestTheGuardScanRefusesEveryFixture(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("testdata", "guardscan"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
		f, fset := parseGo(t, filepath.Join("testdata", "guardscan", e.Name()))
		got := guardFindings(f, fset)
		if e.Name() == "clean.go" {
			if len(got) != 0 {
				t.Errorf("clean.go: %v", got)
			}
			continue
		}
		if len(got) == 0 {
			t.Errorf("%s passed the scan", e.Name())
		}
		if want := fixtureFinding[e.Name()]; !strings.Contains(strings.Join(got, "\n"), want) {
			t.Errorf("%s: want a finding %q, got %v", e.Name(), want, got)
		}
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "async.go,clean.go,field.go,funcvalue.go,gap.go,helper.go,ignored.go,later.go,param.go,pkgvar.go,reassigned.go,second.go,unfenced.go,unknown.go,unused.go,wrapped.go" {
		t.Fatalf("fixtures %v", names)
	}
}

// Codex's F1 review probe, adopted: the real ScaleWorkload, mutated in
// memory to keep its Write and send the write on a constructor taken as a
// value, fails the scan.
func TestTheGuardScanChecksTheForwardedContext(t *testing.T) {
	src, err := os.ReadFile("workloads_service.go")
	if err != nil {
		t.Fatal(err)
	}
	old := "return doScaleWorkload(ctx, conn, kind, namespace, name, uid, replicas)"
	mutated := strings.Replace(string(src), old, "bare := context.Background\n\t_ = ctx\n\treturn doScaleWorkload(bare(), conn, kind, namespace, name, uid, replicas)", 1)
	if mutated == string(src) {
		t.Fatal("the mutation did not land")
	}
	fset := gotoken.NewFileSet()
	f, err := parser.ParseFile(fset, "workloads_service.go", mutated, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	if got := guardFindings(f, fset); len(got) == 0 {
		t.Fatal("an unfenced write in ScaleWorkload passed the scan")
	}
}
