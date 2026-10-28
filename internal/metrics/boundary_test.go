package metrics

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// Settings S3b §3.3; Codex's review of S3b-2a, R1 and its re-reviews, RR2
// and R2-1: only this package sends to Prometheus. Its transports are
// opaque outside it; no production file elsewhere refers to TransportOf
// (a test's); and none writes the query API's paths. The check is typed:
// every package is type-checked, so a name is the object it refers to,
// however its import is spelled, and a constant is the value it is bound
// to where it is used; a call to path.Join, filepath.Join, strings.Join,
// fmt.Sprintf or url.JoinPath is folded when its arguments are constants.
// A path built at run time from data is outside what it claims.

const metricsPath = "github.com/moomora/klyx/internal/metrics"

// listed is one package as go list describes it.
type listed struct {
	ImportPath string
	Dir        string
	GoFiles    []string
	CgoFiles   []string
	Export     string
}

var repoOnce = sync.OnceValues(func() (map[string]*listed, error) {
	// the fixtures' standard packages are listed beside the repository's
	cmd := exec.Command("go", "list", "-e", "-export", "-deps", "-json", "./internal/...", "./cmd/...", "fmt", "path", "path/filepath", "strings", "net/url")
	cmd.Dir = filepath.Join("..", "..")
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("go list: %v\n%s", err, stderr.String())
	}
	all := map[string]*listed{}
	for dec := json.NewDecoder(&out); dec.More(); {
		var p listed
		if err := dec.Decode(&p); err != nil {
			return nil, err
		}
		all[p.ImportPath] = &p
	}
	return all, nil
})

// exportImporter imports a package from the export data go list built.
func exportImporter(fset *token.FileSet, all map[string]*listed) types.Importer {
	return importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		p := all[path]
		if p == nil || p.Export == "" {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(p.Export)
	})
}

func TestNoOtherPackageWritesThePrometheusQueryAPI(t *testing.T) {
	// go list reads the tree in its own process, which go test's cache does
	// not see: walking every directory here makes a cached pass depend on
	// their listings, so a file added anywhere runs the scan again
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join("..", "..", dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && (d.Name() == "node_modules" || d.Name() == "frontend") {
				return filepath.SkipDir
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	all, err := repoOnce()
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for path := range all {
		// the platform build directories hold other platforms' entry points
		// only; CI vets and tests the same set
		if strings.HasPrefix(path, "github.com/moomora/klyx/") && path != metricsPath && !strings.Contains(path, "/cmd/klyx/build/") {
			names = append(names, path)
		}
	}
	sort.Strings(names)
	files := 0
	for _, path := range names {
		p := all[path]
		fset := token.NewFileSet()
		var parsed []*ast.File
		for _, name := range append(append([]string{}, p.GoFiles...), p.CgoFiles...) {
			f, err := parser.ParseFile(fset, filepath.Join(p.Dir, name), nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			parsed = append(parsed, f)
		}
		if len(parsed) == 0 {
			continue
		}
		files += len(parsed)
		info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Uses: map[*ast.Ident]types.Object{}}
		var typeErrs []string
		conf := types.Config{Importer: exportImporter(fset, all), FakeImportC: true, Error: func(err error) { typeErrs = append(typeErrs, err.Error()) }}
		_, _ = conf.Check(path, fset, parsed, info)
		// a package that does not type-check would be read half blind
		if len(typeErrs) > 0 {
			t.Fatalf("%s does not type-check, so the scan cannot read it: %s", path, typeErrs[0])
		}
		rel := func(file string) string {
			r, err := filepath.Rel(root, file)
			if err != nil {
				return file
			}
			return filepath.ToSlash(r)
		}
		for _, problem := range scanTyped(fset, parsed, info, rel) {
			t.Error(problem)
		}
	}
	if files < 100 {
		t.Fatalf("scanned %d files: the listing did not reach the repository", files)
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

// scanTyped is every problem in one type-checked package: a reference to
// TransportOf, or an expression whose value the source fixes naming the
// Prometheus API.
func scanTyped(fset *token.FileSet, files []*ast.File, info *types.Info, rel func(string) string) []string {
	var problems []string
	seen := map[string]bool{}
	add := func(pos token.Pos, msg string) {
		at := fset.Position(pos)
		line := fmt.Sprintf("%s:%d: %s", rel(at.Filename), at.Line, msg)
		if !seen[line] {
			seen[line] = true
			problems = append(problems, line)
		}
	}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				if fn, ok := info.Uses[id].(*types.Func); ok && fn.Pkg() != nil && fn.Pkg().Path() == metricsPath && fn.Name() == "TransportOf" {
					add(id.Pos(), "refers to metrics.TransportOf: only a test makes a Transport of its own Querier")
				}
			}
			e, ok := n.(ast.Expr)
			if !ok {
				return true
			}
			v, ok := foldTyped(info, e)
			if !ok || !promAPI(v) || allowedAPIText[rel(fset.Position(e.Pos()).Filename)+"\x00"+v] {
				return true
			}
			add(e.Pos(), fmt.Sprintf("writes the Prometheus API's path in %q: only internal/metrics sends to Prometheus", v))
			return true
		})
	}
	sort.Strings(problems)
	return problems
}

// foldTyped is e's string value when the source fixes it: a constant under
// its binding, or one of the joining and formatting functions, identified
// as objects, over such values.
func foldTyped(info *types.Info, e ast.Expr) (string, bool) {
	if tv, ok := info.Types[e]; ok && tv.Value != nil {
		if tv.Value.Kind() != constant.String {
			return "", false
		}
		return constant.StringVal(tv.Value), true
	}
	call, ok := ast.Unparen(e).(*ast.CallExpr)
	if !ok {
		return "", false
	}
	fn := callee(info, call)
	if fn == nil {
		return "", false
	}
	switch fn.FullName() {
	case "path.Join", "path/filepath.Join":
		args, ok := foldAll(info, call.Args)
		if !ok {
			return "", false
		}
		return pathpkg.Join(args...), true
	case "net/url.JoinPath":
		args, ok := foldAll(info, call.Args)
		if !ok || len(args) == 0 {
			return "", false
		}
		u, err := url.Parse(args[0])
		if err != nil {
			return "", false
		}
		return u.JoinPath(args[1:]...).String(), true
	case "strings.Join":
		if len(call.Args) != 2 {
			return "", false
		}
		lit, ok := ast.Unparen(call.Args[0]).(*ast.CompositeLit)
		if !ok {
			return "", false
		}
		elems, ok := foldAll(info, lit.Elts)
		if !ok {
			return "", false
		}
		sep, ok := foldTyped(info, call.Args[1])
		if !ok {
			return "", false
		}
		return strings.Join(elems, sep), true
	case "fmt.Sprintf":
		if len(call.Args) == 0 {
			return "", false
		}
		format, ok := foldTyped(info, call.Args[0])
		if !ok {
			return "", false
		}
		vals := make([]any, 0, len(call.Args)-1)
		for _, a := range call.Args[1:] {
			v, ok := constantOf(info, a)
			if !ok {
				return "", false
			}
			vals = append(vals, v)
		}
		return fmt.Sprintf(format, vals...), true
	}
	return "", false
}

// callee is the function a call names, through any import spelling.
func callee(info *types.Info, call *ast.CallExpr) *types.Func {
	var id *ast.Ident
	switch f := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		id = f
	case *ast.SelectorExpr:
		id = f.Sel
	}
	if id == nil {
		return nil
	}
	fn, _ := info.Uses[id].(*types.Func)
	return fn
}

func foldAll(info *types.Info, es []ast.Expr) ([]string, bool) {
	out := make([]string, 0, len(es))
	for _, e := range es {
		v, ok := foldTyped(info, e)
		if !ok {
			return nil, false
		}
		out = append(out, v)
	}
	return out, true
}

// constantOf is a format argument's value: any constant's, or a folded
// string.
func constantOf(info *types.Info, e ast.Expr) (any, bool) {
	if tv, ok := info.Types[e]; ok && tv.Value != nil {
		switch v := tv.Value; v.Kind() {
		case constant.String:
			return constant.StringVal(v), true
		case constant.Int:
			if i, exact := constant.Int64Val(v); exact {
				return i, true
			}
		case constant.Float:
			f, _ := constant.Float64Val(v)
			return f, true
		case constant.Bool:
			return constant.BoolVal(v), true
		}
		return tv.Value.ExactString(), true
	}
	return foldTyped(info, e)
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

// fixtureScan type-checks one fixture package beside stubs of this package
// and of another with its own TransportOf, and scans it.
func fixtureScan(t *testing.T, srcs ...string) []string {
	t.Helper()
	all, err := repoOnce()
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	gc := exportImporter(fset, all)
	stub := func(path, src string) *types.Package {
		f, err := parser.ParseFile(fset, path+".go", src, 0)
		if err != nil {
			t.Fatal(err)
		}
		p, err := (&types.Config{Importer: gc}).Check(path, fset, []*ast.File{f}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	stubs := map[string]*types.Package{
		metricsPath:         stub(metricsPath, "package metrics\ntype Querier interface{ InstantQuery() }\ntype Transport struct{}\nfunc TransportOf(q Querier) Transport { return Transport{} }\n"),
		"example.com/other": stub("example.com/other", "package other\nfunc TransportOf(q any) int { return 0 }\n"),
	}
	var files []*ast.File
	for i, src := range srcs {
		f, err := parser.ParseFile(fset, fmt.Sprintf("internal/x/f%d.go", i), src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Uses: map[*ast.Ident]types.Object{}}
	conf := types.Config{Importer: importerFunc(func(path string) (*types.Package, error) {
		if p := stubs[path]; p != nil {
			return p, nil
		}
		return gc.Import(path)
	})}
	if _, err := conf.Check("example.com/x", fset, files, info); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return scanTyped(fset, files, info, func(s string) string { return s })
}

type importerFunc func(path string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }

// The scan's own fixtures: every ordinary way to write the path or to
// reach TransportOf is found, however its import is spelled; Loki's,
// Alertmanager's, a query parameter and another package's TransportOf are
// not (Codex's re-reviews of S3b-2a, RR2 and R2-1).
func TestTheScanFindsEveryWayToWriteThePath(t *testing.T) {
	m := `"github.com/moomora/klyx/internal/metrics"`
	for name, srcs := range map[string][]string{
		"a literal":                    {`package x; var p = "/api/v1/query"`},
		"a range literal":              {`package x; var p = "/prometheus/api/v1/query_range"`},
		"constant concatenation":       {`package x; const p = "/api/v1/" + "query"`},
		"pieces with no api/v1 in one": {`package x; const base = "/api"`, `package x; var p = base + "/" + "v" + "1/query"`},
		"a joined path":                {`package x; import "path"; var p = path.Join("/api", "v1", "query")`},
		"an aliased path.Join":         {`package x; import pp "path"; var p = pp.Join("/api", "v1", "query")`},
		"a dot-imported path.Join":     {`package x; import . "path"; var p = Join("/api", "v1", "query")`},
		"a filepath.Join":              {`package x; import "path/filepath"; var p = filepath.Join("/api", "v1", "query")`},
		"a strings.Join":               {`package x; import "strings"; var p = strings.Join([]string{"", "api", "v1", "query"}, "/")`},
		"an aliased strings.Join":      {`package x; import ss "strings"; var p = ss.Join([]string{"", "api", "v1", "query"}, "/")`},
		"a url.JoinPath":               {`package x; import "net/url"; var p, _ = url.JoinPath("http://h/api", "v1", "query")`},
		"a formatted path":             {`package x; import "fmt"; var p = fmt.Sprintf("/api/%s/query", "v1")`},
		"a numeric format argument":    {`package x; import "fmt"; var p = fmt.Sprintf("/api/v%d/query", 1)`},
		"a nested fold":                {`package x; import ("fmt"; "path"); var p = path.Join(fmt.Sprintf("/api/v%d", 1), "query")`},
		"lexically distinct constants": {`package x; import "path"; func a() string { const version = "v2"; return path.Join("/api", version, "x") }; func b() string { const version = "v1"; return path.Join("/api", version, "query") }`},
		"the API base alone":           {`package x; var p = "/api/v1"`},
		"a direct TransportOf":         {`package x; import ` + m + `; var t = metrics.TransportOf(nil)`},
		"an aliased TransportOf":       {`package x; import mm ` + m + `; var t = mm.TransportOf(nil)`},
		"TransportOf as a value":       {`package x; import ` + m + `; var wrap = metrics.TransportOf`},
		"a dot-imported TransportOf":   {`package x; import . ` + m + `; var t = TransportOf(nil)`},
	} {
		if len(fixtureScan(t, srcs...)) == 0 {
			t.Errorf("%s: not found", name)
		}
	}
	for name, src := range map[string]string{
		"Loki's query range":       `package x; var p = "/loki/api/v1/query_range"`,
		"Loki's labels":            `package x; const p = "/loki/api/v1/labels"`,
		"Alertmanager's API":       `package x; var p = "/api/v2/alerts"`,
		"a query parameter":        `package x; var p = map[string]string{"query": "x"}`,
		"another package's":        `package x; import "example.com/other"; var t = other.TransportOf(nil)`,
		"a join of other paths":    `package x; import "path"; var p = path.Join("/api", "v2", "alerts")`,
		"a constant of another fn": `package x; import "path"; func a() string { const version = "v1"; return version }; func b() string { const version = "v2"; return path.Join("/api", version, "x") }`,
	} {
		if p := fixtureScan(t, src); len(p) != 0 {
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
