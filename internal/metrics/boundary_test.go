package metrics

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Settings S3b §3.3; Codex's review of S3b-2a, R1 and its re-review, RR2:
// only this package sends to Prometheus. Its transports are opaque outside
// it; no production file elsewhere makes a Transport of its own Querier
// (TransportOf is a test's); and none writes the query API's paths - a
// literal, a constant built from pieces, or a path joined or formatted
// from them - so a Querier written elsewhere never reaches one: the
// checked boundary beside the types.
func TestNoOtherPackageWritesThePrometheusQueryAPI(t *testing.T) {
	root := filepath.Join("..", "..")
	pkgs := map[string]map[string]*ast.File{}
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				switch d.Name() {
				case "node_modules", "frontend", "build", "testdata":
					return filepath.SkipDir
				}
				if rel, _ := filepath.Rel(root, path); rel == filepath.Join("internal", "metrics") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			dirKey := filepath.Dir(rel)
			if pkgs[dirKey] == nil {
				pkgs[dirKey] = map[string]*ast.File{}
			}
			pkgs[dirKey][filepath.ToSlash(rel)] = f
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	scanned := 0
	for _, files := range pkgs {
		scanned += len(files)
		for _, problem := range scanPackage(files) {
			t.Error(problem)
		}
	}
	if scanned < 100 {
		t.Fatalf("scanned %d files: the walk did not reach the repository", scanned)
	}
}

// allowedAPIText are the strings outside this package that name the
// Prometheus API and never send: the load's check that a metrics endpoint
// is the base URL. Each is matched exactly, in its file.
var allowedAPIText = map[string]bool{
	"internal/config/config.go\x00/api/v1":  true,
	"internal/config/sources.go\x00/api/v1": true,
	"internal/config/config.go\x00cluster %s: metrics.endpoint must be the Prometheus base URL without a trailing /api/v1": true,
	"internal/config/sources.go\x00%s.endpoint must be the Prometheus base URL without a trailing /api/v1":                 true,
}

// scanPackage is every problem in one package's files: a string naming the
// Prometheus API, as written or as its constants, joins and formats fold
// it, or a call making a Transport of a Querier.
func scanPackage(files map[string]*ast.File) []string {
	consts := map[string]string{}
	for range 4 { // constants built from constants, a few levels deep
		for _, f := range files {
			ast.Inspect(f, func(n ast.Node) bool {
				g, ok := n.(*ast.GenDecl)
				if !ok || g.Tok != token.CONST {
					return true
				}
				for _, spec := range g.Specs {
					vs := spec.(*ast.ValueSpec)
					for i, name := range vs.Names {
						if i < len(vs.Values) {
							if v, ok := foldString(vs.Values[i], consts); ok {
								consts[name.Name] = v
							}
						}
					}
				}
				return true
			})
		}
	}
	var problems []string
	for path, f := range files {
		seen := map[string]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "TransportOf" {
					if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "metrics" {
						problems = append(problems, path+" makes a metrics Transport of its own Querier: only a test does")
					}
				}
			}
			e, ok := n.(ast.Expr)
			if !ok {
				return true
			}
			v, ok := foldString(e, consts)
			if !ok || seen[v] || !promAPI(v) || allowedAPIText[path+"\x00"+v] {
				return true
			}
			seen[v] = true
			problems = append(problems, fmt.Sprintf("%s writes the Prometheus API's path in %q: only internal/metrics sends to Prometheus", path, v))
			return true
		})
	}
	sort.Strings(problems)
	return problems
}

// foldString is e's string value when it is one the source fixes: a
// literal, a constant, a sum of them, or path.Join, filepath.Join,
// strings.Join or fmt.Sprintf of them.
func foldString(e ast.Expr, consts map[string]string) (string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		v, err := strconv.Unquote(e.Value)
		return v, err == nil
	case *ast.Ident:
		v, ok := consts[e.Name]
		return v, ok
	case *ast.ParenExpr:
		return foldString(e.X, consts)
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		a, ok := foldString(e.X, consts)
		if !ok {
			return "", false
		}
		b, ok := foldString(e.Y, consts)
		return a + b, ok
	case *ast.CallExpr:
		sel, ok := e.Fun.(*ast.SelectorExpr)
		if !ok {
			return "", false
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return "", false
		}
		args := make([]string, 0, len(e.Args))
		for _, a := range e.Args {
			if lit, ok := a.(*ast.CompositeLit); ok && pkg.Name == "strings" && sel.Sel.Name == "Join" {
				var elts []string
				for _, el := range lit.Elts {
					v, ok := foldString(el, consts)
					if !ok {
						return "", false
					}
					elts = append(elts, v)
				}
				args = append(args, strings.Join(elts, "\x00"))
				continue
			}
			v, ok := foldString(a, consts)
			if !ok {
				return "", false
			}
			args = append(args, v)
		}
		switch {
		case (pkg.Name == "path" || pkg.Name == "filepath") && sel.Sel.Name == "Join":
			return pathpkg.Join(args...), true
		case pkg.Name == "strings" && sel.Sel.Name == "Join" && len(args) == 2:
			return strings.ReplaceAll(args[0], "\x00", args[1]), true
		case pkg.Name == "fmt" && sel.Sel.Name == "Sprintf" && len(args) > 0:
			vals := make([]any, len(args)-1)
			for i, a := range args[1:] {
				vals[i] = a
			}
			return fmt.Sprintf(args[0], vals...), true
		}
	}
	return "", false
}

// promAPI: s names the Prometheus API's version path (api/v1, its query
// endpoints among it), not Loki's (/loki/api/v1/...).
func promAPI(s string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], "api/v1")
		if j < 0 {
			return false
		}
		j += i
		if !strings.HasSuffix(s[:j], "loki/") {
			return true
		}
		i = j + 1
	}
}

// The scan's own fixtures: every ordinary way to write the path is found,
// Loki's and Alertmanager's are not (Codex's re-review of S3b-2a, RR2).
func TestTheScanFindsEveryWayToWriteThePath(t *testing.T) {
	parse := func(srcs ...string) map[string]*ast.File {
		files := map[string]*ast.File{}
		for i, src := range srcs {
			f, err := parser.ParseFile(token.NewFileSet(), fmt.Sprintf("f%d.go", i), src, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			files[fmt.Sprintf("internal/x/f%d.go", i)] = f
		}
		return files
	}
	for name, srcs := range map[string][]string{
		"a literal":                    {`package x; var p = "/api/v1/query"`},
		"a range literal":              {`package x; var p = "/prometheus/api/v1/query_range"`},
		"constant concatenation":       {`package x; const p = "/api/v1/" + "query"`},
		"pieces with no api/v1 in one": {`package x; const base = "/api"`, `package x; var p = base + "/" + "v" + "1/query"`},
		"a joined path":                {`package x; import "path"; var p = path.Join("/api", "v1", "query")`},
		"a strings.Join":               {`package x; import "strings"; var p = strings.Join([]string{"", "api", "v1", "query"}, "/")`},
		"a formatted path":             {`package x; import "fmt"; var p = fmt.Sprintf("/api/%s/query", "v1")`},
		"the API base alone":           {`package x; var p = "/api/v1"`},
		"a Transport of a Querier":     {`package x; import "m/metrics"; var t = metrics.TransportOf(nil)`},
	} {
		if len(scanPackage(parse(srcs...))) == 0 {
			t.Errorf("%s: not found", name)
		}
	}
	for name, src := range map[string]string{
		"Loki's query range":   `package x; var p = "/loki/api/v1/query_range"`,
		"Loki's labels":        `package x; const p = "/loki/api/v1/labels"`,
		"Alertmanager's API":   `package x; var p = "/api/v2/alerts"`,
		"a query parameter":    `package x; var p = map[string]string{"query": "x"}`,
		"another package's of": `package x; import "m/other"; var t = other.TransportOf(nil)`,
	} {
		if p := scanPackage(parse(src)); len(p) != 0 {
			t.Errorf("%s: %v", name, p)
		}
	}
}

// Codex's re-review of S3b-2a, RR1: no exported package variable feeds a
// renderer. Every exported variable of this package is a registered
// template (define) or an error; a list or map a renderer reads is the
// package's own.
func TestNoExportedVariableFeedsARenderer(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.VAR {
				continue
			}
			for _, spec := range g.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if !name.IsExported() {
						continue
					}
					if i >= len(vs.Values) || !templateOrError(vs.Values[i]) {
						t.Errorf("%s: the exported variable %s is neither a template nor an error: a caller could change what a renderer reads", path, name.Name)
					}
				}
			}
		}
	}
}

// templateOrError: e is define(...), errors.New(...), fmt.Errorf(...), or
// another package's error (pkg.ErrX).
func templateOrError(e ast.Expr) bool {
	if sel, ok := e.(*ast.SelectorExpr); ok {
		return strings.HasPrefix(sel.Sel.Name, "Err")
	}
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name == "define"
	case *ast.SelectorExpr:
		pkg, ok := fn.X.(*ast.Ident)
		return ok && (pkg.Name == "errors" && fn.Sel.Name == "New" || pkg.Name == "fmt" && fn.Sel.Name == "Errorf")
	}
	return false
}
