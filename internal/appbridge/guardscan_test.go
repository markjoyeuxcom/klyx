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

// Settings slice F §2: every guarded method fences what it sends. In each
// function that takes a ticket from guardFor:
//   - the ticket is used, by Write or StartCLI;
//   - every context.Background or context.TODO is made inside the ticket's
//     Write, or its line says "unfenced:" and why - so no context that
//     reaches a write can bypass the fence;
//   - every child start (a Start call, launchTerminal) follows a StartCLI
//     in the same block, a goroutine's included.

func guardFindings(f *ast.File, fset *gotoken.FileSet) []string {
	unfencedLines := map[int]bool{}
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			if strings.Contains(c.Text, "unfenced:") {
				unfencedLines[fset.Position(c.Pos()).Line] = true
			}
		}
	}
	var out []string
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		tickets := map[string]bool{}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if as, ok := n.(*ast.AssignStmt); ok && len(as.Rhs) == 1 {
				if call, ok := as.Rhs[0].(*ast.CallExpr); ok && isIdent(call.Fun, "guardFor") {
					if id, ok := as.Lhs[0].(*ast.Ident); ok {
						tickets[id.Name] = true
					}
				}
			}
			return true
		})
		if len(tickets) == 0 {
			continue
		}
		name := fd.Name.Name
		used := map[string]bool{}
		var stack []ast.Node
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			stack = append(stack, n)
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if x, ok := sel.X.(*ast.Ident); ok && tickets[x.Name] && (sel.Sel.Name == "Write" || sel.Sel.Name == "StartCLI") {
					used[x.Name] = true
				}
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == "context" && (sel.Sel.Name == "Background" || sel.Sel.Name == "TODO") {
					if !insideWrite(stack, tickets) && !unfencedLines[fset.Position(call.Pos()).Line] {
						out = append(out, name+": a context outside the ticket's Write")
					}
				}
				if sel.Sel.Name == "Start" && !isTicket(sel.X, tickets) && !startCLIBefore(stack, call, tickets) {
					out = append(out, name+": a child started without StartCLI before it")
				}
			}
			if isIdent(call.Fun, "launchTerminal") && !startCLIBefore(stack, call, tickets) {
				out = append(out, name+": a terminal launched without StartCLI before it")
			}
			return true
		})
		for tk := range tickets {
			if !used[tk] {
				out = append(out, name+": ticket "+tk+" is never used by Write or StartCLI")
			}
		}
	}
	return out
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

// startCLIBefore reports whether an earlier statement of the innermost block
// holding the call checks a ticket's StartCLI.
func startCLIBefore(stack []ast.Node, call ast.Node, tickets map[string]bool) bool {
	for i := len(stack) - 2; i >= 0; i-- {
		block, ok := stack[i].(*ast.BlockStmt)
		if !ok {
			continue
		}
		child := stack[i+1]
		for _, st := range block.List {
			if st == child {
				return false
			}
			found := false
			ast.Inspect(st, func(n ast.Node) bool {
				if c, ok := n.(*ast.CallExpr); ok {
					if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "StartCLI" && isTicket(sel.X, tickets) {
						found = true
					}
				}
				return !found
			})
			if found {
				return true
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
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "async.go,clean.go,second.go,unfenced.go,unused.go" {
		t.Fatalf("fixtures %v", names)
	}
}
