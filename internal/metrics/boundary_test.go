package metrics

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Settings S3b §3.3; Codex's review of S3b-2a, R1: only this package sends
// to Prometheus. Its transports are opaque outside it, and no other package
// writes the query API's paths, so a Querier written elsewhere never
// reaches one: the checked boundary beside the types.
func TestNoOtherPackageWritesThePrometheusQueryAPI(t *testing.T) {
	root := filepath.Join("..", "..")
	var scanned int
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				switch d.Name() {
				case "node_modules", "frontend", "build":
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
			scanned++
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					v = lit.Value
				}
				if promQueryPath(v) {
					t.Errorf("%s writes the Prometheus query API's path %q: only internal/metrics sends to Prometheus", path, v)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if scanned < 100 {
		t.Fatalf("scanned %d files: the walk did not reach the repository", scanned)
	}
}

// promQueryPath: s names Prometheus's query API (/api/v1/query or
// query_range), not Loki's (/loki/api/v1/...).
func promQueryPath(s string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], "api/v1/query")
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

// The scan's own controls.
func TestThePrometheusPathScanTellsTheAPIsApart(t *testing.T) {
	for s, want := range map[string]bool{
		"/api/v1/query":                        true,
		"/api/v1/query_range":                  true,
		"api/v1/query":                         true,
		"/prometheus/api/v1/query":             true,
		"/loki/api/v1/query_range":             false,
		"/loki/api/v1/labels":                  false,
		"/api/v2/alerts":                       false,
		"/loki/api/v1/query and /api/v1/query": true,
	} {
		if got := promQueryPath(s); got != want {
			t.Errorf("%q: %v", s, got)
		}
	}
}
